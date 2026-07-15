package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrApplyExecutionInvalid      = errors.New("invalid apply execution")
	ErrApplyMutationFailed        = errors.New("proposal mutation failed")
	ErrApplyVerificationFailed    = errors.New("proposal result verification failed")
	ErrApplyJournalUnavailable    = errors.New("apply journal unavailable")
	ErrApplyRetryAuthorization    = errors.New("retry requires explicit authorization")
	ErrApplyExecutionAlreadyEnded = errors.New("apply execution already ended")
)

// ApplyVerificationEvidenceVersion identifies the persisted post-mutation
// evidence contract.
const ApplyVerificationEvidenceVersion uint32 = 1

// ApplyFailureCode is the bounded, persisted classification reason for a
// non-success terminal operation. It never contains process stderr or paths.
type ApplyFailureCode string

const (
	ApplyFailureNone                    ApplyFailureCode = ""
	ApplyFailureMutationClean           ApplyFailureCode = "mutation_failed_clean"
	ApplyFailureVerificationUnavailable ApplyFailureCode = "verification_unavailable"
	ApplyFailureMixedState              ApplyFailureCode = "mixed_state"
	ApplyFailureUnexpectedPath          ApplyFailureCode = "unexpected_path"
	ApplyFailureIndexChanged            ApplyFailureCode = "index_changed"
	ApplyFailureGlobalChanged           ApplyFailureCode = "global_changed"
	ApplyFailureUnsupported             ApplyFailureCode = "unsupported"
	ApplyFailureRetrySafe               ApplyFailureCode = "retry_safe"
)

func (c ApplyFailureCode) Validate() error {
	switch c {
	case ApplyFailureNone, ApplyFailureMutationClean, ApplyFailureVerificationUnavailable, ApplyFailureMixedState, ApplyFailureUnexpectedPath, ApplyFailureIndexChanged, ApplyFailureGlobalChanged, ApplyFailureUnsupported, ApplyFailureRetrySafe:
		return nil
	default:
		return ErrInvalidApplyPreflight
	}
}

// ApplyObservationState is the verifier's bounded classification of the
// touched destination after a mutation or restart.
type ApplyObservationState string

const (
	ApplyObservationBaseline  ApplyObservationState = "baseline"
	ApplyObservationResult    ApplyObservationState = "result"
	ApplyObservationMixed     ApplyObservationState = "mixed"
	ApplyObservationUncertain ApplyObservationState = "uncertain"
)

func (s ApplyObservationState) Validate() error {
	switch s {
	case ApplyObservationBaseline, ApplyObservationResult, ApplyObservationMixed, ApplyObservationUncertain:
		return nil
	default:
		return ErrApplyVerificationFailed
	}
}

// ApplyPathEvidence is one no-follow, independently observed touched path.
// An absent path carries no kind, mode, content, or link identity.
type ApplyPathEvidence struct {
	Path              repository.RepoPath
	Exists            bool
	Kind              repository.FileKind
	Mode              uint32
	ContentHash       string
	SymlinkTargetHash string
	NativeAlias       *repository.NativeAliasEvidence
}

func (p ApplyPathEvidence) Validate() error {
	if p.Path.Validate() != nil {
		return ErrApplyVerificationFailed
	}
	precondition := repository.PathPrecondition{Path: p.Path, MustExist: p.Exists, Kind: p.Kind, Mode: p.Mode, ContentHash: p.ContentHash, SymlinkTargetHash: p.SymlinkTargetHash, NativeAlias: p.NativeAlias}
	if precondition.Validate() != nil {
		return ErrApplyVerificationFailed
	}
	return nil
}

func (p ApplyPathEvidence) precondition() repository.PathPrecondition {
	return repository.PathPrecondition{Path: p.Path, MustExist: p.Exists, Kind: p.Kind, Mode: p.Mode, ContentHash: p.ContentHash, SymlinkTargetHash: p.SymlinkTargetHash, NativeAlias: p.NativeAlias}
}

