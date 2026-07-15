package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrWorkspaceLifecycleUnavailable = errors.New("proposal workspace lifecycle unavailable")
	ErrWorkspaceLifecycleMismatch    = errors.New("proposal workspace lifecycle mismatch")
)

// LifecycleRequest is the common fenced operation input. Roots come only from
// T108's verified handle; callers cannot substitute arbitrary filesystem paths.
type LifecycleRequest struct {
	Store            SessionTransactionStore
	Allocator        *Allocator
	Capacity         app.CapacityReservationPort
	CapacityPlan     app.CapacityPlan
	CapacityPolicy   app.ResourcePolicy
	CapacityEvidence []app.VolumeEvidence
	Guard            app.SessionWriteGuard
	Handle           WorkspaceHandle
	Workspace        review.ProposalWorkspace
	OperationID      domain.OperationID
	Owner            string
	Now              time.Time
}

// LifecycleResult returns the durable operation evidence and the advanced
// session fence. The result manifest is immutable evidence of the clean root.
type LifecycleResult struct {
	Evidence app.ProposalWorkspaceLifecycle
	Guard    app.SessionWriteGuard
}

// ApplyVerification is the explicit proof required before advancing a trusted
// baseline after destination application. T035 never performs the apply.
type ApplyVerification struct {
	ProposalID domain.ProposalID
	WorktreeID domain.WorktreeID
	VerifiedAt time.Time
	Verified   bool
}

func (p ApplyVerification) Validate(expected domain.WorktreeID) error {
	if p.ProposalID == "" || p.WorktreeID == "" || p.WorktreeID != expected || !p.Verified || p.VerifiedAt.IsZero() {
		return ErrWorkspaceLifecycleMismatch
	}
	return nil
}

type InstallBaselineRequest struct {
	LifecycleRequest
	Source TrustedTreeSource
}

type ResetResultRequest struct {
	LifecycleRequest
}

type AdvanceBaselineRequest struct {
	LifecycleRequest
	Source TrustedTreeSource
	Apply  ApplyVerification
}

// RefreshBaselineRequest replaces the isolated baseline from a newly
// accepted generation without attributing the operation to a destination
// apply. The result root is reset from that verified baseline before the
// lifecycle becomes ready again.
type RefreshBaselineRequest struct {
	LifecycleRequest
	Source TrustedTreeSource
}

type RecoverLifecycleRequest struct {
	LifecycleRequest
	Nonce       string
	Reservation app.CapacityReservation
	Source      TrustedTreeSource
}

// Lifecycle owns active baseline/result contents but not workspace retirement,
// provider permissions, snapshots, patches, or destination application.
type Lifecycle struct{}

func NewLifecycle() *Lifecycle { return &Lifecycle{} }

