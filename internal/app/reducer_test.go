package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

type sequenceIDSource struct {
	ids []string
	pos int
}

func (s *sequenceIDSource) NewID() string {
	if s.pos >= len(s.ids) {
		return "unused-id"
	}
	id := s.ids[s.pos]
	s.pos++
	return id
}

func testReducer(ids ...string) *Reducer {
	return NewReducer(ReducerConfig{
		Clock: fixedClock{when: time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC)},
		IDs:   &sequenceIDSource{ids: ids},
	})
}

func TestReducerPublishesImmutableSnapshot(t *testing.T) {
	reducer := testReducer("open")
	response, err := reducer.Handle(OpenRepository{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if response.Commit.Snapshot.Revision != 1 {
		t.Fatalf("revision = %d, want 1", response.Commit.Snapshot.Revision)
	}
	if len(response.Commit.Snapshot.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(response.Commit.Snapshot.Operations))
	}

	consumerSnapshot := response.Commit.Snapshot
	consumerSnapshot.Operations[0].Status = OperationStatusFailed
	consumerSnapshot.Operations[0].Message = "consumer mutation"

	canonical := reducer.Snapshot()
	if got := canonical.Operations[0].Status; got != OperationStatusRunning {
		t.Fatalf("canonical status = %q, want running", got)
	}
	if canonical.Operations[0].Message != "" {
		t.Fatalf("canonical message changed to %q", canonical.Operations[0].Message)
	}
}

func TestReducerDiscardsSupersededResult(t *testing.T) {
	reducer := testReducer("open", "select", "refresh")
	open, err := reducer.Handle(OpenRepository{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reducer.Handle(RepositoryLoaded{
		OperationID:   open.OperationID,
		CorrelationID: reducer.State().Operations[open.OperationID].CorrelationID,
		Repository:    testRepositoryState(),
	}); err != nil {
		t.Fatal(err)
	}
	spec, err := repository.NewLocalTargetSpec()
	if err != nil {
		t.Fatal(err)
	}
	selectTarget, err := reducer.Handle(SelectTarget{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	target := testTarget(2)
	if _, err := reducer.Handle(TargetLoaded{
		OperationID:      selectTarget.OperationID,
		CorrelationID:    reducer.State().Operations[selectTarget.OperationID].CorrelationID,
		TargetGeneration: target.Generation,
		Target:           target,
	}); err != nil {
		t.Fatal(err)
	}
	refresh, err := reducer.Handle(RefreshTarget{})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := reducer.Handle(RefreshTarget{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reducer.State().Operations[refresh.OperationID].Status; got != OperationStatusCancelled {
		t.Fatalf("superseded refresh status = %q, want cancelled", got)
	}
	before := reducer.Snapshot()
	stale := testTarget(1)
	_, err = reducer.Handle(TargetLoaded{
		OperationID:      replacement.OperationID,
		CorrelationID:    reducer.State().Operations[replacement.OperationID].CorrelationID,
		TargetGeneration: stale.Generation,
		Target:           stale,
	})
	if !errors.Is(err, ErrResultDiscarded) {
		t.Fatalf("stale result error = %v, want ErrResultDiscarded", err)
	}
	after := reducer.Snapshot()
	if after.Revision != before.Revision {
		t.Fatalf("stale result changed revision from %d to %d", before.Revision, after.Revision)
	}
	if after.Target.Generation != 2 {
		t.Fatalf("target generation = %d, want 2", after.Target.Generation)
	}
	if got := reducer.State().Operations[replacement.OperationID].Status; got != OperationStatusRunning {
		t.Fatalf("refresh operation status = %q, want running", got)
	}
}

func TestCancelOperationTransitionsState(t *testing.T) {
	reducer := testReducer("open")
	open, err := reducer.Handle(OpenRepository{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := reducer.Handle(CancelOperation{OperationID: open.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	operation := cancelled.Commit.Snapshot.Operations[0]
	if operation.Status != OperationStatusCancelled {
		t.Fatalf("status = %q, want cancelled", operation.Status)
	}
	if len(cancelled.Commit.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(cancelled.Commit.Events))
	}
	if _, ok := cancelled.Commit.Events[0].(OperationCancelled); !ok {
		t.Fatalf("event type = %T, want OperationCancelled", cancelled.Commit.Events[0])
	}
}

func TestClientSnapshotsAreInitialAndLatestWins(t *testing.T) {
	client, err := NewClient(ClientOptions{IDs: &sequenceIDSource{ids: []string{"one", "two"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snapshots := client.Snapshots()
	initial := <-snapshots
	if initial.Revision != 0 {
		t.Fatalf("initial revision = %d, want 0", initial.Revision)
	}
	if _, err := client.Dispatch(context.Background(), OpenRepository{Path: "."}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dispatch(context.Background(), OpenRepository{Path: "."}); err != nil {
		t.Fatal(err)
	}
	latest := <-snapshots
	if latest.Revision != 2 {
		t.Fatalf("latest revision = %d, want 2", latest.Revision)
	}
	if len(latest.Operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(latest.Operations))
	}
}

func TestClientEventsRemainOrderedAndSlowConsumersClose(t *testing.T) {
	client, err := NewClient(ClientOptions{EventBuffer: 8, IDs: &sequenceIDSource{ids: []string{"open"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := client.Snapshots()
	<-snapshots
	events := client.Events()
	open, err := client.Dispatch(context.Background(), OpenRepository{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SubmitResult(context.Background(), RepositoryLoaded{
		OperationID:   open,
		CorrelationID: CorrelationID(open),
		Repository:    testRepositoryState(),
	}); err != nil {
		t.Fatal(err)
	}
	first := <-events
	second := <-events
	third := <-events
	if _, ok := first.(OperationStarted); !ok {
		t.Fatalf("first event = %T, want OperationStarted", first)
	}
	if _, ok := second.(RepositoryLoaded); !ok {
		t.Fatalf("second event = %T, want RepositoryLoaded", second)
	}
	if _, ok := third.(OperationCompleted); !ok {
		t.Fatalf("third event = %T, want OperationCompleted", third)
	}
	if first.eventMetadata().Revision != 1 || second.eventMetadata().Revision != 2 || third.eventMetadata().Revision != 2 {
		t.Fatalf("event revisions = %d, %d, %d; want 1, 2, 2", first.eventMetadata().Revision, second.eventMetadata().Revision, third.eventMetadata().Revision)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	slow, err := NewClient(ClientOptions{EventBuffer: 1, IDs: &sequenceIDSource{ids: []string{"open"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	<-slow.Snapshots()
	open, err = slow.Dispatch(context.Background(), OpenRepository{Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if err := slow.SubmitResult(context.Background(), RepositoryLoaded{
		OperationID:   open,
		CorrelationID: CorrelationID(open),
		Repository:    testRepositoryState(),
	}); err != nil {
		t.Fatal(err)
	}
	for range slow.Events() {
	}
	if !errors.Is(slow.EventError(), ErrConsumerTooSlow) {
		t.Fatalf("event error = %v, want ErrConsumerTooSlow", slow.EventError())
	}
}

func TestClientShutdownClosesStreams(t *testing.T) {
	client, err := NewClient(ClientOptions{IDs: &sequenceIDSource{ids: []string{"open"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := client.Snapshots()
	<-snapshots
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for range snapshots {
	}
	if _, ok := <-snapshots; ok {
		t.Fatal("snapshot stream remained open")
	}
	if _, err := client.Dispatch(context.Background(), OpenRepository{Path: "."}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("dispatch after close = %v, want ErrClientClosed", err)
	}
}

func testRepositoryState() RepositoryState {
	repositoryID, _ := domain.NewRepositoryID("repository")
	worktreeID, _ := domain.NewWorktreeID("worktree")
	now := time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC)
	commonGitDir := `C:\repo\.git`
	repo := repository.Repository{
		ID:           repositoryID,
		CommonGitDir: commonGitDir,
		Binding: repository.RepositoryBindingEvidence{
			Version:              1,
			ObjectFormat:         "sha1",
			CommonGitDir:         commonGitDir,
			CommonGitDirIdentity: repository.NativeIdentity("common"),
		},
		DisplayName: "repository",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	return RepositoryState{
		Repository: repo,
		Worktree: &repository.WorktreeRef{
			ID:           worktreeID,
			RepositoryID: repositoryID,
			RootPath:     `C:\repo`,
			GitDir:       commonGitDir,
			Binding: repository.WorktreeBindingEvidence{
				Version:        1,
				ObjectFormat:   "sha1",
				RootPath:       `C:\repo`,
				GitDir:         commonGitDir,
				RootIdentity:   repository.NativeIdentity("root"),
				GitDirIdentity: repository.NativeIdentity("git"),
			},
			BranchName: "main",
		},
	}
}

func testTarget(generation repository.TargetGeneration) repository.ResolvedTarget {
	worktreeID, _ := domain.NewWorktreeID("worktree")
	spec, _ := repository.NewLocalTargetSpec()
	base, _ := repository.NewObjectID("base")
	return repository.ResolvedTarget{
		Spec:       spec,
		Generation: generation,
		Base: repository.SnapshotRef{
			Kind:     repository.SnapshotCommit,
			ObjectID: base,
		},
		Head: repository.SnapshotRef{
			Kind:        repository.SnapshotWorkingTree,
			WorktreeID:  worktreeID,
			Fingerprint: "working-tree",
		},
		Editable:        true,
		EditDestination: &worktreeID,
		Fingerprint:     "target",
		ResolvedAt:      time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC),
	}
}
