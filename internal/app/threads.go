package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrThreadServiceUnavailable reports an application composition without a
	// review store-backed thread service.
	ErrThreadServiceUnavailable = errors.New("thread service unavailable")
	// ErrThreadCommentEmpty reports a comment with no nonblank line.
	ErrThreadCommentEmpty = errors.New("thread comment is empty")
	// ErrThreadCommentTooLarge reports a comment beyond the 64 KiB UTF-8 cap.
	ErrThreadCommentTooLarge = errors.New("thread comment exceeds 64 KiB")
	// ErrThreadCommentInvalid reports a comment containing invalid or unsafe
	// text for normalized message storage.
	ErrThreadCommentInvalid = errors.New("invalid thread comment")
	// ErrThreadAnchorEvidenceTooLarge reports an anchor snippet beyond the
	// mandatory 256 KiB evidence cap.
	ErrThreadAnchorEvidenceTooLarge = errors.New("thread anchor evidence exceeds 256 KiB")
	// ErrThreadNotOwned reports a thread that is not part of the guarded
	// session.
	ErrThreadNotOwned = errors.New("thread does not belong to session")
	// ErrThreadIDCollision reports an identity source collision.
	ErrThreadIDCollision = errors.New("thread identity collision")
)

// MaxCommentBytes is the application-authoritative UTF-8 concern and reply
// limit. The editor mirrors this value at its UI boundary.
const MaxCommentBytes = 64 << 10

// CreateThread persists one anchored concern before returning its creation
// event. The provider conversation remains unlinked at this point.
type CreateThread struct {
	Guard         SessionWriteGuard
	Anchor        review.CodeAnchor
	Comment       string
	CorrelationID CorrelationID
}

// ActivateThread changes only the active-thread projection.
type ActivateThread struct {
	SessionID     domain.ReviewSessionID
	ThreadID      domain.ReviewThreadID
	CorrelationID CorrelationID
}

// ReplyToThread persists a local user reply as a pending normalized message.
type ReplyToThread struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	Text          string
	CorrelationID CorrelationID
}

// ResolveThread changes only the resolution axis of one review thread.
type ResolveThread struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	Resolved      bool
	CorrelationID CorrelationID
}

// MarkThreadRead explicitly clears the unread axis after a caller confirms
// that the current discussion content was presented.
type MarkThreadRead struct {
	Guard         SessionWriteGuard
	ThreadID      domain.ReviewThreadID
	CorrelationID CorrelationID
}

func (CreateThread) isReducerInput()   {}
func (ActivateThread) isReducerInput() {}
func (ReplyToThread) isReducerInput()  {}
func (ResolveThread) isReducerInput()  {}
func (MarkThreadRead) isReducerInput() {}

func (CreateThread) isCommand()   {}
func (ActivateThread) isCommand() {}
func (ReplyToThread) isCommand()  {}
func (ResolveThread) isCommand()  {}
func (MarkThreadRead) isCommand() {}

// ThreadServiceConfig composes the application thread use cases with the
// fenced review store and deterministic identity/time sources.
type ThreadServiceConfig struct {
	Store ReviewStore
	Clock Clock
	IDs   IDSource
}

// ThreadService owns persistence-first thread mutations. It does not contact
// a provider or publish to a UI queue; callers publish returned events only
// after the transaction succeeds.
type ThreadService struct {
	store ReviewStore
	clock Clock
	ids   IDSource
}

