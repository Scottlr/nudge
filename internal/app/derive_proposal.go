package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const proposalPatchFormat = "git-binary-v1"

var (
	ErrProposalPublicationInvalid       = errors.New("invalid proposal publication evidence")
	ErrProposalPublicationNotReady      = errors.New("proposal publication evidence is not ready")
	ErrProposalPublicationStale         = errors.New("proposal destination is stale")
	ErrProposalPublicationConflict      = errors.New("proposal publication conflict")
	ErrProposalPublicationNoChanges     = errors.New("proposal has no changes")
	ErrProposalPublicationResetRequired = errors.New("proposal reset verification is required")
)

// ProposalDerivationInput is the immutable T110/T111 evidence plus the
// destination observation used to derive one approvable proposal version.
// DestinationPreconditions may contain unrelated paths; only touched paths
// are compared so unrelated user dirt remains valid.
type ProposalDerivationInput struct {
	Aggregate                review.ProposalAggregate
	AttemptID                domain.OperationID
	Snapshot                 ResultSnapshot
	Artifact                 ProposalPatchArtifact
	Destination              review.DestinationConstraints
	DestinationPreconditions []repository.PathPrecondition
	CreatedAt                time.Time
}

// DeriveProposal converts complete immutable result evidence into an
// artifact-backed proposal version. It never reads a mutable workspace or
// copies the patch bytes into the returned proposal.
func DeriveProposal(input ProposalDerivationInput) (review.ProposedPatch, error) {
	if input.Aggregate.Validate() != nil || input.AttemptID == "" || input.Snapshot.Validate() != nil || input.Destination.Validate() != nil || input.CreatedAt.IsZero() {
		return review.ProposedPatch{}, ErrProposalPublicationInvalid
	}
	attempt, ok := proposalAttempt(input.Aggregate, input.AttemptID)
	if !ok || attempt.Outcome != review.ProposalAttemptDeriving {
		return review.ProposedPatch{}, ErrProposalPublicationConflict
	}
	if err := validatePublicationIdentity(input.Aggregate, attempt, input.Snapshot); err != nil {
		return review.ProposedPatch{}, err
	}
	if input.Snapshot.State != ResultSnapshotReady {
		return review.ProposedPatch{}, ErrProposalPublicationNotReady
	}
	if len(input.Snapshot.Delta.Entries) == 0 {
		return review.ProposedPatch{}, ErrProposalPublicationNoChanges
	}
	if input.Artifact.Validate() != nil {
		return review.ProposedPatch{}, ErrProposalPublicationNotReady
	}
	if err := validateArtifactIdentity(input.Artifact, input.Snapshot, attempt); err != nil {
		return review.ProposedPatch{}, err
	}
	if err := artifactCoversDelta(input.Artifact, input.Snapshot.Delta); err != nil {
		return review.ProposedPatch{}, err
	}
	preconditions, err := deriveDestinationPreconditions(input.Snapshot.Delta, input.Artifact.Index.Files, input.DestinationPreconditions)
	if err != nil {
		return review.ProposedPatch{}, err
	}
	files, err := deriveProposedFiles(input.Artifact.Index.Files, input.Snapshot)
	if err != nil {
		return review.ProposedPatch{}, err
	}
	version, err := nextProposalVersion(input.Aggregate)
	if err != nil {
		return review.ProposedPatch{}, err
	}
	broader := broaderProposalScope(input.Snapshot.Delta, input.Aggregate.Intent.ExpectedPaths)
	scope := review.ProposalScopeFocused
	scopeReason := "within confirmed request scope"
	if broader {
		scope = review.ProposalScopeBroader
		scopeReason = "derived patch includes paths outside the confirmed request scope"
	}
	ref := review.ProposedPatchArtifactReference{
		ArtifactID:              input.Artifact.ID,
		SpoolID:                 input.Artifact.Published.Identity.SpoolID,
		ManifestHash:            input.Artifact.Published.Identity.ManifestHash,
		PatchFormatVersion:      input.Artifact.PatchFormatVersion,
		RenamePolicyVersion:     input.Artifact.RenamePolicyVersion,
		ConversionPolicyVersion: input.Artifact.ConversionPolicyVersion,
		PatchSHA256:             input.Artifact.PatchSHA256,
		PatchBytes:              uint64(input.Artifact.Summary.PatchBytes),
		IndexHash:               input.Artifact.Index.Hash,
		FileCount:               uint64(input.Artifact.Summary.FileCount),
		HunkCount:               uint64(input.Artifact.Summary.HunkCount),
		RowCount:                uint64(input.Artifact.Summary.RowCount),
		BinaryFiles:             uint64(input.Artifact.Summary.BinaryFiles),
	}
	patch := review.ProposedPatch{
		ProposalID:       input.Aggregate.Proposal.ID,
		WorkspaceID:      input.Aggregate.Workspace.ID,
		ThreadID:         input.Aggregate.Proposal.ThreadID,
		AttemptID:        attempt.ID,
		ProviderTurnRef:  input.Snapshot.ProviderTurnRef,
		SourceGeneration: attempt.SourceGeneration,
		Baseline:         input.Snapshot.Baseline,
		Result:           input.Snapshot.Result,
		Destination:      input.Destination,
		Version:          version,
		PatchFormat:      proposalPatchFormat,
		PatchSHA256:      input.Artifact.PatchSHA256,
		Artifact:         ref,
		Files:            files,
		Preconditions:    preconditions,
		Scope:            scope,
		ScopeReason:      scopeReason,
		Status:           review.ProposalVersionReady,
		StatusReason:     "complete immutable patch and preconditions verified",
		CreatedAt:        input.CreatedAt.UTC(),
	}
	created, err := review.NewProposedPatch(patch)
	if err != nil {
		return review.ProposedPatch{}, ErrProposalPublicationInvalid
	}
	return created, nil
}

