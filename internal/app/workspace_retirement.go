package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	ErrWorkspaceRetirementNotFound = errors.New("workspace retirement not found")
	ErrWorkspaceRetirementConflict = errors.New("workspace retirement conflict")
)

// WorkspaceRetirementPhase is the durable, restart-safe removal journal.
type WorkspaceRetirementPhase string

const (
	WorkspaceRetirementPlanned        WorkspaceRetirementPhase = "removal_planned"
	WorkspaceRetirementRemoving       WorkspaceRetirementPhase = "removing"
	WorkspaceRetirementRemoved        WorkspaceRetirementPhase = "removed"
	WorkspaceRetirementRepairRequired WorkspaceRetirementPhase = "repair_required"
)

func (p WorkspaceRetirementPhase) Validate() error {
	switch p {
	case WorkspaceRetirementPlanned, WorkspaceRetirementRemoving, WorkspaceRetirementRemoved, WorkspaceRetirementRepairRequired:
		return nil
	default:
		return ErrWorkspaceRetirementConflict
	}
}

func (p WorkspaceRetirementPhase) CanTransitionTo(next WorkspaceRetirementPhase) bool {
	if p == next {
		return p == WorkspaceRetirementPlanned || p == WorkspaceRetirementRemoving
	}
	switch p {
	case WorkspaceRetirementPlanned:
		return next == WorkspaceRetirementRemoving || next == WorkspaceRetirementRepairRequired
	case WorkspaceRetirementRemoving:
		return next == WorkspaceRetirementRemoved || next == WorkspaceRetirementRepairRequired
	case WorkspaceRetirementRepairRequired:
		return next == WorkspaceRetirementRemoving
	default:
		return false
	}
}

// WorkspaceRetirement is one immutable retention decision plus its mutable
// crash-recovery phase. It contains no filesystem paths.
type WorkspaceRetirement struct {
	Version     uint32
	OperationID domain.OperationID
	Candidate   WorkspaceRetentionCandidate
	Decision    WorkspaceRetentionDecision
	Phase       WorkspaceRetirementPhase
	Reason      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (r WorkspaceRetirement) Validate(policy WorkspaceRetentionPolicy) error {
	if r.Version != 1 || r.OperationID == "" || r.Candidate.Validate() != nil || r.Decision.Validate(policy) != nil || r.Decision.Kind != WorkspaceRetentionEligible || r.Decision.WorkspaceID != r.Candidate.WorkspaceID || r.Decision.ThreadID != r.Candidate.ThreadID || r.Decision.ProposalID != r.Candidate.ProposalID || r.Decision.ApplyOperationID != r.Candidate.ApplyOperationID || r.Phase.Validate() != nil || !safeOptionalText(r.Reason, 256) || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return ErrWorkspaceRetirementConflict
	}
	return nil
}

// WorkspaceRetirementProof is the path-free proof returned by the workspace
// owner after revalidation or exact removal.
type WorkspaceRetirementProof struct {
	WorkspaceID     domain.WorkspaceID
	OwnershipDigest string
	MarkerNonce     string
	AlreadyRemoved  bool
}

func (p WorkspaceRetirementProof) Validate() error {
	if p.WorkspaceID == "" || p.OwnershipDigest == "" || p.MarkerNonce == "" {
		return ErrWorkspaceRetirementConflict
	}
	return nil
}

// WorkspaceRetirementExecutor is the only filesystem mutation seam available
// to the reaper.
type WorkspaceRetirementExecutor interface {
	Verify(context.Context, WorkspaceRetirement) (WorkspaceRetirementProof, error)
	Remove(context.Context, WorkspaceRetirement) (WorkspaceRetirementProof, error)
}

