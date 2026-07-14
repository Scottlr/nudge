package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrStorageLedgerInput reports an invalid owned-storage ledger request.
	ErrStorageLedgerInput = errors.New("invalid owned-storage ledger input")
	// ErrStorageLedgerConflict reports an idempotency, state, or revision conflict.
	ErrStorageLedgerConflict = errors.New("owned-storage ledger conflict")
	// ErrStorageLedgerNotFound reports an unknown reservation or ledger record.
	ErrStorageLedgerNotFound = errors.New("owned-storage ledger record not found")
	// ErrStoragePublicationBlocked reports pressure or uncertainty that prevents
	// optional retained growth or publication.
	ErrStoragePublicationBlocked = errors.New("owned-storage publication blocked")
)

const (
	// CurrentStorageAccountingVersion identifies the conservative charge formula
	// persisted with every reservation and artifact.
	CurrentStorageAccountingVersion uint32 = 1
	// DefaultStorageLedgerPage is the bounded default for ledger snapshots.
	DefaultStorageLedgerPage uint32 = 100
	// MaxStorageLedgerPage is the hard maximum returned by one snapshot query.
	MaxStorageLedgerPage uint32 = 1000
)

// StorageArtifactClass identifies the owner-declared accounting class of an
// accepted artifact. The value is never derived from a path or content.
type StorageArtifactClass string

const (
	StorageClassDatabaseRows    StorageArtifactClass = "database_rows"
	StorageClassDatabaseWAL     StorageArtifactClass = "database_wal"
	StorageClassCapture         StorageArtifactClass = "capture"
	StorageClassReviewSnapshot  StorageArtifactClass = "review_snapshot"
	StorageClassWorkspace       StorageArtifactClass = "workspace"
	StorageClassProposal        StorageArtifactClass = "proposal"
	StorageClassJournal         StorageArtifactClass = "journal"
	StorageClassSpool           StorageArtifactClass = "spool"
	StorageClassCache           StorageArtifactClass = "cache"
	StorageClassLog             StorageArtifactClass = "log"
	StorageClassExport          StorageArtifactClass = "export"
	StorageClassRetainedHistory StorageArtifactClass = "retained_history"
)

func (c StorageArtifactClass) Validate() error {
	switch c {
	case StorageClassDatabaseRows, StorageClassDatabaseWAL, StorageClassCapture,
		StorageClassReviewSnapshot, StorageClassWorkspace, StorageClassProposal,
		StorageClassJournal, StorageClassSpool, StorageClassCache, StorageClassLog,
		StorageClassExport, StorageClassRetainedHistory:
		return nil
	default:
		return ErrStorageLedgerInput
	}
}

// StorageAccountingCharge applies the v1 formula. Every class charges the
// larger of its declared logical and independently observed bytes; this keeps
// unknown compression, deduplication, sparse allocation, and reflink behavior
// conservative. Database rows and WAL use the same formula because their
// observed bytes, when available, already include their physical footprint.
func StorageAccountingCharge(version uint32, class StorageArtifactClass, logical, observed ByteSize) (ByteSize, error) {
	if version != CurrentStorageAccountingVersion || class.Validate() != nil {
		return 0, ErrStorageLedgerInput
	}
	if observed > logical {
		return observed, nil
	}
	return logical, nil
}

// CapacityReservationRecord imports one T065 reservation into durable ledger
// accounting. The marker remains owned by the T065 adapter until the owner
// settles or releases this record.
type CapacityReservationRecord struct {
	Reservation    CapacityReservation
	OwnerKind      OwnerKind
	OwnerID        string
	Plan           CapacityPlan
	IdempotencyKey string
	CreatedAt      time.Time
}

