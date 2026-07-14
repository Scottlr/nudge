package review

import (
	"fmt"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

// ReviewSession covers one repository and one stable review-target intent.
// Target is the current resolved generation; changing target intent selects or
// creates another session rather than changing the meaning of old anchors.
type ReviewSession struct {
	ID           domain.ReviewSessionID
	RepositoryID domain.RepositoryID
	TargetSpec   repository.ReviewTargetSpec
	Target       repository.ResolvedTarget
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     *time.Time
}

// NewReviewSession validates and returns one session value.
func NewReviewSession(session ReviewSession) (ReviewSession, error) {
	if err := session.Validate(); err != nil {
		return ReviewSession{}, err
	}
	return session, nil
}

// NewOpenReviewSession constructs a new open session with the initial target
// generation already resolved by the repository boundary.
func NewOpenReviewSession(id domain.ReviewSessionID, repositoryID domain.RepositoryID, spec repository.ReviewTargetSpec, target repository.ResolvedTarget, now time.Time) (ReviewSession, error) {
	session := ReviewSession{
		ID:           id,
		RepositoryID: repositoryID,
		TargetSpec:   spec,
		Target:       target,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	return NewReviewSession(session)
}

// Validate checks stable identity, target binding, and timestamp ordering.
func (s ReviewSession) Validate() error {
	if !validDomainID(s.ID) || !validDomainID(s.RepositoryID) || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return ErrInvalidReviewSession
	}
	if err := s.TargetSpec.Validate(); err != nil {
		return fmt.Errorf("%w: target spec: %v", ErrInvalidReviewSession, err)
	}
	if err := s.Target.Validate(); err != nil {
		return fmt.Errorf("%w: target: %v", ErrInvalidReviewSession, err)
	}
	if s.Target.Spec != s.TargetSpec {
		return fmt.Errorf("%w: target spec mismatch", ErrInvalidReviewSession)
	}
	if s.ClosedAt != nil && (s.ClosedAt.IsZero() || s.ClosedAt.Before(s.UpdatedAt)) {
		return ErrInvalidReviewSession
	}
	return nil
}

// Close marks the session closed without changing its target or any review
// thread state. Closing is idempotent at the same or later time.
func (s *ReviewSession) Close(now time.Time) error {
	if s == nil || s.Validate() != nil || now.IsZero() || now.Before(s.UpdatedAt) {
		return ErrInvalidStatusTransition
	}
	if s.ClosedAt != nil {
		if now.Before(*s.ClosedAt) {
			return ErrInvalidStatusTransition
		}
		s.UpdatedAt = now
		*s.ClosedAt = now
		return nil
	}
	s.UpdatedAt = now
	s.ClosedAt = &now
	return nil
}
