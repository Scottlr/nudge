package codex

import (
	"errors"

	"github.com/Scottlr/nudge/internal/provider"
	"github.com/Scottlr/nudge/internal/provider/codex/protocol"
)

var ErrInvalidConversationResponse = errors.New("invalid codex conversation response")
var ErrInvalidTurnResponse = errors.New("invalid codex turn response")

func mapConversationResponse(response protocol.ThreadStartResponse) (provider.ProviderConversationRef, error) {
	ref := provider.ProviderConversationRef(response.Thread.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidConversationResponse
	}
	return ref, nil
}

func mapTurnResponse(response protocol.TurnStartResponse) (provider.ProviderTurnRef, error) {
	ref := provider.ProviderTurnRef(response.Turn.ID)
	if ref.Validate() != nil {
		return "", ErrInvalidTurnResponse
	}
	return ref, nil
}