// NewThreadService validates the application composition for thread use
// cases. A durable store is required because v1 threads are persistence-first.
func NewThreadService(config ThreadServiceConfig) (*ThreadService, error) {
	if config.Store == nil {
		return nil, ErrThreadServiceUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	return &ThreadService{store: config.Store, clock: config.Clock, ids: config.IDs}, nil
}

// ThreadUseCases is the descriptive alias used by composition code.
type ThreadUseCases = ThreadService

// NewThreadUseCases constructs the persistence-first thread use cases.
func NewThreadUseCases(config ThreadServiceConfig) (*ThreadUseCases, error) {
	return NewThreadService(config)
}

// ThreadCommit is the durable result of one thread mutation. Events are
// returned only after the store transaction has committed.
type ThreadCommit struct {
	Guard   SessionWriteGuard
	Thread  review.ReviewThread
	Message *review.Message
	Events  []Event
}

func summarizeReviewThread(thread review.ReviewThread) ThreadSummary {
	return ThreadSummary{
		ID:                     thread.ID,
		SessionID:              thread.SessionID,
		Title:                  thread.Title,
		Resolution:             thread.Resolution,
		Conversation:           thread.Conversation,
		Proposal:               thread.Proposal,
		Anchor:                 thread.Anchor.State,
		Read:                   thread.Read,
		FailurePhase:           thread.FailurePhase,
		ErrorCode:              thread.ErrorCode,
		AnchorPath:             repository.RepoPath(thread.Anchor.Path.Bytes()),
		Unread:                 thread.Read == review.Unread,
		ProviderConversationID: cloneProviderConversationID(thread.ProviderConversationID),
		LatestProposalID:       cloneProposalID(thread.LatestProposalID),
		UpdatedAt:              thread.UpdatedAt,
	}
}

// CreateThread atomically persists the thread, its first anchor version, and
// its initial pending user message before returning ThreadCreated.
func (s *ThreadService) CreateThread(ctx context.Context, guard SessionWriteGuard, command CreateThread) (ThreadCommit, error) {
	if err := s.validateMutation(ctx, guard); err != nil {
		return ThreadCommit{}, err
	}
	comment, err := normalizeThreadComment(command.Comment)
	if err != nil {
		return ThreadCommit{}, err
	}
	if err := validateThreadAnchor(command.Anchor); err != nil {
		return ThreadCommit{}, err
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ThreadCommit{}, ErrReviewStoreInput
	}
	threadID, messageID, err := s.newThreadAndMessageIDs()
	if err != nil {
		return ThreadCommit{}, err
	}
	thread, err := review.NewOpenReviewThread(threadID, guard.SessionID, command.Anchor, now)
	if err != nil {
		return ThreadCommit{}, fmt.Errorf("create thread: %w", err)
	}
	thread.Title = deriveThreadTitle(comment)
	message, err := review.NewPendingMessage(messageID, threadID, review.RoleUser, 1, now)
	if err != nil {
		return ThreadCommit{}, fmt.Errorf("create initial message: %w", err)
	}
	message.Content = comment
	message, err = review.NewMessage(message)
	if err != nil {
		return ThreadCommit{}, fmt.Errorf("validate initial message: %w", err)
	}
	if err := thread.AppendMessageID(message.ID, now); err != nil {
		return ThreadCommit{}, fmt.Errorf("attach initial message: %w", err)
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
		Events:  []Event{ThreadCreated{CorrelationID: command.CorrelationID, SessionID: thread.SessionID, ThreadID: thread.ID, InitialMessageID: message.ID, Title: thread.Title, AnchorPath: thread.Anchor.Path.Bytes(), TargetGeneration: thread.Anchor.TargetGeneration}},
	}, nil
}

// ActivateThread validates and returns an activation event without changing
// durable state. Active-thread state is a canonical application projection.
func (s *ThreadService) ActivateThread(ctx context.Context, command ActivateThread) (ThreadCommit, error) {
	if s == nil || s.store == nil || ctx == nil || command.SessionID == "" || command.ThreadID == "" {
		return ThreadCommit{}, ErrReviewStoreInput
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ThreadCommit{}, err
	}
	if thread.SessionID != command.SessionID {
		return ThreadCommit{}, ErrThreadNotOwned
	}
	return ThreadCommit{Thread: thread, Events: []Event{ThreadActivated{CorrelationID: command.CorrelationID, SessionID: thread.SessionID, ThreadID: thread.ID, TargetGeneration: thread.Anchor.TargetGeneration}}}, nil
}

// ResolveThread changes only resolution, preserving provider, proposal,
// anchor, and read axes.
func (s *ThreadService) ResolveThread(ctx context.Context, guard SessionWriteGuard, command ResolveThread) (ThreadCommit, error) {
	if err := s.validateMutation(ctx, guard); err != nil {
		return ThreadCommit{}, err
	}
	thread, err := s.loadOwnedThread(ctx, guard.SessionID, command.ThreadID)
	if err != nil {
		return ThreadCommit{}, err
	}
	now := s.clock.Now().UTC()
	if command.Resolved {
		err = thread.Resolve(now)
	} else {
		err = thread.Reopen(now)
	}
	if err != nil {
		return ThreadCommit{}, err
	}
	nextGuard, err := s.saveThread(ctx, guard, thread)
	if err != nil {
		return ThreadCommit{}, err
	}
	return ThreadCommit{Guard: nextGuard, Thread: thread, Events: []Event{ThreadResolutionChanged{CorrelationID: command.CorrelationID, SessionID: thread.SessionID, ThreadID: thread.ID, Resolved: command.Resolved, TargetGeneration: thread.Anchor.TargetGeneration}}}, nil
}

// MarkThreadRead clears only the notification axis. Callers must dispatch it
// explicitly after presentation; active-thread selection is not sufficient.
func (s *ThreadService) MarkThreadRead(ctx context.Context, guard SessionWriteGuard, command MarkThreadRead) (ThreadCommit, error) {
	if err := s.validateMutation(ctx, guard); err != nil {
		return ThreadCommit{}, err
	}
	thread, err := s.loadOwnedThread(ctx, guard.SessionID, command.ThreadID)
	if err != nil {
		return ThreadCommit{}, err
	}
	if thread.Read == review.Read {
		return ThreadCommit{Guard: guard, Thread: thread}, nil
	}
	if err := thread.MarkRead(s.clock.Now().UTC()); err != nil {
		return ThreadCommit{}, err
	}
	nextGuard, err := s.saveThread(ctx, guard, thread)
	if err != nil {
		return ThreadCommit{}, err
	}
	return ThreadCommit{Guard: nextGuard, Thread: thread, Events: []Event{ThreadReadChanged{CorrelationID: command.CorrelationID, SessionID: thread.SessionID, ThreadID: thread.ID, Read: true, TargetGeneration: thread.Anchor.TargetGeneration}}}, nil
}

func (s *ThreadService) validateMutation(ctx context.Context, guard SessionWriteGuard) error {
	if s == nil || s.store == nil || ctx == nil {
		return ErrThreadServiceUnavailable
	}
	return guard.Validate()
}

func (s *ThreadService) loadOwnedThread(ctx context.Context, sessionID domain.ReviewSessionID, threadID domain.ReviewThreadID) (review.ReviewThread, error) {
	if threadID == "" {
		return review.ReviewThread{}, ErrReviewStoreInput
	}
	thread, err := s.store.LoadThread(ctx, threadID)
	if err != nil {
		return review.ReviewThread{}, err
	}
	if thread.SessionID != sessionID {
		return review.ReviewThread{}, ErrThreadNotOwned
	}
	return thread, nil
}

func (s *ThreadService) saveThread(ctx context.Context, guard SessionWriteGuard, thread review.ReviewThread) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		return tx.SaveThread(ctx, thread)
	})
}

