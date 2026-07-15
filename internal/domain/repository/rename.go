package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

var ErrInvalidRenameEvidence = errors.New("invalid rename evidence")

// RenameEvidence is the per-file result of the bounded Git rename/copy
// policy. It is optional on legacy changes, which remain reviewable but are
// not actionable as renames or copies.
type RenameEvidence struct {
	PolicyVersion     uint32
	SimilarityPercent uint8
	Kind              ChangeKind
	EvidenceHash      string
}

// NewRenameEvidence binds one accepted Git score to its raw old/new paths.
func NewRenameEvidence(policyVersion uint32, similarityPercent uint8, kind ChangeKind, oldPath, newPath RepoPath) (RenameEvidence, error) {
	result := RenameEvidence{PolicyVersion: policyVersion, SimilarityPercent: similarityPercent, Kind: kind}
	if oldPath.Validate() != nil || newPath.Validate() != nil || string(oldPath) == string(newPath) || result.PolicyVersion == 0 || result.SimilarityPercent < 60 || result.SimilarityPercent > 100 || result.Kind != ChangeRenamed && result.Kind != ChangeCopied {
		return RenameEvidence{}, ErrInvalidRenameEvidence
	}
	result.EvidenceHash = renameEvidenceHash(result, oldPath, newPath)
	if result.Validate() != nil {
		return RenameEvidence{}, ErrInvalidRenameEvidence
	}
	return result, nil
}

func (e RenameEvidence) Validate() error {
	if e.PolicyVersion == 0 || e.SimilarityPercent < 60 || e.SimilarityPercent > 100 || e.Kind != ChangeRenamed && e.Kind != ChangeCopied || !validContentHash(e.EvidenceHash) {
		return ErrInvalidRenameEvidence
	}
	return nil
}

// MatchesPaths proves that the evidence belongs to the retained raw paths.
func (e RenameEvidence) MatchesPaths(oldPath, newPath RepoPath) bool {
	return e.Validate() == nil && oldPath.Validate() == nil && newPath.Validate() == nil && string(oldPath) != string(newPath) && e.EvidenceHash == renameEvidenceHash(e, oldPath, newPath)
}

func renameEvidenceHash(e RenameEvidence, oldPath, newPath RepoPath) string {
	h := sha256.New()
	writeRenamePart(h, strconv.FormatUint(uint64(e.PolicyVersion), 10))
	writeRenamePart(h, strconv.FormatUint(uint64(e.SimilarityPercent), 10))
	writeRenamePart(h, string(e.Kind))
	writeRenamePart(h, string(oldPath))
	writeRenamePart(h, string(newPath))
	return hex.EncodeToString(h.Sum(nil))
}

func writeRenamePart(h interface{ Write([]byte) (int, error) }, value string) {
	_, _ = h.Write([]byte(strconv.FormatUint(uint64(len(value)), 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

// RenamePolicyEvidenceHash computes the stable identity of one bounded
// policy outcome without depending on Git adapter types.
func RenamePolicyEvidenceHash(version uint32, similarityPercent uint8, maxDeleteSources, maxAddTargets int, detectChangedSourceCopies, findCopiesHarder bool, outcome string, deleteCandidates, addCandidates int, flags []string) string {
	h := sha256.New()
	for _, value := range []string{
		fmt.Sprint(version), fmt.Sprint(similarityPercent), fmt.Sprint(maxDeleteSources), fmt.Sprint(maxAddTargets),
		fmt.Sprint(detectChangedSourceCopies), fmt.Sprint(findCopiesHarder), outcome, fmt.Sprint(deleteCandidates), fmt.Sprint(addCandidates),
	} {
		writeRenamePart(h, value)
	}
	for _, flag := range flags {
		writeRenamePart(h, flag)
	}
	return hex.EncodeToString(h.Sum(nil))
}
