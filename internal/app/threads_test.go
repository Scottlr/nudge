package app

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestCreateThreadPersistsBeforePublish(t *testing.T) {
	store := newThreadTestStore()
	service, err := NewThreadService(ThreadServiceConfig{
		Store: store,
		Clock: fixedClock{when: time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)},
		IDs:   &sequenceIDSource{ids: []string{"thread-1", "message-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := threadTestAnchor(t)
	guard := threadTestGuard()
	commit, err := service.CreateThread(context.Background(), guard, CreateThread{
		Guard:   guard,
		Anchor:  anchor,
		Comment: "  first line\n\nsecond line  ",
	})
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if len(commit.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(commit.Events))
	}
	if _, ok := commit.Events[0].(ThreadCreated); !ok {
		t.Fatalf("event = %T, want ThreadCreated", commit.Events[0])
	}
	if commit.Thread.Title != "first line" {
		t.Fatalf("title = %q, want %q", commit.Thread.Title, "first line")
	}
	if len(store.threads) != 1 || len(store.messages) != 1 {
		t.Fatalf("stored threads/messages = %d/%d, want 1/1", len(store.threads), len(store.messages))
	}
	message := store.messages[commit.Message.ID]
	if message.Status != review.MessagePending || message.Role != review.RoleUser || message.Content != "  first line\n\nsecond line  " {
		t.Fatalf("stored message = %#v", message)
	}
}

func TestCreateThreadRollback(t *testing.T) {
	store := newThreadTestStore()
	store.failMessage = true
	service, err := NewThreadService(ThreadServiceConfig{
		Store: store,
		Clock: fixedClock{when: time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)},
		IDs:   &sequenceIDSource{ids: []string{"thread-1", "message-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := threadTestAnchor(t)
	_, err = service.CreateThread(context.Background(), threadTestGuard(), CreateThread{Anchor: anchor, Comment: "comment"})
	if !errors.Is(err, errThreadTestMessageWrite) {
		t.Fatalf("error = %v, want message write error", err)
	}
	if len(store.threads) != 0 || len(store.messages) != 0 {
		t.Fatalf("rollback left threads/messages = %d/%d", len(store.threads), len(store.messages))
	}
}

func TestPendingReplySurvivesProviderOffline(t *testing.T) {
	store := newThreadTestStore()
	service, err := NewThreadService(ThreadServiceConfig{
		Store: store,
		Clock: fixedClock{when: time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)},
		IDs:   &sequenceIDSource{ids: []string{"thread-1", "message-1", "message-2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := threadTestAnchor(t)
	guard := threadTestGuard()
	created, err := service.CreateThread(context.Background(), guard, CreateThread{Anchor: anchor, Comment: "concern"})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := service.ReplyToThread(context.Background(), created.Guard, ReplyToThread{ThreadID: created.Thread.ID, Text: "follow-up"})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply.Message.Status != review.MessagePending || reply.Message.Content != "follow-up" || reply.Message.Ordinal != 2 {
		t.Fatalf("reply = %#v", reply.Message)
	}
	if len(store.messages) != 2 {
		t.Fatalf("stored messages = %d, want 2", len(store.messages))
	}
}

func TestResolveChangesOnlyResolution(t *testing.T) {
	store := newThreadTestStore()
	service, err := NewThreadService(ThreadServiceConfig{
		Store: store,
		Clock: fixedClock{when: time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)},
		IDs:   &sequenceIDSource{ids: []string{"thread-1", "message-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := threadTestAnchor(t)
	guard := threadTestGuard()
	created, err := service.CreateThread(context.Background(), guard, CreateThread{Anchor: anchor, Comment: "concern"})
	if err != nil {
		t.Fatal(err)
	}
	before := created.Thread
	resolved, err := service.ResolveThread(context.Background(), created.Guard, ResolveThread{ThreadID: created.Thread.ID, Resolved: true})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Thread.Resolution != review.ResolutionResolved || resolved.Thread.Conversation != before.Conversation || resolved.Thread.Proposal != before.Proposal || resolved.Thread.Read != before.Read || resolved.Thread.Anchor.State != before.Anchor.State {
		t.Fatalf("resolution changed unrelated axes: before=%+v after=%+v", before.Status(), resolved.Thread.Status())
	}
}

var errThreadTestMessageWrite = errors.New("thread test message write")

func threadTestGuard() SessionWriteGuard {
	return SessionWriteGuard{
		SessionID:        domain.ReviewSessionID("session-1"),
		LeaseID:          domain.SessionLeaseID("lease-1"),
		WriterEpoch:      1,
		ExpectedRevision: 1,
	}
}

func threadTestAnchor(t *testing.T) review.CodeAnchor {
	t.Helper()
	target, content, rows := anchorFixture(t)
	anchor, err := BuildCodeAnchor(target, DisplayedContentSnapshot{Content: content, Revision: 1, Target: target, Rows: rows}, AnchorSelection{
		Side:       repository.DiffBase,
		StartRowID: rows[2].ID,
		EndRowID:   rows[3].ID,
		HunkID:     "hunk-1",
	}, time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatalf("build test anchor: %v", err)
	}
	return anchor
}

type threadTestStore struct {
	threads     map[domain.ReviewThreadID]review.ReviewThread
	messages    map[domain.MessageID]review.Message
	guards      map[domain.ReviewSessionID]SessionWriteGuard
	failMessage bool
}

func newThreadTestStore() *threadTestStore {
	guard := threadTestGuard()
	return &threadTestStore{
		threads:  make(map[domain.ReviewThreadID]review.ReviewThread),
		messages: make(map[domain.MessageID]review.Message),
		guards:   map[domain.ReviewSessionID]SessionWriteGuard{guard.SessionID: guard},
	}
}

func (s *threadTestStore) UpsertRepository(context.Context, repository.Repository, repository.WorktreeRef) error {
	return nil
}

func (s *threadTestStore) CreateSession(context.Context, review.ReviewSession, domain.SessionLeaseID) (SessionWriteGuard, error) {
	return SessionWriteGuard{}, ErrReviewStoreInput
}

func (s *threadTestStore) ClaimSessionWriter(context.Context, domain.ReviewSessionID, domain.SessionLeaseID) (SessionWriteGuard, error) {
	return SessionWriteGuard{}, ErrReviewStoreInput
}

func (s *threadTestStore) FindCompatibleSession(context.Context, review.SessionKey) (*review.ReviewSession, error) {
	return nil, ErrReviewStoreNotFound
}

func (s *threadTestStore) ListThreadSummaries(_ context.Context, sessionID domain.ReviewSessionID, page ThreadPage) (ThreadPageResult, error) {
	items := make([]ThreadSummary, 0, len(s.threads))
	for _, thread := range s.threads {
		if thread.SessionID == sessionID {
			items = append(items, summarizeReviewThread(thread))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if page.Limit != 0 && uint32(len(items)) > page.Limit {
		items = items[:page.Limit]
	}
	return ThreadPageResult{Items: items, Revision: 1}, nil
}

func (s *threadTestStore) LoadThread(_ context.Context, threadID domain.ReviewThreadID) (review.ReviewThread, error) {
	thread, ok := s.threads[threadID]
	if !ok {
		return review.ReviewThread{}, ErrReviewStoreNotFound
	}
	return cloneThreadTestValue(thread), nil
}

func (s *threadTestStore) ListMessages(_ context.Context, threadID domain.ReviewThreadID, page MessagePage) (MessagePageResult, error) {
	items := make([]MessageSummary, 0)
	for _, message := range s.messages {
		if message.ThreadID != threadID {
			continue
		}
		items = append(items, MessageSummary{ID: message.ID, ThreadID: message.ThreadID, Role: message.Role, Status: message.Status, Ordinal: message.Ordinal, CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ordinal < items[j].Ordinal })
	if page.Limit != 0 && uint32(len(items)) > page.Limit {
		items = items[:page.Limit]
	}
	return MessagePageResult{Items: items, Revision: 1}, nil
}

func (s *threadTestStore) ReadMessageBody(context.Context, BodyRange) (MessageBodyChunk, error) {
	return MessageBodyChunk{}, ErrReviewStoreNotFound
}

func (s *threadTestStore) WithSessionTx(_ context.Context, guard SessionWriteGuard, fn func(ReviewStoreTx) error) (SessionWriteGuard, error) {
	current, ok := s.guards[guard.SessionID]
	if !ok || current != guard {
		return guard, ErrSessionRevisionConflict
	}
	transaction := &threadTestTx{
		threads:     cloneThreadTestMap(s.threads),
		messages:    cloneMessageTestMap(s.messages),
		sessionID:   guard.SessionID,
		failMessage: s.failMessage,
	}
	if err := fn(transaction); err != nil {
		return guard, err
	}
	s.threads = transaction.threads
	s.messages = transaction.messages
	current.ExpectedRevision++
	s.guards[guard.SessionID] = current
	return current, nil
}

func (s *threadTestStore) Close() error { return nil }

type threadTestTx struct {
	threads     map[domain.ReviewThreadID]review.ReviewThread
	messages    map[domain.MessageID]review.Message
	sessionID   domain.ReviewSessionID
	failMessage bool
}

func (t *threadTestTx) SaveSession(context.Context, review.ReviewSession) error { return nil }

func (t *threadTestTx) SaveThread(_ context.Context, thread review.ReviewThread) error {
	if thread.SessionID != t.sessionID {
		return ErrReviewStoreInput
	}
	t.threads[thread.ID] = cloneThreadTestValue(thread)
	return nil
}

func (t *threadTestTx) SaveMessage(_ context.Context, message review.Message) error {
	if t.failMessage {
		return errThreadTestMessageWrite
	}
	t.messages[message.ID] = message
	return nil
}

func (t *threadTestTx) SaveCaptureGeneration(context.Context, CaptureGeneration, CaptureManifest) error {
	return nil
}
func (t *threadTestTx) SaveAcceptedTargetGeneration(context.Context, AcceptedTargetGeneration) error {
	return nil
}
func (t *threadTestTx) CreateReconciliation(context.Context, ReconciliationOperation) error {
	return nil
}
func (t *threadTestTx) StageReconciliationResult(context.Context, ReconciliationAnchorResult) error {
	return nil
}
func (t *threadTestTx) CompleteReconciliation(context.Context, domain.OperationID, time.Time) error {
	return nil
}
func (t *threadTestTx) ActivateReconciliation(context.Context, domain.OperationID) error {
	return nil
}

func cloneThreadTestValue(thread review.ReviewThread) review.ReviewThread {
	thread.Messages = append([]domain.MessageID(nil), thread.Messages...)
	return thread
}

func cloneThreadTestMap(values map[domain.ReviewThreadID]review.ReviewThread) map[domain.ReviewThreadID]review.ReviewThread {
	copyValues := make(map[domain.ReviewThreadID]review.ReviewThread, len(values))
	for id, thread := range values {
		copyValues[id] = cloneThreadTestValue(thread)
	}
	return copyValues
}

func cloneMessageTestMap(values map[domain.MessageID]review.Message) map[domain.MessageID]review.Message {
	copyValues := make(map[domain.MessageID]review.Message, len(values))
	for id, message := range values {
		copyValues[id] = message
	}
	return copyValues
}

var _ ReviewStore = (*threadTestStore)(nil)
var _ ReviewStoreTx = (*threadTestTx)(nil)
