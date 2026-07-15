package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"reflect"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const (
	// ApplyOperationVersion identifies the durable prepared-apply journal.
	ApplyOperationVersion uint32 = 1
	// ApplyEvidenceVersion identifies the destination evidence contract.
	ApplyEvidenceVersion uint32 = 1
)

var (
	ErrInvalidApplyPreflight       = errors.New("invalid apply preflight")
	ErrApplyOperationConflict      = errors.New("apply operation conflict")
	ErrApplyOperationNotFound      = errors.New("apply operation not found")
	ErrApplyStale                  = errors.New("proposal is stale for application")
	ErrApplyUnsupported            = errors.New("proposal application is unsupported")
	ErrApplyPatchCheckFailed       = errors.New("proposal patch check failed")
	ErrApplyPreflightUnavailable   = errors.New("apply preflight unavailable")
	ErrApplyPreflightRace          = errors.New("destination changed during apply preflight")
	ErrApplyOperationNotPrepared   = errors.New("apply operation is not prepared")
	ErrApplyOperationAlreadyClosed = errors.New("apply operation is already terminal")
)

// ApplyOperationPhase is the journal phase owned by the apply workflow.
// T112 writes Prepared; T113 owns every later phase.
type ApplyOperationPhase string

const (
	ApplyOperationPrepared       ApplyOperationPhase = "prepared"
	ApplyOperationExecuting      ApplyOperationPhase = "executing"
	ApplyOperationApplied        ApplyOperationPhase = "applied"
	ApplyOperationFailed         ApplyOperationPhase = "failed"
	ApplyOperationRepairRequired ApplyOperationPhase = "repair_required"
)

func (p ApplyOperationPhase) Validate() error {
	switch p {
	case ApplyOperationPrepared, ApplyOperationExecuting, ApplyOperationApplied, ApplyOperationFailed, ApplyOperationRepairRequired:
		return nil
	default:
		return ErrInvalidApplyPreflight
	}
}

// ApplyCapabilityEvidence proves that the inspected destination is eligible
// for the exact v1 working-tree apply path. Desired policy is not evidence.
type ApplyCapabilityEvidence struct {
	Version                 uint32
	RegisteredSupport       bool
	CanonicalContainment    bool
	NativePathExecutor      bool
	NoUnmergedIndex         bool
	NoUnsupportedIndexFlags bool
	ConversionByteNeutral   bool
}

func (e ApplyCapabilityEvidence) Validate() error {
	if e.Version == 0 || !e.RegisteredSupport || !e.CanonicalContainment || !e.NativePathExecutor || !e.NoUnmergedIndex || !e.NoUnsupportedIndexFlags || !e.ConversionByteNeutral {
		return ErrInvalidApplyPreflight
	}
	return nil
}

// ApplyDestinationEvidence is the complete non-mutating observation used by
// T112. It is hashed without ObservedAt so two observations can be compared
// across the Git check boundary.
type ApplyDestinationEvidence struct {
	Version                 uint32
	RepositoryID            domain.RepositoryID
	WorktreeID              domain.WorktreeID
	TargetKind              repository.TargetKind
	Head                    repository.ObjectID
	BranchName              string
	Detached                bool
	WorkingTreeFingerprint  string
	Index                   repository.LocalCaptureIndexEvidence
	Paths                   []repository.PathPrecondition
	ConversionPolicyVersion uint32
	ConversionFingerprint   string
	AttributesChanged       bool
	Capability              ApplyCapabilityEvidence
	GlobalFingerprint       string
	ObservedAt              time.Time
	EvidenceHash            string
}

