package app

import (
	"context"
	"fmt"
	"math"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// ReplyToThread persists a local user reply as a pending message before any
// provider delivery is attempted.
func (s *ThreadService) ReplyToThread(ctx context.Context, guard SessionWriteGuard, command ReplyToThread) (ThreadCommit, error) {
	if err := s.validateMutation(ctx, guard); err != nil {
		return ThreadCommit{}, err
	}
	text, err := normalizeThreadComment(command.Text)
	if err != nil {
		return ThreadCommit{}, err
	}
	thread, err := s.loadOwnedThread(ctx, guard.SessionID, command.ThreadID)
	if err != nil {
		return ThreadCommit{}, err
	}
	ordinal, err := s.nextMessageOrdinal(ctx, thread.ID)
	if err != nil {
		return ThreadCommit{}, err
	}
	messageID, err := domain.NewMessageID(s.ids.NewID())
	if err != nil {
		return ThreadCommit{}, ErrThreadIDCollision
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ThreadCommit{}, ErrReviewStoreInput
	}
	message, err := review.NewPendingMessage(messageID, thread.ID, review.RoleUser, ordinal, now)
	if err != nil {
		return ThreadCommit{}, err
	}
	message.Content = text
	message, err = review.NewMessage(message)
	if err != nil {
		return ThreadCommit{}, fmt.Errorf("validate reply: %w", err)
	}
	if err := thread.AppendMessageID(message.ID, now); err != nil {
		return ThreadCommit{}, err
	}
	nextGuard, err := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		if err := tx.SaveThread(ctx, thread); err != nil {
			return err
		}
		return tx.SaveMessage(ctx, message)
	})
	if err != nil {
		return ThreadCommit{}, err
	}
	return ThreadCommit{
		Guard:   nextGuard,
		Thread:  thread,
		Message: &message,
		Events:  []Event{MessageAppended{CorrelationID: command.CorrelationID, SessionID: thread.SessionID, ThreadID: thread.ID, MessageID: message.ID, Role: message.Role, Status: message.Status, TargetGeneration: thread.Anchor.TargetGeneration}},
	}, nil
}

func (s *ThreadService) nextMessageOrdinal(ctx context.Context, threadID domain.ReviewThreadID) (uint64, error) {
	page := MessagePage{ThreadID: threadID, Limit: MaxPageLimit}
	var highest uint64
	for {
		result, err := s.store.ListMessages(ctx, threadID, page)
		if err != nil {
			return 0, err
		}
		for _, item := range result.Items {
			if item.Ordinal > highest {
				highest = item.Ordinal
			}
		}
		if !result.HasMore {
			break
		}
		if result.Next == nil {
			return 0, ErrReviewStoreCorrupt
		}
		page.Cursor = result.Next
	}
	if highest == math.MaxUint64 {
		return 0, ErrReviewStoreInput
	}
	return highest + 1, nil
}
