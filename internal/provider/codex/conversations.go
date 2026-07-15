package codex

import (
	"context"

	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

// StartConversation creates one opaque Codex thread after the application has
// journaled its local conversation intent.
func (c *Connection) StartConversation(ctx context.Context, request provider.StartConversationRequest) (provider.ProviderConversationRef, error) {
	if c == nil || c.client == nil {
		return "", ErrClientClosed
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	var response protocol.ThreadStartResponse
	if err := c.client.Call(ctx, "thread/start", protocol.ThreadStartParams{}, &response); err != nil {
		return "", err
	}
	return mapConversationResponse(response)
}

// ResumeConversation resumes an existing opaque Codex thread. The provider
// history is not used as Nudge's normalized transcript.
func (c *Connection) ResumeConversation(ctx context.Context, ref provider.ProviderConversationRef) error {
	if c == nil || c.client == nil {
		return ErrClientClosed
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	var response protocol.ThreadResumeResponse
	if err := c.client.Call(ctx, "thread/resume", protocol.ThreadResumeParams{ThreadID: string(ref)}, &response); err != nil {
		return err
	}
	resumed := provider.ProviderConversationRef(response.Thread.ID)
	if resumed.Validate() != nil || resumed != ref {
		return ErrInvalidConversationResponse
	}
	return nil
}

// StartTurn maps one bounded application turn request to the stable text input
// shape. Prompt construction and permission policy remain application-owned.
func (c *Connection) StartTurn(ctx context.Context, ref provider.ProviderConversationRef, request provider.TurnRequest) (provider.ProviderTurnRef, error) {
	if c == nil || c.client == nil {
		return "", ErrClientClosed
	}
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	var response protocol.TurnStartResponse
	if err := c.client.Call(ctx, "turn/start", protocol.TurnStartParams{ThreadID: string(ref), Input: []protocol.UserInput{{Type: "text", Text: request.Prompt}}}, &response); err != nil {
		return "", err
	}
	turnRef, err := mapTurnResponse(response)
	if err != nil {
		return "", err
	}
	c.turnMu.Lock()
	c.turns[turnRef] = ref
	c.turnMu.Unlock()
	return turnRef, nil
}

// SteerTurn sends intentional additional guidance to the mapped active turn.
func (c *Connection) SteerTurn(ctx context.Context, ref provider.ProviderTurnRef, input string) error {
	if c == nil || c.client == nil {
		return ErrClientClosed
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if err := provider.ValidateSteeringInput(input); err != nil {
		return err
	}
	c.turnMu.Lock()
	conversationRef, ok := c.turns[ref]
	c.turnMu.Unlock()
	if !ok {
		return ErrInvalidTurnResponse
	}
	var response protocol.TurnSteerResponse
	if err := c.client.Call(ctx, "turn/steer", protocol.TurnSteerParams{ThreadID: string(conversationRef), ExpectedTurnID: string(ref), Input: []protocol.UserInput{{Type: "text", Text: input}}}, &response); err != nil {
		return err
	}
	if response.TurnID != string(ref) {
		return ErrInvalidTurnResponse
	}
	return nil
}

// CancelTurn interrupts one mapped active turn.
func (c *Connection) CancelTurn(ctx context.Context, ref provider.ProviderTurnRef) error {
	if c == nil || c.client == nil {
		return ErrClientClosed
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	c.turnMu.Lock()
	conversationRef, ok := c.turns[ref]
	c.turnMu.Unlock()
	if !ok {
		return ErrInvalidTurnResponse
	}
	var response protocol.TurnInterruptResponse
	if err := c.client.Call(ctx, "turn/interrupt", protocol.TurnInterruptParams{ThreadID: string(conversationRef), TurnID: string(ref)}, &response); err != nil {
		return err
	}
	c.turnMu.Lock()
	delete(c.turns, ref)
	c.turnMu.Unlock()
	return nil
}
