package app

import (
	"sort"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// AcceptedRenameEvidence converts one capture's persisted Git policy record
// into the anchor reconciler's neutral evidence without recomputing policy.
func AcceptedRenameEvidence(policy repository.CapturePolicyEvidence) (review.RenamePolicyEvidence, error) {
	if policy.Validate() != nil || policy.RenameEvidenceHash == "" {
		return review.RenamePolicyEvidence{}, repository.ErrInvalidRenameEvidence
	}
	result := review.RenamePolicyEvidence{
		Version:                   policy.RenameVersion,
		SimilarityPercent:         policy.RenameSimilarityPercent,
		MaxDeleteSources:          int(policy.RenameMaxDeleteSources),
		MaxAddTargets:             int(policy.RenameMaxAddTargets),
		DetectChangedSourceCopies: policy.RenameDetectChangedSourceCopies,
		FindCopiesHarder:          policy.RenameFindCopiesHarder,
		Outcome:                   policy.RenameOutcome,
		DeleteCandidates:          int(policy.RenameDeleteCandidates),
		AddCandidates:             int(policy.RenameAddCandidates),
		Flags:                     append([]string(nil), policy.RenameFlags...),
		EvidenceHash:              policy.RenameEvidenceHash,
	}
	if result.Validate() != nil {
		return review.RenamePolicyEvidence{}, repository.ErrInvalidRenameEvidence
	}
	return result, nil
}

// AcceptedRenameMappings projects only per-file evidence retained by an
// accepted capture. It supplies path scope to anchor reconciliation, never
// line placement or an inferred delete/add pairing.
func AcceptedRenameMappings(changes []repository.ChangedFile, side repository.DiffSide) ([]review.RenameMapping, error) {
	if side != repository.DiffBase && side != repository.DiffHead {
		return nil, repository.ErrInvalidRenameEvidence
	}
	result := make([]review.RenameMapping, 0)
	seen := make(map[string]struct{})
	for _, change := range changes {
		if change.Kind != repository.ChangeRenamed && change.Kind != repository.ChangeCopied {
			continue
		}
		if change.Validate() != nil || change.Rename == nil || change.OldPath == nil || change.NewPath == nil || !change.Rename.MatchesPaths(*change.OldPath, *change.NewPath) {
			return nil, repository.ErrInvalidRenameEvidence
		}
		key := string(change.OldPath.Bytes()) + "\x00" + string(change.NewPath.Bytes()) + "\x00" + string(change.Kind)
		if _, exists := seen[key]; exists {
			return nil, repository.ErrInvalidRenameEvidence
		}
		seen[key] = struct{}{}
		result = append(result, review.RenameMapping{
			OldPath:           repository.RepoPath(change.OldPath.Bytes()),
			NewPath:           repository.RepoPath(change.NewPath.Bytes()),
			Side:              side,
			SimilarityPercent: change.Rename.SimilarityPercent,
			Kind:              change.Rename.Kind,
			EvidenceHash:      change.Rename.EvidenceHash,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if string(result[i].OldPath) == string(result[j].OldPath) {
			return string(result[i].NewPath) < string(result[j].NewPath)
		}
		return string(result[i].OldPath) < string(result[j].OldPath)
	})
	return result, nil
}
