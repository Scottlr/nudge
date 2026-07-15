package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/provider"
)

var (
	ErrProposalTurnUnavailable      = errors.New("proposal turn unavailable")
	ErrProposalTurnConflict         = errors.New("proposal turn already active")
	ErrProposalVersionReady         = errors.New("proposal version already ready")
	ErrProposalTurnRecoveryRequired = errors.New("proposal turn requires recovery")
	ErrProposalTurnNotFound         = errors.New("proposal turn not found")
	ErrProposalTurnQuiescence       = errors.New("proposal workspace quiescence is unproven")
	ErrProposalTurnGrowth           = errors.New("proposal workspace growth limit exceeded")
	ErrProposalRuntimeApproval      = errors.New("proposal runtime approval denied")
)

const (
	proposalTurnPreparedReason = "proposal turn prepared"
	proposalTurnFailedReason   = "proposal turn failed"
	proposalTurnCancelled      = "proposal turn cancelled"
	proposalTurnDisconnected   = "proposal provider disconnected"
	proposalTurnQuiescenceCode = "workspace_quiescence_unproven"
	proposalTurnGrowthCode     = "workspace_growth_limit"
	proposalTurnFailureCode    = "proposal_turn_failed"
)

// ProposalTurnContext is the bounded captured review context used to confirm
// and serialize a request-change action. It never grants a provider a root.
type ProposalTurnContext struct {
	Target             string
	Path               repository.RepoPath
	Side               repository.DiffSide
	Lines              DiscussionLineRange
	SelectedText       string
	Hunk               string
	UserConcern        string
	RelatedContext     []DiscussionContextUnit
	SelectedTranscript []DiscussionContextUnit
}

func (c ProposalTurnContext) discussionContext() DiscussionContext {
	return DiscussionContext{
		Target:             c.Target,
		Path:               repository.RepoPath(c.Path.Bytes()),
		Side:               c.Side,
		Lines:              c.Lines,
		SelectedText:       c.SelectedText,
		Hunk:               c.Hunk,
		UserConcern:        c.UserConcern,
		RelatedContext:     append([]DiscussionContextUnit(nil), c.RelatedContext...),
		SelectedTranscript: append([]DiscussionContextUnit(nil), c.SelectedTranscript...),
	}
}

// Validate checks the review evidence before a proposal lineage is loaded or
// any workspace/provider side effect is admitted.
func (c ProposalTurnContext) Validate() error {
	if err := c.discussionContext().Validate(); err != nil {
		return err
	}
	return nil
}

// BuildProposalPrompt renders the confirmed request-change contract using the
// same bounded context policy as discussion prompts.
func BuildProposalPrompt(context ProposalTurnContext, intent review.ProposalIntent) (string, error) {
	return BuildProposalPromptWithPolicy(context, intent, DefaultResourcePolicy())
}

// BuildProposalPromptWithPolicy keeps the confirmed intent and focused review
// evidence mandatory while compacting only optional context. Expected paths
// are warnings for scope review; they never clip provider output.
func BuildProposalPromptWithPolicy(context ProposalTurnContext, intent review.ProposalIntent, policy ResourcePolicy) (string, error) {
	limits, err := discussionPromptLimits(policy)
	if err != nil {
		return "", err
	}
	if err := intent.Validate(); err != nil {
		return "", err
	}
	if err := context.Validate(); err != nil {
		return "", err
	}
	discussion := context.discussionContext()
	if uint64(len([]byte(discussion.UserConcern))) > uint64(limits.ConcernBytes) || uint64(len([]byte(discussion.SelectedText))) > uint64(limits.SelectedAnchorBytes) {
		return "", ErrProviderInputLimit
	}

	optional := discussionOptionalUnits(discussion)
	chosen, omitted := compactDiscussionUnits(optional, limits)
	for {
		prompt := renderProposalPrompt(discussion, intent, chosen, omitted)
		if uint64(len([]byte(prompt))) <= uint64(limits.SerializedInputBytes) {
			return prompt, nil
		}
		if len(chosen) == 0 {
			return "", ErrProviderInputLimit
		}
		worst := worstOptionalIndex(chosen)
		chosen = append(chosen[:worst], chosen[worst+1:]...)
		omitted++
	}
}

func renderProposalPrompt(context DiscussionContext, intent review.ProposalIntent, units []optionalDiscussionUnit, omitted int) string {
	var builder strings.Builder
	builder.WriteString("Nudge Request change proposal\n\n")
	builder.WriteString("Implement only the confirmed focused intent below. Avoid unrelated formatting or refactors. Perform only the smallest relevant validation.\n")
	builder.WriteString("Edit only the isolated proposal result root. Do not touch refs, the index, baseline, admin state, the source worktree, or the destination. Nudge derives and shows the complete resulting patch before approval.\n\n")
	builder.WriteString("Confirmed intent:\nSummary: ")
	builder.WriteString(intent.Summary)
	builder.WriteString("\nExpected paths (scope warnings only; do not use as an output boundary):\n")
	for _, path := range intent.ExpectedPaths {
		fmt.Fprintf(&builder, "- %s\n", proposalPromptPath(path))
	}
	fmt.Fprintf(&builder, "\nTarget: %s\nPath: %s\nDiff side: %s\nSelected lines: %d-%d\n\n", context.Target, string(context.Path), context.Side, context.Lines.Start, context.Lines.End)
	builder.WriteString("Selected text:\n")
	builder.WriteString(context.SelectedText)
	builder.WriteString("\n\nOriginal concern:\n")
	builder.WriteString(context.UserConcern)
	builder.WriteByte('\n')
	for _, unit := range units {
		switch unit.kind {
		case "hunk":
			builder.WriteString("\nRelevant hunk:\n")
		case "context":
			builder.WriteString("\nRelated review context (captured):\n")
		case "transcript":
			builder.WriteString("\nSelected prior discussion context:\n")
		}
		fmt.Fprintf(&builder, "[%s]\n%s\n", unit.unit.Label, unit.unit.Text)
	}
	if omitted > 0 {
		fmt.Fprintf(&builder, "\n[%d optional context unit(s) omitted due to input limits; the confirmed intent and focused evidence are complete]\n", omitted)
	}
	builder.WriteString("\nUse only the isolated result root granted by Nudge. Do not request network access or another readable/writable root. Do not publish or apply a patch; Nudge owns complete result capture and proposal approval.\n")
	return builder.String()
}

