package watch

import (
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/fsnotify/fsnotify"
)

func TestClassifyEventSeparatesContentAdminAndReplacementHints(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktree")
	set := app.WatchedSet{
		WorktreeRoot:   root,
		WorktreeGitDir: filepath.Join(root, ".git", "worktrees", "one"),
		CommonGitDir:   filepath.Join(root, ".git"),
	}
	tests := []struct {
		name       string
		event      fsnotify.Event
		tracked    bool
		wantReason app.RefreshReason
		wantLoss   bool
		wantUse    bool
	}{
		{
			name:       "worktree content",
			event:      fsnotify.Event{Name: filepath.Join(root, "main.go"), Op: fsnotify.Write},
			wantReason: app.RefreshReasonFilesystemChange,
			wantUse:    true,
		},
		{
			name:       "git admin",
			event:      fsnotify.Event{Name: filepath.Join(root, ".git", "config"), Op: fsnotify.Write},
			wantReason: app.RefreshReasonHeadChanged,
			wantUse:    true,
		},
		{
			name:       "watched root replacement",
			event:      fsnotify.Event{Name: root, Op: fsnotify.Remove},
			tracked:    true,
			wantReason: app.RefreshReasonWatchedRootReplaced,
			wantLoss:   true,
			wantUse:    true,
		},
		{
			name:  "irrelevant op",
			event: fsnotify.Event{Name: filepath.Join(root, "main.go")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, loss, use := classifyEvent(test.event, set, test.tracked)
			if reason != test.wantReason || loss != test.wantLoss || use != test.wantUse {
				t.Fatalf("classification = (%q, %v, %v), want (%q, %v, %v)", reason, loss, use, test.wantReason, test.wantLoss, test.wantUse)
			}
		})
	}
}

func TestPathWithinDoesNotCrossSiblingOrParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	if !pathWithin(root, filepath.Join(root, "src", "main.go")) {
		t.Fatal("child path was not contained")
	}
	if pathWithin(root, root+"-backup") {
		t.Fatal("sibling path was treated as contained")
	}
	if pathWithin(root, filepath.Dir(root)) {
		t.Fatal("parent path was treated as contained")
	}
}

func TestWatcherConfigRejectsUnboundedOrRelativeRoots(t *testing.T) {
	if _, err := NewFileWatcher(Config{HintBuffer: 0, MaxWatchedDirectories: 0, IgnoredRoots: []string{"relative"}}); err == nil {
		t.Fatal("relative ignored root accepted")
	}
	if _, err := NewFileWatcher(Config{HintBuffer: 4097}); err == nil {
		t.Fatal("unbounded hint buffer accepted")
	}
}