func (e ApplyDestinationEvidence) Validate() error {
	if e.Version != ApplyEvidenceVersion || e.RepositoryID == "" || e.WorktreeID == "" || e.TargetKind != repository.TargetLocal && e.TargetKind != repository.TargetCommit && e.TargetKind != repository.TargetBranch || e.Index.Validate() != nil || e.Capability.Validate() != nil || e.ObservedAt.IsZero() || !validSHA256(e.WorkingTreeFingerprint) || !validSHA256(e.ConversionFingerprint) || !validSHA256(e.GlobalFingerprint) || !validSHA256(e.EvidenceHash) {
		return ErrInvalidApplyPreflight
	}
	if e.Head != "" {
		if _, err := repository.NewObjectID(string(e.Head)); err != nil {
			return ErrInvalidApplyPreflight
		}
	}
	if e.BranchName != "" && !utf8.ValidString(e.BranchName) {
		return ErrInvalidApplyPreflight
	}
	if e.ConversionPolicyVersion == 0 {
		return ErrInvalidApplyPreflight
	}
	if err := validateApplyPreconditions(e.Paths); err != nil {
		return err
	}
	if e.EvidenceHash != applyEvidenceHash(e) {
		return ErrInvalidApplyPreflight
	}
	return nil
}

// ApplyOperation is one durable, immutable authority handed from T112 to
// T113. Its lease is intentionally not serializable and is returned beside it.
type ApplyOperation struct {
	Version                 uint32
	ID                      domain.OperationID
	SessionID               domain.ReviewSessionID
	ProposalID              domain.ProposalID
	WorkspaceID             domain.WorkspaceID
	ThreadID                domain.ReviewThreadID
	ProposalVersion         review.ProposalVersionNumber
	IdempotencyKey          string
	ConfirmedReviewRevision uint64
	ExpectedSessionRevision uint64
	ProposalPatchSHA256     string
	ProposalPatchBytes      ByteSize
	Destination             review.DestinationConstraints
	Baseline                review.SnapshotIdentity
	Result                  review.SnapshotIdentity
	Preconditions           []repository.PathPrecondition
	Evidence                ApplyDestinationEvidence
	ApplyPolicyVersion      uint32
	Phase                   ApplyOperationPhase
	CreatedAt               time.Time
	PreparedAt              time.Time
}

func (o ApplyOperation) Validate() error {
	if o.Version != ApplyOperationVersion || o.ID == "" || o.SessionID == "" || o.ProposalID == "" || o.WorkspaceID == "" || o.ThreadID == "" || o.ProposalVersion == 0 || !safeText(o.IdempotencyKey) || o.ConfirmedReviewRevision == 0 || o.ExpectedSessionRevision == 0 || !validSHA256(o.ProposalPatchSHA256) || o.ProposalPatchBytes == 0 || o.Destination.Validate() != nil || o.Baseline.Validate() != nil || o.Result.Validate() != nil || o.Evidence.Validate() != nil || o.ApplyPolicyVersion == 0 || o.Phase.Validate() != nil || o.CreatedAt.IsZero() || o.PreparedAt.IsZero() || o.PreparedAt.Before(o.CreatedAt) {
		return ErrInvalidApplyPreflight
	}
	if o.Evidence.RepositoryID == "" || o.Evidence.WorktreeID != o.Destination.WorktreeID || o.Evidence.TargetKind != o.Destination.TargetKind || (o.Destination.TargetKind == repository.TargetLocal && o.Evidence.WorkingTreeFingerprint != o.Destination.ExpectedWorkingTreeFingerprint) || (o.Destination.TargetKind != repository.TargetLocal && o.Evidence.Head != o.Destination.ExpectedHead) || o.Evidence.Capability.Validate() != nil || o.Evidence.EvidenceHash != applyEvidenceHash(o.Evidence) {
		return ErrInvalidApplyPreflight
	}
	return validateApplyPreconditions(o.Preconditions)
}

// EvidenceHash returns the persisted identity of the complete destination
// observation, excluding its wall-clock observation time.
func (o ApplyOperation) EvidenceHash() string {
	if o.Evidence.Validate() != nil {
		return ""
	}
	return o.Evidence.EvidenceHash
}

// ApplyPreparation is the T112 handoff. T113 must retain the lease until its
// exact mutation and verification phases have classified the result.
type ApplyPreparation struct {
	Operation ApplyOperation
	Lease     ApplyExecutionLease
	Guard     SessionWriteGuard
}

