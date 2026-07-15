package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

// ReconcileAnchor relocates one persisted anchor against one accepted capture.
// It never reads a path and never mutates the input anchor.
func ReconcileAnchor(input review.ReconcileInput) (review.ReconcileOutcome, error) {
	if err := input.Validate(); err != nil {
		return review.ReconcileOutcome{}, err
	}
	path, ok := reconciliationPath(input)
	if !ok {
		return orphanedOutcome(input, "scoped_path_unavailable")
	}

	content := input.NewContent
	oldCount := input.Anchor.EndLine - input.Anchor.StartLine + 1
	if oldCount <= 0 || oldCount > len(content.Lines) {
		return orphanedOutcome(input, "selected_range_not_present")
	}

	identityMatch := contentIdentityMatches(input, path, content)
	if identityMatch && path == string(input.Anchor.Path) {
		if candidate, found := candidateAt(input, content, input.Anchor.StartLine, review.EvidenceTierExactGeneration, "exact_content_identity"); found {
			return relocatedOutcome(input, content, candidate, review.AnchorValid, "exact_content_identity")
		}
	}

	if candidates, found, overflow := firstCandidates(input, content, oldCount, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && start == input.Anchor.StartLine && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, content.Lines, start, start+oldCount-1)
	}, review.EvidenceTierExactContextAtLine, "selected_and_context_at_original_line"); found {
		return outcomeFromCandidates(input, content, candidates, overflow, "selected_and_context_at_original_line")
	}

	if candidates, found, overflow := firstCandidates(input, content, oldCount, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && withinAnchorWindow(input.Anchor.StartLine, start) && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, content.Lines, start, start+oldCount-1)
	}, review.EvidenceTierContextWindow, "selected_and_context_within_window"); found {
		return outcomeFromCandidates(input, content, candidates, overflow, "selected_and_context_within_window")
	}

	if candidates, found, overflow := firstCandidates(input, content, oldCount, func(start int, selected string) bool {
		return hasContextEvidence(input.Anchor) && selectionMatches(input.Anchor, selected) && contextMatches(input.Anchor, content.Lines, start, start+oldCount-1)
	}, review.EvidenceTierContextFile, "selected_and_context_in_file"); found {
		return outcomeFromCandidates(input, content, candidates, overflow, "selected_and_context_in_file")
	}

	if candidates, found, overflow := firstCandidates(input, content, oldCount, func(start int, selected string) bool {
		return withinAnchorWindow(input.Anchor.StartLine, start) && selectionMatches(input.Anchor, selected)
	}, review.EvidenceTierSelectionWindow, "unique_selected_range_within_window"); found {
		return outcomeFromCandidates(input, content, candidates, overflow, "unique_selected_range_within_window")
	}

	if candidates, found, overflow := firstCandidates(input, content, oldCount, func(_ int, selected string) bool {
		return selectionMatches(input.Anchor, selected)
	}, review.EvidenceTierSelectionFile, "unique_selected_range_in_file"); found {
		return outcomeFromCandidates(input, content, candidates, overflow, "unique_selected_range_in_file")
	}

	if candidates, found := lineDiffCandidate(input, content, path, oldCount); found {
		return outcomeFromCandidates(input, content, candidates, false, "versioned_line_diff")
	}

	return orphanedOutcome(input, "anchor_evidence_not_found")
}

// ReconcileInput and related aliases keep the application seam convenient for
// callers while the durable contracts remain owned by the review domain.
type ReconcileInput = review.ReconcileInput
type ReconcileOutcome = review.ReconcileOutcome
type AnchorCandidate = review.AnchorCandidate
type GenerationTransition = review.GenerationTransition

func reconciliationPath(input review.ReconcileInput) (string, bool) {
	oldPath := string(input.Anchor.Path)
	if string(input.NewContent.Path) == oldPath {
		return oldPath, true
	}
	if !renameMappingsAllowed(input.Transition) {
		return "", false
	}
	var mapped string
	for _, mapping := range input.Transition.RenameMappings {
		if string(mapping.OldPath) != oldPath {
			continue
		}
		if mapped != "" {
			return "", false
		}
		mapped = string(mapping.NewPath)
	}
	if mapped == string(input.NewContent.Path) {
		return mapped, true
	}
	return "", false
}