// ProposalBaselineResetRequest identifies the exact T035 reset operation that
// must be verified before a zero-delta attempt can become no_changes.
type ProposalBaselineResetRequest struct {
	SessionID        domain.ReviewSessionID
	ProposalID       domain.ProposalID
	WorkspaceID      domain.WorkspaceID
	AttemptID        domain.OperationID
	OperationID      domain.OperationID
	Baseline         review.SnapshotIdentity
	BaselineManifest WorkspaceManifest
}

func (r ProposalBaselineResetRequest) Validate() error {
	if r.SessionID == "" || r.ProposalID == "" || r.WorkspaceID == "" || r.AttemptID == "" || r.Baseline.Validate() != nil && r.BaselineManifest.Validate() != nil {
		return ErrProposalPublicationInvalid
	}
	return nil
}

// ProposalBaselineResetter is T035's verified filesystem reset boundary. A
// nil return means the reset and independent post-reset verification finished.
type ProposalBaselineResetter interface {
	ResetToBaseline(context.Context, ProposalBaselineResetRequest) error
}

// ProposalPublicationRequest carries the session fence and destination
// observation for one explicit T038 publication attempt.
type ProposalPublicationRequest struct {
	Guard                    SessionWriteGuard
	ProposalID               domain.ProposalID
	AttemptID                domain.OperationID
	Destination              review.DestinationConstraints
	DestinationPreconditions []repository.PathPrecondition
}

// ProposalPublicationCommit is the durable result of T038. NoChanges is true
// only after the resetter has returned verified success.
type ProposalPublicationCommit struct {
	Guard     SessionWriteGuard
	Patch     *review.ProposedPatch
	Attempt   review.ProposalAttempt
	NoChanges bool
}

// ProposalPublicationService owns the application orchestration around the
// immutable stores and the T035 reset boundary.
type ProposalPublicationService struct {
	store     ReviewStore
	proposals ProposalWorkspaceStore
	snapshots ResultSnapshotStore
	artifacts ProposalPatchArtifactStore
	resetter  ProposalBaselineResetter
	clock     Clock
}

// ProposalPublicationServiceConfig composes T038's durable and reset ports.
type ProposalPublicationServiceConfig struct {
	Store     ReviewStore
	Proposals ProposalWorkspaceStore
	Snapshots ResultSnapshotStore
	Artifacts ProposalPatchArtifactStore
	Resetter  ProposalBaselineResetter
	Clock     Clock
}

