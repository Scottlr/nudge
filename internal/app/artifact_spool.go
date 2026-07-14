package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
)

var (
	// ErrInvalidArtifactSpool reports invalid owner, limit, identity, or target data.
	ErrInvalidArtifactSpool = errors.New("invalid artifact spool")
	// ErrSpoolLimitExceeded reports a stream or manifest beyond its admitted bound.
	ErrSpoolLimitExceeded = errors.New("artifact spool limit exceeded")
	// ErrSpoolNotReady reports a lifecycle operation before its required phase.
	ErrSpoolNotReady = errors.New("artifact spool not ready")
	// ErrSpoolBusy reports an open writer or otherwise live spool handle.
	ErrSpoolBusy = errors.New("artifact spool busy")
	// ErrSpoolDestinationExists reports a no-replace publication race.
	ErrSpoolDestinationExists = errors.New("artifact spool destination exists")
	// ErrSpoolPublicationUnsupported reports a platform without proven no-replace adoption.
	ErrSpoolPublicationUnsupported = errors.New("artifact spool publication unsupported")
	// ErrSpoolResidueAmbiguous reports residue that cannot be safely recovered.
	ErrSpoolResidueAmbiguous = errors.New("artifact spool residue ambiguous")
)

// OwnerKind identifies the Nudge owner whose protected roots contain a spool.
type OwnerKind string

const (
	OwnerCapture        OwnerKind = "capture"
	OwnerReviewSnapshot OwnerKind = "review_snapshot"
	OwnerWorkspace      OwnerKind = "workspace"
	OwnerProposal       OwnerKind = "proposal"
	OwnerCache          OwnerKind = "cache"
)

// SpoolState is the monotonic lifecycle state recorded in the owner marker.
type SpoolState string

const (
	SpoolOpen             SpoolState = "open"
	SpoolVerifying        SpoolState = "verifying"
	SpoolVerified         SpoolState = "verified"
	SpoolPublishing       SpoolState = "publishing"
	SpoolPublished        SpoolState = "published"
	SpoolAborted          SpoolState = "aborted"
	SpoolRecoveryRequired SpoolState = "recovery_required"
)

// SpoolRecoveryAction selects the only two explicit crash-recovery effects.
type SpoolRecoveryAction string

const (
	SpoolRecoveryResume SpoolRecoveryAction = "resume"
	SpoolRecoveryRemove SpoolRecoveryAction = "remove"
)

// SpoolLimits bound bytes, entries, path metadata, manifest accounting, and
// the fixed stream buffer. Every field is required to be positive.
type SpoolLimits struct {
	MaxBytes         ByteSize
	MaxEntries       Count
	MaxPathBytes     ByteSize
	MaxManifestBytes ByteSize
	BufferBytes      ByteSize
	CheckEveryBytes  ByteSize
}

// Validate checks the stream bounds and prevents a buffer from exceeding the
// complete artifact budget.
func (l SpoolLimits) Validate() error {
	if l.MaxBytes == 0 || l.MaxEntries == 0 || l.MaxPathBytes == 0 || l.MaxManifestBytes == 0 || l.BufferBytes == 0 || l.CheckEveryBytes == 0 || l.BufferBytes > l.MaxBytes || l.CheckEveryBytes > l.MaxBytes {
		return ErrInvalidArtifactSpool
	}
	return nil
}

// DefaultSpoolLimits derives a bounded generic artifact limit from T070. An
// owner may provide a lower bound for a narrower artifact class.
func DefaultSpoolLimits(policy ResourcePolicy) (SpoolLimits, error) {
	if err := policy.Validate(); err != nil {
		return SpoolLimits{}, err
	}
	return SpoolLimits{
		MaxBytes:         policy.Artifact.SnapshotBytes,
		MaxEntries:       policy.Artifact.SnapshotEntries,
		MaxPathBytes:     policy.Input.RepoPathBytes,
		MaxManifestBytes: 64 * MiB,
		BufferBytes:      64 * KiB,
		CheckEveryBytes:  64 * KiB,
	}, nil
}

// ArtifactSpool is the path-free descriptor returned to the application.
// RootNonce and ReservationID bind the descriptor to one protected marker.
type ArtifactSpool struct {
	SpoolID       string
	OperationID   domain.OperationID
	OwnerKind     OwnerKind
	ReservationID string
	RootNonce     string
	Limits        SpoolLimits
	State         SpoolState
}