// ApplyExecutionLease is deliberately non-serializable and excludes other
// Nudge apply operations for one canonical destination.
type ApplyExecutionLease interface {
	Close() error
}

// ApplyDestinationLockManager supplies the cross-process destination lock.
type ApplyDestinationLockManager interface {
	Acquire(context.Context, domain.RepositoryID, domain.WorktreeID) (ApplyExecutionLease, error)
}

// ApplyDestinationInspectionRequest identifies the exact destination state
// that must be revalidated before and after Git's patch check.
type ApplyDestinationInspectionRequest struct {
	Repository              repository.Repository
	Worktree                repository.WorktreeRef
	Destination             review.DestinationConstraints
	Preconditions           []repository.PathPrecondition
	ConversionPolicyVersion uint32
	ConversionFingerprint   string
}

func (r ApplyDestinationInspectionRequest) Validate() error {
	if r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || r.Destination.Validate() != nil || r.Destination.WorktreeID != r.Worktree.ID || r.ConversionPolicyVersion == 0 || r.ConversionFingerprint != "" && !validSHA256(r.ConversionFingerprint) {
		return ErrInvalidApplyPreflight
	}
	return validateApplyPreconditions(r.Preconditions)
}

// ApplyDestinationInspector reads only the destination and Git metadata; it
// must not modify the worktree, index, refs, or attributes.
type ApplyDestinationInspector interface {
	Inspect(context.Context, ApplyDestinationInspectionRequest) (ApplyDestinationEvidence, error)
}

// ApplyPatchCheckRequest carries one exact persisted patch stream to T006.
type ApplyPatchCheckRequest struct {
	Repository         repository.Repository
	Worktree           repository.WorktreeRef
	PatchSHA256        string
	PatchBytes         ByteSize
	ApplyPolicyVersion uint32
	Patch              io.Reader
}

func (r ApplyPatchCheckRequest) Validate() error {
	if r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID || !validSHA256(r.PatchSHA256) || r.PatchBytes == 0 || r.ApplyPolicyVersion == 0 || r.Patch == nil {
		return ErrInvalidApplyPreflight
	}
	return nil
}

// ApplyPatchChecker performs only the non-mutating Git apply check.
type ApplyPatchChecker interface {
	Check(context.Context, ApplyPatchCheckRequest) error
}

// ApplyOperationStore is the restart/read boundary for prepared operations.
type ApplyOperationStore interface {
	LoadApplyOperationByKey(context.Context, domain.ReviewSessionID, string) (ApplyOperation, error)
	LoadApplyOperationForProposal(context.Context, domain.ProposalID, review.ProposalVersionNumber) (ApplyOperation, error)
}

// ApplyOperationStoreTx persists one prepared operation under the session
// writer fence. It never performs filesystem or destination mutation.
type ApplyOperationStoreTx interface {
	PrepareApplyOperation(context.Context, ApplyOperation) error
}

// ApplyPreflightRequest is the immutable command input for T112.
type ApplyPreflightRequest struct {
	Guard                   SessionWriteGuard
	OperationID             domain.OperationID
	ProposalID              domain.ProposalID
	ProposalVersion         review.ProposalVersionNumber
	ConfirmedReviewRevision uint64
	IdempotencyKey          string
	Repository              repository.Repository
	Worktree                repository.WorktreeRef
}

func (r ApplyPreflightRequest) Validate() error {
	if r.Guard.Validate() != nil || r.OperationID == "" || r.ProposalID == "" || r.ProposalVersion == 0 || r.ConfirmedReviewRevision == 0 || !safeText(r.IdempotencyKey) || r.Repository.Validate() != nil || r.Worktree.Validate() != nil || r.Worktree.RepositoryID != r.Repository.ID {
		return ErrInvalidApplyPreflight
	}
	return nil
}