// NewProposalPublicationService validates the T038 application composition.
func NewProposalPublicationService(config ProposalPublicationServiceConfig) (*ProposalPublicationService, error) {
	if config.Store == nil || config.Proposals == nil || config.Snapshots == nil || config.Artifacts == nil {
		return nil, ErrProposalPublicationInvalid
	}
	if config.Clock == nil {
		config.Clock = SystemClock{}
	}
	return &ProposalPublicationService{store: config.Store, proposals: config.Proposals, snapshots: config.Snapshots, artifacts: config.Artifacts, resetter: config.Resetter, clock: config.Clock}, nil
}

// Publish derives and atomically publishes a version, or completes the
// verified zero-delta reset path without creating an empty version.
func (s *ProposalPublicationService) Publish(ctx context.Context, request ProposalPublicationRequest) (ProposalPublicationCommit, error) {
	if s == nil || ctx == nil || request.Guard.Validate() != nil || request.ProposalID == "" || request.AttemptID == "" || request.Destination.Validate() != nil {
		return ProposalPublicationCommit{}, ErrProposalPublicationInvalid
	}
	aggregate, err := s.proposals.LoadProposalAggregate(ctx, request.ProposalID)
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	snapshot, err := s.snapshots.LoadResultSnapshotForAttempt(ctx, request.AttemptID)
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	if snapshot.ProposalID != request.ProposalID || snapshot.AttemptID != request.AttemptID {
		return ProposalPublicationCommit{}, ErrProposalPublicationInvalid
	}
	attempt, ok := proposalAttempt(aggregate, request.AttemptID)
	if !ok {
		return ProposalPublicationCommit{}, ErrProposalPublicationNotReady
	}
	if err := validatePublicationIdentity(aggregate, attempt, snapshot); err != nil {
		return ProposalPublicationCommit{}, err
	}
	if attempt.Outcome != review.ProposalAttemptDeriving {
		return ProposalPublicationCommit{}, ErrProposalPublicationConflict
	}
	if len(snapshot.Delta.Entries) == 0 {
		if _, loadErr := s.artifacts.LoadProposalPatchArtifactForAttempt(ctx, request.AttemptID); loadErr == nil {
			return ProposalPublicationCommit{}, ErrProposalPublicationInvalid
		} else if !errors.Is(loadErr, ErrProposalPatchArtifactNotFound) {
			return ProposalPublicationCommit{}, loadErr
		}
		return s.publishNoChanges(ctx, request, aggregate, attempt, snapshot)
	}
	artifact, err := s.artifacts.LoadProposalPatchArtifactForAttempt(ctx, request.AttemptID)
	if err != nil {
		if errors.Is(err, ErrProposalPatchArtifactNotFound) {
			return ProposalPublicationCommit{}, ErrProposalPublicationNotReady
		}
		return ProposalPublicationCommit{}, err
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalPublicationCommit{}, ErrProposalPublicationInvalid
	}
	patch, err := DeriveProposal(ProposalDerivationInput{Aggregate: aggregate, AttemptID: request.AttemptID, Snapshot: snapshot, Artifact: artifact, Destination: request.Destination, DestinationPreconditions: request.DestinationPreconditions, CreatedAt: now})
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	guard, err := s.store.WithSessionTx(ctx, request.Guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalPublicationInvalid
		}
		return proposalTx.PublishProposal(ctx, patch)
	})
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	return ProposalPublicationCommit{Guard: guard, Patch: &patch, Attempt: publishedAttempt(attempt, patch)}, nil
}

