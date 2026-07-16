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
	// MaxOwnedStorageReconcileEvidenceBytes bounds owner metadata returned to a
	// single application reconciliation batch.
	MaxOwnedStorageReconcileEvidenceBytes ByteSize = 4 * MiB
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

// OwnedStorageReconcileRequest bounds one ledger/owner evidence batch.
type OwnedStorageReconcileRequest struct {
	RepositoryID           *domain.RepositoryID
	Cursor                 string
	MaxItems               int
	MaxEvidenceBytes       int64
	Mode                   ReconcileMode
	ExpectedLedgerRevision uint64
	Volumes                []VolumeEvidence
}

func (r OwnedStorageReconcileRequest) Validate() error {
	if r.RepositoryID != nil && *r.RepositoryID == "" || r.Cursor != "" && !stableStorageText(r.Cursor) || r.MaxItems < 0 || r.MaxItems > int(MaxOwnedStorageReconcileItems) || r.MaxEvidenceBytes < 0 || r.MaxEvidenceBytes > int64(MaxOwnedStorageReconcileEvidenceBytes) || r.Mode != "" && r.Mode.Validate() != nil {
		return ErrInvalidOwnedStorageReconcile
	}
	for _, volume := range r.Volumes {
		if !validVolumeEvidence(volume) {
			return ErrInvalidOwnedStorageReconcile
		}
	}
	return nil
}

// OwnedStorageEvidenceState describes the owner-side state without exposing
// a path, marker file, or artifact bytes.
type OwnedStorageEvidenceState string

const (
	OwnedStorageEvidenceAccepted   OwnedStorageEvidenceState = "accepted"
	OwnedStorageEvidenceMissing    OwnedStorageEvidenceState = "missing"
	OwnedStorageEvidenceTemporary  OwnedStorageEvidenceState = "temporary"
	OwnedStorageEvidenceFilesystem OwnedStorageEvidenceState = "filesystem_only"
	OwnedStorageEvidenceUncertain  OwnedStorageEvidenceState = "uncertain"
)

func (s OwnedStorageEvidenceState) Validate() error {
	switch s {
	case OwnedStorageEvidenceAccepted, OwnedStorageEvidenceMissing,
		OwnedStorageEvidenceTemporary, OwnedStorageEvidenceFilesystem,
		OwnedStorageEvidenceUncertain:
		return nil
	default:
		return ErrInvalidOwnedStorageReconcile
	}
}

// OwnedStorageArtifactEvidence is the redacted owner proof used by T079.
// MarkerNonce and manifest hashes are identities, never source content.
type OwnedStorageArtifactEvidence struct {
	ArtifactID      string
	OwnerKind       OwnerKind
	OwnerID         string
	ReservationID   string
	RepositoryID    *domain.RepositoryID
	VolumeID        string
	ManifestHash    string
	MarkerNonce     string
	ObservedBytes   ByteSize
	EvidenceBytes   ByteSize
	State           OwnedStorageEvidenceState
	Complete        bool
	Rebuildable     bool
	QuarantineReady bool
	UncertaintyCode string
}