func proposalPromptPath(path repository.RepoPath) string {
	value := path.Bytes()
	if utf8.Valid(value) {
		return string(value)
	}
	return fmt.Sprintf("hex:%x", value)
}

// ProposalTurnPermissionProfile builds the only provider profile accepted by
// proposal turns: one result root, no network, and containment evidence from
// the platform/workspace owner.
type ProposalTurnPermissionProfile struct {
	ResultRoot       string
	RuntimeRoots     []provider.PermissionRoot
	Containment      provider.ContainmentEvidence
	RuntimeApprovals provider.RuntimeApprovalPolicy
}

// Policy returns a detached provider-neutral policy and the result-root cwd.
func (p ProposalTurnPermissionProfile) Policy() (provider.TurnPermissionPolicy, string, error) {
	if !validAbsoluteProposalRoot(p.ResultRoot) || !p.Containment.CanonicalRead || !p.Containment.CanonicalWrite || !p.Containment.NoSymlinkEscape || !p.Containment.NoJunctionEscape || !p.Containment.NoMountEscape || !p.Containment.NoHardLinkAlias || !p.Containment.HandlesQuiescent {
		return provider.TurnPermissionPolicy{}, "", ErrProposalTurnUnavailable
	}
	if p.RuntimeApprovals == "" {
		p.RuntimeApprovals = provider.RuntimeApprovalsExplicit
	}
	runtimeRoots := append([]provider.PermissionRoot(nil), p.RuntimeRoots...)
	if len(runtimeRoots) == 0 {
		runtimeRoots = []provider.PermissionRoot{{Path: p.ResultRoot}}
	}
	for _, root := range runtimeRoots {
		if root.Validate(provider.DefaultValidationLimits()) != nil || !proposalPathWithin(p.ResultRoot, root.Path) {
			return provider.TurnPermissionPolicy{}, "", ErrProposalTurnUnavailable
		}
	}
	policy := provider.TurnPermissionPolicy{
		Filesystem:         provider.FilesystemProposalResult,
		ReadableRoots:      []provider.PermissionRoot{{Path: p.ResultRoot}},
		WritableRoots:      []provider.PermissionRoot{{Path: p.ResultRoot}},
		RuntimeRoots:       runtimeRoots,
		ProposalResultRoot: provider.PermissionRoot{Path: p.ResultRoot},
		Containment:        p.Containment,
		Network:            provider.NetworkDisabled,
		RuntimeApprovals:   p.RuntimeApprovals,
	}
	if err := policy.Validate(provider.DefaultValidationLimits()); err != nil {
		return provider.TurnPermissionPolicy{}, "", err
	}
	return policy, p.ResultRoot, nil
}

func validAbsoluteProposalRoot(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, '\x00')
}

