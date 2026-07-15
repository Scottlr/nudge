package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

var (
	ErrProviderMessageOrder = errors.New("provider message event out of order")
	ErrProviderMessageLimit = errors.New("provider message body limit exceeded")
)

// ProviderActivity is the UI-safe progress projection emitted alongside
// durable transcript changes. It intentionally carries no raw provider body.
type ProviderActivity struct {
	EventKind provider.EventKind
	ThreadID  domain.ReviewThreadID
	TurnID    domain.ProviderTurnID
	MessageID domain.MessageID
	ItemRef   string
	Status    string
}

// ProviderEventCommit is the fenced outcome of one normalized provider event.
type ProviderEventCommit struct {
	Guard    SessionWriteGuard
	Activity ProviderActivity
	Message  *review.Message
	Identity *MessageBodyIdentity
}

type providerMessageKey struct {
	ThreadID domain.ReviewThreadID
	TurnID   domain.ProviderTurnID
	ItemRef  string
}

type providerMessageStream struct {
	message     review.Message
	key         providerMessageKey
	content     strings.Builder
	digest      hash.Hash
	length      uint64
	chunkCount  uint64
	lastUpdated time.Time
	terminal    bool
}

// ProviderEventProcessor normalizes accepted provider events into durable
// message chunks and bounded UI activity. It is deliberately single-writer:
// callers feed it from one owner goroutine or serialize Handle calls.
type ProviderEventProcessor struct {
	store       ReviewStore
	clock       Clock
	ids         IDSource
	persistence PersistenceMode

	mu        sync.Mutex
	messages  map[providerMessageKey]*providerMessageStream
	activity  chan ProviderActivity
	closeOnce sync.Once
}

// ProviderEventProcessorConfig composes durable and no-persist streaming.
type ProviderEventProcessorConfig struct {
	Store          ReviewStore
	Clock          Clock
	IDs            IDSource
	Persistence    PersistenceMode
	ActivityBuffer int
}

// NewProviderEventProcessor constructs the one-writer provider event owner.
func NewProviderEventProcessor(config ProviderEventProcessorConfig) (*ProviderEventProcessor, error) {
	if config.Persistence != PersistenceDurable && config.Persistence != PersistenceNoPersist {
		return nil, ErrProviderConversationUnavailable
	}
	if config.Persistence == PersistenceDurable && config.Store == nil {
		return nil, ErrProviderConversationUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	if config.ActivityBuffer <= 0 {
		config.ActivityBuffer = 64
	}
	return &ProviderEventProcessor{store: config.Store, clock: config.Clock, ids: config.IDs, persistence: config.Persistence, messages: make(map[providerMessageKey]*providerMessageStream), activity: make(chan ProviderActivity, config.ActivityBuffer)}, nil
}

// Activities exposes a bounded latest lifecycle stream for UI projections.
func (p *ProviderEventProcessor) Activities() <-chan ProviderActivity {
	if p == nil {
		return nil
	}
	return p.activity
}

// Close closes the activity stream after the processor owner has stopped.
func (p *ProviderEventProcessor) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() { close(p.activity) })
}

// Run consumes a provider event source until it closes or the context ends.
// The returned channel reports the first processing error and then closes.
func (p *ProviderEventProcessor) Run(ctx context.Context, guard SessionWriteGuard, source <-chan provider.ProviderEvent) <-chan error {
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		for {
			select {
			case <-ctx.Done():
				if ctx.Err() != context.Canceled {
					errs <- ctx.Err()
				}
				return
			case event, ok := <-source:
				if !ok {
					return
				}
				commit, err := p.Handle(ctx, guard, event)
				if err != nil {
					errs <- err
					return
				}
				guard = commit.Guard
			}
		}
	}()
	return errs
}

// Handle applies one normalized event and advances the session fence for each
// accepted durable mutation. No event is reordered or silently swallowed.
func (p *ProviderEventProcessor) Handle(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent) (ProviderEventCommit, error) {
	if p == nil || ctx == nil || event.Validate(provider.DefaultValidationLimits()) != nil {
		return ProviderEventCommit{}, ErrProviderMessageOrder
	}
	switch event.Kind {
	case provider.EventMessageStarted:
		return p.startMessage(ctx, guard, event)
	case provider.EventMessageDelta:
		return p.appendMessage(ctx, guard, event)
	case provider.EventMessageCompleted:
		return p.finishMessage(ctx, guard, event, review.MessageCompleted, review.FailurePhaseNone, "")
	case provider.EventTurnCompleted:
		return p.finishTurnMessages(ctx, guard, event, review.MessageCompleted, review.FailurePhaseNone, "")
	case provider.EventTurnFailed:
		status := review.MessageFailed
		if event.Status == "interrupted" || event.Status == "cancelled" {
			status = review.MessageCancelled
		}
		return p.finishTurnMessages(ctx, guard, event, status, review.FailurePhaseProvider, boundedErrorCode(event.Error, "provider_turn_failed"))
	case provider.EventDisconnected:
		return p.finishDisconnected(ctx, guard, event)
	default:
		activity := ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: event.ItemRef, Status: event.Status}
		p.publishActivity(activity)
		return ProviderEventCommit{Guard: guard, Activity: activity}, nil
	}
}

