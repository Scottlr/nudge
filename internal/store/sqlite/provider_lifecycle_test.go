package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestProviderLifecyclePersistsRestoresAndMarksRestartedTurnInterrupted(t *testing.T) {
	ctx := context.Background()
	store, guard, thread := newProviderLifecycleStore(t)
	fake := &providerLifecycleFake{}
	service, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Store: store, Provider: fake, Persistence: app.PersistenceDurable, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	conversationCommit, err := service.EnsureConversation(ctx, app.EnsureProviderConversation{
		Guard: guard, ThreadID: thread.ID, Mode: provider.TurnDiscuss, Permissions: promptOnlyPermissions(),
		OperationID: "op-create", CorrelationID: "corr-create",
	})
	if err != nil {
		t.Fatalf("ensure conversation: %v", err)
	}
	if conversationCommit.Conversation.ProviderConversationRef != "remote-thread-1" {
		t.Fatalf("conversation ref = %q", conversationCommit.Conversation.ProviderConversationRef)
	}

	restartedFake := &providerLifecycleFake{}
	restarted, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Store: store, Provider: restartedFake, Persistence: app.PersistenceDurable, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	resumeCommit, err := restarted.ResumeConversation(ctx, app.ResumeProviderConversation{
		Guard: conversationCommit.Guard, ThreadID: thread.ID, ConversationID: conversationCommit.Conversation.ID,
		OperationID: "op-resume", CorrelationID: "corr-resume",
	})
	if err != nil {
		t.Fatalf("resume conversation: %v", err)
	}
	if restartedFake.resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want 1", restartedFake.resumeCalls)
	}
	turnCommit, err := restarted.StartTurn(ctx, app.StartProviderTurn{
		Guard: resumeCommit.Guard, ThreadID: thread.ID, ConversationID: conversationCommit.Conversation.ID,
		Mode: provider.TurnDiscuss, Prompt: "hello", OperationID: "op-turn", CorrelationID: "corr-turn",
		Permissions: promptOnlyPermissions(),
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	loadedTurn, err := store.LoadProviderTurn(ctx, turnCommit.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedTurn.ProviderTurnRef != "remote-turn-1" || loadedTurn.CorrelationID != "corr-turn" {
		t.Fatalf("loaded turn = %#v", loadedTurn)
	}

	thirdService, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Store: store, Provider: &providerLifecycleFake{}, Persistence: app.PersistenceDurable, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := thirdService.Restore(ctx, turnCommit.Guard, thread.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	loadedTurn, err = store.LoadProviderTurn(ctx, turnCommit.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedTurn.State != app.ProviderTurnInterrupted || loadedTurn.CompletedAt == nil {
		t.Fatalf("restored turn = %#v, want interrupted with completion time", loadedTurn)
	}
	if loadedTurn.State == app.ProviderTurnCompleted {
		t.Fatal("restart marked the turn completed")
	}
}

func TestProviderLifecycleConcurrentEnsureDoesNotCreateDuplicateMapping(t *testing.T) {
	ctx := context.Background()
	store, guard, thread := newProviderLifecycleStore(t)
	fake := &providerLifecycleFake{}
	service, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Store: store, Provider: fake, Persistence: app.PersistenceDurable, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	command := app.EnsureProviderConversation{Guard: guard, ThreadID: thread.ID, Mode: provider.TurnDiscuss, Permissions: promptOnlyPermissions(), OperationID: "op-create", CorrelationID: "corr-create"}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.EnsureConversation(ctx, command)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, app.ErrProviderConversationInProgress) && !errors.Is(err, app.ErrSessionRevisionConflict) {
			t.Fatalf("concurrent ensure error = %v", err)
		}
	}
	if fake.startConversationCalls != 1 {
		t.Fatalf("provider starts = %d, want 1", fake.startConversationCalls)
	}
	record, err := store.LoadProviderConversationForThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.ProviderConversationRef != "remote-thread-1" {
		t.Fatalf("stored mapping = %#v", record)
	}
}

func TestProviderLifecycleUncertainNoPersistDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	_, _, thread := newProviderLifecycleStore(t)
	fake := &providerLifecycleFake{startConversationErr: errors.New("connection lost")}
	service, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Provider: fake, Persistence: app.PersistenceNoPersist, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	command := app.EnsureProviderConversation{ThreadID: thread.ID, Thread: thread, Mode: provider.TurnDiscuss, Permissions: promptOnlyPermissions(), OperationID: "op-create", CorrelationID: "corr-create"}
	first, err := service.EnsureConversation(ctx, command)
	if !errors.Is(err, app.ErrProviderConversationOrphan) {
		t.Fatalf("uncertain error = %v, want orphan", err)
	}
	if first.Conversation.State != app.ProviderConversationPossibleOrphan {
		t.Fatalf("uncertain state = %s", first.Conversation.State)
	}
	if _, err := service.EnsureConversation(ctx, command); !errors.Is(err, app.ErrProviderConversationOrphan) {
		t.Fatalf("retry error = %v, want orphan", err)
	}
	if fake.startConversationCalls != 1 {
		t.Fatalf("uncertain create retries = %d, want 1", fake.startConversationCalls)
	}
}

func TestProviderLifecycleExactOpaqueRefsRoundTripAndRejectOverflow(t *testing.T) {
	ctx := context.Background()
	store, guard, thread := newProviderLifecycleStore(t)
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	ref := provider.ProviderConversationRef(repeatOpaque(4096))
	record := app.ProviderConversationRecord{ID: "local-conversation", ThreadID: thread.ID, ProviderName: "codex", ProviderConversationRef: ref, ProviderVersion: "codex-test", OperationID: "op", CorrelationID: "corr", State: app.ProviderConversationAttachedState, CreatedAt: now, UpdatedAt: now}
	if _, err := store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error { return tx.SaveProviderConversation(ctx, record) }); err != nil {
		t.Fatalf("save exact ref: %v", err)
	}
	loaded, err := store.LoadProviderConversation(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ProviderConversationRef != ref {
		t.Fatal("exact conversation ref changed during round trip")
	}
	record.ProviderConversationRef = provider.ProviderConversationRef(repeatOpaque(4097))
	if err := record.Validate(); err == nil {
		t.Fatal("overflow conversation ref was accepted")
	}
}

func TestProviderLifecycleStaleGuardBlocksRemoteStart(t *testing.T) {
	ctx := context.Background()
	store, guard, thread := newProviderLifecycleStore(t)
	stale := guard
	guard, err := store.WithSessionTx(ctx, guard, func(app.ReviewStoreTx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	fake := &providerLifecycleFake{}
	service, err := app.NewProviderConversationService(app.ProviderConversationServiceConfig{Store: store, Provider: fake, Persistence: app.PersistenceDurable, ProviderVersion: "codex-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.EnsureConversation(ctx, app.EnsureProviderConversation{Guard: stale, ThreadID: thread.ID, Mode: provider.TurnDiscuss, Permissions: promptOnlyPermissions(), OperationID: "op", CorrelationID: "corr"})
	if !errors.Is(err, app.ErrSessionRevisionConflict) && !errors.Is(err, app.ErrSessionLeaseLost) {
		t.Fatalf("stale guard error = %v", err)
	}
	if fake.startConversationCalls != 0 {
		t.Fatalf("stale guard started provider call %d times", fake.startConversationCalls)
	}
	_ = guard
}

type providerLifecycleFake struct {
	mu                     sync.Mutex
	startConversationErr   error
	startConversationCalls int
	resumeCalls            int
	startTurnCalls         int
}

func (f *providerLifecycleFake) Probe(context.Context) (app.ProviderStatus, error) {
	gate := app.NewProviderDataDisclosureGate()
	_ = gate.Acknowledge(app.ProviderDisclosureVersionV1, time.Now().UTC(), app.DisclosureProcessOnly)
	return app.ProviderStatus{Connection: app.ProviderConnected, Capabilities: provider.ProviderCapabilities{ResumeConversation: true}, Account: app.ProviderAccountStatus{State: app.ProviderAccountAuthenticated}, Disclosure: gate}, nil
}

func (f *providerLifecycleFake) StartConversation(context.Context, provider.StartConversationRequest) (provider.ProviderConversationRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startConversationCalls++
	if f.startConversationErr != nil {
		return "", f.startConversationErr
	}
	return "remote-thread-1", nil
}

func (f *providerLifecycleFake) ResumeConversation(context.Context, provider.ProviderConversationRef) error {
	f.mu.Lock()
	f.resumeCalls++
	f.mu.Unlock()
	return nil
}

func (f *providerLifecycleFake) StartTurn(context.Context, provider.ProviderConversationRef, provider.TurnRequest) (provider.ProviderTurnRef, error) {
	f.mu.Lock()
	f.startTurnCalls++
	f.mu.Unlock()
	return "remote-turn-1", nil
}

func (f *providerLifecycleFake) SteerTurn(context.Context, provider.ProviderTurnRef, string) error {
	return nil
}

func (f *providerLifecycleFake) CancelTurn(context.Context, provider.ProviderTurnRef) error {
	return nil
}

func newProviderLifecycleStore(t *testing.T) (*Store, app.SessionWriteGuard, review.ReviewThread) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo, worktree, session, thread, _ := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatal(err)
	}
	guard, err := store.CreateSession(ctx, session, "lease-1")
	if err != nil {
		t.Fatal(err)
	}
	guard, err = store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error { return tx.SaveThread(ctx, thread) })
	if err != nil {
		t.Fatal(err)
	}
	return store, guard, thread
}

func promptOnlyPermissions() provider.TurnPermissionPolicy {
	return provider.TurnPermissionPolicy{Filesystem: provider.FilesystemPromptOnly, Network: provider.NetworkDisabled, RuntimeApprovals: provider.RuntimeApprovalsDisabled}
}

func repeatOpaque(size int) string {
	result := make([]byte, size)
	for index := range result {
		result[index] = 'x'
	}
	return string(result)
}
