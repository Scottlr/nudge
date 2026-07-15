package review

import (
	"encoding/json"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// SessionKey is the replacement-safe compatibility identity used when
// restoring an unfinished review session. It is derived from verified
// repository/worktree and target evidence, never from a display path.
type SessionKey struct {
	RepositoryID   domain.RepositoryID
	WorktreeID     domain.WorktreeID
	TargetKind     repository.TargetKind
	FrozenCommit   repository.ObjectID
	BaseIdentity   string
	BranchIdentity string
}

// NewSessionKey validates and returns a compatibility key.
func NewSessionKey(key SessionKey) (SessionKey, error) {
	if err := key.Validate(); err != nil {
		return SessionKey{}, err
	}
	return key, nil
}

// Validate rejects fields that would allow one target kind to masquerade as
// another during restoration.
func (k SessionKey) Validate() error {
	if !validDomainID(k.RepositoryID) || k.BaseIdentity == "" || !validMetadata(k.BaseIdentity) {
		return ErrInvalidReviewSession
	}
	switch k.TargetKind {
	case repository.TargetLocal:
		if k.WorktreeID == "" || k.FrozenCommit != "" || k.BranchIdentity != "" {
			return ErrInvalidReviewSession
		}
	case repository.TargetCommit:
		if k.FrozenCommit == "" || k.WorktreeID != "" || k.BranchIdentity != "" {
			return ErrInvalidReviewSession
		}
	case repository.TargetBranch:
		if k.WorktreeID == "" || k.FrozenCommit == "" || k.BranchIdentity == "" || !validMetadata(k.BranchIdentity) {
			return ErrInvalidReviewSession
		}
	default:
		return ErrInvalidReviewSession
	}
	return nil
}

// SessionKeyFor derives the exact compatibility identity from a validated
// session and its resolved target.
func SessionKeyFor(session ReviewSession) (SessionKey, error) {
	if err := session.Validate(); err != nil {
		return SessionKey{}, err
	}
	worktreeID := domain.WorktreeID("")
	if session.Target.EditDestination != nil {
		worktreeID = *session.Target.EditDestination
	} else if session.Target.Head.Kind == repository.SnapshotWorkingTree {
		worktreeID = session.Target.Head.WorktreeID
	}
	baseValue := any(session.Target.Base)
	if session.TargetSpec.Kind == repository.TargetBranch {
		baseValue = struct {
			Base            repository.SnapshotRef
			ResolvedBaseRef repository.ObjectID
			MergeBase       repository.ObjectID
		}{
			Base:            session.Target.Base,
			ResolvedBaseRef: session.Target.ResolvedBaseRef,
			MergeBase:       session.Target.MergeBase,
		}
	}
	base, err := json.Marshal(baseValue)
	if err != nil {
		return SessionKey{}, err
	}
	key := SessionKey{
		RepositoryID: session.RepositoryID,
		WorktreeID:   worktreeID,
		TargetKind:   session.TargetSpec.Kind,
		FrozenCommit: session.Target.ResolvedCommit,
		BaseIdentity: string(base),
	}
	if session.TargetSpec.Kind == repository.TargetBranch {
		key.BranchIdentity = session.Target.BranchRef
	}
	return NewSessionKey(key)
}