func (p *ProviderEventProcessor) startMessage(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent) (ProviderEventCommit, error) {
	if event.ThreadID == "" || event.TurnID == "" || event.ItemRef == "" {
		return ProviderEventCommit{}, ErrProviderMessageOrder
	}
	key := providerMessageKey{ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: event.ItemRef}
	p.mu.Lock()
	if _, exists := p.messages[key]; exists {
		p.mu.Unlock()
		return ProviderEventCommit{Guard: guard, Activity: ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: event.ItemRef, Status: "duplicate"}}, nil
	}
	p.mu.Unlock()
	ordinal, err := p.nextMessageOrdinal(ctx, event.ThreadID)
	if err != nil {
		return ProviderEventCommit{}, err
	}
	id, err := domain.NewMessageID(p.ids.NewID())
	if err != nil {
		return ProviderEventCommit{}, ErrProviderMessageOrder
	}
	now := p.clock.Now().UTC()
	message, err := review.NewPendingMessage(id, event.ThreadID, review.RoleAssistant, ordinal, now)
	if err != nil {
		return ProviderEventCommit{}, err
	}
	message.ProviderID = event.ItemRef
	if err := message.BeginStreaming(now); err != nil {
		return ProviderEventCommit{}, err
	}
	if p.persistence == PersistenceDurable {
		thread, err := p.store.LoadThread(ctx, event.ThreadID)
		if err != nil {
			return ProviderEventCommit{}, err
		}
		if err := thread.AppendMessageID(message.ID, now); err != nil {
			return ProviderEventCommit{}, err
		}
		guard, err = p.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
			if err := tx.SaveThread(ctx, thread); err != nil {
				return err
			}
			return tx.SaveMessage(ctx, message)
		})
		if err != nil {
			return ProviderEventCommit{}, err
		}
	}
	stream := &providerMessageStream{message: message, key: key, digest: sha256.New(), lastUpdated: now}
	p.mu.Lock()
	p.messages[key] = stream
	p.mu.Unlock()
	activity := ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, MessageID: message.ID, ItemRef: event.ItemRef, Status: string(review.MessageStreaming)}
	p.publishActivity(activity)
	return ProviderEventCommit{Guard: guard, Activity: activity, Message: &message}, nil
}

func (p *ProviderEventProcessor) appendMessage(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent) (ProviderEventCommit, error) {
	key := providerMessageKey{ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: event.ItemRef}
	p.mu.Lock()
	stream := p.messages[key]
	p.mu.Unlock()
	if stream == nil || stream.terminal {
		return ProviderEventCommit{}, ErrProviderMessageOrder
	}
	if uint64(stream.length)+uint64(len([]byte(event.Text))) > MaxStreamedMessageBytes {
		return ProviderEventCommit{}, ErrProviderMessageLimit
	}
	parts := splitMessageBytes([]byte(event.Text), int(MaxMessageBodyChunk))
	for _, part := range parts {
		if _, err := stream.digest.Write(part); err != nil {
			return ProviderEventCommit{}, err
		}
		stream.length += uint64(len(part))
		stream.chunkCount++
		stream.content.Write(part)
		stream.lastUpdated = p.clock.Now().UTC()
		totalHash := hex.EncodeToString(stream.digest.Sum(nil))
		chunk := MessageBodyChunkWrite{MessageID: stream.message.ID, Ordinal: stream.chunkCount, Bytes: append([]byte(nil), part...), Hash: digestBytes(part), TotalLength: stream.length, TotalSHA256: totalHash}
		var err error
		if p.persistence == PersistenceDurable {
			guard, err = p.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
				if streamTx, ok := tx.(MessageBodyStreamTx); ok {
					return streamTx.AppendMessageBodyChunk(ctx, chunk)
				}
				if err := stream.message.AppendContent(string(part), stream.lastUpdated); err != nil {
					return err
				}
				return tx.SaveMessage(ctx, stream.message)
			})
		}
		if err != nil {
			return ProviderEventCommit{}, err
		}
	}
	activity := ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, MessageID: stream.message.ID, ItemRef: event.ItemRef, Status: string(review.MessageStreaming)}
	p.publishActivity(activity)
	return ProviderEventCommit{Guard: guard, Activity: activity}, nil
}

