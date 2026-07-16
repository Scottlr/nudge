package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

const (
	// OwnedStorageReconciliationVersion identifies the additive reconciliation
	// epoch and evidence contract.
	OwnedStorageReconciliationVersion uint32 = 1
	// DefaultOwnedStorageReconcileItems is the bounded default for one batch.
	DefaultOwnedStorageReconcileItems uint32 = 100
	// MaxOwnedStorageReconcileItems prevents a caller from turning one health
	// query into an unbounded ledger walk.
	MaxOwnedStorageReconcileItems uint32 = 1000
)

var (
	ErrInvalidOwnedStorageReconcile  = errors.New("invalid owned-storage reconciliation request")
	ErrOwnedStorageReconcileDrift    = errors.New("owned-storage ledger changed during reconciliation")
	ErrOwnedStorageReconcileConflict = errors.New("owned-storage reconciliation conflict")
)

// OwnedStorageDiscrepancyKind identifies one evidence mismatch. The kind is
// intentionally independent from any owner-specific repair effect.
type OwnedStorageDiscrepancyKind string

const (
	ArtifactMissing       OwnedStorageDiscrepancyKind = "artifact_missing"
	LedgerEntryMissing    OwnedStorageDiscrepancyKind = "ledger_entry_missing"
	ManifestMismatch      OwnedStorageDiscrepancyKind = "manifest_mismatch"
	ObservedSizeMismatch  OwnedStorageDiscrepancyKind = "observed_size_mismatch"
	ReservationStale      OwnedStorageDiscrepancyKind = "reservation_stale"
	OwnedTemporaryResidue OwnedStorageDiscrepancyKind = "owned_temporary_residue"
	OwnershipUncertain    OwnedStorageDiscrepancyKind = "ownership_uncertain"
)

func (k OwnedStorageDiscrepancyKind) Validate() error {
	switch k {
	case ArtifactMissing, LedgerEntryMissing, ManifestMismatch, ObservedSizeMismatch,
		ReservationStale, OwnedTemporaryResidue, OwnershipUncertain:
		return nil
	default:
		return ErrInvalidOwnedStorageReconcile
	}
}

// ReconcileMode controls whether the bounded result may advance a durable
// reconciliation epoch. Query-only mode never writes an epoch or ledger row.
type ReconcileMode string

const (
	ReconcileQueryOnly ReconcileMode = "query_only"
	ReconcileAdvance   ReconcileMode = "advance"
)

