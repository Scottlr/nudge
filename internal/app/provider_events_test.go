package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestCancelRace(t *testing.T) {
	router := NewTurnActionRouter(2)
	if err := router.Begin("thread-1"); err != nil {
		t.Fatal(err)
	}
	if route, err := router.Cancel("thread-1"); err != nil || route != TurnRouteCancelling {
		t.Fatalf("cancel = %s, %v", route, err)
	}
	_, queued, cancelled, err := router.Complete("thread-1")
	if err != nil || queued || !cancelled {
		t.Fatalf("completion = queued:%v cancelled:%v err:%v", queued, cancelled, err)
	}
}

func TestReplyQueuesWhileSteerIsExplicit(t *testing.T) {
	router := NewTurnActionRouter(2)
	if route, err := router.Reply(TurnIntent{ThreadID: "thread-1", Text: "first"}); err != nil || route != TurnRouteStarted {
		t.Fatalf("first reply = %s, %v", route, err)
	}
	if route, err := router.Reply(TurnIntent{ThreadID: "thread-1", Text: "second"}); err != nil || route != TurnRouteQueued {
		t.Fatalf("second reply = %s, %v", route, err)
	}
	if route, err := router.Steer("thread-1"); err != nil || route != TurnRouteSteered {
		t.Fatalf("steer = %s, %v", route, err)
	}
	next, queued, cancelled, err := router.Complete("thread-1")
	if err != nil || !queued || cancelled || next.Text != "second" {
		t.Fatalf("next = %#v queued:%v cancelled:%v err:%v", next, queued, cancelled, err)
	}
}

func TestDisconnectPersistsPartialMessage(t *testing.T) {
	processor, err := NewProviderEventProcessor(ProviderEventProcessorConfig{Persistence: PersistenceNoPersist, IDs: fixedIDSource{id: "message-1"}, Clock: fixedClock{when: time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	start := testProviderMessageEvent(provider.EventMessageStarted, 1, "")
	if _, err := processor.Handle(context.Background(), SessionWriteGuard{}, start); err != nil {
		t.Fatalf("start: %v", err)
	}
	delta := testProviderMessageEvent(provider.EventMessageDelta, 2, "partial")
	if _, err := processor.Handle(context.Background(), SessionWriteGuard{}, delta); err != nil {
		t.Fatalf("delta: %v", err)
	}
	commit, err := processor.Handle(context.Background(), SessionWriteGuard{}, provider.ProviderEvent{Kind: provider.EventDisconnected, Sequence: 3})
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if commit.Activity.Status != "disconnected" {
		t.Fatalf("activity = %#v", commit.Activity)
	}
	processor.mu.Lock()
	active := len(processor.messages)
	processor.mu.Unlock()
	if active != 0 {
		t.Fatalf("active partial messages after disconnect = %d", active)
	}
}

func TestStreamingMessageFreezesExactBodyIdentity(t *testing.T) {
	processor, err := NewProviderEventProcessor(ProviderEventProcessorConfig{Persistence: PersistenceNoPersist, IDs: fixedIDSource{id: "message-1"}, Clock: fixedClock{when: time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Handle(context.Background(), SessionWriteGuard{}, testProviderMessageEvent(provider.EventMessageStarted, 1, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.Handle(context.Background(), SessionWriteGuard{}, testProviderMessageEvent(provider.EventMessageDelta, 2, "hello")); err != nil {
		t.Fatal(err)
	}
	commit, err := processor.Handle(context.Background(), SessionWriteGuard{}, testProviderMessageEvent(provider.EventMessageCompleted, 3, ""))
	if err != nil {
		t.Fatal(err)
	}
	if commit.Identity == nil || commit.Identity.ChunkCount != 1 || commit.Identity.ByteLength != 5 {
		t.Fatalf("identity = %#v", commit.Identity)
	}
	digest := sha256.Sum256([]byte("hello"))
	if commit.Identity.SHA256 != hex.EncodeToString(digest[:]) || commit.Identity.TerminalStatus != "completed" {
		t.Fatalf("identity = %#v", commit.Identity)
	}
}

func testProviderMessageEvent(kind provider.EventKind, sequence uint64, text string) provider.ProviderEvent {
	return provider.ProviderEvent{Kind: kind, Sequence: sequence, ThreadID: domain.ReviewThreadID("thread-1"), OperationID: domain.OperationID("operation-1"), CorrelationID: provider.CorrelationID("correlation-1"), ConversationID: domain.ProviderConversationID("conversation-1"), ConversationRef: provider.ProviderConversationRef("remote-thread-1"), TurnID: domain.ProviderTurnID("turn-1"), TurnRef: provider.ProviderTurnRef("remote-turn-1"), ItemRef: "item-1", Text: text, CoalescingKey: "item-1", Coalescible: kind == provider.EventMessageDelta}
}