func renameMappingsAllowed(transition review.GenerationTransition) bool {
	if len(transition.RenameMappings) == 0 {
		return false
	}
	if transition.RenameEvidence.Complete() {
		return completeRenameMappings(transition.RenameMappings)
	}
	return transition.FromRenameEvidence.Complete() && transition.ToRenameEvidence.Complete() && sameRenamePolicyEvidence(transition.FromRenameEvidence, transition.ToRenameEvidence) && completeRenameMappings(transition.RenameMappings)
}

func sameRenamePolicyEvidence(left, right review.RenamePolicyEvidence) bool {
	if left.Version != right.Version || left.SimilarityPercent != right.SimilarityPercent || left.MaxDeleteSources != right.MaxDeleteSources || left.MaxAddTargets != right.MaxAddTargets || left.DetectChangedSourceCopies != right.DetectChangedSourceCopies || left.FindCopiesHarder != right.FindCopiesHarder || left.Outcome != right.Outcome || left.DeleteCandidates != right.DeleteCandidates || left.AddCandidates != right.AddCandidates || left.EvidenceHash != right.EvidenceHash || len(left.Flags) != len(right.Flags) {
		return false
	}
	for index := range left.Flags {
		if left.Flags[index] != right.Flags[index] {
			return false
		}
	}
	return true
}

func completeRenameMappings(mappings []review.RenameMapping) bool {
	if len(mappings) == 0 {
		return false
	}
	for _, mapping := range mappings {
		if mapping.Validate() != nil || mapping.SimilarityPercent < 60 || mapping.Kind != repository.ChangeRenamed && mapping.Kind != repository.ChangeCopied || len(mapping.EvidenceHash) != 64 {
			return false
		}
	}
	return true
}

func contentIdentityMatches(input review.ReconcileInput, path string, content review.CapturedFile) bool {
	if content.ContentIdentity == "" {
		return false
	}
	for _, identity := range input.Transition.ChangedPaths {
		if string(identity.OldPath) == string(input.Anchor.Path) && string(identity.NewPath) == path && identity.OldContentIdentity != "" && identity.OldContentIdentity == identity.NewContentIdentity && identity.NewContentIdentity == content.ContentIdentity {
			return true
		}
	}
	return false
}

func firstCandidates(input review.ReconcileInput, content review.CapturedFile, count int, matches func(int, string) bool, tier review.EvidenceTier, reason string) ([]review.AnchorCandidate, bool, bool) {
	var candidates []review.AnchorCandidate
	for start := 1; start <= len(content.Lines)-count+1; start++ {
		selected := strings.Join(content.Lines[start-1:start+count-1], "\n")
		if !matches(start, selected) {
			continue
		}
		candidates = append(candidates, review.AnchorCandidate{Path: append([]byte(nil), content.Path...), Side: content.Side, StartLine: start, EndLine: start + count - 1, Tier: tier, Reason: reason})
	}
	retained, overflow := capCandidates(candidates)
	return retained, len(candidates) > 0, overflow
}

func capCandidates(candidates []review.AnchorCandidate) ([]review.AnchorCandidate, bool) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if string(candidates[i].Path) != string(candidates[j].Path) {
			return string(candidates[i].Path) < string(candidates[j].Path)
		}
		if candidates[i].Side != candidates[j].Side {
			return candidates[i].Side < candidates[j].Side
		}
		return candidates[i].StartLine < candidates[j].StartLine
	})
	if len(candidates) <= review.MaxAnchorReconciliationCandidates {
		return candidates, false
	}
	return candidates[:review.MaxAnchorReconciliationCandidates], true
}