// WorkspaceRetentionStore persists bounded candidate pages and retirement
// journals. SQLite implements this boundary with immutable plan checks.
type WorkspaceRetentionStore interface {
	ListWorkspaceRetentionCandidates(context.Context, WorkspaceRetentionPage) (WorkspaceRetentionPageResult, error)
	LoadWorkspaceRetentionCandidate(context.Context, domain.WorkspaceID) (WorkspaceRetentionCandidate, error)
	LoadWorkspaceRetirement(context.Context, domain.WorkspaceID, domain.OperationID) (WorkspaceRetirement, error)
	SaveWorkspaceRetirement(context.Context, WorkspaceRetirement) error
}

// WorkspaceRetirementRepairStore exposes only bounded owner journals to the
// repair planner. It does not expose paths or generic deletion authority.
type WorkspaceRetirementRepairStore interface {
	ListWorkspaceRetirements(context.Context, WorkspaceRetirementPhase, uint32) ([]WorkspaceRetirement, error)
}

// WorkspaceRetentionReaper owns one bounded, restartable retention pass.
type WorkspaceRetentionReaper struct {
	store    WorkspaceRetentionStore
	executor WorkspaceRetirementExecutor
	policy   WorkspaceRetentionPolicy
}

// WorkspaceRetentionReaperConfig configures a reaper.
type WorkspaceRetentionReaperConfig struct {
	Store    WorkspaceRetentionStore
	Executor WorkspaceRetirementExecutor
	Policy   WorkspaceRetentionPolicy
}

// NewWorkspaceRetentionReaper validates and constructs a reaper.
func NewWorkspaceRetentionReaper(config WorkspaceRetentionReaperConfig) (*WorkspaceRetentionReaper, error) {
	if config.Store == nil || config.Executor == nil || config.Policy.Validate() != nil {
		return nil, ErrWorkspaceRetirementConflict
	}
	return &WorkspaceRetentionReaper{store: config.Store, executor: config.Executor, policy: config.Policy}, nil
}

// WorkspaceRetentionPass reports one bounded page's outcome.
type WorkspaceRetentionPass struct {
	Candidates uint32
	Retired    uint32
	Blocked    uint32
	NextID     domain.WorkspaceID
	HasMore    bool
}

