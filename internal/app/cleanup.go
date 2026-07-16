package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const CleanupJournalVersion uint64 = 1

var (
	ErrCleanupInvalid      = errors.New("invalid cleanup value")
	ErrCleanupNotFound     = errors.New("cleanup operation not found")
	ErrCleanupConflict     = errors.New("cleanup operation conflict")
	ErrCleanupStalePlan    = errors.New("cleanup plan is stale")
	ErrCleanupConfirmation = errors.New("cleanup confirmation required")
)

// CleanupResourceKind identifies one owner-specific Nudge resource. It is a
// closed vocabulary so cleanup cannot turn a caller-supplied path into an
// effect selector.
type CleanupResourceKind string

const (
	CleanupResourceCapture        CleanupResourceKind = "capture"
	CleanupResourceReviewSnapshot CleanupResourceKind = "review_snapshot"
	CleanupResourceWorkspace      CleanupResourceKind = "workspace"
	CleanupResourceProposal       CleanupResourceKind = "proposal"
	CleanupResourceCache          CleanupResourceKind = "cache"
	CleanupResourceLog            CleanupResourceKind = "log"
)

func (k CleanupResourceKind) Validate() error {
	switch k {
	case CleanupResourceCapture, CleanupResourceReviewSnapshot, CleanupResourceWorkspace,
		CleanupResourceProposal, CleanupResourceCache, CleanupResourceLog:
		return nil
	default:
		return ErrCleanupInvalid
	}
}

// CleanupPhase is the durable phase boundary. The journal is deliberately
// outside repository rows so deletion can resume after those rows disappear.
type CleanupPhase string

const (
	CleanupPhasePrepared          CleanupPhase = "prepared"
	CleanupPhaseQuiesced          CleanupPhase = "quiesced"
	CleanupPhaseFilesystemRemoved CleanupPhase = "filesystem_removed"
	CleanupPhaseDatabaseRemoved   CleanupPhase = "database_removed"
	CleanupPhaseVerified          CleanupPhase = "verified"
	CleanupPhaseComplete          CleanupPhase = "complete"
	CleanupPhaseCleanupRequired   CleanupPhase = "cleanup_required"
)

func (p CleanupPhase) Validate() error {
	switch p {
	case CleanupPhasePrepared, CleanupPhaseQuiesced, CleanupPhaseFilesystemRemoved,
		CleanupPhaseDatabaseRemoved, CleanupPhaseVerified, CleanupPhaseComplete,
		CleanupPhaseCleanupRequired:
		return nil
	default:
		return ErrCleanupInvalid
	}
}

// CleanupOutcome is the terminal classification retained in the journal.
type CleanupOutcome string

const (
	CleanupOutcomeNone           CleanupOutcome = ""
	CleanupOutcomeSucceeded      CleanupOutcome = "succeeded"
	CleanupOutcomeAlreadyRemoved CleanupOutcome = "already_removed"
	CleanupOutcomeRequired       CleanupOutcome = "cleanup_required"
)

func (o CleanupOutcome) Validate() error {
	switch o {
	case CleanupOutcomeNone, CleanupOutcomeSucceeded, CleanupOutcomeAlreadyRemoved, CleanupOutcomeRequired:
		return nil
	default:
		return ErrCleanupInvalid
	}
}

// CleanupResource is a redacted, exact owner identity. Empty CanonicalPath is
// allowed only for a database-owned blocker; a filesystem effect must carry
// a canonical path and positive owner evidence.
type CleanupResource struct {
	ID             string
	Kind           CleanupResourceKind
	OwnerID        string
	RepositoryID   domain.RepositoryID
	CanonicalPath  string
	ParentRoot     string
	MarkerNonce    string
	ManifestHash   string
	NativeIdentity string
	Published      PublishedArtifact
}

func (r CleanupResource) Validate() error {
	if r.ID == "" || r.OwnerID == "" || r.RepositoryID == "" || r.Kind.Validate() != nil {
		return ErrCleanupInvalid
	}
	if r.CanonicalPath == "" {
		if r.Published.Identity.SpoolID != "" {
			if r.Kind != CleanupResourceCapture && r.Kind != CleanupResourceProposal || r.Published.Identity.Validate() != nil || r.Published.Limits.Validate() != nil || r.Published.Target.OwnerKind == "" {
				return ErrCleanupInvalid
			}
		}
		return nil
	}
	if !filepath.IsAbs(r.CanonicalPath) || filepath.Clean(r.CanonicalPath) != r.CanonicalPath || r.ParentRoot == "" || !filepath.IsAbs(r.ParentRoot) || filepath.Clean(r.ParentRoot) != r.ParentRoot || !pathContained(r.ParentRoot, r.CanonicalPath) {
		return ErrCleanupInvalid
	}
	if r.MarkerNonce == "" && r.ManifestHash == "" && r.NativeIdentity == "" {
		return ErrCleanupInvalid
	}
	return nil
}

