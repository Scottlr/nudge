package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestAllocatorCreatesVerifiesAndLocksFourRoots(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	allocator, err := NewAllocator(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := newWorkspaceTestStore()
	request := workspaceCreateRequest(store, destination)
	lease, guard, err := allocator.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if guard.ExpectedRevision != request.Guard.ExpectedRevision+4 {
		t.Fatalf("guard revision = %d, want %d", guard.ExpectedRevision, request.Guard.ExpectedRevision+4)
	}
	handle := lease.Handle()
	roots := []string{handle.Roots.Baseline.Path(), handle.Roots.Admin.Path(), handle.Roots.Result.Path(), handle.Roots.Destination.Path()}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if _, exists := seen[root]; exists {
			t.Fatalf("duplicate root identity: %q", root)
		}
		seen[root] = struct{}{}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			t.Fatalf("root %q = %v, info=%v", root, err, info)
		}
	}
	markerPath := filepath.Join(filepath.Dir(handle.Roots.Baseline.Path()), workspaceMarkerName)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("marker stat: %v", err)
	}
	if evidence := store.evidence; evidence.Phase != WorkspaceVerified || evidence.MarkerSHA256 == "" {
		t.Fatalf("durable evidence = %#v", evidence)
	}

	blockedContext, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := allocator.Inspect(blockedContext, store, handle.WorkspaceID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent inspection error = %v, want deadline", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := allocator.Inspect(context.Background(), store, handle.WorkspaceID)
	if err != nil || inspection.Phase != WorkspaceVerified || inspection.Handle == nil {
		t.Fatalf("inspection = %#v, err=%v", inspection, err)
	}
	if inspection.Handle.Roots.Result.Path() != handle.Roots.Result.Path() {
		t.Fatalf("result path changed after inspection: %q != %q", inspection.Handle.Roots.Result.Path(), handle.Roots.Result.Path())
	}
	store.evidence.Phase = WorkspaceCreating
	store.evidence.MarkerSHA256 = ""
	store.evidence.UpdatedAt = time.Now().UTC()
	resumed, resumedGuard, err := allocator.Resume(context.Background(), ResumeRequest{Store: store, Guard: guard, WorkspaceID: handle.WorkspaceID, OperationID: handle.OperationID, Nonce: handle.Nonce, Reservation: lease.Reservation()})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if resumed.Handle().Roots.Admin.Path() != handle.Roots.Admin.Path() || resumedGuard.ExpectedRevision != guard.ExpectedRevision+2 {
		t.Fatalf("resumed handle/guard = %#v/%#v", resumed.Handle(), resumedGuard)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(markerPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Inspect(context.Background(), store, handle.WorkspaceID); !errors.Is(err, ErrWorkspaceRepairRequired) {
		t.Fatalf("tampered marker error = %v, want repair required", err)
	}
}

func TestAllocatorRejectsDestinationInsideOwnedStorage(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	allocator, err := NewAllocator(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := newWorkspaceTestStore()
	request := workspaceCreateRequest(store, filepath.Join(workspaceRoot, "destination"))
	if err := os.Mkdir(request.DestinationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := allocator.Create(context.Background(), request); !errors.Is(err, ErrWorkspaceRootMismatch) {
		t.Fatalf("Create() error = %v, want root mismatch", err)
	}
}

func TestAllocatorResumesPlannedCreationBeforeFilesystemMutation(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	allocator, err := NewAllocator(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := newWorkspaceTestStore()
	request := workspaceCreateRequest(store, destination)
	prepared, evidence, workspaceDir, err := allocator.prepareRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := request.Capacity.Reserve(context.Background(), request.CapacityPlan, request.CapacityPolicy, request.CapacityEvidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.CapacityReservationMarker = reservation.Marker()
	guard, err := persistWorkspaceCreate(context.Background(), prepared, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(workspaceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planned workspace directory = %v, want absent", err)
	}
	lease, finalGuard, err := allocator.Resume(context.Background(), ResumeRequest{Store: store, Guard: guard, WorkspaceID: evidence.WorkspaceID, OperationID: evidence.OperationID, Nonce: evidence.Nonce, Reservation: reservation})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	defer lease.Close()
	if finalGuard.ExpectedRevision != request.Guard.ExpectedRevision+4 || store.evidence.Phase != WorkspaceVerified {
		t.Fatalf("resumed guard/evidence = %#v/%#v", finalGuard, store.evidence)
	}
}

func workspaceCreateRequest(store *workspaceTestStore, destination string) CreateRequest {
	now := time.Now().UTC().Truncate(time.Microsecond)
	workspaceID := domain.WorkspaceID("workspace-test")
	sessionID := domain.ReviewSessionID("session-test")
	worktreeID := domain.WorktreeID("worktree-test")
	threadID := domain.ReviewThreadID("thread-test")
	captureID := domain.CaptureID("capture-test")
	provenance := review.GenerationProvenance{SessionID: sessionID, Generation: 1, CaptureID: &captureID, Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: "head"}}
	intent := review.ProposalIntent{ID: domain.ProposalID("proposal-test"), ThreadID: threadID, Summary: "change example", ExpectedPaths: []repository.RepoPath{repository.RepoPath("example.go")}, AnchorVersionID: 1, ConfirmedAgainst: provenance, ConfirmedAt: now}
	workspace := review.ProposalWorkspace{ID: workspaceID, RepositoryID: domain.RepositoryID("repo-test"), WorktreeID: worktreeID, SessionID: sessionID, SourceThreadID: threadID, SourceGeneration: provenance, PolicyVersion: 1, State: review.WorkspaceCreating, CreatedAt: now, UpdatedAt: now}
	proposal := review.Proposal{ID: intent.ID, WorkspaceID: workspaceID, ThreadID: threadID, Status: review.ProposalVersionDeriving, CreatedAt: now, UpdatedAt: now}
	operationID := domain.OperationID("operation-test")
	policy := app.DefaultResourcePolicy()
	plan := app.CapacityPlan{OperationID: operationID, PolicyVersion: policy.Version, VolumePeaks: []app.VolumePeak{{ID: "volume", Finals: 1, Reserve: policy.Storage.MinimumFreeBytes}}}
	return CreateRequest{Store: store, Capacity: &workspaceTestCapacity{}, CapacityPlan: plan, CapacityPolicy: policy, CapacityEvidence: []app.VolumeEvidence{{ID: "volume", Free: 1 << 62, Mode: app.VolumeCapacityMonitored, Stable: true}}, Guard: app.SessionWriteGuard{SessionID: sessionID, LeaseID: domain.SessionLeaseID("lease-test"), WriterEpoch: 1, ExpectedRevision: 1}, Workspace: workspace, Intent: intent, Proposal: proposal, DestinationPath: destination, OperationID: operationID, MarkerVersion: workspaceMarkerVersion, IsolationVersion: workspaceIsolationVersion, Now: now}
}

type workspaceTestCapacity struct{}

func (c *workspaceTestCapacity) Reserve(_ context.Context, plan app.CapacityPlan, policy app.ResourcePolicy, evidence []app.VolumeEvidence) (app.CapacityReservation, error) {
	if err := app.ValidateCapacityPlan(policy, plan, evidence); err != nil {
		return app.CapacityReservation{}, err
	}
	digest, err := app.PlanDigest(plan)
	if err != nil {
		return app.CapacityReservation{}, err
	}
	return app.NewCapacityReservation("capacity-marker", plan.OperationID, "", digest, policy.Version)
}

func (c *workspaceTestCapacity) Recheck(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.RecheckBounds, []app.VolumeEvidence) (app.CapacityCheck, error) {
	return app.CapacityCheck{}, nil
}
func (c *workspaceTestCapacity) Release(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy) error {
	return nil
}
func (c *workspaceTestCapacity) Reconcile(context.Context, app.CapacityReservation, app.CapacityPlan, app.ResourcePolicy, app.ReconciliationProof) error {
	return nil
}

type workspaceTestStore struct {
	evidence WorkspaceCreationEvidence
}

func newWorkspaceTestStore() *workspaceTestStore { return &workspaceTestStore{} }

func (s *workspaceTestStore) WithSessionTx(_ context.Context, guard app.SessionWriteGuard, fn func(app.ReviewStoreTx) error) (app.SessionWriteGuard, error) {
	tx := &workspaceTestTx{store: s}
	if err := fn(tx); err != nil {
		return guard, err
	}
	guard.ExpectedRevision++
	return guard, nil
}

func (s *workspaceTestStore) LoadWorkspaceCreation(_ context.Context, id domain.WorkspaceID) (WorkspaceCreationEvidence, error) {
	if s.evidence.WorkspaceID != id {
		return WorkspaceCreationEvidence{}, app.ErrReviewStoreNotFound
	}
	return s.evidence, nil
}

type workspaceTestTx struct{ store *workspaceTestStore }

func (t *workspaceTestTx) CreateWorkspaceCreation(_ context.Context, evidence WorkspaceCreationEvidence) error {
	t.store.evidence = evidence
	return nil
}
func (t *workspaceTestTx) UpdateWorkspaceCreation(_ context.Context, evidence WorkspaceCreationEvidence) error {
	if t.store.evidence.WorkspaceID != evidence.WorkspaceID || !t.store.evidence.Phase.CanTransitionTo(evidence.Phase) {
		return app.ErrSessionRevisionConflict
	}
	t.store.evidence = evidence
	return nil
}

func (t *workspaceTestTx) CreateWorkspace(context.Context, review.ProposalWorkspace, review.ProposalIntent, review.Proposal) error {
	return nil
}
func (t *workspaceTestTx) RecordProposalAttempt(context.Context, review.ProposalAttempt) error {
	return nil
}
func (t *workspaceTestTx) RecordNoChanges(context.Context, review.ProposalAttempt) error { return nil }
func (t *workspaceTestTx) PublishProposal(context.Context, review.ProposedPatch) error   { return nil }
func (t *workspaceTestTx) TransitionProposal(context.Context, review.ProposalTransition) error {
	return nil
}
func (t *workspaceTestTx) SaveSession(context.Context, review.ReviewSession) error { return nil }
func (t *workspaceTestTx) SaveThread(context.Context, review.ReviewThread) error   { return nil }
func (t *workspaceTestTx) SaveMessage(context.Context, review.Message) error       { return nil }
func (t *workspaceTestTx) SaveProviderConversation(context.Context, app.ProviderConversationRecord) error {
	return nil
}
func (t *workspaceTestTx) SaveProviderTurn(context.Context, app.ProviderTurnRecord) error { return nil }
func (t *workspaceTestTx) SaveCaptureGeneration(context.Context, app.CaptureGeneration, app.CaptureManifest) error {
	return nil
}
func (t *workspaceTestTx) SaveAcceptedTargetGeneration(context.Context, app.AcceptedTargetGeneration) error {
	return nil
}
func (t *workspaceTestTx) CreateReconciliation(context.Context, app.ReconciliationOperation) error {
	return nil
}
func (t *workspaceTestTx) UpdateReconciliation(context.Context, app.ReconciliationOperation) error {
	return nil
}
func (t *workspaceTestTx) StageReconciliationResult(context.Context, app.ReconciliationAnchorResult) error {
	return nil
}
func (t *workspaceTestTx) CompleteReconciliation(context.Context, domain.OperationID, time.Time) error {
	return nil
}
func (t *workspaceTestTx) ActivateReconciliation(context.Context, domain.OperationID) error {
	return nil
}
