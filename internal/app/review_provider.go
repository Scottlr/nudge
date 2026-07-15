package app

import (
	"context"

	"github.com/Scottlr/nudge/internal/provider"
)

// ReviewProvider is the consumer-owned port for Nudge's focused provider
// discussions and explicitly authorized proposal turns. It intentionally has
// no patch-approval or generic task-execution method.
type ReviewProvider interface {
	Probe(ctx context.Context) (ProviderStatus, error)
	Connect(ctx context.Context) error

	StartConversation(ctx context.Context, req provider.StartConversationRequest) (provider.ProviderConversationRef, error)
	ResumeConversation(ctx context.Context, ref provider.ProviderConversationRef) error
	StartTurn(ctx context.Context, ref provider.ProviderConversationRef, req provider.TurnRequest) (provider.ProviderTurnRef, error)
	SteerTurn(ctx context.Context, ref provider.ProviderTurnRef, input string) error
	CancelTurn(ctx context.Context, ref provider.ProviderTurnRef) error
	RespondToRuntimeApproval(ctx context.Context, response provider.RuntimeApprovalResponse) error
	Events() <-chan provider.ProviderEvent
	Close() error
}

// ProviderEventSink is the application-owned destination for normalized
// provider events. A provider must observe backpressure or closure instead of
// silently discarding lifecycle events.
type ProviderEventSink interface {
	Deliver(ctx context.Context, event provider.ProviderEvent) provider.EventDelivery
}
