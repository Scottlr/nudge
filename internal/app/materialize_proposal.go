package app

import (
	"errors"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidProposalBaseline reports malformed source, policy, or manifest evidence.
	ErrInvalidProposalBaseline = errors.New("invalid proposal baseline")
	// ErrProposalBaselineStale reports a generation or capture identity mismatch.
	ErrProposalBaselineStale = errors.New("proposal baseline is stale")
	// ErrProposalBaselineUnsupported reports a disabled or unproven baseline capability.
	ErrProposalBaselineUnsupported = errors.New("proposal baseline capability is unsupported")
	// ErrProposalBaselineLimit reports a bounded baseline resource failure.
	ErrProposalBaselineLimit = errors.New("proposal baseline limit exceeded")
)

// ProposalBaselineRequest binds baseline construction to one accepted local
// generation, its immutable capture manifest, and the exact capability
// evaluation that admitted the capture.
type ProposalBaselineRequest struct {
	Generation       CaptureGeneration
	Manifest         CaptureManifest
	PolicyEvaluation CapturePolicyEvaluation
	Policy           CapabilityPolicyV1
	ResourcePolicy   ResourcePolicy
	ModeEvidence     map[repository.RepoPathKey]ModeCapabilityEvidence
	SymlinkEvidence  map[repository.RepoPathKey]SymlinkCapabilityEvidence
}

// ProposalBaseline is the complete independently verified baseline evidence
// that can be persisted with a proposal workspace lifecycle claim.
type ProposalBaseline struct {
	Generation CaptureGeneration
	Target     *repository.ResolvedTarget
	Manifest   WorkspaceManifest
}

// Validate checks the independent generation and filesystem manifest shapes.
func (r ProposalBaselineRequest) Validate() error {
	if err := r.Policy.Validate(); err != nil || r.ResourcePolicy.Validate() != nil {
		return ErrInvalidProposalBaseline
	}
	if r.Generation.Validate() != nil || r.Manifest.Validate() != nil {
		return ErrInvalidProposalBaseline
	}
	if r.Manifest.CaptureID != r.Generation.CaptureID || r.Manifest.RepositoryID != r.Generation.RepositoryID || r.Manifest.WorktreeID != r.Generation.WorktreeID || r.Manifest.ManifestHash != r.Generation.ManifestHash || r.Manifest.Candidate.Fingerprint != r.Generation.Fingerprint || r.Manifest.Candidate.Base != r.Generation.Base {
		return ErrProposalBaselineStale
	}
	if r.PolicyEvaluation.Validate(r.Policy) != nil || r.PolicyEvaluation.CaptureID != r.Generation.CaptureID || r.PolicyEvaluation.CaptureFormatVersion != r.Manifest.Version || r.PolicyEvaluation.PolicyVersion != r.Policy.Version || r.PolicyEvaluation.ResourcePolicyVersion != r.Policy.ResourcePolicyVersion || r.PolicyEvaluation.EvidenceVersion != r.Policy.EvidenceVersion || r.PolicyEvaluation.ManifestHash != r.Generation.ManifestHash {
		return ErrProposalBaselineStale
	}
	candidate := r.Manifest.Candidate
	if candidate.Policy.ResourcePolicyVersion != uint32(r.ResourcePolicy.Version) || candidate.Policy.ConversionDecision != "byte_neutral" || candidate.Policy.AttributesChanged {
		return ErrProposalBaselineUnsupported
	}
	for _, entry := range candidate.Entries {
		if err := validateProposalBaselineChange(entry.Change, r.SymlinkEvidence); err != nil {
			return err
		}
		if entry.Change.ModeTransition != nil && entry.Change.ModeTransition.IsExecutableChange() {
			if evidence, ok := r.ModeEvidence[repository.RepoPath([]byte(changePath(entry.Change))).Key()]; !ok || evidence.Transition != *entry.Change.ModeTransition || !evidence.Supported() {
				return ErrProposalBaselineUnsupported
			}
		}
		for _, path := range changePaths(entry.Change) {
			decision, ok := proposalBaselineDecision(r.PolicyEvaluation.Decisions, path, r.Policy)
			if !ok || !decision.Review || !decision.MaterializeReviewSnapshot || !decision.Propose {
				return ErrProposalBaselineUnsupported
			}
		}
	}
	return nil
}

// Validate checks that a materialized baseline contains complete evidence.
func (b ProposalBaseline) Validate() error {
	if b.Manifest.Validate() != nil {
		return ErrInvalidProposalBaseline
	}
	if b.Target != nil {
		if b.Generation != (CaptureGeneration{}) || b.Target.Validate() != nil || b.Target.Spec.Kind != repository.TargetCommit && b.Target.Spec.Kind != repository.TargetBranch {
			return ErrInvalidProposalBaseline
		}
		return nil
	}
	if b.Generation.Validate() != nil {
		return ErrInvalidProposalBaseline
	}
	return nil
}

func validateProposalBaselineChange(change repository.ChangedFile, symlinkEvidence map[repository.RepoPathKey]SymlinkCapabilityEvidence) error {
	if err := change.Validate(); err != nil {
		return ErrInvalidProposalBaseline
	}
	switch change.Kind {
	case repository.ChangeAdded, repository.ChangeModified, repository.ChangeDeleted, repository.ChangeUntracked:
	default:
		return ErrProposalBaselineUnsupported
	}
	if change.Binary || change.Conflict != nil {
		return ErrProposalBaselineUnsupported
	}
	for _, side := range []struct {
		path repository.RepoPath
		kind repository.FileKind
		mode uint32
	}{
		{path: pathOrEmpty(change.OldPath), kind: change.OldFileKind, mode: change.OldMode},
		{path: pathOrEmpty(change.NewPath), kind: change.NewFileKind, mode: change.NewMode},
	} {
		if side.kind == "" || side.kind == repository.FileKindUnknown {
			continue
		}
		if side.kind == repository.FileKindSymlink {
			if side.path == nil {
				return ErrProposalBaselineUnsupported
			}
			evidence, ok := symlinkEvidence[side.path.Key()]
			requiredChange := change.Kind
			if requiredChange == repository.ChangeUntracked {
				requiredChange = repository.ChangeAdded
			}
			if !ok || evidence.Validate() != nil || !evidence.Supported(requiredChange) {
				return ErrProposalBaselineUnsupported
			}
			continue
		}
		if side.kind != repository.FileKindRegular || side.mode&0o111 != 0 && (change.ModeTransition == nil || !change.ModeTransition.IsExecutableChange()) {
			return ErrProposalBaselineUnsupported
		}
	}
	if change.OldMode != 0 && change.NewMode != 0 && change.OldMode != change.NewMode && (change.ModeTransition == nil || !change.ModeTransition.IsExecutableChange()) {
		return ErrProposalBaselineUnsupported
	}
	return nil
}

func pathOrEmpty(path *repository.RepoPath) repository.RepoPath {
	if path == nil {
		return nil
	}
	return *path
}

func changePaths(change repository.ChangedFile) []repository.RepoPath {
	paths := make([]repository.RepoPath, 0, 2)
	if change.OldPath != nil {
		paths = append(paths, *change.OldPath)
	}
	if change.NewPath != nil {
		paths = append(paths, *change.NewPath)
	}
	return paths
}

func proposalBaselineDecision(decisions []CapabilityDecision, path repository.RepoPath, policy CapabilityPolicyV1) (CapabilityDecision, bool) {
	key := path.Key()
	for _, decision := range decisions {
		if decision.Key.Path == key && decision.Validate(policy) == nil {
			return decision, true
		}
	}
	return CapabilityDecision{}, false
}