func (m ReconcileMode) Validate() error {
	if m != ReconcileQueryOnly && m != ReconcileAdvance {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageReconcileRequest bounds one durable-ledger reconciliation batch.
type OwnedStorageReconcileRequest struct {
	RepositoryID           *domain.RepositoryID
	Cursor                 string
	MaxItems               int
	Mode                   ReconcileMode
	ExpectedLedgerRevision uint64
	Volumes                []VolumeEvidence
}

func (r OwnedStorageReconcileRequest) Validate() error {
	if r.RepositoryID != nil && *r.RepositoryID == "" || r.Cursor != "" && !stableStorageText(r.Cursor) || r.MaxItems < 0 || r.MaxItems > int(MaxOwnedStorageReconcileItems) || r.Mode != "" && r.Mode.Validate() != nil {
		return ErrInvalidOwnedStorageReconcile
	}
	for _, volume := range r.Volumes {
		if !validVolumeEvidence(volume) {
			return ErrInvalidOwnedStorageReconcile
		}
	}
	return nil
}

// OwnedStorageLedgerPage is one consistent SQLite read transaction. The
type OwnedStorageLedgerPage struct {
	Revision           uint64
	Repository         StorageTotals
	Global             StorageTotals
	Pressure           StoragePressureState
	ActiveReservations Count
	Artifacts          []OwnedArtifact
	Reservations       []CapacityReservationSummary
	NextCursor         string
	Complete           bool
}

// OwnedStorageReconcileLedger is a separate consumer port so existing ledger
// users do not gain pagination or filesystem responsibilities accidentally.
type OwnedStorageReconcileLedger interface {
	ReconciliationPage(context.Context, OwnedStorageLedgerPageQuery) (OwnedStorageLedgerPage, error)
}

type OwnedStorageLedgerPageQuery struct {
	RepositoryID *domain.RepositoryID
	Cursor       string
	Limit        uint32
}

func (q OwnedStorageLedgerPageQuery) Validate() error {
	if q.RepositoryID != nil && *q.RepositoryID == "" || q.Cursor != "" && !stableStorageText(q.Cursor) || q.Limit == 0 || q.Limit > MaxOwnedStorageReconcileItems {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageDiscrepancy is a redacted, stable finding suitable for a repair
// plan reference. It contains no filesystem path or source bytes.
type OwnedStorageDiscrepancy struct {
	Kind                 OwnedStorageDiscrepancyKind
	OwnerKind            OwnerKind
	OwnerID              string
	ArtifactID           string
	ReservationID        string
	RepositoryID         *domain.RepositoryID
	VolumeID             string
	MarkerNonce          string
	ExpectedManifestHash string
	ObservedManifestHash string
	ExpectedBytes        ByteSize
	ObservedBytes        ByteSize
	EvidenceCode         string
	PlanEligible         bool
	HandlerKind          RepairHandlerKind
	HandlerVersion       string
	PreconditionsHash    string
}

func (d OwnedStorageDiscrepancy) Validate() error {
	if d.Kind.Validate() != nil || !validOwnerKind(d.OwnerKind) || !stableStorageText(d.OwnerID) || d.ArtifactID != "" && !stableStorageText(d.ArtifactID) || d.ReservationID != "" && !stableStorageText(d.ReservationID) || d.RepositoryID != nil && *d.RepositoryID == "" || !stableStorageText(d.VolumeID) || d.MarkerNonce != "" && !stableStorageText(d.MarkerNonce) || d.ExpectedManifestHash != "" && !validSHA256Text(d.ExpectedManifestHash) || d.ObservedManifestHash != "" && !validSHA256Text(d.ObservedManifestHash) || !stableStorageText(d.EvidenceCode) || d.HandlerKind != "" && !validRepairToken(string(d.HandlerKind), maxRepairHandlerKindBytes) || d.HandlerVersion != "" && !validRepairToken(d.HandlerVersion, maxRepairHandlerVersionBytes) || d.PreconditionsHash != "" && !validRepairHash(d.PreconditionsHash) {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// CapacityScopeHealth is a conservative projection of one ledger scope and
// its supplied path-free volume observations.
type CapacityScopeHealth struct {
	LogicalBytes     ByteSize
	ObservedBytes    ByteSize
	ChargedBytes     ByteSize
	ReservedBytes    ByteSize
	FreeBytes        ByteSize
	ProtectedReserve ByteSize
	SoftLimit        ByteSize
	HardLimit        ByteSize
	Pressure         StoragePressure
}

// CapacityHealthStatus is the actionable aggregate outcome for T049/T079.
type CapacityHealthStatus string

const (
	CapacityHealthOK                  CapacityHealthStatus = "ok"
	CapacityHealthStoragePressure     CapacityHealthStatus = "storage_pressure"
	CapacityHealthHardLimit           CapacityHealthStatus = "hard_limit"
	CapacityHealthAccountingUncertain CapacityHealthStatus = "accounting_uncertain"
)

// CapacityHealth is query-safe and contains no exact paths or artifact bytes.
type CapacityHealth struct {
	Repository         CapacityScopeHealth
	Global             CapacityScopeHealth
	ActiveReservations Count
	UncertaintyCount   Count
	Epoch              string
	Cursor             string
	LedgerRevision     uint64
	Complete           bool
	Status             CapacityHealthStatus
}

// CapacityHealthFromLedger projects a consistent T067 snapshot into the T079
// health shape. V1 has no owner-inspection phase, so ledger-only health is
// always accounting-uncertain even when the bounded ledger page is complete.
func CapacityHealthFromLedger(snapshot StorageLedgerSnapshot, volumes []VolumeEvidence, epoch, cursor string, complete bool) CapacityHealth {
	page := OwnedStorageLedgerPage{Revision: snapshot.Revision, Repository: snapshot.Repository, Global: snapshot.Global, Pressure: snapshot.Pressure}
	health := capacityHealth(page, volumes, epoch, cursor, complete)
	health.ActiveReservations = snapshot.ActiveReservations
	health.Status = CapacityHealthAccountingUncertain
	health.UncertaintyCount, _ = health.UncertaintyCount.Add(1)
	return health
}

// StorageHealthResult converts the bounded storage projection into one
// redacted doctor finding. Counts and revisions are safe; paths and artifact
// identities are intentionally absent.
func StorageHealthResult(health CapacityHealth) HealthResult {
	severity := HealthWarning
	summary := "Owned-storage accounting is uncertain until reconciliation completes."
	switch health.Status {
	case CapacityHealthOK:
		severity, summary = HealthOK, "Owned-storage accounting is healthy."
	case CapacityHealthStoragePressure:
		severity, summary = HealthWarning, "Owned-storage capacity is under soft pressure."
	case CapacityHealthHardLimit:
		severity, summary = HealthError, "Owned-storage capacity is at its hard limit."
	}
	evidence := fmt.Sprintf("status=%s,complete=%t,ledger_revision=%d,uncertainty=%d,reservations=%d", health.Status, health.Complete, health.LedgerRevision, health.UncertaintyCount, health.ActiveReservations)
	return HealthResult{Code: HealthStorageReconciliation, Severity: severity, Summary: summary, RedactedEvidence: evidence}
}

// OwnedStorageRepairCandidate identifies a future T095/T101/T102 effect. T079
// emits candidates only; it never executes or mutates one.
type OwnedStorageRepairCandidate struct {
	Kind                 OwnedStorageDiscrepancyKind
	HandlerKind          RepairHandlerKind
	HandlerVersion       string
	ArtifactID           string
	ReservationID        string
	OwnerID              string
	MarkerNonce          string
	ExpectedManifestHash string
	LedgerRevision       uint64
	PreconditionsHash    string
}

func (c OwnedStorageRepairCandidate) Validate() error {
	if c.Kind.Validate() != nil || !validRepairToken(string(c.HandlerKind), maxRepairHandlerKindBytes) || !validRepairToken(c.HandlerVersion, maxRepairHandlerVersionBytes) || c.ArtifactID == "" && c.ReservationID == "" || c.ArtifactID != "" && !stableStorageText(c.ArtifactID) || c.ReservationID != "" && !stableStorageText(c.ReservationID) || !stableStorageText(c.OwnerID) || c.MarkerNonce != "" && !stableStorageText(c.MarkerNonce) || c.ExpectedManifestHash != "" && !validSHA256Text(c.ExpectedManifestHash) || c.LedgerRevision == 0 || !validRepairHash(c.PreconditionsHash) {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageReconciliationEpoch is the durable bounded progress pointer.
type OwnedStorageReconciliationEpoch struct {
	Epoch            string
	RepositoryID     *domain.RepositoryID
	LedgerRevision   uint64
	PolicyVersion    ResourcePolicyVersion
	Cursor           string
	NextCursor       string
	BatchKey         string
	ProcessedItems   Count
	DiscrepancyCount Count
	EvidenceBytes    ByteSize
	UncertaintyCount Count
	Complete         bool
	UpdatedAt        time.Time
}

func (e OwnedStorageReconciliationEpoch) Validate() error {
	if !validRepairHash(e.Epoch) || e.RepositoryID != nil && *e.RepositoryID == "" || e.PolicyVersion == 0 || e.Cursor != "" && !stableStorageText(e.Cursor) || e.NextCursor != "" && !stableStorageText(e.NextCursor) || !validRepairHash(e.BatchKey) || e.UpdatedAt.IsZero() {
		return ErrInvalidOwnedStorageReconcile
	}
	if e.Complete && e.NextCursor != "" || !e.Complete && e.NextCursor == "" {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageReconciliationStore persists only bounded redacted progress and
// discrepancies. It does not own a repair effect.
type OwnedStorageReconciliationStore interface {
	SaveOwnedStorageReconciliation(context.Context, OwnedStorageReconciliationEpoch, []OwnedStorageDiscrepancy) error
	LoadOwnedStorageReconciliation(context.Context, string) (OwnedStorageReconciliationEpoch, []OwnedStorageDiscrepancy, error)
}

// OwnedStorageReconcileReport is one immutable application projection.
type OwnedStorageReconcileReport struct {
	Epoch          string
	HealthRevision string
	LedgerRevision uint64
	Discrepancies  []OwnedStorageDiscrepancy
	Capacity       CapacityHealth
	Candidates     []OwnedStorageRepairCandidate
	NextCursor     string
	Complete       bool
}

func (r OwnedStorageReconcileReport) Validate() error {
	if !validRepairHash(r.Epoch) || !validRepairHash(r.HealthRevision) || r.NextCursor != "" && !stableStorageText(r.NextCursor) || r.Complete && r.NextCursor != "" || !r.Complete && r.NextCursor == "" {
		return ErrInvalidOwnedStorageReconcile
	}
	for _, discrepancy := range r.Discrepancies {
		if err := discrepancy.Validate(); err != nil {
			return err
		}
	}
	for _, candidate := range r.Candidates {
		if err := candidate.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// OwnedStorageReconciler reconciles the durable ledger in bounded batches. V1
// deliberately does not inspect owner files or advertise owner-specific repair.
type OwnedStorageReconciler struct {
	Ledger OwnedStorageReconcileLedger
	Store  OwnedStorageReconciliationStore
	Policy ResourcePolicy
	Clock  Clock
}

func NewOwnedStorageReconciler(ledger OwnedStorageReconcileLedger, store OwnedStorageReconciliationStore, policy ResourcePolicy, clock Clock) (*OwnedStorageReconciler, error) {
	if ledger == nil || policy.Validate() != nil {
		return nil, ErrInvalidOwnedStorageReconcile
	}
	if clock == nil {
		clock = SystemClock{}
	}
	return &OwnedStorageReconciler{Ledger: ledger, Store: store, Policy: policy, Clock: clock}, nil
}

func (r *OwnedStorageReconciler) Reconcile(ctx context.Context, request OwnedStorageReconcileRequest) (OwnedStorageReconcileReport, error) {
	if request.Mode == "" {
		request.Mode = ReconcileQueryOnly
	}
	if request.MaxItems == 0 {
		request.MaxItems = int(DefaultOwnedStorageReconcileItems)
	}
	if r == nil || ctx == nil || request.Validate() != nil || request.Mode == ReconcileAdvance && r.Store == nil {
		return OwnedStorageReconcileReport{}, ErrInvalidOwnedStorageReconcile
	}
	if err := ctx.Err(); err != nil {
		return OwnedStorageReconcileReport{}, err
	}
	limit := uint32(request.MaxItems)
	page, err := r.Ledger.ReconciliationPage(ctx, OwnedStorageLedgerPageQuery{RepositoryID: request.RepositoryID, Cursor: request.Cursor, Limit: limit})
	if err != nil {
		return OwnedStorageReconcileReport{}, err
	}
	if page.Revision < request.ExpectedLedgerRevision {
		return OwnedStorageReconcileReport{}, ErrOwnedStorageReconcileDrift
	}
	if request.ExpectedLedgerRevision != 0 && page.Revision != request.ExpectedLedgerRevision {
		return OwnedStorageReconcileReport{}, ErrOwnedStorageReconcileDrift
	}
	if page.Repository.RepositoryID == nil && request.RepositoryID != nil {
		page.Repository.RepositoryID = request.RepositoryID
	}
	epochID := reconciliationEpochID(request.RepositoryID, page.Revision, r.Policy.Version)
	discrepancies := []OwnedStorageDiscrepancy(nil)
	candidates := []OwnedStorageRepairCandidate(nil)
	nextCursor := page.NextCursor
	complete := page.Complete
	capacity := capacityHealth(page, request.Volumes, epochID, request.Cursor, complete)
	capacity.Status = CapacityHealthAccountingUncertain
	capacity.UncertaintyCount, _ = capacity.UncertaintyCount.Add(1)
	batchKey := reconciliationBatchID(epochID, request.Cursor, nextCursor, page.Revision, discrepancies)
	epoch := OwnedStorageReconciliationEpoch{Epoch: epochID, RepositoryID: request.RepositoryID, LedgerRevision: page.Revision, PolicyVersion: r.Policy.Version, Cursor: request.Cursor, NextCursor: nextCursor, BatchKey: batchKey, ProcessedItems: Count(len(page.Artifacts) + len(page.Reservations)), DiscrepancyCount: 0, EvidenceBytes: 0, UncertaintyCount: 1, Complete: complete, UpdatedAt: r.Clock.Now().UTC()}
	if err := epoch.Validate(); err != nil {
		return OwnedStorageReconcileReport{}, err
	}
	healthRevision := reconciliationHealthID(epoch, capacity, discrepancies, candidates)
	report := OwnedStorageReconcileReport{Epoch: epochID, HealthRevision: healthRevision, LedgerRevision: page.Revision, Discrepancies: discrepancies, Capacity: capacity, Candidates: candidates, NextCursor: nextCursor, Complete: complete}
	if err := report.Validate(); err != nil {
		return OwnedStorageReconcileReport{}, err
	}
	if request.Mode == ReconcileAdvance {
		if err := r.Store.SaveOwnedStorageReconciliation(ctx, epoch, discrepancies); err != nil {
			return OwnedStorageReconcileReport{}, err
		}
	}
	return report, nil
}

func capacityHealth(page OwnedStorageLedgerPage, volumes []VolumeEvidence, epoch, cursor string, complete bool) CapacityHealth {
	policy := DefaultResourcePolicy()
	uncertainty, _ := page.Repository.UncertainCount.Add(page.Global.UncertainCount)
	result := CapacityHealth{Epoch: epoch, Cursor: cursor, LedgerRevision: page.Revision, Complete: complete, ActiveReservations: page.ActiveReservations, UncertaintyCount: uncertainty}
	result.Repository = scopeHealth(page.Repository, policy.Storage.RepositorySoftBytes, policy.Storage.RepositoryHardBytes, policy.Storage.MinimumFreeBytes, volumes)
	result.Global = scopeHealth(page.Global, policy.Storage.GlobalSoftBytes, policy.Storage.GlobalHardBytes, policy.Storage.MinimumFreeBytes, volumes)
	return result
}

func scopeHealth(totals StorageTotals, soft, hard, reserve ByteSize, volumes []VolumeEvidence) CapacityScopeHealth {
	free := ByteSize(0)
	haveFree := false
	for _, volume := range volumes {
		if !haveFree || volume.Free < free {
			free = volume.Free
			haveFree = true
		}
	}
	used, err := totals.ChargedBytes.Add(totals.ReservedBytes)
	if err != nil {
		return CapacityScopeHealth{LogicalBytes: totals.LogicalBytes, ObservedBytes: totals.ObservedBytes, ChargedBytes: totals.ChargedBytes, ReservedBytes: totals.ReservedBytes, FreeBytes: free, ProtectedReserve: reserve, SoftLimit: soft, HardLimit: hard, Pressure: StoragePressureHard}
	}
	pressure, err := ClassifyStoragePressure(used, soft, hard)
	if err != nil {
		pressure = StoragePressureHard
	}
	return CapacityScopeHealth{LogicalBytes: totals.LogicalBytes, ObservedBytes: totals.ObservedBytes, ChargedBytes: totals.ChargedBytes, ReservedBytes: totals.ReservedBytes, FreeBytes: free, ProtectedReserve: reserve, SoftLimit: soft, HardLimit: hard, Pressure: pressure}
}

func reconciliationEpochID(repositoryID *domain.RepositoryID, revision uint64, policy ResourcePolicyVersion) string {
	value := fmt.Sprintf("%s\x00%d\x00%d", repositoryValue(repositoryID), revision, policy)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func reconciliationBatchID(epoch, cursor, next string, revision uint64, discrepancies []OwnedStorageDiscrepancy) string {
	hash := sha256.New()
	for _, value := range []string{epoch, cursor, next, fmt.Sprint(revision)} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	ordered := append([]OwnedStorageDiscrepancy(nil), discrepancies...)
	sort.SliceStable(ordered, func(i, j int) bool { return discrepancyKey(ordered[i]) < discrepancyKey(ordered[j]) })
	for _, discrepancy := range ordered {
		_, _ = hash.Write([]byte(discrepancyKey(discrepancy)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func reconciliationHealthID(epoch OwnedStorageReconciliationEpoch, capacity CapacityHealth, discrepancies []OwnedStorageDiscrepancy, candidates []OwnedStorageRepairCandidate) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s\x00%s\x00%t\x00%d\x00%d", epoch.Epoch, epoch.LedgerRevision, epoch.NextCursor, capacity.Status, epoch.Complete, len(discrepancies), len(candidates))
	for _, discrepancy := range discrepancies {
		_, _ = hash.Write([]byte(discrepancyKey(discrepancy)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func discrepancyKey(discrepancy OwnedStorageDiscrepancy) string {
	return strings.Join([]string{string(discrepancy.Kind), string(discrepancy.OwnerKind), discrepancy.OwnerID, discrepancy.ArtifactID, discrepancy.ReservationID, discrepancy.VolumeID, discrepancy.ExpectedManifestHash, discrepancy.ObservedManifestHash, fmt.Sprint(discrepancy.ExpectedBytes), fmt.Sprint(discrepancy.ObservedBytes), discrepancy.EvidenceCode, fmt.Sprint(discrepancy.PlanEligible)}, "\x00")
}

func repositoryValue(repositoryID *domain.RepositoryID) string {
	if repositoryID == nil {
		return "global"
	}
	return string(*repositoryID)
}

func validVolumeEvidence(volume VolumeEvidence) bool {
	return stableStorageText(volume.ID) && (volume.Mode == VolumeCapacityMonitored || volume.Mode == VolumeCapacityHard) && volume.Stable && !volume.Observed.IsZero()
}