func (s *ProposalPublicationService) publishNoChanges(ctx context.Context, request ProposalPublicationRequest, aggregate review.ProposalAggregate, attempt review.ProposalAttempt, snapshot ResultSnapshot) (ProposalPublicationCommit, error) {
	if s.resetter == nil || snapshot.State != ResultSnapshotReady || snapshot.Delta.Validate() != nil {
		return ProposalPublicationCommit{}, ErrProposalPublicationResetRequired
	}
	now := s.clock.Now().UTC()
	if now.IsZero() {
		return ProposalPublicationCommit{}, ErrProposalPublicationInvalid
	}
	resetting := attempt
	resetting.Outcome = review.ProposalAttemptNoChangesResetting
	resetting.ResultDisposition = review.ProposalResultDiscarding
	resetting.Reason = "verified zero delta reset in progress"
	guard, err := s.store.WithSessionTx(ctx, request.Guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalPublicationInvalid
		}
		return proposalTx.RecordProposalAttempt(ctx, resetting)
	})
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	resetRequest := ProposalBaselineResetRequest{SessionID: guard.SessionID, ProposalID: aggregate.Proposal.ID, WorkspaceID: aggregate.Workspace.ID, AttemptID: attempt.ID, Baseline: snapshot.Baseline}
	if err := resetRequest.Validate(); err != nil {
		return ProposalPublicationCommit{}, err
	}
	if err := s.resetter.ResetToBaseline(ctx, resetRequest); err != nil {
		failed := resetting
		failed.Outcome = review.ProposalAttemptFailed
		failed.ResultDisposition = review.ProposalResultPresent
		failed.FailurePhase = review.ProposalFailureReset
		failed.Reason = "proposal baseline reset failed"
		failedAt := s.clock.Now().UTC()
		failed.FinishedAt = &failedAt
		_, recordErr := s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
			proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
			if !ok {
				return ErrProposalPublicationInvalid
			}
			return proposalTx.RecordProposalAttempt(ctx, failed)
		})
		return ProposalPublicationCommit{}, errors.Join(err, recordErr)
	}
	completed := resetting
	completed.Outcome = review.ProposalAttemptNoChanges
	completed.ResultDisposition = review.ProposalResultDiscarded
	completed.Baseline = snapshotIdentityPointer(snapshot.Baseline)
	completed.Result = snapshotIdentityPointer(snapshot.Result)
	completed.Reason = "verified no changes after baseline reset"
	completed.FinishedAt = &now
	guard, err = s.store.WithSessionTx(ctx, guard, func(tx ReviewStoreTx) error {
		proposalTx, ok := tx.(ProposalWorkspaceStoreTx)
		if !ok {
			return ErrProposalPublicationInvalid
		}
		return proposalTx.RecordNoChanges(ctx, completed)
	})
	if err != nil {
		return ProposalPublicationCommit{}, err
	}
	return ProposalPublicationCommit{Guard: guard, Attempt: completed, NoChanges: true}, nil
}

func validatePublicationIdentity(aggregate review.ProposalAggregate, attempt review.ProposalAttempt, snapshot ResultSnapshot) error {
	if snapshot.SessionID != aggregate.Workspace.SessionID || snapshot.ProposalID != aggregate.Proposal.ID || snapshot.WorkspaceID != aggregate.Workspace.ID || snapshot.ThreadID != aggregate.Proposal.ThreadID || snapshot.WorktreeID != aggregate.Workspace.WorktreeID || !sameGenerationProvenance(attempt.SourceGeneration, aggregate.Intent.ConfirmedAgainst) {
		return ErrProposalPublicationInvalid
	}
	if attempt.Baseline != nil && *attempt.Baseline != snapshot.Baseline || attempt.Result != nil && *attempt.Result != snapshot.Result {
		return ErrProposalPublicationConflict
	}
	return nil
}

func validateArtifactIdentity(artifact ProposalPatchArtifact, snapshot ResultSnapshot, attempt review.ProposalAttempt) error {
	if artifact.SessionID != snapshot.SessionID || artifact.ProposalID != snapshot.ProposalID || artifact.WorkspaceID != snapshot.WorkspaceID || artifact.AttemptID != snapshot.AttemptID || artifact.ThreadID != snapshot.ThreadID || artifact.Baseline != snapshot.Baseline || artifact.Result != snapshot.Result || artifact.BaselineSnapshotID != snapshot.Baseline.ID || artifact.ResultSnapshotID != snapshot.ID || attempt.SourceGeneration.SessionID != snapshot.SessionID {
		return ErrProposalPublicationInvalid
	}
	return nil
}

