package repository

import (
	"errors"
	"math"
	"testing"
)

func TestRepoPathRoundTripKeepsRawBytes(t *testing.T) {
	t.Parallel()

	want := []byte{'d', 'i', 'r', '/', 0xff, '/', '.', '.', '/', 'f', 'i', 'l', 'e'}
	path, err := NewRepoPath(want)
	if err != nil {
		t.Fatalf("NewRepoPath() error = %v", err)
	}
	key := path.Key()
	got, err := key.Path()
	if err != nil {
		t.Fatalf("RepoPathKey.Path() error = %v", err)
	}
	if string(got.Bytes()) != string(want) {
		t.Fatalf("round trip = %v, want %v", got.Bytes(), want)
	}
	if _, err := NewRepoPath([]byte{'a', 0, 'b'}); !errors.Is(err, ErrInvalidRepoPath) {
		t.Fatalf("embedded NUL error = %v", err)
	}
}

func TestChangedFileValidation(t *testing.T) {
	t.Parallel()

	newPath := mustRepoPath(t, []byte("new.go"))
	oldPath := mustRepoPath(t, []byte("old.go"))
	id := mustObjectID(t, "abc123")

	valid := []ChangedFile{
		{NewPath: &newPath, Kind: ChangeAdded, NewFileKind: FileKindRegular, NewMode: 0o100644, NewObjectID: &id},
		{OldPath: &oldPath, Kind: ChangeDeleted, OldFileKind: FileKindRegular, OldMode: 0o100644, OldObjectID: &id},
		{OldPath: &oldPath, NewPath: &newPath, Kind: ChangeRenamed, OldFileKind: FileKindRegular, NewFileKind: FileKindRegular, OldMode: 0o100644, NewMode: 0o100644, OldObjectID: &id, NewObjectID: &id},
		{NewPath: &newPath, Kind: ChangeUntracked, NewFileKind: FileKindRegular, NewMode: 0o100644, Unstaged: true},
	}
	for _, file := range valid {
		if err := file.Validate(); err != nil {
			t.Errorf("valid ChangedFile rejected: %v", err)
		}
	}

	allZero := ObjectID("000000")
	invalid := []ChangedFile{
		{OldPath: &oldPath, NewPath: &newPath, Kind: ChangeAdded, OldFileKind: FileKindRegular, NewFileKind: FileKindRegular, OldMode: 0o100644, NewMode: 0o100644},
		{NewPath: &newPath, Kind: ChangeUntracked, NewFileKind: FileKindRegular, NewMode: 0o100644, NewObjectID: &allZero},
		{OldPath: &oldPath, Kind: ChangeRenamed, OldFileKind: FileKindRegular, OldMode: 0o100644},
		{OldPath: &oldPath, NewPath: &newPath, Kind: ChangeTypeChanged, OldFileKind: FileKindRegular, NewFileKind: FileKindRegular, OldMode: 0o100644, NewMode: 0o120000},
	}
	for _, file := range invalid {
		if err := file.Validate(); !errors.Is(err, ErrInvalidChangedFile) {
			t.Errorf("invalid ChangedFile error = %v", err)
		}
	}

	conflict := IndexConflictEvidence{
		Code:   "u",
		Stage1: &IndexStage{Mode: 0o100644, ObjectID: id},
		Stage2: &IndexStage{Mode: 0o100644, ObjectID: id},
		Stage3: &IndexStage{Mode: 0o100644, ObjectID: id},
	}
	if err := conflict.Validate(); err != nil {
		t.Fatalf("valid conflict rejected: %v", err)
	}
	conflict.Stage1 = nil
	if err := conflict.Validate(); !errors.Is(err, ErrInvalidConflictEvidence) {
		t.Fatalf("contradictory conflict error = %v", err)
	}
}

func TestDiffLineSideValidation(t *testing.T) {
	t.Parallel()

	base, head := 3, 4
	valid := []DiffLine{
		{Kind: DiffLineContext, BaseLine: &base, HeadLine: &head},
		{Kind: DiffLineAdded, HeadLine: &head},
		{Kind: DiffLineDeleted, BaseLine: &base},
		{Kind: DiffLineNoNewline, Text: "\\ No newline at end of file"},
	}
	for _, line := range valid {
		if err := line.Validate(); err != nil {
			t.Errorf("valid DiffLine rejected: %v", err)
		}
	}
	invalid := DiffLine{Kind: DiffLineAdded, BaseLine: &base, HeadLine: &head}
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidDiffLine) {
		t.Fatalf("wrong-side line error = %v", err)
	}
}

