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
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestRetirementExecutorRemovesOnlyVerifiedWorkspaceAndIsIdempotent(t *testing.T) {
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
	lease, _, err := allocator.Create(context.Background(), workspaceCreateRequest(store, destination))
	if err != nil {
		t.Fatal(err)
	}
	handle := lease.Handle()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	evidence := store.evidence
	digest, err := OwnershipDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	candidate := app.WorkspaceRetentionCandidate{
		RepositoryID: evidence.RepositoryID, WorktreeID: evidence.WorktreeID, SessionID: "session-test", WorkspaceID: evidence.WorkspaceID, ThreadID: evidence.ThreadID, ProposalID: "proposal-test",
		ThreadResolution: review.ResolutionResolved, ProposalState: review.ProposalApplied, WorkspaceState: review.WorkspaceReady, BasisTime: now.Add(-app.DefaultWorkspaceRetentionMinimumAge), EvaluatedRevision: 1,
		ProposalTerminal: true, ApplyTerminal: true, LifecycleTerminal: true, JournalCertain: true, OwnershipCertain: true, OwnershipDigest: digest, MarkerNonce: evidence.Nonce, HistoryCertain: true,
	}
	decision, err := app.EvaluateWorkspaceRetention(app.DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := app.WorkspaceRetirement{Version: 1, OperationID: domain.OperationID("retirement-test"), Candidate: candidate, Decision: decision, Phase: app.WorkspaceRetirementRemoving, CreatedAt: now, UpdatedAt: now}
	executor, err := NewRetirementExecutor(allocator, store)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := executor.Remove(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if proof.AlreadyRemoved || proof.WorkspaceID != handle.WorkspaceID {
		t.Fatalf("proof = %#v", proof)
	}
	if _, err := os.Lstat(filepath.Dir(evidence.Roots.Baseline.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace directory still exists: %v", err)
	}
	retry, err := executor.Remove(context.Background(), plan)
	if err != nil || !retry.AlreadyRemoved {
		t.Fatalf("idempotent retry = %#v, err=%v", retry, err)
	}
}

func TestRetirementExecutorRejectsPartialRootLoss(t *testing.T) {
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
	lease, _, err := allocator.Create(context.Background(), workspaceCreateRequest(store, destination))
	if err != nil {
		t.Fatal(err)
	}
	evidence := store.evidence
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(evidence.Roots.Result.Path); err != nil {
		t.Fatal(err)
	}
	digest, err := OwnershipDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	candidate := app.WorkspaceRetentionCandidate{RepositoryID: evidence.RepositoryID, WorktreeID: evidence.WorktreeID, SessionID: "session-test", WorkspaceID: evidence.WorkspaceID, ThreadID: evidence.ThreadID, ProposalID: "proposal-test", ThreadResolution: review.ResolutionResolved, ProposalState: review.ProposalApplied, WorkspaceState: review.WorkspaceReady, BasisTime: now.Add(-app.DefaultWorkspaceRetentionMinimumAge), EvaluatedRevision: 1, ProposalTerminal: true, ApplyTerminal: true, LifecycleTerminal: true, JournalCertain: true, OwnershipCertain: true, OwnershipDigest: digest, MarkerNonce: evidence.Nonce, HistoryCertain: true}
	decision, err := app.EvaluateWorkspaceRetention(app.DefaultWorkspaceRetentionPolicy(), candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := app.WorkspaceRetirement{Version: 1, OperationID: "retirement-partial", Candidate: candidate, Decision: decision, Phase: app.WorkspaceRetirementRemoving, CreatedAt: now, UpdatedAt: now}
	executor, err := NewRetirementExecutor(allocator, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Remove(context.Background(), plan); !errors.Is(err, ErrWorkspaceRetirementOwnership) {
		t.Fatalf("partial root removal error = %v", err)
	}
}