// ApplyVerificationEvidence is the bounded post-mutation or recovery
// observation. It is independent of Git's exit status and provider prose.
type ApplyVerificationEvidence struct {
	Version                 uint32
	OperationID             domain.OperationID
	RepositoryID            domain.RepositoryID
	WorktreeID              domain.WorktreeID
	TargetKind              repository.TargetKind
	State                   ApplyObservationState
	Paths                   []ApplyPathEvidence
	UnexpectedPaths         []repository.RepoPath
	Head                    repository.ObjectID
	BranchName              string
	Detached                bool
	WorkingTreeFingerprint  string
	Index                   repository.LocalCaptureIndexEvidence
	ConversionPolicyVersion uint32
	ConversionFingerprint   string
	AttributesChanged       bool
	Capability              ApplyCapabilityEvidence
	GlobalFingerprint       string
	ObservedAt              time.Time
	EvidenceHash            string
}

func (e ApplyVerificationEvidence) Validate() error {
	if e.Version != ApplyVerificationEvidenceVersion || e.OperationID == "" || e.RepositoryID == "" || e.WorktreeID == "" || e.TargetKind != repository.TargetLocal && e.TargetKind != repository.TargetCommit && e.TargetKind != repository.TargetBranch || e.State.Validate() != nil || e.Index.Validate() != nil || e.Capability.Validate() != nil || e.ObservedAt.IsZero() || !validSHA256(e.WorkingTreeFingerprint) || e.ConversionPolicyVersion == 0 || !validSHA256(e.ConversionFingerprint) || !validSHA256(e.GlobalFingerprint) || !validSHA256(e.EvidenceHash) {
		return ErrApplyVerificationFailed
	}
	if e.Head != "" {
		if _, err := repository.NewObjectID(string(e.Head)); err != nil {
			return ErrApplyVerificationFailed
		}
	}
	if e.BranchName != "" && !safeText(e.BranchName) {
		return ErrApplyVerificationFailed
	}
	seen := make(map[repository.RepoPathKey]struct{}, len(e.Paths))
	for _, path := range e.Paths {
		if path.Validate() != nil || hasApplyPath(seen, path.Path) {
			return ErrApplyVerificationFailed
		}
		seen[path.Path.Key()] = struct{}{}
	}
	for _, path := range e.UnexpectedPaths {
		if path.Validate() != nil || hasApplyPath(seen, path) {
			return ErrApplyVerificationFailed
		}
		seen[path.Key()] = struct{}{}
	}
	if e.EvidenceHash != applyVerificationEvidenceHash(e) {
		return ErrApplyVerificationFailed
	}
	return nil
}

func hasApplyPath(seen map[repository.RepoPathKey]struct{}, path repository.RepoPath) bool {
	_, ok := seen[path.Key()]
	return ok
}

