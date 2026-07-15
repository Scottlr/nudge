package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

const (
	workspaceMarkerName              = "workspace.marker"
	workspaceLockDirectory           = "locks"
	workspaceMarkerVersion    uint32 = 1
	workspaceIsolationVersion uint32 = 1
)

var (
	ErrInvalidWorkspaceRequest = errors.New("invalid proposal workspace request")
	ErrWorkspaceRepairRequired = errors.New("proposal workspace requires repair")
	ErrWorkspaceMarkerMissing  = errors.New("proposal workspace marker missing")
	ErrWorkspaceMarkerMismatch = errors.New("proposal workspace marker mismatch")
	ErrWorkspaceRootMismatch   = errors.New("proposal workspace root mismatch")
	ErrWorkspaceExists         = errors.New("proposal workspace already exists")
)

// RootKind identifies one of the four non-interchangeable workspace roots.
type RootKind string

const (
	RootBaseline    RootKind = "baseline"
	RootAdmin       RootKind = "admin"
	RootResult      RootKind = "result"
	RootDestination RootKind = "destination"
	RootParent      RootKind = "workspace_parent"
)

func (k RootKind) Validate() error {
	switch k {
	case RootBaseline, RootAdmin, RootResult, RootDestination, RootParent:
		return nil
	default:
		return ErrInvalidWorkspaceRequest
	}
}

// RootIdentity is the persisted evidence for one canonical directory. Path
// and canonical path are retained separately so a later inspection can detect
// an alias even if a user supplied a path that resolves elsewhere.
type RootIdentity struct {
	Kind           RootKind                  `json:"kind"`
	Path           string                    `json:"path"`
	CanonicalPath  string                    `json:"canonical_path"`
	NativeIdentity repository.NativeIdentity `json:"native_identity"`
}

// MarshalJSON encodes native identity bytes without allowing JSON's invalid
// UTF-8 replacement behavior to change the persisted native identity.
func (r RootIdentity) MarshalJSON() ([]byte, error) {
	type encoded struct {
		Kind           RootKind `json:"kind"`
		Path           string   `json:"path"`
		CanonicalPath  string   `json:"canonical_path"`
		NativeIdentity string   `json:"native_identity"`
	}
	return json.Marshal(encoded{Kind: r.Kind, Path: r.Path, CanonicalPath: r.CanonicalPath, NativeIdentity: base64.StdEncoding.EncodeToString([]byte(r.NativeIdentity))})
}

func (r *RootIdentity) UnmarshalJSON(data []byte) error {
	type encoded struct {
		Kind           RootKind `json:"kind"`
		Path           string   `json:"path"`
		CanonicalPath  string   `json:"canonical_path"`
		NativeIdentity string   `json:"native_identity"`
	}
	var value encoded
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	native, err := base64.StdEncoding.DecodeString(value.NativeIdentity)
	if err != nil {
		return err
	}
	r.Kind, r.Path, r.CanonicalPath, r.NativeIdentity = value.Kind, value.Path, value.CanonicalPath, repository.NativeIdentity(native)
	return nil
}

func (r RootIdentity) Validate(requireNative bool) error {
	if r.Kind.Validate() != nil || !validAbsoluteCleanPath(r.Path) || !validAbsoluteCleanPath(r.CanonicalPath) || r.Path != r.CanonicalPath || requireNative && r.NativeIdentity == "" {
		return ErrInvalidWorkspaceRequest
	}
	return nil
}

// RootSet is the exact four-root identity stored in the marker and durable
// creation record. Destination is an identity only; it is never created here.
type RootSet struct {
	Baseline    RootIdentity `json:"baseline"`
	Admin       RootIdentity `json:"admin"`
	Result      RootIdentity `json:"result"`
	Destination RootIdentity `json:"destination"`
}

func (r RootSet) Validate(requireNative bool) error {
	if r.Baseline.Kind != RootBaseline || r.Admin.Kind != RootAdmin || r.Result.Kind != RootResult || r.Destination.Kind != RootDestination || r.Baseline.Validate(requireNative) != nil || r.Admin.Validate(requireNative) != nil || r.Result.Validate(requireNative) != nil || r.Destination.Validate(true) != nil {
		return ErrInvalidWorkspaceRequest
	}
	pathsToCompare := []string{r.Baseline.CanonicalPath, r.Admin.CanonicalPath, r.Result.CanonicalPath, r.Destination.CanonicalPath}
	for left := range pathsToCompare {
		for right := left + 1; right < len(pathsToCompare); right++ {
			if sameOrDescendant(pathsToCompare[left], pathsToCompare[right]) || sameOrDescendant(pathsToCompare[right], pathsToCompare[left]) {
				return ErrWorkspaceRootMismatch
			}
		}
	}
	return nil
}

