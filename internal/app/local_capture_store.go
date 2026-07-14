package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

const LocalCaptureManifestVersion uint32 = 1

var (
	// ErrInvalidLocalCaptureArtifacts reports a candidate whose typed spool
	// handles do not bind to the candidate and reservation they claim.
	ErrInvalidLocalCaptureArtifacts = errors.New("invalid local capture artifacts")
	// ErrInvalidLocalCaptureManifest reports an incomplete accepted manifest.
	ErrInvalidLocalCaptureManifest = errors.New("invalid local capture manifest")
	// ErrCaptureGenerationConflict reports a failed guarded session transition.
	ErrCaptureGenerationConflict = errors.New("capture generation conflict")
	// ErrCaptureCommitUnknown reports a durable transition whose outcome is not
	// safe to classify. Published artifacts must remain for reconciliation.
	ErrCaptureCommitUnknown = errors.New("capture commit outcome unknown")
	// ErrCaptureNotFound reports an absent accepted capture.
	ErrCaptureNotFound = errors.New("capture not found")
	// ErrCaptureCorrupt reports identity-bound accepted bytes that no longer
	// match their manifest.
	ErrCaptureCorrupt = errors.New("capture artifact corrupt")
)

// LocalCaptureArtifacts transfers ownership of one complete T106 result to
// the application adoption boundary. The handles remain temporary until the
// adopter publishes them or aborts them.
type LocalCaptureArtifacts struct {
	Candidate   repository.LocalCaptureCandidate
	PatchSpool  ArtifactSpoolHandle
	BlobSpool   ArtifactSpoolHandle
	Reservation CapacityReservation
	Plan        CapacityPlan
	Policy      ResourcePolicy
	Capacity    CapacityReservationPort
}

// Validate checks that both candidate spools and the reservation describe the
// same capture operation without interpreting any filesystem path.
func (a LocalCaptureArtifacts) Validate() error {
	if err := a.Candidate.Validate(); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	if err := a.Policy.Validate(); err != nil || a.Policy.Version != a.Plan.PolicyVersion || uint32(a.Policy.Version) != a.Candidate.Policy.ResourcePolicyVersion {
		return ErrInvalidLocalCaptureArtifacts
	}
	if a.PatchSpool == nil || a.BlobSpool == nil || a.Capacity == nil || a.Reservation.Marker() == "" {
		return ErrInvalidLocalCaptureArtifacts
	}
	patch := a.PatchSpool.Descriptor()
	blobs := a.BlobSpool.Descriptor()
	if patch.OwnerKind != OwnerCapture || blobs.OwnerKind != OwnerCapture || patch.SpoolID != a.Candidate.Patch.SpoolID || blobs.SpoolID != a.Candidate.BlobSpool.SpoolID || patch.ReservationID != a.Reservation.Marker() || blobs.ReservationID != a.Reservation.Marker() || patch.OperationID != blobs.OperationID || patch.OperationID != a.Reservation.OperationID() || a.Plan.OperationID != patch.OperationID || a.Plan.PolicyVersion != a.Reservation.PolicyVersion() {
		return ErrInvalidLocalCaptureArtifacts
	}
	if _, err := a.PatchIdentity(); err != nil {
		return err
	}
	if _, err := a.BlobIdentity(); err != nil {
		return err
	}
	return nil
}

// PatchIdentity converts the candidate's verified patch evidence into the
// artifact-spool identity required for no-replace publication.
func (a LocalCaptureArtifacts) PatchIdentity() (ArtifactIdentity, error) {
	if a.Candidate.Patch.Kind != repository.CaptureArtifactPatch {
		return ArtifactIdentity{}, ErrInvalidLocalCaptureArtifacts
	}
	return captureArtifactIdentity(a.Candidate.Patch)
}