func applyVerificationEvidenceHash(value ApplyVerificationEvidence) string {
	value.ObservedAt = time.Time{}
	value.EvidenceHash = ""
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ApplyPatchMutationRequest is the semantic T069 mutation request. It shares
// the exact persisted patch and policy fields with the non-mutating check.
type ApplyPatchMutationRequest = ApplyPatchCheckRequest

// ApplyPatchMutator owns the sole Git mutation invocation for T113.
type ApplyPatchMutator interface {
	Mutate(context.Context, ApplyPatchMutationRequest) error
}

// ApplyResultVerificationRequest identifies the immutable operation and
// proposal whose destination result must be independently observed.
type ApplyResultVerificationRequest struct {
	Operation  ApplyOperation
	Proposal   review.ProposedPatch
	Repository repository.Repository
	Worktree   repository.WorktreeRef
}

func (r ApplyResultVerificationRequest) Validate() error {
	if r.Operation.Validate() != nil || r.Proposal.Validate() != nil || r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.Operation.ProposalID != r.Proposal.ProposalID || r.Operation.ProposalVersion != r.Proposal.Version || r.Operation.ProposalPatchSHA256 != r.Proposal.PatchSHA256 || r.Operation.Evidence.RepositoryID != r.Repository.ID || r.Operation.Evidence.WorktreeID != r.Worktree.ID {
		return ErrApplyExecutionInvalid
	}
	return nil
}

// ApplyResultVerifier is deliberately separate from the mutating Git
// adapter. It owns no mutation and must report complete touched-path, index,
// target, conversion, and capability evidence.
type ApplyResultVerifier interface {
	Verify(context.Context, ApplyResultVerificationRequest) (ApplyVerificationEvidence, error)
}

// ApplyExecutionRequest is the exact T113 command identity. A lease handed
// from T112 is consumed; a nil lease is reacquired for restart recovery.
type ApplyExecutionRequest struct {
	Guard               SessionWriteGuard
	OperationID         domain.OperationID
	ProposalID          domain.ProposalID
	ProposalVersion     review.ProposalVersionNumber
	ProposalPatchSHA256 string
	Repository          repository.Repository
	Worktree            repository.WorktreeRef
	Lease               ApplyExecutionLease
	AuthorizeRetry      bool
}

func (r ApplyExecutionRequest) Validate() error {
	if r.Guard.Validate() != nil || r.OperationID == "" || r.ProposalID == "" || r.ProposalVersion == 0 || !validSHA256(r.ProposalPatchSHA256) || r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID {
		return ErrApplyExecutionInvalid
	}
	return nil
}

// ApplyExecutionClassification is the deterministic result returned to T041.
type ApplyExecutionClassification string

const (
	ApplyExecutionApplied        ApplyExecutionClassification = "applied"
	ApplyExecutionFailedClean    ApplyExecutionClassification = "failed_clean"
	ApplyExecutionRetrySafe      ApplyExecutionClassification = "retry_safe"
	ApplyExecutionRepairRequired ApplyExecutionClassification = "repair_required"
)

func (c ApplyExecutionClassification) Validate() error {
	switch c {
	case ApplyExecutionApplied, ApplyExecutionFailedClean, ApplyExecutionRetrySafe, ApplyExecutionRepairRequired:
		return nil
	default:
		return ErrApplyExecutionInvalid
	}
}

// ApplyExecutionResult is the bounded handoff from T113 to T041.
type ApplyExecutionResult struct {
	Operation      ApplyOperation
	Classification ApplyExecutionClassification
	Guard          SessionWriteGuard
}

func (r ApplyExecutionResult) Validate() error {
	if r.Operation.Validate() != nil || r.Classification.Validate() != nil || r.Guard.Validate() != nil {
		return ErrApplyExecutionInvalid
	}
	return nil
}

// ApplyExecutionService owns mutation ordering, independent verification,
// durable phase transitions, and recovery classification.
type ApplyExecutionService struct {
	store       ReviewStore
	proposals   ProposalWorkspaceStore
	operations  ApplyOperationStore
	artifacts   ProposalPatchArtifactStore
	patchReader ProposalPatchReader
	locks       ApplyDestinationLockManager
	mutator     ApplyPatchMutator
	verifier    ApplyResultVerifier
	clock       Clock
}

type ApplyExecutionServiceConfig struct {
	Store       ReviewStore
	Proposals   ProposalWorkspaceStore
	Operations  ApplyOperationStore
	Artifacts   ProposalPatchArtifactStore
	PatchReader ProposalPatchReader
	Locks       ApplyDestinationLockManager
	Mutator     ApplyPatchMutator
	Verifier    ApplyResultVerifier
	Clock       Clock
}

func NewApplyExecutionService(config ApplyExecutionServiceConfig) (*ApplyExecutionService, error) {
	if config.Store == nil || config.Proposals == nil || config.Operations == nil || config.Locks == nil || config.Mutator == nil || config.Verifier == nil {
		return nil, ErrApplyExecutionInvalid
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ApplyExecutionService{store: config.Store, proposals: config.Proposals, operations: config.Operations, artifacts: config.Artifacts, patchReader: config.PatchReader, locks: config.Locks, mutator: config.Mutator, verifier: config.Verifier, clock: config.Clock}, nil
}

// Execute consumes one prepared operation and never replays a terminal
// operation. Nonterminal restart states are classified by verification only.
func (s *ApplyExecutionService) Execute(ctx context.Context, request ApplyExecutionRequest) (ApplyExecutionResult, error) {
	if s == nil || ctx == nil || request.Validate() != nil {
		return ApplyExecutionResult{}, ErrApplyExecutionInvalid
	}
	lease := request.Lease
	if lease == nil {
		var err error
		lease, err = s.locks.Acquire(ctx, request.Repository.ID, request.Worktree.ID)
		if err != nil {
			return ApplyExecutionResult{}, err
		}
	}
	defer lease.Close()

	operation, err := s.operations.LoadApplyOperation(ctx, request.OperationID)
	if err != nil {
		return ApplyExecutionResult{}, err
	}
	if err := validateApplyExecutionIdentity(operation, request); err != nil {
		return ApplyExecutionResult{}, err
	}
	if terminalApplyPhase(operation.Phase) {
		return applyExecutionResult(operation, request.Guard)
	}
	patch, err := s.loadProposal(ctx, operation)
	if err != nil {
		return ApplyExecutionResult{}, err
	}

	if operation.Phase == ApplyOperationMutating || operation.Phase == ApplyOperationVerifying {
		return s.recoverNonterminal(ctx, request, operation, patch)
	}
	if operation.Phase == ApplyOperationRetrySafe {
		if !request.AuthorizeRetry {
			return applyExecutionResult(operation, request.Guard, ApplyExecutionRetrySafe)
		}
	}
	patchReader, policyVersion, _, err := (&ApplyPreflightService{artifacts: s.artifacts, patchReader: s.patchReader}).patchStream(ctx, patch)
	if err != nil {
		return ApplyExecutionResult{}, err
	}
	operation.Phase = ApplyOperationMutating
	operation.FailureCode = ApplyFailureNone
	operation.Verification = ApplyVerificationEvidence{}
	operation.CompletedAt = nil
	guard, err := s.transition(ctx, request.Guard, operation)
	if err != nil {
		return ApplyExecutionResult{}, errors.Join(ErrApplyJournalUnavailable, err)
	}
	request.Guard = guard
	mutationErr := s.mutator.Mutate(ctx, ApplyPatchMutationRequest{Repository: request.Repository, Worktree: request.Worktree, PatchSHA256: operation.ProposalPatchSHA256, PatchBytes: operation.ProposalPatchBytes, ApplyPolicyVersion: policyVersion, Patch: patchReader})
	if mutationErr != nil {
		return s.classifyAfterMutation(ctx, request, operation, patch, mutationErr, true)
	}
	operation.Phase = ApplyOperationVerifying
	guard, err = s.transition(ctx, request.Guard, operation)
	if err != nil {
		return ApplyExecutionResult{}, errors.Join(ErrApplyJournalUnavailable, err)
	}
	request.Guard = guard
	return s.verifyAndTerminalize(ctx, request, operation, patch, false)
}

func validateApplyExecutionIdentity(operation ApplyOperation, request ApplyExecutionRequest) error {
	if operation.Validate() != nil || operation.ID != request.OperationID || operation.SessionID != request.Guard.SessionID || operation.ProposalID != request.ProposalID || operation.ProposalVersion != request.ProposalVersion || operation.ProposalPatchSHA256 != request.ProposalPatchSHA256 || operation.Evidence.RepositoryID != request.Repository.ID || operation.Evidence.WorktreeID != request.Worktree.ID {
		return ErrApplyStale
	}
	return nil
}

func terminalApplyPhase(phase ApplyOperationPhase) bool {
	return phase == ApplyOperationApplied || phase == ApplyOperationFailedClean || phase == ApplyOperationRepairRequired
}

func (s *ApplyExecutionService) loadProposal(ctx context.Context, operation ApplyOperation) (review.ProposedPatch, error) {
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, operation.ProposalID)
	if err != nil {
		return review.ProposedPatch{}, err
	}
	if aggregate.Validate() != nil || aggregate.Workspace.SessionID != operation.SessionID || aggregate.Workspace.RepositoryID != operation.Evidence.RepositoryID || aggregate.Workspace.WorktreeID != operation.Evidence.WorktreeID {
		return review.ProposedPatch{}, ErrApplyStale
	}
	patch, ok := proposalVersion(aggregate, operation.ProposalVersion)
	if !ok || patch.Status != review.ProposalVersionReady || patch.PatchSHA256 != operation.ProposalPatchSHA256 || patch.WorkspaceID != operation.WorkspaceID || patch.ThreadID != operation.ThreadID || patch.Artifact != operation.PatchArtifact {
		return review.ProposedPatch{}, ErrApplyStale
	}
	if err := patch.Validate(); err != nil {
		return review.ProposedPatch{}, ErrApplyStale
	}
	return patch, nil
}

func (s *ApplyExecutionService) recoverNonterminal(ctx context.Context, request ApplyExecutionRequest, operation ApplyOperation, patch review.ProposedPatch) (ApplyExecutionResult, error) {
	result, err := s.verifyAndClassify(ctx, request, operation, patch)
	if err != nil {
		return ApplyExecutionResult{}, err
	}
	if result.Classification == ApplyExecutionRetrySafe {
		operation.Phase = ApplyOperationRetrySafe
		operation.FailureCode = ApplyFailureRetrySafe
		operation.Verification = result.Operation.Verification
		guard, transitionErr := s.transition(ctx, request.Guard, operation)
		if transitionErr != nil {
			return ApplyExecutionResult{}, errors.Join(ErrApplyJournalUnavailable, transitionErr)
		}
		result.Operation = operation
		result.Guard = guard
		return result, nil
	}
	return s.persistClassification(ctx, request, operation, result.Operation.Verification, result.Classification, result.Operation.FailureCode)
}

func (s *ApplyExecutionService) classifyAfterMutation(ctx context.Context, request ApplyExecutionRequest, operation ApplyOperation, patch review.ProposedPatch, mutationErr error, cleanFailure bool) (ApplyExecutionResult, error) {
	result, verifyErr := s.verifyAndClassify(ctx, request, operation, patch)
	if verifyErr != nil {
		return ApplyExecutionResult{}, errors.Join(ErrApplyMutationFailed, mutationErr, verifyErr)
	}
	if result.Classification == ApplyExecutionApplied {
		return s.persistClassification(ctx, request, operation, result.Operation.Verification, ApplyExecutionApplied, ApplyFailureNone)
	}
	if cleanFailure && (result.Classification == ApplyExecutionFailedClean || result.Classification == ApplyExecutionRetrySafe) {
		return s.persistClassification(ctx, request, operation, result.Operation.Verification, ApplyExecutionFailedClean, ApplyFailureMutationClean)
	}
	failure := result.Operation.FailureCode
	if failure == ApplyFailureNone {
		failure = ApplyFailureMixedState
	}
	return s.persistClassification(ctx, request, operation, result.Operation.Verification, ApplyExecutionRepairRequired, failure)
}

func (s *ApplyExecutionService) verifyAndTerminalize(ctx context.Context, request ApplyExecutionRequest, operation ApplyOperation, patch review.ProposedPatch, recovery bool) (ApplyExecutionResult, error) {
	result, err := s.verifyAndClassify(ctx, request, operation, patch)
	if err != nil {
		return ApplyExecutionResult{}, err
	}
	if result.Classification == ApplyExecutionRetrySafe && !recovery {
		result.Classification = ApplyExecutionFailedClean
		return s.persistClassification(ctx, request, operation, result.Operation.Verification, result.Classification, ApplyFailureMutationClean)
	}
	return s.persistClassification(ctx, request, operation, result.Operation.Verification, result.Classification, result.Operation.FailureCode)
}

func (s *ApplyExecutionService) verifyAndClassify(ctx context.Context, request ApplyExecutionRequest, operation ApplyOperation, patch review.ProposedPatch) (ApplyExecutionResult, error) {
	evidence, err := s.verifier.Verify(ctx, ApplyResultVerificationRequest{Operation: operation, Proposal: patch, Repository: request.Repository, Worktree: request.Worktree})
	if evidence.Validate() != nil {
		if err != nil {
			return ApplyExecutionResult{}, errors.Join(ErrApplyVerificationFailed, err)
		}
		return ApplyExecutionResult{}, ErrApplyVerificationFailed
	}
	if err != nil {
		return ApplyExecutionResult{Operation: ApplyOperation{Verification: evidence, FailureCode: ApplyFailureVerificationUnavailable}, Classification: ApplyExecutionRepairRequired, Guard: request.Guard}, nil
	}
	classification, failure := classifyApplyVerification(operation, patch, evidence)
	if classification == ApplyExecutionRepairRequired && failure == ApplyFailureNone {
		failure = ApplyFailureMixedState
	}
	return ApplyExecutionResult{Operation: ApplyOperation{Verification: evidence, FailureCode: failure}, Classification: classification, Guard: request.Guard}, nil
}

func (s *ApplyExecutionService) persistClassification(ctx context.Context, request ApplyExecutionRequest, operation ApplyOperation, evidence ApplyVerificationEvidence, classification ApplyExecutionClassification, failure ApplyFailureCode) (ApplyExecutionResult, error) {
	if evidence.Validate() != nil || classification.Validate() != nil {
		return ApplyExecutionResult{}, ErrApplyVerificationFailed
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ApplyExecutionResult{}, ErrApplyExecutionInvalid
	}
	operation.Verification = evidence
	operation.FailureCode = failure
	operation.CompletedAt = &now
	switch classification {
	case ApplyExecutionApplied:
		operation.Phase = ApplyOperationApplied
	case ApplyExecutionFailedClean:
		operation.Phase = ApplyOperationFailedClean
	case ApplyExecutionRepairRequired:
		operation.Phase = ApplyOperationRepairRequired
	default:
		return ApplyExecutionResult{}, ErrApplyExecutionInvalid
	}
	guard, err := s.transition(ctx, request.Guard, operation)
	if err != nil {
		return ApplyExecutionResult{}, errors.Join(ErrApplyJournalUnavailable, err)
	}
	return ApplyExecutionResult{Operation: operation, Classification: classification, Guard: guard}, nil
}

func (s *ApplyExecutionService) transition(ctx context.Context, guard SessionWriteGuard, operation ApplyOperation) (SessionWriteGuard, error) {
	return s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		operationTx, ok := tx.(ApplyOperationStoreTx)
		if !ok {
			return ErrApplyJournalUnavailable
		}
		return operationTx.TransitionApplyOperation(ctx, operation)
	})
}

