package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

var (
	ErrInvalidDiscussionContext = errors.New("invalid discussion context")
	ErrProviderInputLimit       = errors.New("provider_input_limit")
)

// DiscussionPromptLimits is the T070-bound input contract for one focused
// discussion. The limits are measured in UTF-8 bytes before provider JSON
// encoding.
type DiscussionPromptLimits struct {
	ConcernBytes            ByteSize
	SelectedAnchorBytes     ByteSize
	HunkContextBytes        ByteSize
	SelectedTranscriptBytes ByteSize
	SerializedInputBytes    ByteSize
}

func discussionPromptLimits(policy ResourcePolicy) (DiscussionPromptLimits, error) {
	if err := policy.Validate(); err != nil {
		return DiscussionPromptLimits{}, err
	}
	return DiscussionPromptLimits{
		ConcernBytes:            policy.Provider.ConcernBytes,
		SelectedAnchorBytes:     policy.Provider.SelectedAnchorBytes,
		HunkContextBytes:        policy.Provider.HunkContextBytes,
		SelectedTranscriptBytes: policy.Provider.SelectedTranscriptBytes,
		SerializedInputBytes:    policy.Provider.SerializedTurnInputBytes,
	}, nil
}

// DiscussionLineRange identifies the selected inclusive source range.
type DiscussionLineRange struct {
	Start int
	End   int
}

// DiscussionContextUnit is an optional whole context unit. Distance and
// Ordinal give compaction a deterministic closest/newest preference without
// changing the user's concern or selected evidence.
type DiscussionContextUnit struct {
	Label    string
	Text     string
	Distance uint64
	Ordinal  uint64
}

// DiscussionContext is the bounded, review-specific context sent to a
// provider. It contains captured evidence only; a filesystem turn may use its
// working directory for additional inspection under a separate lease policy.
type DiscussionContext struct {
	Target             string
	Path               repository.RepoPath
	Side               repository.DiffSide
	Lines              DiscussionLineRange
	SelectedText       string
	Hunk               string
	UserConcern        string
	WorkingDir         string
	RelatedContext     []DiscussionContextUnit
	SelectedTranscript []DiscussionContextUnit
}

func (c DiscussionContext) Validate() error {
	if !safeDiscussionText(c.Target, false) || c.Path.Validate() != nil || !utf8.Valid(c.Path) || c.Side != repository.DiffBase && c.Side != repository.DiffHead || c.Lines.Start <= 0 || c.Lines.End < c.Lines.Start || !safeDiscussionText(c.UserConcern, false) || !safeDiscussionText(c.SelectedText, true) || !safeDiscussionText(c.Hunk, true) {
		return ErrInvalidDiscussionContext
	}
	if c.WorkingDir != "" && (!filepath.IsAbs(c.WorkingDir) || filepath.Clean(c.WorkingDir) != c.WorkingDir || !safeDiscussionText(c.WorkingDir, false)) {
		return ErrInvalidDiscussionContext
	}
	for _, unit := range append(append([]DiscussionContextUnit(nil), c.RelatedContext...), c.SelectedTranscript...) {
		if !safeDiscussionText(unit.Label, false) || !safeDiscussionText(unit.Text, true) {
			return ErrInvalidDiscussionContext
		}
	}
	return nil
}

func safeDiscussionText(value string, allowEmpty bool) bool {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return false
	}
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

// BuildDiscussionPrompt builds the default-policy prompt for one focused
// review discussion. Optional context is compacted by whole units and every
// omission is visible in the resulting prompt.
func BuildDiscussionPrompt(context DiscussionContext) (string, error) {
	return BuildDiscussionPromptWithPolicy(context, DefaultResourcePolicy())
}