func (e OwnedStorageArtifactEvidence) Validate() error {
	if e.ArtifactID != "" && !stableStorageText(e.ArtifactID) || !validOwnerKind(e.OwnerKind) || !stableStorageText(e.OwnerID) || e.ReservationID != "" && !stableStorageText(e.ReservationID) || e.RepositoryID != nil && *e.RepositoryID == "" || !stableStorageText(e.VolumeID) || e.ManifestHash != "" && !validSHA256Text(e.ManifestHash) || e.MarkerNonce != "" && !stableStorageText(e.MarkerNonce) || e.State.Validate() != nil || e.EvidenceBytes == 0 || e.EvidenceBytes > MaxOwnedStorageReconcileEvidenceBytes || e.UncertaintyCode != "" && !stableStorageText(e.UncertaintyCode) {
		return ErrInvalidOwnedStorageReconcile
	}
	if e.State == OwnedStorageEvidenceAccepted && (!e.Complete || e.ArtifactID == "" || e.ManifestHash == "") {
		return ErrInvalidOwnedStorageReconcile
	}
	if e.State == OwnedStorageEvidenceUncertain && e.UncertaintyCode == "" {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageReservationEvidence is the bounded proof used to distinguish a
// live reservation from a stale marker. It never trusts elapsed time or PID.
type OwnedStorageReservationEvidence struct {
	ReservationID   string
	OwnerKind       OwnerKind
	OwnerID         string
	MarkerNonce     string
	LeaseActive     bool
	EvidenceBytes   ByteSize
	UncertaintyCode string
}

func (e OwnedStorageReservationEvidence) Validate() error {
	if !stableStorageText(e.ReservationID) || !validOwnerKind(e.OwnerKind) || !stableStorageText(e.OwnerID) || e.MarkerNonce != "" && !stableStorageText(e.MarkerNonce) || e.EvidenceBytes == 0 || e.EvidenceBytes > MaxOwnedStorageReconcileEvidenceBytes || e.UncertaintyCode != "" && !stableStorageText(e.UncertaintyCode) {
		return ErrInvalidOwnedStorageReconcile
	}
	return nil
}

// OwnedStorageDiscoveryRequest constrains owner-side root enumeration. The
// owner may return marker-bound identities only; it cannot return a path.
type OwnedStorageDiscoveryRequest struct {
	RepositoryID     *domain.RepositoryID
	Cursor           string
	MaxItems         int
	MaxEvidenceBytes int64
}

func (r OwnedStorageDiscoveryRequest) Validate() error {
	return (OwnedStorageReconcileRequest{RepositoryID: r.RepositoryID, Cursor: r.Cursor, MaxItems: r.MaxItems, MaxEvidenceBytes: r.MaxEvidenceBytes, Mode: ReconcileQueryOnly}).Validate()
}

// OwnedStorageDiscovery is one bounded owner result. Complete is false when
// the owner has more marker-bound candidates and NextCursor resumes it.
type OwnedStorageDiscovery struct {
	Items      []OwnedStorageArtifactEvidence
	NextCursor string
	Complete   bool
}

func (d OwnedStorageDiscovery) Validate() error {
	if d.NextCursor != "" && !stableStorageText(d.NextCursor) {
		return ErrInvalidOwnedStorageReconcile
	}
	if d.Complete && d.NextCursor != "" || !d.Complete && d.NextCursor == "" {
		return ErrInvalidOwnedStorageReconcile
	}
	var evidence ByteSize
	for _, item := range d.Items {
		if err := item.Validate(); err != nil {
			return err
		}
		var err error
		evidence, err = evidence.Add(item.EvidenceBytes)
		if err != nil || evidence > MaxOwnedStorageReconcileEvidenceBytes {
			return ErrInvalidOwnedStorageReconcile
		}
	}
	return nil
}

// OwnedStorageInspector is implemented by each artifact owner. The adapter
// owns containment, marker, manifest, lease, and native filesystem checks.
type OwnedStorageInspector interface {
	OwnerKind() OwnerKind
	InspectArtifact(context.Context, OwnedArtifact) (OwnedStorageArtifactEvidence, error)
	InspectReservation(context.Context, CapacityReservationSummary) (OwnedStorageReservationEvidence, error)
	Discover(context.Context, OwnedStorageDiscoveryRequest) (OwnedStorageDiscovery, error)
}

// OwnedStorageLedgerPage is one consistent SQLite read transaction. The
// cursor is owner-neutral and interpreted only by the ledger adapter.
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
// health shape. A false complete flag is conservatively accounting-uncertain.
func CapacityHealthFromLedger(snapshot StorageLedgerSnapshot, volumes []VolumeEvidence, epoch, cursor string, complete bool) CapacityHealth {
	page := OwnedStorageLedgerPage{Revision: snapshot.Revision, Repository: snapshot.Repository, Global: snapshot.Global, Pressure: snapshot.Pressure}
	health := capacityHealth(page, volumes, epoch, cursor, complete)
	health.ActiveReservations = snapshot.ActiveReservations
	if !complete {
		health.Status = CapacityHealthAccountingUncertain
	}
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
	if !validRepairHash(r.Epoch) || !validRepairHash(r.HealthRevision) || r.NextCursor != "" && !stableStorageText(r.NextCursor) {
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

// OwnedStorageReconciler compares durable ledger rows with owner-declared
// evidence in bounded batches. It never walks paths or performs repair.
type OwnedStorageReconciler struct {
	Ledger     OwnedStorageReconcileLedger
	Store      OwnedStorageReconciliationStore
	Inspectors map[OwnerKind]OwnedStorageInspector
	Policy     ResourcePolicy
	Clock      Clock
}

func NewOwnedStorageReconciler(ledger OwnedStorageReconcileLedger, store OwnedStorageReconciliationStore, inspectors []OwnedStorageInspector, policy ResourcePolicy, clock Clock) (*OwnedStorageReconciler, error) {
	if ledger == nil || policy.Validate() != nil {
		return nil, ErrInvalidOwnedStorageReconcile
	}
	if clock == nil {
		clock = SystemClock{}
	}
	byOwner := make(map[OwnerKind]OwnedStorageInspector, len(inspectors))
	for _, inspector := range inspectors {
		if inspector == nil || !validOwnerKind(inspector.OwnerKind()) || byOwner[inspector.OwnerKind()] != nil {
			return nil, ErrInvalidOwnedStorageReconcile
		}
		byOwner[inspector.OwnerKind()] = inspector
	}
	return &OwnedStorageReconciler{Ledger: ledger, Store: store, Inspectors: byOwner, Policy: policy, Clock: clock}, nil
}

func (r *OwnedStorageReconciler) Reconcile(ctx context.Context, request OwnedStorageReconcileRequest) (OwnedStorageReconcileReport, error) {
	if request.Mode == "" {
		request.Mode = ReconcileQueryOnly
	}
	if request.MaxItems == 0 {
		request.MaxItems = int(DefaultOwnedStorageReconcileItems)
	}
	if request.MaxEvidenceBytes == 0 {
		request.MaxEvidenceBytes = int64(MaxOwnedStorageReconcileEvidenceBytes)
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
	discrepancies := make([]OwnedStorageDiscrepancy, 0, len(page.Artifacts)+len(page.Reservations))
	seen := make(map[string]struct{}, len(page.Artifacts))
	evidenceBytes := ByteSize(0)
	for _, artifact := range page.Artifacts {
		if err := ctx.Err(); err != nil {
			return OwnedStorageReconcileReport{}, err
		}
		seen[artifact.OwnerID+"\x00"+artifact.ArtifactID] = struct{}{}
		inspector := r.Inspectors[artifact.OwnerKind]
		if inspector == nil {
			discrepancies = append(discrepancies, uncertainArtifact(artifact, "owner_inspector_missing"))
			continue
		}
		evidence, inspectErr := inspector.InspectArtifact(ctx, artifact)
		if inspectErr != nil {
			discrepancies = append(discrepancies, uncertainArtifact(artifact, "owner_inspection_failed"))
			continue
		}
		if err := evidence.Validate(); err != nil || evidence.OwnerKind != artifact.OwnerKind || evidence.OwnerID != artifact.OwnerID || evidence.ArtifactID != artifact.ArtifactID {
			discrepancies = append(discrepancies, uncertainArtifact(artifact, "owner_evidence_invalid"))
			continue
		}
		evidenceBytes, err = addReconcileBytes(evidenceBytes, evidence.EvidenceBytes, int64(request.MaxEvidenceBytes))
		if err != nil {
			return OwnedStorageReconcileReport{}, err
		}
		if discrepancy, ok := compareArtifact(artifact, evidence); ok {
			discrepancies = append(discrepancies, discrepancy)
		}
	}
	for _, reservation := range page.Reservations {
		inspector := r.Inspectors[reservation.OwnerKind]
		if inspector == nil {
			discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: OwnershipUncertain, OwnerKind: reservation.OwnerKind, OwnerID: reservation.OwnerID, ReservationID: reservation.ReservationID, VolumeID: "unknown", EvidenceCode: "owner_inspector_missing"})
			continue
		}
		evidence, inspectErr := inspector.InspectReservation(ctx, reservation)
		if inspectErr != nil || evidence.Validate() != nil || evidence.ReservationID != reservation.ReservationID || evidence.OwnerKind != reservation.OwnerKind || evidence.OwnerID != reservation.OwnerID {
			discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: OwnershipUncertain, OwnerKind: reservation.OwnerKind, OwnerID: reservation.OwnerID, ReservationID: reservation.ReservationID, VolumeID: "unknown", EvidenceCode: "reservation_evidence_uncertain"})
			continue
		}
		evidenceBytes, err = addReconcileBytes(evidenceBytes, evidence.EvidenceBytes, int64(request.MaxEvidenceBytes))
		if err != nil {
			return OwnedStorageReconcileReport{}, err
		}
		if !evidence.LeaseActive {
			discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: ReservationStale, OwnerKind: reservation.OwnerKind, OwnerID: reservation.OwnerID, ReservationID: reservation.ReservationID, VolumeID: "unknown", MarkerNonce: evidence.MarkerNonce, EvidenceCode: "owner_lease_inactive", PlanEligible: evidence.MarkerNonce != ""})
		}
	}
	if page.Complete {
		for ownerKind, inspector := range r.Inspectors {
			discovery, discoverErr := inspector.Discover(ctx, OwnedStorageDiscoveryRequest{RepositoryID: request.RepositoryID, MaxItems: request.MaxItems, MaxEvidenceBytes: request.MaxEvidenceBytes})
			if discoverErr != nil || discovery.Validate() != nil {
				discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: OwnershipUncertain, OwnerKind: ownerKind, OwnerID: "owner", VolumeID: "unknown", EvidenceCode: "owner_discovery_uncertain"})
				continue
			}
			for _, evidence := range discovery.Items {
				if _, ok := seen[evidence.OwnerID+"\x00"+evidence.ArtifactID]; ok {
					continue
				}
				if evidence.State == OwnedStorageEvidenceUncertain || evidence.ArtifactID == "" || evidence.State != OwnedStorageEvidenceTemporary && !evidence.Complete {
					discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: OwnershipUncertain, OwnerKind: evidence.OwnerKind, OwnerID: evidence.OwnerID, ArtifactID: evidence.ArtifactID, VolumeID: evidence.VolumeID, MarkerNonce: evidence.MarkerNonce, EvidenceCode: "filesystem_candidate_uncertain"})
					continue
				}
				kind := LedgerEntryMissing
				if evidence.State == OwnedStorageEvidenceTemporary {
					kind = OwnedTemporaryResidue
				}
				discrepancies = append(discrepancies, OwnedStorageDiscrepancy{Kind: kind, OwnerKind: evidence.OwnerKind, OwnerID: evidence.OwnerID, ArtifactID: evidence.ArtifactID, ReservationID: evidence.ReservationID, RepositoryID: evidence.RepositoryID, VolumeID: evidence.VolumeID, MarkerNonce: evidence.MarkerNonce, ObservedManifestHash: evidence.ManifestHash, ObservedBytes: evidence.ObservedBytes, EvidenceCode: "filesystem_candidate_not_in_ledger", PlanEligible: evidence.QuarantineReady})
			}
		}
	}
	status := capacityHealthStatus(page, discrepancies)
	capacity := capacityHealth(page, request.Volumes, epochID, request.Cursor, page.Complete && len(discrepancies) == 0 || page.Complete)
	capacity.Status = status
	capacity.UncertaintyCount, _ = capacity.UncertaintyCount.Add(countUncertainty(discrepancies))
	batchKey := reconciliationBatchID(epochID, request.Cursor, page.NextCursor, page.Revision, discrepancies)
	epoch := OwnedStorageReconciliationEpoch{Epoch: epochID, RepositoryID: request.RepositoryID, LedgerRevision: page.Revision, PolicyVersion: r.Policy.Version, Cursor: request.Cursor, NextCursor: page.NextCursor, BatchKey: batchKey, ProcessedItems: Count(len(page.Artifacts) + len(page.Reservations)), DiscrepancyCount: Count(len(discrepancies)), EvidenceBytes: evidenceBytes, UncertaintyCount: countUncertainty(discrepancies), Complete: page.Complete, UpdatedAt: r.Clock.Now().UTC()}
	if err := epoch.Validate(); err != nil {
		return OwnedStorageReconcileReport{}, err
	}
	candidates := repairCandidates(page.Revision, discrepancies)
	healthRevision := reconciliationHealthID(epoch, capacity, discrepancies, candidates)
	report := OwnedStorageReconcileReport{Epoch: epochID, HealthRevision: healthRevision, LedgerRevision: page.Revision, Discrepancies: discrepancies, Capacity: capacity, Candidates: candidates, NextCursor: page.NextCursor, Complete: page.Complete}
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

