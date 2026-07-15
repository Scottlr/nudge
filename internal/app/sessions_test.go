package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestOpenRestoresExactCompatibleSession(t *testing.T) {
	store := newFakeSessionStore()
	manager := newTestSessionManager(store, []string{"session-1", "lease-1", "lease-2"})
	request := testSessionRequest(t, "fingerprint-1", "base-object")

	first, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("open first session: %v", err)
	}
	firstID := first.Session.ID
	if err := manager.ReleaseSession(context.Background(), &first); err != nil {
		t.Fatalf("release first session: %v", err)
	}
	second, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	if !second.Restored || second.Session.ID != firstID {
		t.Fatalf("restored = %v, session ID = %q, want restored session %q", second.Restored, second.Session.ID, firstID)
	}
	if err := manager.ReleaseSession(context.Background(), &second); err != nil {
		t.Fatalf("release restored session: %v", err)
	}
}

func TestTargetChangeCreatesSeparateSession(t *testing.T) {
	store := newFakeSessionStore()
	manager := newTestSessionManager(store, []string{"session-1", "lease-1", "session-2", "lease-2"})
	firstRequest := testSessionRequest(t, "fingerprint-1", "base-object-1")
	first, err := manager.OpenSession(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("open first session: %v", err)
	}
	if err := manager.ReleaseSession(context.Background(), &first); err != nil {
		t.Fatalf("release first session: %v", err)
	}
	secondRequest := testSessionRequest(t, "fingerprint-1", "base-object-2")
	second, err := manager.OpenSession(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("open changed target: %v", err)
	}
	if second.Restored || second.Session.ID == first.Session.ID {
		t.Fatalf("changed target restored/forked incorrectly: restored=%v id=%q first=%q", second.Restored, second.Session.ID, first.Session.ID)
	}
	if err := manager.ReleaseSession(context.Background(), &second); err != nil {
		t.Fatalf("release changed target session: %v", err)
	}
}

func TestFingerprintChangeAppendsGeneration(t *testing.T) {
	store := newFakeSessionStore()
	manager := newTestSessionManager(store, []string{"session-1", "lease-1", "lease-2"})
	request := testSessionRequest(t, "fingerprint-1", "base-object")
	handle, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	updated := request.Target
	updated.Fingerprint = "fingerprint-2"
	updated.Head.Fingerprint = "fingerprint-2"
	if err := manager.RefreshTarget(context.Background(), &handle, updated); err != nil {
		t.Fatalf("refresh target: %v", err)
	}
	if handle.Session.Target.Generation != 2 || store.sessions[handle.Session.ID].Target.Generation != 2 {
		t.Fatalf("generation = %d, stored generation = %d, want 2", handle.Session.Target.Generation, store.sessions[handle.Session.ID].Target.Generation)
	}
	if err := manager.ReleaseSession(context.Background(), &handle); err != nil {
		t.Fatalf("release session: %v", err)
	}
}

func TestCloseSessionExcludesAutomaticRestore(t *testing.T) {
	store := newFakeSessionStore()
	manager := newTestSessionManager(store, []string{"session-1", "lease-1", "session-2", "lease-2"})
	request := testSessionRequest(t, "fingerprint-1", "base-object")
	handle, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	closedID := handle.Session.ID
	if err := manager.CloseSession(context.Background(), &handle); err != nil {
		t.Fatalf("close session: %v", err)
	}
	reopened, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	if reopened.Restored || reopened.Session.ID == closedID {
		t.Fatalf("closed session was restored: restored=%v id=%q", reopened.Restored, reopened.Session.ID)
	}
	if err := manager.ReleaseSession(context.Background(), &reopened); err != nil {
		t.Fatalf("release reopened session: %v", err)
	}
}

func TestNoPersistWritesNothing(t *testing.T) {
	manager, err := NewSessionManager(SessionManagerConfig{IDs: &sequenceIDs{values: []string{"ephemeral-session"}}, Clock: fixedClock{when: testTime}})
	if err != nil {
		t.Fatalf("new no-persist manager: %v", err)
	}
	request := testSessionRequest(t, "fingerprint-1", "base-object")
	request.Persistence = PersistenceNoPersist
	handle, err := manager.OpenSession(context.Background(), request)
	if err != nil {
		t.Fatalf("open no-persist session: %v", err)
	}
	if handle.Persistence != PersistenceNoPersist || handle.PersistenceDegraded || handle.Lease != nil {
		t.Fatalf("no-persist handle = %+v", handle)
	}
	if err := handle.Guard.Validate(); !errors.Is(err, ErrReviewStoreInput) {
		t.Fatalf("no-persist guard validation = %v, want input error", err)
	}
}

