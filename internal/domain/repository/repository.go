// Package repository models repository identity, worktrees, targets, and
// immutable snapshot references without executing Git or interpreting paths.
package repository

import (
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrInvalidBindingEvidence reports incomplete or unsafe binding evidence.
	ErrInvalidBindingEvidence = errors.New("invalid repository binding evidence")
	// ErrInvalidRepository reports an invalid repository value.
	ErrInvalidRepository = errors.New("invalid repository")
	// ErrInvalidWorktree reports an invalid worktree value.
	ErrInvalidWorktree = errors.New("invalid worktree")
	// ErrInvalidTargetSpec reports contradictory or incomplete target intent.
	ErrInvalidTargetSpec = errors.New("invalid review target specification")
	// ErrInvalidSnapshotRef reports an invalid snapshot reference.
	ErrInvalidSnapshotRef = errors.New("invalid snapshot reference")
	// ErrInvalidObjectID reports an empty or control-bearing object identity.
	ErrInvalidObjectID = errors.New("invalid object identity")
)

// NativeIdentity is an opaque platform identity supplied by an adapter. Domain
// code preserves it but never interprets its bytes.
type NativeIdentity string

// RepositoryBindingEvidence proves the current binding of a repository's
// common Git directory without making the native identity format a domain
// concern.
type RepositoryBindingEvidence struct {
	Version              uint32
	ObjectFormat         string
	CommonGitDir         string
	CommonGitDirIdentity NativeIdentity
}

// Validate checks that the evidence contains the fields required to bind a
// repository without canonicalizing or resolving any path.
func (e RepositoryBindingEvidence) Validate() error {
	if e.Version == 0 || e.ObjectFormat == "" || !validText(e.ObjectFormat) || e.CommonGitDir == "" || !validText(e.CommonGitDir) {
		return ErrInvalidBindingEvidence
	}
	return nil
}

// WorktreeBindingEvidence proves the current binding of a checked-out root
// and its per-worktree Git directory. The two paths remain distinct even for
// linked worktrees that share a common Git directory.
type WorktreeBindingEvidence struct {
	Version        uint32
	ObjectFormat   string
	RootPath       string
	GitDir         string
	RootIdentity   NativeIdentity
	GitDirIdentity NativeIdentity
}

// Validate checks that the evidence contains both canonical lookup paths.
func (e WorktreeBindingEvidence) Validate() error {
	if e.Version == 0 || e.ObjectFormat == "" || !validText(e.ObjectFormat) || e.RootPath == "" || !validText(e.RootPath) || e.GitDir == "" || !validText(e.GitDir) {
		return ErrInvalidBindingEvidence
	}
	return nil
}

// Repository identifies a Git repository by Nudge identity and verified
// common-directory binding evidence.
type Repository struct {
	ID           domain.RepositoryID
	CommonGitDir string
	Binding      RepositoryBindingEvidence
	DisplayName  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate checks the repository identity, binding, display name, and time
// ordering. It does not inspect or resolve filesystem paths.
func (r Repository) Validate() error {
	if r.ID == "" || r.CommonGitDir == "" || !validText(r.CommonGitDir) || r.DisplayName == "" || !validText(r.DisplayName) || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrInvalidRepository
	}
	if err := r.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRepository, err)
	}
	if r.Binding.CommonGitDir != r.CommonGitDir {
		return fmt.Errorf("%w: common Git directory mismatch", ErrInvalidRepository)
	}
	return nil
}

// WorktreeRef identifies a checked-out worktree and retains its repository
// relationship, per-worktree Git directory, current head state, and launch
// focus separately from repository identity.
type WorktreeRef struct {
	ID              domain.WorktreeID
	RepositoryID    domain.RepositoryID
	RootPath        string
	GitDir          string
	Binding         WorktreeBindingEvidence
	CurrentObjectID ObjectID
	BranchName      string
	Detached        bool
	LaunchFocus     string
}

// Validate checks the structural worktree invariants without resolving Git
// refs or interpreting native path identity payloads.
func (w WorktreeRef) Validate() error {
	if w.ID == "" || w.RepositoryID == "" || w.RootPath == "" || !validText(w.RootPath) || w.GitDir == "" || !validText(w.GitDir) {
		return ErrInvalidWorktree
	}
	if err := w.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorktree, err)
	}
	if w.Binding.RootPath != w.RootPath || w.Binding.GitDir != w.GitDir {
		return fmt.Errorf("%w: worktree path mismatch", ErrInvalidWorktree)
	}
	if w.Detached && w.BranchName != "" {
		return fmt.Errorf("%w: detached worktree has branch name", ErrInvalidWorktree)
	}
	if w.BranchName != "" && !validText(w.BranchName) {
		return ErrInvalidWorktree
	}
	if w.LaunchFocus != "" && !validText(w.LaunchFocus) {
		return ErrInvalidWorktree
	}
	if w.CurrentObjectID != "" {
		if _, err := NewObjectID(string(w.CurrentObjectID)); err != nil {
			return fmt.Errorf("%w: current object ID", ErrInvalidWorktree)
		}
	}
	return nil
}

func validText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
