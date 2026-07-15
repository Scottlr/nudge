package paths

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestNativePathQualificationPreservesRawIdentity(t *testing.T) {
	resolver := NewNativePathResolver()
	tests := []struct {
		name   string
		path   []byte
		safe   bool
		reason string
	}{
		{name: "space", path: []byte("dir/file name.txt"), safe: true},
		{name: "leading dash", path: []byte("-options.txt"), safe: true},
		{name: "unicode", path: []byte("caf\xc3\xa9.txt"), safe: true},
		{name: "traversal", path: []byte("../outside.txt"), reason: string(repository.NativeReasonPathTraversal)},
		{name: "git admin", path: []byte(".Git/config"), reason: string(repository.NativeReasonGitAdminAlias)},
	}
	if runtime.GOOS == "linux" {
		tests = append(tests, struct {
			name   string
			path   []byte
			safe   bool
			reason string
		}{name: "invalid utf8 on unix", path: []byte("bad\xff.txt"), safe: true})
	} else if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			name   string
			path   []byte
			safe   bool
			reason string
		}{name: "reserved device", path: []byte("CON.txt"), reason: string(repository.NativeReasonReservedName)})
	} else {
		tests = append(tests, struct {
			name   string
			path   []byte
			safe   bool
			reason string
		}{name: "invalid utf8 on normalization-sensitive unix", path: []byte("bad\xff.txt"), reason: string(repository.NativeReasonPathUnrepresentable)})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := repository.NewRepoPath(test.path)
			if err != nil {
				t.Fatal(err)
			}
			disposition, reason := resolver.QualifyRepoPath(path)
			if test.safe {
				if disposition != repository.NativePathSafe || reason != "" {
					t.Fatalf("qualification = %q/%q, want safe", disposition, reason)
				}
				return
			}
			if disposition != repository.NativePathReviewOnly || reason != test.reason {
				t.Fatalf("qualification = %q/%q, want review-only/%q", disposition, reason, test.reason)
			}
			key := path.Key()
			if roundTrip, err := key.Path(); err != nil || string(roundTrip) != string(test.path) {
				t.Fatalf("raw key round trip = %q/%v", roundTrip, err)
			}
		})
	}
}

func TestNativePathExecutorRevalidatesAndExecutesTypedLeaves(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "existing.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewVerifiedRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewNativePathResolver()
	path := repository.RepoPath("existing.txt")
	token, evidence, err := resolver.Resolve(context.Background(), root, path, repository.NativeReadExisting)
	if err != nil || evidence.Validate() != nil {
		t.Fatalf("resolve = %v, evidence=%+v", err, evidence)
	}
	var read NativeLeafResult
	if _, err := resolver.ExecuteLeaf(context.Background(), token, evidence, NativeLeafOperation{Kind: NativeLeafRead, MaxBytes: 32, Result: &read}); err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != "before" {
		t.Fatalf("read = %q", read.Data)
	}

	newPath := repository.RepoPath("created.txt")
	createToken, createEvidence, err := resolver.Resolve(context.Background(), root, newPath, repository.NativeCreateParent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ExecuteLeaf(context.Background(), createToken, createEvidence, NativeLeafOperation{Kind: NativeLeafCreate, Mode: 0o600, Data: []byte("created")}); err != nil {
		t.Fatal(err)
	}
	replaceToken, replaceEvidence, err := resolver.Resolve(context.Background(), root, newPath, repository.NativeReplaceLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ExecuteLeaf(context.Background(), replaceToken, replaceEvidence, NativeLeafOperation{Kind: NativeLeafReplace, Mode: 0o600, Data: []byte("replaced")}); err != nil {
		t.Fatal(err)
	}
	deleteToken, deleteEvidence, err := resolver.Resolve(context.Background(), root, newPath, repository.NativeDeleteLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ExecuteLeaf(context.Background(), deleteToken, deleteEvidence, NativeLeafOperation{Kind: NativeLeafDelete}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, "created.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file remains, err=%v", err)
	}
}

func TestNativePathExecutorRejectsStaleLeaf(t *testing.T) {
	rootPath := t.TempDir()
	filePath := filepath.Join(rootPath, "stale.txt")
	if err := os.WriteFile(filePath, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewVerifiedRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewNativePathResolver()
	token, evidence, err := resolver.Resolve(context.Background(), root, repository.RepoPath("stale.txt"), repository.NativeReplaceLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Revalidate(context.Background(), token, evidence); !errors.Is(err, ErrNativePathStale) {
		t.Fatalf("stale revalidation = %v", err)
	}
	_ = token.Close()
}

func TestNativePathExecutorKeepsUnsafePathReviewable(t *testing.T) {
	root, err := NewVerifiedRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := NewNativePathResolver()
	path := repository.RepoPath(".git/config")
	_, evidence, err := resolver.Resolve(context.Background(), root, path, repository.NativeReadExisting)
	if !errors.Is(err, ErrNativePathReviewOnly) || evidence.Disposition != repository.NativePathReviewOnly || evidence.ReasonCode != string(repository.NativeReasonGitAdminAlias) {
		t.Fatalf("unsafe resolve = %v, evidence=%+v", err, evidence)
	}
	if evidence.Validate() != nil {
		t.Fatalf("unsafe evidence invalid: %+v", evidence)
	}
}
