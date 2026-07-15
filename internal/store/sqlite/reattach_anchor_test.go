package sqlite

import (
	"context"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestManualAnchorVersionRoundTripKeepsHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo, worktree, session, thread, _ := testStoreValues()
	thread.Anchor.State = review.AnchorAmbiguous
	if err := thread.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatal(err)
	}
	guard, err := store.CreateSession(ctx, session, "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	guard, err = store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error { return tx.SaveThread(ctx, thread) })
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewAnchorReattachmentService(app.AnchorReattachmentServiceConfig{Store: store, Clock: app.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := review.AnchorCandidate{Generation: 1, Path: thread.Anchor.Path, Side: thread.Anchor.Side, StartLine: 4, EndLine: 4, ContentFingerprint: "content-1", SelectedText: thread.Anchor.SelectedText, Tier: review.EvidenceTierSelectionWindow, Reason: "manual"}
	commit, err := service.ReattachAnchor(ctx, app.ReattachAnchor{Guard: guard, ThreadID: thread.ID, CurrentGeneration: 1, Candidate: candidate, CandidateFingerprint: review.AnchorCandidateFingerprint(candidate), Actor: "reviewer"})
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	history, err := store.ListAnchorVersions(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].Method != review.AnchorVersionMethodManual || history[1].PreviousVersion != 1 || history[1].Candidate == nil || history[1].Candidate.StartLine != 4 {
		t.Fatalf("history = %#v", history)
	}
	loaded, err := store.LoadThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Anchor.State != review.AnchorValid || loaded.Anchor.StartLine != 4 || loaded.Resolution != thread.Resolution || loaded.Read != thread.Read {
		t.Fatalf("loaded thread = %#v", loaded)
	}
	if commit.Version.Version != 2 {
		t.Fatalf("committed version = %#v", commit.Version)
	}
}
