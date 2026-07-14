// Package artifactspool implements Nudge-owned bounded spools and verified
// no-replace adoption. It does not define artifact-specific manifests.
package artifactspool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

const (
	markerVersion = 1
	markerName    = "spool.json"
	payloadName   = "payload"
)

var (
	// ErrMarkerMismatch reports a descriptor that does not match its marker.
	ErrMarkerMismatch = errors.New("artifact spool marker mismatch")
	// ErrMarkerUnknown reports corrupt or future spool state that must remain visible.
	ErrMarkerUnknown = errors.New("unknown artifact spool marker")
	// ErrDestinationExists is the native no-replace adoption race.
	ErrDestinationExists = errors.New("artifact spool destination exists")
)

// Manager owns protected spool and publication roots. Owner and operation IDs
// are hashed before becoming path components, so caller text is never a path.
type Manager struct {
	root        string
	spoolRoot   string
	publishRoot string
	lockRoot    string
}

// NewManager creates the protected roots used by all artifact spools.
func NewManager(root string) (*Manager, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, paths.ErrProtectedPath
	}
	manager := &Manager{
		root:        root,
		spoolRoot:   filepath.Join(root, "spools"),
		publishRoot: filepath.Join(root, "published"),
		lockRoot:    filepath.Join(root, "locks"),
	}
	for _, path := range []string{manager.root, manager.spoolRoot, manager.publishRoot, manager.lockRoot} {
		if err := paths.EnsurePrivateDir(path); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

var _ app.ArtifactSpoolPort = (*Manager)(nil)

// Create creates a marker-bound owner-only spool before returning its handle.
func (m *Manager) Create(ctx context.Context, spec app.SpoolSpec) (app.ArtifactSpoolHandle, error) {
	if m == nil || ctx == nil {
		return nil, app.ErrInvalidArtifactSpool
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	lock, err := m.acquireOwnerLock(ctx, spec.OwnerKind, string(spec.OperationID))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	spoolID := digestID(string(spec.OwnerKind), string(spec.OperationID), nonce)
	spoolPath := m.spoolPath(spec.OwnerKind, string(spec.OperationID), nonce)
	if err := m.ensureSpoolParents(spec.OwnerKind, string(spec.OperationID), spoolPath); err != nil {
		return nil, err
	}
	payloadPath := filepath.Join(spoolPath, payloadName)
	if err := paths.EnsurePrivateDir(payloadPath); err != nil {
		return nil, err
	}
	descriptor := app.ArtifactSpool{
		SpoolID:       spoolID,
		OperationID:   spec.OperationID,
		OwnerKind:     spec.OwnerKind,
		ReservationID: spec.Reservation.Marker(),
		RootNonce:     nonce,
		Limits:        spec.Limits,
		State:         app.SpoolOpen,
	}
	record := markerRecordFromDescriptor(descriptor, time.Now().UTC())
	if err := writeNewMarker(spoolPath, record); err != nil {
		return nil, err
	}
	handle := newHandle(m, descriptor, spoolPath, payloadPath, record)
	return handle, nil
}

// Open resolves an existing descriptor without accepting marker or path
// mismatches. It is used by owner restart/recovery code after the OS lock has
// proved the previous process no longer owns the operation.
func (m *Manager) Open(ctx context.Context, descriptor app.ArtifactSpool) (app.ArtifactSpoolHandle, error) {
	if m == nil || ctx == nil {
		return nil, app.ErrInvalidArtifactSpool
	}
	if err := descriptor.Validate(); err != nil {
		return nil, err
	}
	lock, err := m.acquireOwnerLock(ctx, descriptor.OwnerKind, string(descriptor.OperationID))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	spoolPath := m.spoolPath(descriptor.OwnerKind, string(descriptor.OperationID), descriptor.RootNonce)
	payloadPath := filepath.Join(spoolPath, payloadName)
	if err := paths.EnsurePrivateDir(spoolPath); err != nil {
		return nil, app.ErrSpoolResidueAmbiguous
	}
	record, err := readMarker(spoolPath)
	if err != nil {
		return nil, err
	}
	if err := verifyMarkerDescriptor(record, descriptor); err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(payloadPath); statErr == nil {
		if err := paths.EnsurePrivateDir(payloadPath); err != nil {
			return nil, app.ErrSpoolResidueAmbiguous
		}
	} else if !errors.Is(statErr, os.ErrNotExist) || record.State != string(app.SpoolPublishing) && record.State != string(app.SpoolPublished) {
		return nil, app.ErrSpoolResidueAmbiguous
	}
	var bytes app.ByteSize
	if _, statErr := os.Lstat(payloadPath); statErr == nil {
		bytes, err = scanPayloadBytes(payloadPath, descriptor.Limits)
		if err != nil {
			return nil, err
		}
	}
	descriptor.State = app.SpoolState(record.State)
	handle := newHandle(m, descriptor, spoolPath, payloadPath, record)
	handle.reserved.Store(uint64(bytes))
	if record.ManifestHash != "" {
		handle.verified = artifactIdentityFromMarker(record)
	}
	return handle, nil
}

func newHandle(manager *Manager, descriptor app.ArtifactSpool, spoolPath, payloadPath string, record markerRecord) *Handle {
	handle := &Handle{manager: manager, descriptor: descriptor, spoolPath: spoolPath, payloadPath: payloadPath, marker: record}
	if record.ManifestHash != "" {
		handle.verified = artifactIdentityFromMarker(record)
	}
	return handle
}

// Handle is one owner-bound spool capability. Lifecycle methods are serialized
// by operationMu; file writers may stream independently but do not share a
// writer instance concurrently.
type Handle struct {
	manager     *Manager
	descriptor  app.ArtifactSpool
	spoolPath   string
	payloadPath string
	marker      markerRecord

	operationMu sync.Mutex
	stateMu     sync.Mutex
	active      int
	reserved    atomic.Uint64
	verified    app.ArtifactIdentity
}

// Descriptor returns an immutable descriptor snapshot.
func (h *Handle) Descriptor() app.ArtifactSpool {
	if h == nil {
		return app.ArtifactSpool{}
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	return h.descriptor
}

// CreateFile creates one owner-only, create-new file below the payload root.
func (h *Handle) CreateFile(ctx context.Context, relative string) (app.ArtifactSpoolFile, error) {
	if h == nil || ctx == nil {
		return nil, app.ErrInvalidArtifactSpool
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	h.stateMu.Lock()
	state := h.descriptor.State
	limits := h.descriptor.Limits
	h.stateMu.Unlock()
	if state != app.SpoolOpen {
		return nil, app.ErrSpoolNotReady
	}
	clean, err := validateRelative(relative, limits.MaxPathBytes)
	if err != nil {
		return nil, err
	}
	file, err := paths.OpenProtectedFile(h.payloadPath, clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	writer := &fileWriter{spool: h, file: file, hash: sha256.New()}
	h.stateMu.Lock()
	h.active++
	h.stateMu.Unlock()
	return writer, nil
}

// WriteFrom copies a bounded stream using a fixed buffer and cancellation
// checks at every configured chunk.
func (h *Handle) WriteFrom(ctx context.Context, relative string, source io.Reader) (app.StreamIdentity, error) {
	if source == nil {
		return app.StreamIdentity{}, app.ErrInvalidArtifactSpool
	}
	writerValue, err := h.CreateFile(ctx, relative)
	if err != nil {
		h.markRecoveryLocal()
		return app.StreamIdentity{}, err
	}
	writer := writerValue.(*fileWriter)
	h.stateMu.Lock()
	limits := h.descriptor.Limits
	h.stateMu.Unlock()
	bufferSize := limits.BufferBytes
	if limits.CheckEveryBytes < bufferSize {
		bufferSize = limits.CheckEveryBytes
	}
	if bufferSize > 1*app.MiB {
		bufferSize = 1 * app.MiB
	}
	buffer := make([]byte, int(bufferSize))
	zeroReads := 0
	for {
		if err := contextErr(ctx); err != nil {
			_ = writer.Close()
			h.markRecoveryLocal()
			return app.StreamIdentity{}, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			zeroReads = 0
			if err := contextErr(ctx); err != nil {
				_ = writer.Close()
				h.markRecoveryLocal()
				return app.StreamIdentity{}, err
			}
			if _, err := writer.Write(buffer[:read]); err != nil {
				_ = writer.Close()
				h.markRecoveryLocal()
				return app.StreamIdentity{}, err
			}
		} else if readErr == nil {
			zeroReads++
			if zeroReads >= 100 {
				_ = writer.Close()
				h.markRecoveryLocal()
				return app.StreamIdentity{}, io.ErrNoProgress
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = writer.Close()
			h.markRecoveryLocal()
			return app.StreamIdentity{}, readErr
		}
	}
	if err := writer.Close(); err != nil {
		return app.StreamIdentity{}, err
	}
	return writer.identity(), nil
}

// Abort removes only an exact marker-bound spool after all writers have closed.
func (h *Handle) Abort(ctx context.Context) error {
	if h == nil || ctx == nil {
		return app.ErrInvalidArtifactSpool
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	lock, err := h.manager.acquireOwnerLock(ctx, h.descriptor.OwnerKind, string(h.descriptor.OperationID))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := h.requireNoActiveHandles(); err != nil {
		return err
	}
	if err := h.refreshMarker(); err != nil {
		return err
	}
	if h.marker.State == string(app.SpoolPublished) || h.marker.State == string(app.SpoolAborted) {
		return app.ErrSpoolNotReady
	}
	if err := removeOwnedSpool(h.spoolPath, h.payloadPath); err != nil {
		return err
	}
	h.setDescriptorState(app.SpoolAborted)
	return nil
}

// Recover performs only the proof-selected resume or removal effect.
func (h *Handle) Recover(ctx context.Context, proof app.SpoolRecoveryProof) error {
	if h == nil || ctx == nil {
		return app.ErrInvalidArtifactSpool
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	lock, err := h.manager.acquireOwnerLock(ctx, h.descriptor.OwnerKind, string(h.descriptor.OperationID))
	if err != nil {
		return err
	}
	defer lock.Close()
	if !proof.OwnerLockReconciled || !proof.OperationJournalDone || !proof.NoActiveHandles {
		return app.ErrSpoolResidueAmbiguous
	}
	if err := h.requireNoActiveHandles(); err != nil {
		return err
	}
	if err := h.refreshMarker(); err != nil {
		return err
	}
	switch proof.Action {
	case app.SpoolRecoveryRemove:
		if h.marker.TargetDigest != "" {
			if err := proof.Expected.Validate(); err != nil || proof.Expected.SpoolID != h.descriptor.SpoolID {
				return app.ErrSpoolResidueAmbiguous
			}
			destination, pathErr := h.manager.destinationPath(proof.Target, h.descriptor.Limits.MaxPathBytes)
			if pathErr != nil || digestID(string(proof.Target.OwnerKind), proof.Target.RelativePath, proof.Target.SourceRelativePath) != h.marker.TargetDigest || verifyPublishedTarget(ctx, proof.Expected, proof.Target, destination, h.descriptor.Limits) != nil {
				return app.ErrSpoolResidueAmbiguous
			}
		}
		return removeOwnedSpool(h.spoolPath, h.payloadPath)
	case app.SpoolRecoveryResume:
		if err := proof.Expected.Validate(); err != nil || proof.Expected.SpoolID != h.descriptor.SpoolID {
			return app.ErrSpoolResidueAmbiguous
		}
		if h.marker.State == string(app.SpoolPublishing) {
			destination, pathErr := h.manager.destinationPath(proof.Target, h.descriptor.Limits.MaxPathBytes)
			if pathErr == nil {
				if _, destinationErr := os.Lstat(destination); destinationErr == nil {
					if verifyPublishedTarget(ctx, proof.Expected, proof.Target, destination, h.descriptor.Limits) != nil {
						return app.ErrSpoolResidueAmbiguous
					}
					return removeOwnedSpool(h.spoolPath, h.payloadPath)
				}
			}
		}
		if h.marker.State == string(app.SpoolOpen) || h.marker.State == string(app.SpoolVerifying) || h.marker.State == string(app.SpoolPublishing) || h.marker.State == string(app.SpoolRecoveryRequired) {
			if err := h.updateState(app.SpoolOpen); err != nil {
				return err
			}
			if _, err := h.closeAndVerifyLocked(ctx); err != nil {
				return err
			}
		}
		_, err := h.publishLocked(ctx, proof.Expected, proof.Target)
		return err
	default:
		return app.ErrSpoolResidueAmbiguous
	}
}

func (h *Handle) requireNoActiveHandles() error {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.active != 0 {
		return app.ErrSpoolBusy
	}
	return nil
}

func (h *Handle) setDescriptorState(state app.SpoolState) {
	h.stateMu.Lock()
	h.descriptor.State = state
	h.stateMu.Unlock()
}

func (h *Handle) markRecoveryLocal() {
	h.stateMu.Lock()
	h.descriptor.State = app.SpoolRecoveryRequired
	h.marker.State = string(app.SpoolRecoveryRequired)
	h.stateMu.Unlock()
}

func (h *Handle) refreshMarker() error {
	record, err := readMarker(h.spoolPath)
	if err != nil {
		return err
	}
	if err := verifyMarkerDescriptor(record, h.descriptor); err != nil {
		return err
	}
	h.marker = record
	h.setDescriptorState(app.SpoolState(record.State))
	return nil
}

func (m *Manager) ensureSpoolParents(owner app.OwnerKind, operation, spoolPath string) error {
	ownerPath := filepath.Join(m.spoolRoot, digestID(string(owner)))
	operationPath := filepath.Join(ownerPath, digestID(operation))
	for _, path := range []string{ownerPath, operationPath, spoolPath} {
		if err := paths.EnsurePrivateDir(path); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) spoolPath(owner app.OwnerKind, operation, nonce string) string {
	return filepath.Join(m.spoolRoot, digestID(string(owner)), digestID(operation), nonce)
}

func (m *Manager) acquireOwnerLock(ctx context.Context, owner app.OwnerKind, operation string) (*filelock.Lock, error) {
	path := filepath.Join(m.lockRoot, "owner-"+digestID(string(owner), operation)+".lock")
	return filelock.Acquire(ctx, path)
}

func newNonce() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func digestID(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return app.ErrInvalidArtifactSpool
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func validateRelative(value string, max app.ByteSize) (string, error) {
	if value == "" || len(value) > int(max) || filepath.IsAbs(value) {
		return "", app.ErrInvalidArtifactSpool
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || isParentRelative(clean) {
		return "", app.ErrInvalidArtifactSpool
	}
	return clean, nil
}

func isParentRelative(value string) bool {
	prefix := ".." + string(filepath.Separator)
	return len(value) > len(prefix) && value[:len(prefix)] == prefix
}
