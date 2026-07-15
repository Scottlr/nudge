package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	// ErrInvalidReconciliationRequest reports a refresh that is not bound to
	// one repository, worktree, and fenced session snapshot.
	ErrInvalidReconciliationRequest = errors.New("invalid reconciliation request")
	// ErrReconciliationClosed reports a submission after shutdown.
	ErrReconciliationClosed = errors.New("reconciliation coordinator is closed")
	// ErrReconciliationAlreadyConsumed reports a second request-stream owner.
	ErrReconciliationAlreadyConsumed = errors.New("reconciliation request stream already consumed")
)

// ReconciliationFailureCode is a bounded failure identity safe to publish to
// the UI and operational log. Causes remain available through errors returned
// by synchronous ports and are never copied into canonical state.
type ReconciliationFailureCode string

const (
	ReconciliationFailureCapture   ReconciliationFailureCode = "capture_failed"
	ReconciliationFailureAdoption  ReconciliationFailureCode = "adoption_failed"
	ReconciliationFailureLease     ReconciliationFailureCode = "lease_lost"
	ReconciliationFailureJournal   ReconciliationFailureCode = "journal_failed"
	ReconciliationFailureAnchors   ReconciliationFailureCode = "anchor_reconciliation_failed"
	ReconciliationFailureTruthLost ReconciliationFailureCode = "authoritative_truth_lost"
	ReconciliationFailureCancelled ReconciliationFailureCode = "cancelled"
	ReconciliationFailureStale     ReconciliationFailureCode = "superseded"
)

// ReconciliationEventKind identifies one bounded lifecycle publication.
type ReconciliationEventKind string

const (
	ReconciliationEventStarted    ReconciliationEventKind = "started"
	ReconciliationEventCapturing  ReconciliationEventKind = "capturing"
	ReconciliationEventStaging    ReconciliationEventKind = "staging"
	ReconciliationEventCommitting ReconciliationEventKind = "committing"
	ReconciliationEventFresh      ReconciliationEventKind = "fresh"
	ReconciliationEventCompleted  ReconciliationEventKind = "completed"
	ReconciliationEventStale      ReconciliationEventKind = "stale"
	ReconciliationEventCancelled  ReconciliationEventKind = "cancelled"
	ReconciliationEventFailed     ReconciliationEventKind = "failed"
)

// ReconciliationEvent is the actor-facing projection of one authoritative
// refresh. It is deliberately identity-and-progress only; anchors and source
// content remain in the immutable store and staged journal.
type ReconciliationEvent struct {
	Kind        ReconciliationEventKind
	OperationID domain.OperationID
	SessionID   domain.ReviewSessionID
	Generation  repository.TargetGeneration
	Phase       ReconciliationPhase
	Processed   uint64
	Total       uint64
	Stale       bool
	FollowUp    bool
	ErrorCode   ReconciliationFailureCode
}

// ReconciliationAnchorInput is one complete immutable input supplied by the
// anchor projection. It must be sourced from accepted captures, never from a
// live worktree read.
type ReconciliationAnchorInput struct {
	ThreadID        domain.ReviewThreadID
	Anchor          review.CodeAnchor
	NewContent      review.CapturedFile
	PreviousContent *review.CapturedFile
	SourceBytes     uint64
	EvidenceBytes   uint64
}

func (i ReconciliationAnchorInput) validate() error {
	if i.ThreadID == "" || i.Anchor.Validate() != nil || i.NewContent.Validate() != nil {
		return ErrInvalidReconciliationRequest
	}
	if i.PreviousContent != nil && i.PreviousContent.Validate() != nil {
		return ErrInvalidReconciliationRequest
	}
	return nil
}

// ReconciliationPageRequest is a stable keyset request for one bounded
// anchor batch.
type ReconciliationPageRequest struct {
	SessionID      domain.ReviewSessionID
	OperationID    domain.OperationID
	FromGeneration repository.TargetGeneration
	ToGeneration   repository.TargetGeneration
	CaptureID      domain.CaptureID
	Cursor         string
	Limit          Count
}

// ReconciliationAnchorPage is a complete bounded page. A non-final page must
// advance the opaque cursor; a final page has no cursor.
type ReconciliationAnchorPage struct {
	Items      []ReconciliationAnchorInput
	NextCursor string
	TotalCount uint64
	Done       bool
}

