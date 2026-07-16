package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

const (
	RetirementRepairHandlerKind    app.RepairHandlerKind = "workspace.retirement"
	RetirementRepairHandlerVersion                       = "v1"
)

var (
	ErrRetirementRepairReference = errors.New("invalid workspace retirement repair reference")
	ErrRetirementRepairState     = errors.New("workspace retirement is not repairable")
)

// RetirementRepairOwner is the typed T058 planner/handler for an interrupted
// workspace retirement. It receives no path or generic mutation capability.
type RetirementRepairOwner struct {
	store    app.WorkspaceRetentionStore
	journals app.WorkspaceRetirementRepairStore
	executor app.WorkspaceRetirementExecutor
	now      func() time.Time
}

// NewRetirementRepairOwner constructs the owner-specific repair seam.
func NewRetirementRepairOwner(store app.WorkspaceRetentionStore, executor app.WorkspaceRetirementExecutor) (*RetirementRepairOwner, error) {
	if store == nil || executor == nil {
		return nil, ErrRetirementRepairState
	}
	journals, ok := store.(app.WorkspaceRetirementRepairStore)
	if !ok {
		return nil, ErrRetirementRepairState
	}
	return &RetirementRepairOwner{store: store, journals: journals, executor: executor, now: func() time.Time { return time.Now().UTC() }}, nil
}

// RegisterRetirementRepairOwner registers both exact owner hooks on a common
// repair registry. Composition decides when the registry is active.
func RegisterRetirementRepairOwner(registry *app.RepairRegistry, owner *RetirementRepairOwner) error {
	if registry == nil || owner == nil {
		return ErrRetirementRepairState
	}
	if err := registry.RegisterHandler(owner); err != nil {
		return err
	}
	return registry.RegisterPlanner(app.HealthWorkspaceNotChecked, owner)
}

func (o *RetirementRepairOwner) Kind() app.RepairHandlerKind { return RetirementRepairHandlerKind }

func (o *RetirementRepairOwner) Version() string { return RetirementRepairHandlerVersion }

