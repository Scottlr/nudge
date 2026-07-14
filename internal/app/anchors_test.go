package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestBuildCodeAnchor(t *testing.T) {
	target, content, rows := anchorFixture(t)
	displayed := DisplayedContentSnapshot{Content: content, Revision: 7, Target: target, Rows: rows}

	anchor, err := BuildCodeAnchor(target, displayed, AnchorSelection{
		Side:       repository.DiffBase,
		StartRowID: rows[2].ID,
		EndRowID:   rows[3].ID,
		HunkID:     "hunk-1",
	}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("BuildCodeAnchor() error = %v", err)
	}
	if anchor.Path.Key() != repository.RepoPathKey("old/name.go") || anchor.PreviousPath.Key() != repository.RepoPathKey("new/name.go") {
		t.Fatalf("base paths = %q/%q", anchor.Path, anchor.PreviousPath)
	}
	if anchor.Side != repository.DiffBase || anchor.StartLine != 10 || anchor.EndLine != 11 || anchor.SelectedText != "before\nremoved" {
		t.Fatalf("base anchor = %+v", anchor)
	}
	if anchor.State != review.AnchorValid || anchor.TargetGeneration != target.Generation || anchor.BeforeContextHash != "" || anchor.AfterContextHash == "" {
		t.Fatalf("base anchor evidence = %+v", anchor)
	}

	headAnchor, err := BuildCodeAnchor(target, displayed, AnchorSelection{
		Side:       repository.DiffHead,
		StartRowID: rows[4].ID,
		EndRowID:   rows[5].ID,
		HunkID:     "hunk-1",
	}, time.Unix(100, 0), false)
	if err != nil {
		t.Fatalf("BuildCodeAnchor(head) error = %v", err)
	}
	if headAnchor.Path.Key() != repository.RepoPathKey("new/name.go") || headAnchor.PreviousPath.Key() != repository.RepoPathKey("old/name.go") || headAnchor.StartLine != 11 || headAnchor.EndLine != 12 || headAnchor.SelectedText != "" {
		t.Fatalf("head anchor = %+v", headAnchor)
	}
}

func TestBuildCodeAnchorRejectsInvalidSelection(t *testing.T) {
	target, content, rows := anchorFixture(t)
	displayed := DisplayedContentSnapshot{Content: content, Revision: 1, Target: target, Rows: rows}
	tests := []struct {
		name       string
		selection  AnchorSelection
		wantStale  bool
		wantBinary bool
	}{
		{
			name:      "mixed hunk",
			selection: AnchorSelection{Side: repository.DiffBase, StartRowID: rows[2].ID, EndRowID: rows[6].ID, HunkID: "hunk-1"},
		},
		{
			name:      "header",
			selection: AnchorSelection{Side: repository.DiffBase, StartRowID: rows[1].ID, EndRowID: rows[1].ID, HunkID: "hunk-1"},
		},
		{
			name:      "wrong side",
			selection: AnchorSelection{Side: repository.DiffBase, StartRowID: rows[4].ID, EndRowID: rows[4].ID, HunkID: "hunk-1"},
		},
		{
			name:      "stale row",
			selection: AnchorSelection{Side: repository.DiffBase, StartRowID: CodeRowID{Content: DisplayedContentID(strings.Repeat("b", 64)), Ordinal: rows[2].ID.Ordinal}, EndRowID: rows[2].ID, HunkID: "hunk-1"},
			wantStale: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildCodeAnchor(target, displayed, test.selection, time.Unix(100, 0), false)
			if err == nil || test.wantStale && !errors.Is(err, ErrAnchorSelectionStale) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	binaryContent := content
	binaryContent.Status = ContentBinary
	binaryContent.Reason = "binary"
	_, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: binaryContent, Revision: 1, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffBase, StartRowID: rows[2].ID, EndRowID: rows[2].ID, HunkID: "hunk-1"}, time.Unix(100, 0), false)
	if err == nil {
		t.Fatal("binary content unexpectedly produced an anchor")
	}
}

func TestAnchorHashNormalization(t *testing.T) {
	target, content, rows := anchorFixture(t)
	first := DisplayedContentSnapshot{Content: content, Revision: 1, Target: target, Rows: rows}
	firstAnchor, err := BuildCodeAnchor(target, first, AnchorSelection{Side: repository.DiffHead, StartRowID: rows[4].ID, EndRowID: rows[4].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("first anchor error = %v", err)
	}
	rows[4].Text = "\tadded value  \r"
	rows[4].HeadText = rows[4].Text
	second, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 2, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffHead, StartRowID: rows[4].ID, EndRowID: rows[4].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("second anchor error = %v", err)
	}
	if firstAnchor.SelectionHash == second.SelectionHash || firstAnchor.HunkFingerprint == second.HunkFingerprint {
		t.Fatalf("normalization unexpectedly changed hash identity: %q/%q and %q/%q", firstAnchor.SelectionHash, second.SelectionHash, firstAnchor.HunkFingerprint, second.HunkFingerprint)
	}

	rows[4].Text = "  added value  \r\n"
	rows[4].HeadText = rows[4].Text
	third, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 3, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffHead, StartRowID: rows[4].ID, EndRowID: rows[4].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("third anchor error = %v", err)
	}
	rows[4].Text = "  added value\t\n"
	rows[4].HeadText = rows[4].Text
	fourth, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 4, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffHead, StartRowID: rows[4].ID, EndRowID: rows[4].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("fourth anchor error = %v", err)
	}
	if third.SelectionHash != fourth.SelectionHash || third.HunkFingerprint != fourth.HunkFingerprint {
		t.Fatalf("line-ending/trailing-whitespace normalization differs: %q/%q and %q/%q", third.SelectionHash, third.HunkFingerprint, fourth.SelectionHash, fourth.HunkFingerprint)
	}
	if third.SelectedText == fourth.SelectedText {
		t.Fatal("stored selected text lost original whitespace")
	}
}