func candidateAt(input review.ReconcileInput, content review.CapturedFile, start int, tier review.EvidenceTier, reason string) (review.AnchorCandidate, bool) {
	count := input.Anchor.EndLine - input.Anchor.StartLine + 1
	if start <= 0 || start+count-1 > len(content.Lines) {
		return review.AnchorCandidate{}, false
	}
	selected := strings.Join(content.Lines[start-1:start+count-1], "\n")
	if !selectionMatches(input.Anchor, selected) {
		return review.AnchorCandidate{}, false
	}
	return review.AnchorCandidate{Path: append([]byte(nil), content.Path...), Side: content.Side, StartLine: start, EndLine: start + count - 1, Tier: tier, Reason: reason}, true
}

func outcomeFromCandidates(input review.ReconcileInput, content review.CapturedFile, candidates []review.AnchorCandidate, overflow bool, reason string) (review.ReconcileOutcome, error) {
	if len(candidates) == 0 {
		return orphanedOutcome(input, "anchor_evidence_not_found")
	}
	if len(candidates) > 1 {
		return ambiguousOutcome(input, candidates, overflow, reason)
	}
	state := review.AnchorRelocated
	if string(candidates[0].Path) == string(input.Anchor.Path) && candidates[0].StartLine == input.Anchor.StartLine && candidates[0].EndLine == input.Anchor.EndLine {
		state = review.AnchorValid
	}
	return relocatedOutcome(input, content, candidates[0], state, reason)
}

func relocatedOutcome(input review.ReconcileInput, content review.CapturedFile, candidate review.AnchorCandidate, state review.AnchorState, reason string) (review.ReconcileOutcome, error) {
	anchor := input.Anchor
	anchor.TargetGeneration = input.Transition.ToGeneration
	anchor.State = state
	anchor.FingerprintVersion = review.AnchorFingerprintVersion
	anchor.CreatedAt = input.Anchor.CreatedAt
	if input.Transition.NewBase.Kind != "" {
		anchor.Base = input.Transition.NewBase
	}
	if input.Transition.NewHead.Kind != "" {
		anchor.Head = input.Transition.NewHead
	}
	if string(candidate.Path) != string(input.Anchor.Path) {
		anchor.PreviousPath = append([]byte(nil), input.Anchor.Path...)
	}
	anchor.Path = append([]byte(nil), candidate.Path...)
	anchor.StartLine = candidate.StartLine
	anchor.EndLine = candidate.EndLine
	selected := strings.Join(content.Lines[candidate.StartLine-1:candidate.EndLine], "\n")
	anchor.SelectionHash = review.FingerprintSelection(selected)
	if anchor.BeforeContextHash != "" {
		anchor.BeforeContextHash = review.FingerprintContext(contextLines(content.Lines, candidate.StartLine, -1))
	}
	if anchor.AfterContextHash != "" {
		anchor.AfterContextHash = review.FingerprintContext(contextLines(content.Lines, candidate.EndLine, 1))
	}
	if state != review.AnchorValid || string(candidate.Path) != string(input.Anchor.Path) || candidate.StartLine != input.Anchor.StartLine || candidate.EndLine != input.Anchor.EndLine {
		metadata := &review.RelocationMetadata{PreviousPath: append([]byte(nil), input.Anchor.Path...), PreviousStartLine: input.Anchor.StartLine, PreviousEndLine: input.Anchor.EndLine, Reason: reason, ReconciledAt: input.Now.UTC()}
		anchor.Relocation = metadata
	}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		return review.ReconcileOutcome{}, fmt.Errorf("relocated anchor: %w", err)
	}
	outcome := review.ReconcileOutcome{Anchor: validated, State: state, Reason: reason, AlgorithmVersion: review.AnchorReconciliationAlgorithmVersion}
	if err := outcome.Validate(); err != nil {
		return review.ReconcileOutcome{}, err
	}
	return outcome, nil
}

