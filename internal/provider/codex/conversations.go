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
	sandbox, err := mapThreadSandbox(request.Permissions, request.WorkingDir)
	if err != nil {
		return "", err
	}
	var response protocol.ThreadStartResponse
	if err := c.client.Call(ctx, "thread/start", protocol.ThreadStartParams{CWD: request.WorkingDir, Sandbox: sandbox}, &response); err != nil {
		return "", err
	}
	conversation, err := mapConversationResponse(response)
	if err != nil {
		return "", err
	}
	c.eventMu.Lock()
	if c.conversationBindings == nil {
		c.conversationBindings = make(map[provider.ProviderConversationRef]conversationEventBinding)
	}
	c.conversationBindings[conversation] = conversationEventBinding{ThreadID: request.ThreadID, OperationID: request.OperationID, CorrelationID: request.CorrelationID}
	c.eventMu.Unlock()
	return conversation, nil
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
	c.eventMu.RLock()
	binding, ok := c.conversationBindings[ref]
	c.eventMu.RUnlock()
	if ok && binding.ConversationID != "" && binding.ThreadID != "" && binding.OperationID != "" && binding.CorrelationID != "" {
		if err := c.publishProviderEvent(provider.ProviderEvent{Kind: provider.EventConversationResumed, ThreadID: binding.ThreadID, OperationID: binding.OperationID, CorrelationID: binding.CorrelationID, ConversationID: binding.ConversationID, ConversationRef: ref, Status: "resumed"}); err != nil {
			return err
		}
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
	sandbox, err := MapTurnPermissions(request.Mode, request.Permissions, request.WorkingDir)
	if err != nil {
		return "", err
	}
	var response protocol.TurnStartResponse
	if err := c.client.Call(ctx, "turn/start", protocol.TurnStartParams{
		ThreadID:      string(ref),
		Input:         []protocol.UserInput{{Type: "text", Text: request.Prompt}},
		CWD:           request.WorkingDir,
		SandboxPolicy: &sandbox,
	}, &response); err != nil {
		return "", err
	}
	turnRef, err := mapTurnResponse(response)
	if err != nil {
		return "", err
	}
	c.turnMu.Lock()
	c.turns[turnRef] = ref
	c.turnMu.Unlock()
	c.eventMu.Lock()
	if c.turnBindings == nil {
		c.turnBindings = make(map[provider.ProviderTurnRef]turnEventBinding)
	}
	binding := turnEventBinding{ConversationRef: ref, ThreadID: request.ThreadID, OperationID: request.OperationID, CorrelationID: request.CorrelationID}
	if conversation, ok := c.conversationBindings[ref]; ok {
		binding.ThreadID = conversation.ThreadID
		binding.ConversationID = conversation.ConversationID
	}
	c.turnBindings[turnRef] = binding
	c.eventMu.Unlock()
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