// WorkspaceRoot is a typed read capability for one verified root. It exposes
// no recursive delete or arbitrary path mutation authority.
type WorkspaceRoot struct{ identity RootIdentity }

func (r WorkspaceRoot) Kind() RootKind                            { return r.identity.Kind }
func (r WorkspaceRoot) Path() string                              { return r.identity.CanonicalPath }
func (r WorkspaceRoot) NativeIdentity() repository.NativeIdentity { return r.identity.NativeIdentity }

// ProposalWorkspaceRoots is the immutable root view held by a verified
// workspace handle.
type ProposalWorkspaceRoots struct {
	Baseline    WorkspaceRoot
	Admin       WorkspaceRoot
	Result      WorkspaceRoot
	Destination WorkspaceRoot
}

func newWorkspaceRoots(set RootSet) ProposalWorkspaceRoots {
	return ProposalWorkspaceRoots{
		Baseline: WorkspaceRoot{identity: set.Baseline}, Admin: WorkspaceRoot{identity: set.Admin},
		Result: WorkspaceRoot{identity: set.Result}, Destination: WorkspaceRoot{identity: set.Destination},
	}
}

// WorkspaceCreationPhase separates filesystem creation evidence from the
// review-domain workspace state.
type WorkspaceCreationPhase string

const (
	WorkspacePlanned      WorkspaceCreationPhase = "planned"
	WorkspaceCreating     WorkspaceCreationPhase = "creating"
	WorkspaceRootsCreated WorkspaceCreationPhase = "roots_created"
	WorkspaceVerified     WorkspaceCreationPhase = "verified"
	WorkspaceRepair       WorkspaceCreationPhase = "repair_required"
)

func (p WorkspaceCreationPhase) Validate() error {
	switch p {
	case WorkspacePlanned, WorkspaceCreating, WorkspaceRootsCreated, WorkspaceVerified, WorkspaceRepair:
		return nil
	default:
		return ErrInvalidWorkspaceRequest
	}
}

func (p WorkspaceCreationPhase) CanTransitionTo(next WorkspaceCreationPhase) bool {
	if p == next {
		return p != WorkspaceVerified && p != WorkspaceRepair
	}
	switch p {
	case WorkspacePlanned:
		return next == WorkspaceCreating || next == WorkspaceRepair
	case WorkspaceCreating:
		return next == WorkspaceRootsCreated || next == WorkspaceRepair
	case WorkspaceRootsCreated:
		return next == WorkspaceVerified || next == WorkspaceRepair
	case WorkspaceVerified:
		return next == WorkspaceRepair
	case WorkspaceRepair:
		return false
	default:
		return false
	}
}

// WorkspaceCreationEvidence is the durable, crash-recovery record. It is
// intentionally separate from proposal bytes and can classify partial roots
// without guessing ownership.
type WorkspaceCreationEvidence struct {
	WorkspaceID               domain.WorkspaceID     `json:"workspace_id"`
	RepositoryID              domain.RepositoryID    `json:"repository_id"`
	WorktreeID                domain.WorktreeID      `json:"worktree_id"`
	ThreadID                  domain.ReviewThreadID  `json:"thread_id"`
	OperationID               domain.OperationID     `json:"operation_id"`
	CapacityReservationMarker string                 `json:"capacity_reservation_marker"`
	Nonce                     string                 `json:"nonce"`
	Parent                    RootIdentity           `json:"parent"`
	Roots                     RootSet                `json:"roots"`
	MarkerVersion             uint32                 `json:"marker_version"`
	IsolationVersion          uint32                 `json:"isolation_version"`
	Phase                     WorkspaceCreationPhase `json:"phase"`
	MarkerSHA256              string                 `json:"marker_sha256"`
	CreatedAt                 time.Time              `json:"created_at"`
	UpdatedAt                 time.Time              `json:"updated_at"`
}