// CleanupRowCounts is the explicit database deletion inventory. It contains
// counts only; deleted review/provider content is never copied into the plan.
type CleanupRowCounts struct {
	Repositories           uint64 `json:"repositories"`
	Worktrees              uint64 `json:"worktrees"`
	Sessions               uint64 `json:"sessions"`
	Generations            uint64 `json:"generations"`
	Threads                uint64 `json:"threads"`
	Messages               uint64 `json:"messages"`
	ProviderConversations  uint64 `json:"provider_conversations"`
	ProviderTurns          uint64 `json:"provider_turns"`
	ReviewSnapshots        uint64 `json:"review_snapshots"`
	ReviewSnapshotLeases   uint64 `json:"review_snapshot_leases"`
	ProposalWorkspaces     uint64 `json:"proposal_workspaces"`
	Proposals              uint64 `json:"proposals"`
	ProposalAttempts       uint64 `json:"proposal_attempts"`
	ProposalVersions       uint64 `json:"proposal_versions"`
	ProposalPatchArtifacts uint64 `json:"proposal_patch_artifacts"`
	ApplyOperations        uint64 `json:"apply_operations"`
	OwnedArtifacts         uint64 `json:"owned_artifacts"`
	CapacityReservations   uint64 `json:"capacity_reservations"`
	RepairPlans            uint64 `json:"repair_plans"`
	RepairOperations       uint64 `json:"repair_operations"`
}

// CleanupInventory is a single read-only repository snapshot used to build a
// confirmation-bound plan.
type CleanupInventory struct {
	RepositoryID      domain.RepositoryID
	RepositoryDisplay string
	ObservedRevision  string
	Rows              CleanupRowCounts
	Resources         []CleanupResource
	Exclusions        []string
	Effects           []string
	Blockers          []string
}

func (i CleanupInventory) Validate() error {
	if i.RepositoryID == "" || i.RepositoryDisplay == "" || !validCleanupHash(i.ObservedRevision) {
		return ErrCleanupInvalid
	}
	for _, resource := range i.Resources {
		if resource.Validate() != nil {
			return ErrCleanupInvalid
		}
	}
	return validateCleanupTextList(i.Exclusions)
}

// CleanupPlan is immutable after preview. Its manifest hash covers the exact
// resource and row inventory, while the plan ID identifies the confirmation.
type CleanupPlan struct {
	ID                string
	RepositoryID      domain.RepositoryID
	RepositoryDisplay string
	ObservedRevision  string
	ManifestHash      string
	Rows              CleanupRowCounts
	Resources         []CleanupResource
	Exclusions        []string
	Effects           []string
	Blockers          []string
	CreatedAt         time.Time
}

func (p CleanupPlan) Validate() error {
	if p.ID == "" || p.RepositoryID == "" || p.RepositoryDisplay == "" || !validCleanupHash(p.ObservedRevision) || !validCleanupHash(p.ManifestHash) || p.CreatedAt.IsZero() {
		return ErrCleanupInvalid
	}
	if err := validateCleanupTextList(p.Exclusions); err != nil {
		return err
	}
	if err := validateCleanupTextList(p.Effects); err != nil {
		return err
	}
	if err := validateCleanupTextList(p.Blockers); err != nil {
		return err
	}
	for _, resource := range p.Resources {
		if resource.Validate() != nil || resource.RepositoryID != p.RepositoryID {
			return ErrCleanupInvalid
		}
	}
	return nil
}

