package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	// ErrInvalidNativePathEvidence reports incomplete or contradictory native
	// path qualification evidence.
	ErrInvalidNativePathEvidence = errors.New("invalid native path evidence")
)

// NativePathEvidenceVersion is the persisted evidence format for one
// root-bound native path qualification.
const NativePathEvidenceVersion = "v1"

// NativePathPurpose identifies the bounded native operation that a path is
// being qualified for.
type NativePathPurpose string

const (
	NativeReadExisting NativePathPurpose = "read_existing"
	NativeCreateParent NativePathPurpose = "create_parent"
	NativeReplaceLeaf  NativePathPurpose = "replace_leaf"
	NativeDeleteLeaf   NativePathPurpose = "delete_leaf"
)

func (p NativePathPurpose) Validate() error {
	switch p {
	case NativeReadExisting, NativeCreateParent, NativeReplaceLeaf, NativeDeleteLeaf:
		return nil
	default:
		return ErrInvalidNativePathEvidence
	}
}

// NativePathDisposition separates a retained raw Git identity from native
// action eligibility.
type NativePathDisposition string

const (
	NativePathSafe       NativePathDisposition = "safe"
	NativePathReviewOnly NativePathDisposition = "review_only"
)

// NativePathReason is stable, user-facing policy evidence. Raw paths remain
// reviewable even when one of these reasons disables native effects.
type NativePathReason string

const (
	NativeReasonPathUnrepresentable      NativePathReason = "path_unrepresentable"
	NativeReasonGitAdminAlias            NativePathReason = "git_admin_alias"
	NativeReasonPathCollision            NativePathReason = "path_collision"
	NativeReasonPathContainmentUnproven  NativePathReason = "path_containment_unproven"
	NativeReasonPathTraversal            NativePathReason = "path_traversal"
	NativeReasonInvalidSeparator         NativePathReason = "invalid_separator"
	NativeReasonReservedName             NativePathReason = "reserved_name"
	NativeReasonNativeAlias              NativePathReason = "native_alias"
	NativeReasonUnsupportedNormalization NativePathReason = "normalization_unproven"
	NativeReasonStaleEvidence            NativePathReason = "native_evidence_stale"
)

func (r NativePathReason) Validate() error {
	switch r {
	case NativeReasonPathUnrepresentable, NativeReasonGitAdminAlias, NativeReasonPathCollision,
		NativeReasonPathContainmentUnproven, NativeReasonPathTraversal, NativeReasonInvalidSeparator,
		NativeReasonReservedName, NativeReasonNativeAlias, NativeReasonUnsupportedNormalization,
		NativeReasonStaleEvidence:
		return nil
	default:
		return ErrInvalidNativePathEvidence
	}
}

// NativePathEvidence is bounded, persisted capability evidence for one raw
// repository path under one verified native root. Hashes are opaque values;
// the domain never turns them back into native paths.
type NativePathEvidence struct {
	RepoPathKey       RepoPathKey
	RootIdentity      string
	Platform          string
	FilesystemClass   string
	ComparisonKeyHash string
	ParentChainHash   string
	Disposition       NativePathDisposition
	ReasonCode        string
	EvidenceVersion   string
}

// Validate checks evidence shape without normalizing the repository key.
func (e NativePathEvidence) Validate() error {
	if _, err := e.RepoPathKey.Path(); err != nil || e.RootIdentity == "" || e.Platform == "" || e.FilesystemClass == "" ||
		e.ComparisonKeyHash == "" || e.ParentChainHash == "" || e.EvidenceVersion != NativePathEvidenceVersion {
		return ErrInvalidNativePathEvidence
	}
	if !validText(e.RootIdentity) || !validText(e.Platform) || !validText(e.FilesystemClass) || !validText(e.ComparisonKeyHash) || !validText(e.ParentChainHash) {
		return ErrInvalidNativePathEvidence
	}
	switch e.Disposition {
	case NativePathSafe:
		if e.ReasonCode != "" {
			return ErrInvalidNativePathEvidence
		}
	case NativePathReviewOnly:
		if e.ReasonCode == "" || NativePathReason(e.ReasonCode).Validate() != nil {
			return ErrInvalidNativePathEvidence
		}
	default:
		return ErrInvalidNativePathEvidence
	}
	return nil
}

// NativePathReasonCode returns the stable reason as a typed value after
// validation.
func (e NativePathEvidence) NativePathReasonCode() (NativePathReason, bool) {
	if e.Validate() != nil || e.ReasonCode == "" {
		return "", false
	}
	return NativePathReason(e.ReasonCode), true
}

// IsActionable reports whether the evidence permits a native effect.
func (e NativePathEvidence) IsActionable() bool {
	return e.Validate() == nil && e.Disposition == NativePathSafe
}

// NativePathEvidenceHash is a bounded helper for callers that need to bind
// evidence into an independent proposal or capture fingerprint.
func NativePathEvidenceHash(e NativePathEvidence) string {
	if e.Validate() != nil {
		return ""
	}
	value := strings.Join([]string{string(e.RepoPathKey), e.RootIdentity, e.Platform, e.FilesystemClass, e.ComparisonKeyHash, e.ParentChainHash, string(e.Disposition), e.ReasonCode, e.EvidenceVersion}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