func (e WorkspaceCreationEvidence) Validate() error {
	if e.WorkspaceID == "" || e.RepositoryID == "" || e.WorktreeID == "" || e.ThreadID == "" || e.OperationID == "" || e.CapacityReservationMarker == "" || !validWorkspaceNonce(e.Nonce) || e.Parent.Kind != RootParent || e.Parent.Validate(true) != nil || e.MarkerVersion == 0 || e.IsolationVersion == 0 || e.Phase.Validate() != nil || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() || e.UpdatedAt.Before(e.CreatedAt) {
		return ErrInvalidWorkspaceRequest
	}
	requireNative := e.Phase == WorkspaceRootsCreated || e.Phase == WorkspaceVerified
	if e.Roots.Validate(requireNative) != nil {
		return ErrInvalidWorkspaceRequest
	}
	if e.Phase == WorkspaceRootsCreated || e.Phase == WorkspaceVerified {
		if !validSHA256(e.MarkerSHA256) {
			return ErrInvalidWorkspaceRequest
		}
	} else if e.MarkerSHA256 != "" {
		return ErrInvalidWorkspaceRequest
	}
	return nil
}

// WorkspaceMarker is the ownership proof written below the allocated Nudge
// workspace root. It deliberately excludes its own hash.
type WorkspaceMarker struct {
	WorkspaceID               domain.WorkspaceID    `json:"workspace_id"`
	RepositoryID              domain.RepositoryID   `json:"repository_id"`
	WorktreeID                domain.WorktreeID     `json:"worktree_id"`
	ThreadID                  domain.ReviewThreadID `json:"thread_id"`
	OperationID               domain.OperationID    `json:"operation_id"`
	CapacityReservationMarker string                `json:"capacity_reservation_marker"`
	Nonce                     string                `json:"nonce"`
	Parent                    RootIdentity          `json:"parent"`
	Roots                     RootSet               `json:"roots"`
	MarkerVersion             uint32                `json:"marker_version"`
	IsolationVersion          uint32                `json:"isolation_version"`
	CreatedAt                 time.Time             `json:"created_at"`
}

func (m WorkspaceMarker) Validate(requireNative bool) error {
	if m.WorkspaceID == "" || m.RepositoryID == "" || m.WorktreeID == "" || m.ThreadID == "" || m.OperationID == "" || m.CapacityReservationMarker == "" || !validWorkspaceNonce(m.Nonce) || m.Parent.Kind != RootParent || m.Parent.Validate(true) != nil || m.MarkerVersion == 0 || m.IsolationVersion == 0 || m.CreatedAt.IsZero() || m.Roots.Validate(requireNative) != nil {
		return ErrInvalidWorkspaceRequest
	}
	return nil
}

// WorkspaceHandle is immutable verified identity. It contains only typed
// roots and IDs; filesystem effects remain owned by later lifecycle tasks.
type WorkspaceHandle struct {
	WorkspaceID      domain.WorkspaceID
	RepositoryID     domain.RepositoryID
	WorktreeID       domain.WorktreeID
	ThreadID         domain.ReviewThreadID
	OperationID      domain.OperationID
	Nonce            string
	MarkerVersion    uint32
	IsolationVersion uint32
	Roots            ProposalWorkspaceRoots
}

// WorkspaceLease owns the native workspace lock for the handle lifetime.
type WorkspaceLease struct {
	handle      WorkspaceHandle
	lock        *filelock.Lock
	reservation app.CapacityReservation
	once        sync.Once
	err         error
}

func (l *WorkspaceLease) Handle() WorkspaceHandle {
	if l == nil {
		return WorkspaceHandle{}
	}
	return l.handle
}

// Reservation returns the opaque capacity token retained for this workspace.
// Closing the workspace lock does not release it; lifecycle cleanup owns that
// decision after durable workspace retirement.
func (l *WorkspaceLease) Reservation() app.CapacityReservation {
	if l == nil {
		return app.CapacityReservation{}
	}
	return l.reservation
}

// Close releases the workspace lock. It is safe to call repeatedly.
func (l *WorkspaceLease) Close() error {
	if l == nil || l.lock == nil {
		return filelock.ErrClosed
	}
	l.once.Do(func() { l.err = l.lock.Close() })
	return l.err
}

// SessionTransactionStore is the narrow transaction seam needed by workspace
// creation. The concrete SQLite store also implements app.ReviewStore.
type SessionTransactionStore interface {
	WithSessionTx(context.Context, app.SessionWriteGuard, func(app.ReviewStoreTx) error) (app.SessionWriteGuard, error)
}

// WorkspaceStoreTx persists creation evidence in the same fenced session
// transaction as proposal creation and later phase changes.
type WorkspaceStoreTx interface {
	CreateWorkspaceCreation(context.Context, WorkspaceCreationEvidence) error
	UpdateWorkspaceCreation(context.Context, WorkspaceCreationEvidence) error
}

// WorkspaceStore loads durable creation evidence for exact recovery.
type WorkspaceStore interface {
	LoadWorkspaceCreation(context.Context, domain.WorkspaceID) (WorkspaceCreationEvidence, error)
}