// ReconciliationAnchorSource pages persisted anchors and capture-owned
// content. The source owns stable-keyset lookup; the coordinator owns batch
// bounds and lifecycle.
type ReconciliationAnchorSource interface {
	Page(context.Context, ReconciliationPageRequest) (ReconciliationAnchorPage, error)
}

func (p ReconciliationAnchorPage) validate(policy ResourcePolicy) error {
	if len(p.Items) > int(policy.Batch.AnchorCount) || p.TotalCount != 0 && p.TotalCount < uint64(len(p.Items)) {
		return ErrInvalidReconciliationRequest
	}
	if p.Done && p.NextCursor != "" || !p.Done && len(p.Items) == 0 && p.NextCursor == "" {
		return ErrInvalidReconciliationRequest
	}
	if len(p.NextCursor) > 4<<10 {
		return ErrInvalidReconciliationRequest
	}
	for _, item := range p.Items {
		if err := item.validate(); err != nil {
			return err
		}
	}
	return nil
}

// ReconciliationResume identifies a compatible staged operation and its
// accepted, not-yet-active generation after a process restart.
type ReconciliationResume struct {
	Operation  ReconciliationOperation
	Generation CaptureGeneration
}

func (r ReconciliationResume) validate(sessionID domain.ReviewSessionID) error {
	if r.Operation.SessionID != sessionID || r.Operation.ID == "" || r.Operation.State == ReconciliationCompleted || r.Operation.State == ReconciliationFailed || r.Operation.CaptureID != r.Generation.CaptureID || r.Operation.ManifestHash != r.Generation.ManifestHash || r.Operation.ToGeneration != r.Generation.Generation || r.Generation.Validate() != nil || r.Operation.Progress.Validate() != nil || r.Operation.Progress.Phase != ReconciliationPhaseStaging {
		return ErrInvalidReconciliationRequest
	}
	return nil
}

// ReconciliationRequest binds one T107 request to the current actor-owned
// session snapshot. Transition evidence is optional for unchanged paths, but
// when present it must be complete and is never inferred from live bytes.
type ReconciliationRequest struct {
	Refresh      RefreshRequest
	Repository   repository.Repository
	Worktree     repository.WorktreeRef
	Guard        SessionWriteGuard
	CaptureState CaptureSessionState
	Target       repository.ResolvedTarget
	Transition   review.GenerationTransition
	Resume       *ReconciliationResume
}

func (r ReconciliationRequest) Validate() error {
	if r.Refresh.Validate() != nil || r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.Refresh.WatchedSet.RepositoryID != r.Repository.ID || r.Refresh.WatchedSet.WorktreeID != r.Worktree.ID || r.Guard.Validate() != nil || r.CaptureState.Validate() != nil || r.CaptureState.Guard.SessionID != r.Guard.SessionID || r.CaptureState.Guard.LeaseID != r.Guard.LeaseID || r.CaptureState.Guard.WriterEpoch != r.Guard.WriterEpoch || r.CaptureState.Guard.Revision != r.Guard.ExpectedRevision || r.Target.Validate() != nil {
		return ErrInvalidReconciliationRequest
	}
	if r.CaptureState.RepositoryID != r.Repository.ID || r.CaptureState.WorktreeID != r.Worktree.ID {
		return ErrInvalidReconciliationRequest
	}
	if r.CaptureState.Current != nil && r.Target.Generation != r.CaptureState.Current.Generation {
		return ErrInvalidReconciliationRequest
	}
	if r.Resume != nil {
		if err := r.Resume.validate(r.Guard.SessionID); err != nil {
			return err
		}
		if r.CaptureState.Current == nil || r.Resume.Operation.FromGeneration != r.CaptureState.Current.Generation {
			return ErrInvalidReconciliationRequest
		}
	}
	return nil
}

// ReconciliationJournal is the application-owned operation journal. Each
// method is one short fenced session transaction; filesystem and Git work is
// performed outside the transaction.
type ReconciliationJournal interface {
	Start(context.Context, SessionWriteGuard, ReconciliationOperation, bool) (SessionWriteGuard, error)
	Stage(context.Context, SessionWriteGuard, ReconciliationOperation, []ReconciliationAnchorResult) (SessionWriteGuard, error)
	CompleteAndActivate(context.Context, SessionWriteGuard, ReconciliationOperation, time.Time) (SessionWriteGuard, error)
}

