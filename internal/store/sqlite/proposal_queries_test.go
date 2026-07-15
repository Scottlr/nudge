package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/diff"
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
	baselineManifest, err := app.NewWorkspaceManifest(nil)
	if err != nil {
		t.Fatalf("empty baseline manifest: %v", err)
	}
	resultManifest, err := app.NewResultManifest(nil, app.DefaultResourcePolicy().Version, true, app.ResultReasonNone)
	if err != nil {
		t.Fatalf("empty result manifest: %v", err)
	}
	delta, err := app.CompareResultManifest(baselineManifest, resultManifest)
	if err != nil {
		t.Fatalf("empty result delta: %v", err)
	}
	snapshot, err := app.NewResultSnapshot(app.ResultSnapshot{
		SessionID: session.ID, ProposalID: intent.ID, WorkspaceID: workspace.ID, WorktreeID: worktree.ID,
		AttemptID: attempt.ID, ThreadID: thread.ID, ProviderTurnID: "turn-1", ProviderTurnRef: "opaque-turn",
		Baseline: review.SnapshotIdentity{ID: "baseline-result-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: baselineManifest.Hash},
		Result:   review.SnapshotIdentity{ID: domain.ReviewSnapshotID("result-" + resultManifest.Hash), Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: resultManifest.Hash}, ManifestHash: resultManifest.Hash},
		Manifest: resultManifest, Delta: delta, PolicyVersion: app.DefaultResourcePolicy().Version, IsolationVersion: 1,
		LeaseNonce: strings.Repeat("a", 64), State: app.ResultSnapshotReady, Reason: app.ResultReasonNone, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("new result snapshot: %v", err)
	}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error {
		snapshotTx, ok := tx.(app.ResultSnapshotStoreTx)
		if !ok {
			return errors.New("result snapshot transaction extension unavailable")
		}
		return snapshotTx.AdoptResultSnapshot(ctx, snapshot)
	})
	if err != nil {
		t.Fatalf("adopt result snapshot: %v", err)
	}
	loadedSnapshot, err := store.LoadResultSnapshot(ctx, snapshot.ID)
	if err != nil || loadedSnapshot.ID != snapshot.ID || loadedSnapshot.Manifest.Hash != resultManifest.Hash {
		t.Fatalf("loaded result snapshot = %#v err=%v", loadedSnapshot, err)
	}
	loadedByAttempt, err := store.LoadResultSnapshotForAttempt(ctx, attempt.ID)
	if err != nil || loadedByAttempt.ID != snapshot.ID {
		t.Fatalf("loaded result snapshot by attempt = %#v err=%v", loadedByAttempt, err)
	}
	patchPath := repository.RepoPath("internal/example.go")
	patchFile := repository.ChangedFile{NewPath: &patchPath, Kind: repository.ChangeAdded, NewFileKind: repository.FileKindRegular, NewMode: 0o100644}
	patchIndex, err := app.NewProposalReviewIndex(diff.PatchIndexIdentity{Version: diff.PatchIndexVersion, SourceID: "patch-spool-1", Size: 1, SHA256: strings.Repeat("a", 64), FileCount: 1}, []diff.PatchIndexEntry{{Version: diff.PatchIndexVersion, SourceID: "patch-spool-1", Index: 0, Offset: 0, Length: 1, HeaderLength: 1, File: patchFile, SHA256: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatalf("proposal patch index: %v", err)
	}
	spoolLimits, err := app.DefaultSpoolLimits(app.DefaultResourcePolicy())
	if err != nil {
		t.Fatal(err)
	}
	patchArtifact, err := app.NewProposalPatchArtifact(app.ProposalPatchArtifact{
		SessionID: session.ID, ProposalID: intent.ID, WorkspaceID: workspace.ID, AttemptID: attempt.ID, ThreadID: thread.ID,
		Baseline: snapshot.Baseline, Result: snapshot.Result, BaselineSnapshotID: snapshot.Baseline.ID, ResultSnapshotID: snapshot.ID, PatchFormatVersion: 1, RenamePolicyVersion: 1, ConversionPolicyVersion: 1, ConversionFingerprint: strings.Repeat("c", 64), ResourcePolicyVersion: app.DefaultResourcePolicy().Version,
		Published: app.PublishedArtifact{Identity: app.ArtifactIdentity{SpoolID: "patch-spool-1", ManifestHash: strings.Repeat("d", 64), Bytes: 1, Entries: 1, Complete: true, VerifiedAt: now}, Target: app.PublishTarget{OwnerKind: app.OwnerProposal, RelativePath: "patch"}, Limits: spoolLimits}, PatchSHA256: strings.Repeat("a", 64),
		Index: patchIndex, Summary: app.ProposalPatchSummary{FileCount: 1, PatchBytes: 1}, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("new proposal patch artifact: %v", err)
	}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error {
		artifactTx, ok := tx.(app.ProposalPatchArtifactStoreTx)
		if !ok {
			return errors.New("proposal patch artifact transaction extension unavailable")
		}
		return artifactTx.AdoptProposalPatchArtifact(ctx, patchArtifact)
	})
	if err != nil {
		t.Fatalf("adopt proposal patch artifact: %v", err)
	}
	loadedArtifact, err := store.LoadProposalPatchArtifact(ctx, patchArtifact.ID)
	if err != nil || loadedArtifact.ID != patchArtifact.ID || loadedArtifact.Index.Hash != patchIndex.Hash {
		t.Fatalf("loaded proposal patch artifact = %#v err=%v", loadedArtifact, err)
	}

	path := repository.RepoPath("internal/example.go")
	oldPath := repository.RepoPath("internal/example.go")
	patch := review.ProposedPatch{
		ProposalID:       intent.ID,
		WorkspaceID:      workspace.ID,
		ThreadID:         thread.ID,
		AttemptID:        attempt.ID,
		SourceGeneration: intent.ConfirmedAgainst,
		Baseline:         snapshot.Baseline,
		Result:           snapshot.Result,
		Destination:      review.DestinationConstraints{TargetKind: repository.TargetLocal, WorktreeID: worktree.ID, ExpectedWorkingTreeFingerprint: "destination"},
		Version:          1,
		PatchFormat:      "git-binary-v1",
		PatchSHA256:      patchArtifact.PatchSHA256,
		Artifact: review.ProposedPatchArtifactReference{
			ArtifactID: patchArtifact.ID, SpoolID: patchArtifact.Published.Identity.SpoolID, ManifestHash: patchArtifact.Published.Identity.ManifestHash,
			PatchFormatVersion: patchArtifact.PatchFormatVersion, RenamePolicyVersion: patchArtifact.RenamePolicyVersion, ConversionPolicyVersion: patchArtifact.ConversionPolicyVersion,
			PatchSHA256: patchArtifact.PatchSHA256, PatchBytes: uint64(patchArtifact.Published.Identity.Bytes), IndexHash: patchArtifact.Index.Hash,
			FileCount: uint64(patchArtifact.Summary.FileCount), HunkCount: uint64(patchArtifact.Summary.HunkCount), RowCount: uint64(patchArtifact.Summary.RowCount), BinaryFiles: uint64(patchArtifact.Summary.BinaryFiles),
		},
		Files:         []review.ProposedFile{{Path: path, OldPath: &oldPath, OldKind: repository.FileKindRegular, Kind: repository.FileKindRegular, OldMode: 0o100644, Mode: 0o100644, Binary: true}},
		Preconditions: []repository.PathPrecondition{{Path: path, MustExist: true, Kind: repository.FileKindRegular, Mode: 0o100644, ContentHash: strings.Repeat("c", 64)}},
		Scope:         review.ProposalScopeFocused,
		Status:        review.ProposalVersionReady,
		CreatedAt:     now.Add(time.Minute),
	}
	guard, err = withProposalTx(ctx, store, guard, func(tx app.ProposalWorkspaceStoreTx) error { return tx.PublishProposal(ctx, patch) })
	if err != nil {
		t.Fatalf("publish proposal: %v", err)
	}
	aggregate, err = store.LoadProposalAggregate(ctx, intent.ID)
	if err != nil {
		t.Fatalf("load published aggregate: %v", err)
	}
	if len(aggregate.Versions) != 1 || len(aggregate.Versions[0].PatchBytes) != 0 || aggregate.Versions[0].Artifact.ArtifactID != patchArtifact.ID || aggregate.Attempts[0].Outcome != review.ProposalAttemptVersionPublished {
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
