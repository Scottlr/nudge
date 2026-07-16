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

// DatabaseManifestHash identifies only repository rows and database-owned
// resource records. Repository-scoped logs are verified by their owner and
// are intentionally excluded from the SQL deletion CAS.
func (i CleanupInventory) DatabaseManifestHash() string {
	return cleanupDatabaseManifestHash(i.RepositoryID, i.Rows, i.Resources)
}

// DatabaseManifestHash identifies the database portion of one full plan.
func (p CleanupPlan) DatabaseManifestHash() string {
	return cleanupDatabaseManifestHash(p.RepositoryID, p.Rows, p.Resources)
}

func cleanupDatabaseManifestHash(repositoryID domain.RepositoryID, rows CleanupRowCounts, resources []CleanupResource) string {
	filtered := make([]CleanupResource, 0, len(resources))
	for _, resource := range resources {
		if resource.Kind != CleanupResourceLog {
			filtered = append(filtered, resource)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Kind != filtered[j].Kind {
			return filtered[i].Kind < filtered[j].Kind
		}
		return filtered[i].ID < filtered[j].ID
	})
	data, err := json.Marshal(struct {
		RepositoryID domain.RepositoryID
		Rows         CleanupRowCounts
		Resources    []CleanupResource
	}{repositoryID, rows, filtered})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// CleanupOperation is the durable journal record that survives repository-row
// deletion and can be resumed without guessing which phase completed.
type CleanupOperation struct {
	Version            uint64
	ID                 domain.OperationID
	PlanID             string
	RepositoryID       domain.RepositoryID
	ManifestHash       string
	ObservedRevision   string
	Phase              CleanupPhase
	Outcome            CleanupOutcome
	Attempt            uint64
	CompletedResources []string
	ErrorCode          string
	EvidenceHash       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

func (o CleanupOperation) Validate() error {
	if o.Version != CleanupJournalVersion || o.ID == "" || o.PlanID == "" || o.RepositoryID == "" || !validCleanupHash(o.ManifestHash) || !validCleanupHash(o.ObservedRevision) || o.Phase.Validate() != nil || o.Outcome.Validate() != nil || o.Attempt == 0 || o.CreatedAt.IsZero() || o.UpdatedAt.IsZero() || o.UpdatedAt.Before(o.CreatedAt) {
		return ErrCleanupInvalid
	}
	if len(o.CompletedResources) > 4096 || o.ErrorCode != "" && !cleanupToken(o.ErrorCode, 128) || o.EvidenceHash != "" && !validCleanupHash(o.EvidenceHash) {
		return ErrCleanupInvalid
	}
	for index, resourceID := range o.CompletedResources {
		if !cleanupToken(resourceID, 512) || index > 0 && resourceID <= o.CompletedResources[index-1] {
			return ErrCleanupInvalid
		}
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
	Inventory   CleanupInventoryStore
	Journal     CleanupJournal
	Gate        RepositoryMaintenanceGate
	Quiescer    CleanupQuiescer
	Enumerators []CleanupResourceEnumerator
	Owners      map[CleanupResourceKind]CleanupResourceOwner
	IDs         IDSource
	Clock       Clock
}

// CleanupResourceOwner is the narrow mutation seam for one exact resource
// class. Implementations revalidate marker, containment, and native identity.
type CleanupResourceOwner interface {
	Remove(context.Context, CleanupResource) error
}

// CleanupResourceEnumerator adds resources owned outside the repository
// database, such as repository-scoped protected logs. It returns only exact
// owner-backed resources and redacted blockers.
type CleanupResourceEnumerator interface {
	Resources(context.Context, domain.RepositoryID) ([]CleanupResource, []string, error)
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
	for _, enumerator := range s.Enumerators {
		if enumerator == nil {
			return CleanupPlan{}, ErrCleanupInvalid
		}
		resources, blockers, enumErr := enumerator.Resources(ctx, repositoryID)
		if enumErr != nil {
			return CleanupPlan{}, enumErr
		}
		inventory.Resources = append(inventory.Resources, resources...)
		inventory.Blockers = append(inventory.Blockers, blockers...)
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
	operation, operationErr := s.Journal.LoadCleanupOperationByPlan(ctx, plan.ID)
	operationExists := operationErr == nil
	if operationErr != nil && !errors.Is(operationErr, ErrCleanupNotFound) {
		return CleanupOperation{}, operationErr
	}
	if operationExists && operation.Phase == CleanupPhaseComplete {
		return operation, nil
	}
	if len(plan.Blockers) != 0 {
		return CleanupOperation{}, ErrCleanupConflict
	}
	current, currentErr := s.Inventory.LoadCleanupInventory(ctx, plan.RepositoryID)
	if currentErr != nil && !(operationExists && cleanupDatabasePhase(operation.Phase) && errors.Is(currentErr, ErrCleanupNotFound)) {
		if errors.Is(currentErr, ErrCleanupNotFound) {
			return CleanupOperation{}, ErrCleanupStalePlan
		}
		return CleanupOperation{}, currentErr
	}
	if currentErr == nil && !cleanupPlanMatches(plan, current, operation.CompletedResources) {
		return CleanupOperation{}, ErrCleanupStalePlan
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
	current, currentErr = s.Inventory.LoadCleanupInventory(ctx, plan.RepositoryID)
	if currentErr != nil && !(operationExists && cleanupDatabasePhase(operation.Phase) && errors.Is(currentErr, ErrCleanupNotFound)) {
		if errors.Is(currentErr, ErrCleanupNotFound) {
			return CleanupOperation{}, ErrCleanupStalePlan
		}
		return CleanupOperation{}, currentErr
	}
	if currentErr == nil && !cleanupPlanMatches(plan, current, operation.CompletedResources) {
		return CleanupOperation{}, ErrCleanupStalePlan
	}
	now := s.Clock.Now().UTC()
	if !operationExists {
		operationID, idErr := domain.NewOperationID(s.IDs.NewID())
		if idErr != nil {
			return CleanupOperation{}, idErr
		}
		operation = CleanupOperation{Version: CleanupJournalVersion, ID: operationID, PlanID: plan.ID, RepositoryID: plan.RepositoryID, ManifestHash: plan.ManifestHash, ObservedRevision: plan.ObservedRevision, Phase: CleanupPhasePrepared, Attempt: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
			return CleanupOperation{}, err
		}
	} else {
		operation.Attempt++
		operation.Outcome = CleanupOutcomeNone
		operation.ErrorCode = ""
	}
	if operation.Phase != CleanupPhaseFilesystemRemoved && operation.Phase != CleanupPhaseDatabaseRemoved && operation.Phase != CleanupPhaseVerified {
		operation.Phase = CleanupPhaseQuiesced
		operation.UpdatedAt = now
		if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
			return CleanupOperation{}, err
		}
	}
	if operation.Phase == CleanupPhaseQuiesced || operation.Phase == CleanupPhaseCleanupRequired {
		for _, resource := range plan.Resources {
			if cleanupResourceCompleted(operation.CompletedResources, resource) {
				continue
			}
			owner := s.Owners[resource.Kind]
			if owner == nil {
				return s.cleanupRequired(ctx, operation, "owner_unavailable")
			}
			if err := owner.Remove(ctx, resource); err != nil {
				return s.cleanupRequired(ctx, operation, cleanupErrorCode(err))
			}
			operation.CompletedResources = append(operation.CompletedResources, cleanupResourceID(resource))
			sort.Strings(operation.CompletedResources)
			operation.UpdatedAt = s.Clock.Now().UTC()
			if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
				return CleanupOperation{}, err
			}
		}
		operation.Phase = CleanupPhaseFilesystemRemoved
		operation.UpdatedAt = s.Clock.Now().UTC()
		if err := s.Journal.SaveCleanupOperation(ctx, operation); err != nil {
			return CleanupOperation{}, err
		}
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

func cleanupResourceID(resource CleanupResource) string {
	return string(resource.Kind) + ":" + resource.ID
}

func cleanupResourceCompleted(completed []string, resource CleanupResource) bool {
	key := cleanupResourceID(resource)
	index := sort.SearchStrings(completed, key)
	return index < len(completed) && completed[index] == key
}

func cleanupDatabasePhase(phase CleanupPhase) bool {
	return phase == CleanupPhaseFilesystemRemoved || phase == CleanupPhaseDatabaseRemoved || phase == CleanupPhaseVerified
}

func cleanupPlanMatches(plan CleanupPlan, current CleanupInventory, completed []string) bool {
	if current.DatabaseManifestHash() != plan.DatabaseManifestHash() {
		return false
	}
	if len(completed) == 0 {
		return current.ObservedRevision == plan.ObservedRevision && current.ManifestHash() == plan.ManifestHash
	}
	planned := make(map[string]CleanupResource, len(plan.Resources))
	for _, resource := range plan.Resources {
		planned[cleanupResourceID(resource)] = resource
	}
	seen := make(map[string]struct{}, len(current.Resources))
	for _, resource := range current.Resources {
		key := cleanupResourceID(resource)
		plannedResource, ok := planned[key]
		if !ok || plannedResource != resource {
			return false
		}
		seen[key] = struct{}{}
	}
	for _, resource := range plan.Resources {
		if _, ok := seen[cleanupResourceID(resource)]; !ok && !cleanupResourceCompleted(completed, resource) {
			return false
		}
	}
	return len(current.Blockers) == 0
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