// ReviewStoreReconciliationJournal adapts the core ReviewStore transaction to
// the reconciliation lifecycle without exposing SQL rows to the coordinator.
type ReviewStoreReconciliationJournal struct {
	Store ReviewStore
}

func (j ReviewStoreReconciliationJournal) Start(ctx context.Context, guard SessionWriteGuard, operation ReconciliationOperation, resume bool) (SessionWriteGuard, error) {
	if j.Store == nil {
		return guard, ErrReviewStoreInput
	}
	return j.Store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		if resume {
			return tx.UpdateReconciliation(ctx, operation)
		}
		return tx.CreateReconciliation(ctx, operation)
	})
}

func (j ReviewStoreReconciliationJournal) Stage(ctx context.Context, guard SessionWriteGuard, operation ReconciliationOperation, results []ReconciliationAnchorResult) (SessionWriteGuard, error) {
	if j.Store == nil {
		return guard, ErrReviewStoreInput
	}
	return j.Store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		for _, result := range results {
			if err := tx.StageReconciliationResult(ctx, result); err != nil {
				return err
			}
		}
		return tx.UpdateReconciliation(ctx, operation)
	})
}

func (j ReviewStoreReconciliationJournal) CompleteAndActivate(ctx context.Context, guard SessionWriteGuard, operation ReconciliationOperation, completedAt time.Time) (SessionWriteGuard, error) {
	if j.Store == nil {
		return guard, ErrReviewStoreInput
	}
	return j.Store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		if err := tx.UpdateReconciliation(ctx, operation); err != nil {
			return err
		}
		if err := tx.CompleteReconciliation(ctx, operation.ID, completedAt); err != nil {
			return err
		}
		return tx.ActivateReconciliation(ctx, operation.ID)
	})
}

// AuthoritativeReconcilerConfig composes the T106/T009/T023 and journal
// boundaries. Policy limits apply to every page and staged result batch.
type AuthoritativeReconcilerConfig struct {
	Capture LocalCaptureSource
	Store   LocalCaptureStore
	Journal ReconciliationJournal
	Anchors ReconciliationAnchorSource
	IDs     IDSource
	Clock   Clock
	Policy  ResourcePolicy
}

// AuthoritativeReconciler owns one cancellable compute operation and one
// follow-up. It never mutates canonical UI state directly; consumers apply
// its bounded events through the application actor.
type AuthoritativeReconciler struct {
	capture LocalCaptureSource
	store   LocalCaptureStore
	journal ReconciliationJournal
	anchors ReconciliationAnchorSource
	ids     IDSource
	clock   Clock
	policy  ResourcePolicy

	mu           sync.Mutex
	active       *reconciliationWork
	followUp     *queuedReconciliation
	closed       bool
	consuming    bool
	events       chan ReconciliationEvent
	eventsClosed bool
}

type queuedReconciliation struct {
	request ReconciliationRequest
	parent  context.Context
}

type reconciliationWork struct {
	request     ReconciliationRequest
	operationID domain.OperationID
	ctx         context.Context
	cancel      context.CancelFunc
	phase       ReconciliationPhase
	generation  repository.TargetGeneration
	followUp    bool
}