// BlobIdentity converts the candidate's verified blob-directory evidence into
// the artifact-spool identity required for no-replace publication.
func (a LocalCaptureArtifacts) BlobIdentity() (ArtifactIdentity, error) {
	if a.Candidate.BlobSpool.Kind != repository.CaptureArtifactBlobs {
		return ArtifactIdentity{}, ErrInvalidLocalCaptureArtifacts
	}
	return captureArtifactIdentity(a.Candidate.BlobSpool)
}

func captureArtifactIdentity(value repository.CaptureArtifact) (ArtifactIdentity, error) {
	identity := ArtifactIdentity{
		SpoolID:      value.SpoolID,
		ManifestHash: value.ManifestHash,
		Bytes:        ByteSize(value.Bytes),
		Entries:      Count(value.Entries),
		Complete:     true,
		VerifiedAt:   value.VerifiedAt,
	}
	if err := identity.Validate(); err != nil {
		return ArtifactIdentity{}, ErrInvalidLocalCaptureArtifacts
	}
	return identity, nil
}

// Abort releases only the exact candidate handles and matching reservation.
// It is valid before publication; published artifacts require their owner
// lifecycle instead of this temporary-result cleanup.
func (a LocalCaptureArtifacts) Abort(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidLocalCaptureArtifacts
	}
	var first error
	if a.PatchSpool != nil {
		if err := a.PatchSpool.Abort(ctx); err != nil && first == nil {
			first = err
		}
	}
	if a.BlobSpool != nil {
		if err := a.BlobSpool.Abort(ctx); err != nil && first == nil {
			first = err
		}
	}
	if a.Capacity != nil && a.Reservation.Marker() != "" {
		if err := a.Capacity.Release(ctx, a.Reservation, a.Plan, a.Policy); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CaptureSessionGuard is the exact durable writer fence required for a
// session-scoped generation transition.
type CaptureSessionGuard struct {
	SessionID   domain.ReviewSessionID
	LeaseID     domain.SessionLeaseID
	WriterEpoch uint64
	Revision    uint64
}

// Validate checks the lease, epoch, and revision fence.
func (g CaptureSessionGuard) Validate() error {
	if g.SessionID == "" || g.LeaseID == "" || g.WriterEpoch == 0 || g.Revision == 0 {
		return ErrCaptureGenerationConflict
	}
	return nil
}

// CaptureGeneration is the canonical immutable identity published for one
// accepted local capture. It contains no live filesystem path.
type CaptureGeneration struct {
	CaptureID    domain.CaptureID
	Generation   repository.TargetGeneration
	RepositoryID domain.RepositoryID
	WorktreeID   domain.WorktreeID
	Fingerprint  string
	ManifestHash string
	Base         repository.LocalCaptureBase
	CreatedAt    time.Time
}

// Validate checks generation and identity relationships without opening any
// artifact.
func (g CaptureGeneration) Validate() error {
	if g.CaptureID == "" || g.Generation == 0 || g.RepositoryID == "" || g.WorktreeID == "" || g.Fingerprint == "" || g.ManifestHash == "" || g.CreatedAt.IsZero() {
		return ErrInvalidLocalCaptureManifest
	}
	if err := g.Base.Validate(); err != nil {
		return ErrInvalidLocalCaptureManifest
	}
	if !validLocalCaptureHash(g.Fingerprint) || !validLocalCaptureHash(g.ManifestHash) {
		return ErrInvalidLocalCaptureManifest
	}
	return nil
}

// CaptureSessionState is the actor's read snapshot used for one guarded
// adoption. The durable committer must compare it again inside its write
// transaction.
type CaptureSessionState struct {
	Guard        CaptureSessionGuard
	RepositoryID domain.RepositoryID
	WorktreeID   domain.WorktreeID
	Current      *CaptureGeneration
}

// Validate checks the session binding and optional current generation.
func (s CaptureSessionState) Validate() error {
	if err := s.Guard.Validate(); err != nil || s.RepositoryID == "" || s.WorktreeID == "" {
		return ErrCaptureGenerationConflict
	}
	if s.Current != nil {
		if err := s.Current.Validate(); err != nil || s.Current.RepositoryID != s.RepositoryID || s.Current.WorktreeID != s.WorktreeID {
			return ErrCaptureGenerationConflict
		}
	}
	return nil
}

// CaptureArtifactRef binds one accepted artifact to a protected owner target.
// RelativePath is a member name inside the accepted blob directory or the
// empty value for the patch file.
type CaptureArtifactRef struct {
	Kind         repository.CaptureArtifactKind
	Identity     ArtifactIdentity
	Target       PublishTarget
	RelativePath string
}

// Validate checks the path-free identity and protected target shape.
func (r CaptureArtifactRef) Validate() error {
	if err := r.Identity.Validate(); err != nil || r.Target.OwnerKind != OwnerCapture || r.Target.RelativePath == "" || r.Target.SourceRelativePath != "" && r.RelativePath != "" {
		return ErrInvalidLocalCaptureManifest
	}
	if r.Kind != repository.CaptureArtifactPatch && r.Kind != repository.CaptureArtifactBlobs {
		return ErrInvalidLocalCaptureManifest
	}
	if r.Kind == repository.CaptureArtifactPatch && (r.RelativePath != "" || r.Target.SourceRelativePath == "") {
		return ErrInvalidLocalCaptureManifest
	}
	if r.Kind == repository.CaptureArtifactBlobs && (r.RelativePath == "" || r.Target.SourceRelativePath != "") {
		return ErrInvalidLocalCaptureManifest
	}
	return nil
}

// CaptureManifest is the immutable metadata needed to reproduce an accepted
// candidate without consulting the live worktree.
type CaptureManifest struct {
	Version      uint32
	CaptureID    domain.CaptureID
	RepositoryID domain.RepositoryID
	WorktreeID   domain.WorktreeID
	Candidate    repository.LocalCaptureCandidate
	Patch        CaptureArtifactRef
	Blobs        CaptureArtifactRef
	ManifestHash string
	CreatedAt    time.Time
}

// Validate checks that the accepted manifest remains bound to its candidate
// and artifact identities.
func (m CaptureManifest) Validate() error {
	if m.Version != LocalCaptureManifestVersion || m.CaptureID == "" || m.RepositoryID == "" || m.WorktreeID == "" || m.CreatedAt.IsZero() || !validLocalCaptureHash(m.ManifestHash) {
		return ErrInvalidLocalCaptureManifest
	}
	if err := m.Candidate.Validate(); err != nil || m.Candidate.RepositoryID != m.RepositoryID || m.Candidate.WorktreeID != m.WorktreeID {
		return ErrInvalidLocalCaptureManifest
	}
	if err := m.Patch.Validate(); err != nil {
		return ErrInvalidLocalCaptureManifest
	}
	if err := m.Blobs.Validate(); err != nil || m.Patch.Kind != repository.CaptureArtifactPatch || m.Blobs.Kind != repository.CaptureArtifactBlobs || m.Patch.Identity.SpoolID != m.Candidate.Patch.SpoolID || m.Blobs.Identity.SpoolID != m.Candidate.BlobSpool.SpoolID || m.Patch.Identity.ManifestHash != m.Candidate.Patch.ManifestHash || m.Patch.Identity.Bytes != ByteSize(m.Candidate.Patch.Bytes) || m.Patch.Identity.Entries != Count(m.Candidate.Patch.Entries) || m.Blobs.Identity.ManifestHash != m.Candidate.BlobSpool.ManifestHash || m.Blobs.Identity.Bytes != ByteSize(m.Candidate.BlobSpool.Bytes) || m.Blobs.Identity.Entries != Count(m.Candidate.BlobSpool.Entries) || m.Patch.Target.SourceRelativePath != m.Candidate.Patch.RelativePath || m.Blobs.RelativePath != m.Candidate.BlobSpool.RelativePath {
		return ErrInvalidLocalCaptureManifest
	}
	expected, err := CaptureManifestHash(m.Candidate, m.Patch.Identity, m.Blobs.Identity)
	if err != nil || expected != m.ManifestHash {
		return ErrInvalidLocalCaptureManifest
	}
	return nil
}

// CaptureManifestHash computes the stable accepted-manifest identity. Source
// spool IDs and timestamps are excluded so an identical recapture can reuse a
// generation even though its temporary handles are new.
func CaptureManifestHash(candidate repository.LocalCaptureCandidate, patch, blobs ArtifactIdentity) (string, error) {
	if err := candidate.Validate(); err != nil || patch.Validate() != nil || blobs.Validate() != nil {
		return "", ErrInvalidLocalCaptureManifest
	}
	h := sha256.New()
	writeCaptureDigestString(h, fmt.Sprint(candidate.Version))
	writeCaptureDigestString(h, string(candidate.RepositoryID))
	writeCaptureDigestString(h, string(candidate.WorktreeID))
	writeCaptureDigestString(h, candidate.Fingerprint)
	writeCaptureDigestString(h, string(candidate.Base.ObjectID))
	writeCaptureDigestString(h, patch.ManifestHash)
	writeCaptureDigestString(h, blobs.ManifestHash)
	writeCaptureDigestString(h, string(candidate.Policy.ConversionDecision))
	writeCaptureDigestString(h, candidate.Policy.ConversionFingerprint)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeCaptureDigestString(h io.Writer, value string) {
	var length [8]byte
	for index := range length {
		length[len(length)-index-1] = byte(uint64(len(value)) >> (index * 8))
	}
	_, _ = h.Write(length[:])
	_, _ = io.WriteString(h, value)
}

// CaptureAdoption reports a committed generation. Reused is true when the
// incoming fingerprint already identifies the current session generation.
type CaptureAdoption struct {
	Generation CaptureGeneration
	Manifest   CaptureManifest
	Reused     bool
}

// LocalCaptureCommitter atomically persists the manifest, generation, and
// T067 reservation/accounting transition under the supplied session guard.
// Implementations must return ErrCaptureCommitUnknown when the durable outcome
// cannot be classified; callers then preserve published artifacts for repair.
type LocalCaptureCommitter interface {
	CommitLocalCapture(context.Context, CaptureSessionState, CaptureGeneration, CaptureManifest, CapacityReservation, CapacityPlan) error
}

// CaptureManifestReader loads one immutable accepted manifest by CaptureID.
type CaptureManifestReader interface {
	OpenCaptureManifest(context.Context, domain.CaptureID) (CaptureManifest, error)
}

// PublishedArtifactReader reads a bounded identity-bound range without
// exposing the protected absolute path to application code.
type PublishedArtifactReader interface {
	ReadPublishedRange(context.Context, PublishTarget, string, StreamIdentity, ByteSize, ByteSize) ([]byte, error)
}

// PublishedArtifactReleaser removes only a newly published artifact after a
// classified pre-commit failure, after rechecking its exact identity.
type PublishedArtifactReleaser interface {
	RemovePublished(context.Context, PublishedArtifact) error
}

// CaptureBlobRead identifies one accepted blob member and its bounded range.
type CaptureBlobRead struct {
	CaptureID    domain.CaptureID
	ManifestHash string
	RelativePath string
	Expected     StreamIdentity
	Offset       ByteSize
	MaxBytes     ByteSize
}

// LocalCaptureStore is the downstream application port for adoption and
// immutable accepted-capture reads.
type LocalCaptureStore interface {
	Adopt(context.Context, LocalCaptureArtifacts, CaptureSessionState) (CaptureAdoption, error)
	OpenCaptureManifest(context.Context, domain.CaptureID) (CaptureManifest, error)
	ReadBlobRange(context.Context, CaptureBlobRead) ([]byte, error)
}

func validLocalCaptureHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
