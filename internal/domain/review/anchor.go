package review

import (
	"errors"
	"fmt"
	"time"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

// RelocationMetadata records why a later anchor version moved from its
// original placement. It is evidence, not permission to apply a change.
type RelocationMetadata struct {
	PreviousPath      repository.RepoPath
	PreviousStartLine int
	PreviousEndLine   int
	Reason            string
	ReconciledAt      time.Time
}

// CodeAnchor is durable, snapshot-bound evidence for one selected code range.
// Path bytes remain repository identity and are never interpreted as native
// paths by this package.
type CodeAnchor struct {
	Path               repository.RepoPath
	PreviousPath       repository.RepoPath
	Side               repository.DiffSide
	StartLine          int
	EndLine            int
	TargetGeneration   repository.TargetGeneration
	Base               repository.SnapshotRef
	Head               repository.SnapshotRef
	HunkFingerprint    string
	SelectionHash      string
	SelectedText       string
	BeforeContextHash  string
	AfterContextHash   string
	FingerprintVersion uint32
	State              AnchorState
	CreatedAt          time.Time
	Relocation         *RelocationMetadata
}

// NewCodeAnchor validates and returns one immutable anchor value.
func NewCodeAnchor(anchor CodeAnchor) (CodeAnchor, error) {
	if err := anchor.Validate(); err != nil {
		return CodeAnchor{}, err
	}
	anchor.Path = append(repository.RepoPath(nil), anchor.Path...)
	anchor.PreviousPath = append(repository.RepoPath(nil), anchor.PreviousPath...)
	if anchor.Relocation != nil {
		copy := *anchor.Relocation
		copy.PreviousPath = append(repository.RepoPath(nil), copy.PreviousPath...)
		anchor.Relocation = &copy
	}
	return anchor, nil
}

// Validate checks all durable identity and range evidence for an anchor.
func (a CodeAnchor) Validate() error {
	if err := a.Path.Validate(); err != nil || (len(a.PreviousPath) > 0 && a.PreviousPath.Validate() != nil) {
		return ErrInvalidCodeAnchor
	}
	if a.Side != repository.DiffBase && a.Side != repository.DiffHead {
		return ErrInvalidCodeAnchor
	}
	if a.StartLine <= 0 || a.EndLine < a.StartLine || a.TargetGeneration == 0 {
		return ErrInvalidCodeAnchor
	}
	if err := a.Base.Validate(); err != nil {
		return fmt.Errorf("%w: base snapshot: %v", ErrInvalidCodeAnchor, err)
	}
	if err := a.Head.Validate(); err != nil {
		return fmt.Errorf("%w: head snapshot: %v", ErrInvalidCodeAnchor, err)
	}
	if a.FingerprintVersion != 0 && a.FingerprintVersion != AnchorFingerprintVersion || !validMetadata(a.HunkFingerprint) || !validMetadata(a.SelectionHash) || !validOptionalMetadata(a.BeforeContextHash) || !validOptionalMetadata(a.AfterContextHash) || !validContent(a.SelectedText) {
		return ErrInvalidCodeAnchor
	}
	if a.Relocation != nil {
		if err := a.Relocation.validate(); err != nil {
			return fmt.Errorf("%w: relocation: %v", ErrInvalidCodeAnchor, err)
		}
	}
	return nil
}

func (r RelocationMetadata) validate() error {
	if err := r.PreviousPath.Validate(); err != nil || r.PreviousStartLine <= 0 || r.PreviousEndLine < r.PreviousStartLine || !validMetadata(r.Reason) || r.ReconciledAt.IsZero() {
		return errors.New("invalid relocation metadata")
	}
	return nil
}