func TestBuildCodeAnchorSelectionLimit(t *testing.T) {
	target, content, rows := anchorFixture(t)
	rows = rows[:2]
	line := strings.Repeat("x", MaxAnchorSelectionBytes)
	rows = append(rows, DisplayedRow{ID: CodeRowID{Content: content.ID, Ordinal: 2}, Kind: DisplayedRowContext, HunkID: "hunk-1", BaseLine: intPointer(10), HeadLine: intPointer(10), BaseText: line, HeadText: line, Text: line, Side: SideBoth, Selectable: true})
	_, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 1, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffBase, StartRowID: rows[2].ID, EndRowID: rows[2].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if err != nil {
		t.Fatalf("exact limit error = %v", err)
	}

	rows[2].BaseText += "x"
	rows[2].Text = rows[2].BaseText
	_, err = BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 2, Target: target, Rows: rows}, AnchorSelection{Side: repository.DiffBase, StartRowID: rows[2].ID, EndRowID: rows[2].ID, HunkID: "hunk-1"}, time.Unix(100, 0), true)
	if !errors.Is(err, ErrAnchorEvidenceTooLarge) {
		t.Fatalf("over-limit error = %v", err)
	}
}

func anchorFixture(t *testing.T) (repository.ResolvedTarget, DisplayedContent, []DisplayedRow) {
	t.Helper()
	basePath, err := repository.NewRepoPath([]byte("old/name.go"))
	if err != nil {
		t.Fatal(err)
	}
	headPath, err := repository.NewRepoPath([]byte("new/name.go"))
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.NewSnapshotRef(repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "wt-1", Fingerprint: "head-fingerprint"})
	if err != nil {
		t.Fatal(err)
	}
	target := repository.ResolvedTarget{
		Spec:        repository.ReviewTargetSpec{Kind: repository.TargetLocal},
		Generation:  3,
		Base:        repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:        worktree,
		Fingerprint: "target-fingerprint",
		ResolvedAt:  time.Unix(10, 0),
	}
	if _, err := repository.NewResolvedTarget(target); err != nil {
		t.Fatal(err)
	}
	contentID := DisplayedContentID(strings.Repeat("a", 64))
	content := DisplayedContent{ID: contentID, Mode: DisplayUnifiedDiff, Status: ContentReady, BasePath: &basePath, HeadPath: &headPath}
	rows := []DisplayedRow{
		{ID: CodeRowID{Content: contentID, Ordinal: 0}, Kind: DisplayedRowDiffHeader, Side: SideNone, Text: "old/name.go -> new/name.go"},
		{ID: CodeRowID{Content: contentID, Ordinal: 1}, Kind: DisplayedRowHunkHeader, HunkID: "hunk-1", Side: SideNone, Text: "@@"},
		{ID: CodeRowID{Content: contentID, Ordinal: 2}, Kind: DisplayedRowContext, HunkID: "hunk-1", BaseLine: intPointer(10), HeadLine: intPointer(10), BaseText: "before", HeadText: "before", Text: "before", Side: SideBoth, Selectable: true},
		{ID: CodeRowID{Content: contentID, Ordinal: 3}, Kind: DisplayedRowDeleted, HunkID: "hunk-1", BaseLine: intPointer(11), BaseText: "removed", Text: "removed", Side: SideBase, Selectable: true},
		{ID: CodeRowID{Content: contentID, Ordinal: 4}, Kind: DisplayedRowAdded, HunkID: "hunk-1", HeadLine: intPointer(11), HeadText: "added", Text: "added", Side: SideHead, Selectable: true},
		{ID: CodeRowID{Content: contentID, Ordinal: 5}, Kind: DisplayedRowContext, HunkID: "hunk-1", BaseLine: intPointer(12), HeadLine: intPointer(12), BaseText: "after", HeadText: "after", Text: "after", Side: SideBoth, Selectable: true},
		{ID: CodeRowID{Content: contentID, Ordinal: 6}, Kind: DisplayedRowHunkHeader, HunkID: "hunk-2", Side: SideNone, Text: "@@"},
	}
	return target, content, rows
}

func intPointer(value int) *int {
	return &value
}