// CreateRequest supplies the already-confirmed proposal lineage and external
// destination identity. This task creates roots only; it does not install
// baseline content, initialize Git, or grant provider permissions.
type CreateRequest struct {
	Store            SessionTransactionStore
	Capacity         app.CapacityReservationPort
	CapacityPlan     app.CapacityPlan
	CapacityPolicy   app.ResourcePolicy
	CapacityEvidence []app.VolumeEvidence
	Guard            app.SessionWriteGuard
	Workspace        review.ProposalWorkspace
	Intent           review.ProposalIntent
	Proposal         review.Proposal
	DestinationPath  string
	OperationID      domain.OperationID
	MarkerVersion    uint32
	IsolationVersion uint32
	Now              time.Time
}

// ResumeRequest identifies the exact persisted creation operation to resume.
type ResumeRequest struct {
	Store       SessionTransactionStore
	Guard       app.SessionWriteGuard
	WorkspaceID domain.WorkspaceID
	OperationID domain.OperationID
	Nonce       string
	Reservation app.CapacityReservation
}

// WorkspaceInspection is a read-only recovery classification plus verified
// evidence. It never removes or recreates a root.
type WorkspaceInspection struct {
	Evidence WorkspaceCreationEvidence
	Handle   *WorkspaceHandle
	Phase    WorkspaceCreationPhase
}

// Allocator owns only the direct workspace allocation parent. It never accepts
// arbitrary recursive paths from consumers.
type Allocator struct{ root string }

func NewAllocator(root string) (*Allocator, error) {
	if !validAbsoluteCleanPath(root) {
		return nil, ErrInvalidWorkspaceRequest
	}
	if err := paths.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || !validAbsoluteCleanPath(filepath.Clean(canonical)) {
		return nil, ErrWorkspaceRepairRequired
	}
	return &Allocator{root: filepath.Clean(canonical)}, nil
}

// Create records creating intent before making any filesystem mutation, then
// creates and verifies the three Nudge-owned roots and marker.
func (a *Allocator) Create(ctx context.Context, request CreateRequest) (*WorkspaceLease, app.SessionWriteGuard, error) {
	if a == nil || ctx == nil {
		return nil, request.Guard, ErrInvalidWorkspaceRequest
	}
	prepared, evidence, workspaceDir, err := a.prepareRequest(request)
	if err != nil {
		return nil, request.Guard, err
	}
	reservation, err := request.Capacity.Reserve(ctx, request.CapacityPlan, request.CapacityPolicy, request.CapacityEvidence)
	if err != nil {
		return nil, request.Guard, err
	}
	evidence.CapacityReservationMarker = reservation.Marker()
	if evidence.CapacityReservationMarker == "" {
		_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		return nil, request.Guard, ErrInvalidWorkspaceRequest
	}
	reserved := true
	durableCreated := false
	defer func() {
		if reserved && !durableCreated {
			_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		}
	}()
	lock, err := a.acquireWorkspaceLock(ctx, prepared.Workspace.ID)
	if err != nil {
		return nil, request.Guard, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = lock.Close()
		}
	}()
	if _, err := os.Lstat(workspaceDir); err == nil {
		return nil, request.Guard, ErrWorkspaceExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, request.Guard, err
	}
	guard, err := persistWorkspaceCreate(ctx, prepared, evidence)
	if err != nil {
		return nil, request.Guard, err
	}
	durableCreated = true
	evidence.Phase = WorkspaceCreating
	evidence.UpdatedAt = time.Now().UTC()
	guard, err = persistWorkspaceUpdate(ctx, prepared.Store, guard, evidence)
	if err != nil {
		return nil, guard, err
	}
	updatedEvidence, err := a.createFilesystem(workspaceDir, evidence)
	if err != nil {
		return nil, guard, err
	}
	updated, nextGuard, err := a.verifyAndPersist(ctx, prepared.Store, guard, updatedEvidence, workspaceDir)
	if err != nil {
		return nil, nextGuard, err
	}
	keep = true
	reserved = false
	return &WorkspaceLease{handle: handleFromEvidence(updated), lock: lock, reservation: reservation}, nextGuard, nil
}

