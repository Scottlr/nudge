package logging

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

const (
	logSchemaVersion    uint32 = 1
	defaultLogBytes            = 4 * 1024 * 1024
	defaultLogFiles            = 4
	defaultLogRetention        = 7 * 24 * time.Hour
	maxLogFileBytes            = 64 * 1024 * 1024
	maxLogFileCount            = 1024
	maxLogRetention            = 30 * 24 * time.Hour
	maxOwnerMarkerBytes        = 32 * 1024
)

var (
	errLogDisabled = errors.New("operational log sink disabled")
	errLogClosed   = errors.New("operational log sink closed")
)

// Config bounds one owner-marked process log identity.
type Config struct {
	Root         string
	ProcessID    string
	RepositoryID domain.RepositoryID
	MaxBytes     app.ByteSize
	MaxFiles     app.Count
	Retention    time.Duration
	Now          func() time.Time
	Level        slog.Level
	Capacity     *CapacityBinding
}

// CapacityBinding connects one writer to the existing T067 reservation and
// ledger contracts. The writer never scans or reconciles other owners; T079
// remains responsible for crash recovery from these durable identities.
type CapacityBinding struct {
	Ledger       app.OwnedStorageLedger
	Reservations app.CapacityReservationPort
	Reservation  app.CapacityReservation
	Plan         app.CapacityPlan
	Policy       app.ResourcePolicy
	VolumeID     string
}

// DefaultConfig returns the bounded default sink policy for one log root.
func DefaultConfig(root string) Config {
	return Config{Root: root, MaxBytes: defaultLogBytes, MaxFiles: defaultLogFiles, Retention: defaultLogRetention, Level: slog.LevelInfo}
}

// ParseLevel maps the closed configuration vocabulary to slog levels. Unknown
// values remain conservative at info; config validation owns user-facing
// rejection before this adapter is composed.
func ParseLevel(value string) slog.Level {
	switch value {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Writer is a protected owner-marked JSONL file writer. It rotates only its
// own files and exposes no path or underlying file descriptor to callers.
type Writer struct {
	mu           sync.Mutex
	root         string
	ownerRoot    string
	processID    string
	activeName   string
	markerName   string
	maxBytes     uint64
	maxFiles     uint64
	retention    time.Duration
	now          func() time.Time
	file         *os.File
	ownerLock    *filelock.Lock
	bytes        uint64
	generation   uint64
	files        []string
	closed       bool
	health       *healthState
	capacity     *CapacityBinding
	repositoryID domain.RepositoryID
}

// NewWriter creates one private process log identity. Creation failure is
// returned to the caller so a logger can expose a redacted sink health state.
func NewWriter(ctx context.Context, config Config, health *healthState) (*Writer, error) {
	if ctx == nil || config.Root == "" || !filepath.IsAbs(config.Root) || filepath.Clean(config.Root) != config.Root || config.MaxBytes == 0 || config.MaxFiles == 0 || config.Retention < 0 {
		return nil, errLogDisabled
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RepositoryID != "" && !validRepositoryID(config.RepositoryID) {
		return nil, errLogDisabled
	}
	processID := config.ProcessID
	if processID == "" {
		var raw [12]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		processID = hex.EncodeToString(raw[:])
	}
	if !validOwnerToken(processID) {
		return nil, errLogDisabled
	}
	if config.MaxBytes > app.ByteSize(maxLogFileBytes) || config.MaxFiles > app.Count(maxLogFileCount) || config.Retention > maxLogRetention {
		return nil, errLogDisabled
	}
	scopeRoot := config.Root
	if config.RepositoryID != "" {
		scopeRoot = filepath.Join(config.Root, "repositories", repositoryScopeID(config.RepositoryID))
	}
	if err := paths.EnsurePrivateDir(scopeRoot); err != nil {
		return nil, err
	}
	ownerRoot := filepath.Join(scopeRoot, "owners")
	if err := paths.EnsurePrivateDir(ownerRoot); err != nil {
		return nil, err
	}
	writer := &Writer{
		root: scopeRoot, ownerRoot: ownerRoot, processID: processID, repositoryID: config.RepositoryID,
		markerName: processID + ".json", maxBytes: uint64(config.MaxBytes), maxFiles: uint64(config.MaxFiles),
		retention: config.Retention, now: config.Now, health: health,
		activeName: logName(processID, 0), files: []string{logName(processID, 0)},
		capacity: config.Capacity,
	}
	if err := writer.validateCapacity(); err != nil {
		return nil, err
	}
	ownerLockPath := filepath.Join(ownerRoot, processID+".lock")
	ownerLock, err := filelock.Acquire(ctx, ownerLockPath)
	if err != nil {
		return nil, err
	}
	writer.ownerLock = ownerLock
	if err := writer.recordCapacity(ctx); err != nil {
		_ = ownerLock.Close()
		return nil, err
	}
	if err := writer.createMarker(false); err != nil {
		_ = writer.removeMarker()
		_ = writer.releaseCapacity(context.Background())
		_ = ownerLock.Close()
		return nil, err
	}
	file, err := paths.OpenProtectedFile(config.Root, writer.activeName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = writer.removeMarker()
		_ = writer.releaseCapacity(context.Background())
		_ = ownerLock.Close()
		return nil, err
	}
	writer.file = file
	if err := writer.reapClosed(); err != nil {
		writer.healthFailure(app.LogFailureRetention)
	}
	return writer, nil
}

func validRepositoryID(value domain.RepositoryID) bool {
	return value != "" && strings.TrimSpace(string(value)) == string(value) && !strings.ContainsAny(string(value), "\r\n\x00")
}

func repositoryScopeID(repositoryID domain.RepositoryID) string {
	digest := sha256.Sum256([]byte(string(repositoryID)))
	return hex.EncodeToString(digest[:])
}

// Write appends one already-encoded slog record or disables the sink when
// the record cannot be admitted within the configured per-file budget.
func (w *Writer) Write(data []byte) (int, error) {
	if w == nil {
		return 0, errLogDisabled
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errLogClosed
	}
	if w.health != nil && w.health.snapshot().Disabled {
		return 0, errLogDisabled
	}
	if w.file == nil {
		w.healthFailure(app.LogFailureOpen)
		return 0, errLogDisabled
	}
	if len(data) == 0 || uint64(len(data)) > w.maxBytes {
		w.healthFailure(app.LogFailureCapacity)
		return 0, errLogDisabled
	}
	if w.bytes > w.maxBytes-uint64(len(data)) {
		if err := w.rotateLocked(); err != nil {
			w.healthFailure(app.LogFailureRotation)
			return 0, errLogDisabled
		}
	}
	n, err := w.file.Write(data)
	if err != nil || n != len(data) {
		w.healthFailure(app.LogFailureWrite)
		return n, errLogDisabled
	}
	w.bytes += uint64(n)
	return n, nil
}

// Close marks the owner identity closed, flushes the active file, and releases
// its lock. It is safe to call repeatedly.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var first error
	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			first = err
		}
		if err := w.file.Close(); err != nil && first == nil {
			first = err
		}
		w.file = nil
	}
	if err := w.updateMarker(true); err != nil && first == nil {
		first = err
	}
	if first == nil {
		if err := w.settleCapacity(); err != nil {
			w.healthFailure(app.LogFailureCapacity)
			first = err
		} else if err := w.releaseCapacity(context.Background()); err != nil {
			w.healthFailure(app.LogFailureCapacity)
			first = err
		}
	}
	if w.ownerLock != nil {
		if err := w.ownerLock.Close(); err != nil && first == nil {
			first = err
		}
		w.ownerLock = nil
	}
	return first
}

