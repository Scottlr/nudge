// Package capacitystore implements the protected, cross-process reservation
// marker adapter. Application policy and plan semantics remain in internal/app.
package capacitystore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

const markerVersion uint32 = 1

var (
	// ErrUnknownMarker reports an unreadable, corrupt, or future marker. Such
	// markers remain charged and block new admission until reconciled.
	ErrUnknownMarker = errors.New("unknown capacity reservation marker")
	// ErrMarkerNotFound reports a handle whose owner marker is absent.
	ErrMarkerNotFound = errors.New("capacity reservation marker not found")
)

// Manager owns one Nudge capacity marker root. It does not infer ownership
// from age, PID, or timestamps.
type Manager struct {
	root       string
	markerRoot string
	lockRoot   string
}

// NewManager creates a protected marker and lock root.
func NewManager(root string) (*Manager, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, paths.ErrProtectedPath
	}
	manager := &Manager{root: root, markerRoot: filepath.Join(root, "markers"), lockRoot: filepath.Join(root, "locks")}
	if err := paths.EnsurePrivateDir(manager.root); err != nil {
		return nil, err
	}
	if err := paths.EnsurePrivateDir(manager.markerRoot); err != nil {
		return nil, err
	}
	if err := paths.EnsurePrivateDir(manager.lockRoot); err != nil {
		return nil, err
	}
	return manager, nil
}

var _ app.CapacityReservationPort = (*Manager)(nil)

// Reserve admits a plan after validating fresh volume evidence and all
// existing protected markers while holding the global-then-repository lock.
func (m *Manager) Reserve(ctx context.Context, plan app.CapacityPlan, policy app.ResourcePolicy, evidence []app.VolumeEvidence) (app.CapacityReservation, error) {
	if m == nil {
		return app.CapacityReservation{}, app.ErrInvalidCapacityPlan
	}
	if err := app.ValidateCapacityPlan(policy, plan, evidence); err != nil {
		return app.CapacityReservation{}, err
	}
	digest, err := app.PlanDigest(plan)
	if err != nil {
		return app.CapacityReservation{}, err
	}
	locks, err := m.acquireLocks(ctx, plan.RepositoryID)
	if err != nil {
		return app.CapacityReservation{}, err
	}
	defer closeLocks(locks)
	markers, err := m.loadMarkers()
	if err != nil {
		return app.CapacityReservation{}, err
	}
	if err := checkSpace(plan, evidence, markers, nil); err != nil {
		return app.CapacityReservation{}, err
	}
	nonce, err := newNonce()
	if err != nil {
		return app.CapacityReservation{}, err
	}
	name := nonce + ".json"
	record := markerRecordFromPlan(plan, digest, nonce, policy.Version, time.Now().UTC())
	if err := m.writeMarker(name, record); err != nil {
		return app.CapacityReservation{}, err
	}
	repositoryID := ""
	if plan.RepositoryID != nil {
		repositoryID = string(*plan.RepositoryID)
	}
	return app.NewCapacityReservation(name, plan.OperationID, repositoryID, digest, policy.Version)
}

// Recheck re-reads all active marker charges against fresh evidence. It does
// not silently release a reservation when an external process consumes space.
func (m *Manager) Recheck(ctx context.Context, reservation app.CapacityReservation, plan app.CapacityPlan, policy app.ResourcePolicy, bounds app.RecheckBounds, evidence []app.VolumeEvidence) (app.CapacityCheck, error) {
	if m == nil {
		return app.CapacityCheck{}, app.ErrReservationNotReady
	}
	if err := bounds.Validate(); err != nil {
		return app.CapacityCheck{}, err
	}
	digest, err := validateHandle(reservation, plan, policy)
	if err != nil {
		return app.CapacityCheck{}, err
	}
	if err := app.ValidateCapacityPlan(policy, plan, evidence); err != nil {
		var pressure *app.CapacityPressureError
		if errors.As(err, &pressure) {
			return app.CapacityCheck{Status: app.CapacityCheckPressure, PolicyVersion: policy.Version, Observed: time.Now().UTC(), Volumes: evidence}, err
		}
		return app.CapacityCheck{}, err
	}
	locks, err := m.acquireLocks(ctx, plan.RepositoryID)
	if err != nil {
		return app.CapacityCheck{}, err
	}
	defer closeLocks(locks)
	markers, err := m.loadMarkers()
	if err != nil {
		return app.CapacityCheck{}, err
	}
	owner, ok := findMarker(markers, reservation.Marker())
	if !ok {
		return app.CapacityCheck{}, ErrMarkerNotFound
	}
	if owner.PlanDigest != digest || owner.OperationID != string(plan.OperationID) || owner.Phase != "reserved" {
		return app.CapacityCheck{}, app.ErrReservationMismatch
	}
	if err := checkSpace(plan, evidence, markers, &reservation); err != nil {
		var pressure *app.CapacityPressureError
		if errors.As(err, &pressure) {
			return app.CapacityCheck{Status: app.CapacityCheckPressure, PolicyVersion: policy.Version, Observed: time.Now().UTC(), Volumes: evidence}, err
		}
		return app.CapacityCheck{}, err
	}
	return app.CapacityCheck{Status: app.CapacityCheckAdmitted, PolicyVersion: policy.Version, Observed: time.Now().UTC(), Volumes: evidence}, nil
}