// Resume continues only the exact operation recorded in durable evidence.
func (a *Allocator) Resume(ctx context.Context, request ResumeRequest) (*WorkspaceLease, app.SessionWriteGuard, error) {
	if a == nil || ctx == nil || request.Store == nil || request.Guard.Validate() != nil || request.WorkspaceID == "" || request.OperationID == "" || !validWorkspaceNonce(request.Nonce) || request.Reservation.Marker() == "" {
		return nil, request.Guard, ErrInvalidWorkspaceRequest
	}
	loader, ok := request.Store.(WorkspaceStore)
	if !ok {
		return nil, request.Guard, ErrInvalidWorkspaceRequest
	}
	evidence, err := loader.LoadWorkspaceCreation(ctx, request.WorkspaceID)
	if err != nil {
		return nil, request.Guard, err
	}
	if evidence.WorkspaceID != request.WorkspaceID || evidence.OperationID != request.OperationID || evidence.Nonce != request.Nonce || evidence.CapacityReservationMarker != request.Reservation.Marker() {
		return nil, request.Guard, ErrWorkspaceMarkerMismatch
	}
	workspaceDir := filepath.Dir(evidence.Roots.Baseline.Path)
	if filepath.Base(workspaceDir) != string(evidence.WorkspaceID) || filepath.Dir(workspaceDir) != a.root {
		return nil, request.Guard, ErrWorkspaceRootMismatch
	}
	lock, err := a.acquireWorkspaceLock(ctx, evidence.WorkspaceID)
	if err != nil {
		return nil, request.Guard, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = lock.Close()
		}
	}()
	if evidence.Phase == WorkspaceRepair {
		return nil, request.Guard, ErrWorkspaceRepairRequired
	}
	if evidence.Phase == WorkspacePlanned {
		if _, err := os.Lstat(workspaceDir); err == nil {
			return nil, request.Guard, ErrWorkspaceRepairRequired
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, request.Guard, err
		}
		evidence.Phase = WorkspaceCreating
		evidence.UpdatedAt = time.Now().UTC()
		request.Guard, err = persistWorkspaceUpdate(ctx, request.Store, request.Guard, evidence)
		if err != nil {
			return nil, request.Guard, err
		}
	}
	if evidence.Phase == WorkspaceCreating {
		evidence, err = a.resumeFilesystem(workspaceDir, evidence)
		if err != nil {
			return nil, request.Guard, err
		}
	}
	updated, guard, err := a.verifyAndPersist(ctx, request.Store, request.Guard, evidence, workspaceDir)
	if err != nil {
		return nil, guard, err
	}
	keep = true
	return &WorkspaceLease{handle: handleFromEvidence(updated), lock: lock, reservation: request.Reservation}, guard, nil
}

// Inspect acquires the workspace lock and returns evidence without mutating
// files or durable state. Ambiguous ownership is repair-required.
func (a *Allocator) Inspect(ctx context.Context, store WorkspaceStore, workspaceID domain.WorkspaceID) (WorkspaceInspection, error) {
	if a == nil || ctx == nil || store == nil || workspaceID == "" {
		return WorkspaceInspection{}, ErrInvalidWorkspaceRequest
	}
	evidence, err := store.LoadWorkspaceCreation(ctx, workspaceID)
	if err != nil {
		return WorkspaceInspection{}, err
	}
	lock, err := a.acquireWorkspaceLock(ctx, workspaceID)
	if err != nil {
		return WorkspaceInspection{}, err
	}
	defer lock.Close()
	workspaceDir := filepath.Dir(evidence.Roots.Baseline.Path)
	if evidence.Phase == WorkspaceVerified {
		if err := a.verifyFilesystem(workspaceDir, evidence, true); err != nil {
			return WorkspaceInspection{Evidence: evidence, Phase: WorkspaceRepair}, ErrWorkspaceRepairRequired
		}
		handle := handleFromEvidence(evidence)
		return WorkspaceInspection{Evidence: evidence, Handle: &handle, Phase: evidence.Phase}, nil
	}
	return WorkspaceInspection{Evidence: evidence, Phase: evidence.Phase}, nil
}

