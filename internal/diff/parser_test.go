package diff

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestParsePatchGolden(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/hello.txt b/hello.txt",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644",
		"--- a/hello.txt",
		"+++ b/hello.txt",
		"@@ -1,2 +1,3 @@ greeting",
		" one",
		"-two",
		"+two changed",
		"+three",
		"\\ No newline at end of file",
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000000000000000000000000000000000000..3333333333333333333333333333333333333333",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+new",
		"diff --git a/deleted.txt b/deleted.txt",
		"deleted file mode 100644",
		"index 4444444444444444444444444444444444444444..0000000000000000000000000000000000000000",
		"--- a/deleted.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-gone",
		"diff --git a/old.txt b/new-name.txt",
		"similarity index 100%",
		"rename from old.txt",
		"rename to new-name.txt",
		"diff --git a/source.txt b/copy.txt",
		"similarity index 100%",
		"copy from source.txt",
		"copy to copy.txt",
		"diff --git a/mode.txt b/mode.txt",
		"old mode 100644",
		"new mode 100755",
		"diff --git a/image.bin b/image.bin",
		"index 5555555555555555555555555555555555555555..6666666666666666666666666666666666666666",
		"GIT binary patch",
		"literal 4",
		"A0;T",
		"",
	}, "\n")

	files, err := ParsePatch([]byte(patch))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("file count = %d, want 7", len(files))
	}
	if files[0].File.Kind != repository.ChangeModified || len(files[0].Hunks) != 1 || files[0].Hunks[0].BaseCount != 2 || files[0].Hunks[0].HeadCount != 3 {
		t.Fatalf("modified diff = %#v", files[0])
	}
	if files[0].Hunks[0].Lines[len(files[0].Hunks[0].Lines)-1].Kind != repository.DiffLineNoNewline {
		t.Fatal("no-newline marker was not retained")
	}
	if !files[0].Hunks[0].Lines[len(files[0].Hunks[0].Lines)-2].NoNewlineHead || files[0].Hunks[0].Lines[len(files[0].Hunks[0].Lines)-2].Terminator != repository.TerminatorNone {
		t.Fatalf("no-newline side evidence = %#v", files[0].Hunks[0].Lines[len(files[0].Hunks[0].Lines)-2])
	}
	if files[1].File.Kind != repository.ChangeAdded || files[1].File.OldPath != nil || files[1].File.NewPath == nil {
		t.Fatalf("added diff = %#v", files[1].File)
	}
	if files[2].File.Kind != repository.ChangeDeleted || files[2].File.NewPath != nil {
		t.Fatalf("deleted diff = %#v", files[2].File)
	}
	if files[3].File.Kind != repository.ChangeRenamed || files[4].File.Kind != repository.ChangeCopied {
		t.Fatalf("rename/copy kinds = %s/%s", files[3].File.Kind, files[4].File.Kind)
	}
	if files[3].File.Rename == nil || files[3].File.Rename.SimilarityPercent != 100 || files[3].File.Rename.Kind != repository.ChangeRenamed || !files[3].File.Rename.MatchesPaths(*files[3].File.OldPath, *files[3].File.NewPath) {
		t.Fatalf("rename evidence = %#v", files[3].File.Rename)
	}
	if files[4].File.Rename == nil || files[4].File.Rename.SimilarityPercent != 100 || files[4].File.Rename.Kind != repository.ChangeCopied {
		t.Fatalf("copy evidence = %#v", files[4].File.Rename)
	}
	if files[5].File.OldMode != 0o100644 || files[5].File.NewMode != 0o100755 {
		t.Fatalf("mode change = %#v", files[5].File)
	}
	if !files[6].File.Binary || files[6].File.ContentClass != repository.ContentClassRegularBinary || !files[6].BinaryComplete || files[6].BinaryPatch == nil || files[6].BinaryPatch.Length == 0 {
		t.Fatalf("binary diff = %#v", files[6])
	}
}

func TestParsePatchPreservesCRLFDisplayEvidence(t *testing.T) {
	files, err := ParsePatch([]byte(strings.Join([]string{
		"diff --git a/file.txt b/file.txt",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222",
		"--- a/file.txt",
		"+++ b/file.txt",
		"@@ -1 +1 @@",
		"-old\r",
		"+new\r",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	line := files[0].Hunks[0].Lines[0]
	if line.Text != "old" || line.Terminator != repository.TerminatorCRLF {
		t.Fatalf("CRLF line = %#v", line)
	}
}

func TestParsePatchRejectsDetachedNoNewlineMarker(t *testing.T) {
	_, err := ParsePatch([]byte(strings.Join([]string{
		"diff --git a/file.txt b/file.txt",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222",
		"--- a/file.txt",
		"+++ b/file.txt",
		"@@ -1 +1 @@",
		"\\ No newline at end of file",
		"-old",
		"+new",
	}, "\n")))
	if !errors.Is(err, ErrPatchMalformed) {
		t.Fatalf("error = %v, want malformed patch", err)
	}
}

func TestParsePatchKeepsSummaryBinaryReviewOnly(t *testing.T) {
	files, err := ParsePatch([]byte(strings.Join([]string{
		"diff --git a/image.bin b/image.bin",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222",
		"Binary files a/image.bin and b/image.bin differ",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files[0].File.Binary || files[0].File.ContentClass != repository.ContentClassRegularBinary || files[0].BinaryComplete {
		t.Fatalf("summary binary evidence = %#v", files)
	}
}

func TestParsePatchRejectsTruncatedInput(t *testing.T) {
	_, err := ParsePatch([]byte(strings.Join([]string{
		"diff --git a/file.txt b/file.txt",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644",
		"--- a/file.txt",
		"+++ b/file.txt",
		"@@ -1,2 +1,2 @@",
		"-one",
	}, "\n")))
	if !errors.Is(err, ErrPatchMalformed) {
		t.Fatalf("error = %v, want malformed patch", err)
	}
}

func TestBuildPatchIndexPublishesNoPartialRecords(t *testing.T) {
	value := strings.Join([]string{
		"diff --git a/ok.txt b/ok.txt",
		"index 1111111111111111111111111111111111111111..2222222222222222222222222222222222222222 100644",
		"--- a/ok.txt",
		"+++ b/ok.txt",
		"@@ -1 +1 @@",
		"-one",
		"+two",
		"diff --git a/bad.txt b/bad.txt",
		"@@ -1,2 +1 @@",
		"-only-one",
	}, "\n")
	source := bytePatchSource{data: []byte(value), id: "partial-test"}
	sink := new(MemoryPatchIndexSink)
	_, err := BuildPatchIndex(context.Background(), source, PatchParseLimits{}, sink)
	if !errors.Is(err, ErrPatchMalformed) || len(sink.Entries()) != 0 {
		t.Fatalf("error/entries = %v/%d, want malformed/0", err, len(sink.Entries()))
	}
}