// Release removes exactly the matching owner marker while holding the same
// lock order used for admission.
func (m *Manager) Release(ctx context.Context, reservation app.CapacityReservation, plan app.CapacityPlan, policy app.ResourcePolicy) error {
	if m == nil {
		return app.ErrReservationNotReady
	}
	if _, err := validateHandle(reservation, plan, policy); err != nil {
		return err
	}
	locks, err := m.acquireLocks(ctx, plan.RepositoryID)
	if err != nil {
		return err
	}
	defer closeLocks(locks)
	markers, err := m.loadMarkers()
	if err != nil {
		return err
	}
	owner, ok := findMarker(markers, reservation.Marker())
	if !ok {
		return ErrMarkerNotFound
	}
	if err := verifyMarkerOwner(owner, reservation, plan); err != nil {
		return err
	}
	return m.removeMarker(reservation.Marker())
}

// Reconcile removes a crashed marker only after both the owner lock and the
// operation journal have independently proved closure.
func (m *Manager) Reconcile(ctx context.Context, reservation app.CapacityReservation, plan app.CapacityPlan, policy app.ResourcePolicy, proof app.ReconciliationProof) error {
	if m == nil {
		return app.ErrReservationNotReady
	}
	if !proof.OwnerLockReconciled || !proof.OperationJournalDone {
		return app.ErrReservationNotReady
	}
	if _, err := validateHandle(reservation, plan, policy); err != nil {
		return err
	}
	locks, err := m.acquireLocks(ctx, plan.RepositoryID)
	if err != nil {
		return err
	}
	defer closeLocks(locks)
	markers, err := m.loadMarkers()
	if err != nil {
		return err
	}
	owner, ok := findMarker(markers, reservation.Marker())
	if !ok {
		return ErrMarkerNotFound
	}
	if err := verifyMarkerOwner(owner, reservation, plan); err != nil {
		return err
	}
	return m.removeMarker(reservation.Marker())
}

func (m *Manager) acquireLocks(ctx context.Context, repositoryID *domain.RepositoryID) ([]*filelock.Lock, error) {
	global, err := filelock.Acquire(ctx, filepath.Join(m.lockRoot, "global.lock"))
	if err != nil {
		return nil, err
	}
	locks := []*filelock.Lock{global}
	if repositoryID == nil {
		return locks, nil
	}
	repoDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(string(*repositoryID))))
	repository, err := filelock.Acquire(ctx, filepath.Join(m.lockRoot, "repo-"+repoDigest+".lock"))
	if err != nil {
		closeLocks(locks)
		return nil, err
	}
	return append(locks, repository), nil
}

func closeLocks(locks []*filelock.Lock) {
	for index := len(locks) - 1; index >= 0; index-- {
		_ = locks[index].Close()
	}
}

func (m *Manager) loadMarkers() ([]markerRecord, error) {
	entries, err := os.ReadDir(m.markerRoot)
	if err != nil {
		return nil, err
	}
	markers := make([]markerRecord, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(m.markerRoot, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, ErrUnknownMarker
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || filepath.Ext(name) != ".json" || !validMarkerName(name) {
			return nil, ErrUnknownMarker
		}
		data, err := paths.ReadProtectedFile(m.markerRoot, name)
		if err != nil {
			return nil, ErrUnknownMarker
		}
		var record markerRecord
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, ErrUnknownMarker
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, ErrUnknownMarker
		}
		if err := validateMarker(record); err != nil {
			return nil, ErrUnknownMarker
		}
		if record.Nonce+".json" != name {
			return nil, ErrUnknownMarker
		}
		markers = append(markers, record)
	}
	return markers, nil
}