func (s *ThreadService) newThreadAndMessageIDs() (domain.ReviewThreadID, domain.MessageID, error) {
	threadID, err := domain.NewReviewThreadID(s.ids.NewID())
	if err != nil {
		return "", "", ErrThreadIDCollision
	}
	messageID, err := domain.NewMessageID(s.ids.NewID())
	if err != nil || string(messageID) == string(threadID) {
		return "", "", ErrThreadIDCollision
	}
	return threadID, messageID, nil
}

func validateThreadAnchor(anchor review.CodeAnchor) error {
	if err := anchor.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAnchorSelection, err)
	}
	if len([]byte(anchor.SelectedText)) > MaxAnchorSelectionBytes {
		return ErrThreadAnchorEvidenceTooLarge
	}
	return nil
}

func deriveThreadTitle(comment string) string {
	for _, line := range strings.Split(comment, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > 120 {
			runes = runes[:120]
		}
		return string(runes)
	}
	return ""
}

func normalizeThreadComment(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", ErrThreadCommentInvalid
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	if len([]byte(value)) > MaxCommentBytes {
		return "", ErrThreadCommentTooLarge
	}
	lines := strings.Split(value, "\n")
	first, last := 0, len(lines)-1
	for first <= last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last >= first && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if first > last {
		return "", ErrThreadCommentEmpty
	}
	value = strings.Join(lines[first:last+1], "\n")
	for _, r := range value {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return "", ErrThreadCommentInvalid
		}
	}
	return value, nil
}
