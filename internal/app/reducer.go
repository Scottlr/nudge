package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/provider"
)

// ReducerConfig supplies the owned clock and identity source used by a
// reducer. Nil dependencies use the production implementations.
type ReducerConfig struct {
	Clock   Clock
	IDs     IDSource
	Threads *ThreadService
}

// Reducer is the single mutable-state owner for the application runtime. It
// is intentionally not safe for concurrent use; Client serializes all calls.
type Reducer struct {
	state   State
	clock   Clock
	ids     IDSource
	threads *ThreadService
	closed  bool
}

// Commit is the reducer's complete externally visible result for one input.
// Events are ordered in the same order as the reducer applied them.
type Commit struct {
	Changed  bool
	Closed   bool
	Snapshot AppSnapshot
	Events   []Event
}

// ReducerResponse contains the operation admitted by a command and its commit.
type ReducerResponse struct {
	OperationID  domain.OperationID
	SessionGuard *SessionWriteGuard
	Commit       Commit
}

// NewReducer constructs an empty single-writer reducer.
func NewReducer(config ReducerConfig) *Reducer {
	clock := config.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	ids := config.IDs
	if ids == nil {
		ids = RandomIDSource{}
	}
	return &Reducer{state: NewState(), clock: clock, ids: ids, threads: config.Threads}
}

// State returns a detached copy for reducer-owned tests and composition code.
// Client frontends should use AppSnapshot instead.
func (r *Reducer) State() State {
	return r.state.clone()
}

// Snapshot returns a complete detached frontend projection.
func (r *Reducer) Snapshot() AppSnapshot {
	return snapshotFromState(r.state)
}

// Handle applies one sealed command or result. All canonical state mutation
// happens here, on the reducer owner, before one revisioned commit is returned.
func (r *Reducer) Handle(input ReducerInput) (ReducerResponse, error) {
	if input == nil {
		return ReducerResponse{}, ErrInvalidReducerInput
	}
	if r.closed {
		if _, ok := input.(Shutdown); !ok {
			return ReducerResponse{}, ErrReducerClosed
		}
		return ReducerResponse{}, ErrReducerClosed
	}

	switch value := input.(type) {
	case Command:
		return r.handleCommand(value)
	case Result:
		return r.handleResult(value)
	default:
		return ReducerResponse{}, ErrInvalidReducerInput
	}
}

// HandleResult makes the asynchronous result path explicit while retaining
// Reducer.Handle as the only mutation point.
func (r *Reducer) HandleResult(result Result) (ReducerResponse, error) {
	return r.Handle(result)
}