// NewCleanupPlan computes the immutable manifest identity from one inventory.
func NewCleanupPlan(id string, inventory CleanupInventory, now time.Time) (CleanupPlan, error) {
	if inventory.Validate() != nil || id == "" || now.IsZero() {
		return CleanupPlan{}, ErrCleanupInvalid
	}
	resources := append([]CleanupResource(nil), inventory.Resources...)
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].ID < resources[j].ID
	})
	plan := CleanupPlan{ID: id, RepositoryID: inventory.RepositoryID, RepositoryDisplay: inventory.RepositoryDisplay, ObservedRevision: inventory.ObservedRevision, Rows: inventory.Rows, Resources: resources, Exclusions: append([]string(nil), inventory.Exclusions...), Effects: append([]string(nil), inventory.Effects...), Blockers: append([]string(nil), inventory.Blockers...), CreatedAt: now.UTC()}
	manifest, err := cleanupManifestHash(plan)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan.ManifestHash = manifest
	return plan, plan.Validate()
}

// ManifestHash returns the revision-independent identity of this inventory.
// It is used by the store immediately before the destructive phase.
func (i CleanupInventory) ManifestHash() string {
	plan, err := NewCleanupPlan("inventory", i, time.Unix(1, 0).UTC())
	if err != nil {
		return ""
	}
	return plan.ManifestHash
}

// CleanupOperation is the durable journal record that survives repository-row
// deletion and can be resumed without guessing which phase completed.
type CleanupOperation struct {
	Version          uint64
	ID               domain.OperationID
	PlanID           string
	RepositoryID     domain.RepositoryID
	ManifestHash     string
	ObservedRevision string
	Phase            CleanupPhase
	Outcome          CleanupOutcome
	Attempt          uint64
	ErrorCode        string
	EvidenceHash     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      *time.Time
}

func (o CleanupOperation) Validate() error {
	if o.Version != CleanupJournalVersion || o.ID == "" || o.PlanID == "" || o.RepositoryID == "" || !validCleanupHash(o.ManifestHash) || !validCleanupHash(o.ObservedRevision) || o.Phase.Validate() != nil || o.Outcome.Validate() != nil || o.Attempt == 0 || o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		return ErrCleanupInvalid
	}
	if o.ErrorCode != "" && !cleanupToken(o.ErrorCode, 128) || o.EvidenceHash != "" && !validCleanupHash(o.EvidenceHash) {
		return ErrCleanupInvalid
	}
	if o.CompletedAt != nil && o.CompletedAt.Before(o.CreatedAt) {
		return ErrCleanupInvalid
	}
	return nil
}

// CleanupJournal is the durable administration boundary. Implementations must
// not place these records under repository foreign keys.
type CleanupJournal interface {
	SaveCleanupPlan(context.Context, CleanupPlan) error
	LoadCleanupPlan(context.Context, string) (CleanupPlan, error)
	SaveCleanupOperation(context.Context, CleanupOperation) error
	LoadCleanupOperation(context.Context, domain.OperationID) (CleanupOperation, error)
	LoadCleanupOperationByPlan(context.Context, string) (CleanupOperation, error)
}

// CleanupQuiescer acquires every existing repository session/owner lock in the
// stable order required by ADR-012. It is intentionally separate from the
// repository gate so the coordinator cannot accidentally claim a new session.
type CleanupQuiescer interface {
	Acquire(context.Context, domain.RepositoryID) (io.Closer, error)
}

// CleanupSessionLockTarget is the durable identity needed to reacquire one
// existing session lock without claiming a new writer epoch.
type CleanupSessionLockTarget struct {
	SessionID domain.ReviewSessionID
	Key       review.SessionKey
	LeaseID   domain.SessionLeaseID
	Distinct  bool
}

func (t CleanupSessionLockTarget) Validate() error {
	if t.SessionID == "" || t.LeaseID == "" || t.Key.Validate() != nil {
		return ErrCleanupInvalid
	}
	return nil
}

// CleanupSessionStore enumerates only durable session lock identities. It does
// not expose review content or grant a writer mutation capability.
type CleanupSessionStore interface {
	ListCleanupSessionLockTargets(context.Context, domain.RepositoryID) ([]CleanupSessionLockTarget, error)
}

// SessionLockQuiescer acquires existing session locks in stable ID order.
// Every acquired lock is held until Close, including when a later lock fails.
type SessionLockQuiescer struct {
	Store  CleanupSessionStore
	Leases SessionLeaseManager
}