func ambiguousOutcome(input review.ReconcileInput, candidates []review.AnchorCandidate, overflow bool, reason string) (review.ReconcileOutcome, error) {
	anchor := input.Anchor
	anchor.TargetGeneration = input.Transition.ToGeneration
	anchor.State = review.AnchorAmbiguous
	anchor.FingerprintVersion = review.AnchorFingerprintVersion
	if input.Transition.NewBase.Kind != "" {
		anchor.Base = input.Transition.NewBase
	}
	if input.Transition.NewHead.Kind != "" {
		anchor.Head = input.Transition.NewHead
	}
	anchor.Relocation = &review.RelocationMetadata{PreviousPath: append([]byte(nil), input.Anchor.Path...), PreviousStartLine: input.Anchor.StartLine, PreviousEndLine: input.Anchor.EndLine, Reason: reason, ReconciledAt: input.Now.UTC()}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		return review.ReconcileOutcome{}, err
	}
	outcome := review.ReconcileOutcome{Anchor: validated, Candidates: candidates, CandidateOverflow: overflow, State: review.AnchorAmbiguous, Reason: reason, AlgorithmVersion: review.AnchorReconciliationAlgorithmVersion}
	if err := outcome.Validate(); err != nil {
		return review.ReconcileOutcome{}, err
	}
	return outcome, nil
}

func orphanedOutcome(input review.ReconcileInput, reason string) (review.ReconcileOutcome, error) {
	anchor := input.Anchor
	anchor.TargetGeneration = input.Transition.ToGeneration
	anchor.State = review.AnchorOrphaned
	anchor.FingerprintVersion = review.AnchorFingerprintVersion
	if input.Transition.NewBase.Kind != "" {
		anchor.Base = input.Transition.NewBase
	}
	if input.Transition.NewHead.Kind != "" {
		anchor.Head = input.Transition.NewHead
	}
	anchor.Relocation = &review.RelocationMetadata{PreviousPath: append([]byte(nil), input.Anchor.Path...), PreviousStartLine: input.Anchor.StartLine, PreviousEndLine: input.Anchor.EndLine, Reason: reason, ReconciledAt: input.Now.UTC()}
	validated, err := review.NewCodeAnchor(anchor)
	if err != nil {
		return review.ReconcileOutcome{}, err
	}
	outcome := review.ReconcileOutcome{Anchor: validated, State: review.AnchorOrphaned, Reason: reason, AlgorithmVersion: review.AnchorReconciliationAlgorithmVersion}
	if err := outcome.Validate(); err != nil {
		return review.ReconcileOutcome{}, err
	}
	return outcome, nil
}

func selectionMatches(anchor review.CodeAnchor, selected string) bool {
	if anchor.SelectedText != "" {
		return review.NormalizeAnchorFingerprintText(anchor.SelectedText) == review.NormalizeAnchorFingerprintText(selected)
	}
	return anchor.SelectionHash == review.FingerprintSelection(selected) || anchor.SelectionHash == review.LegacyFingerprintSelection(anchor.Side, anchor.Path, anchor.StartLine, anchor.EndLine, selected)
}

func contextMatches(anchor review.CodeAnchor, lines []string, start, end int) bool {
	if anchor.BeforeContextHash != "" {
		actual := review.FingerprintContext(contextLines(lines, start, -1))
		if anchor.BeforeContextHash != actual && anchor.BeforeContextHash != legacyContextHash(anchor, lines, start, -1) {
			return false
		}
	}
	if anchor.AfterContextHash != "" {
		actual := review.FingerprintContext(contextLines(lines, end, 1))
		if anchor.AfterContextHash != actual && anchor.AfterContextHash != legacyContextHash(anchor, lines, end, 1) {
			return false
		}
	}
	return true
}

func hasContextEvidence(anchor review.CodeAnchor) bool {
	return anchor.BeforeContextHash != "" || anchor.AfterContextHash != ""
}

func legacyContextHash(anchor review.CodeAnchor, lines []string, edge, direction int) string {
	values := contextLines(lines, edge, direction)
	legacy := make([]review.LegacyContextLine, len(values))
	for index, value := range values {
		line := anchor.StartLine - len(values) + index
		if direction > 0 {
			line = anchor.EndLine + 1 + index
		}
		legacy[index] = review.LegacyContextLine{Line: line, Text: value}
	}
	return review.LegacyFingerprintContext(legacy)
}

func contextLines(lines []string, edge, direction int) []string {
	var result []string
	if direction < 0 {
		first := edge - 1 - 3
		if first < 0 {
			first = 0
		}
		for index := first; index < edge-1 && index < len(lines); index++ {
			result = append(result, lines[index])
		}
		return result
	}
	last := edge + 3
	if last > len(lines) {
		last = len(lines)
	}
	for index := edge; index < last; index++ {
		result = append(result, lines[index])
	}
	return result
}

