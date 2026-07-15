package gitcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/paths"
	"github.com/Scottlr/nudge/internal/process"
)

func TestProposalPatchGeneratorAndArtifactIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	admin, baselineRoot, resultRoot := filepath.Join(root, "admin"), filepath.Join(root, "baseline"), filepath.Join(root, "result")
	for _, path := range []string{admin, baselineRoot, resultRoot} {
		if err := paths.EnsurePrivateDir(path); err != nil {
			t.Fatalf("private dir %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(baselineRoot, "main.go"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "main.go"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "binary.dat"), []byte{0, 1, 2, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	baseEntry := testPatchWorkspaceEntry(t, "main.go", []byte("before\n"), 0o100644)
	resultEntries := []app.ResultSnapshotEntry{
		{Path: []byte("main.go"), Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: 6, SHA256: testPatchHash([]byte("after\n")), NativeIdentityHash: strings.Repeat("a", 64), Complete: true},
		{Path: []byte("binary.dat"), Kind: repository.FileKindRegular, Mode: 0o100644, Bytes: 4, SHA256: testPatchHash([]byte{0, 1, 2, 255}), ContentClass: repository.ContentClassRegularBinary, NativeIdentityHash: strings.Repeat("b", 64), Complete: true},
	}
	baseline, err := app.NewWorkspaceManifest([]app.WorkspaceManifestEntry{baseEntry})
	if err != nil {
		t.Fatal(err)
	}
	resultManifest, err := app.NewResultManifest(resultEntries, app.DefaultResourcePolicy().Version, true, app.ResultReasonNone)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := app.CompareResultManifest(baseline, resultManifest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot, err := app.NewResultSnapshot(app.ResultSnapshot{
		SessionID: "session-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", WorktreeID: "worktree-1", AttemptID: "attempt-1", ThreadID: "thread-1", ProviderTurnID: "turn-1", ProviderTurnRef: "opaque-turn", Baseline: review.SnapshotIdentity{ID: "baseline-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: baseline.Hash}, Result: review.SnapshotIdentity{ID: domain.ReviewSnapshotID("result-" + resultManifest.Hash), Ref: repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: "worktree-1", Fingerprint: resultManifest.Hash}, ManifestHash: resultManifest.Hash}, Manifest: resultManifest, Delta: delta, PolicyVersion: app.DefaultResourcePolicy().Version, IsolationVersion: 1, LeaseNonce: strings.Repeat("c", 64), State: app.ResultSnapshotReady, Reason: app.ResultReasonNone, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := process.NewExecutableResolver().Resolve(ctx, process.ResolveExecutableRequest{Kind: process.ExecutableGit, SearchPath: os.Getenv("PATH"), CurrentDir: root})
	if err != nil {
		t.Fatal(err)
	}
	generator, err := NewProposalPatchGenerator(ProposalPatchGeneratorConfig{Executable: identity, Runner: process.NewRunner(), Policy: DefaultMachineGitReadPolicyV1(), Format: DefaultPatchFormatV1(), Rename: DefaultRenamePolicyV1(), Conversion: DefaultContentConversionPolicyV1()})
	if err != nil {
		t.Fatal(err)
	}
	patchPath := filepath.Join(root, "patch")
	patchFile, err := os.OpenFile(patchPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	streamIdentity, err := generator.Generate(ctx, ProposalPatchRequest{AdminRoot: admin, BaselineRoot: baselineRoot, ResultRoot: resultRoot, Baseline: baseline, Result: snapshot, ResourcePolicy: app.DefaultResourcePolicy(), ConversionDecision: "byte_neutral"}, patchFile)
	if closeErr := patchFile.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	patchSource := &testPatchFileSource{file: mustOpenPatch(t, patchPath), id: "patch-spool-1"}
	defer patchSource.file.Close()
	limits, err := app.DefaultSpoolLimits(app.DefaultResourcePolicy())
	if err != nil {
		t.Fatal(err)
	}
	published := app.PublishedArtifact{Identity: app.ArtifactIdentity{SpoolID: patchSource.id, ManifestHash: strings.Repeat("e", 64), Bytes: streamIdentity.Bytes, Entries: 1, Complete: true, VerifiedAt: now}, Target: app.PublishTarget{OwnerKind: app.OwnerProposal, RelativePath: "patch"}, Limits: limits}
	artifact, err := app.BuildProposalPatchArtifact(ctx, app.ProposalPatchBuildInput{Source: patchSource, Published: published, PatchSHA256: streamIdentity.SHA256, Baseline: baseline, Result: snapshot, PatchFormatVersion: app.ProposalPatchFormatVersion, RenamePolicyVersion: DefaultRenamePolicyV1().Version, ConversionPolicyVersion: DefaultContentConversionPolicyV1().Version, ConversionFingerprint: strings.Repeat("d", 64), ResourcePolicy: app.DefaultResourcePolicy(), SessionID: "session-1", ProposalID: "proposal-1", WorkspaceID: "workspace-1", AttemptID: "attempt-1", ThreadID: "thread-1", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.ID == "" || artifact.Summary.FileCount != 2 || artifact.Index.PatchSHA256 != streamIdentity.SHA256 {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func testPatchWorkspaceEntry(t *testing.T, path string, data []byte, mode uint32) app.WorkspaceManifestEntry {
	t.Helper()
	return app.WorkspaceManifestEntry{Path: []byte(path), Kind: repository.FileKindRegular, Mode: mode, Bytes: uint64(len(data)), SHA256: testPatchHash(data)}
}

func testPatchHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type testPatchFileSource struct {
	file *os.File
	id   string
}

func (s *testPatchFileSource) ID() string { return s.id }
func (s *testPatchFileSource) Size() int64 {
	info, _ := s.file.Stat()
	return info.Size()
}
func (s *testPatchFileSource) Open() (io.ReadCloser, error) { return os.Open(s.file.Name()) }
func (s *testPatchFileSource) ReadAt(data []byte, offset int64) (int, error) {
	return s.file.ReadAt(data, offset)
}

func mustOpenPatch(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return file
}

var _ diff.PatchSource = (*testPatchFileSource)(nil)
