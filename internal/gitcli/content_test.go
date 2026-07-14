package gitcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/artifactspool"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

func TestLoadLocalBaseAndHeadContent(t *testing.T) {
	root, gitPath := initializedRepository(t)
	gitIdentity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:       process.ExecutableGit,
		SearchPath: os.Getenv("PATH"),
		CurrentDir: root,
	})
	if err != nil || gitIdentity.CanonicalPath != gitPath {
		t.Fatalf("trusted Git identity = %v, %q; want %q", err, gitIdentity.CanonicalPath, gitPath)
	}
	headOutput := runGit(t, root, gitPath, "rev-parse", "HEAD")
	head, err := repository.NewObjectID(string(headOutput[:len(headOutput)-1]))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: head}
	loader, err := NewContentLoader(ContentLoaderConfig{
		Executable:      gitIdentity,
		StartPath:       root,
		MaxContentBytes: 1 * app.MiB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("new working tree bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := loader.LoadFile(context.Background(), "", snapshot, repository.RepoPath("tracked file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content.Bytes) != "initial\n" || content.Truncated || content.Binary {
		t.Fatalf("pinned content = %#v", content)
	}
	empty, err := loader.LoadFile(context.Background(), "", repository.SnapshotRef{Kind: repository.SnapshotEmpty}, repository.RepoPath("new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Bytes) != 0 || empty.Truncated || empty.ContentHash == "" {
		t.Fatalf("empty content = %#v", empty)
	}
}

func TestLoadGitContentReturnsBoundedLimit(t *testing.T) {
	root, gitPath := initializedRepository(t)
	gitIdentity, err := process.NewExecutableResolver().Resolve(context.Background(), process.ResolveExecutableRequest{
		Kind:       process.ExecutableGit,
		SearchPath: os.Getenv("PATH"),
		CurrentDir: root,
	})
	if err != nil || gitIdentity.CanonicalPath != gitPath {
		t.Fatalf("trusted Git identity = %v, %q; want %q", err, gitIdentity.CanonicalPath, gitPath)
	}
	headOutput := runGit(t, root, gitPath, "rev-parse", "HEAD")
	head, err := repository.NewObjectID(string(headOutput[:len(headOutput)-1]))
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewContentLoader(ContentLoaderConfig{
		Executable:      gitIdentity,
		StartPath:       root,
		MaxContentBytes: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := loader.LoadFile(context.Background(), domain.CaptureID(""), repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: head}, repository.RepoPath("tracked file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !content.Truncated || content.LimitReason != "content_limit" || string(content.Bytes) != "ini" {
		t.Fatalf("bounded content = %#v", content)
	}
}

func TestLoadCaptureAfterWorktreeDrift(t *testing.T) {
	root, gitPath := initializedRepository(t)
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("captured working bytes\n"), 0o644); err != nil {
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
	if result.Candidate.EntryCount != 1 {
		t.Fatalf("capture entries = %d, want 1", result.Candidate.EntryCount)
	}
	manager, ok := adapter.spools.(*artifactspool.Manager)
	if !ok {
		t.Fatal("capture spool manager was not concrete")
	}
	captureID, err := domain.NewCaptureID("content-capture")
	if err != nil {
		t.Fatal(err)
	}
	patchIdentity := app.ArtifactIdentity{SpoolID: result.Candidate.Patch.SpoolID, ManifestHash: result.Candidate.Patch.ManifestHash, Bytes: app.ByteSize(result.Candidate.Patch.Bytes), Entries: 1, Complete: true, VerifiedAt: result.Candidate.Patch.VerifiedAt}
	blobIdentity := app.ArtifactIdentity{SpoolID: result.Candidate.BlobSpool.SpoolID, ManifestHash: result.Candidate.BlobSpool.ManifestHash, Bytes: app.ByteSize(result.Candidate.BlobSpool.Bytes), Entries: app.Count(result.Candidate.BlobSpool.Entries), Complete: true, VerifiedAt: result.Candidate.BlobSpool.VerifiedAt}
	patchTarget := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "captures/content-capture/patch", SourceRelativePath: "patch"}
	blobTarget := app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "captures/content-capture/blobs"}
	if _, err := result.PatchSpool.Publish(context.Background(), patchIdentity, patchTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := result.BlobSpool.Publish(context.Background(), blobIdentity, blobTarget); err != nil {
		t.Fatal(err)
	}
	manifest := app.CaptureManifest{
		Version: app.LocalCaptureManifestVersion, CaptureID: captureID, RepositoryID: result.Candidate.RepositoryID, WorktreeID: result.Candidate.WorktreeID,
		Candidate: result.Candidate,
		Patch:     app.CaptureArtifactRef{Kind: repository.CaptureArtifactPatch, Identity: patchIdentity, Target: patchTarget},
		Blobs:     app.CaptureArtifactRef{Kind: repository.CaptureArtifactBlobs, Identity: blobIdentity, Target: blobTarget, RelativePath: "payload"},
		CreatedAt: time.Now().UTC(),
	}
	manifest.ManifestHash, err = app.CaptureManifestHash(result.Candidate, patchIdentity, blobIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	loader, err := NewContentLoader(ContentLoaderConfig{Executable: adapter.executable, Runner: process.NewRunner(), StartPath: root, Manifests: fixedManifestReader{manifest: manifest}, Artifacts: manager})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Candidate.Entries[0]
	snapshot := repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: result.Candidate.WorktreeID, Fingerprint: result.Candidate.Fingerprint}
	content, err := loader.LoadFile(context.Background(), captureID, snapshot, *entry.Change.NewPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content.Bytes) != "captured working bytes\n" || content.Truncated {
		t.Fatalf("captured content = %#v", content)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked file.txt"), []byte("newer live bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := loader.LoadFile(context.Background(), captureID, snapshot, *entry.Change.NewPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Bytes) != "captured working bytes\n" {
		t.Fatalf("capture changed after worktree drift = %q", again.Bytes)
	}
}

type fixedManifestReader struct{ manifest app.CaptureManifest }

func (r fixedManifestReader) OpenCaptureManifest(_ context.Context, id domain.CaptureID) (app.CaptureManifest, error) {
	if id != r.manifest.CaptureID {
		return app.CaptureManifest{}, app.ErrCaptureNotFound
	}
	return r.manifest, nil
}