// NewAuthoritativeReconciler validates the bounded composition root.
func NewAuthoritativeReconciler(config AuthoritativeReconcilerConfig) (*AuthoritativeReconciler, error) {
	if config.Capture == nil || config.Store == nil || config.Journal == nil || config.Anchors == nil {
		return nil, ErrInvalidReconciliationRequest
	}
	if config.IDs == nil {
		config.IDs = RandomIDSource{}
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	if config.Policy == (ResourcePolicy{}) {
		config.Policy = DefaultResourcePolicy()
	}
	if config.Policy.Validate() != nil {
		return nil, ErrInvalidReconciliationRequest
	}
	return &AuthoritativeReconciler{
		capture: config.Capture, store: config.Store, journal: config.Journal, anchors: config.Anchors,
		ids: config.IDs, clock: config.Clock, policy: config.Policy, events: make(chan ReconciliationEvent, 32),
	}, nil
}

// Events returns the bounded lifecycle stream. A consumer should continue
// draining it until Close returns so progress backpressure is explicit.
func (r *AuthoritativeReconciler) Events() <-chan ReconciliationEvent {
	if r == nil {
		return nil
	}
	return r.events
}

// Submit admits a T107-bound request. Pre-commit work is cancelled and
// replaced by the newest request. Once commit begins, exactly one follow-up
// is retained.
func (r *AuthoritativeReconciler) Submit(ctx context.Context, request ReconciliationRequest) error {
	if r == nil || ctx == nil || request.Validate() != nil {
		return ErrInvalidReconciliationRequest
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrReconciliationClosed
	}
	operationID := domain.OperationID(request.ResumeID())
	if operationID == "" {
		value, err := domain.NewOperationID(r.ids.NewID())
		if err != nil {
			return fmt.Errorf("reconciliation operation ID: %w", err)
		}
		operationID = value
	}
	if r.active != nil {
		if r.active.phase == ReconciliationPhaseCommitting {
			r.followUp = &queuedReconciliation{request: request, parent: ctx}
			return nil
		}
		r.active.cancel()
		r.followUp = &queuedReconciliation{request: request, parent: ctx}
		return nil
	}
	r.launchLocked(request, operationID, ctx, false)
	return nil
}

// Consume forwards one T107 request stream to Submit and gives it one owner.
func (r *AuthoritativeReconciler) Consume(ctx context.Context, requests <-chan ReconciliationRequest) error {
	if r == nil || ctx == nil || requests == nil {
		return ErrInvalidReconciliationRequest
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrReconciliationClosed
	}
	if r.consuming {
		r.mu.Unlock()
		return ErrReconciliationAlreadyConsumed
	}
	r.consuming = true
	r.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case request, ok := <-requests:
			if !ok {
				return nil
			}
			if err := r.Submit(ctx, request); err != nil {
				return err
			}
		}
	}
}

// Close cancels active compute, drops any unstarted follow-up, and closes the
// event stream after the active operation has reached a terminal point.
func (r *AuthoritativeReconciler) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.followUp = nil
	if r.active == nil {
		r.closeEventsLocked()
	} else {
		r.active.cancel()
	}
	r.mu.Unlock()
	return nil
}

func (r *AuthoritativeReconciler) launchLocked(request ReconciliationRequest, operationID domain.OperationID, parent context.Context, followUp bool) {
	ctx, cancel := context.WithCancel(parent)
	work := &reconciliationWork{request: request, operationID: operationID, ctx: ctx, cancel: cancel, phase: ReconciliationPhaseCapture, followUp: followUp}
	r.active = work
	go r.run(work)
}

