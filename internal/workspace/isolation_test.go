package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

func TestIsolationContractRejectsMissingNativeBoundary(t *testing.T) {
	roots := testIsolationRoots(t)
	_, err := NewIsolationContract(roots, IsolationCapability{}, monitoredGrowth())
	if !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("NewIsolationContract() error = %v, want ErrIsolationUnavailable", err)
	}
}

func TestIsolationContractRequiresFourNonOverlappingRoots(t *testing.T) {
	root := t.TempDir()
	growth := monitoredGrowth()
	_, err := NewIsolationContract(IsolationRoots{
		Baseline:    filepath.Join(root, "baseline"),
		Admin:       filepath.Join(root, "admin"),
		Result:      filepath.Join(root, "result"),
		Destination: root,
	}, fullCapability(), growth)
	if !errors.Is(err, ErrInvalidIsolationRoots) {
		t.Fatalf("NewIsolationContract() error = %v, want ErrInvalidIsolationRoots", err)
	}
}

func TestIsolationContractRejectsHardLinkAliasEvidence(t *testing.T) {
	roots := testIsolationRoots(t)
	adminFile := filepath.Join(roots.Admin, "private")
	resultFile := filepath.Join(roots.Result, "alias")
	if err := os.WriteFile(adminFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(adminFile, resultFile); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hard links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	adminInfo, err := os.Stat(adminFile)
	if err != nil {
		t.Fatal(err)
	}
	resultInfo, err := os.Stat(resultFile)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(adminInfo, resultInfo) {
		t.Fatal("fixture did not create a shared inode")
	}
	if err := os.WriteFile(resultFile, []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(adminFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "mutated" {
		t.Fatal("shared alias fixture did not demonstrate the escape")
	}
	capability := fullCapability()
	capability.NoHardLinkAlias = false
	_, err = NewIsolationContract(roots, capability, monitoredGrowth())
	if !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("NewIsolationContract() error = %v, want ErrIsolationUnavailable", err)
	}
}

func TestIsolationContractRequiresQuiescentTurnBeforeSnapshot(t *testing.T) {
	contract, err := NewIsolationContract(testIsolationRoots(t), fullCapability(), monitoredGrowth())
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.VerifyQuiescence(QuiescenceProof{DescendantsEmpty: true, WritableHandlesClosed: false, ResultRootStable: true}); !errors.Is(err, ErrQuiescenceUnproven) {
		t.Fatalf("VerifyQuiescence() error = %v, want ErrQuiescenceUnproven", err)
	}
	if err := contract.VerifyQuiescence(QuiescenceProof{DescendantsEmpty: true, WritableHandlesClosed: true, ResultRootStable: true}); err != nil {
		t.Fatalf("VerifyQuiescence() error = %v", err)
	}
}

func TestIsolationContractReportsMonitoredGrowthLimit(t *testing.T) {
	contract, err := NewIsolationContract(testIsolationRoots(t), fullCapability(), monitoredGrowth())
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.GrowthExceeded(contract.Growth.LimitBytes + 1); !errors.Is(err, ErrWorkspaceGrowthLimit) {
		t.Fatalf("GrowthExceeded() error = %v, want ErrWorkspaceGrowthLimit", err)
	}
}

func TestIsolationContractRejectsUnprovenNativeEscapeClasses(t *testing.T) {
	cases := []struct {
		name string
		set  func(*IsolationCapability)
	}{
		{name: "symlink", set: func(value *IsolationCapability) { value.NoSymlinkEscape = false }},
		{name: "reparse-or-junction", set: func(value *IsolationCapability) { value.NoJunctionEscape = false }},
		{name: "mount-or-bind", set: func(value *IsolationCapability) { value.NoMountEscape = false }},
		{name: "hard-link", set: func(value *IsolationCapability) { value.NoHardLinkAlias = false }},
		{name: "shared-clone", set: func(value *IsolationCapability) { value.NoSharedCloneAlias = false }},
		{name: "detached-descendant", set: func(value *IsolationCapability) { value.DescendantsContained = false }},
		{name: "environment-backchannel", set: func(value *IsolationCapability) { value.EnvironmentSanitized = false }},
		{name: "inherited-writable-handle", set: func(value *IsolationCapability) { value.NoInheritedWritableHandles = false }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capability := fullCapability()
			testCase.set(&capability)
			_, err := NewIsolationContract(testIsolationRoots(t), capability, monitoredGrowth())
			if !errors.Is(err, ErrIsolationUnavailable) {
				t.Fatalf("NewIsolationContract() error = %v, want ErrIsolationUnavailable", err)
			}
		})
	}
}

func testIsolationRoots(t *testing.T) IsolationRoots {
	t.Helper()
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "baseline"),
		filepath.Join(root, "admin"),
		filepath.Join(root, "result"),
		filepath.Join(root, "destination"),
	}
	for _, path := range paths {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return IsolationRoots{Baseline: paths[0], Admin: paths[1], Result: paths[2], Destination: paths[3]}
}

func fullCapability() IsolationCapability {
	return IsolationCapability{
		FilesystemBoundary: true, DescendantsContained: true, NetworkDisabled: true,
		EnvironmentSanitized: true, NoInheritedWritableHandles: true,
		NoSymlinkEscape: true, NoJunctionEscape: true, NoMountEscape: true,
		NoHardLinkAlias: true, NoSharedCloneAlias: true,
	}
}

func monitoredGrowth() GrowthPolicy {
	return GrowthPolicy{
		Mode: app.VolumeCapacityMonitored, LimitBytes: 1024, ReserveBytes: 2048, RecoveryReserveBytes: 256,
		RecheckBytes: 256, RecheckInterval: time.Second, CancelOnLimit: true,
	}
}