func (l *Lifecycle) InstallBaseline(ctx context.Context, request InstallBaselineRequest) (LifecycleResult, error) {
	if l == nil || request.Source == nil || request.Workspace.State != review.WorkspaceCreating {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	if err := request.validate(); err != nil {
		return LifecycleResult{}, err
	}
	if err := request.Source.Identity().Validate(); err != nil {
		return LifecycleResult{}, err
	}
	reservation, lease, claim, guard, err := beginLifecycle(ctx, request.LifecycleRequest, app.WorkspacePurposeInstallBaseline, app.WorkspaceBaselineInstalling, request.Source.Identity(), app.WorkspaceManifest{}, app.WorkspaceManifest{})
	if err != nil {
		return LifecycleResult{}, err
	}
	durable := true
	defer func() {
		if !durable {
			_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		}
		_ = lease.Close()
	}()
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := clearMaterializeRoot(lease.Handle().Roots.Baseline.Path()); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	baseline, err := MaterializeTree(ctx, request.Source, lease.Handle().Roots.Baseline, request.CapacityPolicy)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Baseline = baseline
	claim.Phase = app.WorkspaceResultPreparing
	claim.UpdatedAt = lifecycleNow(request.Now)
	guard, err = persistLifecycleUpdate(ctx, request.Store, guard, claim)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := clearMaterializeRoot(lease.Handle().Roots.Result.Path()); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := CopyManifestToRoot(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, baseline, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Result = baseline.Clone()
	claim.Phase = app.WorkspaceLifecycleReady
	claim.UpdatedAt = lifecycleNow(request.Now)
	workspace := request.Workspace
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = claim.UpdatedAt
	guard, err = persistLifecycleCompletion(ctx, request.Store, guard, claim, workspace)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	durable = false
	if err := request.Capacity.Release(ctx, reservation, request.CapacityPlan, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	return LifecycleResult{Evidence: claim, Guard: guard}, nil
}

func (l *Lifecycle) ResetResult(ctx context.Context, request ResetResultRequest) (LifecycleResult, error) {
	if l == nil {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	if err := request.validate(); err != nil {
		return LifecycleResult{}, err
	}
	if !resettableWorkspaceState(request.Workspace.State) {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	previous, err := loadLatestLifecycle(ctx, request.Store, request.Handle.WorkspaceID)
	if err != nil || previous.Phase != app.WorkspaceLifecycleReady || previous.Baseline.Validate() != nil {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	reservation, lease, claim, guard, err := beginLifecycle(ctx, request.LifecycleRequest, app.WorkspacePurposeResetResult, app.WorkspaceResultResetting, app.WorkspaceSourceIdentity{}, previous.Baseline.Clone(), previous.Result.Clone())
	if err != nil {
		return LifecycleResult{}, err
	}
	durable := true
	defer func() {
		if !durable {
			_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		}
		_ = lease.Close()
	}()
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, claim.Baseline, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Result = claim.Baseline.Clone()
	claim.Phase = app.WorkspaceLifecycleReady
	claim.UpdatedAt = lifecycleNow(request.Now)
	workspace := request.Workspace
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = claim.UpdatedAt
	guard, err = persistLifecycleCompletion(ctx, request.Store, guard, claim, workspace)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	durable = false
	if err := request.Capacity.Release(ctx, reservation, request.CapacityPlan, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	return LifecycleResult{Evidence: claim, Guard: guard}, nil
}

func (l *Lifecycle) AdvanceBaseline(ctx context.Context, request AdvanceBaselineRequest) (LifecycleResult, error) {
	if l == nil || request.Source == nil {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	if err := request.validate(); err != nil {
		return LifecycleResult{}, err
	}
	if err := request.Source.Identity().Validate(); err != nil {
		return LifecycleResult{}, err
	}
	if err := request.Apply.Validate(request.Workspace.WorktreeID); err != nil {
		return LifecycleResult{}, err
	}
	if request.Workspace.State != review.WorkspaceResultReady && request.Workspace.State != review.WorkspaceReady {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	previous, err := loadLatestLifecycle(ctx, request.Store, request.Handle.WorkspaceID)
	if err != nil || previous.Phase != app.WorkspaceLifecycleReady {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	reservation, lease, claim, guard, err := beginLifecycle(ctx, request.LifecycleRequest, app.WorkspacePurposeAdvanceBaseline, app.WorkspaceBaselineAdvancing, request.Source.Identity(), previous.Baseline.Clone(), previous.Result.Clone())
	if err != nil {
		return LifecycleResult{}, err
	}
	durable := true
	defer func() {
		if !durable {
			_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		}
		_ = lease.Close()
	}()
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := clearMaterializeRoot(lease.Handle().Roots.Baseline.Path()); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	baseline, err := MaterializeTree(ctx, request.Source, lease.Handle().Roots.Baseline, request.CapacityPolicy)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Baseline = baseline
	claim.UpdatedAt = lifecycleNow(request.Now)
	guard, err = persistLifecycleUpdate(ctx, request.Store, guard, claim)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, baseline, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Result = baseline.Clone()
	claim.Phase = app.WorkspaceLifecycleReady
	claim.UpdatedAt = lifecycleNow(request.Now)
	workspace := request.Workspace
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = claim.UpdatedAt
	guard, err = persistLifecycleCompletion(ctx, request.Store, guard, claim, workspace)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	durable = false
	if err := request.Capacity.Release(ctx, reservation, request.CapacityPlan, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	return LifecycleResult{Evidence: claim, Guard: guard}, nil
}

// RefreshBaseline rebuilds an existing proposal workspace from current
// immutable source evidence. It never reads or mutates the user destination.
func (l *Lifecycle) RefreshBaseline(ctx context.Context, request RefreshBaselineRequest) (LifecycleResult, error) {
	if l == nil || request.Source == nil {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	if err := request.validate(); err != nil {
		return LifecycleResult{}, err
	}
	if err := request.Source.Identity().Validate(); err != nil {
		return LifecycleResult{}, err
	}
	if request.Workspace.State != review.WorkspaceReady && request.Workspace.State != review.WorkspaceResultReady {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	previous, err := loadLatestLifecycle(ctx, request.Store, request.Handle.WorkspaceID)
	if err != nil || previous.Phase != app.WorkspaceLifecycleReady || previous.Baseline.Validate() != nil {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	reservation, lease, claim, guard, err := beginLifecycle(ctx, request.LifecycleRequest, app.WorkspacePurposeRefreshBaseline, app.WorkspaceBaselineAdvancing, request.Source.Identity(), previous.Baseline.Clone(), previous.Result.Clone())
	if err != nil {
		return LifecycleResult{}, err
	}
	durable := true
	defer func() {
		if !durable {
			_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		}
		_ = lease.Close()
	}()
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := clearMaterializeRoot(lease.Handle().Roots.Baseline.Path()); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	baseline, err := MaterializeTree(ctx, request.Source, lease.Handle().Roots.Baseline, request.CapacityPolicy)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Baseline = baseline
	claim.UpdatedAt = lifecycleNow(request.Now)
	guard, err = persistLifecycleUpdate(ctx, request.Store, guard, claim)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, baseline, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	claim.Result = baseline.Clone()
	claim.Phase = app.WorkspaceLifecycleReady
	claim.UpdatedAt = lifecycleNow(request.Now)
	workspace := request.Workspace
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = claim.UpdatedAt
	guard, err = persistLifecycleCompletion(ctx, request.Store, guard, claim, workspace)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	durable = false
	if err := request.Capacity.Release(ctx, reservation, request.CapacityPlan, request.CapacityPolicy); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	return LifecycleResult{Evidence: claim, Guard: guard}, nil
}

// Recover resumes only the exact durable operation and reservation. A missing
// or mismatched source leaves the journal untouched for explicit repair.
func (l *Lifecycle) Recover(ctx context.Context, request RecoverLifecycleRequest) (LifecycleResult, error) {
	if l == nil || request.Store == nil || request.Reservation.Marker() == "" {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	if err := request.validate(); err != nil || !validWorkspaceNonce(request.Nonce) {
		return LifecycleResult{}, err
	}
	lifecycleStore, ok := request.Store.(app.ProposalWorkspaceLifecycleStore)
	if !ok {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	claim, err := lifecycleStore.LoadProposalWorkspaceLifecycle(ctx, request.Handle.WorkspaceID, request.OperationID)
	if err != nil || claim.Owner != request.Owner || claim.Nonce != request.Nonce || claim.CapacityReservationMarker != request.Reservation.Marker() {
		return LifecycleResult{}, ErrWorkspaceLifecycleMismatch
	}
	workspaceStore, ok := request.Store.(WorkspaceStore)
	if !ok {
		return LifecycleResult{}, ErrWorkspaceLifecycleUnavailable
	}
	lease, err := request.Allocator.AcquireVerified(ctx, workspaceStore, request.Handle, request.Reservation)
	if err != nil {
		return LifecycleResult{}, err
	}
	defer lease.Close()
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, request.Reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: request.Guard}, err
	}
	guard := request.Guard
	switch claim.Phase {
	case app.WorkspaceBaselineInstalling:
		if request.Source == nil || request.Source.Identity() != claim.Source {
			return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
		}
		if err := clearMaterializeRoot(lease.Handle().Roots.Baseline.Path()); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		baseline, materializeErr := MaterializeTree(ctx, request.Source, lease.Handle().Roots.Baseline, request.CapacityPolicy)
		if materializeErr != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, materializeErr
		}
		claim.Baseline = baseline
		claim.Phase = app.WorkspaceResultPreparing
		claim.UpdatedAt = lifecycleNow(request.Now)
		guard, err = persistLifecycleUpdate(ctx, request.Store, guard, claim)
		if err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		fallthrough
	case app.WorkspaceResultPreparing:
		if claim.Baseline.Validate() != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
		}
		if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, claim.Baseline, request.CapacityPolicy); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		claim.Result = claim.Baseline.Clone()
		claim.Phase = app.WorkspaceLifecycleReady
		claim.UpdatedAt = lifecycleNow(request.Now)
	case app.WorkspaceResultResetting:
		if claim.Baseline.Validate() != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
		}
		if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, claim.Baseline, request.CapacityPolicy); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		claim.Result = claim.Baseline.Clone()
		claim.Phase = app.WorkspaceLifecycleReady
		claim.UpdatedAt = lifecycleNow(request.Now)
	case app.WorkspaceBaselineAdvancing:
		if request.Source == nil || request.Source.Identity() != claim.Source {
			return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
		}
		if err := clearMaterializeRoot(lease.Handle().Roots.Baseline.Path()); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		baseline, materializeErr := MaterializeTree(ctx, request.Source, lease.Handle().Roots.Baseline, request.CapacityPolicy)
		if materializeErr != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, materializeErr
		}
		claim.Baseline = baseline
		if err := ResetResultToBaseline(ctx, lease.Handle().Roots.Baseline, lease.Handle().Roots.Result, baseline, request.CapacityPolicy); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		claim.Result = baseline.Clone()
		claim.Phase = app.WorkspaceLifecycleReady
		claim.UpdatedAt = lifecycleNow(request.Now)
	case app.WorkspaceLifecycleReady:
		if claim.Baseline.Validate() != nil || claim.Result.Validate() != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
		}
		if err := verifyMaterializedManifest(lease.Handle().Roots.Baseline.Path(), claim.Baseline, request.CapacityPolicy); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		if err := verifyMaterializedManifest(lease.Handle().Roots.Result.Path(), claim.Result, request.CapacityPolicy); err != nil {
			return LifecycleResult{Evidence: claim, Guard: guard}, err
		}
		return LifecycleResult{Evidence: claim, Guard: guard}, nil
	default:
		return LifecycleResult{Evidence: claim, Guard: guard}, ErrWorkspaceLifecycleMismatch
	}
	workspace := request.Workspace
	workspace.State = review.WorkspaceReady
	workspace.UpdatedAt = claim.UpdatedAt
	guard, err = persistLifecycleCompletion(ctx, request.Store, guard, claim, workspace)
	if err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	if err := recheckLifecycleCapacity(ctx, request.LifecycleRequest, request.Reservation); err != nil {
		return LifecycleResult{Evidence: claim, Guard: guard}, err
	}
	return LifecycleResult{Evidence: claim, Guard: guard}, nil
}

func (r LifecycleRequest) validate() error {
	if r.Store == nil || r.Allocator == nil || r.Capacity == nil || r.Guard.Validate() != nil || r.Handle.WorkspaceID == "" || r.Workspace.Validate() != nil || r.OperationID == "" || r.Owner == "" || r.CapacityPlan.OperationID != r.OperationID || r.CapacityPlan.PolicyVersion != r.CapacityPolicy.Version || r.Now.IsZero() {
		return ErrWorkspaceLifecycleUnavailable
	}
	if err := app.ValidateCapacityPlan(r.CapacityPolicy, r.CapacityPlan, r.CapacityEvidence); err != nil {
		return err
	}
	if r.Handle.WorkspaceID != r.Workspace.ID || r.Handle.RepositoryID != r.Workspace.RepositoryID || r.Handle.WorktreeID != r.Workspace.WorktreeID || r.Handle.ThreadID != r.Workspace.SourceThreadID || r.Handle.Roots.Baseline.Path() != r.Workspace.Roots.Baseline || r.Handle.Roots.Admin.Path() != r.Workspace.Roots.Admin || r.Handle.Roots.Result.Path() != r.Workspace.Roots.Result || r.Handle.Roots.Destination.Path() != r.Workspace.Roots.Destination {
		return ErrWorkspaceLifecycleMismatch
	}
	return nil
}

func beginLifecycle(ctx context.Context, request LifecycleRequest, purpose app.ProposalWorkspaceLifecyclePurpose, phase app.ProposalWorkspaceLifecyclePhase, source app.WorkspaceSourceIdentity, baseline, result app.WorkspaceManifest) (app.CapacityReservation, *WorkspaceLease, app.ProposalWorkspaceLifecycle, app.SessionWriteGuard, error) {
	reservation, err := request.Capacity.Reserve(ctx, request.CapacityPlan, request.CapacityPolicy, request.CapacityEvidence)
	if err != nil {
		return app.CapacityReservation{}, nil, app.ProposalWorkspaceLifecycle{}, request.Guard, err
	}
	workspaceStore, ok := request.Store.(WorkspaceStore)
	if !ok {
		_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		return app.CapacityReservation{}, nil, app.ProposalWorkspaceLifecycle{}, request.Guard, ErrWorkspaceLifecycleUnavailable
	}
	lease, err := request.Allocator.AcquireVerified(ctx, workspaceStore, request.Handle, reservation)
	if err != nil {
		_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		return app.CapacityReservation{}, nil, app.ProposalWorkspaceLifecycle{}, request.Guard, err
	}
	nonce, err := lifecycleNonce()
	if err != nil {
		_ = lease.Close()
		_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		return app.CapacityReservation{}, nil, app.ProposalWorkspaceLifecycle{}, request.Guard, err
	}
	claim := app.ProposalWorkspaceLifecycle{WorkspaceID: request.Handle.WorkspaceID, RepositoryID: request.Handle.RepositoryID, WorktreeID: request.Handle.WorktreeID, SessionID: request.Workspace.SessionID, ThreadID: request.Handle.ThreadID, OperationID: request.OperationID, Owner: request.Owner, Nonce: nonce, CapacityReservationMarker: reservation.Marker(), Purpose: purpose, Phase: phase, Source: source, Baseline: baseline, Result: result, CreatedAt: lifecycleNow(request.Now), UpdatedAt: lifecycleNow(request.Now)}
	guard, err := persistLifecycleCreate(ctx, request.Store, request.Guard, claim, request.Workspace, phase == app.WorkspaceResultResetting)
	if err != nil {
		_ = lease.Close()
		_ = request.Capacity.Release(context.Background(), reservation, request.CapacityPlan, request.CapacityPolicy)
		return app.CapacityReservation{}, nil, app.ProposalWorkspaceLifecycle{}, guard, err
	}
	return reservation, lease, claim, guard, nil
}

func persistLifecycleCreate(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, lifecycle app.ProposalWorkspaceLifecycle, workspace review.ProposalWorkspace, resetting bool) (app.SessionWriteGuard, error) {
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		lifecycleTx, ok := tx.(app.ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrWorkspaceLifecycleUnavailable
		}
		if err := lifecycleTx.CreateProposalWorkspaceLifecycle(ctx, lifecycle); err != nil {
			return err
		}
		if resetting {
			workspace.State = review.WorkspaceResetting
			workspace.UpdatedAt = lifecycle.UpdatedAt
			return lifecycleTx.UpdateProposalWorkspace(ctx, workspace)
		}
		return nil
	})
}

func persistLifecycleUpdate(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, lifecycle app.ProposalWorkspaceLifecycle) (app.SessionWriteGuard, error) {
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		lifecycleTx, ok := tx.(app.ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrWorkspaceLifecycleUnavailable
		}
		return lifecycleTx.UpdateProposalWorkspaceLifecycle(ctx, lifecycle)
	})
}

func persistLifecycleCompletion(ctx context.Context, store SessionTransactionStore, guard app.SessionWriteGuard, lifecycle app.ProposalWorkspaceLifecycle, workspace review.ProposalWorkspace) (app.SessionWriteGuard, error) {
	return store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		lifecycleTx, ok := tx.(app.ProposalWorkspaceLifecycleStoreTx)
		if !ok {
			return ErrWorkspaceLifecycleUnavailable
		}
		if err := lifecycleTx.UpdateProposalWorkspaceLifecycle(ctx, lifecycle); err != nil {
			return err
		}
		return lifecycleTx.UpdateProposalWorkspace(ctx, workspace)
	})
}

func loadLatestLifecycle(ctx context.Context, store SessionTransactionStore, workspaceID domain.WorkspaceID) (app.ProposalWorkspaceLifecycle, error) {
	lifecycleStore, ok := store.(app.ProposalWorkspaceLifecycleStore)
	if !ok {
		return app.ProposalWorkspaceLifecycle{}, ErrWorkspaceLifecycleUnavailable
	}
	return lifecycleStore.LoadLatestProposalWorkspaceLifecycle(ctx, workspaceID)
}

func recheckLifecycleCapacity(ctx context.Context, request LifecycleRequest, reservation app.CapacityReservation) error {
	_, err := request.Capacity.Recheck(ctx, reservation, request.CapacityPlan, request.CapacityPolicy, app.RecheckBounds{MaxBytes: request.CapacityPolicy.Artifact.SnapshotBytes, MaxInterval: time.Minute}, request.CapacityEvidence)
	return err
}

func resettableWorkspaceState(state review.WorkspaceState) bool {
	return state == review.WorkspaceReady || state == review.WorkspaceTurnRunning || state == review.WorkspaceResultReady || state == review.WorkspaceResetting
}

func lifecycleNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func lifecycleNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}