func (r *AuthoritativeReconciler) run(work *reconciliationWork) {
	defer r.finish(work)
	r.emit(work.ctx, ReconciliationEvent{Kind: ReconciliationEventStarted, OperationID: work.operationID, SessionID: work.request.Guard.SessionID, Phase: ReconciliationPhaseCapture})

	if work.request.Resume == nil {
		r.emit(work.ctx, ReconciliationEvent{Kind: ReconciliationEventCapturing, OperationID: work.operationID, SessionID: work.request.Guard.SessionID, Phase: ReconciliationPhaseCapture})
	}

	generation, reused, err := r.acceptCapture(work)
	if err != nil {
		r.failOrCancel(work, ReconciliationFailureCapture, true, err)
		return
	}
	work.generation = generation.Generation
	if reused {
		r.terminal(work, ReconciliationEventFresh, "", false)
		return
	}
	if work.request.CaptureState.Current == nil {
		r.terminal(work, ReconciliationEventFresh, "", false)
		return
	}

	from := *work.request.CaptureState.Current
	transition, err := reconciliationTransition(work.request, from, generation)
	if err != nil {
		r.failOrCancel(work, ReconciliationFailureTruthLost, true, err)
		return
	}

	progress := ReconciliationProgress{Phase: ReconciliationPhaseStaging}
	resume := false
	if work.request.Resume != nil {
		progress = work.request.Resume.Operation.Progress
		resume = true
	}
	operation := ReconciliationOperation{
		ID: work.operationID, SessionID: work.request.Guard.SessionID, FromGeneration: from.Generation,
		ToGeneration: generation.Generation, CaptureID: generation.CaptureID, ManifestHash: generation.ManifestHash,
		State: ReconciliationStaged, Progress: progress, StartedAt: r.clock.Now().UTC(),
	}
	if resume {
		operation.StartedAt = work.request.Resume.Operation.StartedAt
		if operation.ID != work.request.Resume.Operation.ID || operation.ManifestHash != work.request.Resume.Operation.ManifestHash || operation.ToGeneration != work.request.Resume.Operation.ToGeneration {
			r.terminal(work, ReconciliationEventFailed, ReconciliationFailureStale, true)
			return
		}
	}
	guard, err := r.journal.Start(work.ctx, work.request.Guard, operation, resume)
	if err != nil {
		r.failOrCancel(work, reconciliationFailureCode(err, ReconciliationFailureJournal), true, err)
		return
	}
	work.request.Guard = guard

	cursor := progress.Cursor
	for {
		if err := work.ctx.Err(); err != nil {
			r.failOrCancel(work, ReconciliationFailureCancelled, true, err)
			return
		}
		page, err := r.anchors.Page(work.ctx, ReconciliationPageRequest{SessionID: operation.SessionID, OperationID: operation.ID, FromGeneration: operation.FromGeneration, ToGeneration: operation.ToGeneration, CaptureID: operation.CaptureID, Cursor: cursor, Limit: r.policy.Batch.AnchorCount})
		if err != nil || page.validate(r.policy) != nil {
			r.failOrCancel(work, ReconciliationFailureAnchors, true, err)
			return
		}
		if !page.Done && page.NextCursor == cursor {
			r.terminal(work, ReconciliationEventFailed, ReconciliationFailureAnchors, true)
			return
		}
		if progress.TotalAnchors == 0 && page.TotalCount != 0 {
			progress.TotalAnchors = page.TotalCount
		} else if page.TotalCount != 0 && progress.TotalAnchors != page.TotalCount {
			r.terminal(work, ReconciliationEventFailed, ReconciliationFailureAnchors, true)
			return
		}
		results := make([]ReconciliationAnchorResult, 0, len(page.Items))
		pathCount, sourceBytes, evidenceBytes, err := reconciliationPageBudget(page, r.policy)
		if err != nil {
			r.failOrCancel(work, ReconciliationFailureAnchors, true, err)
			return
		}
		for _, item := range page.Items {
			outcome, reconcileErr := ReconcileAnchor(review.ReconcileInput{Anchor: item.Anchor, Transition: transition, NewContent: item.NewContent, PreviousContent: item.PreviousContent, Now: r.clock.Now().UTC()})
			if reconcileErr != nil {
				r.terminal(work, ReconciliationEventFailed, ReconciliationFailureAnchors, true)
				return
			}
			results = append(results, ReconciliationAnchorResult{OperationID: operation.ID, ThreadID: item.ThreadID, Anchor: outcome.Anchor, State: outcome.State, Reason: outcome.Reason, ReportID: operation.ID, Candidates: outcome.Candidates, CandidateOverflow: outcome.CandidateOverflow, AlgorithmVersion: outcome.AlgorithmVersion})
		}
		if wouldOverflow(progress.ProcessedAnchors, uint64(len(results))) || wouldOverflow(progress.ProcessedPaths, pathCount) || wouldOverflow(progress.SourceBytes, sourceBytes) || wouldOverflow(progress.EvidenceBytes, evidenceBytes) {
			r.terminal(work, ReconciliationEventFailed, ReconciliationFailureAnchors, true)
			return
		}
		progress.ProcessedAnchors += uint64(len(results))
		progress.ProcessedPaths += pathCount
		progress.SourceBytes += sourceBytes
		progress.EvidenceBytes += evidenceBytes
		if page.Done {
			cursor = ""
		} else {
			cursor = page.NextCursor
		}
		progress.Cursor = cursor
		operation.Progress = progress
		guard, err = r.journal.Stage(work.ctx, guard, operation, results)
		if err != nil {
			r.failOrCancel(work, reconciliationFailureCode(err, ReconciliationFailureJournal), true, err)
			return
		}
		work.request.Guard = guard
		r.emit(work.ctx, ReconciliationEvent{Kind: ReconciliationEventStaging, OperationID: operation.ID, SessionID: operation.SessionID, Generation: operation.ToGeneration, Phase: progress.Phase, Processed: progress.ProcessedAnchors, Total: progress.TotalAnchors})
		if page.Done {
			break
		}
	}
	if progress.TotalAnchors == 0 {
		progress.TotalAnchors = progress.ProcessedAnchors
	}
	if progress.ProcessedAnchors != progress.TotalAnchors {
		r.terminal(work, ReconciliationEventFailed, ReconciliationFailureAnchors, true)
		return
	}
	if !r.setPhase(work, ReconciliationPhaseCommitting) {
		return
	}
	progress.Phase = ReconciliationPhaseCommitting
	progress.Cursor = ""
	operation.Progress = progress
	r.emit(work.ctx, ReconciliationEvent{Kind: ReconciliationEventCommitting, OperationID: operation.ID, SessionID: operation.SessionID, Generation: operation.ToGeneration, Phase: progress.Phase, Processed: progress.ProcessedAnchors, Total: progress.TotalAnchors})
	guard, err = r.journal.CompleteAndActivate(work.ctx, guard, operation, r.clock.Now().UTC())
	if err != nil {
		r.failOrCancel(work, reconciliationFailureCode(err, ReconciliationFailureJournal), true, err)
		return
	}
	work.request.Guard = guard
	r.terminal(work, ReconciliationEventCompleted, "", false)
}