// BuildDiscussionPromptWithPolicy applies the versioned resource policy and
// returns provider_input_limit before truncating mandatory user evidence.
func BuildDiscussionPromptWithPolicy(context DiscussionContext, policy ResourcePolicy) (string, error) {
	limits, err := discussionPromptLimits(policy)
	if err != nil {
		return "", err
	}
	if err := context.Validate(); err != nil {
		return "", err
	}
	if uint64(len([]byte(context.UserConcern))) > uint64(limits.ConcernBytes) || uint64(len([]byte(context.SelectedText))) > uint64(limits.SelectedAnchorBytes) {
		return "", ErrProviderInputLimit
	}

	optional := discussionOptionalUnits(context)
	chosen, omitted := compactDiscussionUnits(optional, limits)
	for {
		prompt := renderDiscussionPrompt(context, chosen, omitted)
		if uint64(len([]byte(prompt))) <= uint64(limits.SerializedInputBytes) {
			return prompt, nil
		}
		if len(chosen) == 0 {
			return "", ErrProviderInputLimit
		}
		worst := worstOptionalIndex(chosen)
		chosen = append(chosen[:worst], chosen[worst+1:]...)
		omitted++
	}
}

type optionalDiscussionUnit struct {
	kind     string
	unit     DiscussionContextUnit
	original int
}

func discussionOptionalUnits(context DiscussionContext) []optionalDiscussionUnit {
	units := make([]optionalDiscussionUnit, 0, len(context.RelatedContext)+len(context.SelectedTranscript)+1)
	if context.Hunk != "" {
		units = append(units, optionalDiscussionUnit{kind: "hunk", unit: DiscussionContextUnit{Label: "Relevant hunk", Text: context.Hunk, Distance: 0, Ordinal: ^uint64(0)}, original: 0})
	}
	for index, unit := range context.RelatedContext {
		units = append(units, optionalDiscussionUnit{kind: "context", unit: unit, original: index + 1})
	}
	for index, unit := range context.SelectedTranscript {
		units = append(units, optionalDiscussionUnit{kind: "transcript", unit: unit, original: index + len(context.RelatedContext) + 1})
	}
	return units
}

func compactDiscussionUnits(units []optionalDiscussionUnit, limits DiscussionPromptLimits) ([]optionalDiscussionUnit, int) {
	componentLimit := uint64(limits.HunkContextBytes)
	transcriptLimit := uint64(limits.SelectedTranscriptBytes)
	ordered := append([]optionalDiscussionUnit(nil), units...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].kind != ordered[j].kind {
			return ordered[i].kind < ordered[j].kind
		}
		if ordered[i].unit.Distance != ordered[j].unit.Distance {
			return ordered[i].unit.Distance < ordered[j].unit.Distance
		}
		if ordered[i].unit.Ordinal != ordered[j].unit.Ordinal {
			return ordered[i].unit.Ordinal > ordered[j].unit.Ordinal
		}
		return ordered[i].unit.Label < ordered[j].unit.Label
	})
	chosen := make([]optionalDiscussionUnit, 0, len(units))
	omitted := 0
	var hunkBytes, transcriptBytes uint64
	for _, unit := range ordered {
		size := uint64(len([]byte(unit.unit.Text)))
		switch unit.kind {
		case "transcript":
			if size > transcriptLimit || transcriptBytes > transcriptLimit-size {
				omitted++
				continue
			}
			transcriptBytes += size
		default:
			if size > componentLimit || hunkBytes > componentLimit-size {
				omitted++
				continue
			}
			hunkBytes += size
		}
		chosen = append(chosen, unit)
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].original < chosen[j].original })
	return chosen, omitted
}

func worstOptionalIndex(units []optionalDiscussionUnit) int {
	worst := 0
	for index := 1; index < len(units); index++ {
		left, right := units[worst], units[index]
		if right.unit.Distance > left.unit.Distance || right.unit.Distance == left.unit.Distance && right.unit.Ordinal < left.unit.Ordinal {
			worst = index
		}
	}
	return worst
}

