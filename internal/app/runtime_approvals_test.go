package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/provider"
)

type runtimeApprovalProviderFake struct {
	responses []provider.RuntimeApprovalResponse
}

func (f *runtimeApprovalProviderFake) Probe(context.Context) (ProviderStatus, error) {
	return ProviderStatus{}, nil
}
func (f *runtimeApprovalProviderFake) Connect(context.Context) error { return nil }
func (f *runtimeApprovalProviderFake) StartConversation(context.Context, provider.StartConversationRequest) (provider.ProviderConversationRef, error) {
	return "conversation", nil
}
func (f *runtimeApprovalProviderFake) ResumeConversation(context.Context, provider.ProviderConversationRef) error {
	return nil
}
func (f *runtimeApprovalProviderFake) StartTurn(context.Context, provider.ProviderConversationRef, provider.TurnRequest) (provider.ProviderTurnRef, error) {
	return "remote-turn", nil
}
func (f *runtimeApprovalProviderFake) SteerTurn(context.Context, provider.ProviderTurnRef, string) error {
	return nil
}
func (f *runtimeApprovalProviderFake) CancelTurn(context.Context, provider.ProviderTurnRef) error {
	return nil
}
func (f *runtimeApprovalProviderFake) RespondToRuntimeApproval(_ context.Context, response provider.RuntimeApprovalResponse) error {
	f.responses = append(f.responses, response)
	return nil
}
func (f *runtimeApprovalProviderFake) Events() <-chan provider.ProviderEvent { return nil }
func (f *runtimeApprovalProviderFake) Close() error                          { return nil }

func runtimeApprovalEvent(t *testing.T, kind provider.RuntimeApprovalKind, expires time.Time, details provider.RuntimeApprovalDetails) provider.ProviderEvent {
	t.Helper()
	scope := provider.RuntimeApprovalScope{Kind: kind}
	if kind == provider.RuntimeApprovalCommand {
		scope.Executable, _ = filepath.Abs(filepath.Join("Program Files", "Git", "bin", "git.exe"))
		scope.ArgumentsDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	} else {
		path, _ := filepath.Abs(filepath.Join("repo", "proposal"))
		scope.Path = provider.PermissionRoot{Path: path}
	}
	request := provider.RuntimeApprovalRequest{RequestID: "request-1", ThreadID: "thread-1", OperationID: "operation-1", CorrelationID: "correlation-1", TurnRef: "remote-turn", Scope: scope, ExpiresAt: expires}
	approval, err := provider.NewRuntimeApproval(request, expires.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approval.Details = details
	return provider.ProviderEvent{Kind: provider.EventRuntimeApprovalRequested, Sequence: 1, ThreadID: "thread-1", OperationID: "operation-1", CorrelationID: "correlation-1", ConversationID: "conversation-1", ConversationRef: "remote-thread", TurnID: domain.ProviderTurnID("turn-1"), TurnRef: "remote-turn", RequestID: request.RequestID, ExpiresAt: request.ExpiresAt, Scope: scope, Approval: &approval}
}

func TestDiscussionWriteRequestDenied(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	fake := &runtimeApprovalProviderFake{}
	service, err := NewRuntimeApprovalService(RuntimeApprovalServiceConfig{Provider: fake, Clock: fixedClock{when: now}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleProviderEvent(runtimeApprovalEvent(t, provider.RuntimeApprovalFile, now.Add(time.Minute), provider.RuntimeApprovalDetails{RequestedScope: "filesystem permission expansion"})); err != nil {
		t.Fatal(err)
	}
	approval, ok := service.Current()
	if !ok {
		t.Fatal("missing pending approval")
	}
	err = service.Respond(context.Background(), provider.RuntimeApprovalResponse{RequestID: approval.ID, ThreadID: approval.ThreadID, OperationID: approval.OperationID, TurnRef: approval.ProviderTurnRef, CorrelationID: approval.CorrelationID, Scope: approval.RequestedScopeID, Decision: provider.ApprovalAllowOnce})
	if err != ErrRuntimeApprovalPolicy || len(fake.responses) != 1 || fake.responses[0].Decision != provider.ApprovalDeny {
		t.Fatalf("err=%v responses=%#v, want policy denial", err, fake.responses)
	}
}

func TestExpiredApprovalDenied(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	fake := &runtimeApprovalProviderFake{}
	service, err := NewRuntimeApprovalService(RuntimeApprovalServiceConfig{Provider: fake, Clock: fixedClock{when: now}})
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(-time.Second)
	// Admission is deliberately independent from the test clock; expiry is
	// enforced when the user response is received.
	event := runtimeApprovalEvent(t, provider.RuntimeApprovalCommand, now.Add(time.Minute), provider.RuntimeApprovalDetails{ExactCommandArgs: "sentinel"})
	if _, err := service.HandleProviderEvent(event); err != nil {
		t.Fatal(err)
	}
	approval, _ := service.Current()
	approval.ExpiresAt = expires
	service.mu.Lock()
	service.pending[approval.ID].view.ExpiresAt = expires
	service.pending[approval.ID].providerApproval.Request.ExpiresAt = expires
	service.mu.Unlock()
	err = service.Respond(context.Background(), provider.RuntimeApprovalResponse{RequestID: approval.ID, ThreadID: approval.ThreadID, OperationID: approval.OperationID, TurnRef: approval.ProviderTurnRef, CorrelationID: approval.CorrelationID, Scope: approval.RequestedScopeID, Decision: provider.ApprovalAllowOnce})
	if err != provider.ErrApprovalExpired || len(fake.responses) != 1 || fake.responses[0].Decision != provider.ApprovalDeny {
		t.Fatalf("err=%v responses=%#v, want expired denial", err, fake.responses)
	}
}