func (a *Allocator) prepareRequest(request CreateRequest) (CreateRequest, WorkspaceCreationEvidence, string, error) {
	if request.Store == nil || request.Capacity == nil || request.Guard.Validate() != nil || request.Workspace.Validate() != nil || request.Intent.Validate() != nil || request.Proposal.Validate() != nil || request.OperationID == "" || request.CapacityPlan.OperationID != request.OperationID || request.CapacityPlan.PolicyVersion != request.CapacityPolicy.Version || request.MarkerVersion == 0 || request.IsolationVersion == 0 || request.Now.IsZero() || request.Workspace.State != review.WorkspaceCreating || request.Workspace.ID != request.Proposal.WorkspaceID || request.Workspace.SourceThreadID != request.Intent.ThreadID || request.Workspace.SessionID != request.Intent.ConfirmedAgainst.SessionID || request.Workspace.SessionID != request.Guard.SessionID || request.DestinationPath == "" || !validAbsoluteCleanPath(request.DestinationPath) || !validWorkspaceLeaf(string(request.Workspace.ID)) {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", ErrInvalidWorkspaceRequest
	}
	destination, err := inspectRoot(RootDestination, request.DestinationPath, true)
	if err != nil {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", fmt.Errorf("destination identity: %w", err)
	}
	workspaceDir := filepath.Join(a.root, string(request.Workspace.ID))
	if !contained(a.root, workspaceDir) || sameOrDescendant(destination.CanonicalPath, a.root) || sameOrDescendant(a.root, destination.CanonicalPath) {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", ErrWorkspaceRootMismatch
	}
	parent, err := inspectRoot(RootParent, a.root, true)
	if err != nil {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", fmt.Errorf("parent identity: %w", err)
	}
	rootSet := RootSet{
		Baseline:    RootIdentity{Kind: RootBaseline, Path: filepath.Join(workspaceDir, "baseline"), CanonicalPath: filepath.Join(workspaceDir, "baseline")},
		Admin:       RootIdentity{Kind: RootAdmin, Path: filepath.Join(workspaceDir, "admin"), CanonicalPath: filepath.Join(workspaceDir, "admin")},
		Result:      RootIdentity{Kind: RootResult, Path: filepath.Join(workspaceDir, "result"), CanonicalPath: filepath.Join(workspaceDir, "result")},
		Destination: destination,
	}
	if err := rootSet.Validate(false); err != nil {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", fmt.Errorf("workspace roots: %w", err)
	}
	nonce, err := newWorkspaceNonce()
	if err != nil {
		return CreateRequest{}, WorkspaceCreationEvidence{}, "", err
	}
	prepared := request
	prepared.Workspace.Roots = review.WorkspaceRoots{Baseline: rootSet.Baseline.Path, Admin: rootSet.Admin.Path, Result: rootSet.Result.Path, Destination: rootSet.Destination.CanonicalPath}
	prepared.Workspace.UpdatedAt = request.Now
	evidence := WorkspaceCreationEvidence{WorkspaceID: request.Workspace.ID, RepositoryID: request.Workspace.RepositoryID, WorktreeID: request.Workspace.WorktreeID, ThreadID: request.Workspace.SourceThreadID, OperationID: request.OperationID, Nonce: nonce, Parent: parent, Roots: rootSet, MarkerVersion: request.MarkerVersion, IsolationVersion: request.IsolationVersion, Phase: WorkspacePlanned, CreatedAt: request.Now, UpdatedAt: request.Now}
	return prepared, evidence, workspaceDir, nil
}

func (a *Allocator) createFilesystem(workspaceDir string, evidence WorkspaceCreationEvidence) (WorkspaceCreationEvidence, error) {
	if err := paths.EnsurePrivateDir(workspaceDir); err != nil {
		return evidence, err
	}
	if err := writeMarker(workspaceDir, markerFromEvidence(evidence, evidence.Roots), true); err != nil {
		return evidence, err
	}
	return a.resumeFilesystem(workspaceDir, evidence)
}

func (a *Allocator) resumeFilesystem(workspaceDir string, evidence WorkspaceCreationEvidence) (WorkspaceCreationEvidence, error) {
	if _, err := os.Lstat(workspaceDir); errors.Is(err, os.ErrNotExist) {
		return a.createFilesystem(workspaceDir, evidence)
	} else if err != nil {
		return evidence, err
	}
	if err := verifyMarker(workspaceDir, markerFromEvidence(evidence, evidence.Roots), false); err != nil {
		return evidence, err
	}
	for _, root := range []RootIdentity{evidence.Roots.Baseline, evidence.Roots.Admin, evidence.Roots.Result} {
		if err := ensureEmptyPrivateRoot(root.CanonicalPath); err != nil {
			return evidence, err
		}
	}
	actual, err := inspectRootSet(evidence.Roots)
	if err != nil {
		return evidence, err
	}
	marker := markerFromEvidence(evidence, actual)
	if err := writeMarker(workspaceDir, marker, false); err != nil {
		return evidence, err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return evidence, err
	}
	digest := sha256.Sum256(data)
	evidence.Roots = actual
	evidence.MarkerSHA256 = hex.EncodeToString(digest[:])
	evidence.Phase = WorkspaceRootsCreated
	evidence.UpdatedAt = time.Now().UTC()
	return evidence, nil
}