func (r *Reducer) handleCommand(command Command) (ReducerResponse, error) {
	switch value := command.(type) {
	case OpenRepository:
		if !safeText(value.Path) {
			return ReducerResponse{}, fmt.Errorf("%w: repository path", ErrInvalidReducerInput)
		}
		return r.startOperation(OperationOpenRepository, value.CorrelationID, 0, true)
	case SelectTarget:
		if r.state.Repository == nil {
			return ReducerResponse{}, ErrRepositoryNotLoaded
		}
		if err := value.Spec.Validate(); err != nil {
			return ReducerResponse{}, fmt.Errorf("%w: target: %v", ErrInvalidReducerInput, err)
		}
		return r.startOperation(OperationSelectTarget, value.CorrelationID, targetGeneration(r.state.Target), true)
	case RefreshTarget:
		if r.state.Repository == nil {
			return ReducerResponse{}, ErrRepositoryNotLoaded
		}
		if r.state.Target == nil {
			return ReducerResponse{}, ErrTargetNotLoaded
		}
		return r.startOperation(OperationRefreshTarget, value.CorrelationID, r.state.Target.Generation, true)
	case SelectFile:
		if r.state.Repository == nil {
			return ReducerResponse{}, ErrRepositoryNotLoaded
		}
		if r.state.Target == nil {
			return ReducerResponse{}, ErrTargetNotLoaded
		}
		if err := value.Path.Validate(); err != nil {
			return ReducerResponse{}, fmt.Errorf("%w: file path", ErrInvalidReducerInput)
		}
		operationID, correlationID, started, err := r.newOperation(OperationSelectFile, value.CorrelationID, r.state.Target.Generation, false)
		if err != nil {
			return ReducerResponse{}, err
		}
		path := repository.RepoPath(value.Path.Bytes())
		r.state.ActiveFile = &path
		operation := r.state.Operations[operationID]
		r.finishOperation(&operation, OperationStatusSucceeded, "", "")
		r.state.Operations[operationID] = operation
		return r.commit(operationID, started, FileSelected{
			OperationID:      operationID,
			CorrelationID:    correlationID,
			TargetGeneration: r.state.Target.Generation,
			Path:             path,
		}, OperationCompleted{
			OperationID:      operationID,
			CorrelationID:    correlationID,
			TargetGeneration: r.state.Target.Generation,
			Kind:             OperationSelectFile,
		}), nil
	case CancelOperation:
		return r.cancelOperation(value)
	case RespondToRuntimeApproval:
		if value.Response.RequestID == "" || value.Response.ThreadID == "" || value.Response.OperationID == "" || value.Response.CorrelationID.Validate() != nil || value.Response.TurnRef.Validate() != nil || (value.Response.Decision != provider.ApprovalAllowOnce && value.Response.Decision != provider.ApprovalDeny) || value.Response.Scope.Validate(provider.DefaultValidationLimits()) != nil {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.commit("", RuntimeApprovalDecisionRequested{RequestID: value.Response.RequestID, TurnRef: value.Response.TurnRef, Decision: value.Response.Decision, CorrelationID: value.CorrelationID}), nil
	case RequestProposal:
		if value.Guard.Validate() != nil || value.ThreadID == "" || value.ProposalID == "" || value.ConversationID == "" || value.OperationID == "" || value.CorrelationID == "" || value.Intent.Validate() != nil || value.Intent.ID != value.ProposalID || value.Intent.ThreadID != value.ThreadID || value.Context.Validate() != nil {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.startOperation(OperationRequestProposal, value.CorrelationID, value.Intent.ConfirmedAgainst.Generation, true)
	case RefreshProposal:
		if value.Guard.Validate() != nil || value.ThreadID == "" || value.ProposalID == "" || value.Version == 0 || value.ConversationID == "" || value.OperationID == "" || value.CorrelationID == "" || value.Intent.Validate() != nil || value.Intent.ID != value.ProposalID || value.Intent.ThreadID != value.ThreadID || value.Provenance.Validate() != nil || value.Provenance != value.Intent.ConfirmedAgainst || value.Context.Validate() != nil {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.startOperation(OperationRefreshProposal, value.CorrelationID, value.Provenance.Generation, true)
	case CancelProposal:
		if value.Guard.Validate() != nil || value.AttemptID == "" || value.CorrelationID == "" {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.cancelOperation(CancelOperation{OperationID: value.AttemptID, CorrelationID: value.CorrelationID})
	case RejectProposal:
		if value.Guard.Validate() != nil || value.ThreadID == "" || value.ProposalID == "" || value.Version == 0 || value.OperationID == "" || value.CorrelationID == "" || !safeOptionalText(value.Reason, 256) {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.startOperation(OperationRejectProposal, value.CorrelationID, 0, false)
	case ApproveProposal:
		if value.Guard.Validate() != nil || value.ThreadID == "" || value.ProposalID == "" || value.Version == 0 || !validSHA256(value.PatchSHA256) || value.ConfirmedReviewRevision == 0 || !validSHA256(value.ReviewCompletenessIdentity) || value.Destination.Validate() != nil || value.Repository.Validate() != nil || value.Worktree.Validate() != nil || value.Worktree.RepositoryID != value.Repository.ID || !safeText(value.IdempotencyKey) || value.OperationID == "" || value.CorrelationID == "" {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.startOperation(OperationApproveProposal, value.CorrelationID, 0, false)
	case DiscardProposalResult:
		if value.Guard.Validate() != nil || value.ProposalID == "" || value.AttemptID == "" || value.OperationID == "" || value.CorrelationID == "" || !safeOptionalText(value.Reason, 256) {
			return ReducerResponse{}, ErrInvalidReducerInput
		}
		return r.startOperation(OperationDiscardProposalResult, value.CorrelationID, 0, false)
	case Shutdown:
		return r.shutdown()
	case CreateThread:
		if r.threads == nil {
			return ReducerResponse{}, ErrThreadServiceUnavailable
		}
		commit, err := r.threads.CreateThread(context.Background(), value.Guard, value)
		if err != nil {
			return ReducerResponse{}, err
		}
		return r.commitThread(commit)
	case ActivateThread:
		if r.threads == nil {
			return ReducerResponse{}, ErrThreadServiceUnavailable
		}
		commit, err := r.threads.ActivateThread(context.Background(), value)
		if err != nil {
			return ReducerResponse{}, err
		}
		return r.commitThread(commit)
	case ReplyToThread:
		if r.threads == nil {
			return ReducerResponse{}, ErrThreadServiceUnavailable
		}
		commit, err := r.threads.ReplyToThread(context.Background(), value.Guard, value)
		if err != nil {
			return ReducerResponse{}, err
		}
		return r.commitThread(commit)
	case ResolveThread:
		if r.threads == nil {
			return ReducerResponse{}, ErrThreadServiceUnavailable
		}
		commit, err := r.threads.ResolveThread(context.Background(), value.Guard, value)
		if err != nil {
			return ReducerResponse{}, err
		}
		return r.commitThread(commit)
	case MarkThreadRead:
		if r.threads == nil {
			return ReducerResponse{}, ErrThreadServiceUnavailable
		}
		commit, err := r.threads.MarkThreadRead(context.Background(), value.Guard, value)
		if err != nil {
			return ReducerResponse{}, err
		}
		return r.commitThread(commit)
	default:
		return ReducerResponse{}, ErrInvalidReducerInput
	}
}

func (r *Reducer) commitThread(threadCommit ThreadCommit) (ReducerResponse, error) {
	if len(threadCommit.Events) == 0 {
		response := ReducerResponse{Commit: Commit{Snapshot: r.Snapshot()}}
		if threadCommit.Guard.Validate() == nil {
			guard := threadCommit.Guard
			response.SessionGuard = &guard
		}
		return response, nil
	}
	threadSummary := summarizeReviewThread(threadCommit.Thread)
	window := &r.state.ThreadWindow
	found := false
	for index := range window.Items {
		if window.Items[index].ID == threadSummary.ID {
			window.Items[index] = threadSummary
			found = true
			break
		}
	}
	if !found {
		window.TotalCount++
		if len(window.Items) < int(MaxPageLimit) {
			window.Items = append(window.Items, threadSummary)
		}
	}
	detailCount := uint64(len(threadCommit.Thread.Messages))
	if r.state.ActiveThread != nil && *r.state.ActiveThread == threadSummary.ID && r.state.ActiveThreadDetail != nil && r.state.ActiveThreadDetail.MessageCount > detailCount {
		detailCount = r.state.ActiveThreadDetail.MessageCount
	}
	detail := ThreadDetail{Summary: threadSummary, MessageCount: detailCount}
	if threadCommit.Message != nil && threadCommit.Message.Ordinal > detail.MessageCount {
		detail.MessageCount = threadCommit.Message.Ordinal
	}
	window.Revision = r.state.Revision + 1
	for _, event := range threadCommit.Events {
		switch value := event.(type) {
		case ThreadActivated:
			id := value.ThreadID
			r.state.ActiveThread = &id
			r.state.ActiveThreadDetail = &detail
		}
	}
	if r.state.ActiveThread != nil && *r.state.ActiveThread == threadSummary.ID {
		r.state.ActiveThreadDetail = &detail
	}
	response := r.commit("", threadCommit.Events...)
	if threadCommit.Guard.Validate() == nil {
		guard := threadCommit.Guard
		response.SessionGuard = &guard
	}
	return response, nil
}

func (r *Reducer) handleResult(result Result) (ReducerResponse, error) {
	switch value := result.(type) {
	case RepositoryLoaded:
		if err := value.Repository.Validate(); err != nil {
			return ReducerResponse{}, fmt.Errorf("%w: repository: %v", ErrInvalidReducerInput, err)
		}
		operation, err := r.acceptResult(value.resultMetadata(), OperationOpenRepository)
		if err != nil {
			return ReducerResponse{}, err
		}
		loaded := value
		loaded.Repository = *cloneRepositoryState(&value.Repository)
		r.state.Repository = cloneRepositoryState(&value.Repository)
		r.state.SessionID = nil
		r.state.Target = nil
		r.state.ActiveFile = nil
		r.state.ActiveThread = nil
		r.state.ActiveThreadDetail = nil
		r.state.ThreadWindow = ThreadWindow{}
		r.state.Tree = TreeProjection{}
		r.state.ChangedFiles = nil
		r.finishOperation(&operation, OperationStatusSucceeded, "", "")
		r.state.Operations[operation.ID] = operation
		return r.commit(operation.ID, loaded, OperationCompleted{
			OperationID:      operation.ID,
			CorrelationID:    operation.CorrelationID,
			TargetGeneration: operation.TargetGeneration,
			Kind:             operation.Kind,
		}), nil
	case TargetLoaded:
		if value.TargetGeneration == 0 || value.Target.Generation != value.TargetGeneration {
			return ReducerResponse{}, fmt.Errorf("%w: target generation", ErrInvalidReducerInput)
		}
		if err := value.Target.Validate(); err != nil {
			return ReducerResponse{}, fmt.Errorf("%w: target: %v", ErrInvalidReducerInput, err)
		}
		operation, err := r.acceptResult(value.resultMetadata(), OperationSelectTarget, OperationRefreshTarget)
		if err != nil {
			return ReducerResponse{}, err
		}
		loaded := value
		loaded.Target = *cloneResolvedTarget(&value.Target)
		target := value.Target
		r.state.Target = &target
		r.state.ActiveFile = nil
		r.finishOperation(&operation, OperationStatusSucceeded, "", "")
		r.state.Operations[operation.ID] = operation
		return r.commit(operation.ID, loaded, OperationCompleted{
			OperationID:      operation.ID,
			CorrelationID:    operation.CorrelationID,
			TargetGeneration: value.TargetGeneration,
			Kind:             operation.Kind,
		}), nil
	case OperationFailed:
		if value.Code == "" || !safeText(value.Message) {
			return ReducerResponse{}, fmt.Errorf("%w: failure event", ErrInvalidReducerInput)
		}
		operation, err := r.acceptResult(value.resultMetadata())
		if err != nil {
			return ReducerResponse{}, err
		}
		failed := value
		r.finishOperation(&operation, OperationStatusFailed, value.Code, value.Message)
		r.state.Operations[operation.ID] = operation
		return r.commit(operation.ID, failed), nil
	case OperationCancelled:
		if !safeText(value.Reason) {
			return ReducerResponse{}, fmt.Errorf("%w: cancellation reason", ErrInvalidReducerInput)
		}
		operation, err := r.acceptResult(value.resultMetadata())
		if err != nil {
			return ReducerResponse{}, err
		}
		cancelled := value
		r.finishOperation(&operation, OperationStatusCancelled, CodeCancelled, value.Reason)
		r.state.Operations[operation.ID] = operation
		return r.commit(operation.ID, cancelled), nil
	default:
		return ReducerResponse{}, ErrInvalidReducerInput
	}
}

func (r *Reducer) startOperation(kind OperationKind, correlationID CorrelationID, generation repository.TargetGeneration, cancellable bool) (ReducerResponse, error) {
	operationID, _, started, err := r.newOperation(kind, correlationID, generation, cancellable)
	if err != nil {
		return ReducerResponse{}, err
	}
	events := r.supersede(kind, operationID)
	events = append(events, started)
	return r.commit(operationID, events...), nil
}

func (r *Reducer) supersede(kind OperationKind, replacementID domain.OperationID) []Event {
	family := operationFamily(kind)
	ids := make([]domain.OperationID, 0)
	for id, operation := range r.state.Operations {
		if id != replacementID && operation.active() && operationFamily(operation.Kind) == family {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	events := make([]Event, 0, len(ids))
	for _, id := range ids {
		operation := r.state.Operations[id]
		r.finishOperation(&operation, OperationStatusCancelled, CodeCancelled, "operation superseded")
		r.state.Operations[id] = operation
		events = append(events, OperationCancelled{
			OperationID:      operation.ID,
			CorrelationID:    operation.CorrelationID,
			TargetGeneration: operation.TargetGeneration,
			Reason:           "operation superseded",
		})
	}
	return events
}

func operationFamily(kind OperationKind) OperationKind {
	if kind == OperationSelectTarget || kind == OperationRefreshTarget {
		return OperationRefreshTarget
	}
	return kind
}

func (r *Reducer) newOperation(kind OperationKind, correlationID CorrelationID, generation repository.TargetGeneration, cancellable bool) (domain.OperationID, CorrelationID, OperationStarted, error) {
	rawID := r.ids.NewID()
	operationID, err := domain.NewOperationID(rawID)
	if err != nil {
		return "", "", OperationStarted{}, fmt.Errorf("%w: operation id: %v", ErrInvalidReducerInput, err)
	}
	if _, exists := r.state.Operations[operationID]; exists {
		return "", "", OperationStarted{}, fmt.Errorf("%w: duplicate operation id", ErrInvalidReducerInput)
	}
	if correlationID == "" {
		correlationID = CorrelationID(operationID)
	}
	if !safeText(string(correlationID)) {
		return "", "", OperationStarted{}, ErrEmptyCorrelationID
	}
	now := r.clock.Now().UTC()
	operation := OperationState{
		ID:               operationID,
		Kind:             kind,
		Status:           OperationStatusRunning,
		CorrelationID:    correlationID,
		TargetGeneration: generation,
		StartedAt:        now,
		Cancellable:      cancellable,
	}
	r.state.Operations[operationID] = operation
	return operationID, correlationID, OperationStarted{
		OperationID:      operationID,
		CorrelationID:    correlationID,
		TargetGeneration: generation,
		Kind:             kind,
	}, nil
}

func (r *Reducer) cancelOperation(command CancelOperation) (ReducerResponse, error) {
	operation, exists := r.state.Operations[command.OperationID]
	if !exists {
		return ReducerResponse{}, ErrOperationNotFound
	}
	if !operation.active() {
		return ReducerResponse{OperationID: operation.ID}, nil
	}
	if !operation.Cancellable {
		return ReducerResponse{}, ErrOperationNotCancellable
	}
	r.finishOperation(&operation, OperationStatusCancelled, CodeCancelled, "operation cancelled")
	r.state.Operations[operation.ID] = operation
	return r.commit(operation.ID, OperationCancelled{
		OperationID:      operation.ID,
		CorrelationID:    operation.CorrelationID,
		TargetGeneration: operation.TargetGeneration,
		Reason:           "operation cancelled",
	}), nil
}

func (r *Reducer) shutdown() (ReducerResponse, error) {
	ids := make([]domain.OperationID, 0, len(r.state.Operations))
	for id, operation := range r.state.Operations {
		if operation.active() {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	events := make([]Event, 0, len(ids)+1)
	for _, id := range ids {
		operation := r.state.Operations[id]
		r.finishOperation(&operation, OperationStatusCancelled, CodeCancelled, "application shutdown")
		r.state.Operations[id] = operation
		events = append(events, OperationCancelled{
			OperationID:      operation.ID,
			CorrelationID:    operation.CorrelationID,
			TargetGeneration: operation.TargetGeneration,
			Reason:           "application shutdown",
		})
	}
	r.closed = true
	response := r.commit("", append(events, ApplicationClosed{})...)
	response.Commit.Closed = true
	return response, nil
}

func (r *Reducer) acceptResult(metadata ResultMetadata, allowedKinds ...OperationKind) (OperationState, error) {
	if metadata.OperationID == "" || !safeText(string(metadata.CorrelationID)) {
		return OperationState{}, ErrResultDiscarded
	}
	operation, exists := r.state.Operations[metadata.OperationID]
	if !exists || !operation.active() || operation.CorrelationID != metadata.CorrelationID {
		return OperationState{}, ErrResultDiscarded
	}
	if len(allowedKinds) > 0 {
		allowed := false
		for _, kind := range allowedKinds {
			if operation.Kind == kind {
				allowed = true
				break
			}
		}
		if !allowed {
			return OperationState{}, ErrResultDiscarded
		}
	}
	if operation.TargetGeneration != 0 && metadata.TargetGeneration < operation.TargetGeneration {
		return OperationState{}, ErrResultDiscarded
	}
	if r.state.Target != nil && metadata.TargetGeneration != 0 && metadata.TargetGeneration < r.state.Target.Generation {
		return OperationState{}, ErrResultDiscarded
	}
	return operation, nil
}

func (r *Reducer) finishOperation(operation *OperationState, status OperationStatus, code ErrorCode, message string) {
	operation.Status = status
	operation.Cancellable = false
	operation.FinishedAt = r.clock.Now().UTC()
	operation.ErrorCode = code
	operation.Message = message
}

func (r *Reducer) commit(operationID domain.OperationID, events ...Event) ReducerResponse {
	r.state.Revision++
	revision := r.state.Revision
	committedEvents := make([]Event, len(events))
	for i, event := range events {
		committedEvents[i] = event.withRevision(revision)
	}
	return ReducerResponse{
		OperationID: operationID,
		Commit: Commit{
			Changed:  true,
			Closed:   r.closed,
			Snapshot: snapshotFromState(r.state),
			Events:   committedEvents,
		},
	}
}

func safeText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeOptionalText(value string, maxBytes int) bool {
	if value == "" {
		return true
	}
	if len([]byte(value)) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}
