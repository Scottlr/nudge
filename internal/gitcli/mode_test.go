package gitcli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Scottlr/nudge/internal/diff"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestRealGitExecutableModeTransitionIsOneModeAwareEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide exact Git executable-bit representation")
	}
	root := t.TempDir()
	gitPath := testGitPath(t, root)
	runGit(t, root, gitPath, "init")
	runGit(t, root, gitPath, "config", "user.email", "nudge@example.invalid")
	runGit(t, root, gitPath, "config", "user.name", "Nudge Test")
	runGit(t, root, gitPath, "config", "core.filemode", "true")
	path := filepath.Join(root, "tool.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, gitPath, "add", "--", "tool.sh")
	runGit(t, root, gitPath, "commit", "-m", "baseline")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	rawChanges, err := parseRawDiff(runGit(t, root, gitPath, "diff", "--raw", "-z", "--full-index", "HEAD", "--"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rawChanges) != 1 || rawChanges[0].ModeTransition == nil || rawChanges[0].ModeTransition.Kind != repository.ModeExecutableOn || rawChanges[0].Kind != repository.ChangeModified {
		t.Fatalf("raw executable transition = %#v", rawChanges)
	}

	patches, err := diff.ParsePatch(runGit(t, root, gitPath, "diff", "--binary", "--full-index", "HEAD", "--"))
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || patches[0].File.ModeTransition == nil || patches[0].File.ModeTransition.Kind != repository.ModeExecutableOn {
		t.Fatalf("patch executable transition = %#v", patches)
	}
}
