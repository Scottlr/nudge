package workspace

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/paths"
)

func TestFreezeResultBuildsCompleteDeltaWithoutTrustingGitMetadata(t *testing.T) {
	root := t.TempDir()
	baselineRoot := filepath.Join(root, "baseline")
	adminRoot := filepath.Join(root, "admin")
	resultRoot := filepath.Join(root, "result")
	destinationRoot := filepath.Join(root, "destination")
	for _, path := range []string{baselineRoot, adminRoot, resultRoot, destinationRoot} {
		if err := paths.EnsurePrivateDir(path); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "main.go"), []byte("changed\n"), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "copy.go"), []byte("same\n"), 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, "keep.go"), []byte("same\n"), 0o600); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	if err := os.Mkdir(filepath.Join(resultRoot, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resultRoot, ".git", "config"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write git metadata: %v", err)
	}

	policy := app.DefaultResourcePolicy()
	baseline, err := app.NewWorkspaceManifest([]app.WorkspaceManifestEntry{
		workspaceManifestEntry(t, "main.go", "base\n", resultFileMode(t, filepath.Join(resultRoot, "main.go"))),
		workspaceManifestEntry(t, "old.go", "same\n", resultFileMode(t, filepath.Join(resultRoot, "copy.go"))),
		workspaceManifestEntry(t, "keep.go", "same\n", resultFileMode(t, filepath.Join(resultRoot, "keep.go"))),
	})
	if err != nil {
		t.Fatalf("baseline manifest: %v", err)
	}
	handle := testFreezeHandle(t, baselineRoot, adminRoot, resultRoot, destinationRoot)
	isolation, err := NewIsolationContract(
		IsolationRoots{Baseline: baselineRoot, Admin: adminRoot, Result: resultRoot, Destination: destinationRoot},
		IsolationCapability{FilesystemBoundary: true, DescendantsContained: true, NetworkDisabled: true, EnvironmentSanitized: true, NoInheritedWritableHandles: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, NoSharedCloneAlias: true},
		GrowthPolicy{Mode: app.VolumeCapacityMonitored, LimitBytes: policy.Artifact.SnapshotBytes, ReserveBytes: 1, RecoveryReserveBytes: 1, RecheckBytes: 1, RecheckInterval: time.Second, CancelOnLimit: true},
	)
	if err != nil {
		t.Fatalf("isolation: %v", err)
	}
	snapshot, err := FreezeResult(context.Background(), &WorkspaceLease{handle: handle}, ResultFreezeRequest{
		SessionID:        "session-1",
		ProposalID:       "proposal-1",
		AttemptID:        "attempt-1",
		ThreadID:         "thread-1",
		ProviderTurnID:   "turn-1",
		ProviderTurnRef:  "opaque-turn",
		Baseline:         baseline,
		BaselineIdentity: review.SnapshotIdentity{ID: "baseline-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: baseline.Hash},
		Isolation:        isolation,
		Quiescence:       QuiescenceProof{DescendantsEmpty: true, WritableHandlesClosed: true, ResultRootStable: true},
		Policy:           policy,
		Now:              time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("freeze result: %v", err)
	}
	if snapshot.State != app.ResultSnapshotNonReady || snapshot.Reason != app.ResultReasonGitAdminPath {
		t.Fatalf("snapshot state/reason = %s/%s", snapshot.State, snapshot.Reason)
	}
	if len(snapshot.Delta.Entries) != 4 {
		t.Fatalf("delta entries = %d, want 4: %#v", len(snapshot.Delta.Entries), snapshot.Delta.Entries)
	}
	if len(snapshot.Delta.Candidates) != 2 || string(snapshot.Delta.Candidates[0].Source) != "keep.go" || string(snapshot.Delta.Candidates[0].Target) != "copy.go" || !snapshot.Delta.Candidates[0].Copy || string(snapshot.Delta.Candidates[1].Source) != "old.go" || string(snapshot.Delta.Candidates[1].Target) != "copy.go" || snapshot.Delta.Candidates[1].Copy {
		t.Fatalf("rename candidates = %#v, entries = %#v", snapshot.Delta.Candidates, snapshot.Delta.Entries)
	}
	var gitMetadataFound bool
	for _, entry := range snapshot.Manifest.Entries {
		if string(entry.Path) == ".git" {
			gitMetadataFound = true
			if entry.Kind != repository.FileKindUnknown || entry.Reason != app.ResultReasonGitAdminPath {
				t.Fatalf("git metadata evidence = %#v", entry)
			}
		}
	}
	if !gitMetadataFound {
		t.Fatal("git metadata was omitted from result manifest")
	}
}

func TestFreezeResultRequiresQuiescence(t *testing.T) {
	root := t.TempDir()
	pathsToCreate := []string{"baseline", "admin", "result", "destination"}
	for _, name := range pathsToCreate {
		if err := paths.EnsurePrivateDir(filepath.Join(root, name)); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	policy := app.DefaultResourcePolicy()
	handle := testFreezeHandle(t, filepath.Join(root, "baseline"), filepath.Join(root, "admin"), filepath.Join(root, "result"), filepath.Join(root, "destination"))
	isolation, err := NewIsolationContract(IsolationRoots{Baseline: filepath.Join(root, "baseline"), Admin: filepath.Join(root, "admin"), Result: filepath.Join(root, "result"), Destination: filepath.Join(root, "destination")}, IsolationCapability{FilesystemBoundary: true, DescendantsContained: true, NetworkDisabled: true, EnvironmentSanitized: true, NoInheritedWritableHandles: true, NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true, NoHardLinkAlias: true, NoSharedCloneAlias: true}, GrowthPolicy{Mode: app.VolumeCapacityMonitored, LimitBytes: policy.Artifact.SnapshotBytes, ReserveBytes: 1, RecoveryReserveBytes: 1, RecheckBytes: 1, RecheckInterval: time.Second, CancelOnLimit: true})
	if err != nil {
		t.Fatalf("isolation: %v", err)
	}
	baseline, err := app.NewWorkspaceManifest(nil)
	if err != nil {
		t.Fatalf("empty baseline: %v", err)
	}
	_, err = FreezeResult(context.Background(), &WorkspaceLease{handle: handle}, ResultFreezeRequest{SessionID: "session-1", ProposalID: "proposal-1", AttemptID: "attempt-1", ThreadID: "thread-1", ProviderTurnID: "turn-1", ProviderTurnRef: "opaque-turn", Baseline: baseline, BaselineIdentity: review.SnapshotIdentity{ID: "baseline-1", Ref: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, ManifestHash: baseline.Hash}, Isolation: isolation, Quiescence: QuiescenceProof{DescendantsEmpty: true, ResultRootStable: true}, Policy: policy, Now: time.Now().UTC()})
	if !errors.Is(err, ErrQuiescenceUnproven) {
		t.Fatalf("freeze error = %v, want ErrQuiescenceUnproven", err)
	}
}

func testFreezeHandle(t *testing.T, baseline, admin, result, destination string) WorkspaceHandle {
	t.Helper()
	root := func(kind RootKind, path string) RootIdentity {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("%s canonical path: %v", kind, err)
		}
		identity, err := paths.NativeDirectoryIdentity(path)
		if err != nil {
			t.Fatalf("%s identity: %v", kind, err)
		}
		return RootIdentity{Kind: kind, Path: canonical, CanonicalPath: canonical, NativeIdentity: identity}
	}
	return WorkspaceHandle{WorkspaceID: "workspace-1", RepositoryID: "repository-1", WorktreeID: "worktree-1", ThreadID: "thread-1", OperationID: "operation-1", Nonce: strings.Repeat("a", 64), IsolationVersion: 1, Roots: newWorkspaceRoots(RootSet{Baseline: root(RootBaseline, baseline), Admin: root(RootAdmin, admin), Result: root(RootResult, result), Destination: root(RootDestination, destination)})}
}

func workspaceManifestEntry(t *testing.T, path, value string, mode uint32) app.WorkspaceManifestEntry {
	t.Helper()
	digest := sha256.Sum256([]byte(value))
	return app.WorkspaceManifestEntry{Path: []byte(path), Kind: repository.FileKindRegular, Mode: mode, Bytes: uint64(len(value)), SHA256: hexDigest(digest[:])}
}

func resultFileMode(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return 0o100000 | uint32(info.Mode().Perm())
}

func hexDigest(value []byte) string {
	const hexDigits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, value := range value {
		result[index*2] = hexDigits[value>>4]
		result[index*2+1] = hexDigits[value&0xf]
	}
	return string(result)
}
