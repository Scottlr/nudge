package app

import (
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestReconcileAnchorCases(t *testing.T) {
	tests := []struct {
		name       string
		anchor     review.CodeAnchor
		content    review.CapturedFile
		wantState  review.AnchorState
		wantLine   int
		wantReason string
	}{
		{
			name:       "nearby insertion",
			anchor:     testReconcileAnchor(t, "src/example.go", 4, 4, "target", []string{"old-before"}, []string{"old-after"}, false),
			content:    review.CapturedFile{Path: repository.RepoPath([]byte("src/example.go")), Side: repository.DiffHead, Lines: []string{"a", "b", "c", "inserted", "target", "d"}},
			wantState:  review.AnchorRelocated,
			wantLine:   5,
			wantReason: "unique_selected_range_within_window",
		},
		{
			name:       "moved block with context",
			anchor:     testReconcileAnchor(t, "src/example.go", 4, 4, "target", []string{"a", "b", "c"}, []string{"d", "e", "f"}, false),
			content:    review.CapturedFile{Path: repository.RepoPath([]byte("src/example.go")), Side: repository.DiffHead, Lines: []string{"prefix", "a", "b", "c", "target", "d", "e", "f", "suffix"}},
			wantState:  review.AnchorRelocated,
			wantLine:   5,
			wantReason: "selected_and_context_within_window",
		},
		{
			name:       "deleted selection",
			anchor:     testReconcileAnchor(t, "src/example.go", 2, 2, "gone", nil, nil, false),
			content:    review.CapturedFile{Path: repository.RepoPath([]byte("src/example.go")), Side: repository.DiffHead, Lines: []string{"one", "two", "three"}},
			wantState:  review.AnchorOrphaned,
			wantReason: "anchor_evidence_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := ReconcileAnchor(review.ReconcileInput{Anchor: test.anchor, Transition: testTransition(1, 2), NewContent: test.content, Now: time.Unix(20, 0).UTC()})
			if err != nil {
				t.Fatalf("ReconcileAnchor() error = %v", err)
			}
			if outcome.State != test.wantState {
				t.Fatalf("state = %s, want %s", outcome.State, test.wantState)
			}
			if test.wantLine != 0 && outcome.Anchor.StartLine != test.wantLine {
				t.Fatalf("start line = %d, want %d", outcome.Anchor.StartLine, test.wantLine)
			}
			if test.wantReason != "" && outcome.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", outcome.Reason, test.wantReason)
			}
			if err := outcome.Validate(); err != nil {
				t.Fatalf("outcome validation: %v", err)
			}
		})
	}
}

func TestReconcileDuplicateTextIsAmbiguous(t *testing.T) {
	anchor := testReconcileAnchor(t, "src/example.go", 1, 1, "target", nil, nil, true)
	content := review.CapturedFile{Path: repository.RepoPath([]byte("src/example.go")), Side: repository.DiffHead, Lines: []string{"changed"}}
	for index := 0; index < 25; index++ {
		content.Lines = append(content.Lines, "target")
	}
	outcome, err := ReconcileAnchor(review.ReconcileInput{Anchor: anchor, Transition: testTransition(1, 2), NewContent: content, Now: time.Unix(20, 0).UTC()})
	if err != nil {
		t.Fatalf("ReconcileAnchor() error = %v", err)
	}
	if outcome.State != review.AnchorAmbiguous || !outcome.CandidateOverflow || len(outcome.Candidates) != review.MaxAnchorReconciliationCandidates {
		t.Fatalf("ambiguous outcome = %#v", outcome)
	}
	for index, candidate := range outcome.Candidates {
		if candidate.StartLine != index+2 {
			t.Fatalf("candidate %d = %#v", index, candidate)
		}
	}
}

