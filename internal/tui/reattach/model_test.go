package reattach

import (
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestSelectionUsesCandidateIdentityAndConfirms(t *testing.T) {
	anchor := reattachTestAnchor(t)
	first := reattachTestCandidate(anchor, 4, "first")
	second := reattachTestCandidate(anchor, 8, "second")
	model := NewModel("reviewer")
	model.Update(SetProjectionMsg{Projection: Projection{ThreadID: "thread-1", CurrentGeneration: 2, Original: anchor, State: review.AnchorAmbiguous, Reason: "duplicate", Candidates: []review.AnchorCandidate{first, second}}})
	model.Update(SelectCandidateMsg{Fingerprint: review.AnchorCandidateFingerprint(second)})
	model.Update(BeginConfirmMsg{})
	intents := model.Update(ConfirmMsg{})
	if len(intents) != 1 || intents[0].Reattach == nil || intents[0].Reattach.Candidate.StartLine != second.StartLine || intents[0].Reattach.CandidateFingerprint != review.AnchorCandidateFingerprint(second) {
		t.Fatalf("confirm intents = %#v", intents)
	}
}

func TestGenerationErrorReturnsToSelection(t *testing.T) {
	anchor := reattachTestAnchor(t)
	candidate := reattachTestCandidate(anchor, 4, "first")
	model := NewModel("reviewer")
	model.Update(SetProjectionMsg{Projection: Projection{ThreadID: "thread-1", CurrentGeneration: 2, Original: anchor, State: review.AnchorAmbiguous, Reason: "duplicate", Candidates: []review.AnchorCandidate{candidate}}})
	model.Update(BeginConfirmMsg{})
	model.Update(SetErrorMsg{Message: "candidate generation advanced"})
	if model.Confirming() || model.LastError() == "" {
		t.Fatalf("stale selection state: confirming=%v error=%q", model.Confirming(), model.LastError())
	}
}

func reattachTestAnchor(t *testing.T) review.CodeAnchor {
	t.Helper()
	path := repository.RepoPath("main.go")
	anchor, err := review.NewCodeAnchor(review.CodeAnchor{Path: path, Side: repository.DiffHead, StartLine: 2, EndLine: 2, TargetGeneration: 2, Base: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Head: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree", Fingerprint: "head"}, HunkFingerprint: "hunk", SelectionHash: review.FingerprintSelection("target"), SelectedText: "target", FingerprintVersion: review.AnchorFingerprintVersion, State: review.AnchorAmbiguous, CreatedAt: time.Unix(1, 0).UTC(), Relocation: &review.RelocationMetadata{PreviousPath: path, PreviousStartLine: 2, PreviousEndLine: 2, Reason: "ambiguous", ReconciledAt: time.Unix(2, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return anchor
}

func reattachTestCandidate(anchor review.CodeAnchor, line int, text string) review.AnchorCandidate {
	candidate := review.AnchorCandidate{Generation: anchor.TargetGeneration, Path: anchor.Path, Side: anchor.Side, StartLine: line, EndLine: line, ContentFingerprint: strings.Repeat("a", 64), SelectedText: text, Tier: review.EvidenceTierSelectionWindow, Reason: "test"}
	if err := candidate.Validate(); err != nil {
		panic(err)
	}
	return candidate
}
