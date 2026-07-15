package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	// ErrInvalidReviewSnapshot reports an incomplete snapshot identity or
	// lifecycle value.
	ErrInvalidReviewSnapshot = errors.New("invalid review snapshot")
	// ErrReviewSnapshotNotFound reports an absent snapshot or lease.
	ErrReviewSnapshotNotFound = errors.New("review snapshot not found")
	// ErrReviewSnapshotCorrupt reports disagreement between durable and
	// filesystem snapshot evidence.
	ErrReviewSnapshotCorrupt = errors.New("review snapshot corrupt")
	// ErrReviewSnapshotLimit reports a bounded materialization refusal.
	ErrReviewSnapshotLimit = errors.New("review_snapshot_limit")
	// ErrReviewSnapshotUnsafe reports an entry that cannot be materialized
	// without weakening the read-only filesystem contract.
	ErrReviewSnapshotUnsafe = errors.New("review snapshot entry unsafe")
	// ErrReviewSnapshotBusy reports an active reader lease or another owner.
	ErrReviewSnapshotBusy = errors.New("review snapshot busy")
	// ErrReviewSnapshotResidue reports residue without positive ownership
	// evidence.
	ErrReviewSnapshotResidue = errors.New("review snapshot residue ambiguous")
)

// ReviewSnapshotState is the durable readiness state of one immutable root.
type ReviewSnapshotState string

const (
	ReviewSnapshotCreating         ReviewSnapshotState = "creating"
	ReviewSnapshotReady            ReviewSnapshotState = "ready"
	ReviewSnapshotRecoveryRequired ReviewSnapshotState = "recovery_required"
	ReviewSnapshotRemoved          ReviewSnapshotState = "removed"
)

// ReviewSnapshot is the application identity of a materialized, read-only
// view. Root is exposed only after the workspace adapter has independently
// verified the marker, manifest, and containment evidence.
type ReviewSnapshot struct {
	ID              domain.ReviewSnapshotID
	CaptureID       domain.CaptureID
	RepositoryID    domain.RepositoryID
	WorktreeID      domain.WorktreeID
	Root            string
	MarkerNonce     string
	ManifestHash    string
	PolicyVersion   ResourcePolicyVersion
	EvidenceVersion EvidenceVersion
	State           ReviewSnapshotState
	CreatedAt       time.Time
}

// Validate checks the path-free identity and the adapter-returned root shape.
func (s ReviewSnapshot) Validate() error {
	if s.ID == "" || s.CaptureID == "" || s.RepositoryID == "" || s.WorktreeID == "" || s.Root == "" || !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || !validSnapshotHash(s.MarkerNonce) || !validSnapshotHash(s.ManifestHash) || s.PolicyVersion == 0 || s.EvidenceVersion == 0 || !validReviewSnapshotState(s.State) || s.CreatedAt.IsZero() {
		return ErrInvalidReviewSnapshot
	}
	return nil
}

func validReviewSnapshotState(value ReviewSnapshotState) bool {
	switch value {
	case ReviewSnapshotCreating, ReviewSnapshotReady, ReviewSnapshotRecoveryRequired, ReviewSnapshotRemoved:
		return true
	default:
		return false
	}
}

// ReviewSnapshotLease is the exact read capability held by a discussion turn.
// It contains no writable root and is bound to one immutable manifest.
type ReviewSnapshotLease struct {
	ID           domain.ReviewSnapshotLeaseID
	SnapshotID   domain.ReviewSnapshotID
	CaptureID    domain.CaptureID
	Root         string
	ManifestHash string
	ProcessNonce string
	AcquiredAt   time.Time
}

// Validate checks the lease binding before it is released or passed to a
// provider permission mapper.
func (l ReviewSnapshotLease) Validate() error {
	if l.ID == "" || l.SnapshotID == "" || l.CaptureID == "" || l.Root == "" || !filepath.IsAbs(l.Root) || filepath.Clean(l.Root) != l.Root || !validSnapshotHash(l.ManifestHash) || l.ProcessNonce == "" || l.AcquiredAt.IsZero() {
		return ErrInvalidReviewSnapshot
	}
	return nil
}

// ReviewSnapshotEnsureRequest identifies the accepted capture and policy cell
// that the immutable view must reproduce.
type ReviewSnapshotEnsureRequest struct {
	CaptureID       domain.CaptureID
	RepositoryID    domain.RepositoryID
	WorktreeID      domain.WorktreeID
	PolicyVersion   ResourcePolicyVersion
	EvidenceVersion EvidenceVersion
	Persist         bool
}

// Validate checks the exact capture/policy binding before materialization.
func (r ReviewSnapshotEnsureRequest) Validate() error {
	if r.CaptureID == "" || r.RepositoryID == "" || r.WorktreeID == "" || r.PolicyVersion == 0 || r.EvidenceVersion == 0 {
		return ErrInvalidReviewSnapshot
	}
	return nil
}