func (a *Allocator) verifyAndPersist(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, evidence WorkspaceCreationEvidence, workspaceDir string) (WorkspaceCreationEvidence, app.SessionWriteGuard, error) {
	if evidence.Phase != WorkspaceRootsCreated && evidence.Phase != WorkspaceVerified {
		return evidence, guard, ErrWorkspaceRepairRequired
	}
	if evidence.Phase == WorkspaceRootsCreated {
		var err error
		guard, err = persistWorkspaceUpdate(ctx, store, guard, evidence)
		if err != nil {
			return evidence, guard, err
		}
	}
	if err := a.verifyFilesystem(workspaceDir, evidence, true); err != nil {
		return evidence, guard, err
	}
	if evidence.Phase == WorkspaceVerified {
		return evidence, guard, nil
	}
	evidence.Phase = WorkspaceVerified
	evidence.UpdatedAt = time.Now().UTC()
	next, err := persistWorkspaceUpdate(ctx, store, guard, evidence)
	if err != nil {
		return evidence, next, err
	}
	return evidence, next, nil
}

func (a *Allocator) verifyFilesystem(workspaceDir string, evidence WorkspaceCreationEvidence, final bool) error {
	if err := paths.EnsurePrivateDir(workspaceDir); err != nil {
		return err
	}
	parent, err := inspectRoot(RootParent, a.root, true)
	if err != nil || parent != evidence.Parent {
		return ErrWorkspaceRootMismatch
	}
	if err := verifyMarker(workspaceDir, markerFromEvidence(evidence, evidence.Roots), final); err != nil {
		return err
	}
	actual, err := inspectRootSet(evidence.Roots)
	if err != nil || actual != evidence.Roots {
		return ErrWorkspaceRootMismatch
	}
	return nil
}

func (a *Allocator) acquireWorkspaceLock(ctx context.Context, workspaceID domain.WorkspaceID) (*filelock.Lock, error) {
	if !validWorkspaceLeaf(string(workspaceID)) {
		return nil, ErrInvalidWorkspaceRequest
	}
	lockRoot := filepath.Join(a.root, workspaceLockDirectory)
	if err := paths.EnsurePrivateDir(lockRoot); err != nil {
		return nil, err
	}
	return filelock.Acquire(ctx, filepath.Join(lockRoot, string(workspaceID)+".lock"))
}

func persistWorkspaceCreate(ctx context.Context, request CreateRequest, evidence WorkspaceCreationEvidence) (app.SessionWriteGuard, error) {
	return request.Store.WithSessionTx(ctx, request.Guard, func(tx app.ReviewStoreTx) error {
		proposalTx, ok := tx.(app.ProposalWorkspaceStoreTx)
		if !ok {
			return ErrInvalidWorkspaceRequest
		}
		workspaceTx, ok := tx.(WorkspaceStoreTx)
		if !ok {
			return ErrInvalidWorkspaceRequest
		}
		if err := proposalTx.CreateWorkspace(ctx, request.Workspace, request.Intent, request.Proposal); err != nil {
			return err
		}
		return workspaceTx.CreateWorkspaceCreation(ctx, evidence)
	})
}

func persistWorkspaceUpdate(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, evidence WorkspaceCreationEvidence) (app.SessionWriteGuard, error) {
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		workspaceTx, ok := tx.(WorkspaceStoreTx)
		if !ok {
			return ErrInvalidWorkspaceRequest
		}
		return workspaceTx.UpdateWorkspaceCreation(ctx, evidence)
	})
}

func handleFromEvidence(evidence WorkspaceCreationEvidence) WorkspaceHandle {
	return WorkspaceHandle{WorkspaceID: evidence.WorkspaceID, RepositoryID: evidence.RepositoryID, WorktreeID: evidence.WorktreeID, ThreadID: evidence.ThreadID, OperationID: evidence.OperationID, Nonce: evidence.Nonce, MarkerVersion: evidence.MarkerVersion, IsolationVersion: evidence.IsolationVersion, Roots: newWorkspaceRoots(evidence.Roots)}
}

func markerFromEvidence(evidence WorkspaceCreationEvidence, roots RootSet) WorkspaceMarker {
	return WorkspaceMarker{WorkspaceID: evidence.WorkspaceID, RepositoryID: evidence.RepositoryID, WorktreeID: evidence.WorktreeID, ThreadID: evidence.ThreadID, OperationID: evidence.OperationID, CapacityReservationMarker: evidence.CapacityReservationMarker, Nonce: evidence.Nonce, Parent: evidence.Parent, Roots: roots, MarkerVersion: evidence.MarkerVersion, IsolationVersion: evidence.IsolationVersion, CreatedAt: evidence.CreatedAt}
}

