package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Scottlr/nudge/internal/capacity"
	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrInvalidCapacityPlan reports a plan that cannot be admitted under its
	// recorded policy version.
	ErrInvalidCapacityPlan = errors.New("invalid capacity plan")
	// ErrCapacityPressure reports insufficient observed space for a reservation.
	ErrCapacityPressure = errors.New("capacity pressure")
	// ErrReservationMismatch reports a release or recheck for another owner.
	ErrReservationMismatch = errors.New("capacity reservation mismatch")
	// ErrReservationNotReady reports a reservation that requires reconciliation.
	ErrReservationNotReady = errors.New("capacity reservation not ready")
)

// ArtifactClass identifies one policy-bounded heavy artifact estimate.
type ArtifactClass string

const (
	ArtifactProposal       ArtifactClass = "proposal"
	ArtifactCompletePatch  ArtifactClass = "complete_patch"
	ArtifactPublishedDelta ArtifactClass = "published_delta"
	ArtifactCapture        ArtifactClass = "capture"
	ArtifactSnapshot       ArtifactClass = "snapshot"
)

// ArtifactEstimate is a bounded estimate admitted before heavy allocation.
// LargestItem is used for per-file proposal limits; Bytes is the total class
// estimate and Entries is the item count where the policy defines one.
type ArtifactEstimate struct {
	Class       ArtifactClass
	Entries     Count
	Bytes       ByteSize
	LargestItem ByteSize
}

// VolumeCapacityMode records whether an evidence source proves a hard quota
// or only supplies a monitored free-space observation.
type VolumeCapacityMode string

const (
	VolumeCapacityMonitored VolumeCapacityMode = "monitored"
	VolumeCapacityHard      VolumeCapacityMode = "hard"
)

// VolumeEvidence is a point-in-time, path-free capacity observation.
type VolumeEvidence struct {
	ID       string
	Free     ByteSize
	Mode     VolumeCapacityMode
	Stable   bool
	Observed time.Time
}

// VolumePeak is the independent peak charge for one volume. Every field is a
// checked byte class; RetainedDelta is included so retained output cannot be
// forgotten when the plan is converted to a charge.
type VolumePeak struct {
	ID                     string
	Inputs                 ByteSize
	Temporaries            ByteSize
	Finals                 ByteSize
	CopyOnWrite            ByteSize
	DatabaseWAL            ByteSize
	AtomicOutput           ByteSize
	ConcurrentReservations ByteSize
	Reserve                ByteSize
	RetainedDelta          ByteSize
}

// Charge returns the complete checked charge for this volume.
func (p VolumePeak) Charge() (ByteSize, error) {
	value, err := capacity.VolumePeak{
		Inputs:                 capacity.Bytes(p.Inputs),
		Temporaries:            capacity.Bytes(p.Temporaries),
		Finals:                 capacity.Bytes(p.Finals),
		CopyOnWrite:            capacity.Bytes(p.CopyOnWrite),
		DatabaseWAL:            capacity.Bytes(p.DatabaseWAL),
		AtomicOutput:           capacity.Bytes(p.AtomicOutput),
		ConcurrentReservations: capacity.Bytes(p.ConcurrentReservations),
		Reserve:                capacity.Bytes(p.Reserve),
		RetainedDelta:          capacity.Bytes(p.RetainedDelta),
	}.Charge()
	if err != nil {
		return 0, ErrInvalidCapacityPlan
	}
	return ByteSize(value), nil
}

// CapacityBudget is the already-retained usage charged against the v1
// repository and global hard budgets.
type CapacityBudget struct {
	RepositoryUsed ByteSize
	GlobalUsed     ByteSize
}

// CapacityPlan is the application-owned admission record for one heavy
// operation. It contains no filesystem paths or provider content.
type CapacityPlan struct {
	OperationID   domain.OperationID
	RepositoryID  *domain.RepositoryID
	Artifacts     []ArtifactEstimate
	VolumePeaks   []VolumePeak
	RetainedDelta ByteSize
	Budget        CapacityBudget
	PolicyVersion ResourcePolicyVersion
}