func proposalPathWithin(root, child string) bool {
	if !validAbsoluteProposalRoot(root) || !validAbsoluteProposalRoot(child) {
		return false
	}
	relative, err := filepath.Rel(root, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// ValidateProposalRuntimeApproval keeps a runtime request inside the already
// recorded proposal profile. It never expands roots and rejects network/tool
// requests whose scope cannot be proven from the normalized request.
func ValidateProposalRuntimeApproval(approval RuntimeApproval, policy provider.TurnPermissionPolicy) error {
	if err := policy.Validate(provider.DefaultValidationLimits()); err != nil || policy.Filesystem != provider.FilesystemProposalResult || policy.Network != provider.NetworkDisabled || policy.RuntimeApprovals != provider.RuntimeApprovalsExplicit || approval.RequestedScopeID.Validate(provider.DefaultValidationLimits()) != nil || approval.RequestedScopeID.Kind != approval.Kind {
		return ErrProposalRuntimeApproval
	}
	scope := approval.RequestedScopeID
	switch approval.Kind {
	case provider.RuntimeApprovalCommand:
		if approval.NetworkTarget != "" || !proposalPathWithinAny(policy.RuntimeRoots, scope.Executable) {
			return ErrProposalRuntimeApproval
		}
	case provider.RuntimeApprovalFile:
		if approval.NetworkTarget != "" || !proposalPathWithinAny(policy.RuntimeRoots, scope.Path.Path) {
			return ErrProposalRuntimeApproval
		}
	default:
		return ErrProposalRuntimeApproval
	}
	return nil
}

func proposalPathWithinAny(roots []provider.PermissionRoot, child string) bool {
	for _, root := range roots {
		if proposalPathWithin(root.Path, child) {
			return true
		}
	}
	return false
}

// ProposalQuiescenceProof is the application-facing proof required before a
// result becomes available to T038. It is intentionally independent of any
// process or OS implementation.
type ProposalQuiescenceProof struct {
	DescendantsEmpty      bool
	WritableHandlesClosed bool
	ResultRootStable      bool
}

// ProposalTurnLease is owned by the workspace/platform adapter. Acquire must
// already have admitted capacity and the native isolation boundary.
type ProposalTurnLease interface {
	PermissionProfile() ProposalTurnPermissionProfile
	Quiesce(context.Context) (ProposalQuiescenceProof, error)
	Terminate(context.Context) error
	Close() error
}

// ProposalTurnWorkspace is the consumer-owned workspace seam. It must return
// only a verified four-root workspace lease and never expose destination or
// admin paths through the provider policy.
type ProposalTurnWorkspace interface {
	AcquireProposalTurn(context.Context, review.ProposalWorkspace) (ProposalTurnLease, error)
}

// ProposalGrowthStatus is optional adapter evidence sampled while a turn is
// active. Monitored evidence cancels on a breach but never claims hard quota.
type ProposalGrowthStatus struct {
	ObservedBytes ByteSize
	LimitBytes    ByteSize
	ReserveBytes  ByteSize
	Enforcement   VolumeCapacityMode
}

type proposalGrowthMonitor interface {
	CheckGrowth(context.Context) (ProposalGrowthStatus, error)
}

// ProposalTurnOutcome is the terminal provider classification consumed by the
// proposal coordinator. Only Succeeded can hand a quiescent result to T038.
type ProposalTurnOutcome string

const (
	ProposalTurnSucceeded     ProposalTurnOutcome = "succeeded"
	ProposalTurnCancelled     ProposalTurnOutcome = "cancelled"
	ProposalTurnOutcomeFailed ProposalTurnOutcome = "failed"
	ProposalTurnDisconnected  ProposalTurnOutcome = "disconnected"
)

func (o ProposalTurnOutcome) Validate() error {
	switch o {
	case ProposalTurnSucceeded, ProposalTurnCancelled, ProposalTurnOutcomeFailed, ProposalTurnDisconnected:
		return nil
	default:
		return ErrProposalTurnUnavailable
	}
}

// FinishProposalTurn is the terminal application use case. The result root
// is never read or hashed here; T110/T111 own independent capture and patch
// derivation after ResultReady is emitted.
type FinishProposalTurn struct {
	AttemptID     domain.OperationID
	ProposalID    domain.ProposalID
	ThreadID      domain.ReviewThreadID
	TurnID        domain.ProviderTurnID
	OperationID   domain.OperationID
	CorrelationID CorrelationID
	Outcome       ProposalTurnOutcome
	Reason        string
}

// RecoverProposalTurn closes a durable turn that was active when Nudge
// restarted. It deliberately leaves the result non-ready because no process
// or writable-handle quiescence can be inferred after a crash.
type RecoverProposalTurn struct {
	Guard         SessionWriteGuard
	AttemptID     domain.OperationID
	ProposalID    domain.ProposalID
	ThreadID      domain.ReviewThreadID
	CorrelationID CorrelationID
}

// ProposalTurnCommit is the durable outcome and non-authoritative lifecycle
// events returned by the coordinator.
type ProposalTurnCommit struct {
	Guard     SessionWriteGuard
	Attempt   review.ProposalAttempt
	Workspace review.ProposalWorkspace
	Thread    review.ReviewThread
	Turn      *ProviderTurnRecord
	Events    []Event
}

type proposalTurnActive struct {
	attempt   review.ProposalAttempt
	workspace review.ProposalWorkspace
	thread    review.ReviewThread
	lease     ProposalTurnLease
	turn      *ProviderTurnRecord
	guard     SessionWriteGuard
	finishing bool
}

// ProposalTurnService owns one in-process proposal-turn lease at a time while
// durable attempt/workspace/thread state remains the recovery authority.
type ProposalTurnService struct {
	store        ReviewStore
	proposals    ProposalWorkspaceStore
	lifecycle    ProposalWorkspaceLifecycleStore
	conversation *ProviderConversationService
	workspace    ProposalTurnWorkspace
	clock        Clock

	startMu sync.Mutex
	mu      sync.Mutex
	active  map[domain.OperationID]*proposalTurnActive
}

// ProposalTurnServiceConfig composes the application-owned coordinator with
// the existing provider lifecycle and verified workspace adapters.
type ProposalTurnServiceConfig struct {
	Store        ReviewStore
	Proposals    ProposalWorkspaceStore
	Lifecycle    ProposalWorkspaceLifecycleStore
	Conversation *ProviderConversationService
	Workspace    ProposalTurnWorkspace
	Clock        Clock
}

// NewProposalTurnService validates the proposal-turn composition. Proposal
// mode is unavailable when any durable or native owner is absent.
func NewProposalTurnService(config ProposalTurnServiceConfig) (*ProposalTurnService, error) {
	if config.Store == nil || config.Proposals == nil || config.Lifecycle == nil || config.Conversation == nil || config.Workspace == nil {
		return nil, ErrProposalTurnUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ProposalTurnService{store: config.Store, proposals: config.Proposals, lifecycle: config.Lifecycle, conversation: config.Conversation, workspace: config.Workspace, clock: config.Clock, active: make(map[domain.OperationID]*proposalTurnActive)}, nil
}

// Start admits one explicit Request change after validating the exact durable
// intent, verified baseline/workspace, capacity-backed lease, and profile.
func (s *ProposalTurnService) Start(ctx context.Context, command RequestProposal) (ProposalTurnCommit, error) {
	if s == nil || s.store == nil || s.proposals == nil || s.lifecycle == nil || s.conversation == nil || s.workspace == nil {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	if err := validateProposalRequest(ctx, command); err != nil {
		return ProposalTurnCommit{}, err
	}
	if _, err := BuildProposalPrompt(command.Context, command.Intent); err != nil {
		return ProposalTurnCommit{}, err
	}

	s.startMu.Lock()
	defer s.startMu.Unlock()
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if err := validateProposalAggregateForRequest(aggregate, command); err != nil {
		if errors.Is(err, ErrProposalTurnConflict) && !s.hasActive(command.ProposalID, aggregate.Workspace.ID) {
			return ProposalTurnCommit{}, ErrProposalTurnRecoveryRequired
		}
		return ProposalTurnCommit{}, err
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if thread.SessionID != command.Guard.SessionID || thread.ProviderConversationID == nil || *thread.ProviderConversationID != command.ConversationID {
		return ProposalTurnCommit{}, ErrThreadNotOwned
	}
	lifecycle, err := s.loadDurableWorkspace(ctx, aggregate.Workspace)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if s.hasActive(command.ProposalID, aggregate.Workspace.ID) {
		return ProposalTurnCommit{}, ErrProposalTurnConflict
	}
	for _, attempt := range aggregate.Attempts {
		if attempt.Outcome == review.ProposalAttemptDeriving || attempt.Outcome == review.ProposalAttemptNoChangesResetting {
			return ProposalTurnCommit{}, ErrProposalTurnConflict
		}
	}

	lease, err := s.workspace.AcquireProposalTurn(ctx, aggregate.Workspace)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.Close()
		}
	}()
	profile := lease.PermissionProfile()
	permissions, workingDir, err := profile.Policy()
	if err != nil || workingDir != aggregate.Workspace.Roots.Result {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}

	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	attempt := review.ProposalAttempt{ID: command.OperationID, ProposalID: command.ProposalID, WorkspaceID: aggregate.Workspace.ID, ThreadID: command.ThreadID, ProviderConversationID: providerConversationIDPointer(command.ConversationID), SourceGeneration: aggregate.Intent.ConfirmedAgainst, Outcome: review.ProposalAttemptDeriving, ResultDisposition: review.ProposalResultNone, Reason: proposalTurnPreparedReason, StartedAt: now}
	workspace := aggregate.Workspace
	if err := workspace.Transition(review.WorkspaceTurnRunning); err != nil {
		return ProposalTurnCommit{}, ErrProposalTurnConflict
	}
	workspace.UpdatedAt = now
	if err := thread.SetProposal(review.ProposalGenerating, providerProposalIDPointer(command.ProposalID), now); err != nil {
		return ProposalTurnCommit{}, err
	}
	guard, err := s.persistPrepared(ctx, command.Guard, thread, workspace, attempt)
	if err != nil {
		return ProposalTurnCommit{}, err
	}

	prompt, err := BuildProposalPrompt(command.Context, command.Intent)
	if err != nil {
		return ProposalTurnCommit{Guard: guard, Attempt: attempt, Workspace: workspace, Thread: thread}, err
	}
	provenance := proposalTurnProvenance(command, aggregate, lifecycle.Baseline.Hash, prompt, permissions)
	providerCommit, providerErr := s.conversation.StartTurn(ctx, StartProviderTurn{Guard: guard, ThreadID: command.ThreadID, ConversationID: command.ConversationID, Mode: provider.TurnPropose, Prompt: prompt, WorkingDir: workingDir, Permissions: permissions, Provenance: provenance, OperationID: command.OperationID, CorrelationID: command.CorrelationID})
	providerGuard := providerCommit.Guard
	if providerGuard.Validate() != nil {
		providerGuard = guard
	}
	if providerCommit.Turn != nil {
		attempt.ProviderTurnID = providerTurnIDPointer(providerCommit.Turn.ID)
		attempt.ProviderTurnRef = string(providerCommit.Turn.ProviderTurnRef)
		attempt.ProviderConversationRef = string(providerCommit.Conversation.ProviderConversationRef)
		nextGuard, updateErr := s.persistAttempt(ctx, providerGuard, attempt)
		if updateErr != nil {
			providerErr = errors.Join(providerErr, updateErr)
		} else {
			providerGuard = nextGuard
		}
	}
	if providerErr != nil || providerCommit.Turn == nil {
		if providerErr == nil {
			providerErr = ErrProviderTurnOrphan
		}
		failed, failErr := s.failStarted(ctx, providerGuard, attempt, workspace, thread, lease, providerCommit.Turn, proposalTurnFailureCode, providerTurnFailureReason(providerErr))
		keepLease = true
		if failErr != nil {
			return failed, errors.Join(providerErr, failErr)
		}
		return failed, providerErr
	}

	active := &proposalTurnActive{attempt: attempt, workspace: workspace, thread: thread, lease: lease, turn: providerCommit.Turn, guard: providerGuard}
	s.mu.Lock()
	if _, exists := s.active[attempt.ID]; exists {
		s.mu.Unlock()
		_ = lease.Close()
		return ProposalTurnCommit{}, ErrProposalTurnConflict
	}
	s.active[attempt.ID] = active
	s.mu.Unlock()
	keepLease = true
	events := append([]Event{}, providerCommit.Events...)
	events = append(events, ProposalTurnPrepared{AttemptID: attempt.ID, ProposalID: attempt.ProposalID, WorkspaceID: attempt.WorkspaceID, ThreadID: attempt.ThreadID, TurnID: providerCommit.Turn.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID})
	return ProposalTurnCommit{Guard: providerCommit.Guard, Attempt: attempt, Workspace: workspace, Thread: thread, Turn: providerCommit.Turn, Events: events}, nil
}

// Finish applies one terminal provider fact. Success requires a positive
// quiescence proof and returns ResultReady without reading result bytes.
func (s *ProposalTurnService) Finish(ctx context.Context, command FinishProposalTurn) (ProposalTurnCommit, error) {
	if err := validateFinishProposalTurn(ctx, command); err != nil {
		return ProposalTurnCommit{}, err
	}
	active, err := s.takeActive(command.AttemptID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if active.attempt.ProposalID != command.ProposalID || active.attempt.ThreadID != command.ThreadID || active.turn == nil || active.turn.ID != command.TurnID || active.turn.OperationID != command.OperationID {
		s.restoreActive(command.AttemptID, active)
		return ProposalTurnCommit{}, ErrProposalTurnNotFound
	}
	if command.Outcome != ProposalTurnSucceeded {
		return s.finishFailure(ctx, command, active, proposalOutcomeReason(command.Outcome), proposalFailureCode(command.Outcome))
	}

	proof, quiesceErr := active.lease.Quiesce(ctx)
	if quiesceErr != nil || !proof.DescendantsEmpty || !proof.WritableHandlesClosed || !proof.ResultRootStable {
		_ = active.lease.Terminate(context.Background())
		_, _ = active.lease.Quiesce(context.Background())
		_ = active.lease.Close()
		return s.persistFailure(ctx, active, proposalTurnQuiescenceCode, proposalTurnQuiescenceCode, review.ProposalFailureWorkspace)
	}
	guard := activeGuard(active)
	turnCommit, turnErr := s.conversation.CompleteTurn(ctx, CompleteProviderTurn{Guard: guard, ThreadID: command.ThreadID, TurnID: command.TurnID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, State: ProviderTurnCompleted})
	if turnErr != nil {
		_ = active.lease.Terminate(context.Background())
		_ = active.lease.Close()
		return s.persistFailure(ctx, active, proposalTurnFailureCode, proposalTurnFailedReason, review.ProposalFailureProvider)
	}
	active.guard = turnCommit.Guard
	if err := active.lease.Close(); err != nil {
		return s.persistFailure(ctx, active, proposalTurnQuiescenceCode, proposalTurnQuiescenceCode, review.ProposalFailureWorkspace)
	}
	active.workspace.State = review.WorkspaceResultReady
	active.workspace.UpdatedAt = s.clock.Now().UTC()
	guard, err = s.persistResultReady(ctx, turnCommit.Guard, active.thread, active.workspace)
	if err != nil {
		return s.persistFailure(ctx, active, proposalTurnFailureCode, proposalTurnFailedReason, review.ProposalFailurePersistence)
	}
	s.removeActive(active.attempt.ID)
	return ProposalTurnCommit{Guard: guard, Attempt: active.attempt, Workspace: active.workspace, Thread: active.thread, Turn: turnCommit.Turn, Events: []Event{ProposalResultReady{AttemptID: active.attempt.ID, ProposalID: active.attempt.ProposalID, WorkspaceID: active.attempt.WorkspaceID, ThreadID: active.attempt.ThreadID, TurnID: command.TurnID, OperationID: command.OperationID, CorrelationID: command.CorrelationID}}}, nil
}

// Recover records repair-required state for a possibly-mutated result after a
// restart. A later owner-specific repair flow must prove process and handle
// closure before reset or derivation.
func (s *ProposalTurnService) Recover(ctx context.Context, command RecoverProposalTurn) (ProposalTurnCommit, error) {
	if s == nil || ctx == nil || command.Guard.Validate() != nil || command.AttemptID == "" || command.ProposalID == "" || command.ThreadID == "" || command.CorrelationID == "" {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, command.ProposalID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if aggregate.Workspace.State != review.WorkspaceTurnRunning || aggregate.Workspace.SourceThreadID != command.ThreadID {
		return ProposalTurnCommit{}, ErrProposalTurnRecoveryRequired
	}
	var attempt review.ProposalAttempt
	found := false
	for _, candidate := range aggregate.Attempts {
		if candidate.ID == command.AttemptID {
			attempt = candidate
			found = true
			break
		}
	}
	if !found || attempt.Outcome != review.ProposalAttemptDeriving || attempt.ThreadID != command.ThreadID {
		return ProposalTurnCommit{}, ErrProposalTurnRecoveryRequired
	}
	thread, err := s.store.LoadThread(ctx, command.ThreadID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	now := s.clock.Now().UTC()
	attempt.Outcome = review.ProposalAttemptFailed
	attempt.ResultDisposition = review.ProposalResultPresent
	attempt.FailurePhase = review.ProposalFailureWorkspace
	attempt.Reason = "proposal turn requires recovery"
	attempt.FinishedAt = &now
	workspace := aggregate.Workspace
	if err := workspace.Transition(review.WorkspaceRepairRequired); err != nil {
		return ProposalTurnCommit{}, err
	}
	workspace.UpdatedAt = now
	if err := thread.SetProposal(review.ProposalFailed, providerProposalIDPointer(command.ProposalID), now); err != nil {
		return ProposalTurnCommit{}, err
	}
	guard, err := s.store.WithSessionTx(ctx, command.Guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		if err := proposalTx.RecordProposalAttempt(ctx, attempt); err != nil {
			return err
		}
		if err := lifecycleTx.UpdateProposalWorkspace(ctx, workspace); err != nil {
			return err
		}
		return tx.SaveThread(ctx, thread)
	})
	if err != nil {
		return ProposalTurnCommit{Guard: guard, Attempt: attempt, Workspace: workspace, Thread: thread}, err
	}
	return ProposalTurnCommit{Guard: guard, Attempt: attempt, Workspace: workspace, Thread: thread, Events: []Event{ProposalTurnFailed{AttemptID: attempt.ID, ProposalID: attempt.ProposalID, WorkspaceID: attempt.WorkspaceID, ThreadID: attempt.ThreadID, TurnID: providerTurnID(attempt.ProviderTurnID), OperationID: attempt.ID, CorrelationID: command.CorrelationID, Reason: "proposal turn requires recovery"}}}, nil
}

// Cancel terminates the contained provider and records a non-ready outcome.
func (s *ProposalTurnService) Cancel(ctx context.Context, command CancelProposal) (ProposalTurnCommit, error) {
	if ctx == nil || command.Guard.Validate() != nil || command.AttemptID == "" || command.CorrelationID == "" {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	active, err := s.takeActive(command.AttemptID)
	if err != nil {
		return ProposalTurnCommit{}, err
	}
	if active.turn == nil {
		return ProposalTurnCommit{}, ErrProposalTurnNotFound
	}
	providerCommit, cancelErr := s.conversation.InterruptTurn(ctx, InterruptProviderTurn{Guard: activeGuard(active), ThreadID: active.thread.ID, TurnID: active.turn.ID, OperationID: active.turn.OperationID, CorrelationID: command.CorrelationID})
	if providerCommit.Guard.Validate() == nil {
		active.guard = providerCommit.Guard
	}
	_ = active.lease.Terminate(context.Background())
	_, _ = active.lease.Quiesce(context.Background())
	_ = active.lease.Close()
	if cancelErr != nil {
		return s.persistFailure(ctx, active, proposalTurnFailureCode, proposalTurnFailedReason, review.ProposalFailureProvider)
	}
	result, failureErr := s.persistFailure(ctx, active, proposalTurnCancelled, proposalTurnCancelled, review.ProposalFailureProvider)
	if failureErr != nil {
		return result, failureErr
	}
	result.Guard = providerCommit.Guard
	return result, nil
}

// HandleProviderEvent maps normalized terminal events to the proposal
// coordinator. Streaming message events remain owned by ProviderEventProcessor.
func (s *ProposalTurnService) HandleProviderEvent(ctx context.Context, event provider.ProviderEvent) (ProposalTurnCommit, error) {
	if event.Kind != provider.EventTurnCompleted && event.Kind != provider.EventTurnFailed && event.Kind != provider.EventDisconnected && event.Kind != provider.EventProviderError {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	s.mu.Lock()
	var activeID domain.OperationID
	for id, active := range s.active {
		if active.turn == nil {
			continue
		}
		turnMatches := active.turn.ID == event.TurnID && active.thread.ID == event.ThreadID
		if event.Kind == provider.EventDisconnected || event.Kind == provider.EventProviderError {
			turnMatches = (event.TurnID == "" || active.turn.ID == event.TurnID) && (event.ThreadID == "" || active.thread.ID == event.ThreadID)
		}
		if turnMatches {
			activeID = id
			break
		}
	}
	s.mu.Unlock()
	if activeID == "" {
		return ProposalTurnCommit{}, ErrProposalTurnNotFound
	}
	outcome := ProposalTurnSucceeded
	if event.Kind == provider.EventTurnFailed || event.Kind == provider.EventDisconnected || event.Kind == provider.EventProviderError {
		outcome = ProposalTurnOutcomeFailed
	}
	active, err := s.lookupActive(activeID)
	if err != nil || active.turn == nil {
		return ProposalTurnCommit{}, ErrProposalTurnNotFound
	}
	threadID, turnID, operationID, correlationID := event.ThreadID, event.TurnID, event.OperationID, CorrelationID(event.CorrelationID)
	if threadID == "" {
		threadID = active.thread.ID
	}
	if turnID == "" {
		turnID = active.turn.ID
	}
	if operationID == "" {
		operationID = active.turn.OperationID
	}
	if correlationID == "" {
		correlationID = active.turn.CorrelationID
	}
	return s.Finish(ctx, FinishProposalTurn{AttemptID: activeID, ProposalID: active.attempt.ProposalID, ThreadID: threadID, TurnID: turnID, OperationID: operationID, CorrelationID: correlationID, Outcome: outcome, Reason: boundedProposalReason(event.Error)})
}

// CheckGrowth samples optional native/monitored growth evidence and terminates
// the provider before any result can be marked ready when the limit is crossed.
func (s *ProposalTurnService) CheckGrowth(ctx context.Context, attemptID domain.OperationID) (ProposalGrowthStatus, error) {
	active, err := s.lookupActive(attemptID)
	if err != nil {
		return ProposalGrowthStatus{}, err
	}
	monitor, ok := active.lease.(proposalGrowthMonitor)
	if !ok {
		return ProposalGrowthStatus{}, ErrProposalTurnUnavailable
	}
	status, err := monitor.CheckGrowth(ctx)
	if err != nil {
		return status, err
	}
	if status.LimitBytes != 0 && status.ObservedBytes > status.LimitBytes {
		active, takeErr := s.takeActive(attemptID)
		if takeErr != nil {
			return status, takeErr
		}
		if active.turn == nil {
			return status, ErrProposalTurnNotFound
		}
		_, finishErr := s.finishFailure(ctx, FinishProposalTurn{AttemptID: active.attempt.ID, ProposalID: active.attempt.ProposalID, ThreadID: active.thread.ID, TurnID: active.turn.ID, OperationID: active.attempt.ID, CorrelationID: active.turn.CorrelationID, Outcome: ProposalTurnOutcomeFailed, Reason: proposalTurnGrowthCode}, active, proposalTurnGrowthCode, proposalTurnGrowthCode)
		if finishErr != nil {
			return status, errors.Join(ErrProposalTurnGrowth, finishErr)
		}
		return status, ErrProposalTurnGrowth
	}
	return status, nil
}

func validateProposalRequest(ctx context.Context, command RequestProposal) error {
	if ctx == nil || command.Guard.Validate() != nil || command.ThreadID == "" || command.ProposalID == "" || command.ConversationID == "" || command.OperationID == "" || command.CorrelationID == "" || command.Intent.Validate() != nil || command.Intent.ID != command.ProposalID || command.Intent.ThreadID != command.ThreadID {
		return ErrProposalTurnUnavailable
	}
	return command.Context.Validate()
}

func validateFinishProposalTurn(ctx context.Context, command FinishProposalTurn) error {
	if ctx == nil || command.AttemptID == "" || command.ProposalID == "" || command.ThreadID == "" || command.TurnID == "" || command.OperationID == "" || command.CorrelationID == "" {
		return ErrProposalTurnUnavailable
	}
	return command.Outcome.Validate()
}

func validateProposalAggregateForRequest(aggregate review.ProposalAggregate, command RequestProposal) error {
	if err := aggregate.Validate(); err != nil || aggregate.Intent.ID != command.Intent.ID || !proposalIntentsEqual(aggregate.Intent, command.Intent) || aggregate.Proposal.ID != command.ProposalID || aggregate.Proposal.ThreadID != command.ThreadID || aggregate.Workspace.SourceThreadID != command.ThreadID {
		return ErrProposalTurnUnavailable
	}
	if aggregate.Intent.ConfirmedAgainst.Head.Kind == repository.SnapshotCommit {
		if command.Eligibility == nil || command.Eligibility.Validate() != nil || !command.Eligibility.Eligible || command.Eligibility.WorktreeID != aggregate.Workspace.WorktreeID || command.Eligibility.ExpectedHead != aggregate.Intent.ConfirmedAgainst.Head.ObjectID || command.Eligibility.ObservedHead != command.Eligibility.ExpectedHead {
			return ErrProposalTurnUnavailable
		}
	}
	if aggregate.Workspace.State != review.WorkspaceReady {
		if aggregate.Workspace.State == review.WorkspaceTurnRunning {
			return ErrProposalTurnConflict
		}
		return ErrProposalTurnUnavailable
	}
	for _, version := range aggregate.Versions {
		if version.Status == review.ProposalVersionReady {
			return ErrProposalVersionReady
		}
	}
	return nil
}

func proposalIntentsEqual(left, right review.ProposalIntent) bool {
	if left.ID != right.ID || left.ThreadID != right.ThreadID || left.Summary != right.Summary || left.AnchorVersionID != right.AnchorVersionID || !left.ConfirmedAt.Equal(right.ConfirmedAt) || !generationProvenanceEqual(left.ConfirmedAgainst, right.ConfirmedAgainst) || len(left.ExpectedPaths) != len(right.ExpectedPaths) {
		return false
	}
	for index := range left.ExpectedPaths {
		if string(left.ExpectedPaths[index].Bytes()) != string(right.ExpectedPaths[index].Bytes()) {
			return false
		}
	}
	return true
}

func generationProvenanceEqual(left, right review.GenerationProvenance) bool {
	if left.SessionID != right.SessionID || left.Generation != right.Generation || left.Base != right.Base || left.Head != right.Head {
		return false
	}
	if left.CaptureID == nil || right.CaptureID == nil {
		return left.CaptureID == nil && right.CaptureID == nil
	}
	return *left.CaptureID == *right.CaptureID
}

func (s *ProposalTurnService) loadDurableWorkspace(ctx context.Context, workspace review.ProposalWorkspace) (ProposalWorkspaceLifecycle, error) {
	lifecycle, err := s.lifecycle.LoadLatestProposalWorkspaceLifecycle(ctx, workspace.ID)
	if err != nil || lifecycle.Phase != WorkspaceLifecycleReady || lifecycle.Baseline.Validate() != nil || lifecycle.Result.Validate() != nil || lifecycle.WorkspaceID != workspace.ID || lifecycle.ThreadID != workspace.SourceThreadID {
		return ProposalWorkspaceLifecycle{}, ErrProposalTurnRecoveryRequired
	}
	return lifecycle, nil
}

func (s *ProposalTurnService) persistPrepared(ctx context.Context, guard SessionWriteGuard, thread review.ReviewThread, workspace review.ProposalWorkspace, attempt review.ProposalAttempt) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		if err := tx.SaveThread(ctx, thread); err != nil {
			return err
		}
		if err := lifecycleTx.UpdateProposalWorkspace(ctx, workspace); err != nil {
			return err
		}
		return proposalTx.RecordProposalAttempt(ctx, attempt)
	})
}

func (s *ProposalTurnService) persistAttempt(ctx context.Context, guard SessionWriteGuard, attempt review.ProposalAttempt) (SessionWriteGuard, error) {
	next, err := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		return proposalTx.RecordProposalAttempt(ctx, attempt)
	})
	return next, err
}

func (s *ProposalTurnService) persistResultReady(ctx context.Context, guard SessionWriteGuard, thread review.ReviewThread, workspace review.ProposalWorkspace) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		if err := lifecycleTx.UpdateProposalWorkspace(ctx, workspace); err != nil {
			return err
		}
		return tx.SaveThread(ctx, thread)
	})
}