func writeMarker(workspaceDir string, marker WorkspaceMarker, create bool) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	var file *os.File
	if create {
		file, err = paths.OpenProtectedFile(workspaceDir, workspaceMarkerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	} else {
		file, err = paths.OpenExistingProtectedFileForUpdate(workspaceDir, workspaceMarkerName, os.O_WRONLY|os.O_TRUNC)
	}
	if err != nil {
		if errors.Is(err, os.ErrExist) && create {
			return verifyMarker(workspaceDir, marker, false)
		}
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

func verifyMarker(workspaceDir string, expected WorkspaceMarker, final bool) error {
	data, err := paths.ReadProtectedFileBounded(workspaceDir, workspaceMarkerName, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceMarkerMissing
	}
	if err != nil {
		return err
	}
	var actual WorkspaceMarker
	if err := json.Unmarshal(data, &actual); err != nil {
		return fmt.Errorf("%w: decode", ErrWorkspaceMarkerMismatch)
	}
	if err := actual.Validate(final); err != nil {
		return fmt.Errorf("%w: validate", ErrWorkspaceMarkerMismatch)
	}
	if actual.WorkspaceID != expected.WorkspaceID || actual.RepositoryID != expected.RepositoryID || actual.WorktreeID != expected.WorktreeID || actual.ThreadID != expected.ThreadID || actual.OperationID != expected.OperationID || actual.CapacityReservationMarker != expected.CapacityReservationMarker || actual.Nonce != expected.Nonce || actual.MarkerVersion != expected.MarkerVersion || actual.IsolationVersion != expected.IsolationVersion || !actual.CreatedAt.Equal(expected.CreatedAt) {
		return fmt.Errorf("%w: metadata", ErrWorkspaceMarkerMismatch)
	}
	if actual.Parent != expected.Parent {
		return fmt.Errorf("%w: parent", ErrWorkspaceMarkerMismatch)
	}
	if actual.Roots.Baseline != expected.Roots.Baseline || actual.Roots.Admin != expected.Roots.Admin || actual.Roots.Result != expected.Roots.Result || actual.Roots.Destination != expected.Roots.Destination {
		return fmt.Errorf("%w: roots", ErrWorkspaceMarkerMismatch)
	}
	return nil
}

func inspectRootSet(expected RootSet) (RootSet, error) {
	baseline, err := inspectRoot(RootBaseline, expected.Baseline.CanonicalPath, true)
	if err != nil {
		return RootSet{}, err
	}
	admin, err := inspectRoot(RootAdmin, expected.Admin.CanonicalPath, true)
	if err != nil {
		return RootSet{}, err
	}
	result, err := inspectRoot(RootResult, expected.Result.CanonicalPath, true)
	if err != nil {
		return RootSet{}, err
	}
	destination, err := inspectRoot(RootDestination, expected.Destination.CanonicalPath, true)
	if err != nil {
		return RootSet{}, err
	}
	return RootSet{Baseline: baseline, Admin: admin, Result: result, Destination: destination}, nil
}

func inspectRoot(kind RootKind, path string, requireNative bool) (RootIdentity, error) {
	if !validAbsoluteCleanPath(path) {
		return RootIdentity{}, ErrInvalidWorkspaceRequest
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !validAbsoluteCleanPath(filepath.Clean(canonical)) {
		return RootIdentity{}, ErrWorkspaceRepairRequired
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RootIdentity{}, ErrWorkspaceRepairRequired
	}
	native, err := paths.NativeDirectoryIdentity(canonical)
	if err != nil && requireNative {
		return RootIdentity{}, err
	}
	identity := RootIdentity{Kind: kind, Path: canonical, CanonicalPath: canonical, NativeIdentity: native}
	if identity.Validate(requireNative) != nil {
		return RootIdentity{}, ErrWorkspaceRootMismatch
	}
	return identity, nil
}

func ensureEmptyPrivateRoot(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrWorkspaceRepairRequired
		}
		if err := paths.EnsurePrivateDir(path); err != nil {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return ErrWorkspaceRepairRequired
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := paths.EnsurePrivateDir(path); err != nil {
		return err
	}
	return nil
}

func newWorkspaceNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validWorkspaceNonce(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validAbsoluteCleanPath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func validWorkspaceLeaf(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`) && !strings.ContainsRune(value, '\x00')
}

func contained(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != "" && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func (r RootIdentity) String() string {
	return fmt.Sprintf("%s:%s", r.Kind, r.CanonicalPath)
}