// CapacityPressureError identifies a bounded non-ready capacity result
// without exposing a path or source content.
type CapacityPressureError struct {
	VolumeID string
	Required ByteSize
	Free     ByteSize
	Mode     VolumeCapacityMode
}

func (e *CapacityPressureError) Error() string {
	if e == nil {
		return ErrCapacityPressure.Error()
	}
	return fmt.Sprintf("capacity pressure on volume %s", e.VolumeID)
}

func (e *CapacityPressureError) Unwrap() error { return ErrCapacityPressure }

// CapacityCheckStatus is the outcome of a reservation recheck.
type CapacityCheckStatus string

const (
	CapacityCheckAdmitted CapacityCheckStatus = "admitted"
	CapacityCheckPressure CapacityCheckStatus = "pressure"
	CapacityCheckRecovery CapacityCheckStatus = "recovery_required"
)

// CapacityCheck contains path-free evidence from a bounded recheck.
type CapacityCheck struct {
	Status        CapacityCheckStatus
	PolicyVersion ResourcePolicyVersion
	Observed      time.Time
	Volumes       []VolumeEvidence
}

// RecheckBounds makes a long-operation recheck cadence explicit. A
// reservation owner must recheck before either bound is crossed.
type RecheckBounds struct {
	MaxBytes    ByteSize
	MaxInterval time.Duration
}

// Validate checks that both recheck dimensions are bounded and positive.
func (b RecheckBounds) Validate() error {
	if b.MaxBytes == 0 || b.MaxInterval <= 0 {
		return ErrInvalidCapacityPlan
	}
	return nil
}

// CapacityReservation is an opaque handle returned by an admitted port.
// Callers can compare identity and policy binding but cannot alter its owner
// marker or charge.
type CapacityReservation struct {
	marker        string
	operationID   domain.OperationID
	repositoryID  string
	planDigest    string
	policyVersion ResourcePolicyVersion
}

// NewCapacityReservation constructs a handle for a trusted reservation port.
// marker and planDigest are opaque adapter values and are never interpreted by
// application callers.
func NewCapacityReservation(marker string, operationID domain.OperationID, repositoryID string, planDigest string, policyVersion ResourcePolicyVersion) (CapacityReservation, error) {
	if marker == "" || operationID == "" || planDigest == "" || policyVersion == 0 {
		return CapacityReservation{}, ErrInvalidCapacityPlan
	}
	return CapacityReservation{marker: marker, operationID: operationID, repositoryID: repositoryID, planDigest: planDigest, policyVersion: policyVersion}, nil
}

// Marker returns the opaque adapter handle.
func (r CapacityReservation) Marker() string { return r.marker }

// OperationID returns the operation bound to the reservation.
func (r CapacityReservation) OperationID() domain.OperationID { return r.operationID }

// RepositoryID returns the optional repository binding.
func (r CapacityReservation) RepositoryID() string { return r.repositoryID }

// PlanDigest returns the immutable plan digest used for release matching.
func (r CapacityReservation) PlanDigest() string { return r.planDigest }

// PolicyVersion returns the admitted policy version.
func (r CapacityReservation) PolicyVersion() ResourcePolicyVersion { return r.policyVersion }

// CapacityReservationPort owns cross-process reservation implementation. The
// application supplies the plan and policy; adapters own locks, markers, and
// native volume observations.
type CapacityReservationPort interface {
	Reserve(context.Context, CapacityPlan, ResourcePolicy, []VolumeEvidence) (CapacityReservation, error)
	Recheck(context.Context, CapacityReservation, CapacityPlan, ResourcePolicy, RecheckBounds, []VolumeEvidence) (CapacityCheck, error)
	Release(context.Context, CapacityReservation, CapacityPlan, ResourcePolicy) error
	Reconcile(context.Context, CapacityReservation, CapacityPlan, ResourcePolicy, ReconciliationProof) error
}