func (m *Manager) writeMarker(name string, record markerRecord) error {
	file, err := paths.OpenProtectedFile(m.markerRoot, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(filepath.Join(m.markerRoot, name))
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syncDirectory(m.markerRoot); err != nil {
		return err
	}
	keep = true
	return nil
}

func (m *Manager) removeMarker(name string) error {
	if !validMarkerName(name) {
		return app.ErrReservationMismatch
	}
	path := filepath.Join(m.markerRoot, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrMarkerNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnknownMarker
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(m.markerRoot)
}

type markerRecord struct {
	Version         uint32         `json:"version"`
	OperationID     string         `json:"operation_id"`
	RepositoryID    string         `json:"repository_id,omitempty"`
	PlanDigest      string         `json:"plan_digest"`
	Nonce           string         `json:"nonce"`
	PolicyVersion   uint32         `json:"policy_version"`
	Phase           string         `json:"phase"`
	CreatedUnixNano int64          `json:"created_unix_nano"`
	Volumes         []markerVolume `json:"volumes"`
}

type markerVolume struct {
	ID                     string `json:"id"`
	Inputs                 uint64 `json:"inputs"`
	Temporaries            uint64 `json:"temporaries"`
	Finals                 uint64 `json:"finals"`
	CopyOnWrite            uint64 `json:"copy_on_write"`
	DatabaseWAL            uint64 `json:"database_wal"`
	AtomicOutput           uint64 `json:"atomic_output"`
	ConcurrentReservations uint64 `json:"concurrent_reservations"`
	Reserve                uint64 `json:"reserve"`
	RetainedDelta          uint64 `json:"retained_delta"`
}

func markerRecordFromPlan(plan app.CapacityPlan, digest, nonce string, policyVersion app.ResourcePolicyVersion, now time.Time) markerRecord {
	repositoryID := ""
	if plan.RepositoryID != nil {
		repositoryID = string(*plan.RepositoryID)
	}
	record := markerRecord{Version: markerVersion, OperationID: string(plan.OperationID), RepositoryID: repositoryID, PlanDigest: digest, Nonce: nonce, PolicyVersion: uint32(policyVersion), Phase: "reserved", CreatedUnixNano: now.UnixNano(), Volumes: make([]markerVolume, 0, len(plan.VolumePeaks))}
	for _, peak := range plan.VolumePeaks {
		record.Volumes = append(record.Volumes, markerVolume{ID: peak.ID, Inputs: uint64(peak.Inputs), Temporaries: uint64(peak.Temporaries), Finals: uint64(peak.Finals), CopyOnWrite: uint64(peak.CopyOnWrite), DatabaseWAL: uint64(peak.DatabaseWAL), AtomicOutput: uint64(peak.AtomicOutput), ConcurrentReservations: uint64(peak.ConcurrentReservations), Reserve: uint64(peak.Reserve), RetainedDelta: uint64(peak.RetainedDelta)})
	}
	return record
}

func validateMarker(record markerRecord) error {
	if record.Version != markerVersion || record.OperationID == "" || record.PlanDigest == "" || record.Nonce == "" || record.PolicyVersion != uint32(app.CurrentResourcePolicyVersion) || record.Phase != "reserved" || record.CreatedUnixNano <= 0 || len(record.Volumes) == 0 {
		return ErrUnknownMarker
	}
	seen := make(map[string]struct{}, len(record.Volumes))
	for _, volume := range record.Volumes {
		if volume.ID == "" {
			return ErrUnknownMarker
		}
		if _, ok := seen[volume.ID]; ok {
			return ErrUnknownMarker
		}
		seen[volume.ID] = struct{}{}
		if _, err := (app.VolumePeak{ID: volume.ID, Inputs: app.ByteSize(volume.Inputs), Temporaries: app.ByteSize(volume.Temporaries), Finals: app.ByteSize(volume.Finals), CopyOnWrite: app.ByteSize(volume.CopyOnWrite), DatabaseWAL: app.ByteSize(volume.DatabaseWAL), AtomicOutput: app.ByteSize(volume.AtomicOutput), ConcurrentReservations: app.ByteSize(volume.ConcurrentReservations), Reserve: app.ByteSize(volume.Reserve), RetainedDelta: app.ByteSize(volume.RetainedDelta)}).Charge(); err != nil {
			return ErrUnknownMarker
		}
	}
	return nil
}

func checkSpace(plan app.CapacityPlan, evidence []app.VolumeEvidence, markers []markerRecord, owner *app.CapacityReservation) error {
	free := make(map[string]app.VolumeEvidence, len(evidence))
	for _, value := range evidence {
		free[value.ID] = value
	}
	active := make(map[string]app.ByteSize)
	for _, marker := range markers {
		for _, volume := range marker.Volumes {
			charge, err := (app.VolumePeak{Inputs: app.ByteSize(volume.Inputs), Temporaries: app.ByteSize(volume.Temporaries), Finals: app.ByteSize(volume.Finals), CopyOnWrite: app.ByteSize(volume.CopyOnWrite), DatabaseWAL: app.ByteSize(volume.DatabaseWAL), AtomicOutput: app.ByteSize(volume.AtomicOutput), ConcurrentReservations: app.ByteSize(volume.ConcurrentReservations), Reserve: app.ByteSize(volume.Reserve), RetainedDelta: app.ByteSize(volume.RetainedDelta)}).Charge()
			if err != nil {
				return ErrUnknownMarker
			}
			var addErr error
			active[volume.ID], addErr = active[volume.ID].Add(charge)
			if addErr != nil {
				return ErrUnknownMarker
			}
		}
	}
	planCharges := make(map[string]app.ByteSize, len(plan.VolumePeaks))
	for _, peak := range plan.VolumePeaks {
		charge, err := peak.Charge()
		if err != nil {
			return err
		}
		planCharges[peak.ID] = charge
	}
	for volumeID, charge := range active {
		observation, ok := free[volumeID]
		if !ok {
			return app.ErrReservationNotReady
		}
		if charge > observation.Free {
			return &app.CapacityPressureError{VolumeID: volumeID, Required: charge, Free: observation.Free, Mode: observation.Mode}
		}
	}
	for volumeID, charge := range planCharges {
		observation, ok := free[volumeID]
		if !ok {
			return app.ErrInvalidCapacityPlan
		}
		if owner != nil && volumeInMarker(markers, owner.Marker(), volumeID) {
			continue
		}
		required, err := active[volumeID].Add(charge)
		if err != nil {
			return app.ErrInvalidCapacityPlan
		}
		if required > observation.Free {
			return &app.CapacityPressureError{VolumeID: volumeID, Required: required, Free: observation.Free, Mode: observation.Mode}
		}
	}
	return nil
}

func volumeInMarker(markers []markerRecord, markerName, volumeID string) bool {
	for _, marker := range markers {
		if marker.Nonce+".json" != markerName {
			continue
		}
		for _, volume := range marker.Volumes {
			if volume.ID == volumeID {
				return true
			}
		}
	}
	return false
}

func findMarker(markers []markerRecord, name string) (markerRecord, bool) {
	if !validMarkerName(name) {
		return markerRecord{}, false
	}
	for _, marker := range markers {
		if marker.Nonce+".json" == name {
			return marker, true
		}
	}
	return markerRecord{}, false
}

func validateHandle(reservation app.CapacityReservation, plan app.CapacityPlan, policy app.ResourcePolicy) (string, error) {
	if reservation.Marker() == "" || reservation.OperationID() != plan.OperationID || reservation.PolicyVersion() != policy.Version || !validMarkerName(reservation.Marker()) {
		return "", app.ErrReservationMismatch
	}
	expectedRepositoryID := ""
	if plan.RepositoryID != nil {
		expectedRepositoryID = string(*plan.RepositoryID)
	}
	if reservation.RepositoryID() != expectedRepositoryID {
		return "", app.ErrReservationMismatch
	}
	digest, err := app.PlanDigest(plan)
	if err != nil || digest != reservation.PlanDigest() {
		return "", app.ErrReservationMismatch
	}
	return digest, nil
}

func verifyMarkerOwner(record markerRecord, reservation app.CapacityReservation, plan app.CapacityPlan) error {
	expectedRepositoryID := ""
	if plan.RepositoryID != nil {
		expectedRepositoryID = string(*plan.RepositoryID)
	}
	if record.OperationID != string(reservation.OperationID()) || record.RepositoryID != expectedRepositoryID || record.PlanDigest != reservation.PlanDigest() || record.Nonce+".json" != reservation.Marker() {
		return app.ErrReservationMismatch
	}
	return nil
}

func newNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validMarkerName(name string) bool {
	if len(name) != 37 || filepath.Base(name) != name || name[32:] != ".json" {
		return false
	}
	_, err := hex.DecodeString(name[:32])
	return err == nil
}