func (r CapacityReservationRecord) Validate() error {
	if r.Reservation.Marker() == "" || r.Reservation.OperationID() != r.Plan.OperationID || r.Reservation.PolicyVersion() != r.Plan.PolicyVersion || r.Reservation.PlanDigest() == "" || !validOwnerKind(r.OwnerKind) || !stableStorageText(r.OwnerID) || !stableStorageText(r.IdempotencyKey) || r.CreatedAt.IsZero() {
		return ErrStorageLedgerInput
	}
	expectedRepositoryID := ""
	if r.Plan.RepositoryID != nil {
		expectedRepositoryID = string(*r.Plan.RepositoryID)
	}
	if r.Reservation.RepositoryID() != expectedRepositoryID {
		return ErrStorageLedgerInput
	}
	digest, err := PlanDigest(r.Plan)
	if err != nil || digest != r.Reservation.PlanDigest() {
		return ErrStorageLedgerInput
	}
	if len(r.Plan.VolumePeaks) == 0 {
		return ErrStorageLedgerInput
	}
	for _, peak := range r.Plan.VolumePeaks {
		if peak.ID == "" {
			return ErrStorageLedgerInput
		}
		if _, err := peak.Charge(); err != nil {
			return ErrStorageLedgerInput
		}
	}
	return nil
}

// ReservationRelease requests one idempotent release of an active ledger
// reservation after its owner has completed or abandoned temporary work.
type ReservationRelease struct {
	ReservationID    string
	IdempotencyKey   string
	ExpectedRevision uint64
}

func (r ReservationRelease) Validate() error {
	if !stableStorageText(r.ReservationID) || !stableStorageText(r.IdempotencyKey) {
		return ErrStorageLedgerInput
	}
	return nil
}

// OwnedArtifact is the independently verified identity and accounting record
// produced by T066 adoption. It contains metadata only, never artifact bytes.
type OwnedArtifact struct {
	ArtifactID        string
	OwnerKind         OwnerKind
	OwnerID           string
	OperationID       domain.OperationID
	ReservationID     string
	RepositoryID      *domain.RepositoryID
	Class             StorageArtifactClass
	Lifecycle         OwnedArtifactLifecycle
	LogicalBytes      ByteSize
	ObservedBytes     ByteSize
	ChargedBytes      ByteSize
	VolumeID          string
	ManifestHash      string
	AccountingVersion uint32
	PolicyVersion     ResourcePolicyVersion
	Complete          bool
	CreatedAt         time.Time
}

// OwnedArtifactLifecycle is the durable lifecycle of an artifact ledger row.
type OwnedArtifactLifecycle string

const (
	OwnedArtifactAccepted            OwnedArtifactLifecycle = "accepted"
	OwnedArtifactAccountingUncertain OwnedArtifactLifecycle = "accounting_uncertain"
)

func (a OwnedArtifact) Validate() error {
	if !stableStorageText(a.ArtifactID) || !validOwnerKind(a.OwnerKind) || !stableStorageText(a.OwnerID) || a.OperationID == "" || !stableStorageText(a.ReservationID) || a.Class.Validate() != nil || a.Lifecycle != OwnedArtifactAccepted && a.Lifecycle != OwnedArtifactAccountingUncertain || !stableStorageText(a.VolumeID) || !validSHA256Text(a.ManifestHash) || a.AccountingVersion != CurrentStorageAccountingVersion || a.PolicyVersion == 0 || a.CreatedAt.IsZero() {
		return ErrStorageLedgerInput
	}
	if a.Lifecycle == OwnedArtifactAccepted && !a.Complete || a.Lifecycle == OwnedArtifactAccountingUncertain && a.Complete {
		return ErrStorageLedgerInput
	}
	charged, err := StorageAccountingCharge(a.AccountingVersion, a.Class, a.LogicalBytes, a.ObservedBytes)
	if err != nil || a.ChargedBytes != charged {
		return ErrStorageLedgerInput
	}
	return nil
}

// ReservationSettlement converts one active reservation into one or more
// verified accepted artifacts in one durable ledger transaction.
type ReservationSettlement struct {
	ReservationID    string
	IdempotencyKey   string
	ExpectedRevision uint64
	Artifacts        []OwnedArtifact
}

