package codex

import (
	"context"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
)

func TestDeltaCoalescingBounded(t *testing.T) {
	stream := newProviderEventStream(EventStreamConfig{Capacity: 1, ResidentBytes: 4096, CoalesceBytes: 32})
	defer stream.Close()
	base := provider.ProviderEvent{Kind: provider.EventMessageDelta, Sequence: 1, ThreadID: "thread-1", OperationID: "operation-1", CorrelationID: "correlation-1", ConversationID: "conversation-1", ConversationRef: "remote-thread-1", TurnID: "turn-1", TurnRef: "remote-turn-1", ItemRef: "item-1", Text: "a", CoalescingKey: "item-1", Coalescible: true}
	for _, event := range []provider.ProviderEvent{base, func() provider.ProviderEvent { value := base; value.Sequence = 2; value.Text = "b"; return value }(), func() provider.ProviderEvent { value := base; value.Sequence = 3; value.Text = "c"; return value }()} {
		if got := stream.Deliver(context.Background(), event); got != provider.EventAccepted {
			t.Fatalf("Deliver() = %s for %#v", got, event)
		}
	}
	select {
	case merged := <-stream.Events():
		if merged.Text != "abc" || merged.Sequence != 3 {
			t.Fatalf("merged event = %#v", merged)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced event not delivered")
	}
}

func TestProviderEventStreamRejectsNonCoalescibleSaturation(t *testing.T) {
	stream := newProviderEventStream(EventStreamConfig{Capacity: 1, ResidentBytes: 4096})
	defer stream.Close()
	base := provider.ProviderEvent{Kind: provider.EventTurnStarted, Sequence: 1, ThreadID: domain.ReviewThreadID("thread-1"), OperationID: domain.OperationID("operation-1"), CorrelationID: provider.CorrelationID("correlation-1"), ConversationID: domain.ProviderConversationID("conversation-1"), ConversationRef: "remote-thread-1", TurnID: domain.ProviderTurnID("turn-1"), TurnRef: "remote-turn-1", Status: "in_progress"}
	if stream.Deliver(context.Background(), base) != provider.EventAccepted {
		t.Fatal("first lifecycle event was not accepted")
	}
	deadline := time.Now().Add(time.Second)
	for {
		output, _ := streamCounts(stream)
		if output > 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if output, _ := streamCounts(stream); output == 0 {
		t.Fatal("first lifecycle event did not reach bounded output")
	}
	second := base
	second.Sequence = 2
	if stream.Deliver(context.Background(), second) != provider.EventAccepted {
		t.Fatal("second lifecycle event was not accepted")
	}
	deadline = time.Now().Add(time.Second)
	for {
		_, queued := streamCounts(stream)
		if queued > 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	third := base
	third.Sequence = 3
	if stream.Deliver(context.Background(), third) != provider.EventBackpressure {
		t.Fatal("saturated lifecycle event was not rejected")
	}
}

func streamCounts(stream *providerEventStream) (int, int) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return len(stream.out), len(stream.queue)
}