func (r *AuthoritativeReconciler) acceptCapture(work *reconciliationWork) (CaptureGeneration, bool, error) {
	if work.request.Resume != nil {
		generation := work.request.Resume.Generation
		if generation.Validate() != nil || generation.Generation != work.request.Resume.Operation.ToGeneration {
			return CaptureGeneration{}, false, ErrInvalidReconciliationRequest
		}
		return generation, false, nil
	}
	artifacts, err := r.capture.Capture(work.ctx, work.request.Repository, work.request.Worktree)
	if err != nil {
		return CaptureGeneration{}, false, err
	}
	adopted := false
	defer func() {
		if !adopted {
			_ = artifacts.Abort(context.Background())
		}
	}()
	state := work.request.CaptureState
	state.Guard = CaptureSessionGuard{SessionID: work.request.Guard.SessionID, LeaseID: work.request.Guard.LeaseID, WriterEpoch: work.request.Guard.WriterEpoch, Revision: work.request.Guard.ExpectedRevision}
	result, err := r.store.Adopt(work.ctx, artifacts, state)
	if err != nil {
		return CaptureGeneration{}, false, err
	}
	adopted = true
	if result.Generation.Validate() != nil {
		return CaptureGeneration{}, false, ErrCaptureCorrupt
	}
	return result.Generation, result.Reused, nil
}

func reconciliationTransition(request ReconciliationRequest, from, to CaptureGeneration) (review.GenerationTransition, error) {
	transition := request.Transition
	if transition.FromCaptureID == "" {
		transition.FromCaptureID = from.CaptureID
	}
	if transition.ToCaptureID == "" {
		transition.ToCaptureID = to.CaptureID
	}
	if transition.FromGeneration == 0 {
		transition.FromGeneration = from.Generation
	}
	if transition.ToGeneration == 0 {
		transition.ToGeneration = to.Generation
	}
	if transition.NewBase.Kind == "" {
		kind := repository.SnapshotCommit
		if to.Base.Unborn {
			kind = repository.SnapshotTree
		}
		transition.NewBase = repository.SnapshotRef{Kind: kind, ObjectID: to.Base.ObjectID}
	}
	if transition.NewHead.Kind == "" {
		transition.NewHead = repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: to.WorktreeID, Fingerprint: to.Fingerprint}
	}
	if transition.FromGeneration != from.Generation || transition.ToGeneration != to.Generation {
		return review.GenerationTransition{}, ErrInvalidReconciliationRequest
	}
	if err := transition.Validate(); err != nil {
		return review.GenerationTransition{}, err
	}
	return transition, nil
}

