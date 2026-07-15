package gitcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/artifactspool"
	"github.com/Scottlr/nudge/internal/capacitystore"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

func TestCaptureLocalCandidateStreamsTrackedAndUntrackedState(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	adapter := newCaptureTestAdapter(t, root, gitPath)
	indexPath := filepath.Join(worktree.GitDir, "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if err := result.Candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.Candidate.EntryCount != 2 || result.Candidate.Patch.Bytes == 0 || result.Candidate.BlobSpool.Entries == 0 {
		t.Fatalf("candidate summary = %#v", result.Candidate)
	}
	var foundUntracked bool
	for _, entry := range result.Candidate.Entries {
		if entry.Change.NewPath != nil && string(*entry.Change.NewPath) == "untracked.txt" {
			foundUntracked = true
			if entry.Change.Kind != "untracked" {
				t.Fatalf("untracked entry kind = %q", entry.Change.Kind)
			}
		}
	}
	if !foundUntracked {
		t.Fatal("candidate omitted eligible untracked file")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexBefore) != string(indexAfter) {
		t.Fatal("capture modified the Git index")
	}
	fingerprint := result.Candidate.Fingerprint
	patchHash := result.Candidate.Patch.ContentSHA256
	blobHash := result.Candidate.BlobSpool.ManifestHash
	if err := result.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	repeated, err := adapter.Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if repeated.Candidate.Fingerprint != fingerprint || repeated.Candidate.Patch.ContentSHA256 != patchHash || repeated.Candidate.BlobSpool.ManifestHash != blobHash {
		t.Fatalf("repeated capture identities changed: first=%s/%s/%s second=%s/%s/%s", fingerprint, patchHash, blobHash, repeated.Candidate.Fingerprint, repeated.Candidate.Patch.ContentSHA256, repeated.Candidate.BlobSpool.ManifestHash)
	}
	if err := repeated.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureUnbornUsesInstalledGitEmptyTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "unborn")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath := testGitPath(t, root)
	runGit(t, root, gitPath, "init")
	if err := os.WriteFile(filepath.Join(root, "first.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	adapter := newCaptureTestAdapter(t, root, gitPath)
	result, err := adapter.Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	if !result.Candidate.Base.Unborn || result.Candidate.Base.ObjectID == "" || result.Candidate.EntryCount != 1 {
		t.Fatalf("unborn candidate base = %#v", result.Candidate.Base)
	}
	if err := result.Candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := result.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureAppliesCompleteRenameEvidence(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "mv", "tracked file.txt", "renamed.txt")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	result, err := newCaptureTestAdapter(t, root, gitPath).Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	defer func() { _ = result.Abort(context.Background()) }()
	if result.Candidate.EntryCount != 1 || result.Candidate.Entries[0].Change.Kind != "renamed" {
		t.Fatalf("rename candidate = %#v", result.Candidate.Entries)
	}
	entry := result.Candidate.Entries[0]
	if entry.Change.OldPath == nil || entry.Change.NewPath == nil || string(*entry.Change.OldPath) != "tracked file.txt" || string(*entry.Change.NewPath) != "renamed.txt" {
		t.Fatalf("rename paths = %#v", entry.Change)
	}
	if entry.Change.Rename == nil || entry.Change.Rename.SimilarityPercent < 60 || entry.Change.Rename.Kind != repository.ChangeRenamed || !entry.Change.Rename.MatchesPaths(*entry.Change.OldPath, *entry.Change.NewPath) || result.Candidate.Policy.RenameEvidenceHash == "" || len(result.Candidate.Policy.RenameFlags) != 2 {
		t.Fatalf("rename evidence = %#v policy=%#v", entry.Change.Rename, result.Candidate.Policy)
	}
}

func TestCaptureAppliesChangedSourceCopyEvidence(t *testing.T) {
	root, gitPath := initializedRepository(t)
	source := filepath.Join(root, "tracked file.txt")
	target := filepath.Join(root, "copied.txt")
	original := []byte(strings.Repeat("stable source line\n", 10))
	if err := os.WriteFile(source, original, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "tracked file.txt")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "expand copy source")
	content := append(append([]byte(nil), original...), []byte("changed source content\n")...)
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "tracked file.txt", "copied.txt")
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	result, err := newCaptureTestAdapter(t, root, gitPath).Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	defer func() { _ = result.Abort(context.Background()) }()
	var found bool
	for _, entry := range result.Candidate.Entries {
		if entry.Change.Kind != repository.ChangeCopied {
			continue
		}
		found = true
		if entry.Change.OldPath == nil || entry.Change.NewPath == nil || string(*entry.Change.OldPath) != "tracked file.txt" || string(*entry.Change.NewPath) != "copied.txt" || entry.Change.Rename == nil || !entry.Change.Rename.MatchesPaths(*entry.Change.OldPath, *entry.Change.NewPath) {
			t.Fatalf("copy entry = %#v", entry.Change)
		}
	}
	if !found {
		t.Fatalf("capture entries = %#v", result.Candidate.Entries)
	}
}

