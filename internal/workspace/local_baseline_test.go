package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestLocalBaselineMaterializesExactAcceptedGeneration(t *testing.T) {
	request, source := localBaselineFixture(t, []byte("package main\n"))
	allocator, store, handle, lease := localBaselineWorkspace(t)
	defer lease.Close()
	_ = allocator
	_ = store

	baseline, err := NewLocalBaseline(request, source, func(context.Context, app.CaptureGeneration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := baseline.Materialize(context.Background(), handle.Roots.Baseline)
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if result.Generation.Fingerprint != request.Generation.Fingerprint || len(result.Manifest.Entries) != 1 {
		t.Fatalf("baseline evidence = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(handle.Roots.Baseline.Path(), "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("materialized data = %q/%v", data, err)
	}
}

func TestLocalBaselineRejectsUnsupportedBytesAndPathCollisions(t *testing.T) {
	request, source := localBaselineFixture(t, []byte{0xff, 0x00})
	if _, err := NewLocalBaseline(request, source, func(context.Context, app.CaptureGeneration) error { return nil }); !errors.Is(err, app.ErrProposalBaselineUnsupported) {
		t.Fatalf("invalid text error = %v", err)
	}

	request, source = localBaselineFixture(t, []byte("safe\n"))
	file := treeEntryForPath(repository.RepoPath("a"), repository.FileKindRegular, 0o100644)
	child := treeEntryForPath(repository.RepoPath("a/b.txt"), repository.FileKindRegular, 0o100644)
	source.entries = append(source.entries, file, child)
	source.content[file.Path.Key()] = []byte("one\n")
	source.content[child.Path.Key()] = []byte("two\n")
	baseline, err := NewLocalBaseline(request, source, func(context.Context, app.CaptureGeneration) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseline.List(context.Background()); err == nil {
		t.Fatal("path collision unexpectedly accepted")
	}
}

func TestLocalBaselineStopsOnGenerationChange(t *testing.T) {
	request, source := localBaselineFixture(t, []byte("safe\n"))
	stale := false
	baseline, err := NewLocalBaseline(request, source, func(context.Context, app.CaptureGeneration) error {
		if stale {
			return errors.New("generation changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stale = true
	if _, err := baseline.List(context.Background()); !errors.Is(err, app.ErrProposalBaselineStale) {
		t.Fatalf("stale generation error = %v", err)
	}
}

func localBaselineWorkspace(t *testing.T) (*Allocator, *workspaceTestStore, WorkspaceHandle, *WorkspaceLease) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspaces")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	allocator, err := NewAllocator(root)
	if err != nil {
		t.Fatal(err)
	}
	store := newWorkspaceTestStore()
	lease, _, err := allocator.Create(context.Background(), workspaceCreateRequest(store, destination))
	if err != nil {
		t.Fatal(err)
	}
	return allocator, store, lease.Handle(), lease
}

func localBaselineFixture(t *testing.T, content []byte) (app.ProposalBaselineRequest, workspaceTestSource) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	path := repository.RepoPath("main.go")
	digest := sha256.Sum256(content)
	contentHash := hex.EncodeToString(digest[:])
	manifestHash := strings.Repeat("a", 64)
	baseID := repository.ObjectID(strings.Repeat("1", 40))
	blob := repository.CaptureBlobRef{Side: repository.CaptureBlobWorkingTree, Path: path, Artifact: repository.CaptureArtifact{Kind: repository.CaptureArtifactBlobs, SpoolID: "blobs", ManifestHash: strings.Repeat("b", 64), RelativePath: "payload/main.go", Bytes: uint64(len(content)), Entries: 1, ContentSHA256: contentHash, VerifiedAt: now}}
	change := repository.ChangedFile{NewPath: &path, Kind: repository.ChangeUntracked, NewFileKind: repository.FileKindRegular, NewMode: 0o100644, Binary: bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content), Unstaged: true}
	candidate := repository.LocalCaptureCandidate{
		Version:      repository.LocalCaptureCandidateVersion,
		RepositoryID: "repo",
		WorktreeID:   "worktree",
		Base:         repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: baseID},
		Policy:       repository.CapturePolicyEvidence{MachineGitVersion: 1, RenameVersion: 1, RenameOutcome: "none", PatchFormatVersion: 1, ConversionPolicyVersion: 1, ConversionDecision: "byte_neutral", ConversionFingerprint: strings.Repeat("c", 64), ResourcePolicyVersion: uint32(app.CurrentResourcePolicyVersion)},
		Consistency:  repository.CaptureConsistencyEvidence{HeadToken: manifestHash, IndexToken: manifestHash, StatusToken: manifestHash, FlagsToken: manifestHash, FilesystemToken: manifestHash, AggregateToken: manifestHash},
		Entries:      []repository.LocalCaptureEntry{{Change: change, Blobs: []repository.CaptureBlobRef{blob}}},
		EntryCount:   1,
		Patch:        repository.CaptureArtifact{Kind: repository.CaptureArtifactPatch, SpoolID: "patch", ManifestHash: strings.Repeat("d", 64), RelativePath: "patch.bin", Bytes: 1, Entries: 1, ContentSHA256: strings.Repeat("e", 64), VerifiedAt: now},
		BlobSpool:    repository.CaptureArtifact{Kind: repository.CaptureArtifactBlobs, SpoolID: "blobs", ManifestHash: strings.Repeat("b", 64), RelativePath: "payload", Bytes: uint64(len(content)), Entries: 1, ContentSHA256: contentHash, VerifiedAt: now},
		TotalBytes:   uint64(len(content)) + 1,
		CapturedAt:   now,
	}
	fingerprint, err := candidate.FingerprintValue()
	if err != nil {
		t.Fatal(err)
	}
	candidate.Fingerprint = fingerprint
	patchIdentity := app.ArtifactIdentity{SpoolID: "patch", ManifestHash: candidate.Patch.ManifestHash, Bytes: 1, Entries: 1, Complete: true, VerifiedAt: now}
	blobsIdentity := app.ArtifactIdentity{SpoolID: "blobs", ManifestHash: candidate.BlobSpool.ManifestHash, Bytes: app.ByteSize(len(content)), Entries: 1, Complete: true, VerifiedAt: now}
	acceptedManifestHash, err := app.CaptureManifestHash(candidate, patchIdentity, blobsIdentity)
	if err != nil {
		t.Fatal(err)
	}
	manifest := app.CaptureManifest{Version: app.LocalCaptureManifestVersion, CaptureID: "capture-local", RepositoryID: candidate.RepositoryID, WorktreeID: candidate.WorktreeID, Candidate: candidate, Patch: app.CaptureArtifactRef{Kind: repository.CaptureArtifactPatch, Identity: patchIdentity, Target: app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "capture/patch", SourceRelativePath: candidate.Patch.RelativePath}}, Blobs: app.CaptureArtifactRef{Kind: repository.CaptureArtifactBlobs, Identity: blobsIdentity, Target: app.PublishTarget{OwnerKind: app.OwnerCapture, RelativePath: "capture/blobs"}, RelativePath: "payload"}, ManifestHash: acceptedManifestHash, CreatedAt: now}
	generation := app.CaptureGeneration{CaptureID: manifest.CaptureID, Generation: 1, RepositoryID: manifest.RepositoryID, WorktreeID: manifest.WorktreeID, Fingerprint: candidate.Fingerprint, ManifestHash: manifest.ManifestHash, Base: candidate.Base, CreatedAt: now}
	policy := app.NewCapabilityPolicyV1()
	decision := CapabilityDecisionForLocalBaseline(policy, path.Key(), manifest.CaptureID)
	evaluation := app.CapturePolicyEvaluation{CaptureID: manifest.CaptureID, CaptureFormatVersion: manifest.Version, PolicyVersion: policy.Version, ResourcePolicyVersion: policy.ResourcePolicyVersion, EvidenceVersion: policy.EvidenceVersion, Decisions: []app.CapabilityDecision{decision}, ManifestHash: manifest.ManifestHash}
	request := app.ProposalBaselineRequest{Generation: generation, Manifest: manifest, PolicyEvaluation: evaluation, Policy: policy, ResourcePolicy: app.DefaultResourcePolicy()}
	source := workspaceTestSource{identity: app.WorkspaceSourceIdentity{Kind: "accepted_capture", ID: string(manifest.CaptureID), ManifestHash: manifest.ManifestHash}, entries: []repository.TreeEntry{treeEntryForPath(path, repository.FileKindRegular, 0o100644)}, content: map[repository.RepoPathKey][]byte{path.Key(): append([]byte(nil), content...)}}
	return request, source
}

func CapabilityDecisionForLocalBaseline(policy app.CapabilityPolicyV1, path repository.RepoPathKey, captureID domain.CaptureID) app.CapabilityDecision {
	reasons := map[app.CapabilityAxis][]app.CapabilityReason{}
	for _, axis := range []app.CapabilityAxis{app.CapabilityReview, app.CapabilityAnchor, app.CapabilityMaterializeReviewSnapshot, app.CapabilityPropose, app.CapabilityApply} {
		reasons[axis] = nil
	}
	return app.CapabilityDecision{Key: app.CapabilityKey{Path: path, CaptureID: captureID, PolicyVersion: policy.Version, ResourcePolicyVersion: policy.ResourcePolicyVersion, EvidenceVersion: policy.EvidenceVersion}, Review: true, Anchor: true, MaterializeReviewSnapshot: true, Propose: true, Apply: true, ReasonsByAxis: reasons, PolicyVersion: policy.Version, ResourcePolicyVersion: policy.ResourcePolicyVersion, EvidenceVersion: policy.EvidenceVersion}
}