// ReconciliationProof is explicit owner/journal evidence required before a
// crashed reservation marker may be removed.
type ReconciliationProof struct {
	OwnerLockReconciled  bool
	OperationJournalDone bool
}

// PlanDigest computes a stable path-free identity for a plan. The encoding is
// deliberately fixed and contains only bounded scalar admission data.
func PlanDigest(plan CapacityPlan) (string, error) {
	if plan.OperationID == "" || plan.PolicyVersion == 0 || len(plan.VolumePeaks) == 0 || plan.RepositoryID != nil && *plan.RepositoryID == "" {
		return "", ErrInvalidCapacityPlan
	}
	seen := make(map[string]struct{}, len(plan.VolumePeaks))
	for _, peak := range plan.VolumePeaks {
		if peak.ID == "" {
			return "", ErrInvalidCapacityPlan
		}
		if _, exists := seen[peak.ID]; exists {
			return "", ErrInvalidCapacityPlan
		}
		seen[peak.ID] = struct{}{}
		if _, err := peak.Charge(); err != nil {
			return "", err
		}
	}
	h := sha256.New()
	writeDigestString(h, string(plan.OperationID))
	if plan.RepositoryID != nil {
		writeDigestUint64(h, 1)
		writeDigestString(h, string(*plan.RepositoryID))
	} else {
		writeDigestUint64(h, 0)
	}
	writeDigestUint64(h, uint64(plan.PolicyVersion))
	writeDigestUint64(h, uint64(plan.RetainedDelta))
	writeDigestUint64(h, uint64(plan.Budget.RepositoryUsed))
	writeDigestUint64(h, uint64(plan.Budget.GlobalUsed))
	artifacts := append([]ArtifactEstimate(nil), plan.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Class < artifacts[j].Class })
	for _, artifact := range artifacts {
		writeDigestString(h, string(artifact.Class))
		writeDigestUint64(h, uint64(artifact.Entries))
		writeDigestUint64(h, uint64(artifact.Bytes))
		writeDigestUint64(h, uint64(artifact.LargestItem))
	}
	peaks := append([]VolumePeak(nil), plan.VolumePeaks...)
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].ID < peaks[j].ID })
	for _, peak := range peaks {
		writeDigestString(h, peak.ID)
		for _, value := range []ByteSize{peak.Inputs, peak.Temporaries, peak.Finals, peak.CopyOnWrite, peak.DatabaseWAL, peak.AtomicOutput, peak.ConcurrentReservations, peak.Reserve, peak.RetainedDelta} {
			writeDigestUint64(h, uint64(value))
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeDigestString(h interface{ Write([]byte) (int, error) }, value string) {
	writeDigestUint64(h, uint64(len(value)))
	_, _ = h.Write([]byte(value))
}

func writeDigestUint64(h interface{ Write([]byte) (int, error) }, value uint64) {
	var bytes [8]byte
	for index := range bytes {
		bytes[len(bytes)-index-1] = byte(value >> (index * 8))
	}
	_, _ = h.Write(bytes[:])
}

// ValidateCapacityPlan checks policy binding, checked arithmetic, volume
// uniqueness, artifact ceilings, reserves, and retained budget hard limits.
func ValidateCapacityPlan(policy ResourcePolicy, plan CapacityPlan, evidence []VolumeEvidence) error {
	if err := policy.Validate(); err != nil || plan.OperationID == "" || plan.PolicyVersion != policy.Version || len(plan.VolumePeaks) == 0 || plan.RepositoryID != nil && *plan.RepositoryID == "" {
		return ErrInvalidCapacityPlan
	}
	if plan.RetainedDelta > policy.Storage.GlobalHardBytes || plan.RetainedDelta > policy.Storage.RepositoryHardBytes {
		return ErrLimitExceeded
	}
	if _, err := plan.Budget.RepositoryUsed.Add(plan.RetainedDelta); err != nil {
		return ErrInvalidCapacityPlan
	} else if plan.Budget.RepositoryUsed+plan.RetainedDelta > policy.Storage.RepositoryHardBytes {
		return ErrLimitExceeded
	}
	if _, err := plan.Budget.GlobalUsed.Add(plan.RetainedDelta); err != nil {
		return ErrInvalidCapacityPlan
	} else if plan.Budget.GlobalUsed+plan.RetainedDelta > policy.Storage.GlobalHardBytes {
		return ErrLimitExceeded
	}
	if err := validateArtifacts(policy.Artifact, plan.Artifacts); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(plan.VolumePeaks))
	for _, peak := range plan.VolumePeaks {
		if peak.ID == "" {
			return ErrInvalidCapacityPlan
		}
		if _, exists := seen[peak.ID]; exists {
			return ErrInvalidCapacityPlan
		}
		seen[peak.ID] = struct{}{}
		if peak.Reserve < policy.Storage.MinimumFreeBytes || peak.Reserve < policy.Storage.RecoveryFileBytes {
			return ErrInvalidCapacityPlan
		}
		if _, err := peak.Charge(); err != nil {
			return err
		}
	}
	var retained ByteSize
	for _, peak := range plan.VolumePeaks {
		var err error
		retained, err = retained.Add(peak.RetainedDelta)
		if err != nil {
			return ErrInvalidCapacityPlan
		}
	}
	if retained != plan.RetainedDelta {
		return ErrInvalidCapacityPlan
	}
	if evidence == nil {
		return nil
	}
	observed := make(map[string]VolumeEvidence, len(evidence))
	for _, value := range evidence {
		if value.ID == "" || !value.Stable || value.Mode != VolumeCapacityMonitored && value.Mode != VolumeCapacityHard {
			return ErrInvalidCapacityPlan
		}
		if _, exists := observed[value.ID]; exists {
			return ErrInvalidCapacityPlan
		}
		observed[value.ID] = value
	}
	for _, peak := range plan.VolumePeaks {
		value, exists := observed[peak.ID]
		if !exists {
			return ErrInvalidCapacityPlan
		}
		charge, err := peak.Charge()
		if err != nil {
			return err
		}
		if value.Free < charge {
			return &CapacityPressureError{VolumeID: peak.ID, Required: charge, Free: value.Free, Mode: value.Mode}
		}
	}
	return nil
}

func validateArtifacts(limits ArtifactLimits, estimates []ArtifactEstimate) error {
	seen := make(map[ArtifactClass]struct{}, len(estimates))
	for _, estimate := range estimates {
		if estimate.Bytes == 0 || estimate.Class == "" {
			return ErrInvalidCapacityPlan
		}
		if _, exists := seen[estimate.Class]; exists {
			return ErrInvalidCapacityPlan
		}
		seen[estimate.Class] = struct{}{}
		switch estimate.Class {
		case ArtifactProposal:
			if estimate.Entries == 0 || estimate.Entries > limits.ProposalFiles || estimate.LargestItem == 0 || estimate.LargestItem > limits.ProposalFileBytes || estimate.Bytes > limits.CompletePatchBytes {
				return ErrLimitExceeded
			}
		case ArtifactCompletePatch:
			if estimate.Bytes > limits.CompletePatchBytes {
				return ErrLimitExceeded
			}
		case ArtifactPublishedDelta:
			if estimate.Bytes > limits.PublishedDeltaBytes {
				return ErrLimitExceeded
			}
		case ArtifactCapture:
			if estimate.Entries == 0 || estimate.Entries > limits.CaptureEntries || estimate.Bytes > limits.CaptureDeltaBytes {
				return ErrLimitExceeded
			}
		case ArtifactSnapshot:
			if estimate.Entries == 0 || estimate.Entries > limits.SnapshotEntries || estimate.Bytes > limits.SnapshotBytes {
				return ErrLimitExceeded
			}
		default:
			return ErrInvalidCapacityPlan
		}
	}
	return nil
}
