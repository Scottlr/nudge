package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestLifecycleInstallsAndResetsIndependentBaselineAndResult(t *testing.T) {
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	allocator, err := NewAllocator(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	store := newWorkspaceTestStore()
	creation := workspaceCreateRequest(store, destination)
	lease, guard, err := allocator.Create(context.Background(), creation)
	if err != nil {
		t.Fatal(err)
	}
	handle := lease.Handle()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}

	policy := app.DefaultResourcePolicy()
	installID := domain.OperationID("lifecycle-install")
	installPlan := lifecycleTestPlan(policy, installID)
	source := workspaceTestSource{
		identity: app.WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-lifecycle", ManifestHash: strings.Repeat("a", 64)},
		entries: []repository.TreeEntry{
			treeEntryForPath(repository.RepoPath("pkg/example.go"), repository.FileKindRegular, 0o100644),
			treeEntryForPath(repository.RepoPath("pkg/readme.txt"), repository.FileKindRegular, 0o100644),
		},
		content: map[repository.RepoPathKey][]byte{
			repository.RepoPath("pkg/example.go").Key(): []byte("package example\n"),
			repository.RepoPath("pkg/readme.txt").Key(): []byte("baseline text\n"),
		},
	}
	request := LifecycleRequest{Store: store, Allocator: allocator, Capacity: creation.Capacity, CapacityPlan: installPlan, CapacityPolicy: policy, CapacityEvidence: creation.CapacityEvidence, Guard: guard, Handle: handle, Workspace: store.workspace, OperationID: installID, Owner: "session-owner", Now: time.Now().UTC().Truncate(time.Microsecond)}
	result, err := NewLifecycle().InstallBaseline(context.Background(), InstallBaselineRequest{LifecycleRequest: request, Source: source})
	if err != nil {
		t.Fatalf("InstallBaseline() error = %v", err)
	}
	if result.Evidence.Phase != app.WorkspaceLifecycleReady || store.workspace.State != review.WorkspaceReady {
		t.Fatalf("installed lifecycle/workspace = %#v/%#v", result.Evidence, store.workspace)
	}
	baselinePath := filepath.Join(handle.Roots.Baseline.Path(), "pkg", "example.go")
	resultPath := filepath.Join(handle.Roots.Result.Path(), "pkg", "example.go")
	baselineInfo, err := os.Stat(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	resultInfo, err := os.Stat(resultPath)
	if err != nil || os.SameFile(baselineInfo, resultInfo) {
		t.Fatalf("baseline/result identity = %v/%v", baselineInfo, resultInfo)
	}
	if err := os.WriteFile(resultPath, []byte("provider edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(baselinePath); err != nil || string(data) != "package example\n" {
		t.Fatalf("baseline after result edit = %q/%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(handle.Roots.Result.Path(), "provider-residue.txt"), []byte("untrusted"), 0o600); err != nil {
		t.Fatal(err)
	}

	resetID := domain.OperationID("lifecycle-reset")
	resetPlan := lifecycleTestPlan(policy, resetID)
	resetRequest := request
	resetRequest.CapacityPlan = resetPlan
	resetRequest.Guard = result.Guard
	resetRequest.Workspace = store.workspace
	resetRequest.OperationID = resetID
	resetRequest.Now = time.Now().UTC().Truncate(time.Microsecond)
	reset, err := NewLifecycle().ResetResult(context.Background(), ResetResultRequest{LifecycleRequest: resetRequest})
	if err != nil {
		t.Fatalf("ResetResult() error = %v", err)
	}
	if reset.Evidence.Phase != app.WorkspaceLifecycleReady || store.workspace.State != review.WorkspaceReady {
		t.Fatalf("reset lifecycle/workspace = %#v/%#v", reset.Evidence, store.workspace)
	}
	if _, err := os.Stat(filepath.Join(handle.Roots.Result.Path(), "provider-residue.txt")); !os.IsNotExist(err) {
		t.Fatalf("residue stat error = %v, want not exist", err)
	}
	if data, err := os.ReadFile(resultPath); err != nil || string(data) != "package example\n" {
		t.Fatalf("result after reset = %q/%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(handle.Roots.Result.Path(), "pkg", "readme.txt")); err != nil || string(data) != "baseline text\n" {
		t.Fatalf("second result after reset = %q/%v", data, err)
	}

	advanceID := domain.OperationID("lifecycle-advance")
	advancePlan := lifecycleTestPlan(policy, advanceID)
	advanceRequest := resetRequest
	advanceRequest.CapacityPlan = advancePlan
	advanceRequest.Guard = reset.Guard
	advanceRequest.Workspace = store.workspace
	advanceRequest.OperationID = advanceID
	advanceRequest.Now = time.Now().UTC().Truncate(time.Microsecond)
	advancedSource := workspaceTestSource{
		identity: app.WorkspaceSourceIdentity{Kind: "accepted_capture", ID: "capture-advanced", ManifestHash: strings.Repeat("d", 64)},
		entries:  []repository.TreeEntry{treeEntryForPath(repository.RepoPath("pkg/example.go"), repository.FileKindRegular, 0o100644)},
		content:  map[repository.RepoPathKey][]byte{repository.RepoPath("pkg/example.go").Key(): []byte("package advanced\n")},
	}
	advanced, err := NewLifecycle().AdvanceBaseline(context.Background(), AdvanceBaselineRequest{LifecycleRequest: advanceRequest, Source: advancedSource, Apply: ApplyVerification{ProposalID: domain.ProposalID("proposal-applied"), WorktreeID: advanceRequest.Workspace.WorktreeID, VerifiedAt: time.Now().UTC(), Verified: true}})
	if err != nil {
		t.Fatalf("AdvanceBaseline() error = %v", err)
	}
	if advanced.Evidence.Phase != app.WorkspaceLifecycleReady {
		t.Fatalf("advanced lifecycle = %#v", advanced.Evidence)
	}
	if data, err := os.ReadFile(baselinePath); err != nil || string(data) != "package advanced\n" {
		t.Fatalf("baseline after advance = %q/%v", data, err)
	}
	if data, err := os.ReadFile(resultPath); err != nil || string(data) != "package advanced\n" {
		t.Fatalf("result after advance = %q/%v", data, err)
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("destination after lifecycle = %v/%d", err, len(entries))
	}
}

func lifecycleTestPlan(policy app.ResourcePolicy, operationID domain.OperationID) app.CapacityPlan {
	return app.CapacityPlan{OperationID: operationID, PolicyVersion: policy.Version, Artifacts: []app.ArtifactEstimate{{Class: app.ArtifactSnapshot, Entries: 2, Bytes: 128, LargestItem: 64}}, VolumePeaks: []app.VolumePeak{{ID: "volume", Finals: 128, Reserve: policy.Storage.MinimumFreeBytes}}}
}
