package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

type reconcilerCaptureSource struct {
	mu      sync.Mutex
	started chan struct{}
	block   bool
}

func (s *reconcilerCaptureSource) Capture(ctx context.Context, _ repository.Repository, _ repository.WorktreeRef) (LocalCaptureArtifacts, error) {
	s.mu.Lock()
	if s.started != nil {
		close(s.started)
		s.started = nil
	}
	block := s.block
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return LocalCaptureArtifacts{}, ctx.Err()
	}
	return LocalCaptureArtifacts{}, nil
}

type reconcilerCaptureStore struct {
	mu        sync.Mutex
	adoptions []CaptureAdoption
	index     int
}

func (s *reconcilerCaptureStore) Adopt(context.Context, LocalCaptureArtifacts, CaptureSessionState) (CaptureAdoption, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.adoptions) {
		return CaptureAdoption{}, errors.New("missing test adoption")
	}
	value := s.adoptions[s.index]
	s.index++
	return value, nil
}

func (s *reconcilerCaptureStore) OpenCaptureManifest(context.Context, domain.CaptureID) (CaptureManifest, error) {
	return CaptureManifest{}, ErrCaptureNotFound
}

func (s *reconcilerCaptureStore) ReadBlobRange(context.Context, CaptureBlobRead) ([]byte, error) {
	return nil, ErrCaptureNotFound
}

type reconcilerAnchorSource struct {
	page ReconciliationAnchorPage
	err  error
}

func (s reconcilerAnchorSource) Page(context.Context, ReconciliationPageRequest) (ReconciliationAnchorPage, error) {
	return s.page, s.err
}

type reconcilerJournal struct {
	mu           sync.Mutex
	starts       []ReconciliationOperation
	resumeStarts int
	stages       []ReconciliationAnchorResult
	completed    []ReconciliationOperation
	completeGate chan struct{}
}

func (j *reconcilerJournal) Start(_ context.Context, guard SessionWriteGuard, operation ReconciliationOperation, resume bool) (SessionWriteGuard, error) {
	j.mu.Lock()
	if resume {
		j.resumeStarts++
	} else {
		j.starts = append(j.starts, operation)
	}
	j.mu.Unlock()
	guard.ExpectedRevision++
	return guard, nil
}

func (j *reconcilerJournal) Stage(_ context.Context, guard SessionWriteGuard, operation ReconciliationOperation, results []ReconciliationAnchorResult) (SessionWriteGuard, error) {
	j.mu.Lock()
	j.stages = append(j.stages, results...)
	j.mu.Unlock()
	guard.ExpectedRevision++
	return guard, nil
}

func (j *reconcilerJournal) CompleteAndActivate(ctx context.Context, guard SessionWriteGuard, operation ReconciliationOperation, _ time.Time) (SessionWriteGuard, error) {
	if j.completeGate != nil {
		select {
		case <-j.completeGate:
		case <-ctx.Done():
			return guard, ctx.Err()
		}
	}
	j.mu.Lock()
	j.completed = append(j.completed, operation)
	j.mu.Unlock()
	guard.ExpectedRevision++
	return guard, nil
}

