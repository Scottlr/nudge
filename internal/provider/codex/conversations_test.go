package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

func TestConnectionMapsConversationAndTurnLifecycle(t *testing.T) {
	client := newTestClient(t, "conversation_lifecycle", Config{})
	defer client.Close()
	connection := &Connection{client: client, turns: make(map[provider.ProviderTurnRef]provider.ProviderConversationRef)}
	request := provider.StartConversationRequest{
		ThreadID: "thread-1", OperationID: "operation-1", CorrelationID: "correlation-1", Mode: provider.TurnDiscuss,
		Permissions: provider.TurnPermissionPolicy{Filesystem: provider.FilesystemPromptOnly, Network: provider.NetworkDisabled, RuntimeApprovals: provider.RuntimeApprovalsDisabled},
	}
	conversation, err := connection.StartConversation(context.Background(), request)
	if err != nil || conversation != "codex-thread-1" {
		t.Fatalf("start conversation = %q, err=%v", conversation, err)
	}
	if err := connection.ResumeConversation(context.Background(), conversation); err != nil {
		t.Fatalf("resume conversation: %v", err)
	}
	turn, err := connection.StartTurn(context.Background(), conversation, provider.TurnRequest{
		ThreadID: "thread-1", OperationID: "operation-2", CorrelationID: "correlation-2", Mode: provider.TurnDiscuss,
		Prompt: "hello", Permissions: request.Permissions,
	})
	if err != nil || turn != "codex-turn-1" {
		t.Fatalf("start turn = %q, err=%v", turn, err)
	}
	if err := connection.SteerTurn(context.Background(), turn, "continue"); err != nil {
		t.Fatalf("steer turn: %v", err)
	}
	if err := connection.CancelTurn(context.Background(), turn); err != nil {
		t.Fatalf("interrupt turn: %v", err)
	}
	if err := connection.CancelTurn(context.Background(), turn); !errors.Is(err, ErrInvalidTurnResponse) {
		t.Fatalf("second interrupt error = %v, want invalid turn response", err)
	}
}

func TestCodexConversationAndTurnMappersPreserveExactOpaqueLimit(t *testing.T) {
	conversation, err := mapConversationResponse(protocol.ThreadStartResponse{Thread: protocol.ThreadSummary{ID: strings.Repeat("c", 4096)}})
	if err != nil || len(conversation) != 4096 {
		t.Fatalf("exact conversation mapping = %q, err=%v", conversation, err)
	}
	if _, err := mapConversationResponse(protocol.ThreadStartResponse{Thread: protocol.ThreadSummary{ID: strings.Repeat("c", 4097)}}); !errors.Is(err, ErrInvalidConversationResponse) {
		t.Fatalf("overflow conversation mapping error = %v", err)
	}
	turn, err := mapTurnResponse(protocol.TurnStartResponse{Turn: protocol.TurnSummary{ID: strings.Repeat("t", 4096)}})
	if err != nil || len(turn) != 4096 {
		t.Fatalf("exact turn mapping = %q, err=%v", turn, err)
	}
	if _, err := mapTurnResponse(protocol.TurnStartResponse{Turn: protocol.TurnSummary{ID: strings.Repeat("t", 4097)}}); !errors.Is(err, ErrInvalidTurnResponse) {
		t.Fatalf("overflow turn mapping error = %v", err)
	}
}
