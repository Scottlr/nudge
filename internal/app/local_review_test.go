package app

import (
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestDisplayedDiffPreservesUnifiedRowSides(t *testing.T) {
	basePath := repository.RepoPath("file.go")
	headPath := repository.RepoPath("file.go")
	file := repository.ChangedFile{
		OldPath:     &basePath,
		NewPath:     &headPath,
		Kind:        repository.ChangeModified,
		OldFileKind: repository.FileKindRegular,
		NewFileKind: repository.FileKindRegular,
		OldMode:     0o100644,
		NewMode:     0o100644,
	}
	baseLine, headLine, addedLine := 1, 1, 2
	diffValue := repository.FileDiff{
		File: file,
		Hunks: []repository.DiffHunk{{
			ID:        "hunk-1",
			BaseStart: 1,
			BaseCount: 1,
			HeadStart: 1,
			HeadCount: 2,
			Header:    "@@ -1 +1,2 @@",
			Lines: []repository.DiffLine{
				{Kind: repository.DiffLineContext, BaseLine: &baseLine, HeadLine: &headLine, Text: "same"},
				{Kind: repository.DiffLineAdded, HeadLine: &addedLine, Text: "new"},
			},
		}},
	}
	target := repository.ResolvedTarget{
		Fingerprint: "target-fingerprint",
		Base:        repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: repository.ObjectID("base")},
		Head:        repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree"), Fingerprint: "capture-fingerprint"},
	}
	captureID := domain.CaptureID("capture")
	content := repository.FileContent{}
	_, page, err := displayedDiff(target, captureID, file, diffValue, &content)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 4 {
		t.Fatalf("displayed rows = %d, want 4", len(page.Rows))
	}
	if page.Rows[2].Kind != DisplayedRowContext || page.Rows[2].Side != SideBoth || !page.Rows[2].Selectable {
		t.Fatalf("context row = %#v", page.Rows[2])
	}
	if page.Rows[3].Kind != DisplayedRowAdded || page.Rows[3].Side != SideHead || !page.Rows[3].Selectable {
		t.Fatalf("added row = %#v", page.Rows[3])
	}
}

func TestDisplayedBinaryDiffExposesMetadataWithoutTextRows(t *testing.T) {
	basePath := repository.RepoPath("image.bin")
	headPath := repository.RepoPath("image.bin")
	file := repository.ChangedFile{
		OldPath:      &basePath,
		NewPath:      &headPath,
		Kind:         repository.ChangeModified,
		OldFileKind:  repository.FileKindRegular,
		NewFileKind:  repository.FileKindRegular,
		OldMode:      0o100644,
		NewMode:      0o100644,
		ContentClass: repository.ContentClassRegularBinary,
		Binary:       true,
	}
	digest := strings.Repeat("a", 64)
	diffValue := repository.FileDiff{File: file, BinaryPatch: &repository.PatchByteRange{ArtifactID: "patch-1", Offset: 12, Length: 24, SHA256: digest}}
	target := repository.ResolvedTarget{
		Fingerprint: "target-fingerprint",
		Base:        repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: repository.ObjectID("base")},
		Head:        repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: domain.WorktreeID("worktree"), Fingerprint: "capture-fingerprint"},
	}
	baseContent := &repository.FileContent{ByteLength: 32, ContentHash: strings.Repeat("b", 64)}
	headContent := &repository.FileContent{ByteLength: 48, ContentHash: strings.Repeat("c", 64)}
	displayed, page, err := displayedDiffWithSides(target, domain.CaptureID("capture"), file, diffValue, baseContent, headContent)
	if err != nil {
		t.Fatal(err)
	}
	if displayed.Status != ContentBinary || displayed.Binary == nil || displayed.Binary.BaseBytes != 32 || displayed.Binary.HeadBytes != 48 || displayed.Binary.Patch == nil {
		t.Fatalf("binary metadata = %#v", displayed.Binary)
	}
	if len(page.Rows) != 1 || page.Rows[0].Kind != DisplayedRowPlaceholder || page.Rows[0].Selectable {
		t.Fatalf("binary rows = %#v", page.Rows)
	}
}