func compareArtifact(artifact OwnedArtifact, evidence OwnedStorageArtifactEvidence) (OwnedStorageDiscrepancy, bool) {
	switch evidence.State {
	case OwnedStorageEvidenceMissing:
		return OwnedStorageDiscrepancy{Kind: ArtifactMissing, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ArtifactID: artifact.ArtifactID, ReservationID: artifact.ReservationID, RepositoryID: artifact.RepositoryID, VolumeID: artifact.VolumeID, MarkerNonce: evidence.MarkerNonce, ExpectedManifestHash: artifact.ManifestHash, ExpectedBytes: artifact.ObservedBytes, EvidenceCode: "ledger_artifact_missing", PlanEligible: evidence.Rebuildable}, true
	case OwnedStorageEvidenceUncertain, OwnedStorageEvidenceTemporary, OwnedStorageEvidenceFilesystem:
		return uncertainArtifact(artifact, "owner_artifact_state_uncertain"), true
	case OwnedStorageEvidenceAccepted:
		if artifact.ManifestHash != evidence.ManifestHash {
			return OwnedStorageDiscrepancy{Kind: ManifestMismatch, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ArtifactID: artifact.ArtifactID, ReservationID: artifact.ReservationID, RepositoryID: artifact.RepositoryID, VolumeID: artifact.VolumeID, MarkerNonce: evidence.MarkerNonce, ExpectedManifestHash: artifact.ManifestHash, ObservedManifestHash: evidence.ManifestHash, ExpectedBytes: artifact.ObservedBytes, ObservedBytes: evidence.ObservedBytes, EvidenceCode: "manifest_mismatch", PlanEligible: evidence.Rebuildable}, true
		}
		if artifact.ObservedBytes != evidence.ObservedBytes {
			return OwnedStorageDiscrepancy{Kind: ObservedSizeMismatch, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ArtifactID: artifact.ArtifactID, ReservationID: artifact.ReservationID, RepositoryID: artifact.RepositoryID, VolumeID: artifact.VolumeID, MarkerNonce: evidence.MarkerNonce, ExpectedManifestHash: artifact.ManifestHash, ObservedManifestHash: evidence.ManifestHash, ExpectedBytes: artifact.ObservedBytes, ObservedBytes: evidence.ObservedBytes, EvidenceCode: "observed_size_mismatch", PlanEligible: evidence.Rebuildable}, true
		}
	}
	return OwnedStorageDiscrepancy{}, false
}