func applyExecutionResult(operation ApplyOperation, guard SessionWriteGuard, requested ...ApplyExecutionClassification) (ApplyExecutionResult, error) {
	classification := applyClassificationForPhase(operation.Phase)
	if len(requested) != 0 {
		classification = requested[0]
	}
	return ApplyExecutionResult{Operation: operation, Classification: classification, Guard: guard}, nil
}

func applyClassificationForPhase(phase ApplyOperationPhase) ApplyExecutionClassification {
	switch phase {
	case ApplyOperationApplied:
		return ApplyExecutionApplied
	case ApplyOperationFailedClean:
		return ApplyExecutionFailedClean
	case ApplyOperationRetrySafe:
		return ApplyExecutionRetrySafe
	default:
		return ApplyExecutionRepairRequired
	}
}

func classifyApplyVerification(operation ApplyOperation, patch review.ProposedPatch, evidence ApplyVerificationEvidence) (ApplyExecutionClassification, ApplyFailureCode) {
	if evidence.Validate() != nil || evidence.OperationID != operation.ID || evidence.RepositoryID != operation.Evidence.RepositoryID || evidence.WorktreeID != operation.Evidence.WorktreeID || evidence.TargetKind != operation.Destination.TargetKind || evidence.ConversionPolicyVersion != operation.Evidence.ConversionPolicyVersion || evidence.ConversionFingerprint != operation.Evidence.ConversionFingerprint || evidence.AttributesChanged || !reflect.DeepEqual(evidence.Capability, operation.Evidence.Capability) || !reflect.DeepEqual(evidence.Index, operation.Evidence.Index) || evidence.Head != operation.Evidence.Head || evidence.BranchName != operation.Evidence.BranchName || evidence.Detached != operation.Evidence.Detached || len(evidence.UnexpectedPaths) != 0 {
		return ApplyExecutionRepairRequired, ApplyFailureGlobalChanged
	}
	actual := make(map[repository.RepoPathKey]ApplyPathEvidence, len(evidence.Paths))
	for _, path := range evidence.Paths {
		actual[path.Path.Key()] = path
	}
	resultMatches := applyResultPathsMatch(actual, patch.Files)
	baselineMatches := applyBaselinePathsMatch(actual, operation.Preconditions)
	if evidence.State == ApplyObservationResult && resultMatches {
		return ApplyExecutionApplied, ApplyFailureNone
	}
	if evidence.State == ApplyObservationBaseline && baselineMatches && evidence.WorkingTreeFingerprint == operation.Evidence.WorkingTreeFingerprint {
		return ApplyExecutionRetrySafe, ApplyFailureRetrySafe
	}
	return ApplyExecutionRepairRequired, ApplyFailureMixedState
}