func (s *ProposalTurnService) persistFailure(ctx context.Context, active *proposalTurnActive, reason, errorCode string, phase review.ProposalFailurePhase) (ProposalTurnCommit, error) {
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalTurnCommit{}, ErrProposalTurnUnavailable
	}
	active.attempt.Outcome = review.ProposalAttemptFailed
	active.attempt.ResultDisposition = review.ProposalResultPresent
	active.attempt.FailurePhase = phase
	active.attempt.Reason = boundedProposalReason(reason)
	active.attempt.FinishedAt = &now
	if active.workspace.State != review.WorkspaceRepairRequired {
		if err := active.workspace.Transition(review.WorkspaceRepairRequired); err != nil {
			return ProposalTurnCommit{}, err
		}
	}
	active.workspace.UpdatedAt = now
	if err := active.thread.SetProposal(review.ProposalFailed, providerProposalIDPointer(active.attempt.ProposalID), now); err != nil {
		return ProposalTurnCommit{}, err
	}
	guard := activeGuard(active)
	guard, err := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		if err := proposalTx.RecordProposalAttempt(ctx, active.attempt); err != nil {
			return err
		}
		if err := lifecycleTx.UpdateProposalWorkspace(ctx, active.workspace); err != nil {
			return err
		}
		return tx.SaveThread(ctx, active.thread)
	})
	s.removeActive(active.attempt.ID)
	if err != nil {
		return ProposalTurnCommit{Guard: guard, Attempt: active.attempt, Workspace: active.workspace, Thread: active.thread, Turn: active.turn}, err
	}
	return ProposalTurnCommit{Guard: guard, Attempt: active.attempt, Workspace: active.workspace, Thread: active.thread, Turn: active.turn, Events: []Event{ProposalTurnFailed{AttemptID: active.attempt.ID, ProposalID: active.attempt.ProposalID, WorkspaceID: active.attempt.WorkspaceID, ThreadID: active.attempt.ThreadID, TurnID: providerTurnID(active.attempt.ProviderTurnID), OperationID: active.attempt.ID, CorrelationID: active.turnCorrelation(active), Reason: errorCode}}}, nil
}