func uncertainArtifact(artifact OwnedArtifact, code string) OwnedStorageDiscrepancy {
	return OwnedStorageDiscrepancy{Kind: OwnershipUncertain, OwnerKind: artifact.OwnerKind, OwnerID: artifact.OwnerID, ArtifactID: artifact.ArtifactID, ReservationID: artifact.ReservationID, RepositoryID: artifact.RepositoryID, VolumeID: artifact.VolumeID, ExpectedManifestHash: artifact.ManifestHash, ExpectedBytes: artifact.ObservedBytes, EvidenceCode: code}
}

func repairCandidates(revision uint64, discrepancies []OwnedStorageDiscrepancy) []OwnedStorageRepairCandidate {
	result := make([]OwnedStorageRepairCandidate, 0)
	for index := range discrepancies {
		discrepancy := &discrepancies[index]
		if !discrepancy.PlanEligible {
			continue
		}
		var kind RepairHandlerKind
		var version string
		switch discrepancy.Kind {
		case ReservationStale:
			kind, version = "owned_storage_reservation", "v1"
		case ArtifactMissing, ManifestMismatch, ObservedSizeMismatch:
			kind, version = "owned_storage_artifact_rebuild", "v1"
		case OwnedTemporaryResidue, LedgerEntryMissing:
			kind, version = "owned_storage_residue_quarantine", "v1"
		default:
			continue
		}
		preconditions := reconciliationPreconditions(*discrepancy, revision)
		candidate := OwnedStorageRepairCandidate{Kind: discrepancy.Kind, HandlerKind: kind, HandlerVersion: version, ArtifactID: discrepancy.ArtifactID, ReservationID: discrepancy.ReservationID, OwnerID: discrepancy.OwnerID, MarkerNonce: discrepancy.MarkerNonce, ExpectedManifestHash: discrepancy.ExpectedManifestHash, LedgerRevision: revision, PreconditionsHash: preconditions}
		if candidate.Validate() == nil {
			discrepancy.HandlerKind = kind
			discrepancy.HandlerVersion = version
			discrepancy.PreconditionsHash = preconditions
			result = append(result, candidate)
		}
	}
	return result
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

func capacityHealthStatus(page OwnedStorageLedgerPage, discrepancies []OwnedStorageDiscrepancy) CapacityHealthStatus {
	if !page.Complete {
		return CapacityHealthAccountingUncertain
	}
	for _, discrepancy := range discrepancies {
		if discrepancy.Kind == OwnershipUncertain {
			return CapacityHealthAccountingUncertain
		}
	}
	if page.Pressure.Uncertain || page.Repository.UncertainCount > 0 || page.Global.UncertainCount > 0 {
		return CapacityHealthAccountingUncertain
	}
	if page.Pressure.RepositoryPressure == StoragePressureHard || page.Pressure.GlobalPressure == StoragePressureHard {
		return CapacityHealthHardLimit
	}
	if page.Pressure.RepositoryPressure == StoragePressureSoft || page.Pressure.GlobalPressure == StoragePressureSoft {
		return CapacityHealthStoragePressure
	}
	return CapacityHealthOK
}

func countUncertainty(discrepancies []OwnedStorageDiscrepancy) Count {
	var count Count
	for _, discrepancy := range discrepancies {
		if discrepancy.Kind == OwnershipUncertain {
			count++
		}
	}
	return count
}

func addReconcileBytes(current, addition ByteSize, max int64) (ByteSize, error) {
	if addition > ByteSize(^uint64(0))-current || current+addition > ByteSize(max) {
		return 0, ErrInvalidOwnedStorageReconcile
	}
	return current + addition, nil
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

func reconciliationPreconditions(discrepancy OwnedStorageDiscrepancy, revision uint64) string {
	value := strings.Join([]string{string(discrepancy.Kind), discrepancy.OwnerID, discrepancy.ArtifactID, discrepancy.ReservationID, discrepancy.MarkerNonce, discrepancy.ExpectedManifestHash, discrepancy.ObservedManifestHash, fmt.Sprint(discrepancy.ExpectedBytes), fmt.Sprint(discrepancy.ObservedBytes), fmt.Sprint(revision)}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
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