func artifactCoversDelta(artifact ProposalPatchArtifact, delta ResultDelta) error {
	byPath := make(map[repository.RepoPathKey]ResultDeltaEntry, len(delta.Entries))
	for _, entry := range delta.Entries {
		if entry.Validate() != nil || !entry.Complete || entry.Reason != ResultReasonNone {
			return ErrProposalPublicationNotReady
		}
		byPath[entry.Path.Key()] = entry
	}
	covered := make(map[repository.RepoPathKey]struct{}, len(byPath))
	for _, indexed := range artifact.Index.Files {
		file := indexed.File
		if file.Validate() != nil {
			return ErrProposalPublicationInvalid
		}
		if file.Kind == repository.ChangeRenamed || file.Kind == repository.ChangeCopied {
			if !renamePatchMatchesDelta(delta, file, artifact.RenamePolicyVersion) {
				return ErrProposalPublicationInvalid
			}
		}
		if file.OldPath != nil {
			entry, ok := byPath[file.OldPath.Key()]
			if !ok {
				if file.Kind != repository.ChangeCopied {
					return ErrProposalPublicationInvalid
				}
			} else if entry.Baseline == nil || entry.Baseline.Kind != file.OldFileKind || !patchModeMatches(entry.Baseline.Mode, file.OldMode, file.OldFileKind) {
				return ErrProposalPublicationInvalid
			} else {
				covered[file.OldPath.Key()] = struct{}{}
			}
		}
		if file.NewPath == nil {
			if file.Kind != repository.ChangeDeleted {
				return ErrProposalPublicationInvalid
			}
			continue
		}
		entry, ok := byPath[file.NewPath.Key()]
		if !ok || entry.Result == nil || entry.Result.Kind != file.NewFileKind || !patchModeMatches(entry.Result.Mode, file.NewMode, file.NewFileKind) {
			return ErrProposalPublicationInvalid
		}
		covered[file.NewPath.Key()] = struct{}{}
	}
	if len(covered) != len(byPath) {
		return ErrProposalPublicationInvalid
	}
	return nil
}

func renamePatchMatchesDelta(delta ResultDelta, file repository.ChangedFile, policyVersion uint32) bool {
	if file.OldPath == nil || file.NewPath == nil || file.Rename == nil || file.Rename.PolicyVersion != policyVersion || !file.Rename.MatchesPaths(*file.OldPath, *file.NewPath) {
		return false
	}
	var oldEntry, newEntry *ResultDeltaEntry
	for index := range delta.Entries {
		entry := &delta.Entries[index]
		if entry.Path.Key() == file.OldPath.Key() {
			oldEntry = entry
		}
		if entry.Path.Key() == file.NewPath.Key() {
			newEntry = entry
		}
	}
	if newEntry == nil || newEntry.Baseline != nil || newEntry.Result == nil {
		return false
	}
	if file.Kind == repository.ChangeRenamed {
		return oldEntry != nil && oldEntry.Baseline != nil && oldEntry.Result == nil
	}
	return oldEntry != nil && oldEntry.Baseline != nil && oldEntry.Result != nil
}

func deriveDestinationPreconditions(delta ResultDelta, files []ProposalReviewFile, observed []repository.PathPrecondition) ([]repository.PathPrecondition, error) {
	actual := make(map[repository.RepoPathKey]repository.PathPrecondition, len(observed))
	for _, precondition := range observed {
		if precondition.Validate() != nil {
			return nil, ErrProposalPublicationInvalid
		}
		if _, exists := actual[precondition.Path.Key()]; exists {
			return nil, ErrProposalPublicationInvalid
		}
		actual[precondition.Path.Key()] = clonePathPrecondition(precondition)
	}
	expected := make(map[repository.RepoPathKey]repository.PathPrecondition, len(delta.Entries))
	for _, entry := range delta.Entries {
		expected[entry.Path.Key()] = baselinePrecondition(entry)
	}
	for _, indexed := range files {
		if indexed.File.Validate() != nil {
			return nil, ErrProposalPublicationInvalid
		}
		if indexed.File.Kind != repository.ChangeCopied || indexed.File.OldPath == nil {
			continue
		}
		oldPath := indexed.File.OldPath.Key()
		if _, exists := expected[oldPath]; exists {
			continue
		}
		var source ResultDeltaEntry
		found := false
		for _, entry := range delta.Entries {
			if entry.Path.Key() == oldPath {
				source, found = entry, true
				break
			}
		}
		if !found || source.Baseline == nil {
			return nil, ErrProposalPublicationStale
		}
		expected[oldPath] = baselinePrecondition(source)
	}
	result := make([]repository.PathPrecondition, 0, len(expected))
	for path, want := range expected {
		value, ok := actual[path]
		if !ok || !preconditionMatchesExpected(value, want) {
			return nil, ErrProposalPublicationStale
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return string(result[i].Path) < string(result[j].Path) })
	return result, nil
}