// Plans produces bounded plans for repair-required retirement journals.
func (o *RetirementRepairOwner) Plans(ctx context.Context, report app.HealthReport) ([]app.RepairPlan, error) {
	if o == nil || ctx == nil || report.HealthRevision == "" {
		return nil, ErrRetirementRepairState
	}
	retirements, err := o.journals.ListWorkspaceRetirements(ctx, app.WorkspaceRetirementRepairRequired, app.MaxWorkspaceRetentionCandidatePage)
	if err != nil {
		return nil, err
	}
	plans := make([]app.RepairPlan, 0, len(retirements))
	now := o.now().UTC()
	for _, retirement := range retirements {
		if err := retirement.Validate(app.DefaultWorkspaceRetentionPolicy()); err != nil || retirement.Phase != app.WorkspaceRetirementRepairRequired {
			return nil, ErrRetirementRepairState
		}
		ref, err := retirementReference(retirement.Candidate.WorkspaceID, retirement.OperationID)
		if err != nil {
			return nil, err
		}
		preconditions, err := retirementPreconditionHash(retirement)
		if err != nil {
			return nil, err
		}
		plan := app.RepairPlan{
			ID:                app.RepairPlanID("workspace-retirement-" + string(retirement.Candidate.WorkspaceID) + "-" + string(retirement.OperationID)),
			HealthCode:        app.HealthWorkspaceNotChecked,
			HealthRevision:    report.HealthRevision,
			PolicyVersion:     app.RepairFrameworkVersion,
			Summary:           "A proposal workspace retirement needs explicit owner repair.",
			Effect:            "Revalidate and finish the recorded workspace retirement.",
			OwnedResourceRefs: []string{ref},
			PreconditionsHash: preconditions,
			ConfirmationText:  "repair workspace retirement",
			HandlerKind:       RetirementRepairHandlerKind,
			HandlerVersion:    RetirementRepairHandlerVersion,
			CreatedAt:         now,
			ExpiresAt:         now.Add(24 * time.Hour),
		}
		if err := plan.Validate(); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (o *RetirementRepairOwner) Revalidate(ctx context.Context, plan app.RepairPlan) (app.RepairRevalidation, error) {
	retirement, err := o.loadPlanRetirement(ctx, plan)
	if err != nil {
		return app.RepairRevalidation{}, err
	}
	if retirement.Phase != app.WorkspaceRetirementRepairRequired {
		return app.RepairRevalidation{}, ErrRetirementRepairState
	}
	hash, err := retirementPreconditionHash(retirement)
	if err != nil || hash != plan.PreconditionsHash {
		return app.RepairRevalidation{}, app.ErrRepairPreconditions
	}
	proof, err := o.executor.Verify(ctx, retirement)
	if err != nil || proof.WorkspaceID != retirement.Candidate.WorkspaceID || proof.OwnershipDigest != retirement.Candidate.OwnershipDigest || proof.MarkerNonce != retirement.Candidate.MarkerNonce {
		return app.RepairRevalidation{}, ErrRetirementRepairState
	}
	return app.RepairRevalidation{PreconditionsHash: hash, LockProof: "workspace-retirement-lock:" + string(retirement.Candidate.WorkspaceID), JournalID: string(retirement.OperationID)}, nil
}

func (o *RetirementRepairOwner) Execute(ctx context.Context, operation app.RepairOperation, plan app.RepairPlan) (app.RepairEffect, error) {
	retirement, err := o.loadPlanRetirement(ctx, plan)
	if err != nil {
		return app.RepairEffect{}, err
	}
	if retirement.Phase != app.WorkspaceRetirementRepairRequired {
		return app.RepairEffect{}, ErrRetirementRepairState
	}
	retirement.Phase = app.WorkspaceRetirementRemoving
	retirement.UpdatedAt = o.now().UTC()
	if err := o.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
		return app.RepairEffect{}, err
	}
	proof, err := o.executor.Remove(ctx, retirement)
	if err != nil {
		retirement.Phase = app.WorkspaceRetirementRepairRequired
		retirement.Reason = "explicit retirement repair did not complete"
		retirement.UpdatedAt = o.now().UTC()
		if saveErr := o.store.SaveWorkspaceRetirement(ctx, retirement); saveErr != nil {
			return app.RepairEffect{}, errors.Join(err, saveErr)
		}
		return app.RepairEffect{}, err
	}
	if proof.WorkspaceID != retirement.Candidate.WorkspaceID || proof.OwnershipDigest != retirement.Candidate.OwnershipDigest || proof.MarkerNonce != retirement.Candidate.MarkerNonce {
		retirement.Phase = app.WorkspaceRetirementRepairRequired
		retirement.Reason = "explicit retirement repair proof mismatch"
		retirement.UpdatedAt = o.now().UTC()
		if saveErr := o.store.SaveWorkspaceRetirement(ctx, retirement); saveErr != nil {
			return app.RepairEffect{}, errors.Join(ErrRetirementRepairState, saveErr)
		}
		return app.RepairEffect{}, ErrRetirementRepairState
	}
	retirement.Phase = app.WorkspaceRetirementRemoved
	retirement.UpdatedAt = o.now().UTC()
	if err := o.store.SaveWorkspaceRetirement(ctx, retirement); err != nil {
		return app.RepairEffect{}, err
	}
	return app.RepairEffect{EffectID: "workspace-retirement-effect:" + string(retirement.OperationID), IdempotencyKey: operation.IdempotencyKey}, nil
}

func (o *RetirementRepairOwner) Verify(ctx context.Context, operation app.RepairOperation, plan app.RepairPlan) (app.RepairVerification, error) {
	retirement, err := o.loadPlanRetirement(ctx, plan)
	if err != nil {
		return app.RepairVerification{}, err
	}
	if retirement.Phase != app.WorkspaceRetirementRemoved {
		return app.RepairVerification{}, ErrRetirementRepairState
	}
	proof, err := o.executor.Verify(ctx, retirement)
	if err != nil || !proof.AlreadyRemoved {
		return app.RepairVerification{}, ErrRetirementRepairState
	}
	value, err := json.Marshal(struct {
		OperationID domain.OperationID
		Proof       app.WorkspaceRetirementProof
	}{retirement.OperationID, proof})
	if err != nil {
		return app.RepairVerification{}, err
	}
	digest := sha256.Sum256(value)
	return app.RepairVerification{PostconditionHash: hex.EncodeToString(digest[:]), AlreadyRepaired: true}, nil
}

func (o *RetirementRepairOwner) loadPlanRetirement(ctx context.Context, plan app.RepairPlan) (app.WorkspaceRetirement, error) {
	if o == nil || ctx == nil || plan.Validate() != nil || plan.HandlerKind != RetirementRepairHandlerKind || plan.HandlerVersion != RetirementRepairHandlerVersion || len(plan.OwnedResourceRefs) != 1 {
		return app.WorkspaceRetirement{}, ErrRetirementRepairReference
	}
	workspaceID, operationID, err := parseRetirementReference(plan.OwnedResourceRefs[0])
	if err != nil {
		return app.WorkspaceRetirement{}, err
	}
	retirement, err := o.store.LoadWorkspaceRetirement(ctx, workspaceID, operationID)
	if err != nil {
		return app.WorkspaceRetirement{}, err
	}
	if retirement.Candidate.WorkspaceID != workspaceID || retirement.OperationID != operationID {
		return app.WorkspaceRetirement{}, ErrRetirementRepairReference
	}
	return retirement, nil
}

func retirementReference(workspaceID domain.WorkspaceID, operationID domain.OperationID) (string, error) {
	if workspaceID == "" || operationID == "" || strings.ContainsAny(string(workspaceID)+string(operationID), "/\\\r\n") {
		return "", ErrRetirementRepairReference
	}
	return "workspace-retirement:" + string(workspaceID) + ":" + string(operationID), nil
}

func parseRetirementReference(value string) (domain.WorkspaceID, domain.OperationID, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "workspace-retirement" || parts[1] == "" || parts[2] == "" {
		return "", "", ErrRetirementRepairReference
	}
	return domain.WorkspaceID(parts[1]), domain.OperationID(parts[2]), nil
}

func retirementPreconditionHash(retirement app.WorkspaceRetirement) (string, error) {
	value, err := json.Marshal(struct {
		Candidate app.WorkspaceRetentionCandidate
		Decision  app.WorkspaceRetentionDecision
		Phase     app.WorkspaceRetirementPhase
	}{retirement.Candidate, retirement.Decision, retirement.Phase})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}