func (s *ProposalTurnService) failStarted(ctx context.Context, guard SessionWriteGuard, attempt review.ProposalAttempt, workspace review.ProposalWorkspace, thread review.ReviewThread, lease ProposalTurnLease, turn *ProviderTurnRecord, code, reason string) (ProposalTurnCommit, error) {
	_ = lease.Terminate(context.Background())
	_, _ = lease.Quiesce(context.Background())
	_ = lease.Close()
	active := &proposalTurnActive{attempt: attempt, workspace: workspace, thread: thread, lease: lease, turn: turn, guard: guard}
	return s.persistFailureWithGuard(ctx, guard, active, reason, code, review.ProposalFailureProvider)
}

func (s *ProposalTurnService) finishFailure(ctx context.Context, command FinishProposalTurn, active *proposalTurnActive, reason, code string) (ProposalTurnCommit, error) {
	_ = active.lease.Terminate(context.Background())
	_, _ = active.lease.Quiesce(context.Background())
	_ = active.lease.Close()
	if active.turn != nil {
		state := ProviderTurnFailed
		if command.Outcome == ProposalTurnCancelled {
			state = ProviderTurnInterrupted
		}
		if turnCommit, err := s.conversation.CompleteTurn(ctx, CompleteProviderTurn{Guard: activeGuard(active), ThreadID: active.thread.ID, TurnID: active.turn.ID, OperationID: command.OperationID, CorrelationID: command.CorrelationID, State: state, ErrorCode: review.ErrorCode(code)}); err == nil {
			active.turn = turnCommit.Turn
			active.guard = turnCommit.Guard
		}
	}
	return s.persistFailure(ctx, active, reason, code, review.ProposalFailureProvider)
}