func baselinePrecondition(entry ResultDeltaEntry) repository.PathPrecondition {
	if entry.Baseline == nil {
		return repository.PathPrecondition{Path: repository.RepoPath(entry.Path.Bytes())}
	}
	value := repository.PathPrecondition{Path: repository.RepoPath(entry.Path.Bytes()), MustExist: true, Kind: entry.Baseline.Kind, Mode: entry.Baseline.Mode}
	switch entry.Baseline.Kind {
	case repository.FileKindRegular:
		value.ContentHash = entry.Baseline.SHA256
		if entry.Baseline.ContentClass != "" {
			value.ContentBytes = entry.Baseline.Bytes
			value.ContentClass = entry.Baseline.ContentClass
		}
	case repository.FileKindSymlink:
		value.SymlinkTargetHash = hashBytes(entry.Baseline.LinkTarget)
	}
	return value
}

func preconditionMatchesExpected(actual, expected repository.PathPrecondition) bool {
	if actual.Path.Key() != expected.Path.Key() || actual.MustExist != expected.MustExist || actual.Kind != expected.Kind || actual.Mode != expected.Mode || actual.ContentHash != expected.ContentHash || actual.SymlinkTargetHash != expected.SymlinkTargetHash {
		return false
	}
	if expected.ContentClass != "" {
		return actual.ContentBytes == expected.ContentBytes && actual.ContentClass == expected.ContentClass
	}
	return true
}

func deriveProposedFiles(files []ProposalReviewFile, snapshot ResultSnapshot) ([]review.ProposedFile, error) {
	byPath := make(map[repository.RepoPathKey]ResultDeltaEntry, len(snapshot.Delta.Entries))
	for _, entry := range snapshot.Delta.Entries {
		byPath[entry.Path.Key()] = entry
	}
	result := make([]review.ProposedFile, 0, len(files))
	for _, indexed := range files {
		file := indexed.File
		if file.Validate() != nil {
			return nil, ErrProposalPublicationInvalid
		}
		value := review.ProposedFile{Binary: indexed.Binary}
		switch {
		case file.NewPath != nil:
			value.Path = repository.RepoPath(file.NewPath.Bytes())
			value.Kind = file.NewFileKind
			value.Mode = file.NewMode
			if entry := byPath[file.NewPath.Key()]; entry.Result != nil {
				value.ContentBytes = entry.Result.Bytes
				value.ContentHash = entry.Result.SHA256
				value.ContentClass = entry.Result.ContentClass
			}
		case file.OldPath != nil:
			value.Path = repository.RepoPath(file.OldPath.Bytes())
			value.Deleted = true
		default:
			return nil, ErrProposalPublicationInvalid
		}
		if file.OldPath != nil && file.NewPath != nil {
			oldPath := repository.RepoPath(file.OldPath.Bytes())
			value.OldPath = &oldPath
			value.OldKind = file.OldFileKind
			value.OldMode = file.OldMode
			if oldEntry, ok := byPath[file.OldPath.Key()]; ok && oldEntry.Baseline != nil {
				value.OldContentBytes = oldEntry.Baseline.Bytes
				value.OldContentClass = oldEntry.Baseline.ContentClass
			}
			if file.Kind == repository.ChangeCopied {
				if oldEntry, ok := byPath[file.OldPath.Key()]; ok && oldEntry.Result != nil {
					value.OldContentBytes = oldEntry.Result.Bytes
					value.OldContentHash = oldEntry.Result.SHA256
					value.OldContentClass = oldEntry.Result.ContentClass
				} else if oldEntry, ok := byPath[file.OldPath.Key()]; ok && oldEntry.Baseline != nil {
					value.OldContentBytes = oldEntry.Baseline.Bytes
					value.OldContentHash = oldEntry.Baseline.SHA256
					value.OldContentClass = oldEntry.Baseline.ContentClass
				}
			}
		}
		value.Added = file.Kind == repository.ChangeAdded || file.Kind == repository.ChangeUntracked
		value.Copied = file.Kind == repository.ChangeCopied
		value.TypeChanged = file.Kind == repository.ChangeTypeChanged
		if file.Kind == repository.ChangeDeleted {
			value.Deleted = true
			value.Kind = repository.FileKindUnknown
			value.Mode = 0
			value.ContentHash = ""
		}
		if value.Added && value.OldPath != nil {
			value.Added = false
		}
		if value.Deleted {
			value.OldPath = nil
			value.OldKind = repository.FileKindUnknown
			value.OldMode = 0
			value.OldContentBytes = 0
			value.OldContentHash = ""
			value.OldContentClass = ""
		}
		if value.Validate() != nil {
			return nil, ErrProposalPublicationInvalid
		}
		result = append(result, value)
	}
	return result, nil
}