func TestAuthoritativeReconcilerUnchangedCaptureRetainsGeneration(t *testing.T) {
	request, current := testAuthoritativeReconciliationRequest(t)
	capture := &reconcilerCaptureSource{}
	store := &reconcilerCaptureStore{adoptions: []CaptureAdoption{{Generation: current, Reused: true}}}
	journal := &reconcilerJournal{}
	reconciler := newTestAuthoritativeReconciler(t, capture, store, journal, reconcilerAnchorPage(t, request.Target.Generation))

	if err := reconciler.Submit(context.Background(), request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	event := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventFresh)
	if event.Generation != current.Generation || event.Stale || len(journal.starts) != 0 {
		t.Fatalf("unchanged event = %#v, starts = %d", event, len(journal.starts))
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAuthoritativeReconcilerStagesThenActivatesChangedCapture(t *testing.T) {
	request, _ := testAuthoritativeReconciliationRequest(t)
	newGeneration := testCaptureGeneration(2, "capture-new", "b")
	store := &reconcilerCaptureStore{adoptions: []CaptureAdoption{{Generation: newGeneration}}}
	journal := &reconcilerJournal{}
	reconciler := newTestAuthoritativeReconciler(t, &reconcilerCaptureSource{}, store, journal, reconcilerAnchorPage(t, request.Target.Generation))

	if err := reconciler.Submit(context.Background(), request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	event := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCompleted)
	if event.Generation != newGeneration.Generation || event.Stale || len(journal.stages) != 1 || len(journal.completed) != 1 {
		t.Fatalf("completed event = %#v, stages = %d, completed = %d", event, len(journal.stages), len(journal.completed))
	}
	if journal.stages[0].Anchor.TargetGeneration != newGeneration.Generation {
		t.Fatalf("staged anchor generation = %d, want %d", journal.stages[0].Anchor.TargetGeneration, newGeneration.Generation)
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAuthoritativeReconcilerFailureLeavesLastGenerationStale(t *testing.T) {
	request, _ := testAuthoritativeReconciliationRequest(t)
	newGeneration := testCaptureGeneration(2, "capture-new", "b")
	store := &reconcilerCaptureStore{adoptions: []CaptureAdoption{{Generation: newGeneration}}}
	journal := &reconcilerJournal{}
	anchorSource := reconcilerAnchorSource{err: errors.New("capture content unavailable")}
	reconciler := newTestAuthoritativeReconciler(t, &reconcilerCaptureSource{}, store, journal, anchorSource)

	if err := reconciler.Submit(context.Background(), request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	event := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventFailed)
	if !event.Stale || len(journal.completed) != 0 {
		t.Fatalf("failed event = %#v, completed = %d", event, len(journal.completed))
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAuthoritativeReconcilerResumesStagedCursor(t *testing.T) {
	request, _ := testAuthoritativeReconciliationRequest(t)
	newGeneration := testCaptureGeneration(2, "capture-new", "b")
	request.Resume = &ReconciliationResume{Operation: ReconciliationOperation{ID: "reconcile-resume", SessionID: request.Guard.SessionID, FromGeneration: 1, ToGeneration: 2, CaptureID: newGeneration.CaptureID, ManifestHash: newGeneration.ManifestHash, State: ReconciliationStaged, Progress: ReconciliationProgress{Phase: ReconciliationPhaseStaging, Cursor: "cursor-1", TotalAnchors: 1}, StartedAt: testTime}, Generation: newGeneration}
	capture := &reconcilerCaptureSource{started: make(chan struct{})}
	store := &reconcilerCaptureStore{}
	journal := &reconcilerJournal{}
	reconciler := newTestAuthoritativeReconciler(t, capture, store, journal, reconcilerAnchorPage(t, request.Target.Generation))

	if err := reconciler.Submit(context.Background(), request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	event := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCompleted)
	if event.Generation != newGeneration.Generation || journal.resumeStarts != 1 || len(journal.starts) != 0 {
		t.Fatalf("resume event = %#v, resume starts = %d, fresh starts = %d", event, journal.resumeStarts, len(journal.starts))
	}
	select {
	case <-capture.started:
		t.Fatal("resume unexpectedly recaptured the worktree")
	default:
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAuthoritativeReconcilerCancellationDoesNotActivate(t *testing.T) {
	request, _ := testAuthoritativeReconciliationRequest(t)
	newGeneration := testCaptureGeneration(2, "capture-new", "b")
	capture := &reconcilerCaptureSource{started: make(chan struct{}), block: true}
	store := &reconcilerCaptureStore{adoptions: []CaptureAdoption{{Generation: newGeneration}}}
	journal := &reconcilerJournal{}
	reconciler := newTestAuthoritativeReconciler(t, capture, store, journal, reconcilerAnchorPage(t, request.Target.Generation))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := reconciler.Submit(ctx, request); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	select {
	case <-capture.started:
	case <-time.After(time.Second):
		t.Fatal("capture did not start")
	}
	cancel()
	event := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCancelled)
	if !event.Stale || len(journal.completed) != 0 {
		t.Fatalf("cancelled event = %#v, completed = %d", event, len(journal.completed))
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestAuthoritativeReconcilerCoalescesOneFollowUpDuringCommit(t *testing.T) {
	request, _ := testAuthoritativeReconciliationRequest(t)
	second := request
	second.Refresh.Sequence = 2
	firstGeneration := testCaptureGeneration(2, "capture-first", "b")
	secondGeneration := testCaptureGeneration(3, "capture-second", "c")
	store := &reconcilerCaptureStore{adoptions: []CaptureAdoption{{Generation: firstGeneration}, {Generation: secondGeneration}}}
	journal := &reconcilerJournal{completeGate: make(chan struct{})}
	reconciler := newTestAuthoritativeReconciler(t, &reconcilerCaptureSource{}, store, journal, reconcilerAnchorPage(t, request.Target.Generation))

	if err := reconciler.Submit(context.Background(), request); err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCommitting)
	if err := reconciler.Submit(context.Background(), second); err != nil {
		t.Fatalf("follow-up Submit() error = %v", err)
	}
	close(journal.completeGate)
	firstCompleted := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCompleted)
	secondCompleted := waitForReconciliationEvent(t, reconciler.Events(), ReconciliationEventCompleted)
	if firstCompleted.Generation != firstGeneration.Generation || secondCompleted.Generation != secondGeneration.Generation || !secondCompleted.FollowUp || len(journal.completed) != 2 {
		t.Fatalf("completed events = %#v and %#v, journal completions = %d", firstCompleted, secondCompleted, len(journal.completed))
	}
	if err := reconciler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func newTestAuthoritativeReconciler(t *testing.T, capture LocalCaptureSource, store LocalCaptureStore, journal ReconciliationJournal, page ReconciliationAnchorSource) *AuthoritativeReconciler {
	t.Helper()
	reconciler, err := NewAuthoritativeReconciler(AuthoritativeReconcilerConfig{Capture: capture, Store: store, Journal: journal, Anchors: page, IDs: &sequenceIDs{values: []string{"operation-1", "operation-2"}}, Clock: fixedClock{when: testTime}})
	if err != nil {
		t.Fatalf("NewAuthoritativeReconciler() error = %v", err)
	}
	return reconciler
}

func testAuthoritativeReconciliationRequest(t *testing.T) (ReconciliationRequest, CaptureGeneration) {
	t.Helper()
	open := testSessionRequest(t, "old-fingerprint", "base")
	open.Repository.CommonGitDir = `C:\common.git`
	open.Repository.Binding.CommonGitDir = open.Repository.CommonGitDir
	open.Worktree.GitDir = `C:\repo\.git`
	open.Worktree.Binding.GitDir = open.Worktree.GitDir
	set, err := NewWatchedSet(open.Repository, open.Worktree)
	if err != nil {
		t.Fatalf("watched set: %v", err)
	}
	current := testCaptureGeneration(1, "capture-old", "a")
	guard := SessionWriteGuard{SessionID: "session", LeaseID: "lease", WriterEpoch: 1, ExpectedRevision: 1}
	return ReconciliationRequest{
		Refresh:      RefreshRequest{WatchedSet: set, Reasons: []RefreshReason{RefreshReasonExplicit}, RequestedAt: testTime, Sequence: 1},
		Repository:   open.Repository,
		Worktree:     open.Worktree,
		Guard:        guard,
		CaptureState: CaptureSessionState{Guard: CaptureSessionGuard{SessionID: guard.SessionID, LeaseID: guard.LeaseID, WriterEpoch: guard.WriterEpoch, Revision: guard.ExpectedRevision}, RepositoryID: open.Repository.ID, WorktreeID: open.Worktree.ID, Current: &current},
		Target:       open.Target,
	}, current
}

func testCaptureGeneration(generation repository.TargetGeneration, captureID, marker string) CaptureGeneration {
	return CaptureGeneration{CaptureID: domain.CaptureID(captureID), Generation: generation, RepositoryID: "repo", WorktreeID: "worktree", Fingerprint: strings.Repeat(marker, 64), ManifestHash: strings.Repeat(marker, 64), Base: repository.LocalCaptureBase{ObjectFormat: "sha1", ObjectID: "base"}, CreatedAt: testTime}
}

func reconcilerAnchorPage(t *testing.T, generation repository.TargetGeneration) ReconciliationAnchorSource {
	t.Helper()
	anchor := testReconcileAnchor(t, "src/example.go", 2, 2, "target", nil, nil, false)
	return reconcilerAnchorSource{page: ReconciliationAnchorPage{Items: []ReconciliationAnchorInput{{ThreadID: "thread-1", Anchor: anchor, NewContent: review.CapturedFile{Path: anchor.Path, Side: anchor.Side, ContentIdentity: "content", Lines: []string{"before", "target", "after"}}}}, TotalCount: 1, Done: true}}
}

func waitForReconciliationEvent(t *testing.T, events <-chan ReconciliationEvent, kind ReconciliationEventKind) ReconciliationEvent {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == kind {
				return event
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for reconciliation event %q", kind)
		}
	}
}
