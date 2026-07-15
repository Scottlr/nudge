package repository

import (
	"fmt"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

// TargetKind identifies the user's review-target intent.
type TargetKind string

const (
	// TargetLocal reviews the current working-tree change.
	TargetLocal TargetKind = "local"
	// TargetCommit reviews one frozen commit against its parent.
	TargetCommit TargetKind = "commit"
	// TargetBranch reviews the current branch against a base branch.
	TargetBranch TargetKind = "branch"
)

// TargetGeneration identifies one frozen resolution of a stable target.
type TargetGeneration uint64

// ReviewTargetSpec records the user's target expression before Git resolves it.
// Expressions remain available for display while resolved object identities are
// stored separately in ResolvedTarget.
type ReviewTargetSpec struct {
	Kind             TargetKind
	CommitExpression string
	ParentExpression string
	BaseBranch       string
}

// NewLocalTargetSpec constructs a local working-tree target specification.
func NewLocalTargetSpec() (ReviewTargetSpec, error) {
	spec := ReviewTargetSpec{Kind: TargetLocal}
	return spec, spec.Validate()
}

// NewCommitTargetSpec constructs a commit target specification. Parent is an
// optional user expression for merge-parent selection.
func NewCommitTargetSpec(commit, parent string) (ReviewTargetSpec, error) {
	spec := ReviewTargetSpec{
		Kind:             TargetCommit,
		CommitExpression: commit,
		ParentExpression: parent,
	}
	return spec, spec.Validate()
}

// NewBranchTargetSpec constructs a current-branch target specification.
func NewBranchTargetSpec(baseBranch string) (ReviewTargetSpec, error) {
	spec := ReviewTargetSpec{Kind: TargetBranch, BaseBranch: baseBranch}
	return spec, spec.Validate()
}

// Validate checks that a target specification contains only fields meaningful
// to its target kind.
func (s ReviewTargetSpec) Validate() error {
	switch s.Kind {
	case TargetLocal:
		if s.CommitExpression != "" || s.ParentExpression != "" || s.BaseBranch != "" {
			return ErrInvalidTargetSpec
		}
	case TargetCommit:
		if s.CommitExpression == "" || s.BaseBranch != "" || !validText(s.CommitExpression) || (s.ParentExpression != "" && !validText(s.ParentExpression)) {
			return ErrInvalidTargetSpec
		}
	case TargetBranch:
		if s.BaseBranch == "" || s.CommitExpression != "" || s.ParentExpression != "" || !validText(s.BaseBranch) {
			return ErrInvalidTargetSpec
		}
	default:
		return ErrInvalidTargetSpec
	}
	return nil
}

// ResolvedTarget is one immutable Git interpretation of a target intent.
// Resolved object identities are kept distinct from the original expressions.
type ResolvedTarget struct {
	Spec             ReviewTargetSpec
	Generation       TargetGeneration
	Base             SnapshotRef
	Head             SnapshotRef
	ResolvedCommit   ObjectID
	ResolvedParent   ObjectID
	ResolvedBaseRef  ObjectID
	MergeBase        ObjectID
	BaseBranchSource string
	BranchRef        string
	// BaseBranchRef records the exact local ref selected during discovery when
	// the base expression came from a repository-local candidate.
	BaseBranchRef   string
	DirtyWorktree   bool
	NoFetchWarning  bool
	Editable        bool
	EditDestination *domain.WorktreeID
	Fingerprint     string
	ResolvedAt      time.Time
}

// NewResolvedTarget validates and returns one resolved target generation.
func NewResolvedTarget(target ResolvedTarget) (ResolvedTarget, error) {
	if err := target.Validate(); err != nil {
		return ResolvedTarget{}, err
	}
	return target, nil
}

// Validate checks generation, snapshot, target-kind, and edit-destination
// invariants without resolving Git or touching persistence.
func (t ResolvedTarget) Validate() error {
	if err := t.Spec.Validate(); err != nil || t.Generation == 0 || t.ResolvedAt.IsZero() {
		return ErrInvalidTargetSpec
	}
	if err := t.Base.Validate(); err != nil {
		return fmt.Errorf("%w: base snapshot", ErrInvalidTargetSpec)
	}
	if err := t.Head.Validate(); err != nil {
		return fmt.Errorf("%w: head snapshot", ErrInvalidTargetSpec)
	}
	if t.Editable && (t.EditDestination == nil || *t.EditDestination == "") {
		return fmt.Errorf("%w: editable target requires destination", ErrInvalidTargetSpec)
	}
	if t.Fingerprint != "" && !validText(t.Fingerprint) {
		return ErrInvalidTargetSpec
	}
	if t.ResolvedCommit != "" {
		if _, err := NewObjectID(string(t.ResolvedCommit)); err != nil {
			return fmt.Errorf("%w: resolved commit", ErrInvalidTargetSpec)
		}
	}
	if t.ResolvedParent != "" {
		if _, err := NewObjectID(string(t.ResolvedParent)); err != nil {
			return fmt.Errorf("%w: resolved parent", ErrInvalidTargetSpec)
		}
	}
	if t.MergeBase != "" {
		if _, err := NewObjectID(string(t.MergeBase)); err != nil {
			return fmt.Errorf("%w: merge base", ErrInvalidTargetSpec)
		}
	}
	if t.ResolvedBaseRef != "" {
		if _, err := NewObjectID(string(t.ResolvedBaseRef)); err != nil {
			return fmt.Errorf("%w: resolved base ref", ErrInvalidTargetSpec)
		}
	}
	if t.BaseBranchSource != "" && !validText(t.BaseBranchSource) {
		return ErrInvalidTargetSpec
	}
	if t.BranchRef != "" && !validText(t.BranchRef) {
		return ErrInvalidTargetSpec
	}
	if t.BaseBranchRef != "" && !validText(t.BaseBranchRef) {
		return ErrInvalidTargetSpec
	}

	switch t.Spec.Kind {
	case TargetLocal:
		if t.Base.Kind == SnapshotWorkingTree || t.Head.Kind != SnapshotWorkingTree || t.ResolvedParent != "" || t.ResolvedBaseRef != "" || t.MergeBase != "" || t.BaseBranchSource != "" || t.BranchRef != "" || t.BaseBranchRef != "" || t.NoFetchWarning {
			return ErrInvalidTargetSpec
		}
	case TargetCommit:
		if t.Base.Kind == SnapshotWorkingTree || t.Head.Kind != SnapshotCommit || t.ResolvedCommit == "" || t.ResolvedBaseRef != "" || t.MergeBase != "" || t.BaseBranchSource != "" || t.BranchRef != "" || t.BaseBranchRef != "" || t.NoFetchWarning {
			return ErrInvalidTargetSpec
		}
		if t.Head.ObjectID != t.ResolvedCommit {
			return ErrInvalidTargetSpec
		}
	case TargetBranch:
		if t.Base.Kind == SnapshotWorkingTree || t.Head.Kind != SnapshotCommit || t.ResolvedCommit == "" || t.ResolvedBaseRef == "" || t.MergeBase == "" || !validBaseBranchSource(t.BaseBranchSource) || t.BranchRef == "" || t.Head.ObjectID != t.ResolvedCommit {
			return ErrInvalidTargetSpec
		}
		if t.BaseBranchSource != "discovery" && t.BaseBranchRef != "" {
			return ErrInvalidTargetSpec
		}
	}
	return nil
}

func validBaseBranchSource(value string) bool {
	switch value {
	case "explicit_branch_flag", "session_choice", "repository_preference", "discovery":
		return true
	default:
		return false
	}
}