func broaderProposalScope(delta ResultDelta, expected []repository.RepoPath) bool {
	paths := make(map[repository.RepoPathKey]struct{}, len(expected))
	for _, path := range expected {
		paths[path.Key()] = struct{}{}
	}
	for _, entry := range delta.Entries {
		if _, ok := paths[entry.Path.Key()]; !ok {
			return true
		}
	}
	return false
}

func proposalAttempt(aggregate review.ProposalAggregate, id domain.OperationID) (review.ProposalAttempt, bool) {
	for _, attempt := range aggregate.Attempts {
		if attempt.ID == id {
			return attempt, true
		}
	}
	return review.ProposalAttempt{}, false
}

func nextProposalVersion(aggregate review.ProposalAggregate) (review.ProposalVersionNumber, error) {
	var highest review.ProposalVersionNumber
	for _, version := range aggregate.Versions {
		if version.Version > highest {
			highest = version.Version
		}
	}
	if aggregate.Proposal.CurrentVersion != nil && *aggregate.Proposal.CurrentVersion > highest {
		highest = *aggregate.Proposal.CurrentVersion
	}
	if highest == ^review.ProposalVersionNumber(0) {
		return 0, ErrProposalPublicationConflict
	}
	return highest + 1, nil
}

func publishedAttempt(attempt review.ProposalAttempt, patch review.ProposedPatch) review.ProposalAttempt {
	value := attempt
	version := patch.Version
	value.VersionNumber = &version
	value.Outcome = review.ProposalAttemptVersionPublished
	value.ResultDisposition = review.ProposalResultPresent
	value.Baseline = snapshotIdentityPointer(patch.Baseline)
	value.Result = snapshotIdentityPointer(patch.Result)
	value.FinishedAt = &patch.CreatedAt
	return value
}

func snapshotIdentityPointer(value review.SnapshotIdentity) *review.SnapshotIdentity {
	copyValue := value
	return &copyValue
}

func clonePathPrecondition(value repository.PathPrecondition) repository.PathPrecondition {
	value.Path = repository.RepoPath(value.Path.Bytes())
	if value.NativeAlias != nil {
		alias := *value.NativeAlias
		value.NativeAlias = &alias
	}
	return value
}

func sameGenerationProvenance(left, right review.GenerationProvenance) bool {
	if left.SessionID != right.SessionID || left.Generation != right.Generation || left.Base != right.Base || left.Head != right.Head {
		return false
	}
	if left.CaptureID == nil || right.CaptureID == nil {
		return left.CaptureID == nil && right.CaptureID == nil
	}
	return *left.CaptureID == *right.CaptureID
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
