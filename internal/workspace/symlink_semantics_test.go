package workspace

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestEnumerateResultRootRetainsUnsafeSymlinkEvidenceWithoutActionability(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("target", filepath.Join(root, "safe")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("native symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(root, "unsafe")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("native symlink creation unavailable: %v", err)
		}
		t.Fatal(err)
	}
	entries, reason, err := enumerateResultRoot(context.Background(), root, app.DefaultResourcePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if reason != app.ResultReasonPathAlias && reason != app.ResultReasonUnsupportedEntry {
		t.Fatalf("freeze reason = %s", reason)
	}
	byPath := make(map[string]app.ResultSnapshotEntry, len(entries))
	for _, entry := range entries {
		byPath[string(entry.Path)] = entry
	}
	safe := byPath["safe"]
	if safe.Kind != repository.FileKindSymlink || safe.Reason != app.ResultReasonNone || safe.SymlinkEvidence == nil || !safe.SymlinkEvidence.IsActionable() {
		t.Fatalf("safe symlink = %+v", safe)
	}
	unsafe := byPath["unsafe"]
	if unsafe.Kind != repository.FileKindSymlink || unsafe.SymlinkEvidence == nil || unsafe.SymlinkEvidence.IsActionable() || unsafe.SymlinkEvidence.TargetClass != repository.SymlinkLexicallyEscaping && unsafe.SymlinkEvidence.TargetClass != repository.SymlinkUnrepresentable || unsafe.Reason != app.ResultReasonPathAlias && unsafe.Reason != app.ResultReasonUnsupportedEntry {
		t.Fatalf("unsafe symlink = %+v", unsafe)
	}
}