func (s *ProposalTurnService) persistFailureWithGuard(ctx context.Context, guard SessionWriteGuard, active *proposalTurnActive, reason, code string, phase review.ProposalFailurePhase) (ProposalTurnCommit, error) {
	// The normal failure path uses the current fenced guard. This helper keeps
	// the provider-start failure's advanced fence when no active map exists.
	activeGuardOverride := activeGuard(active)
	if guard.Validate() == nil {
		activeGuardOverride = guard
	}
	active.thread.UpdatedAt = s.clock.Now().UTC()
	active.attempt.Outcome = review.ProposalAttemptFailed
	active.attempt.ResultDisposition = review.ProposalResultPresent
	active.attempt.FailurePhase = phase
	active.attempt.Reason = boundedProposalReason(reason)
	active.attempt.FinishedAt = &active.thread.UpdatedAt
	if active.workspace.State != review.WorkspaceRepairRequired {
		_ = active.workspace.Transition(review.WorkspaceRepairRequired)
	}
	active.workspace.UpdatedAt = active.thread.UpdatedAt
	_ = active.thread.SetProposal(review.ProposalFailed, providerProposalIDPointer(active.attempt.ProposalID), active.thread.UpdatedAt)
	next, err := s.store.WithSessionTx(ctx, activeGuardOverride, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		lifecycleTx, ok := tx.(ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrProposalTurnUnavailable
		}
		if err := proposalTx.RecordProposalAttempt(ctx, active.attempt); err != nil {
			return err
		}
		if err := lifecycleTx.UpdateProposalWorkspace(ctx, active.workspace); err != nil {
			return err
		}
		return tx.SaveThread(ctx, active.thread)
	})
	return ProposalTurnCommit{Guard: next, Attempt: active.attempt, Workspace: active.workspace, Thread: active.thread, Turn: active.turn}, err
}