func (q SessionLockQuiescer) Acquire(ctx context.Context, repositoryID domain.RepositoryID) (io.Closer, error) {
	if ctx == nil || q.Store == nil || q.Leases == nil || repositoryID == "" {
		return nil, ErrCleanupInvalid
	}
	targets, err := q.Store.ListCleanupSessionLockTargets(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].SessionID < targets[j].SessionID })
	locks := make([]SessionLease, 0, len(targets))
	for _, target := range targets {
		if target.Validate() != nil {
			closeSessionLeases(locks)
			return nil, ErrCleanupConflict
		}
		lease, err := q.Leases.Acquire(ctx, SessionLeaseRequest{Key: target.Key, SessionID: target.SessionID, LeaseID: target.LeaseID, Distinct: target.Distinct})
		if err != nil {
			closeSessionLeases(locks)
			return nil, err
		}
		locks = append(locks, lease)
	}
	return sessionLeaseSet{leases: locks}, nil
}

type sessionLeaseSet struct{ leases []SessionLease }

func (s sessionLeaseSet) Close() error { return closeSessionLeases(s.leases) }

func closeSessionLeases(leases []SessionLease) error {
	var first error
	for index := len(leases) - 1; index >= 0; index-- {
		if err := leases[index].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CleanupService coordinates confirmation, journal phases, and owner-specific
// effects. It has no generic filesystem or SQL mutation capability.
type CleanupService struct {
	Inventory CleanupInventoryStore
	Journal   CleanupJournal
	Gate      RepositoryMaintenanceGate
	Quiescer  CleanupQuiescer
	Owners    map[CleanupResourceKind]CleanupResourceOwner
	IDs       IDSource
	Clock     Clock
}

// CleanupResourceOwner is the narrow mutation seam for one exact resource
// class. Implementations revalidate marker, containment, and native identity.
type CleanupResourceOwner interface {
	Remove(context.Context, CleanupResource) error
}

// CleanupRequest is the only destructive cleanup input accepted by the app.
type CleanupRequest struct {
	PlanID       string
	Confirmation string
}

func (r CleanupRequest) Validate() error {
	if r.PlanID == "" || r.Confirmation != RepairConfirmationYes {
		return ErrCleanupConfirmation
	}
	return nil
}

// NewCleanupService validates the owner-specific cleanup composition.
func NewCleanupService(service CleanupService) (*CleanupService, error) {
	if service.Inventory == nil || service.Journal == nil || service.Gate == nil || service.Quiescer == nil {
		return nil, ErrCleanupInvalid
	}
	if service.IDs == nil {
		service.IDs = RandomIDSource{}
	}
	if service.Clock == nil {
		service.Clock = SystemClock{}
	}
	return &service, nil
}

// PlanRepositoryCleanup creates and durably records one exact read-only plan.
func (s *CleanupService) PlanRepositoryCleanup(ctx context.Context, repositoryID domain.RepositoryID) (CleanupPlan, error) {
	if s == nil || ctx == nil || repositoryID == "" {
		return CleanupPlan{}, ErrCleanupInvalid
	}
	inventory, err := s.Inventory.LoadCleanupInventory(ctx, repositoryID)
	if err != nil {
		return CleanupPlan{}, err
	}
	plan, err := NewCleanupPlan(s.IDs.NewID(), inventory, s.Clock.Now().UTC())
	if err != nil {
		return CleanupPlan{}, err
	}
	if err := s.Journal.SaveCleanupPlan(ctx, plan); err != nil {
		return CleanupPlan{}, err
	}
	return plan, nil
}

// Execute applies one exact, confirmed plan and leaves a resumable journal.
func (s *CleanupService) Execute(ctx context.Context, request CleanupRequest) (CleanupOperation, error) {
	if s == nil || ctx == nil || request.Validate() != nil {
		return CleanupOperation{}, ErrCleanupConfirmation
	}
	plan, err := s.Journal.LoadCleanupPlan(ctx, request.PlanID)
	if err != nil {
		return CleanupOperation{}, err
	}
	if plan.Validate() != nil {
		return CleanupOperation{}, ErrCleanupNotFound
	}
	current, err := s.Inventory.LoadCleanupInventory(ctx, plan.RepositoryID)
	if err != nil {
		if errors.Is(err, ErrCleanupNotFound) {
			return CleanupOperation{}, ErrCleanupStalePlan
		}
		return CleanupOperation{}, err
	}
	if current.ObservedRevision != plan.ObservedRevision || current.ManifestHash() != plan.ManifestHash {
		return CleanupOperation{}, ErrCleanupStalePlan
	}
	if len(plan.Blockers) != 0 {
		return CleanupOperation{}, ErrCleanupConflict
	}
	maintenance, err := s.Gate.Acquire(ctx, plan.RepositoryID)
	if err != nil {
		return CleanupOperation{}, err
	}
	defer maintenance.Close()
	quiesced, err := s.Quiescer.Acquire(ctx, plan.RepositoryID)
	if err != nil {
		return CleanupOperation{}, err
	}
	defer quiesced.Close()
	now := s.Clock.Now().UTC()
	operation, err := s.Journal.LoadCleanupOperationByPlan(ctx, plan.ID)
	if errors.Is(err, ErrCleanupNotFound) {
		operationID, idErr := domain.NewOperationID(s.IDs.NewID())
		if idErr != nil {
			return CleanupOperation{}, idErr
		}
		operation = CleanupOperation{Version: CleanupJournalVersion, ID: operationID, PlanID: plan.ID, RepositoryID: plan.RepositoryID, ManifestHash: plan.ManifestHash, ObservedRevision: plan.ObservedRevision, Phase: CleanupPhasePrepared, Attempt: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
			return CleanupOperation{}, err
		}
	} else if err != nil {
		return CleanupOperation{}, err
	}
	if operation.Phase == CleanupPhaseComplete {
		return operation, nil
	}
	operation.Phase = CleanupPhaseQuiesced
	operation.UpdatedAt = now
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	for _, resource := range plan.Resources {
		owner := s.Owners[resource.Kind]
		if owner == nil {
			return s.cleanupRequired(ctx, operation, "owner_unavailable")
		}
		if err := owner.Remove(ctx, resource); err != nil {
			return s.cleanupRequired(ctx, operation, cleanupErrorCode(err))
		}
	}
	operation.Phase = CleanupPhaseFilesystemRemoved
	operation.UpdatedAt = s.Clock.Now().UTC()
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	if err := s.Inventory.DeleteRepositoryRows(ctx, plan.RepositoryID, plan); err != nil {
		return s.cleanupRequired(ctx, operation, cleanupErrorCode(err))
	}
	operation.Phase = CleanupPhaseDatabaseRemoved
	operation.UpdatedAt = s.Clock.Now().UTC()
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	operation.Phase = CleanupPhaseVerified
	operation.UpdatedAt = s.Clock.Now().UTC()
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	completed := s.Clock.Now().UTC()
	operation.Phase = CleanupPhaseComplete
	operation.Outcome = CleanupOutcomeSucceeded
	operation.CompletedAt = &completed
	operation.UpdatedAt = completed
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	return operation, nil
}

func (s *CleanupService) cleanupRequired(ctx context.Context, operation CleanupOperation, code string) (CleanupOperation, error) {
	operation.Phase = CleanupPhaseCleanupRequired
	operation.Outcome = CleanupOutcomeRequired
	operation.ErrorCode = code
	operation.UpdatedAt = s.Clock.Now().UTC()
	if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
		return CleanupOperation{}, err
	}
	return operation, ErrCleanupConflict
}

func cleanupErrorCode(err error) string {
	if err == nil {
		return ""
	}
	code := strings.ReplaceAll(err.Error(), " ", "_")
	if len(code) > 128 {
		code = code[:128]
	}
	return code
}

// CleanupInventoryStore reads one consistent repository inventory and owns the
// explicit transactional database deletion order.
type CleanupInventoryStore interface {
	LoadCleanupInventory(context.Context, domain.RepositoryID) (CleanupInventory, error)
	DeleteRepositoryRows(context.Context, domain.RepositoryID, CleanupPlan) error
}

func cleanupManifestHash(plan CleanupPlan) (string, error) {
	resources := append([]CleanupResource(nil), plan.Resources...)
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].ID < resources[j].ID
	})
	payload := struct {
		RepositoryID     domain.RepositoryID
		ObservedRevision string
		Rows             CleanupRowCounts
		Resources        []CleanupResource
		Exclusions       []string
		Effects          []string
		Blockers         []string
	}{plan.RepositoryID, plan.ObservedRevision, plan.Rows, resources, plan.Exclusions, plan.Effects, plan.Blockers}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func pathContained(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != "" && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validCleanupHash(value string) bool {
	if len(value) != sha256.Size*2 || !cleanupToken(value, sha256.Size*2) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cleanupToken(value string, max int) bool {
	if value == "" || len(value) > max || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	return true
}

func validateCleanupTextList(values []string) error {
	if len(values) > 256 {
		return ErrCleanupInvalid
	}
	for _, value := range values {
		if !cleanupToken(value, 512) {
			return ErrCleanupInvalid
		}
	}
	return nil
}