func TestCaptureRecordsNonNeutralAttributeEvidence(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.txt text eol=crlf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", ".gitattributes")
	runGit(t, root, gitPath, "commit", "--no-gpg-sign", "-m", "attributes")
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	result, err := newCaptureTestAdapter(t, root, gitPath).Capture(context.Background(), repo, worktree)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	defer func() { _ = result.Abort(context.Background()) }()
	if result.Candidate.Policy.ConversionDecision != "review_only" || result.Candidate.Policy.ConversionFingerprint == "" {
		t.Fatalf("conversion evidence = %#v", result.Candidate.Policy)
	}
}

func TestCaptureRejectsAssumeUnchangedAsNonCandidate(t *testing.T) {
	root, gitPath := initializedRepository(t)
	runGit(t, root, gitPath, "update-index", "--assume-unchanged", "--", "tracked file.txt")
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := newTestResolver(t, root, gitPath)
	repo, worktree, err := resolver.ResolveRepository(context.Background(), root)
	if err != nil {
		t.Fatal(describeGitError(err))
	}
	_, err = newCaptureTestAdapter(t, root, gitPath).Capture(context.Background(), repo, worktree)
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Code != ErrorLocalCaptureIncomplete {
		t.Fatalf("assume-unchanged capture error = %v, want local_capture_incomplete", err)
	}
}

func newCaptureTestAdapter(t *testing.T, root, gitPath string) *LocalCaptureAdapter {
	t.Helper()
	policy := app.DefaultResourcePolicy()
	policy.Artifact.CaptureDeltaBytes = 128 * app.KiB
	policy.Storage.MinimumFreeBytes = 1 * app.KiB
	policy.Storage.RecoveryFileBytes = 512
	policy.Storage.RepositorySoftBytes = 1 * app.MiB
	policy.Storage.RepositoryHardBytes = 2 * app.MiB
	policy.Storage.GlobalSoftBytes = 1 * app.MiB
	policy.Storage.GlobalHardBytes = 2 * app.MiB
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	capacity, err := capacitystore.NewManager(filepath.Join(t.TempDir(), "capacity"))
	if err != nil {
		t.Fatal(err)
	}
	spools, err := artifactspool.NewManager(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	operation, err := domain.NewOperationID("capture-test")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{Kind: process.ExecutableGit, SearchPath: os.Getenv("PATH"), CurrentDir: root})
	if err != nil || identity.CanonicalPath != gitPath {
		t.Fatalf("trusted Git identity = %v, %q; want %q", err, identity.CanonicalPath, gitPath)
	}
	adapter, err := NewLocalCaptureAdapter(LocalCaptureConfig{
		Executable:     identity,
		Runner:         process.NewRunner(),
		Policy:         policy,
		Capacity:       capacity,
		Spools:         spools,
		OperationID:    operation,
		VolumeID:       "capture-volume",
		VolumeEvidence: []app.VolumeEvidence{{ID: "capture-volume", Free: 8 * app.MiB, Mode: app.VolumeCapacityMonitored, Stable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}