func (s *ProposalTurnService) hasActive(proposalID domain.ProposalID, workspaceID domain.WorkspaceID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, active := range s.active {
		if active.attempt.ProposalID == proposalID || active.attempt.WorkspaceID == workspaceID {
			return true
		}
	}
	return false
}

func (s *ProposalTurnService) takeActive(id domain.OperationID) (*proposalTurnActive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[id]
	if !ok || active.finishing {
		return nil, ErrProposalTurnNotFound
	}
	active.finishing = true
	return active, nil
}

func (s *ProposalTurnService) restoreActive(id domain.OperationID, active *proposalTurnActive) {
	s.mu.Lock()
	active.finishing = false
	s.active[id] = active
	s.mu.Unlock()
}

func (s *ProposalTurnService) removeActive(id domain.OperationID) {
	s.mu.Lock()
	delete(s.active, id)
	s.mu.Unlock()
}

func (s *ProposalTurnService) lookupActive(id domain.OperationID) (*proposalTurnActive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.active[id]
	if !ok {
		return nil, ErrProposalTurnNotFound
	}
	return active, nil
}

func (a *proposalTurnActive) turnCorrelation(_ *proposalTurnActive) CorrelationID {
	return CorrelationID(a.attempt.ID)
}

func activeGuard(active *proposalTurnActive) SessionWriteGuard {
	// The provider lifecycle returns the advanced fence to its caller. The
	// active record is intentionally not the source of durable authority; the
	// current turn command carries the guard at each boundary.
	if active == nil {
		return SessionWriteGuard{}
	}
	return active.guard
}

