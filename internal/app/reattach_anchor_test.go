package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestGenerateAnchorReattachmentCandidatesPreservesStableIdentity(t *testing.T) {
	anchor := reattachApplicationAnchor(t, review.AnchorAmbiguous)
	set, err := GenerateAnchorReattachmentCandidates(AnchorReattachmentInput{
		ThreadID:          "thread-1",
		Anchor:            anchor,
		CurrentGeneration: 1,
		Content:           review.CapturedFile{Path: anchor.Path, Side: anchor.Side, Lines: []string{"before", "target", "target", "after"}},
	})
	if err != nil {
		t.Fatalf("generate candidates: %v", err)
	}
	if set.State != review.AnchorAmbiguous || len(set.Candidates) != 2 {
		t.Fatalf("candidate set = %#v", set)
	}
	first, second := set.Candidates[0], set.Candidates[1]
	if first.Generation != 1 || first.ContentFingerprint == "" || review.AnchorCandidateFingerprint(first) == review.AnchorCandidateFingerprint(second) {
		t.Fatalf("candidate identities = %q/%q", review.AnchorCandidateFingerprint(first), review.AnchorCandidateFingerprint(second))
	}
}

func TestReattachAnchorAppendsManualVersionAndPreservesThreadAxes(t *testing.T) {
	store := newThreadTestStore()
	anchor := reattachApplicationAnchor(t, review.AnchorAmbiguous)
	thread, err := review.NewOpenReviewThread("thread-1", "session-1", anchor, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	conversation := domain.ProviderConversationID("conversation-1")
	thread.ProviderConversationID = &conversation
	thread.Resolution = review.ResolutionResolved
	thread.Read = review.Read
	if err := thread.Validate(); err != nil {
		t.Fatal(err)
	}
	store.threads[thread.ID] = thread
	service, err := NewAnchorReattachmentService(AnchorReattachmentServiceConfig{Store: store, Clock: fixedClock{when: time.Unix(20, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := review.AnchorCandidate{Generation: 1, Path: anchor.Path, Side: anchor.Side, StartLine: 4, EndLine: 4, ContentFingerprint: "content-1", SelectedText: "target", Tier: review.EvidenceTierSelectionWindow, Reason: "manual"}
	command := ReattachAnchor{Guard: threadTestGuard(), ThreadID: thread.ID, CurrentGeneration: 1, Candidate: candidate, CandidateFingerprint: review.AnchorCandidateFingerprint(candidate), Actor: "reviewer"}
	commit, err := service.ReattachAnchor(context.Background(), command)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if commit.Version.Method != review.AnchorVersionMethodManual || commit.Version.PreviousVersion != 1 || commit.Thread.Anchor.State != review.AnchorValid || commit.Thread.Resolution != thread.Resolution || commit.Thread.Read != thread.Read || commit.Thread.ProviderConversationID == nil || *commit.Thread.ProviderConversationID != conversation {
		t.Fatalf("reattachment commit = %#v", commit)
	}
	if len(commit.Events) != 1 {
		t.Fatalf("events = %#v", commit.Events)
	}
	if _, ok := commit.Events[0].(AnchorReattached); !ok {
		t.Fatalf("event type = %T", commit.Events[0])
	}
}

func TestReattachAnchorRejectsGenerationDrift(t *testing.T) {
	store := newThreadTestStore()
	anchor := reattachApplicationAnchor(t, review.AnchorAmbiguous)
	thread, err := review.NewOpenReviewThread("thread-1", "session-1", anchor, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	store.threads[thread.ID] = thread
	service, err := NewAnchorReattachmentService(AnchorReattachmentServiceConfig{Store: store, Clock: fixedClock{when: time.Unix(20, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := review.AnchorCandidate{Generation: 2, Path: anchor.Path, Side: anchor.Side, StartLine: 4, EndLine: 4, ContentFingerprint: "content-2", SelectedText: "target", Tier: review.EvidenceTierSelectionWindow, Reason: "manual"}
	_, err = service.ReattachAnchor(context.Background(), ReattachAnchor{Guard: threadTestGuard(), ThreadID: thread.ID, CurrentGeneration: 2, Candidate: candidate, CandidateFingerprint: review.AnchorCandidateFingerprint(candidate), Actor: "reviewer"})
	if !errors.Is(err, ErrAnchorCandidateStale) {
		t.Fatalf("error = %v, want generation drift", err)
	}
}

func reattachApplicationAnchor(t *testing.T, state review.AnchorState) review.CodeAnchor {
	t.Helper()
	path := repository.RepoPath("main.go")
	anchor, err := review.NewCodeAnchor(review.CodeAnchor{Path: path, Side: repository.DiffHead, StartLine: 2, EndLine: 2, TargetGeneration: 1, Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree", Fingerprint: "head"}, HunkFingerprint: "hunk", SelectionHash: review.FingerprintSelection("target"), SelectedText: "target", FingerprintVersion: review.AnchorFingerprintVersion, State: state, CreatedAt: time.Unix(1, 0).UTC(), Relocation: &review.RelocationMetadata{PreviousPath: path, PreviousStartLine: 2, PreviousEndLine: 2, Reason: "ambiguous", ReconciledAt: time.Unix(2, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return anchor
}