var testTime = time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC)

func testSessionRequest(t *testing.T, fingerprint, baseObject string) OpenSessionRequest {
	t.Helper()
	baseSpec, err := repository.NewLocalTargetSpec()
	if err != nil {
		t.Fatalf("local target spec: %v", err)
	}
	repo := repository.Repository{
		ID:           domain.RepositoryID("repo"),
		CommonGitDir: `C:\repo\.git`,
		Binding: repository.RepositoryBindingEvidence{
			Version: 1, ObjectFormat: "sha1", CommonGitDir: `C:\repo\.git`, CommonGitDirIdentity: repository.NativeIdentity("repo-native"),
		},
		DisplayName: "repo", CreatedAt: testTime, UpdatedAt: testTime,
	}
	worktree := repository.WorktreeRef{
		ID: domain.WorktreeID("worktree"), RepositoryID: repo.ID, RootPath: `C:\repo`, GitDir: `C:\repo\.git`,
		Binding: repository.WorktreeBindingEvidence{
			Version: 1, ObjectFormat: "sha1", RootPath: `C:\repo`, GitDir: `C:\repo\.git`, RootIdentity: repository.NativeIdentity("root-native"), GitDirIdentity: repository.NativeIdentity("git-native"),
		},
	}
	destination := worktree.ID
	target, err := repository.NewResolvedTarget(repository.ResolvedTarget{
		Spec: baseSpec, Generation: 1,
		Base:     repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: repository.ObjectID(baseObject)},
		Head:     repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: fingerprint},
		Editable: true, EditDestination: &destination, Fingerprint: fingerprint, ResolvedAt: testTime,
	})
	if err != nil {
		t.Fatalf("resolved target: %v", err)
	}
	return OpenSessionRequest{Repository: repo, Worktree: worktree, Target: target, Mode: SessionWritable, Persistence: PersistenceDurable}
}

func newTestSessionManager(store *fakeSessionStore, ids []string) *SessionManager {
	manager, err := NewSessionManager(SessionManagerConfig{Store: store, Leases: fakeLeaseManager{}, IDs: &sequenceIDs{values: ids}, Clock: fixedClock{when: testTime}})
	if err != nil {
		panic(err)
	}
	return manager
}

type sequenceIDs struct {
	values []string
	index  int
}

func (s *sequenceIDs) NewID() string {
	if s.index >= len(s.values) {
		return fmt.Sprintf("generated-%d", s.index)
	}
	value := s.values[s.index]
	s.index++
	return value
}

type fakeLease struct{ id domain.SessionLeaseID }

func (l fakeLease) LeaseID() domain.SessionLeaseID { return l.id }
func (l fakeLease) Close() error                   { return nil }

type fakeLeaseManager struct{}

func (fakeLeaseManager) Acquire(_ context.Context, request SessionLeaseRequest) (SessionLease, error) {
	return fakeLease{id: request.LeaseID}, nil
}

type fakeSessionStore struct {
	sessions map[domain.ReviewSessionID]review.ReviewSession
	guards   map[domain.ReviewSessionID]SessionWriteGuard
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: make(map[domain.ReviewSessionID]review.ReviewSession), guards: make(map[domain.ReviewSessionID]SessionWriteGuard)}
}

func (s *fakeSessionStore) UpsertRepository(context.Context, repository.Repository, repository.WorktreeRef) error {
	return nil
}

func (s *fakeSessionStore) CreateSession(_ context.Context, session review.ReviewSession, leaseID domain.SessionLeaseID) (SessionWriteGuard, error) {
	if _, exists := s.sessions[session.ID]; exists {
		return SessionWriteGuard{}, ErrSessionRevisionConflict
	}
	guard := SessionWriteGuard{SessionID: session.ID, LeaseID: leaseID, WriterEpoch: 1, ExpectedRevision: 1}
	s.sessions[session.ID] = session
	s.guards[session.ID] = guard
	return guard, nil
}

func (s *fakeSessionStore) ClaimSessionWriter(_ context.Context, sessionID domain.ReviewSessionID, leaseID domain.SessionLeaseID) (SessionWriteGuard, error) {
	session, ok := s.sessions[sessionID]
	if !ok {
		return SessionWriteGuard{}, ErrReviewStoreNotFound
	}
	if session.ClosedAt != nil {
		return SessionWriteGuard{}, ErrSessionRevisionConflict
	}
	guard := s.guards[sessionID]
	guard.LeaseID, guard.WriterEpoch, guard.ExpectedRevision = leaseID, guard.WriterEpoch+1, guard.ExpectedRevision+1
	s.guards[sessionID] = guard
	return guard, nil
}