// Reap evaluates and processes one page. The caller must hold the repository
// maintenance and affected session locks required by ADR-012.
func (r *WorkspaceRetentionReaper) Reap(ctx context.Context, page WorkspaceRetentionPage, now time.Time) (WorkspaceRetentionPass, error) {
	if r == nil || ctx == nil || page.Validate() != nil || now.IsZero() {
		return WorkspaceRetentionPass{}, ErrWorkspaceRetirementConflict
	}
	cursorStore, hasCursor := r.store.(WorkspaceRetentionCursorStore)
	if hasCursor && page.AfterID == "" {
		cursor, cursorErr := cursorStore.LoadWorkspaceRetentionCursor(ctx)
		if cursorErr == nil {
			page.AfterID = cursor.AfterID
		} else if !errors.Is(cursorErr, ErrWorkspaceRetentionCursorNotFound) {
			return WorkspaceRetentionPass{}, cursorErr
		}
	}
	result, err := r.store.ListWorkspaceRetentionCandidates(ctx, page)
	if err != nil {
		return WorkspaceRetentionPass{}, err
	}
	pass := WorkspaceRetentionPass{Candidates: uint32(len(result.Candidates)), NextID: result.NextID, HasMore: result.HasMore}
	for _, candidate := range result.Candidates {
		decision, err := EvaluateWorkspaceRetention(r.policy, candidate, now)
		if err != nil {
			return pass, err
		}
		if decision.Kind != WorkspaceRetentionEligible {
			pass.Blocked++
			continue
		}
		operationID := domain.OperationID(fmt.Sprintf("workspace-retirement-%s-%d", candidate.WorkspaceID, candidate.EvaluatedRevision))
		retirement, err := r.store.LoadWorkspaceRetirement(ctx, candidate.WorkspaceID, operationID)
		if errors.Is(err, ErrWorkspaceRetirementNotFound) {
			retirement = WorkspaceRetirement{Version: 1, OperationID: operationID, Candidate: candidate, Decision: decision, Phase: WorkspaceRetirementPlanned, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			if err := r.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
				return pass, err
			}
		} else if err != nil {
			return pass, err
		} else if err := retirement.Validate(r.policy); err != nil {
			pass.Blocked++
			continue
		}
		if retirement.Phase == WorkspaceRetirementRemoved || retirement.Phase == WorkspaceRetirementRepairRequired {
			if retirement.Phase == WorkspaceRetirementRemoved {
				pass.Retired++
			} else {
				pass.Blocked++
			}
			continue
		}
		if retirement.Phase == WorkspaceRetirementPlanned {
			current, refreshErr := r.store.LoadWorkspaceRetentionCandidate(ctx, candidate.WorkspaceID)
			if refreshErr != nil {
				return pass, refreshErr
			}
			if !reflect.DeepEqual(current, candidate) {
				pass.Blocked++
				continue
			}
			currentDecision, decisionErr := EvaluateWorkspaceRetention(r.policy, current, now)
			if decisionErr != nil {
				return pass, decisionErr
			}
			if currentDecision.Kind != WorkspaceRetentionEligible {
				pass.Blocked++
				continue
			}
			proof, verifyErr := r.executor.Verify(ctx, retirement)
			if verifyErr != nil {
				if err := r.markRepairRequired(ctx, &retirement, now, "ownership verification failed"); err != nil {
					return pass, err
				}
				pass.Blocked++
				continue
			}
			if err := validateRetirementProof(retirement, proof); err != nil {
				if err := r.markRepairRequired(ctx, &retirement, now, "ownership verification mismatch"); err != nil {
					return pass, err
				}
				pass.Blocked++
				continue
			}
			if proof.AlreadyRemoved {
				retirement.Phase = WorkspaceRetirementRemoved
				retirement.UpdatedAt = now.UTC()
				if err := r.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
					return pass, err
				}
				pass.Retired++
				continue
			}
			retirement.Phase = WorkspaceRetirementRemoving
			retirement.UpdatedAt = now.UTC()
			if err := r.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
				return pass, err
			}
		}
		proof, removeErr := r.executor.Remove(ctx, retirement)
		if removeErr != nil {
			if err := r.markRepairRequired(ctx, &retirement, now, "workspace removal interrupted"); err != nil {
				return pass, err
			}
			pass.Blocked++
			continue
		}
		if err := validateRetirementProof(retirement, proof); err != nil {
			if err := r.markRepairRequired(ctx, &retirement, now, "workspace removal verification mismatch"); err != nil {
				return pass, err
			}
			pass.Blocked++
			continue
		}
		retirement.Phase = WorkspaceRetirementRemoved
		retirement.UpdatedAt = now.UTC()
		if err := r.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
			return pass, err
		}
		pass.Retired++
	}
	if hasCursor {
		cursor := WorkspaceRetentionCursor{AfterID: result.NextID, UpdatedAt: now.UTC()}
		if !result.HasMore {
			cursor.AfterID = ""
		}
		if err := cursorStore.SaveWorkspaceRetentionCursor(ctx, cursor); err != nil {
			return pass, err
		}
	}
	return pass, nil
}

func (r *WorkspaceRetentionReaper) markRepairRequired(ctx context.Context, retirement *WorkspaceRetirement, now time.Time, reason string) error {
	retirement.Phase = WorkspaceRetirementRepairRequired
	retirement.Reason = reason
	retirement.UpdatedAt = now.UTC()
	return r.store.SaveWorkspaceRetirement(ctx, *retirement)
}

func validateRetirementProof(retirement WorkspaceRetirement, proof WorkspaceRetirementProof) error {
	if proof.Validate() != nil || proof.WorkspaceID != retirement.Candidate.WorkspaceID || proof.OwnershipDigest != retirement.Candidate.OwnershipDigest || proof.MarkerNonce != retirement.Candidate.MarkerNonce {
		return ErrWorkspaceRetirementConflict
	}
	return nil
}
