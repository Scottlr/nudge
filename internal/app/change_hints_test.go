package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testWatchedSet(t *testing.T, suffix string) WatchedSet {
	t.Helper()
	root := filepath.Join(t.TempDir(), suffix)
	set := WatchedSet{
		RepositoryID:   "repository-1",
		WorktreeID:     "worktree-1",
		WorktreeRoot:   root,
		WorktreeGitDir: filepath.Join(root, ".git", "worktrees", "one"),
		CommonGitDir:   filepath.Join(root, ".git"),
		Paths: []WatchedPath{
			{Path: root, Kind: WatchPathWorktreeRoot},
			{Path: filepath.Join(root, ".git", "worktrees", "one"), Kind: WatchPathWorktreeGit},
			{Path: filepath.Join(root, ".git"), Kind: WatchPathCommonGit},
		},
	}
	if err := set.finalize(); err != nil {
		t.Fatalf("watched set: %v", err)
	}
	return set
}

func refreshAt(seconds int) time.Time {
	return time.Unix(int64(seconds), 0).UTC()
}

func TestRefreshSchedulerCoalescesQuietBurst(t *testing.T) {
	scheduler, err := NewRefreshScheduler(RefreshSchedulerConfig{
		QuietDelay:        200 * time.Millisecond,
		MaximumDelay:      2 * time.Second,
		FocusedMaximumAge: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := testWatchedSet(t, "burst")
	if err := scheduler.SetWatchedSet(refreshAt(0), set); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SubmitHint(refreshAt(1), WatchHint{WatchedSet: set, Reason: RefreshReasonFilesystemChange}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SubmitHint(refreshAt(1), WatchHint{WatchedSet: set, Reason: RefreshReasonHeadChanged}); err != nil {
		t.Fatal(err)
	}
	if _, _, due := scheduler.Due(refreshAt(1)); due {
		t.Fatal("request emitted before quiet delay")
	}
	request, ticket, due := scheduler.Due(refreshAt(2))
	if !due || ticket == 0 {
		t.Fatal("request was not emitted after quiet delay")
	}
	if len(request.Reasons) != 2 || request.Reasons[0] != RefreshReasonFilesystemChange || request.Reasons[1] != RefreshReasonHeadChanged {
		t.Fatalf("reasons = %#v", request.Reasons)
	}
	if request.WatchedSet.WatchedSetID != set.WatchedSetID || request.TruthLost {
		t.Fatalf("request identity/loss = %#v", request)
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("request validation: %v", err)
	}
}

func TestRefreshSchedulerMaximumDelayPreventsStarvation(t *testing.T) {
	scheduler, err := NewRefreshScheduler(RefreshSchedulerConfig{
		QuietDelay:        2 * time.Second,
		MaximumDelay:      5 * time.Second,
		FocusedMaximumAge: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := testWatchedSet(t, "continuous")
	if err := scheduler.SetWatchedSet(refreshAt(0), set); err != nil {
		t.Fatal(err)
	}
	for second := 0; second < 5; second++ {
		if err := scheduler.SubmitHint(refreshAt(second), WatchHint{WatchedSet: set, Reason: RefreshReasonFilesystemChange}); err != nil {
			t.Fatal(err)
		}
	}
	request, ticket, due := scheduler.Due(refreshAt(5))
	if !due || ticket == 0 || request.Reasons[0] != RefreshReasonFilesystemChange {
		t.Fatalf("maximum-delay request = %#v, ticket=%d, due=%v", request, ticket, due)
	}
}

func TestRefreshSchedulerKeepsOneFollowUpDuringActiveRequest(t *testing.T) {
	scheduler, err := NewRefreshScheduler(RefreshSchedulerConfig{
		QuietDelay:        time.Second,
		MaximumDelay:      4 * time.Second,
		FocusedMaximumAge: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := testWatchedSet(t, "follow-up")
	if err := scheduler.SetWatchedSet(refreshAt(0), set); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SubmitHint(refreshAt(0), WatchHint{WatchedSet: set, Reason: RefreshReasonExplicit}); err != nil {
		t.Fatal(err)
	}
	_, ticket, due := scheduler.Due(refreshAt(1))
	if !due {
		t.Fatal("initial request not emitted")
	}
	if err := scheduler.SubmitHint(refreshAt(2), WatchHint{WatchedSet: set, Reason: RefreshReasonWatcherOverflow, TruthLost: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, due := scheduler.Due(refreshAt(10)); due {
		t.Fatal("active request allowed overlap")
	}
	if err := scheduler.Complete(refreshAt(3), ticket); err != nil {
		t.Fatal(err)
	}
	request, followUpTicket, due := scheduler.Due(refreshAt(4))
	if !due || followUpTicket == 0 || !request.TruthLost || len(request.Reasons) != 1 || request.Reasons[0] != RefreshReasonWatcherOverflow {
		t.Fatalf("follow-up = %#v, ticket=%d, due=%v", request, followUpTicket, due)
	}
}

func TestRefreshSchedulerFocusedMaximumAgePausesWhenUnfocused(t *testing.T) {
	scheduler, err := NewRefreshScheduler(RefreshSchedulerConfig{
		QuietDelay:        time.Second,
		MaximumDelay:      4 * time.Second,
		FocusedMaximumAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := testWatchedSet(t, "focus")
	if err := scheduler.SetWatchedSet(refreshAt(0), set); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SetFocused(refreshAt(0), true, true); err != nil {
		t.Fatal(err)
	}
	request, ticket, due := scheduler.Due(refreshAt(3))
	if !due || ticket == 0 || len(request.Reasons) != 1 || request.Reasons[0] != RefreshReasonMaximumAge {
		t.Fatalf("focused maximum-age request = %#v, ticket=%d, due=%v", request, ticket, due)
	}
	if err := scheduler.Complete(refreshAt(3), ticket); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SetFocused(refreshAt(4), false, true); err != nil {
		t.Fatal(err)
	}
	if _, _, due := scheduler.Due(refreshAt(100)); due {
		t.Fatal("unfocused scheduler emitted maximum-age request")
	}
}

func TestRefreshSchedulerWatchedSetReplacementLatchesTruthLoss(t *testing.T) {
	scheduler, err := NewRefreshScheduler(DefaultRefreshSchedulerConfig())
	if err != nil {
		t.Fatal(err)
	}
	first := testWatchedSet(t, "first")
	second := testWatchedSet(t, "second")
	if err := scheduler.SetWatchedSet(refreshAt(0), first); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.SetWatchedSet(refreshAt(1), second); err != nil {
		t.Fatal(err)
	}
	request, _, due := scheduler.Due(refreshAt(2))
	if !due || !request.TruthLost || len(request.Reasons) != 1 || request.Reasons[0] != RefreshReasonWatchedSetChanged {
		t.Fatalf("replacement request = %#v, due=%v", request, due)
	}
	if request.WatchedSet.WatchedSetID != second.WatchedSetID {
		t.Fatalf("replacement identity = %q, want %q", request.WatchedSet.WatchedSetID, second.WatchedSetID)
	}
}

type fakeFileWatcher struct {
	mu      sync.Mutex
	hints   chan WatchHint
	started bool
	closed  bool
	set     WatchedSet
}

func newFakeFileWatcher() *fakeFileWatcher { return &fakeFileWatcher{hints: make(chan WatchHint, 4)} }

func (w *fakeFileWatcher) Start(_ context.Context, set WatchedSet) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("already started")
	}
	w.started = true
	w.set = set
	return nil
}

func (w *fakeFileWatcher) Hints() <-chan WatchHint { return w.hints }

func (w *fakeFileWatcher) Replace(_ context.Context, set WatchedSet) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started || w.closed {
		return errors.New("not active")
	}
	w.set = set
	return nil
}

func (w *fakeFileWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.hints)
	}
	return nil
}

func TestRefreshCoordinatorCloseClosesWatcher(t *testing.T) {
	watcher := newFakeFileWatcher()
	coordinator, err := NewRefreshCoordinator(RefreshCoordinatorConfig{Watcher: watcher, Clock: fixedClock{when: refreshAt(0)}})
	if err != nil {
		t.Fatal(err)
	}
	set := testWatchedSet(t, "coordinator")
	if err := coordinator.Start(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-coordinator.Requests(); ok {
		t.Fatal("request stream remained open")
	}
	watcher.mu.Lock()
	closed := watcher.closed
	watcher.mu.Unlock()
	if !closed {
		t.Fatal("watcher was not closed")
	}
}