func (s *fakeSessionStore) FindCompatibleSession(_ context.Context, key review.SessionKey) (*review.ReviewSession, error) {
	var found *review.ReviewSession
	for _, session := range s.sessions {
		if session.ClosedAt != nil {
			continue
		}
		candidate, err := review.SessionKeyFor(session)
		if err == nil && candidate == key {
			copy := session
			found = &copy
		}
	}
	if found == nil {
		return nil, ErrReviewStoreNotFound
	}
	return found, nil
}

func (s *fakeSessionStore) ListThreadSummaries(context.Context, domain.ReviewSessionID, ThreadPage) (ThreadPageResult, error) {
	return ThreadPageResult{}, nil
}

func (s *fakeSessionStore) LoadThread(context.Context, domain.ReviewThreadID) (review.ReviewThread, error) {
	return review.ReviewThread{}, ErrReviewStoreNotFound
}

func (s *fakeSessionStore) ListMessages(context.Context, domain.ReviewThreadID, MessagePage) (MessagePageResult, error) {
	return MessagePageResult{}, nil
}

func (s *fakeSessionStore) ReadMessageBody(context.Context, BodyRange) (MessageBodyChunk, error) {
	return MessageBodyChunk{}, ErrReviewStoreNotFound
}
func (s *fakeSessionStore) LoadProviderConversation(context.Context, domain.ProviderConversationID) (*ProviderConversationRecord, error) {
	return nil, ErrReviewStoreNotFound
}
func (s *fakeSessionStore) LoadProviderConversationForThread(context.Context, domain.ReviewThreadID) (*ProviderConversationRecord, error) {
	return nil, ErrReviewStoreNotFound
}
func (s *fakeSessionStore) LoadProviderTurn(context.Context, domain.ProviderTurnID) (*ProviderTurnRecord, error) {
	return nil, ErrReviewStoreNotFound
}
func (s *fakeSessionStore) ListProviderTurns(context.Context, domain.ReviewThreadID) ([]ProviderTurnRecord, error) {
	return nil, nil
}

func (s *fakeSessionStore) WithSessionTx(_ context.Context, guard SessionWriteGuard, fn func(ReviewStoreTx) error) (SessionWriteGuard, error) {
	current, ok := s.guards[guard.SessionID]
	if !ok {
		return guard, ErrReviewStoreNotFound
	}
	if current != guard {
		return guard, ErrSessionRevisionConflict
	}
	if err := fn(fakeSessionTx{store: s, sessionID: guard.SessionID}); err != nil {
		return guard, err
	}
	current.ExpectedRevision++
	s.guards[guard.SessionID] = current
	return current, nil
}

func (s *fakeSessionStore) Close() error { return nil }

type fakeSessionTx struct {
	store     *fakeSessionStore
	sessionID domain.ReviewSessionID
}

func (t fakeSessionTx) SaveSession(_ context.Context, session review.ReviewSession) error {
	if session.ID != t.sessionID {
		return ErrReviewStoreInput
	}
	t.store.sessions[session.ID] = session
	return nil
}
func (fakeSessionTx) SaveThread(context.Context, review.ReviewThread) error { return nil }
func (fakeSessionTx) SaveMessage(context.Context, review.Message) error     { return nil }
func (fakeSessionTx) SaveProviderConversation(context.Context, ProviderConversationRecord) error {
	return nil
}
func (fakeSessionTx) SaveProviderTurn(context.Context, ProviderTurnRecord) error { return nil }
func (fakeSessionTx) SaveCaptureGeneration(context.Context, CaptureGeneration, CaptureManifest) error {
	return nil
}
func (fakeSessionTx) SaveAcceptedTargetGeneration(context.Context, AcceptedTargetGeneration) error {
	return nil
}
func (fakeSessionTx) AppendAnchorVersion(context.Context, AnchorVersionWrite) (AnchorVersionRecord, error) {
	return AnchorVersionRecord{}, nil
}
func (fakeSessionTx) CreateReconciliation(context.Context, ReconciliationOperation) error { return nil }
func (fakeSessionTx) UpdateReconciliation(context.Context, ReconciliationOperation) error { return nil }
func (fakeSessionTx) StageReconciliationResult(context.Context, ReconciliationAnchorResult) error {
	return nil
}
func (fakeSessionTx) CompleteReconciliation(context.Context, domain.OperationID, time.Time) error {
	return nil
}
func (fakeSessionTx) ActivateReconciliation(context.Context, domain.OperationID) error { return nil }

var _ ReviewStore = (*fakeSessionStore)(nil)
var _ SessionLeaseManager = fakeLeaseManager{}