func reconciliationPageBudget(page ReconciliationAnchorPage, policy ResourcePolicy) (uint64, uint64, uint64, error) {
	paths := make(map[string]struct{}, len(page.Items)*2)
	var sourceBytes, evidenceBytes uint64
	for _, item := range page.Items {
		paths[string(item.Anchor.Path)] = struct{}{}
		paths[string(item.NewContent.Path)] = struct{}{}
		if item.SourceBytes == 0 {
			item.SourceBytes = capturedFileBytes(item.NewContent)
		}
		if item.EvidenceBytes == 0 {
			item.EvidenceBytes = uint64(len(item.Anchor.SelectedText) + len(item.Anchor.SelectionHash) + len(item.Anchor.BeforeContextHash) + len(item.Anchor.AfterContextHash) + len(item.Anchor.Path))
		}
		if wouldOverflow(sourceBytes, item.SourceBytes) || wouldOverflow(evidenceBytes, item.EvidenceBytes) {
			return 0, 0, 0, ErrInvalidReconciliationRequest
		}
		sourceBytes += item.SourceBytes
		evidenceBytes += item.EvidenceBytes
	}
	if Count(len(paths)) > policy.Batch.AnchorPaths || ByteSize(sourceBytes) > policy.Batch.AnchorSourceBytes || ByteSize(evidenceBytes) > policy.Batch.AnchorEvidenceBytes {
		return 0, 0, 0, ErrInvalidReconciliationRequest
	}
	return uint64(len(paths)), sourceBytes, evidenceBytes, nil
}

func capturedFileBytes(file review.CapturedFile) uint64 {
	var total uint64
	for _, line := range file.Lines {
		total += uint64(len(line)) + 1
	}
	return total
}

func wouldOverflow(value, add uint64) bool { return add > math.MaxUint64-value }

func (r *AuthoritativeReconciler) setPhase(work *reconciliationWork, phase ReconciliationPhase) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != work || r.closed {
		return false
	}
	work.phase = phase
	return true
}

func (r *AuthoritativeReconciler) terminal(work *reconciliationWork, kind ReconciliationEventKind, code ReconciliationFailureCode, stale bool) {
	generation := work.request.Target.Generation
	if work.generation != 0 {
		generation = work.generation
	}
	emitContext := work.ctx
	if kind == ReconciliationEventCancelled {
		emitContext = context.Background()
	}
	r.emit(emitContext, ReconciliationEvent{Kind: kind, OperationID: work.operationID, SessionID: work.request.Guard.SessionID, Generation: generation, Phase: work.phase, Stale: stale, FollowUp: work.followUp, ErrorCode: code})
}

func (r *AuthoritativeReconciler) failOrCancel(work *reconciliationWork, code ReconciliationFailureCode, stale bool, cause error) {
	if errors.Is(cause, context.Canceled) || work.ctx.Err() != nil {
		r.terminal(work, ReconciliationEventCancelled, ReconciliationFailureCancelled, stale)
		return
	}
	r.terminal(work, ReconciliationEventFailed, code, stale)
}

func (r *AuthoritativeReconciler) emit(ctx context.Context, event ReconciliationEvent) {
	select {
	case r.events <- event:
	case <-ctx.Done():
	}
}

func (r *AuthoritativeReconciler) finish(work *reconciliationWork) {
	work.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != work {
		return
	}
	if r.followUp != nil && !r.closed {
		queued := r.followUp
		r.followUp = nil
		value, err := domain.NewOperationID(r.ids.NewID())
		if queued.request.Resume != nil {
			value = queued.request.Resume.Operation.ID
		}
		if err == nil || queued.request.Resume != nil {
			r.launchLocked(queued.request, value, queued.parent, true)
			return
		}
	}
	r.active = nil
	if r.closed {
		r.closeEventsLocked()
	}
}

func (r *AuthoritativeReconciler) closeEventsLocked() {
	if !r.eventsClosed {
		close(r.events)
		r.eventsClosed = true
	}
}

func reconciliationFailureCode(err error, fallback ReconciliationFailureCode) ReconciliationFailureCode {
	if errors.Is(err, ErrSessionLeaseLost) || errors.Is(err, ErrSessionRevisionConflict) {
		return ReconciliationFailureLease
	}
	return fallback
}

// ResumeID returns the durable operation identity when the request resumes a
// staged operation. It is kept as a small helper so admission does not infer
// IDs from arbitrary request fields.
func (r ReconciliationRequest) ResumeID() string {
	if r.Resume == nil {
		return ""
	}
	return string(r.Resume.Operation.ID)
}