func (s ReservationSettlement) Validate() error {
	if !stableStorageText(s.ReservationID) || !stableStorageText(s.IdempotencyKey) || len(s.Artifacts) == 0 {
		return ErrStorageLedgerInput
	}
	seen := make(map[string]struct{}, len(s.Artifacts))
	for _, artifact := range s.Artifacts {
		if err := artifact.Validate(); err != nil || artifact.ReservationID != s.ReservationID {
			return ErrStorageLedgerInput
		}
		if _, exists := seen[artifact.ArtifactID]; exists {
			return ErrStorageLedgerInput
		}
		seen[artifact.ArtifactID] = struct{}{}
	}
	return nil
}

// StorageTotals is one consistent repository or global aggregate. It is
// derived solely from ledger transitions and never from filesystem inspection.
type StorageTotals struct {
	RepositoryID   *domain.RepositoryID
	LogicalBytes   ByteSize
	ObservedBytes  ByteSize
	ChargedBytes   ByteSize
	ReservedBytes  ByteSize
	UncertainCount Count
	Revision       uint64
}

// CapacityReservationSummary is bounded reservation metadata returned by a
// ledger snapshot.
type CapacityReservationSummary struct {
	ReservationID string
	OwnerKind     OwnerKind
	OwnerID       string
	OperationID   domain.OperationID
	RepositoryID  *domain.RepositoryID
	State         CapacityReservationState
	RetainedBytes ByteSize
	CreatedAt     time.Time
}

// CapacityReservationState is the durable terminal classification of a
// reservation. Unknown states are corruption, never implicit release.
type CapacityReservationState string

const (
	ReservationActive   CapacityReservationState = "active"
	ReservationConsumed CapacityReservationState = "consumed"
	ReservationReleased CapacityReservationState = "released"
)

func (s CapacityReservationState) Validate() error {
	switch s {
	case ReservationActive, ReservationConsumed, ReservationReleased:
		return nil
	default:
		return ErrStorageLedgerInput
	}
}

// StorageLedgerQuery requests one bounded, filesystem-free ledger snapshot.
type StorageLedgerQuery struct {
	RepositoryID        *domain.RepositoryID
	Limit               uint32
	IncludeReservations bool
	IncludeArtifacts    bool
}

func (q StorageLedgerQuery) Validate() error {
	if q.RepositoryID != nil && *q.RepositoryID == "" {
		return ErrStorageLedgerInput
	}
	if q.Limit == 0 {
		return ErrStorageLedgerInput
	}
	if q.Limit > MaxStorageLedgerPage {
		return ErrStorageLedgerInput
	}
	return nil
}

// StorageDecision is the result of applying repository/global soft, hard, and
// accounting-uncertainty policy to a snapshot.
type StorageDecision string

const (
	StorageDecisionAllowed             StorageDecision = "allowed"
	StorageDecisionSoftPressure        StorageDecision = "soft_pressure"
	StorageDecisionHardPressure        StorageDecision = "hard_pressure"
	StorageDecisionAccountingUncertain StorageDecision = "accounting_uncertain"
)

// StoragePressureState exposes pressure and publication/reservation decisions
// without authorizing cleanup or changing accepted history.
type StoragePressureState struct {
	RepositoryPressure StoragePressure
	GlobalPressure     StoragePressure
	Reservation        StorageDecision
	Publication        StorageDecision
	Uncertain          bool
}

// StorageLedgerSnapshot is a bounded revisioned query result.
type StorageLedgerSnapshot struct {
	Revision     uint64
	Repository   StorageTotals
	Global       StorageTotals
	Pressure     StoragePressureState
	Reservations []CapacityReservationSummary
	Artifacts    []OwnedArtifact
	Complete     bool
}

// OwnedStorageLedger is the application-owned durable storage boundary. The
// SQLite adapter owns persistence and transactions; callers own lifecycle
// semantics and T066 verification.
type OwnedStorageLedger interface {
	RecordReservation(context.Context, CapacityReservationRecord) error
	SettleReservation(context.Context, ReservationSettlement) error
	ReleaseReservation(context.Context, ReservationRelease) error
	Snapshot(context.Context, StorageLedgerQuery) (StorageLedgerSnapshot, error)
}

func stableStorageText(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsRune(value, '\x00') && stableText(value)
}

func validSHA256Text(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