func withinAnchorWindow(original, candidate int) bool {
	distance := int64(candidate) - int64(original)
	if distance < 0 {
		distance = -distance
	}
	return distance <= review.AnchorReconciliationWindow
}

func lineDiffCandidate(input review.ReconcileInput, content review.CapturedFile, path string, count int) ([]review.AnchorCandidate, bool) {
	if input.PreviousContent == nil || string(input.PreviousContent.Path) != string(input.Anchor.Path) || len(input.PreviousContent.Lines) == 0 {
		return nil, false
	}
	pairs := myersEqualLines(input.PreviousContent.Lines, content.Lines)
	var mapped []linePair
	for _, pair := range pairs {
		if pair.oldLine < input.Anchor.StartLine || pair.oldLine > input.Anchor.EndLine {
			continue
		}
		mapped = append(mapped, pair)
	}
	if len(mapped) != count {
		return nil, false
	}
	first := mapped[0]
	for index, candidate := range mapped[1:] {
		if candidate.oldLine != first.oldLine+index+1 || candidate.newLine != first.newLine+index+1 {
			return nil, false
		}
	}
	if first.newLine <= 0 || first.newLine+count-1 > len(content.Lines) || !selectionMatches(input.Anchor, strings.Join(content.Lines[first.newLine-1:first.newLine+count-1], "\n")) {
		return nil, false
	}
	if !contextMatches(input.Anchor, content.Lines, first.newLine, first.newLine+count-1) {
		return nil, false
	}
	return []review.AnchorCandidate{{Path: append([]byte(nil), path...), Side: content.Side, StartLine: first.newLine, EndLine: first.newLine + count - 1, Tier: review.EvidenceTierLineDiff, Reason: "versioned_line_diff"}}, true
}

type linePair struct{ oldLine, newLine int }

func myersEqualLines(oldLines, newLines []string) []linePair {
	if len(oldLines) == 0 || len(newLines) == 0 {
		return nil
	}
	max := len(oldLines) + len(newLines)
	v := map[int]int{1: 0}
	trace := make([]map[int]int, 0, max+1)
	for distance := 0; distance <= max; distance++ {
		current := make(map[int]int, distance+1)
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			var x int
			if diagonal == -distance || diagonal != distance && v[diagonal-1] < v[diagonal+1] {
				x = v[diagonal+1]
			} else {
				x = v[diagonal-1] + 1
			}
			y := x - diagonal
			for x < len(oldLines) && y < len(newLines) && review.NormalizeAnchorFingerprintText(oldLines[x]) == review.NormalizeAnchorFingerprintText(newLines[y]) {
				x++
				y++
			}
			current[diagonal] = x
			if x >= len(oldLines) && y >= len(newLines) {
				trace = append(trace, current)
				return reconstructLinePairs(trace, oldLines, newLines)
			}
		}
		trace = append(trace, current)
		v = current
	}
	return nil
}

func reconstructLinePairs(trace []map[int]int, oldLines, newLines []string) []linePair {
	if len(trace) == 0 {
		return nil
	}
	x, y := len(oldLines), len(newLines)
	pairs := make([]linePair, 0)
	for distance := len(trace) - 1; distance > 0; distance-- {
		previous := trace[distance-1]
		diagonal := x - y
		var previousDiagonal int
		if diagonal == -distance || diagonal != distance && previous[diagonal-1] < previous[diagonal+1] {
			previousDiagonal = diagonal + 1
		} else {
			previousDiagonal = diagonal - 1
		}
		previousX := previous[previousDiagonal]
		previousY := previousX - previousDiagonal
		for x > previousX && y > previousY {
			pairs = append(pairs, linePair{oldLine: x, newLine: y})
			x--
			y--
		}
		x, y = previousX, previousY
	}
	for left, right := 0, len(pairs)-1; left < right; left, right = left+1, right-1 {
		pairs[left], pairs[right] = pairs[right], pairs[left]
	}
	return pairs
}
