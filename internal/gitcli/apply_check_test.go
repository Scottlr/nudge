package gitcli

import (
	"context"
	"crypto/sha256"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

func TestApplyPatchCheckerUsesExactV1PolicyAndPatchStream(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	repo := repository.Repository{ID: "repo-1", CommonGitDir: filepath.Join(root, ".git"), Binding: repository.RepositoryBindingEvidence{Version: 1, ObjectFormat: "sha1", CommonGitDir: filepath.Join(root, ".git"), CommonGitDirIdentity: "repo"}, DisplayName: "repo", CreatedAt: now, UpdatedAt: now}
	worktree := repository.WorktreeRef{ID: "worktree-1", RepositoryID: repo.ID, RootPath: root, GitDir: filepath.Join(root, ".git"), Binding: repository.WorktreeBindingEvidence{Version: 1, ObjectFormat: "sha1", RootPath: root, GitDir: filepath.Join(root, ".git"), RootIdentity: "root", GitDirIdentity: "git"}}
	patch := []byte("diff --git a/main.go b/main.go\n")
	digest := sha256.Sum256(patch)
	runner := &applyCheckRunner{}
	checker, err := NewApplyPatchChecker(ApplyPatchCheckerConfig{Executable: process.ExecutableIdentity{Kind: process.ExecutableGit, Source: process.ExecutableConfigured, CanonicalPath: filepath.Join(root, "git.exe"), NativeID: []byte("native"), Size: 1, ModTime: now, SHA256: [32]byte{1}}, Runner: runner, Machine: DefaultMachineGitReadPolicyV1(), Apply: DefaultApplyPolicyV1()})
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Check(context.Background(), app.ApplyPatchCheckRequest{Repository: repo, Worktree: worktree, PatchSHA256: hexDigest(digest), PatchBytes: app.ByteSize(len(patch)), ApplyPolicyVersion: 1, Patch: bytesReader(patch)}); err != nil {
		t.Fatal(err)
	}
	args, err := DefaultApplyPolicyV1().Args(ApplyCheckPhase)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.args[len(runner.args)-len(args):], args) || string(runner.input) != string(patch) {
		t.Fatalf("git apply invocation args=%v input=%q", runner.args, runner.input)
	}
}

type applyCheckRunner struct {
	args  []string
	input []byte
}

func (r *applyCheckRunner) Run(_ context.Context, spec process.Spec) (process.Result, error) {
	r.args = append([]string(nil), spec.Args...)
	if spec.Stdin != nil {
		value, err := io.ReadAll(spec.Stdin)
		if err != nil {
			return process.Result{}, err
		}
		r.input = value
	}
	return process.Result{}, nil
}

func (r *applyCheckRunner) RunStream(context.Context, process.Spec, io.Writer) (process.StreamResult, error) {
	return process.StreamResult{}, nil
}

func (r *applyCheckRunner) Start(context.Context, process.Spec, process.StreamSink) (process.Process, error) {
	return nil, nil
}

func hexDigest(value [32]byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, 64)
	for index, value := range value {
		result[index*2] = digits[value>>4]
		result[index*2+1] = digits[value&0xf]
	}
	return string(result)
}

func bytesReader(value []byte) io.Reader { return &sliceReader{value: append([]byte(nil), value...)} }

type sliceReader struct {
	value  []byte
	offset int
}

func (r *sliceReader) Read(target []byte) (int, error) {
	if r.offset == len(r.value) {
		return 0, io.EOF
	}
	n := copy(target, r.value[r.offset:])
	r.offset += n
	return n, nil
}

var _ process.Runner = (*applyCheckRunner)(nil)