// Validate checks the descriptor without interpreting any filesystem path.
func (s ArtifactSpool) Validate() error {
	if !validSpoolID(s.SpoolID) || s.OperationID == "" || !validOwnerKind(s.OwnerKind) || s.ReservationID == "" || !validHex(s.RootNonce, 32) || !validSpoolState(s.State) {
		return ErrInvalidArtifactSpool
	}
	return s.Limits.Validate()
}

// SpoolSpec binds creation to a live T065 reservation and an owner identity.
type SpoolSpec struct {
	OperationID domain.OperationID
	OwnerKind   OwnerKind
	Reservation CapacityReservation
	Limits      SpoolLimits
}

// Validate checks creation inputs before any filesystem mutation.
func (s SpoolSpec) Validate() error {
	if s.OperationID == "" || !validOwnerKind(s.OwnerKind) || s.Reservation.Marker() == "" || s.Reservation.OperationID() != s.OperationID || s.Reservation.PolicyVersion() == 0 {
		return ErrInvalidArtifactSpool
	}
	return s.Limits.Validate()
}

// StreamIdentity is the independently computed identity of one closed file.
type StreamIdentity struct {
	Bytes  ByteSize
	SHA256 string
}

// ArtifactIdentity is the immutable, complete spool identity produced by
// CloseAndVerify. It is not accepted artifact truth until Publish succeeds.
type ArtifactIdentity struct {
	SpoolID      string
	ManifestHash string
	Bytes        ByteSize
	Entries      Count
	Complete     bool
	VerifiedAt   time.Time
}

// Validate checks the identity shape without reading a path or content.
func (i ArtifactIdentity) Validate() error {
	if i.SpoolID == "" || !validSHA256(i.ManifestHash) || !i.Complete || i.VerifiedAt.IsZero() {
		return ErrInvalidArtifactSpool
	}
	return nil
}

// PublishTarget identifies a destination beneath a configured owner root.
// Neither field accepts an absolute path; the adapter resolves containment.
// SourceRelativePath is empty when the complete spool payload directory is
// adopted, otherwise it names one verified file beneath that payload.
type PublishTarget struct {
	OwnerKind          OwnerKind
	RelativePath       string
	SourceRelativePath string
}

// SpoolRecoveryProof is owner/journal evidence needed before cleanup or
// resumed publication can touch interrupted residue.
type SpoolRecoveryProof struct {
	OwnerLockReconciled  bool
	OperationJournalDone bool
	NoActiveHandles      bool
	Action               SpoolRecoveryAction
	Expected             ArtifactIdentity
	Target               PublishTarget
}

// PublishedArtifact is the exact identity and owner-relative target returned
// after successful no-replace adoption.
type PublishedArtifact struct {
	Identity ArtifactIdentity
	Target   PublishTarget
	Limits   SpoolLimits
}

// ArtifactSpoolFile is owned by the caller until Close. It is not safe for
// concurrent Write calls; the parent spool serializes lifecycle operations.
type ArtifactSpoolFile interface {
	io.Writer
	Close() error
}

// ArtifactSpoolHandle is the filesystem adapter's capability for one spool.
// Relative names are interpreted only below the protected spool payload root.
type ArtifactSpoolHandle interface {
	Descriptor() ArtifactSpool
	CreateFile(context.Context, string) (ArtifactSpoolFile, error)
	WriteFrom(context.Context, string, io.Reader) (StreamIdentity, error)
	CloseAndVerify(context.Context) (ArtifactIdentity, error)
	Publish(context.Context, ArtifactIdentity, PublishTarget) (PublishedArtifact, error)
	Abort(context.Context) error
	Recover(context.Context, SpoolRecoveryProof) error
}

// ArtifactSpoolPort is the application-consumed creation boundary. The
// platform implementation owns protected roots, markers, handles, and native
// adoption; artifact owners own the semantic manifest and ledger transition.
type ArtifactSpoolPort interface {
	Create(context.Context, SpoolSpec) (ArtifactSpoolHandle, error)
}

func validOwnerKind(value OwnerKind) bool {
	if value == "" || len(value) > 64 || !utf8.ValidString(string(value)) {
		return false
	}
	for _, character := range string(value) {
		if character == '/' || character == '\\' || character == ':' || character == 0 || character < 0x20 || strings.ContainsRune("\"'", character) {
			return false
		}
	}
	return true
}

func validSpoolState(value SpoolState) bool {
	switch value {
	case SpoolOpen, SpoolVerifying, SpoolVerified, SpoolPublishing, SpoolPublished, SpoolAborted, SpoolRecoveryRequired:
		return true
	default:
		return false
	}
}

func validSHA256(value string) bool {
	return validHex(value, 64)
}

func validSpoolID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || character == 0 || character < 0x20 {
			return false
		}
	}
	return true
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}
