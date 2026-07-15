package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/workspace"
)

func TestWorkspaceCreationEvidenceRoundTripsThroughFencedStore(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo, worktree, session, thread, _ := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatal(err)
	}
	guard, err := store.CreateSession(ctx, session, domain.SessionLeaseID("workspace-evidence-lease"))
	if err != nil {
		t.Fatal(err)
	}
	guard, err = store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error { return tx.SaveThread(ctx, thread) })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	captureID := domain.CaptureID("capture-evidence")
	provenance := review.GenerationProvenance{SessionID: session.ID, Generation: 1, CaptureID: &captureID, Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: worktreeSnapshot(worktree.ID)}
	intent := review.ProposalIntent{ID: domain.ProposalID("proposal-evidence"), ThreadID: thread.ID, Summary: "persist roots", ExpectedPaths: []repository.RepoPath{repository.RepoPath("example.go")}, AnchorVersionID: 1, ConfirmedAgainst: provenance, ConfirmedAt: now}
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(workspaceRoot); err != nil {
		t.Fatal(err)
	}
	workspaceID := domain.WorkspaceID("workspace-evidence")
	workspaceDir := filepath.Join(workspaceRoot, string(workspaceID))
	rootSet := workspace.RootSet{
		Baseline: workspace.RootIdentity{Kind: workspace.RootBaseline, Path: filepath.Join(workspaceDir, "baseline"), CanonicalPath: filepath.Join(workspaceDir, "baseline")},
		Admin:    workspace.RootIdentity{Kind: workspace.RootAdmin, Path: filepath.Join(workspaceDir, "admin"), CanonicalPath: filepath.Join(workspaceDir, "admin")},
		Result:   workspace.RootIdentity{Kind: workspace.RootResult, Path: filepath.Join(workspaceDir, "result"), CanonicalPath: filepath.Join(workspaceDir, "result")},
	}
	destinationNative, err := paths.NativeDirectoryIdentity(destination)
	if err != nil {
		t.Fatal(err)
	}
	rootSet.Destination = workspace.RootIdentity{Kind: workspace.RootDestination, Path: destination, CanonicalPath: destination, NativeIdentity: destinationNative}
	parentNative, err := paths.NativeDirectoryIdentity(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	evidence := workspace.WorkspaceCreationEvidence{WorkspaceID: workspaceID, RepositoryID: repo.ID, WorktreeID: worktree.ID, ThreadID: thread.ID, OperationID: domain.OperationID("operation-evidence"), CapacityReservationMarker: "capacity-evidence", Nonce: strings.Repeat("a", 64), Parent: workspace.RootIdentity{Kind: workspace.RootParent, Path: workspaceRoot, CanonicalPath: workspaceRoot, NativeIdentity: parentNative}, Roots: rootSet, MarkerVersion: 1, IsolationVersion: 1, Phase: workspace.WorkspaceCreating, CreatedAt: now, UpdatedAt: now}
	proposalWorkspace := review.ProposalWorkspace{ID: workspaceID, RepositoryID: repo.ID, WorktreeID: worktree.ID, SessionID: session.ID, SourceThreadID: thread.ID, SourceGeneration: provenance, PolicyVersion: 1, State: review.WorkspaceCreating, CreatedAt: now, UpdatedAt: now}
	proposal := review.Proposal{ID: intent.ID, WorkspaceID: workspaceID, ThreadID: thread.ID, Status: review.ProposalVersionDeriving, CreatedAt: now, UpdatedAt: now}
	guard, err = store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		proposalTx, ok := tx.(app.ProposalWorkspaceStoreTx)
		if !ok {
			return app.ErrReviewStoreInput
		}
		if err := proposalTx.CreateWorkspace(ctx, proposalWorkspace, intent, proposal); err != nil {
			return err
		}
		workspaceTx, ok := tx.(workspace.WorkspaceStoreTx)
		if !ok {
			return app.ErrReviewStoreInput
		}
		return workspaceTx.CreateWorkspaceCreation(ctx, evidence)
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.LoadWorkspaceCreation(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, evidence) || guard.ExpectedRevision != 3 {
		t.Fatalf("restored evidence = %#v, guard=%#v", restored, guard)
	}
}

func worktreeSnapshot(worktreeID domain.WorktreeID) repository.SnapshotRef {
	return repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: "head"}
}
