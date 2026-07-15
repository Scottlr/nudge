package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

var ErrInvalidReviewOnlyEntryEvidence = errors.New("invalid review-only entry evidence")

// SpecialFileKind identifies a native entry that v1 can describe but cannot
// treat as repository content or an editable filesystem leaf.
type SpecialFileKind string

const (
	SpecialSocket      SpecialFileKind = "socket"
	SpecialFIFO        SpecialFileKind = "fifo"
	SpecialCharDevice  SpecialFileKind = "char_device"
	SpecialBlockDevice SpecialFileKind = "block_device"
	SpecialJunction    SpecialFileKind = "junction_or_reparse"
	SpecialUnknown     SpecialFileKind = "unknown"
)

func (k SpecialFileKind) Validate() error {
	switch k {
	case SpecialSocket, SpecialFIFO, SpecialCharDevice, SpecialBlockDevice, SpecialJunction, SpecialUnknown:
		return nil
	default:
		return ErrInvalidReviewOnlyEntryEvidence
	}
}

// ReviewMetadataLevel describes how much no-follow metadata was safely
// obtained. It never implies content readability.
type ReviewMetadataLevel string

const (
	ReviewMetadataComplete    ReviewMetadataLevel = "complete"
	ReviewMetadataPathOnly    ReviewMetadataLevel = "path_only"
	ReviewOnlyEvidenceVersion                     = "v1"
)

const (
	ReasonSpecialSocket              = "special_socket"
	ReasonSpecialFIFO                = "special_fifo"
	ReasonSpecialCharDevice          = "special_char_device"
	ReasonSpecialBlockDevice         = "special_block_device"
	ReasonSpecialJunctionOrReparse   = "special_junction_or_reparse"
	ReasonSpecialUnknown             = "special_unknown"
	ReasonSpecialMetadataUnavailable = "special_metadata_unavailable"
)

// ReviewOnlyEntryEvidence preserves bounded metadata for a visible special
// entry. Canonical path, kind, mode, and object IDs remain on their owning
// tree/change records.
type ReviewOnlyEntryEvidence struct {
	SpecialKind     SpecialFileKind
	MetadataLevel   ReviewMetadataLevel
	MetadataHash    string
	ReasonCode      string
	EvidenceVersion string
}

func (e ReviewOnlyEntryEvidence) Validate() error {
	if e.SpecialKind.Validate() != nil || e.EvidenceVersion != ReviewOnlyEvidenceVersion || !validText(e.EvidenceVersion) || e.ReasonCode == "" || !validSpecialReason(e.ReasonCode) {
		return ErrInvalidReviewOnlyEntryEvidence
	}
	switch e.MetadataLevel {
	case ReviewMetadataComplete:
		if !validContentHash(e.MetadataHash) || e.ReasonCode == ReasonSpecialMetadataUnavailable {
			return ErrInvalidReviewOnlyEntryEvidence
		}
	case ReviewMetadataPathOnly:
		if e.MetadataHash != "" && !validContentHash(e.MetadataHash) || e.ReasonCode != ReasonSpecialMetadataUnavailable {
			return ErrInvalidReviewOnlyEntryEvidence
		}
	default:
		return ErrInvalidReviewOnlyEntryEvidence
	}
	return nil
}

// SpecialFileKindFromMode classifies no-follow filesystem metadata without
// opening the entry or following an alias.
func SpecialFileKindFromMode(mode fs.FileMode) (SpecialFileKind, bool) {
	switch {
	case mode&fs.ModeSocket != 0:
		return SpecialSocket, true
	case mode&fs.ModeNamedPipe != 0:
		return SpecialFIFO, true
	case mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice != 0:
		return SpecialCharDevice, true
	case mode&fs.ModeDevice != 0:
		return SpecialBlockDevice, true
	case mode&fs.ModeIrregular != 0:
		return SpecialUnknown, true
	default:
		return "", false
	}
}

// NewCompleteReviewOnlyEntryEvidence creates bounded metadata evidence. The
// hash covers only stable no-follow metadata and never includes the path.
func NewCompleteReviewOnlyEntryEvidence(kind SpecialFileKind, mode fs.FileMode, size int64, modified time.Time) ReviewOnlyEntryEvidence {
	metadata := fmt.Sprintf("%s|%#o|%d|%d", kind, uint32(mode), size, modified.UnixNano())
	digest := sha256.Sum256([]byte(metadata))
	return ReviewOnlyEntryEvidence{
		SpecialKind:     kind,
		MetadataLevel:   ReviewMetadataComplete,
		MetadataHash:    hex.EncodeToString(digest[:]),
		ReasonCode:      specialReason(kind),
		EvidenceVersion: ReviewOnlyEvidenceVersion,
	}
}

// NewPathOnlyReviewOnlyEntryEvidence preserves a known path when no-follow
// metadata could not be read safely.
func NewPathOnlyReviewOnlyEntryEvidence() ReviewOnlyEntryEvidence {
	return ReviewOnlyEntryEvidence{
		SpecialKind:     SpecialUnknown,
		MetadataLevel:   ReviewMetadataPathOnly,
		ReasonCode:      ReasonSpecialMetadataUnavailable,
		EvidenceVersion: ReviewOnlyEvidenceVersion,
	}
}

func specialReason(kind SpecialFileKind) string {
	switch kind {
	case SpecialSocket:
		return ReasonSpecialSocket
	case SpecialFIFO:
		return ReasonSpecialFIFO
	case SpecialCharDevice:
		return ReasonSpecialCharDevice
	case SpecialBlockDevice:
		return ReasonSpecialBlockDevice
	case SpecialJunction:
		return ReasonSpecialJunctionOrReparse
	default:
		return ReasonSpecialUnknown
	}
}

func validSpecialReason(value string) bool {
	switch value {
	case ReasonSpecialSocket, ReasonSpecialFIFO, ReasonSpecialCharDevice, ReasonSpecialBlockDevice, ReasonSpecialJunctionOrReparse, ReasonSpecialUnknown, ReasonSpecialMetadataUnavailable:
		return true
	default:
		return false
	}
}
