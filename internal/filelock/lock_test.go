package filelock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/paths"
)

func TestAcquireHonorsNativeExclusionAndCancellation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "locks")
	if err := paths.EnsurePrivateDir(root); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	path := filepath.Join(root, "capacity.lock")
	first, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Acquire() error = %v, want context deadline", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("Acquire() after release error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestAcquireCrossProcessExclusion(t *testing.T) {
	if os.Getenv("NUDGE_FILELOCK_HELPER") == "1" {
		path := os.Getenv("NUDGE_FILELOCK_PATH")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
			os.Exit(2)
		}
		return
	}
	root := filepath.Join(t.TempDir(), "locks")
	if err := paths.EnsurePrivateDir(root); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	path := filepath.Join(root, "capacity.lock")
	lock, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer lock.Close()
	command := exec.Command(os.Args[0], "-test.run=TestAcquireCrossProcessExclusion", "--")
	command.Env = append(os.Environ(), "NUDGE_FILELOCK_HELPER=1", "NUDGE_FILELOCK_PATH="+path)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process error = %v, output = %s", err, strings.TrimSpace(string(output)))
	}
}