// ReviewSnapshotRecoveryAction selects an explicit residue effect.
type ReviewSnapshotRecoveryAction string

const (
	ReviewSnapshotRecoveryRemove ReviewSnapshotRecoveryAction = "remove"
)

// ReviewSnapshotRecoveryProof is the positive evidence required before
// recovery may remove an interrupted root or stale lease.
type ReviewSnapshotRecoveryProof struct {
	SnapshotID          domain.ReviewSnapshotID
	ProcessNonce        string
	OwnerLockReconciled bool
	NoActiveLeases      bool
	Action              ReviewSnapshotRecoveryAction
}

// Validate checks that recovery is an explicit, owner-reconciled action.
func (p ReviewSnapshotRecoveryProof) Validate() error {
	if p.SnapshotID == "" || p.ProcessNonce == "" || !p.OwnerLockReconciled || !p.NoActiveLeases || p.Action != ReviewSnapshotRecoveryRemove {
		return ErrInvalidReviewSnapshot
	}
	return nil
}

// ReviewSnapshotLimits are the materialization bounds consumed by the
// workspace owner. They mirror the versioned T070 artifact and reserve policy.
type ReviewSnapshotLimits struct {
	MaxEntries      Count
	MaxBytes        ByteSize
	MaxDuration     time.Duration
	MinimumFreeByte ByteSize
}

// NewReviewSnapshotLimits derives the one snapshot limit set from T070.
func NewReviewSnapshotLimits(policy ResourcePolicy) (ReviewSnapshotLimits, error) {
	if err := policy.Validate(); err != nil {
		return ReviewSnapshotLimits{}, err
	}
	return ReviewSnapshotLimits{
		MaxEntries:      policy.Artifact.SnapshotEntries,
		MaxBytes:        policy.Artifact.SnapshotBytes,
		MaxDuration:     policy.Artifact.SnapshotDeadline,
		MinimumFreeByte: policy.Storage.MinimumFreeBytes,
	}, nil
}

// ReviewSnapshotBaseSource enumerates and opens only pinned Git objects. It
// must never read a live worktree path.
type ReviewSnapshotBaseSource interface {
	ListBase(context.Context, repository.LocalCaptureBase) ([]repository.TreeEntry, error)
	OpenBase(context.Context, repository.LocalCaptureBase, repository.TreeEntry) (io.ReadCloser, error)
}

// ReviewSnapshotStore is the durable ownership seam used in persisted mode.
// Implementations own transactions and never infer ownership from a path.
type ReviewSnapshotStore interface {
	SaveReviewSnapshot(context.Context, ReviewSnapshot) error
	LoadReviewSnapshot(context.Context, domain.ReviewSnapshotID) (ReviewSnapshot, error)
	LoadReviewSnapshotByCapture(context.Context, domain.CaptureID) (ReviewSnapshot, error)
	DeleteReviewSnapshot(context.Context, domain.ReviewSnapshotID) error
	SaveReviewSnapshotLease(context.Context, ReviewSnapshotLease) error
	ReleaseReviewSnapshotLease(context.Context, domain.ReviewSnapshotLeaseID) error
	CountReviewSnapshotLeases(context.Context, domain.ReviewSnapshotID) (Count, error)
}

// ReviewSnapshotManager is the lifecycle consumed by application discussion
// and doctor. Implementations retain the owner lock for each operation.
type ReviewSnapshotManager interface {
	Ensure(context.Context, ReviewSnapshotEnsureRequest) (ReviewSnapshot, error)
	AcquireReadLease(context.Context, domain.ReviewSnapshotID) (ReviewSnapshotLease, error)
	Release(context.Context, ReviewSnapshotLease) error
	Recover(context.Context, ReviewSnapshotRecoveryProof) error
	Remove(context.Context, domain.ReviewSnapshotID) error
}

// ReviewSnapshotMaterializationEvidence returns independent implementation
// evidence for the capability axis. It intentionally says nothing about
// provider read containment, account, disclosure, or an active lease.
func ReviewSnapshotMaterializationEvidence(policy CapabilityPolicyV1, fileKind repository.FileKind, changeKind repository.ChangeKind, pathClass PathClass) (ImplementationEvidence, error) {
	if err := policy.Validate(); err != nil {
		return ImplementationEvidence{}, err
	}
	evidence := ImplementationEvidence{
		Cell:              CapabilityCell{FileKind: fileKind, ChangeKind: changeKind, PathClass: pathClass, Axis: CapabilityMaterializeReviewSnapshot},
		OwnerVersion:      "T055-review-snapshot-v1",
		ConformanceSet:    "immutable-local-capture",
		ExpiresWithPolicy: policy.ResourcePolicyVersion,
		EvidenceVersion:   policy.EvidenceVersion,
		Supported:         true,
	}
	if err := evidence.Validate(policy); err != nil {
		return ImplementationEvidence{}, err
	}
	return evidence, nil
}

func validSnapshotHash(value string) bool {
	if len(value) != sha256.Size*2 || strings.TrimSpace(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
