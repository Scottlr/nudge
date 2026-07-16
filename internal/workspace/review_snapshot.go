package workspace

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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/capacityprobe"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

const (
	reviewSnapshotMarkerVersion = 1
	reviewSnapshotMarkerName    = ".nudge-review-snapshot.json"
	copyBufferBytes             = 64 * 1024
	maxMarkerBytes              = 64 * 1024
)

var (
	errReviewSnapshotPath = errors.New("review snapshot path cannot be represented safely")
)

// ReviewSnapshotConfig composes the application ports and the protected root
// used by the local immutable snapshot owner.
type ReviewSnapshotConfig struct {
	Root         string
	Source       app.ReviewSnapshotBaseSource
	Captures     app.LocalCaptureStore
	Store        app.ReviewSnapshotStore
	IDs          app.IDSource
	Clock        app.Clock
	Policy       app.ResourcePolicy
	FreeSpace    func(context.Context, string) (app.ByteSize, error)
	Persist      bool
	ProcessNonce string
}

// ReviewSnapshotManager owns published local review snapshots and their read
// leases. It does not execute Git or read the live worktree.
type ReviewSnapshotManager struct {
	root         string
	published    string
	ephemeral    string
	temporary    string
	locks        string
	source       app.ReviewSnapshotBaseSource
	captures     app.LocalCaptureStore
	store        app.ReviewSnapshotStore
	ids          app.IDSource
	clock        app.Clock
	policy       app.ResourcePolicy
	limits       app.ReviewSnapshotLimits
	freeSpace    func(context.Context, string) (app.ByteSize, error)
	persist      bool
	processNonce string

	mu        sync.Mutex
	snapshots map[domain.ReviewSnapshotID]app.ReviewSnapshot
	leases    map[domain.ReviewSnapshotLeaseID]app.ReviewSnapshotLease
}

// ReviewSnapshotCleanupOwner adapts the snapshot owner to the repository
// cleanup coordinator. The manager still performs all marker, manifest,
// containment, lease, and owner-lock checks before removing anything.
type ReviewSnapshotCleanupOwner struct {
	Manager *ReviewSnapshotManager
}

func (o ReviewSnapshotCleanupOwner) Remove(ctx context.Context, resource app.CleanupResource) error {
	if o.Manager == nil || resource.Kind != app.CleanupResourceReviewSnapshot || resource.ID == "" {
		return app.ErrCleanupInvalid
	}
	snapshot, err := o.Manager.loadSnapshot(ctx, domain.ReviewSnapshotID(resource.ID))
	if err != nil {
		return err
	}
	if snapshot.RepositoryID != resource.RepositoryID || snapshot.Root != resource.CanonicalPath || snapshot.MarkerNonce != resource.MarkerNonce || snapshot.ManifestHash != resource.ManifestHash {
		return app.ErrCleanupConflict
	}
	return o.Manager.Remove(ctx, snapshot.ID)
}