// ProcessID returns the opaque process identity, not a filesystem path.
func (w *Writer) ProcessID() string {
	if w == nil {
		return ""
	}
	return w.processID
}

func (w *Writer) healthFailure(code app.LogFailureCode) {
	if w != nil && w.health != nil {
		w.health.fail(code)
	}
}

func (w *Writer) createMarker(closed bool) error {
	return w.writeMarker(closed, true)
}

func (w *Writer) validateCapacity() error {
	if w.capacity == nil {
		return nil
	}
	if w.capacity.Ledger == nil || w.capacity.Reservations == nil || w.capacity.VolumeID == "" ||
		w.capacity.Reservation.Marker() == "" || w.capacity.Plan.OperationID == "" ||
		w.capacity.Reservation.OperationID() != w.capacity.Plan.OperationID ||
		w.capacity.Reservation.PolicyVersion() != w.capacity.Plan.PolicyVersion ||
		w.capacity.Plan.PolicyVersion != w.capacity.Policy.Version {
		return app.ErrInvalidCapacityPlan
	}
	for _, peak := range w.capacity.Plan.VolumePeaks {
		if peak.ID == w.capacity.VolumeID {
			return nil
		}
	}
	return app.ErrInvalidCapacityPlan
}

func (w *Writer) recordCapacity(ctx context.Context) error {
	if w.capacity == nil {
		return nil
	}
	record := app.CapacityReservationRecord{
		Reservation:    w.capacity.Reservation,
		OwnerKind:      app.OwnerLog,
		OwnerID:        w.processID,
		Plan:           w.capacity.Plan,
		IdempotencyKey: fmt.Sprintf("log:%s:reserve", w.processID),
		CreatedAt:      w.now().UTC(),
	}
	if err := record.Validate(); err != nil {
		return err
	}
	return w.capacity.Ledger.RecordReservation(ctx, record)
}

