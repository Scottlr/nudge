package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestProposalWorkspaceAndVersionRoundTrip(t *testing.T) {
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
	guard, err := store.CreateSession(ctx, session, domain.SessionLeaseID("proposal-lease"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error { return tx.SaveThread(ctx, thread) }); err != nil {
		t.Fatal(err)
	}
	guard.ExpectedRevision++
	now := session.CreatedAt.Add(time.Minute)
	intent := review.ProposalIntent{
		ID:              domain.ProposalID("proposal-1"),
		ThreadID:        thread.ID,
		Summary:         "adjust example",
		ExpectedPaths:   []repository.RepoPath{repository.RepoPath("internal/example.go")},
		AnchorVersionID: 1,
		ConfirmedAgainst: review.GenerationProvenance{
			SessionID:  session.ID,
			Generation: 1,
			CaptureID:  captureIDPointer("capture-1"),
			Base:       repository.SnapshotRef{Kind: repository.SnapshotEmpty},
			Head:       repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: "head"},
		},
		ConfirmedAt: now,
	}
	workspace := review.ProposalWorkspace{
		ID:               domain.WorkspaceID("workspace-1"),
		RepositoryID:     repo.ID,
		WorktreeID:       worktree.ID,
		SessionID:        session.ID,
		SourceThreadID:   thread.ID,
		SourceGeneration: intent.ConfirmedAgainst,
		PolicyVersion:    1,
		State:            review.WorkspaceCreating,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	proposal := review.Proposal{ID: intent.ID, WorkspaceID: workspace.ID, ThreadID: thread.ID, Status: review.ProposalVersionDeriving, CreatedAt: now, UpdatedAt: now}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error {
		return tx.CreateWorkspace(ctx, workspace, intent, proposal)
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	aggregate, err := store.LoadProposalAggregate(ctx, intent.ID)
	if err != nil {
		t.Fatalf("load new aggregate: %v", err)
	}
	if aggregate.Intent.ID != intent.ID || aggregate.Workspace.ID != workspace.ID || len(aggregate.Versions) != 0 {
		t.Fatalf("new aggregate = %#v", aggregate)
	}

	attempt := review.ProposalAttempt{ID: domain.OperationID("attempt-1"), ProposalID: intent.ID, WorkspaceID: workspace.ID, ThreadID: thread.ID, ProviderTurnID: providerTurnPointer("turn-1"), SourceGeneration: intent.ConfirmedAgainst, Outcome: review.ProposalAttemptDeriving, ResultDisposition: review.ProposalResultNone, StartedAt: now}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.RecordProposalAttempt(ctx, attempt) })
	if err != nil {
		t.Fatalf("record attempt: %v", err)
	}

	data := []byte{0x00, 0xff, 0x01}
	digest := sha256.Sum256(data)
	path := repository.RepoPath("internal/example.go")
	oldPath := repository.RepoPath("internal/example.go")
	patch := review.ProposedPatch{
		ProposalID:       intent.ID,
		WorkspaceID:      workspace.ID,
		ThreadID:         thread.ID,
		AttemptID:        attempt.ID,
		SourceGeneration: intent.ConfirmedAgainst,
		Baseline:         review.SnapshotIdentity{ID: domain.ReviewSnapshotID("baseline-1"), Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: strings.Repeat("a", 64)},
		Result:           review.SnapshotIdentity{ID: domain.ReviewSnapshotID("result-1"), Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: "result"}, ManifestHash: strings.Repeat("b", 64)},
		Destination:      review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: worktree.ID, ExpectedWorkingTreeFingerprint: "destination"},
		Version:          1,
		PatchFormat:      "git-binary-safe",
		PatchBytes:       data,
		PatchSHA256:      hex.EncodeToString(digest[:]),
		Files:            []review.ProposedFile{{Path: path, OldPath: &oldPath, OldKind: repository.FileKindRegular, Kind: repository.FileKindRegular, OldMode: 0o100644, Mode: 0o100644, Binary: true}},
		Preconditions:    []repository.PathPrecondition{{Path: path, MustExist: true, Kind: repository.FileKindRegular, Mode: 0o100644, ContentHash: strings.Repeat("c", 64)}},
		Scope:            review.ProposalScopeFocused,
		Status:           review.ProposalVersionReady,
		CreatedAt:        now.Add(time.Minute),
	}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.PublishProposal(ctx, patch) })
	if err != nil {
		t.Fatalf("publish proposal: %v", err)
	}
	aggregate, err = store.LoadProposalAggregate(ctx, intent.ID)
	if err != nil {
		t.Fatalf("load published aggregate: %v", err)
	}
	if len(aggregate.Versions) != 1 || string(aggregate.Versions[0].PatchBytes) != string(data) || aggregate.Attempts[0].Outcome != review.ProposalAttemptVersionPublished {
		t.Fatalf("published aggregate = %#v", aggregate)
	}

	for _, status := range []review.ProposalStatus{review.ProposalVersionApplying, review.ProposalVersionApplied} {
		guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error {
			return tx.TransitionProposal(ctx, review.ProposalTransition{ProposalID: intent.ID, Version: 1, Status: status, FailurePhase: review.ProposalFailureNone, ChangedAt: now.Add(2 * time.Minute)})
		})
		if err != nil {
			t.Fatalf("transition %s: %v", status, err)
		}
	}
	aggregate, err = store.LoadProposalAggregate(ctx, intent.ID)
	if err != nil || aggregate.Versions[0].Status != review.ProposalVersionApplied {
		t.Fatalf("applied aggregate = %#v err=%v", aggregate, err)
	}

	noChanges := attempt
	noChanges.ID = domain.OperationID("attempt-2")
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.RecordProposalAttempt(ctx, noChanges) })
	if err != nil {
		t.Fatal(err)
	}
	noChanges.Outcome = review.ProposalAttemptNoChangesResetting
	noChanges.ResultDisposition = review.ProposalResultDiscarding
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.RecordProposalAttempt(ctx, noChanges) })
	if err != nil {
		t.Fatal(err)
	}
	noChanges.Outcome = review.ProposalAttemptNoChanges
	noChanges.ResultDisposition = review.ProposalResultDiscarded
	noChanges.Baseline = &review.SnapshotIdentity{ID: domain.ReviewSnapshotID("baseline-2"), Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: strings.Repeat("d", 64)}
	noChanges.Result = &review.SnapshotIdentity{ID: domain.ReviewSnapshotID("result-2"), Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: "result-2"}, ManifestHash: strings.Repeat("e", 64)}
	finished := now.Add(3 * time.Minute)
	noChanges.FinishedAt = &finished
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.RecordNoChanges(ctx, noChanges) })
	if err != nil {
		t.Fatalf("record no changes: %v", err)
	}
	aggregate, err = store.LoadProposalAggregate(ctx, intent.ID)
	if err != nil || len(aggregate.Attempts) != 2 || aggregate.Attempts[1].Outcome != review.ProposalAttemptNoChanges || len(aggregate.Versions) != 1 {
		t.Fatalf("no-change aggregate = %#v err=%v", aggregate, err)
	}

	if _, err := store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		proposalTx, ok := tx.(app.ProposalWorkspaceStoreTx)
		if !ok {
			return errors.New("proposal transaction extension unavailable")
		}
		return proposalTx.TransitionProposal(ctx, review.ProposalTransition{ProposalID: intent.ID, Version: 1, Status: review.ProposalVersionReady, ChangedAt: now})
	}); !errors.Is(err, review.ErrInvalidProposalTransition) {
		t.Fatalf("applied-to-ready error = %v, want invalid transition", err)
	}
}

func withProposalTx(ctx context.Context, store app.ReviewStore, guard app.SessionWriteGuard, fn func(app.ProposalWorkspaceStoreTx) error) (app.SessionWriteGuard, error) {
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		proposalTx, ok := tx.(app.ProposalWorkspaceStoreTx)
		if !ok {
			return errors.New("proposal transaction extension unavailable")
		}
		return fn(proposalTx)
	})
}

func captureIDPointer(value string) *domain.CaptureID {
	id := domain.CaptureID(value)
	return &id
}

func providerTurnPointer(value string) *domain.ProviderTurnID {
	id := domain.ProviderTurnID(value)
	return &id
}