// NewReviewSnapshotManager creates the protected roots and validates the
// source, persistence, and versioned limit contract before use.
func NewReviewSnapshotManager(config ReviewSnapshotConfig) (*ReviewSnapshotManager, error) {
	if config.Root == "" || !filepath.IsAbs(config.Root) || filepath.Clean(config.Root) != config.Root || config.Source == nil || config.Captures == nil {
		return nil, app.ErrInvalidReviewSnapshot
	}
	if config.Persist && config.Store == nil {
		return nil, app.ErrInvalidReviewSnapshot
	}
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	limits, err := app.NewReviewSnapshotLimits(config.Policy)
	if err != nil {
		return nil, err
	}
	if config.IDs == nil {
		config.IDs = app.RandomIDSource{}
	}
	if config.Clock == nil {
		config.Clock = app.SystemClock{}
	}
	if config.ProcessNonce == "" {
		config.ProcessNonce, err = newNonce()
		if err != nil {
			return nil, err
		}
	}
	if !validNonce(config.ProcessNonce) {
		return nil, app.ErrInvalidReviewSnapshot
	}
	if config.FreeSpace == nil {
		config.FreeSpace = func(ctx context.Context, path string) (app.ByteSize, error) {
			observation, observeErr := (capacityprobe.Probe{}).Observe(ctx, path)
			if observeErr != nil {
				return 0, observeErr
			}
			return observation.Free, nil
		}
	}
	manager := &ReviewSnapshotManager{
		root: config.Root, published: filepath.Join(config.Root, "published"),
		ephemeral: filepath.Join(config.Root, "ephemeral"), temporary: filepath.Join(config.Root, "temporary"),
		locks: filepath.Join(config.Root, "locks"), source: config.Source, captures: config.Captures,
		store: config.Store, ids: config.IDs, clock: config.Clock, policy: config.Policy, limits: limits,
		freeSpace: config.FreeSpace, persist: config.Persist, processNonce: config.ProcessNonce,
		snapshots: make(map[domain.ReviewSnapshotID]app.ReviewSnapshot),
		leases:    make(map[domain.ReviewSnapshotLeaseID]app.ReviewSnapshotLease),
	}
	for _, path := range []string{manager.root, manager.published, manager.ephemeral, manager.temporary, manager.locks} {
		if err := paths.EnsurePrivateDir(path); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

var _ app.ReviewSnapshotManager = (*ReviewSnapshotManager)(nil)

// Ensure reuses a verified snapshot for the same accepted capture or builds a
// new private root from pinned objects and accepted capture blobs.
func (m *ReviewSnapshotManager) Ensure(ctx context.Context, request app.ReviewSnapshotEnsureRequest) (app.ReviewSnapshot, error) {
	if m == nil || ctx == nil {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	if err := request.Validate(); err != nil {
		return app.ReviewSnapshot{}, err
	}
	if request.Target != nil || request.PolicyVersion != m.policy.Version || request.EvidenceVersion != app.CurrentCapabilityEvidenceVersion {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	manifest, err := m.captures.OpenCaptureManifest(ctx, request.CaptureID)
	if err != nil {
		return app.ReviewSnapshot{}, err
	}
	if manifest.CaptureID != request.CaptureID || manifest.RepositoryID != request.RepositoryID || manifest.WorktreeID != request.WorktreeID || manifest.Validate() != nil {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotCorrupt
	}
	return m.ensureMaterialized(ctx, request, &manifest)
}

// EnsureTarget materializes a read-only snapshot from a pinned branch or
// commit tree. It never consults the current worktree or follows a ref.
func (m *ReviewSnapshotManager) EnsureTarget(ctx context.Context, request app.ReviewSnapshotEnsureRequest) (app.ReviewSnapshot, error) {
	if m == nil || ctx == nil || request.Target == nil {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	if err := request.Validate(); err != nil || request.PolicyVersion != m.policy.Version || request.EvidenceVersion != app.CurrentCapabilityEvidenceVersion || request.Persist != m.persist {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	return m.ensureMaterialized(ctx, request, nil)
}

func (m *ReviewSnapshotManager) ensureMaterialized(ctx context.Context, request app.ReviewSnapshotEnsureRequest, manifest *app.CaptureManifest) (app.ReviewSnapshot, error) {
	if request.Persist != m.persist {
		return app.ReviewSnapshot{}, app.ErrInvalidReviewSnapshot
	}
	if existing, ok, loadErr := m.loadExisting(ctx, request); loadErr != nil {
		return app.ReviewSnapshot{}, loadErr
	} else if ok {
		return existing, nil
	}
	lock, err := m.acquireSnapshotLock(ctx, m.snapshotKey(request), request.Persist)
	if err != nil {
		return app.ReviewSnapshot{}, err
	}
	defer lock.Close()
	if existing, ok, loadErr := m.loadExisting(ctx, request); loadErr != nil {
		return app.ReviewSnapshot{}, loadErr
	} else if ok {
		return existing, nil
	}
	buildCtx, cancel := context.WithTimeout(ctx, m.limits.MaxDuration)
	defer cancel()
	snapshot, marker, temporaryRoot, err := m.materialize(buildCtx, request, manifest)
	if err != nil {
		return app.ReviewSnapshot{}, err
	}
	finalRoot := m.snapshotRoot(request)
	if _, statErr := os.Lstat(finalRoot); statErr == nil {
		_ = removeOwnedTree(temporaryRoot)
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotBusy
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = removeOwnedTree(temporaryRoot)
		return app.ReviewSnapshot{}, statErr
	}
	if err := os.Rename(temporaryRoot, finalRoot); err != nil {
		_ = removeOwnedTree(temporaryRoot)
		return app.ReviewSnapshot{}, err
	}
	snapshot.Root = finalRoot
	marker.Root = finalRoot
	if err := m.writeMarker(finalRoot, marker); err != nil {
		_ = removeOwnedTree(finalRoot)
		return app.ReviewSnapshot{}, err
	}
	if err := m.makeReadOnly(finalRoot); err != nil {
		_ = removeOwnedTree(finalRoot)
		return app.ReviewSnapshot{}, err
	}
	if err := m.verifySnapshot(snapshot); err != nil {
		_ = removeOwnedTree(finalRoot)
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotCorrupt
	}
	if request.Persist {
		if err := m.store.SaveReviewSnapshot(ctx, snapshot); err != nil {
			_ = removeOwnedTree(finalRoot)
			return app.ReviewSnapshot{}, err
		}
	} else {
		m.mu.Lock()
		m.snapshots[snapshot.ID] = snapshot
		m.mu.Unlock()
	}
	return snapshot, nil
}

// AcquireReadLease verifies the durable marker and manifest before granting a
// lease. The returned capability has no writable root.
func (m *ReviewSnapshotManager) AcquireReadLease(ctx context.Context, id domain.ReviewSnapshotID) (app.ReviewSnapshotLease, error) {
	if m == nil || ctx == nil || id == "" {
		return app.ReviewSnapshotLease{}, app.ErrInvalidReviewSnapshot
	}
	snapshot, err := m.loadSnapshot(ctx, id)
	if err != nil {
		return app.ReviewSnapshotLease{}, err
	}
	if snapshot.State != app.ReviewSnapshotReady || m.verifySnapshot(snapshot) != nil {
		return app.ReviewSnapshotLease{}, app.ErrReviewSnapshotCorrupt
	}
	leaseID, err := domain.NewReviewSnapshotLeaseID(m.ids.NewID())
	if err != nil {
		return app.ReviewSnapshotLease{}, err
	}
	lease := app.ReviewSnapshotLease{ID: leaseID, SnapshotID: snapshot.ID, CaptureID: snapshot.CaptureID, TargetKind: snapshot.TargetKind, HeadObjectID: snapshot.HeadObjectID, BaseObjectID: snapshot.BaseObjectID, ParentLabel: snapshot.ParentLabel, SourceRef: snapshotSourceRef(snapshot), Root: snapshot.Root, ManifestHash: snapshot.ManifestHash, ProcessNonce: m.processNonce, AcquiredAt: m.clock.Now().UTC()}
	if err := lease.Validate(); err != nil {
		return app.ReviewSnapshotLease{}, err
	}
	if m.persist {
		if err := m.store.SaveReviewSnapshotLease(ctx, lease); err != nil {
			return app.ReviewSnapshotLease{}, err
		}
	} else {
		m.mu.Lock()
		m.leases[lease.ID] = lease
		m.mu.Unlock()
	}
	return lease, nil
}

// Release closes one read lease and never removes the underlying snapshot.
func (m *ReviewSnapshotManager) Release(ctx context.Context, lease app.ReviewSnapshotLease) error {
	if m == nil || ctx == nil || lease.Validate() != nil {
		return app.ErrInvalidReviewSnapshot
	}
	if m.persist {
		return m.store.ReleaseReviewSnapshotLease(ctx, lease.ID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.leases[lease.ID]; !ok {
		return app.ErrReviewSnapshotNotFound
	}
	delete(m.leases, lease.ID)
	return nil
}

// Recover performs the only explicit residue action: removal after positive
// owner-lock and no-active-lease proof.
func (m *ReviewSnapshotManager) Recover(ctx context.Context, proof app.ReviewSnapshotRecoveryProof) error {
	if m == nil || ctx == nil || proof.Validate() != nil {
		return app.ErrInvalidReviewSnapshot
	}
	snapshot, err := m.loadSnapshot(ctx, proof.SnapshotID)
	if errors.Is(err, app.ErrReviewSnapshotNotFound) && !m.persist {
		snapshot, err = m.findEphemeralSnapshot(proof.SnapshotID)
	}
	if err != nil {
		return err
	}
	if snapshot.MarkerNonce != proof.ProcessNonce {
		return app.ErrReviewSnapshotResidue
	}
	return m.removeSnapshot(ctx, snapshot, true)
}

// Remove reclaims a verified, unleased snapshot through positive containment
// and marker ownership checks.
func (m *ReviewSnapshotManager) Remove(ctx context.Context, id domain.ReviewSnapshotID) error {
	if m == nil || ctx == nil || id == "" {
		return app.ErrInvalidReviewSnapshot
	}
	snapshot, err := m.loadSnapshot(ctx, id)
	if err != nil {
		return err
	}
	return m.removeSnapshot(ctx, snapshot, false)
}

// Close removes clean no-persist snapshots and leaves ambiguous residue for
// an explicit proof-based recovery path.
func (m *ReviewSnapshotManager) Close(ctx context.Context) error {
	if m == nil || ctx == nil || m.persist {
		return nil
	}
	m.mu.Lock()
	snapshots := make([]app.ReviewSnapshot, 0, len(m.snapshots))
	for _, snapshot := range m.snapshots {
		snapshots = append(snapshots, snapshot)
	}
	m.mu.Unlock()
	var result error
	for _, snapshot := range snapshots {
		if err := m.removeSnapshot(ctx, snapshot, false); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (m *ReviewSnapshotManager) loadExisting(ctx context.Context, request app.ReviewSnapshotEnsureRequest) (app.ReviewSnapshot, bool, error) {
	var snapshot app.ReviewSnapshot
	var err error
	if m.persist {
		if request.Target != nil {
			snapshot, err = m.store.LoadReviewSnapshotByObject(ctx, request.RepositoryID, request.Target.Head.ObjectID, request.PolicyVersion, request.FormatVersion)
		} else {
			snapshot, err = m.store.LoadReviewSnapshotByCapture(ctx, request.CaptureID)
		}
	} else {
		m.mu.Lock()
		for _, value := range m.snapshots {
			if request.Target != nil {
				if value.RepositoryID == request.RepositoryID && value.TargetKind == request.Target.Spec.Kind && value.HeadObjectID == request.Target.Head.ObjectID && value.PolicyVersion == request.PolicyVersion && value.FormatVersion == request.FormatVersion {
					snapshot = value
					break
				}
			} else if value.CaptureID == request.CaptureID {
				snapshot = value
				break
			}
		}
		m.mu.Unlock()
		if snapshot.ID == "" {
			return app.ReviewSnapshot{}, false, nil
		}
	}
	if errors.Is(err, app.ErrReviewSnapshotNotFound) || snapshot.ID == "" {
		return app.ReviewSnapshot{}, false, nil
	}
	if err != nil {
		return app.ReviewSnapshot{}, false, err
	}
	if snapshot.RepositoryID != request.RepositoryID || request.Target == nil && snapshot.WorktreeID != request.WorktreeID || snapshot.PolicyVersion != request.PolicyVersion || snapshot.EvidenceVersion != request.EvidenceVersion || snapshot.State != app.ReviewSnapshotReady {
		return app.ReviewSnapshot{}, false, app.ErrReviewSnapshotCorrupt
	}
	if request.Target == nil && snapshot.CaptureID != request.CaptureID || request.Target != nil && (snapshot.CaptureID != "" || snapshot.HeadObjectID != request.Target.Head.ObjectID || snapshot.FormatVersion != request.FormatVersion) {
		return app.ReviewSnapshot{}, false, app.ErrReviewSnapshotCorrupt
	}
	if err := m.verifySnapshot(snapshot); err != nil {
		return app.ReviewSnapshot{}, false, app.ErrReviewSnapshotCorrupt
	}
	return snapshot, true, nil
}

func (m *ReviewSnapshotManager) loadSnapshot(ctx context.Context, id domain.ReviewSnapshotID) (app.ReviewSnapshot, error) {
	if m.persist {
		return m.store.LoadReviewSnapshot(ctx, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot, ok := m.snapshots[id]
	if !ok {
		return app.ReviewSnapshot{}, app.ErrReviewSnapshotNotFound
	}
	return snapshot, nil
}

func (m *ReviewSnapshotManager) removeSnapshot(ctx context.Context, snapshot app.ReviewSnapshot, recovery bool) error {
	lock, err := m.acquireSnapshotLock(ctx, snapshotKeyFromSnapshot(snapshot), m.persist)
	if err != nil {
		return err
	}
	defer lock.Close()
	if m.persist {
		count, countErr := m.store.CountReviewSnapshotLeases(ctx, snapshot.ID)
		if countErr != nil {
			return countErr
		}
		if count != 0 {
			return app.ErrReviewSnapshotBusy
		}
	} else {
		m.mu.Lock()
		for _, lease := range m.leases {
			if lease.SnapshotID == snapshot.ID {
				m.mu.Unlock()
				return app.ErrReviewSnapshotBusy
			}
		}
		m.mu.Unlock()
	}
	if err := m.verifySnapshot(snapshot); err != nil && !recovery {
		return app.ErrReviewSnapshotCorrupt
	}
	if err := m.verifyOwnedSnapshotRoot(snapshot.Root); err != nil {
		return app.ErrReviewSnapshotResidue
	}
	marker, err := m.readMarker(snapshot.Root)
	if err != nil || marker.ID != snapshot.ID || marker.CaptureID != snapshot.CaptureID || marker.Nonce != snapshot.MarkerNonce {
		return app.ErrReviewSnapshotResidue
	}
	if err := removeOwnedTree(snapshot.Root); err != nil {
		return err
	}
	if m.persist {
		if err := m.store.DeleteReviewSnapshot(ctx, snapshot.ID); err != nil && !errors.Is(err, app.ErrReviewSnapshotNotFound) {
			return err
		}
	} else {
		m.mu.Lock()
		delete(m.snapshots, snapshot.ID)
		m.mu.Unlock()
	}
	return nil
}

type snapshotMarker struct {
	Version         uint32                    `json:"version"`
	ID              domain.ReviewSnapshotID   `json:"id"`
	CaptureID       domain.CaptureID          `json:"capture_id"`
	RepositoryID    domain.RepositoryID       `json:"repository_id"`
	WorktreeID      domain.WorktreeID         `json:"worktree_id"`
	TargetKind      repository.TargetKind     `json:"target_kind"`
	HeadObjectID    repository.ObjectID       `json:"head_object_id"`
	BaseObjectID    repository.ObjectID       `json:"base_object_id"`
	ParentLabel     string                    `json:"parent_label"`
	ObjectFormat    string                    `json:"object_format"`
	FormatVersion   uint32                    `json:"format_version"`
	Root            string                    `json:"root"`
	Nonce           string                    `json:"nonce"`
	ManifestHash    string                    `json:"manifest_hash"`
	PolicyVersion   app.ResourcePolicyVersion `json:"policy_version"`
	EvidenceVersion app.EvidenceVersion       `json:"evidence_version"`
	State           app.ReviewSnapshotState   `json:"state"`
	CreatedAt       time.Time                 `json:"created_at"`
}

func (m *ReviewSnapshotManager) materialize(ctx context.Context, request app.ReviewSnapshotEnsureRequest, manifest *app.CaptureManifest) (app.ReviewSnapshot, snapshotMarker, string, error) {
	var base repository.LocalCaptureBase
	if request.Target != nil {
		base = repository.LocalCaptureBase{ObjectFormat: request.ObjectFormat, ObjectID: request.Target.Head.ObjectID}
	} else {
		base = manifest.Candidate.Base
	}
	baseEntries, err := m.source.ListBase(ctx, base)
	if err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	if app.Count(len(baseEntries)) > m.limits.MaxEntries {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotLimit
	}
	entryCapacity := len(baseEntries)
	if manifest != nil {
		entryCapacity += len(manifest.Candidate.Entries)
	}
	entries := make(map[string]materializationEntry, entryCapacity)
	for _, entry := range baseEntries {
		if err := entry.Validate(); err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotCorrupt
		}
		if entry.Kind == repository.FileKindUnknown || entry.Kind == repository.FileKindGitlink {
			continue
		}
		path, err := nativeRepoPath(entry.Path)
		if err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", fmt.Errorf("%w: %v", app.ErrReviewSnapshotUnsafe, err)
		}
		key := string(entry.Path.Bytes())
		if _, exists := entries[key]; exists {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotCorrupt
		}
		baseEntry := entry
		entries[key] = materializationEntry{path: entry.Path.Bytes(), nativePath: path, kind: entry.Kind, mode: entry.Mode, base: &baseEntry}
	}
	var captureEntries []repository.LocalCaptureEntry
	if manifest != nil {
		captureEntries = manifest.Candidate.SortedEntries()
	}
	for _, captureEntry := range captureEntries {
		change := captureEntry.Change
		if err := change.Validate(); err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotCorrupt
		}
		if change.OldPath != nil && (change.Kind == repository.ChangeDeleted || change.Kind == repository.ChangeRenamed || change.Kind == repository.ChangeTypeChanged) {
			m.removeMaterialization(entries, string(change.OldPath.Bytes()))
		}
		if change.NewPath == nil {
			continue
		}
		if change.NewFileKind != repository.FileKindRegular && change.NewFileKind != repository.FileKindSymlink {
			continue
		}
		nativePath, pathErr := nativeRepoPath(*change.NewPath)
		if pathErr != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", fmt.Errorf("%w: %v", app.ErrReviewSnapshotUnsafe, pathErr)
		}
		blob, found := workingBlob(captureEntry, *change.NewPath)
		if !found {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotCorrupt
		}
		m.removeMaterialization(entries, string(change.NewPath.Bytes()))
		entries[string(change.NewPath.Bytes())] = materializationEntry{path: change.NewPath.Bytes(), nativePath: nativePath, kind: change.NewFileKind, mode: change.NewMode, capture: &blob}
	}
	if app.Count(len(entries)) > m.limits.MaxEntries {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotLimit
	}
	temporaryRoot, err := m.newTemporaryRoot(request.Persist)
	if err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = removeOwnedTree(temporaryRoot)
		}
	}()
	if err := paths.EnsurePrivateDir(temporaryRoot); err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	expected := make(map[string]snapshotManifestEntry, len(entries))
	var total app.ByteSize
	for _, key := range keys {
		entry := entries[key]
		if entry.kind == repository.FileKindDirectory {
			if err := m.ensureDirectory(temporaryRoot, entry.nativePath); err != nil {
				return app.ReviewSnapshot{}, snapshotMarker{}, "", err
			}
			expected[key] = snapshotManifestEntry{Path: key, Kind: entry.kind, Mode: entry.mode}
			continue
		}
		if err := m.ensureParentDirectories(temporaryRoot, entry.nativePath, expected); err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", err
		}
		var hash string
		var size app.ByteSize
		switch {
		case entry.capture != nil && manifest != nil:
			if entry.kind == repository.FileKindSymlink {
				hash, size, err = m.writeCaptureSymlink(ctx, temporaryRoot, entry, *manifest)
			} else {
				hash, size, err = m.writeCaptureBlob(ctx, temporaryRoot, entry, *manifest)
			}
		case entry.base != nil:
			if entry.kind == repository.FileKindSymlink {
				hash, size, err = m.writeBaseSymlink(ctx, temporaryRoot, entry, base)
			} else {
				hash, size, err = m.writeBaseBlob(ctx, temporaryRoot, entry, base)
			}
		default:
			err = app.ErrReviewSnapshotCorrupt
		}
		if err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", err
		}
		if size > m.limits.MaxBytes || total > m.limits.MaxBytes-size {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotLimit
		}
		total += size
		if entry.kind == repository.FileKindRegular {
			if err := os.Chmod(filepath.Join(temporaryRoot, entry.nativePath), os.FileMode(entry.mode&0o777)); err != nil {
				return app.ReviewSnapshot{}, snapshotMarker{}, "", err
			}
		}
		expected[key] = snapshotManifestEntry{Path: key, Kind: entry.kind, Mode: snapshotLogicalMode(entry.kind, entry.mode), Bytes: uint64(size), SHA256: hash}
	}
	observed, observedHash, err := m.walkManifest(ctx, temporaryRoot)
	if err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	if !sameManifest(expected, observed) {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", app.ErrReviewSnapshotCorrupt
	}
	nonce := m.processNonce
	if request.Persist {
		nonce, err = newNonce()
		if err != nil {
			return app.ReviewSnapshot{}, snapshotMarker{}, "", err
		}
	}
	id, err := domain.NewReviewSnapshotID(m.ids.NewID())
	if err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	created := m.clock.Now().UTC()
	snapshot := app.ReviewSnapshot{ID: id, CaptureID: request.CaptureID, RepositoryID: request.RepositoryID, WorktreeID: request.WorktreeID, Root: temporaryRoot, MarkerNonce: nonce, ManifestHash: observedHash, PolicyVersion: request.PolicyVersion, EvidenceVersion: request.EvidenceVersion, State: app.ReviewSnapshotReady, CreatedAt: created}
	if request.Target != nil {
		snapshot.WorktreeID = ""
		snapshot.TargetKind = request.Target.Spec.Kind
		snapshot.HeadObjectID = request.Target.Head.ObjectID
		snapshot.BaseObjectID = request.Target.Base.ObjectID
		snapshot.ParentLabel = request.Target.ParentLabel
		snapshot.ObjectFormat = request.ObjectFormat
		snapshot.FormatVersion = request.FormatVersion
	}
	if err := snapshot.Validate(); err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	marker := snapshotMarker{Version: reviewSnapshotMarkerVersion, ID: snapshot.ID, CaptureID: snapshot.CaptureID, RepositoryID: snapshot.RepositoryID, WorktreeID: snapshot.WorktreeID, TargetKind: snapshot.TargetKind, HeadObjectID: snapshot.HeadObjectID, BaseObjectID: snapshot.BaseObjectID, ParentLabel: snapshot.ParentLabel, ObjectFormat: snapshot.ObjectFormat, FormatVersion: snapshot.FormatVersion, Root: temporaryRoot, Nonce: snapshot.MarkerNonce, ManifestHash: snapshot.ManifestHash, PolicyVersion: snapshot.PolicyVersion, EvidenceVersion: snapshot.EvidenceVersion, State: snapshot.State, CreatedAt: snapshot.CreatedAt}
	if err := writeMarkerFile(temporaryRoot, marker); err != nil {
		return app.ReviewSnapshot{}, snapshotMarker{}, "", err
	}
	cleanup = false
	return snapshot, marker, temporaryRoot, nil
}

type materializationEntry struct {
	path       []byte
	nativePath string
	kind       repository.FileKind
	mode       uint32
	base       *repository.TreeEntry
	capture    *repository.CaptureBlobRef
}

type snapshotManifestEntry struct {
	Path   string
	Kind   repository.FileKind
	Mode   uint32
	Bytes  uint64
	SHA256 string
}

func workingBlob(entry repository.LocalCaptureEntry, path repository.RepoPath) (repository.CaptureBlobRef, bool) {
	for _, blob := range entry.Blobs {
		if blob.Side == repository.CaptureBlobWorkingTree && bytes.Equal(blob.Path.Bytes(), path.Bytes()) {
			return blob, true
		}
	}
	return repository.CaptureBlobRef{}, false
}

func (m *ReviewSnapshotManager) removeMaterialization(entries map[string]materializationEntry, path string) {
	for key := range entries {
		if key == path || strings.HasPrefix(key, path+"/") {
			delete(entries, key)
		}
	}
}

func (m *ReviewSnapshotManager) writeCaptureBlob(ctx context.Context, root string, entry materializationEntry, manifest app.CaptureManifest) (string, app.ByteSize, error) {
	blob := *entry.capture
	file, err := paths.OpenProtectedFile(root, entry.nativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	var written app.ByteSize
	for written < app.ByteSize(blob.Artifact.Bytes) {
		if err := m.checkContext(ctx); err != nil {
			return "", 0, err
		}
		request := app.ByteSize(blob.Artifact.Bytes) - written
		if request > 256*app.KiB {
			request = 256 * app.KiB
		}
		chunk, readErr := m.captures.ReadBlobRange(ctx, app.CaptureBlobRead{CaptureID: manifest.CaptureID, ManifestHash: manifest.ManifestHash, RelativePath: blob.Artifact.RelativePath, Expected: app.StreamIdentity{Bytes: app.ByteSize(blob.Artifact.Bytes), SHA256: blob.Artifact.ContentSHA256}, Offset: written, MaxBytes: request})
		if readErr != nil {
			return "", 0, readErr
		}
		if len(chunk) == 0 || app.ByteSize(len(chunk)) != request {
			return "", 0, app.ErrReviewSnapshotCorrupt
		}
		if err := m.checkFreeSpace(ctx, root, app.ByteSize(len(chunk))); err != nil {
			return "", 0, err
		}
		if _, err := file.Write(chunk); err != nil {
			return "", 0, err
		}
		_, _ = hash.Write(chunk)
		written += app.ByteSize(len(chunk))
	}
	if written != app.ByteSize(blob.Artifact.Bytes) || hex.EncodeToString(hash.Sum(nil)) != blob.Artifact.ContentSHA256 {
		return "", 0, app.ErrReviewSnapshotCorrupt
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func (m *ReviewSnapshotManager) writeBaseBlob(ctx context.Context, root string, entry materializationEntry, base repository.LocalCaptureBase) (string, app.ByteSize, error) {
	reader, err := m.source.OpenBase(ctx, base, *entry.base)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	file, err := paths.OpenProtectedFile(root, entry.nativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, copyBufferBytes)
	var written app.ByteSize
	for {
		if err := m.checkContext(ctx); err != nil {
			return "", 0, err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if err := m.checkFreeSpace(ctx, root, app.ByteSize(read)); err != nil {
				return "", 0, err
			}
			if written > m.limits.MaxBytes-app.ByteSize(read) {
				return "", 0, app.ErrReviewSnapshotLimit
			}
			if _, err := file.Write(buffer[:read]); err != nil {
				return "", 0, err
			}
			_, _ = hash.Write(buffer[:read])
			written += app.ByteSize(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func (m *ReviewSnapshotManager) writeCaptureSymlink(ctx context.Context, root string, entry materializationEntry, manifest app.CaptureManifest) (string, app.ByteSize, error) {
	blob := *entry.capture
	if app.ByteSize(blob.Artifact.Bytes) > m.policy.Symlink.TrackedBlobBytes {
		return "", 0, app.ErrReviewSnapshotLimit
	}
	data, err := m.readCaptureBytes(ctx, blob, manifest)
	if err != nil {
		return "", 0, err
	}
	if err := m.checkFreeSpace(ctx, root, app.ByteSize(len(data))); err != nil {
		return "", 0, err
	}
	verifiedRoot, rootErr := paths.NewVerifiedRoot(root)
	if rootErr != nil {
		return "", 0, app.ErrReviewSnapshotUnsafe
	}
	evidence, evidenceErr := paths.NewNativePathResolver().QualifySymlinkTarget(verifiedRoot, repository.RepoPath(entry.path), data)
	if evidenceErr != nil || !evidence.IsActionable() {
		return "", 0, app.ErrReviewSnapshotUnsafe
	}
	if err := os.Symlink(string(data), filepath.Join(root, entry.nativePath)); err != nil {
		return "", 0, fmt.Errorf("%w: symlink", app.ErrReviewSnapshotUnsafe)
	}
	return hashBytes(data), app.ByteSize(len(data)), nil
}

func (m *ReviewSnapshotManager) writeBaseSymlink(ctx context.Context, root string, entry materializationEntry, base repository.LocalCaptureBase) (string, app.ByteSize, error) {
	reader, err := m.source.OpenBase(ctx, base, *entry.base)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, int64(m.policy.Symlink.TrackedBlobBytes)+1))
	if err != nil {
		return "", 0, err
	}
	if app.ByteSize(len(data)) > m.policy.Symlink.TrackedBlobBytes {
		return "", 0, app.ErrReviewSnapshotLimit
	}
	if err := m.checkFreeSpace(ctx, root, app.ByteSize(len(data))); err != nil {
		return "", 0, err
	}
	verifiedRoot, rootErr := paths.NewVerifiedRoot(root)
	if rootErr != nil {
		return "", 0, app.ErrReviewSnapshotUnsafe
	}
	evidence, evidenceErr := paths.NewNativePathResolver().QualifySymlinkTarget(verifiedRoot, repository.RepoPath(entry.path), data)
	if evidenceErr != nil || !evidence.IsActionable() {
		return "", 0, app.ErrReviewSnapshotUnsafe
	}
	if err := os.Symlink(string(data), filepath.Join(root, entry.nativePath)); err != nil {
		return "", 0, fmt.Errorf("%w: symlink", app.ErrReviewSnapshotUnsafe)
	}
	return hashBytes(data), app.ByteSize(len(data)), nil
}

func (m *ReviewSnapshotManager) readCaptureBytes(ctx context.Context, blob repository.CaptureBlobRef, manifest app.CaptureManifest) ([]byte, error) {
	data := make([]byte, 0, int(blob.Artifact.Bytes))
	var offset app.ByteSize
	for offset < app.ByteSize(blob.Artifact.Bytes) {
		if err := m.checkContext(ctx); err != nil {
			return nil, err
		}
		request := app.ByteSize(blob.Artifact.Bytes) - offset
		if request > 256*app.KiB {
			request = 256 * app.KiB
		}
		chunk, err := m.captures.ReadBlobRange(ctx, app.CaptureBlobRead{CaptureID: manifest.CaptureID, ManifestHash: manifest.ManifestHash, RelativePath: blob.Artifact.RelativePath, Expected: app.StreamIdentity{Bytes: app.ByteSize(blob.Artifact.Bytes), SHA256: blob.Artifact.ContentSHA256}, Offset: offset, MaxBytes: request})
		if err != nil {
			return nil, err
		}
		if app.ByteSize(len(chunk)) != request {
			return nil, app.ErrReviewSnapshotCorrupt
		}
		data = append(data, chunk...)
		offset += app.ByteSize(len(chunk))
	}
	if hashBytes(data) != blob.Artifact.ContentSHA256 {
		return nil, app.ErrReviewSnapshotCorrupt
	}
	return data, nil
}

func (m *ReviewSnapshotManager) ensureParentDirectories(root, nativePath string, expected map[string]snapshotManifestEntry) error {
	parent := filepath.Dir(nativePath)
	if parent == "." || parent == "" {
		return nil
	}
	parts := strings.Split(filepath.ToSlash(parent), "/")
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errReviewSnapshotPath
		}
		if current != "" {
			current += string(os.PathSeparator)
		}
		current += part
		if err := m.ensureDirectory(root, current); err != nil {
			return err
		}
		key := filepath.ToSlash(current)
		if _, ok := expected[key]; !ok {
			expected[key] = snapshotManifestEntry{Path: key, Kind: repository.FileKindDirectory, Mode: 0o40000}
		}
	}
	return nil
}

func (m *ReviewSnapshotManager) ensureDirectory(root, relative string) error {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || strings.Contains(relative, ".."+string(os.PathSeparator)) {
		return errReviewSnapshotPath
	}
	clean := filepath.Clean(relative)
	parts := strings.Split(filepath.ToSlash(clean), "/")
	current := root
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errReviewSnapshotPath
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: directory alias", app.ErrReviewSnapshotUnsafe)
		}
	}
	return nil
}

func (m *ReviewSnapshotManager) walkManifest(ctx context.Context, root string) (map[string]snapshotManifestEntry, string, error) {
	entries := make(map[string]snapshotManifestEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := m.checkContext(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || !containedPath(root, path) {
			return errReviewSnapshotPath
		}
		key := filepath.ToSlash(relative)
		if key == reviewSnapshotMarkerName {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := snapshotManifestEntry{Path: key}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = repository.FileKindSymlink
			item.Mode = snapshotLogicalMode(repository.FileKindSymlink, uint32(info.Mode().Perm()))
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			item.Bytes = uint64(len([]byte(target)))
			item.SHA256 = hashBytes([]byte(target))
		case info.IsDir():
			item.Kind = repository.FileKindDirectory
			item.Mode = 0o40000
		default:
			item.Kind = repository.FileKindRegular
			item.Mode = snapshotLogicalMode(repository.FileKindRegular, uint32(info.Mode().Perm()))
			file, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			hash := sha256.New()
			buffer := make([]byte, copyBufferBytes)
			for {
				read, readErr := file.Read(buffer)
				if read > 0 {
					item.Bytes += uint64(read)
					if app.ByteSize(item.Bytes) > m.limits.MaxBytes {
						_ = file.Close()
						return app.ErrReviewSnapshotLimit
					}
					_, _ = hash.Write(buffer[:read])
				}
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					_ = file.Close()
					return readErr
				}
			}
			if err := file.Close(); err != nil {
				return err
			}
			item.SHA256 = hex.EncodeToString(hash.Sum(nil))
		}
		if app.Count(len(entries)) >= m.limits.MaxEntries {
			return app.ErrReviewSnapshotLimit
		}
		entries[key] = item
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return entries, manifestHash(entries), nil
}

func sameManifest(left, right map[string]snapshotManifestEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if value != right[key] {
			return false
		}
	}
	return true
}

func manifestHash(entries map[string]snapshotManifestEntry) string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		value := entries[key]
		writeHashString(hash, value.Path)
		writeHashString(hash, string(value.Kind))
		writeHashUint(hash, uint64(value.Mode))
		writeHashUint(hash, value.Bytes)
		writeHashString(hash, value.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeHashString(writer io.Writer, value string) {
	var size [8]byte
	encoded := uint64(len(value))
	for index := range size {
		size[len(size)-1-index] = byte(encoded >> (index * 8))
	}
	_, _ = writer.Write(size[:])
	_, _ = io.WriteString(writer, value)
}

func writeHashUint(writer io.Writer, value uint64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-1-index] = byte(value >> (index * 8))
	}
	_, _ = writer.Write(encoded[:])
}

func (m *ReviewSnapshotManager) verifySnapshot(snapshot app.ReviewSnapshot) error {
	if snapshot.Validate() != nil || m.verifyOwnedSnapshotRoot(snapshot.Root) != nil {
		return app.ErrReviewSnapshotCorrupt
	}
	marker, err := m.readMarker(snapshot.Root)
	if err != nil || marker.ID != snapshot.ID || marker.CaptureID != snapshot.CaptureID || marker.RepositoryID != snapshot.RepositoryID || marker.WorktreeID != snapshot.WorktreeID || marker.TargetKind != snapshot.TargetKind || marker.HeadObjectID != snapshot.HeadObjectID || marker.BaseObjectID != snapshot.BaseObjectID || marker.ParentLabel != snapshot.ParentLabel || marker.ObjectFormat != snapshot.ObjectFormat || marker.FormatVersion != snapshot.FormatVersion || marker.Root != snapshot.Root || marker.Nonce != snapshot.MarkerNonce || marker.ManifestHash != snapshot.ManifestHash || marker.PolicyVersion != snapshot.PolicyVersion || marker.EvidenceVersion != snapshot.EvidenceVersion || marker.State != app.ReviewSnapshotReady {
		return app.ErrReviewSnapshotCorrupt
	}
	_, hash, err := m.walkManifest(context.Background(), snapshot.Root)
	if err != nil || hash != snapshot.ManifestHash {
		return app.ErrReviewSnapshotCorrupt
	}
	return nil
}

func (m *ReviewSnapshotManager) verifyOwnedSnapshotRoot(root string) error {
	parent := m.published
	if !m.persist {
		parent = m.ephemeral
	}
	return verifyOwnedRoot(parent, root)
}

func (m *ReviewSnapshotManager) findEphemeralSnapshot(id domain.ReviewSnapshotID) (app.ReviewSnapshot, error) {
	entries, err := os.ReadDir(m.ephemeral)
	if err != nil {
		return app.ReviewSnapshot{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(m.ephemeral, entry.Name())
		marker, markerErr := m.readMarker(root)
		if markerErr != nil || marker.ID != id {
			continue
		}
		return app.ReviewSnapshot{ID: marker.ID, CaptureID: marker.CaptureID, RepositoryID: marker.RepositoryID, WorktreeID: marker.WorktreeID, TargetKind: marker.TargetKind, HeadObjectID: marker.HeadObjectID, BaseObjectID: marker.BaseObjectID, ParentLabel: marker.ParentLabel, ObjectFormat: marker.ObjectFormat, FormatVersion: marker.FormatVersion, Root: root, MarkerNonce: marker.Nonce, ManifestHash: marker.ManifestHash, PolicyVersion: marker.PolicyVersion, EvidenceVersion: marker.EvidenceVersion, State: marker.State, CreatedAt: marker.CreatedAt}, nil
	}
	return app.ReviewSnapshot{}, app.ErrReviewSnapshotNotFound
}

func (m *ReviewSnapshotManager) makeReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		mode := info.Mode().Perm() &^ 0o222
		if info.IsDir() {
			mode |= 0o500
		}
		return os.Chmod(path, mode)
	})
}

func (m *ReviewSnapshotManager) writeMarker(root string, marker snapshotMarker) error {
	data, err := marshalMarker(marker)
	if err != nil {
		return err
	}
	file, err := paths.OpenExistingProtectedFileForUpdate(root, reviewSnapshotMarkerName, os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeMarkerFile(root string, marker snapshotMarker) error {
	data, err := marshalMarker(marker)
	if err != nil {
		return err
	}
	file, err := paths.OpenProtectedFile(root, reviewSnapshotMarkerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func marshalMarker(marker snapshotMarker) ([]byte, error) {
	if marker.Version != reviewSnapshotMarkerVersion || marker.ID == "" || marker.Root == "" || !validNonce(marker.Nonce) || !validHash(marker.ManifestHash) || marker.State != app.ReviewSnapshotReady || marker.CaptureID == "" && (marker.TargetKind != repository.TargetBranch && marker.TargetKind != repository.TargetCommit) {
		return nil, app.ErrInvalidReviewSnapshot
	}
	data, err := json.Marshal(marker)
	if err != nil || len(data) > maxMarkerBytes {
		return nil, app.ErrInvalidReviewSnapshot
	}
	return data, nil
}

func (m *ReviewSnapshotManager) readMarker(root string) (snapshotMarker, error) {
	data, err := paths.ReadProtectedFileBounded(root, reviewSnapshotMarkerName, maxMarkerBytes)
	if err != nil {
		return snapshotMarker{}, err
	}
	var marker snapshotMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.Version != reviewSnapshotMarkerVersion || marker.State != app.ReviewSnapshotReady || !validNonce(marker.Nonce) || !validHash(marker.ManifestHash) || marker.Root != root {
		return snapshotMarker{}, app.ErrReviewSnapshotCorrupt
	}
	return marker, nil
}

func (m *ReviewSnapshotManager) acquireSnapshotLock(ctx context.Context, key string, persist bool) (*filelock.Lock, error) {
	key = m.snapshotRootForKey(key, persist)
	digest := sha256.Sum256([]byte(key))
	return filelock.Acquire(ctx, filepath.Join(m.locks, hex.EncodeToString(digest[:])+".lock"))
}

func (m *ReviewSnapshotManager) newTemporaryRoot(persist bool) (string, error) {
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	parent := m.temporary
	if !persist {
		parent = m.ephemeral
	}
	root := filepath.Join(parent, nonce)
	if err := paths.EnsurePrivateDir(root); err != nil {
		return "", err
	}
	return root, nil
}

func (m *ReviewSnapshotManager) snapshotRoot(request app.ReviewSnapshotEnsureRequest) string {
	return m.snapshotRootForKey(m.snapshotKey(request), request.Persist)
}

func (m *ReviewSnapshotManager) snapshotRootForKey(key string, persist bool) string {
	digest := sha256.Sum256([]byte(key))
	parent := m.published
	if !persist {
		parent = m.ephemeral
	}
	return filepath.Join(parent, hex.EncodeToString(digest[:]))
}

func (m *ReviewSnapshotManager) snapshotKey(request app.ReviewSnapshotEnsureRequest) string {
	if request.Target != nil {
		return fmt.Sprintf("object:%s:%s:%d:%d", request.RepositoryID, request.Target.Head.ObjectID, request.PolicyVersion, request.FormatVersion)
	}
	return fmt.Sprintf("capture:%s:%d", request.CaptureID, request.PolicyVersion)
}

func snapshotKeyFromSnapshot(snapshot app.ReviewSnapshot) string {
	if snapshot.CaptureID == "" {
		return fmt.Sprintf("object:%s:%s:%d:%d", snapshot.RepositoryID, snapshot.HeadObjectID, snapshot.PolicyVersion, snapshot.FormatVersion)
	}
	return fmt.Sprintf("capture:%s:%d", snapshot.CaptureID, snapshot.PolicyVersion)
}

func snapshotSourceRef(snapshot app.ReviewSnapshot) string {
	if snapshot.CaptureID != "" {
		return ""
	}
	return fmt.Sprintf("target:%s:head:%s:base:%s:parent:%s", snapshot.TargetKind, snapshot.HeadObjectID, snapshot.BaseObjectID, snapshot.ParentLabel)
}

func (m *ReviewSnapshotManager) checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return app.ErrReviewSnapshotLimit
		}
		return ctx.Err()
	default:
		return nil
	}
}

func (m *ReviewSnapshotManager) checkFreeSpace(ctx context.Context, root string, additional app.ByteSize) error {
	free, err := m.freeSpace(ctx, root)
	if err != nil {
		return err
	}
	required, err := m.limits.MinimumFreeByte.Add(additional)
	if err != nil || free < required {
		return app.ErrReviewSnapshotLimit
	}
	return nil
}

func nativeRepoPath(path repository.RepoPath) (string, error) {
	if err := path.Validate(); err != nil {
		return "", errReviewSnapshotPath
	}
	raw := path.Bytes()
	if bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\\') >= 0 || !utf8.Valid(raw) {
		return "", errReviewSnapshotPath
	}
	parts := strings.Split(string(raw), "/")
	if len(parts) == 0 {
		return "", errReviewSnapshotPath
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errReviewSnapshotPath
		}
	}
	result := filepath.FromSlash(string(raw))
	if filepath.IsAbs(result) || filepath.VolumeName(result) != "" || filepath.Clean(result) != result {
		return "", errReviewSnapshotPath
	}
	return result, nil
}

func verifyOwnedRoot(parent, root string) error {
	if parent == "" || root == "" || !filepath.IsAbs(parent) || !filepath.IsAbs(root) || filepath.Clean(parent) != parent || filepath.Clean(root) != root || !containedPath(parent, root) || filepath.Clean(root) == filepath.Clean(parent) {
		return app.ErrReviewSnapshotResidue
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if !containedPath(canonicalParent, canonicalRoot) || filepath.Clean(canonicalParent) == filepath.Clean(canonicalRoot) {
		return app.ErrReviewSnapshotResidue
	}
	return nil
}

func removeOwnedTree(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return app.ErrReviewSnapshotResidue
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	return removeOwnedTreeEntry(root)
}

func removeOwnedTreeEntry(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		info, err := os.Lstat(child)
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := removeOwnedTreeEntry(child); err != nil {
				return err
			}
			if err := os.Remove(child); err != nil {
				return err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if err := os.Chmod(child, 0o600); err != nil {
				return err
			}
		}
		if err := os.Remove(child); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func containedPath(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false
	}
	return relative != ""
}

func hashBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func snapshotLogicalMode(kind repository.FileKind, mode uint32) uint32 {
	switch kind {
	case repository.FileKindDirectory:
		return 0o40000
	case repository.FileKindSymlink:
		return 0o120000
	case repository.FileKindRegular:
		if mode&0o111 != 0 {
			return 0o100755
		}
		return 0o100644
	default:
		return mode
	}
}

func newNonce() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validNonce(value string) bool { return validHash(value) }

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
