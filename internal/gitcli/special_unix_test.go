//go:build !windows && (linux || darwin || freebsd || openbsd || netbsd)

package gitcli

import (
	"os"
	"syscall"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestObserveWorkingEntryRetainsFIFOWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	native := root + string(os.PathSeparator) + "pending.pipe"
	if err := syscall.Mkfifo(native, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	path := repository.RepoPath("pending.pipe")
	kind, mode, evidence, err := observeWorkingEntry(root, path)
	if err != nil {
		t.Fatalf("observeWorkingEntry: %v", err)
	}
	if kind != repository.FileKindUnknown || mode != 0 || evidence == nil || evidence.SpecialKind != repository.SpecialFIFO {
		t.Fatalf("FIFO observation = kind %q, mode %#o, evidence %#v", kind, mode, evidence)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("FIFO evidence invalid: %v", err)
	}
}