func TestReconcileRename(t *testing.T) {
	oldPath := repository.RepoPath([]byte("old/name.go"))
	newPath := repository.RepoPath([]byte("new/name.go"))
	anchor := testReconcileAnchor(t, string(oldPath), 2, 2, "target", nil, nil, false)
	evidence := review.RenamePolicyEvidence{Version: 1, SimilarityPercent: 60, MaxDeleteSources: 1000, MaxAddTargets: 1000, DetectChangedSourceCopies: true, Outcome: "complete"}
	content := review.CapturedFile{Path: newPath, Side: repository.DiffHead, Lines: []string{"before", "target", "after"}}
	transition := testTransition(1, 2)
	transition.RenameMappings = []review.RenameMapping{{OldPath: oldPath, NewPath: newPath, Side: repository.DiffHead}}
	transition.RenameEvidence = evidence
	outcome, err := ReconcileAnchor(review.ReconcileInput{Anchor: anchor, Transition: transition, NewContent: content, Now: time.Unix(20, 0).UTC()})
	if err != nil || outcome.State != review.AnchorRelocated || outcome.Anchor.Path.Key() != newPath.Key() {
		t.Fatalf("complete rename = %#v, err=%v", outcome, err)
	}

	transition.RenameEvidence.Outcome = "rename_detection_limited"
	transition.RenameEvidence.DeleteCandidates = 1000
	outcome, err = ReconcileAnchor(review.ReconcileInput{Anchor: anchor, Transition: transition, NewContent: content, Now: time.Unix(20, 0).UTC()})
	if err != nil || outcome.State != review.AnchorOrphaned {
		t.Fatalf("limited rename = %#v, err=%v", outcome, err)
	}
}

func TestReconcilePersistsVersion(t *testing.T) {
	anchor := testReconcileAnchor(t, "src/example.go", 2, 2, "target", nil, nil, true)
	outcome, err := ReconcileAnchor(review.ReconcileInput{
		Anchor:     anchor,
		Transition: testTransition(1, 2),
		NewContent: review.CapturedFile{Path: anchor.Path, Side: anchor.Side, Lines: []string{"before", "target", "after"}},
		Now:        time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	result := ReconciliationAnchorResult{OperationID: "operation-1", ThreadID: "thread-1", ReportID: "report-1", Anchor: outcome.Anchor, State: outcome.State, Reason: outcome.Reason, Candidates: outcome.Candidates, CandidateOverflow: outcome.CandidateOverflow, AlgorithmVersion: outcome.AlgorithmVersion}
	if result.AlgorithmVersion != review.AnchorReconciliationAlgorithmVersion || result.Anchor.TargetGeneration != 2 || result.Anchor.CreatedAt != anchor.CreatedAt {
		t.Fatalf("staged result = %#v", result)
	}
}

func testTransition(from, to repository.TargetGeneration) review.GenerationTransition {
	return review.GenerationTransition{FromCaptureID: "capture-old", ToCaptureID: "capture-new", FromGeneration: from, ToGeneration: to}
}

func testReconcileAnchor(t *testing.T, path string, start, end int, selected string, before, after []string, legacyHash bool) review.CodeAnchor {
	t.Helper()
	repoPath, err := repository.NewRepoPath([]byte(path))
	if err != nil {
		t.Fatal(err)
	}
	selectionHash := review.FingerprintSelection(selected)
	if legacyHash {
		selectionHash = review.LegacyFingerprintSelection(repository.DiffHead, repoPath, start, end, selected)
	}
	anchor := review.CodeAnchor{
		Path:               repoPath,
		Side:               repository.DiffHead,
		StartLine:          start,
		EndLine:            end,
		TargetGeneration:   1,
		Base:               repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:               repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree-1", Fingerprint: "head-fingerprint"},
		HunkFingerprint:    "hunk-fingerprint",
		SelectionHash:      selectionHash,
		SelectedText:       selected,
		BeforeContextHash:  review.FingerprintContext(before),
		AfterContextHash:   review.FingerprintContext(after),
		FingerprintVersion: review.AnchorFingerprintVersion,
		State:              review.AnchorValid,
		CreatedAt:          time.Unix(10, 0).UTC(),
	}
	if legacyHash {
		anchor.SelectedText = ""
	}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		t.Fatal(err)
	}
	return validated
}