func renderDiscussionPrompt(context DiscussionContext, units []optionalDiscussionUnit, omitted int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Nudge focused code-review discussion\n\n")
	fmt.Fprintf(&builder, "Read-only boundary: do not edit, create, delete, or format files. Answer the focused concern directly and avoid broad unsolicited rewrites.\n\n")
	fmt.Fprintf(&builder, "Target: %s\nPath: %s\nDiff side: %s\nSelected lines: %d-%d\n\n", context.Target, string(context.Path), context.Side, context.Lines.Start, context.Lines.End)
	builder.WriteString("Selected text:\n")
	if context.SelectedText == "" {
		builder.WriteString("[empty selection]\n")
	} else {
		builder.WriteString(context.SelectedText)
		builder.WriteByte('\n')
	}
	builder.WriteString("\nUser concern:\n")
	builder.WriteString(context.UserConcern)
	builder.WriteByte('\n')
	for _, unit := range units {
		switch unit.kind {
		case "hunk":
			builder.WriteString("\nRelevant hunk:\n")
		case "context":
			builder.WriteString("\nRelated review context (captured):\n")
		case "transcript":
			builder.WriteString("\nSelected prior discussion context:\n")
		}
		fmt.Fprintf(&builder, "[%s]\n%s\n", unit.unit.Label, unit.unit.Text)
	}
	if omitted > 0 {
		fmt.Fprintf(&builder, "\n[%d optional context unit(s) omitted due to input limits; mandatory concern and selected evidence are complete]\n", omitted)
	}
	builder.WriteString("\nInspect related code only through the read-only review boundary when available. Do not use network access or modify any workspace.\n")
	return builder.String()
}

// DiscussionPromptHash binds provenance to the exact bounded prompt without
// retaining the prompt body in durable state.
func DiscussionPromptHash(prompt string) string {
	digest := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(digest[:])
}

// NewDiscussionContextFromAnchor copies the immutable anchor evidence into a
// provider-facing context while keeping raw repository path identity intact.
func NewDiscussionContextFromAnchor(anchor review.CodeAnchor, target string, concern string) (DiscussionContext, error) {
	context := DiscussionContext{Target: target, Path: anchor.Path, Side: anchor.Side, Lines: DiscussionLineRange{Start: anchor.StartLine, End: anchor.EndLine}, SelectedText: anchor.SelectedText, UserConcern: concern}
	if err := context.Validate(); err != nil {
		return DiscussionContext{}, err
	}
	return context, nil
}

// DiscussionTurnProvenance records the evidence identity used by one turn.
type DiscussionTurnProvenance struct {
	Mode                    DiscussionMode
	ReviewSnapshotID        domain.ReviewSnapshotID
	SourceCaptureID         domain.CaptureID
	SourceSnapshotRef       string
	ContextHash             string
	ManifestHash            string
	CapabilityPolicyVersion CapabilityPolicyVersion
	ResourcePolicyVersion   ResourcePolicyVersion
	EvidenceVersion         EvidenceVersion
	PermissionVersion       string
}

func (p DiscussionTurnProvenance) IsZero() bool {
	return p == DiscussionTurnProvenance{}
}

func (p DiscussionTurnProvenance) Validate() error {
	if p.Mode == "" {
		return ErrInvalidDiscussionContext
	}
	if p.ContextHash == "" || len(p.ContextHash) != sha256.Size*2 || p.PermissionVersion == "" || p.CapabilityPolicyVersion == 0 || p.ResourcePolicyVersion == 0 || p.EvidenceVersion == 0 {
		return ErrInvalidDiscussionContext
	}
	if _, err := hex.DecodeString(p.ContextHash); err != nil {
		return ErrInvalidDiscussionContext
	}
	switch p.Mode {
	case DiscussionModeFilesystem:
		if p.ReviewSnapshotID == "" || p.SourceCaptureID == "" || p.ManifestHash == "" || len(p.ManifestHash) != sha256.Size*2 || p.SourceSnapshotRef != "" {
			return ErrInvalidDiscussionContext
		}
		if _, err := hex.DecodeString(p.ManifestHash); err != nil {
			return ErrInvalidDiscussionContext
		}
	case DiscussionModePromptOnly:
		if p.ReviewSnapshotID != "" || p.ManifestHash != "" || p.SourceSnapshotRef != "" && p.SourceCaptureID != "" {
			return ErrInvalidDiscussionContext
		}
	case DiscussionModeDisabled:
		return ErrInvalidDiscussionContext
	default:
		return ErrInvalidDiscussionContext
	}
	return nil
}