func TestDiffHunkRangeValidation(t *testing.T) {
	t.Parallel()

	base1, base2, head1, head2 := 1, 2, 1, 2
	valid := DiffHunk{
		ID:        "h1",
		BaseStart: 1,
		BaseCount: 2,
		HeadStart: 1,
		HeadCount: 2,
		Lines: []DiffLine{
			{Kind: DiffLineContext, BaseLine: &base1, HeadLine: &head1},
			{Kind: DiffLineDeleted, BaseLine: &base2},
			{Kind: DiffLineAdded, HeadLine: &head2},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid hunk rejected: %v", err)
	}
	valid.BaseCount = 1
	if err := valid.Validate(); !errors.Is(err, ErrInvalidDiffHunk) {
		t.Fatalf("wrong hunk count error = %v", err)
	}
	valid = DiffHunk{ID: "h2", BaseStart: 0, BaseCount: 1, HeadStart: 1, HeadCount: 1}
	if err := valid.Validate(); !errors.Is(err, ErrInvalidDiffHunk) {
		t.Fatalf("invalid zero hunk start error = %v", err)
	}
}

func TestPathPreconditionValidation(t *testing.T) {
	t.Parallel()

	path := mustRepoPath(t, []byte{'l', 'i', 'n', 'k', '/', 0xfe, '.', '.', '/', 'x'})
	validSymlink := PathPrecondition{
		Path:              path,
		MustExist:         true,
		Kind:              FileKindSymlink,
		Mode:              0o120000,
		SymlinkTargetHash: "target-hash",
	}
	if err := validSymlink.Validate(); err != nil {
		t.Fatalf("valid symlink precondition rejected: %v", err)
	}
	validAbsent := PathPrecondition{Path: mustRepoPath(t, []byte("gone"))}
	if err := validAbsent.Validate(); err != nil {
		t.Fatalf("valid absent precondition rejected: %v", err)
	}
	invalid := validAbsent
	invalid.Mode = 0o100644
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidPathPrecondition) {
		t.Fatalf("contradictory absent precondition error = %v", err)
	}

	regular := PathPrecondition{
		Path:        mustRepoPath(t, []byte("main.go")),
		MustExist:   true,
		Kind:        FileKindRegular,
		Mode:        0o100644,
		ContentHash: "content-hash",
		NativeAlias: &NativeAliasEvidence{Platform: "windows", VolumeIdentityHash: "volume", FileIdentityHash: "file", LinkCount: 2},
	}
	if err := regular.Validate(); err != nil {
		t.Fatalf("valid regular precondition rejected: %v", err)
	}
}

func TestPatchByteRangeValidation(t *testing.T) {
	t.Parallel()

	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := PatchByteRange{ArtifactID: "patch-1", Offset: 2, Length: 4, SHA256: digest}
	if err := valid.ValidateAgainstArtifact("patch-1", 10, digest); err != nil {
		t.Fatalf("valid patch range rejected: %v", err)
	}
	if err := valid.ValidateAgainstArtifact("patch-1", 5, digest); !errors.Is(err, ErrInvalidPatchRange) {
		t.Fatalf("out-of-artifact range error = %v", err)
	}
	for _, candidate := range []PatchByteRange{
		{ArtifactID: "patch-1", Offset: -1, Length: 1, SHA256: digest},
		{ArtifactID: "patch-1", Offset: math.MaxInt64, Length: 1, SHA256: digest},
		{ArtifactID: "patch-1", Offset: 0, Length: 1, SHA256: "bad"},
	} {
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidPatchRange) {
			t.Errorf("invalid patch range error = %v", err)
		}
	}
}

func TestFileContentValidation(t *testing.T) {
	t.Parallel()

	path := mustRepoPath(t, []byte("main.go"))
	content := FileContent{
		Snapshot:    SnapshotRef{Kind: SnapshotEmpty},
		Path:        path,
		Kind:        FileKindRegular,
		Mode:        0o100644,
		Bytes:       []byte("package main\n"),
		ContentHash: "content-hash",
	}
	if err := content.Validate(); err != nil {
		t.Fatalf("valid content rejected: %v", err)
	}
	content.Truncated = true
	content.LimitReason = "content limit"
	if err := content.Validate(); err != nil {
		t.Fatalf("valid truncated content rejected: %v", err)
	}
}

func mustRepoPath(t *testing.T, raw []byte) RepoPath {
	t.Helper()
	path, err := NewRepoPath(raw)
	if err != nil {
		t.Fatalf("NewRepoPath(%v): %v", raw, err)
	}
	return path
}

func mustObjectID(t *testing.T, raw string) ObjectID {
	t.Helper()
	id, err := NewObjectID(raw)
	if err != nil {
		t.Fatalf("NewObjectID(%q): %v", raw, err)
	}
	return id
}