func (w *Writer) releaseCapacity(ctx context.Context) error {
	if w.capacity == nil {
		return nil
	}
	release := app.ReservationRelease{ReservationID: w.capacity.Reservation.Marker(), IdempotencyKey: fmt.Sprintf("log:%s:release", w.processID)}
	if err := w.capacity.Ledger.ReleaseReservation(ctx, release); err != nil {
		return err
	}
	return w.capacity.Reservations.Release(ctx, w.capacity.Reservation, w.capacity.Plan, w.capacity.Policy)
}

func (w *Writer) settleCapacity() error {
	if w.capacity == nil {
		return nil
	}
	artifacts := make([]app.OwnedArtifact, 0, len(w.files))
	for _, name := range w.files {
		info, err := os.Lstat(filepath.Join(w.root, name))
		if err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = paths.ErrProtectedPath
			}
			return err
		}
		hash, err := hashOwnedFile(w.root, name, w.maxBytes)
		if err != nil {
			return err
		}
		charged, err := app.StorageAccountingCharge(app.CurrentStorageAccountingVersion, app.StorageClassLog, app.ByteSize(info.Size()), app.ByteSize(info.Size()))
		if err != nil {
			return err
		}
		artifacts = append(artifacts, app.OwnedArtifact{
			ArtifactID:        "log-" + w.processID + "-" + name,
			OwnerKind:         app.OwnerLog,
			OwnerID:           w.processID,
			OperationID:       w.capacity.Plan.OperationID,
			ReservationID:     w.capacity.Reservation.Marker(),
			Class:             app.StorageClassLog,
			Lifecycle:         app.OwnedArtifactAccepted,
			LogicalBytes:      app.ByteSize(info.Size()),
			ObservedBytes:     app.ByteSize(info.Size()),
			ChargedBytes:      charged,
			VolumeID:          w.capacity.VolumeID,
			ManifestHash:      hash,
			AccountingVersion: app.CurrentStorageAccountingVersion,
			PolicyVersion:     w.capacity.Plan.PolicyVersion,
			Complete:          true,
			CreatedAt:         w.now().UTC(),
		})
	}
	settlement := app.ReservationSettlement{ReservationID: w.capacity.Reservation.Marker(), IdempotencyKey: fmt.Sprintf("log:%s:settle", w.processID), Artifacts: artifacts}
	return w.capacity.Ledger.SettleReservation(context.Background(), settlement)
}

func hashOwnedFile(root, name string, maxBytes uint64) (string, error) {
	file, err := paths.OpenExistingProtectedFile(root, name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	limited := io.LimitReader(file, int64(maxBytes)+1)
	bytes, err := io.Copy(hash, limited)
	if err != nil {
		return "", err
	}
	if uint64(bytes) > maxBytes {
		return "", paths.ErrProtectedTooLarge
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (w *Writer) updateMarker(closed bool) error {
	return w.writeMarker(closed, false)
}

func (w *Writer) writeMarker(closed, create bool) error {
	marker := ownerMarker{Version: logSchemaVersion, ProcessID: w.processID, RepositoryID: w.repositoryID, State: "active", CreatedAt: w.now().UTC().Format(time.RFC3339Nano), Files: append([]string(nil), w.files...)}
	if w.capacity != nil {
		marker.ReservationID = w.capacity.Reservation.Marker()
		marker.PlanDigest = w.capacity.Reservation.PlanDigest()
		marker.VolumeID = w.capacity.VolumeID
	}
	if closed {
		marker.State = "closed"
		marker.ClosedAt = w.now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(marker)
	if err != nil || len(data) > maxOwnerMarkerBytes {
		return errLogDisabled
	}
	if !create {
		file, err := paths.OpenExistingProtectedFileForUpdate(w.ownerRoot, w.markerName, os.O_WRONLY|os.O_TRUNC)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			return err
		}
		return file.Sync()
	}
	file, err := paths.OpenProtectedFile(w.ownerRoot, w.markerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (w *Writer) removeMarker() error {
	return removeOwnedFile(w.ownerRoot, w.markerName)
}

func validOwnerToken(value string) bool {
	if value == "" || len(value) > 64 || filepath.Base(value) != value || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func logName(processID string, generation uint64) string {
	return "log-" + processID + "-" + strconv.FormatUint(generation, 10) + ".jsonl"
}

func removeOwnedFile(root, name string) error {
	if name == "" || filepath.Base(name) != name || strings.Contains(name, "..") {
		return paths.ErrProtectedPath
	}
	file, err := paths.OpenExistingProtectedFile(root, name)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return paths.ErrProtectedPath
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Remove(filepath.Join(root, name))
}

var _ io.Writer = (*Writer)(nil)