// ApplyPreflightService owns validation ordering and the prepared journal.
type ApplyPreflightService struct {
	store       ReviewStore
	proposals   ProposalWorkspaceStore
	operations  ApplyOperationStore
	artifacts   ProposalPatchArtifactStore
	patchReader ProposalPatchReader
	locks       ApplyDestinationLockManager
	inspector   ApplyDestinationInspector
	checker     ApplyPatchChecker
	clock       Clock
}

type ApplyPreflightServiceConfig struct {
	Store       ReviewStore
	Proposals   ProposalWorkspaceStore
	Operations  ApplyOperationStore
	Artifacts   ProposalPatchArtifactStore
	PatchReader ProposalPatchReader
	Locks       ApplyDestinationLockManager
	Inspector   ApplyDestinationInspector
	Checker     ApplyPatchChecker
	Clock       Clock
}

func NewApplyPreflightService(config ApplyPreflightServiceConfig) (*ApplyPreflightService, error) {
	if config.Store == nil || config.Proposals == nil || config.Operations == nil || config.Locks == nil || config.Inspector == nil || config.Checker == nil {
		return nil, ErrApplyPreflightUnavailable
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ApplyPreflightService{store: config.Store, proposals: config.Proposals, operations: config.Operations, artifacts: config.Artifacts, patchReader: config.PatchReader, locks: config.Locks, inspector: config.Inspector, checker: config.Checker, clock: config.Clock}, nil
}

// Prepare revalidates one ready immutable version twice around Git's exact
// check, then persists prepared authority while retaining the destination lock.
func (s *ApplyPreflightService) Prepare(ctx context.Context, request ApplyPreflightRequest) (ApplyPreparation, error) {
	if s == nil || ctx == nil || request.Validate() != nil {
		return ApplyPreparation{}, ErrInvalidApplyPreflight
	}
	lease, err := s.locks.Acquire(ctx, request.Repository.ID, request.Worktree.ID)
	if err != nil {
		return ApplyPreparation{}, err
	}
	keepLease := false
	defer func() {
		if !keepLease {
			_ = lease.Close()
		}
	}()

	if existing, err := s.operations.LoadApplyOperationByKey(ctx, request.Guard.SessionID, request.IdempotencyKey); err == nil {
		if existing.Validate() != nil || existing.ID != request.OperationID || existing.ProposalID != request.ProposalID || existing.ProposalVersion != request.ProposalVersion || existing.ConfirmedReviewRevision != request.ConfirmedReviewRevision || existing.ExpectedSessionRevision > request.Guard.ExpectedRevision || existing.Evidence.RepositoryID != request.Repository.ID || existing.Evidence.WorktreeID != request.Worktree.ID {
			return ApplyPreparation{}, ErrApplyOperationConflict
		}
		if existing.Phase != ApplyOperationPrepared {
			return ApplyPreparation{}, ErrApplyOperationAlreadyClosed
		}
		keepLease = true
		return ApplyPreparation{Operation: existing, Lease: lease, Guard: request.Guard}, nil
	} else if !errors.Is(err, ErrApplyOperationNotFound) {
		return ApplyPreparation{}, err
	}
	if existing, err := s.operations.LoadApplyOperationForProposal(ctx, request.ProposalID, request.ProposalVersion); err == nil {
		if existing.Validate() != nil || existing.Phase != ApplyOperationPrepared || existing.Destination.WorktreeID != request.Worktree.ID {
			return ApplyPreparation{}, ErrApplyOperationConflict
		}
		return ApplyPreparation{}, ErrApplyOperationConflict
	} else if !errors.Is(err, ErrApplyOperationNotFound) {
		return ApplyPreparation{}, err
	}

	aggregate, err := s.proposals.LoadProposalAggregate(ctx, request.ProposalID)
	if err != nil {
		return ApplyPreparation{}, err
	}
	if err := aggregate.Validate(); err != nil || aggregate.Proposal.ID != request.ProposalID || aggregate.Workspace.SessionID != request.Guard.SessionID {
		return ApplyPreparation{}, ErrInvalidApplyPreflight
	}
	if aggregate.Workspace.RepositoryID != request.Repository.ID || aggregate.Workspace.WorktreeID != request.Worktree.ID {
		return ApplyPreparation{}, ErrApplyStale
	}
	patch, ok := proposalVersion(aggregate, request.ProposalVersion)
	if !ok || patch.Status != review.ProposalVersionReady || patch.Version != request.ProposalVersion || patch.Destination.WorktreeID != request.Worktree.ID {
		return ApplyPreparation{}, ErrApplyStale
	}
	if err := validateApplyPatchIdentity(patch); err != nil {
		return ApplyPreparation{}, err
	}

	patchReader, conversionPolicyVersion, conversionFingerprint, err := s.patchStream(ctx, patch)
	if err != nil {
		return ApplyPreparation{}, err
	}
	inspectionRequest := ApplyDestinationInspectionRequest{Repository: request.Repository, Worktree: request.Worktree, Destination: patch.Destination, Preconditions: cloneApplyPreconditions(patch.Preconditions), ConversionPolicyVersion: conversionPolicyVersion, ConversionFingerprint: conversionFingerprint}
	before, err := s.inspector.Inspect(ctx, inspectionRequest)
	if err != nil {
		return ApplyPreparation{}, err
	}
	if err := validateApplyInspection(before, patch, request.Worktree, inspectionRequest); err != nil {
		return ApplyPreparation{}, err
	}
	if err := s.checker.Check(ctx, ApplyPatchCheckRequest{Repository: request.Repository, Worktree: request.Worktree, PatchSHA256: patch.PatchSHA256, PatchBytes: patchBytes(patch), ApplyPolicyVersion: ApplyPolicyVersion(patch), Patch: patchReader}); err != nil {
		if errors.Is(err, ErrApplyPatchCheckFailed) || errors.Is(err, ErrApplyStale) {
			return ApplyPreparation{}, err
		}
		return ApplyPreparation{}, err
	}
	after, err := s.inspector.Inspect(ctx, inspectionRequest)
	if err != nil {
		return ApplyPreparation{}, err
	}
	if err := validateApplyInspection(after, patch, request.Worktree, inspectionRequest); err != nil {
		return ApplyPreparation{}, err
	}
	if before.EvidenceHash != after.EvidenceHash {
		return ApplyPreparation{}, ErrApplyPreflightRace
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ApplyPreparation{}, ErrInvalidApplyPreflight
	}
	operation := ApplyOperation{Version: ApplyOperationVersion, ID: request.OperationID, SessionID: request.Guard.SessionID, ProposalID: patch.ProposalID, WorkspaceID: patch.WorkspaceID, ThreadID: patch.ThreadID, ProposalVersion: patch.Version, IdempotencyKey: request.IdempotencyKey, ConfirmedReviewRevision: request.ConfirmedReviewRevision, ExpectedSessionRevision: request.Guard.ExpectedRevision, ProposalPatchSHA256: patch.PatchSHA256, ProposalPatchBytes: patchBytes(patch), Destination: patch.Destination, Baseline: patch.Baseline, Result: patch.Result, Preconditions: cloneApplyPreconditions(patch.Preconditions), Evidence: after, ApplyPolicyVersion: ApplyPolicyVersion(patch), Phase: ApplyOperationPrepared, CreatedAt: now, PreparedAt: now}
	if err := operation.Validate(); err != nil {
		return ApplyPreparation{}, err
	}
	nextGuard, err := s.store.WithSessionTx(ctx, request.Guard, func(tx ReviewStoreTx) error {
		operationTx, ok := tx.(ApplyOperationStoreTx)
		if !ok {
			return ErrApplyPreflightUnavailable
		}
		return operationTx.PrepareApplyOperation(ctx, operation)
	})
	if err != nil {
		return ApplyPreparation{Operation: operation, Lease: lease, Guard: request.Guard}, err
	}
	keepLease = true
	return ApplyPreparation{Operation: operation, Lease: lease, Guard: nextGuard}, nil
}

func validateApplyPatchIdentity(patch review.ProposedPatch) error {
	if patch.Validate() != nil || patch.Status != review.ProposalVersionReady || len(patch.Preconditions) == 0 {
		return ErrApplyStale
	}
	if patch.Artifact == (review.ProposedPatchArtifactReference{}) {
		if len(patch.PatchBytes) == 0 {
			return ErrApplyUnsupported
		}
		return nil
	}
	if patch.Artifact.PatchSHA256 != patch.PatchSHA256 || patch.Artifact.PatchBytes == 0 || patch.Artifact.PatchFormatVersion == 0 || patch.Artifact.RenamePolicyVersion == 0 || patch.Artifact.ConversionPolicyVersion == 0 {
		return ErrApplyUnsupported
	}
	return nil
}

func validateApplyInspection(evidence ApplyDestinationEvidence, patch review.ProposedPatch, worktree repository.WorktreeRef, request ApplyDestinationInspectionRequest) error {
	if evidence.Validate() != nil || evidence.RepositoryID == "" || evidence.WorktreeID != worktree.ID || evidence.TargetKind != patch.Destination.TargetKind || evidence.Capability.Validate() != nil {
		return ErrApplyUnsupported
	}
	if evidence.ConversionPolicyVersion != request.ConversionPolicyVersion || request.ConversionFingerprint != "" && evidence.ConversionFingerprint != request.ConversionFingerprint {
		return ErrApplyStale
	}
	if evidence.AttributesChanged {
		return ErrApplyUnsupported
	}
	if patch.Destination.TargetKind == repository.TargetLocal && evidence.WorkingTreeFingerprint != patch.Destination.ExpectedWorkingTreeFingerprint || patch.Destination.TargetKind != repository.TargetLocal && evidence.Head != patch.Destination.ExpectedHead {
		return ErrApplyStale
	}
	if !sameApplyPreconditions(evidence.Paths, patch.Preconditions) {
		return ErrApplyStale
	}
	for _, entry := range evidence.Index.Entries {
		if entry.Stage != 0 || len(entry.Flags) != 0 {
			return ErrApplyUnsupported
		}
	}
	return nil
}

func (s *ApplyPreflightService) patchStream(ctx context.Context, patch review.ProposedPatch) (io.Reader, uint32, string, error) {
	if patch.Artifact == (review.ProposedPatchArtifactReference{}) {
		digest := sha256.Sum256(patch.PatchBytes)
		if uint64(len(patch.PatchBytes)) != uint64(patchBytes(patch)) || hex.EncodeToString(digest[:]) != patch.PatchSHA256 {
			return nil, 1, "", ErrApplyStale
		}
		return bytes.NewReader(patch.PatchBytes), 1, "", nil
	}
	if s.artifacts == nil || s.patchReader == nil {
		return nil, 0, "", ErrApplyPreflightUnavailable
	}
	artifact, err := s.artifacts.LoadProposalPatchArtifactForAttempt(ctx, patch.AttemptID)
	if err != nil {
		return nil, 0, "", err
	}
	if artifact.Validate() != nil || artifact.ID != patch.Artifact.ArtifactID || artifact.ProposalID != patch.ProposalID || artifact.WorkspaceID != patch.WorkspaceID || artifact.AttemptID != patch.AttemptID || artifact.PatchSHA256 != patch.PatchSHA256 || uint64(artifact.Published.Identity.Bytes) != patch.Artifact.PatchBytes || artifact.Published.Identity.SpoolID != patch.Artifact.SpoolID || artifact.Index.Hash != patch.Artifact.IndexHash || uint64(artifact.Summary.FileCount) != patch.Artifact.FileCount || uint64(artifact.Summary.HunkCount) != patch.Artifact.HunkCount || uint64(artifact.Summary.RowCount) != patch.Artifact.RowCount || uint64(artifact.Summary.BinaryFiles) != patch.Artifact.BinaryFiles {
		return nil, 0, "", ErrApplyStale
	}
	if artifact.Published.Identity.ManifestHash != patch.Artifact.ManifestHash || artifact.PatchFormatVersion != patch.Artifact.PatchFormatVersion || artifact.RenamePolicyVersion != patch.Artifact.RenamePolicyVersion || artifact.ConversionPolicyVersion != patch.Artifact.ConversionPolicyVersion {
		return nil, 0, "", ErrApplyStale
	}
	return &proposalPatchRangeReader{ctx: ctx, reader: s.patchReader, request: ProposalPatchRangeRequest{ArtifactID: artifact.ID, Published: artifact.Published, PatchSHA256: artifact.PatchSHA256, PatchBytes: artifact.Published.Identity.Bytes}, hasher: sha256.New()}, artifact.ConversionPolicyVersion, artifact.ConversionFingerprint, nil
}

type proposalPatchRangeReader struct {
	ctx      context.Context
	reader   ProposalPatchReader
	request  ProposalPatchRangeRequest
	offset   ByteSize
	buffer   []byte
	hasher   hash.Hash
	verified bool
}

func (r *proposalPatchRangeReader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 && r.offset < r.request.PatchBytes {
		max := ByteSize(len(output))
		if max > ProposalPatchRangeBytes {
			max = ProposalPatchRangeBytes
		}
		request := r.request
		request.Offset, request.MaxBytes = r.offset, max
		value, err := r.reader.ReadProposalPatchRange(r.ctx, request)
		if err != nil {
			return 0, err
		}
		if value.Validate(request) != nil || value.Offset != r.offset || value.Length == 0 {
			return 0, ErrApplyStale
		}
		r.buffer = append(r.buffer[:0], value.Bytes...)
		r.offset += value.Length
	}
	if len(r.buffer) == 0 {
		if !r.verified {
			if hex.EncodeToString(r.hasher.Sum(nil)) != r.request.PatchSHA256 {
				return 0, ErrApplyStale
			}
			r.verified = true
		}
		return 0, io.EOF
	}
	n := copy(output, r.buffer)
	r.buffer = r.buffer[n:]
	_, _ = r.hasher.Write(output[:n])
	return n, nil
}

func patchBytes(patch review.ProposedPatch) ByteSize {
	if patch.Artifact != (review.ProposedPatchArtifactReference{}) {
		return ByteSize(patch.Artifact.PatchBytes)
	}
	return ByteSize(len(patch.PatchBytes))
}

func ApplyPolicyVersion(patch review.ProposedPatch) uint32 {
	return 1
}

func validateApplyPreconditions(values []repository.PathPrecondition) error {
	seen := make(map[repository.RepoPathKey]struct{}, len(values))
	for _, value := range values {
		if value.Validate() != nil {
			return ErrInvalidApplyPreflight
		}
		if _, exists := seen[value.Path.Key()]; exists {
			return ErrInvalidApplyPreflight
		}
		seen[value.Path.Key()] = struct{}{}
	}
	return nil
}

func cloneApplyPreconditions(values []repository.PathPrecondition) []repository.PathPrecondition {
	result := make([]repository.PathPrecondition, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Path = repository.RepoPath(value.Path.Bytes())
		if value.NativeAlias != nil {
			alias := *value.NativeAlias
			result[index].NativeAlias = &alias
		}
	}
	return result
}

func sameApplyPreconditions(left, right []repository.PathPrecondition) bool {
	if len(left) != len(right) {
		return false
	}
	copyLeft := cloneApplyPreconditions(left)
	copyRight := cloneApplyPreconditions(right)
	sort.Slice(copyLeft, func(i, j int) bool { return string(copyLeft[i].Path) < string(copyLeft[j].Path) })
	sort.Slice(copyRight, func(i, j int) bool { return string(copyRight[i].Path) < string(copyRight[j].Path) })
	return reflect.DeepEqual(copyLeft, copyRight)
}

func applyEvidenceHash(value ApplyDestinationEvidence) string {
	value.ObservedAt = time.Time{}
	value.EvidenceHash = ""
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
