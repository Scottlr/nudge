// Package review models Nudge's provider-independent review domain.
package review

import (
	"errors"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrInvalidReviewSession reports contradictory session metadata.
	ErrInvalidReviewSession = errors.New("invalid review session")
	// ErrInvalidReviewThread reports contradictory thread metadata.
	ErrInvalidReviewThread = errors.New("invalid review thread")
	// ErrInvalidCodeAnchor reports incomplete or contradictory anchor evidence.
	ErrInvalidCodeAnchor = errors.New("invalid code anchor")
	// ErrInvalidMessage reports incomplete or contradictory message metadata.
	ErrInvalidMessage = errors.New("invalid review message")
	// ErrInvalidStatusTransition reports a lifecycle transition that cannot be
	// represented without rewriting history.
	ErrInvalidStatusTransition = errors.New("invalid review status transition")
)

func validReviewID(value string) bool {
	return value != ""
}

func validMetadata(value string) bool {
	return value != "" && utf8.ValidString(value)
}

func validOptionalMetadata(value string) bool {
	return value == "" || utf8.ValidString(value)
}

func validContent(value string) bool {
	return utf8.ValidString(value)
}

func validDomainID(id any) bool {
	switch value := id.(type) {
	case domain.RepositoryID:
		return validReviewID(string(value))
	case domain.ReviewSessionID:
		return validReviewID(string(value))
	case domain.ReviewThreadID:
		return validReviewID(string(value))
	case domain.MessageID:
		return validReviewID(string(value))
	case domain.ProposalID:
		return validReviewID(string(value))
	case domain.ProviderConversationID:
		return validReviewID(string(value))
	default:
		return false
	}
}