func applyResultPathsMatch(actual map[repository.RepoPathKey]ApplyPathEvidence, files []review.ProposedFile) bool {
	expected := make(map[repository.RepoPathKey]applyExpectedPath, len(files))
	for _, file := range files {
		expected[file.Path.Key()] = applyExpectedPath{exists: !file.Deleted, kind: file.Kind, mode: file.Mode, contentHash: file.ContentHash}
		if file.OldPath != nil && !bytes.Equal(file.OldPath.Bytes(), file.Path.Bytes()) {
			expected[(*file.OldPath).Key()] = applyExpectedPath{}
		}
	}
	if len(actual) != len(expected) {
		return false
	}
	for key, want := range expected {
		observed, ok := actual[key]
		if !ok || observed.Exists != want.exists {
			return false
		}
		if !want.exists {
			continue
		}
		observedHash := observed.ContentHash
		if want.kind == repository.FileKindSymlink {
			observedHash = observed.SymlinkTargetHash
		}
		if observed.Kind != want.kind || observed.Mode != want.mode || want.contentHash != "" && observedHash != want.contentHash {
			return false
		}
	}
	return true
}

type applyExpectedPath struct {
	exists      bool
	kind        repository.FileKind
	mode        uint32
	contentHash string
}

func applyBaselinePathsMatch(actual map[repository.RepoPathKey]ApplyPathEvidence, expected []repository.PathPrecondition) bool {
	if len(actual) != len(expected) {
		return false
	}
	observed := make([]repository.PathPrecondition, 0, len(actual))
	for _, path := range actual {
		observed = append(observed, path.precondition())
	}
	return sameApplyPreconditions(observed, expected)
}