func (p *ProviderEventProcessor) finishMessage(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent, status review.MessageStatus, phase review.FailurePhase, code string) (ProviderEventCommit, error) {
	key := providerMessageKey{ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: event.ItemRef}
	p.mu.Lock()
	stream := p.messages[key]
	p.mu.Unlock()
	if stream == nil || stream.terminal {
		return ProviderEventCommit{}, ErrProviderMessageOrder
	}
	now := p.clock.Now().UTC()
	var err error
	switch status {
	case review.MessageCompleted:
		err = stream.message.Complete(now)
	case review.MessageFailed:
		err = stream.message.Fail(phase, review.ErrorCode(code), now)
	case review.MessageCancelled:
		err = stream.message.Cancel(now)
	}
	if err != nil {
		return ProviderEventCommit{}, err
	}
	identity := MessageBodyIdentity{MessageID: stream.message.ID, ChunkCount: stream.chunkCount, ByteLength: stream.length, SHA256: hex.EncodeToString(stream.digest.Sum(nil)), TerminalStatus: status, FailurePhase: phase, ErrorCode: review.ErrorCode(code), CompletedAt: now}
	if p.persistence == PersistenceDurable {
		guard, err = p.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
			if streamTx, ok := tx.(MessageBodyStreamTx); ok {
				return streamTx.FinalizeMessageBody(ctx, identity)
			}
			stream.message.Content = stream.content.String()
			return tx.SaveMessage(ctx, stream.message)
		})
		if err != nil {
			return ProviderEventCommit{}, err
		}
	}
	stream.terminal = true
	p.mu.Lock()
	delete(p.messages, key)
	p.mu.Unlock()
	activity := ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, MessageID: stream.message.ID, ItemRef: event.ItemRef, Status: string(status)}
	p.publishActivity(activity)
	return ProviderEventCommit{Guard: guard, Activity: activity, Message: &stream.message, Identity: &identity}, nil
}

func (p *ProviderEventProcessor) finishTurnMessages(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent, status review.MessageStatus, phase review.FailurePhase, code string) (ProviderEventCommit, error) {
	var keys []providerMessageKey
	p.mu.Lock()
	for key := range p.messages {
		if key.ThreadID == event.ThreadID && key.TurnID == event.TurnID {
			keys = append(keys, key)
		}
	}
	p.mu.Unlock()
	for _, key := range keys {
		commit, err := p.finishMessage(ctx, guard, provider.ProviderEvent{Kind: provider.EventMessageCompleted, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemRef: key.ItemRef}, status, phase, code)
		if err != nil {
			return ProviderEventCommit{}, err
		}
		guard = commit.Guard
	}
	activity := ProviderActivity{EventKind: event.Kind, ThreadID: event.ThreadID, TurnID: event.TurnID, Status: string(status)}
	p.publishActivity(activity)
	return ProviderEventCommit{Guard: guard, Activity: activity}, nil
}

func (p *ProviderEventProcessor) finishDisconnected(ctx context.Context, guard SessionWriteGuard, event provider.ProviderEvent) (ProviderEventCommit, error) {
	var keys []providerMessageKey
	p.mu.Lock()
	for key := range p.messages {
		keys = append(keys, key)
	}
	p.mu.Unlock()
	for _, key := range keys {
		commit, err := p.finishMessage(ctx, guard, provider.ProviderEvent{Kind: provider.EventMessageCompleted, ThreadID: key.ThreadID, TurnID: key.TurnID, ItemRef: key.ItemRef}, review.MessageFailed, review.FailurePhaseProvider, "provider_disconnected")
		if err != nil {
			return ProviderEventCommit{}, err
		}
		guard = commit.Guard
	}
	activity := ProviderActivity{EventKind: event.Kind, Status: "disconnected"}
	p.publishActivity(activity)
	return ProviderEventCommit{Guard: guard, Activity: activity}, nil
}

func (p *ProviderEventProcessor) publishActivity(activity ProviderActivity) {
	if p == nil {
		return
	}
	select {
	case p.activity <- activity:
	default:
		// Activity is a projection; durable message and lifecycle truth are
		// handled before this bounded UI hint. A slow UI cannot block pipes.
	}
}

func (p *ProviderEventProcessor) nextMessageOrdinal(ctx context.Context, threadID domain.ReviewThreadID) (uint64, error) {
	if p.persistence == PersistenceNoPersist {
		var highest uint64
		p.mu.Lock()
		for key, stream := range p.messages {
			if key.ThreadID == threadID && stream.message.Ordinal > highest {
				highest = stream.message.Ordinal
			}
		}
		p.mu.Unlock()
		return highest + 1, nil
	}
	page := MessagePage{ThreadID: threadID, Limit: MaxPageLimit}
	var highest uint64
	for {
		result, err := p.store.ListMessages(ctx, threadID, page)
		if err != nil {
			return 0, err
		}
		for _, item := range result.Items {
			if item.Ordinal > highest {
				highest = item.Ordinal
			}
		}
		if !result.HasMore {
			return highest + 1, nil
		}
		if result.Next == nil {
			return 0, ErrReviewStoreCorrupt
		}
		page.Cursor = result.Next
	}
}

func splitMessageBytes(value []byte, limit int) [][]byte {
	if len(value) == 0 {
		return nil
	}
	var parts [][]byte
	for len(value) > 0 {
		end := len(value)
		if end > limit {
			end = limit
			for end > 0 && end < len(value) && !utf8.RuneStart(value[end]) {
				end--
			}
			if end == 0 {
				end = limit
			}
		}
		parts = append(parts, value[:end])
		value = value[end:]
	}
	return parts
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func boundedErrorCode(value, fallback string) string {
	if value == "" {
		return fallback
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 64 {
		return fallback
	}
	return value
}