func proposalTurnProvenance(command RequestProposal, aggregate review.ProposalAggregate, baselineHash, prompt string, permissions provider.TurnPermissionPolicy) DiscussionTurnProvenance {
	captureID := ""
	if aggregate.Intent.ConfirmedAgainst.CaptureID != nil {
		captureID = string(*aggregate.Intent.ConfirmedAgainst.CaptureID)
	}
	provenanceRef := fmt.Sprintf("generation:%d", aggregate.Intent.ConfirmedAgainst.Generation)
	if aggregate.Intent.ConfirmedAgainst.Head.Kind == repository.SnapshotCommit {
		provenanceRef = fmt.Sprintf("target:head:%s:base:%s", aggregate.Intent.ConfirmedAgainst.Head.ObjectID, aggregate.Intent.ConfirmedAgainst.Base.ObjectID)
	}
	return DiscussionTurnProvenance{Mode: DiscussionModeProposal, SourceCaptureID: domain.CaptureID(captureID), SourceSnapshotRef: provenanceRef, ContextHash: DiscussionPromptHash(prompt), ManifestHash: baselineHash, CapabilityPolicyVersion: CurrentCapabilityPolicyVersion, ResourcePolicyVersion: CurrentResourcePolicyVersion, EvidenceVersion: CurrentCapabilityEvidenceVersion, PermissionVersion: proposalPermissionVersion(permissions), ProposalID: command.ProposalID, WorkspaceID: aggregate.Workspace.ID, IntentID: aggregate.Intent.ID}
}

func proposalPermissionVersion(policy provider.TurnPermissionPolicy) string {
	if policy.RuntimeApprovals == provider.RuntimeApprovalsExplicit {
		return "proposal-permissions-v1-explicit"
	}
	return "proposal-permissions-v1"
}

func providerConversationIDPointer(value domain.ProviderConversationID) *domain.ProviderConversationID {
	copyValue := value
	return &copyValue
}

func providerProposalIDPointer(value domain.ProposalID) *domain.ProposalID {
	copyValue := value
	return &copyValue
}

func providerTurnIDPointer(value domain.ProviderTurnID) *domain.ProviderTurnID {
	copyValue := value
	return &copyValue
}

func providerTurnID(value *domain.ProviderTurnID) domain.ProviderTurnID {
	if value == nil {
		return ""
	}
	return *value
}

func proposalFailureCode(outcome ProposalTurnOutcome) string {
	switch outcome {
	case ProposalTurnCancelled:
		return proposalTurnCancelled
	case ProposalTurnDisconnected:
		return proposalTurnDisconnected
	default:
		return proposalTurnFailureCode
	}
}

func proposalOutcomeReason(outcome ProposalTurnOutcome) string {
	switch outcome {
	case ProposalTurnCancelled:
		return proposalTurnCancelled
	case ProposalTurnDisconnected:
		return proposalTurnDisconnected
	default:
		return proposalTurnFailedReason
	}
}

func providerTurnFailureReason(err error) string {
	if err == nil {
		return proposalTurnFailedReason
	}
	return boundedProposalReason(err.Error())
}

func boundedProposalReason(value string) string {
	if value == "" {
		return proposalTurnFailedReason
	}
	if len([]byte(value)) > 256 || !utf8.ValidString(value) {
		return proposalTurnFailedReason
	}
	return value
}

var _ Command = RequestProposal{}
var _ Command = CancelProposal{}
