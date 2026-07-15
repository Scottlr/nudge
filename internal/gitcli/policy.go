package gitcli

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

const currentGitPolicyVersion uint32 = MachineGitReadPolicyVersion

var (
	// ErrInvalidGitPolicy reports a malformed versioned Git policy value.
	ErrInvalidGitPolicy = errors.New("invalid Git policy")
	// ErrInvalidPolicyEvidence reports incomplete conformance evidence.
	ErrInvalidPolicyEvidence = errors.New("invalid Git policy evidence")
	// ErrUnknownPolicyEvidence reports a registry lookup miss.
	ErrUnknownPolicyEvidence = errors.New("unknown Git policy evidence")
)

// GitPolicyClass identifies one command family with its own argv and
// configuration contract.
type GitPolicyClass string

const (
	GitPolicyMachineRead       GitPolicyClass = "machine_read"
	GitPolicyPatchDerivation   GitPolicyClass = "patch_derivation"
	GitPolicyContentConversion GitPolicyClass = "content_conversion"
	GitPolicyApplyCheck        GitPolicyClass = "apply_check"
	GitPolicyApplyMutation     GitPolicyClass = "apply_mutation"
)

// GitPolicySelection binds a policy class to an explicit version.
type GitPolicySelection struct {
	Class   GitPolicyClass
	Version uint32
}

// Validate checks that a selection names one supported v1 policy class.
func (s GitPolicySelection) Validate() error {
	if s.Version != currentGitPolicyVersion {
		return ErrInvalidGitPolicy
	}
	switch s.Class {
	case GitPolicyMachineRead, GitPolicyPatchDerivation, GitPolicyContentConversion, GitPolicyApplyCheck, GitPolicyApplyMutation:
		return nil
	default:
		return ErrInvalidGitPolicy
	}
}

// Selection returns the explicit class/version pair for a Git policy.
func Selection(class GitPolicyClass) GitPolicySelection {
	return GitPolicySelection{Class: class, Version: currentGitPolicyVersion}
}

// EnvironmentPolicy returns the complete inherited-environment suppression
// used by every non-mutating machine Git read.
func (p MachineGitReadPolicyV1) EnvironmentPolicy() process.EnvironmentPolicy {
	return process.EnvironmentPolicy{
		Mode: process.EnvironmentInherit,
		Set: map[string]string{
			"GIT_CONFIG_GLOBAL":   os.DevNull,
			"GIT_CONFIG_NOSYSTEM": "1",
			"GIT_CONFIG_SYSTEM":   os.DevNull,
			"GIT_NO_LAZY_FETCH":   "1",
			"GIT_OPTIONAL_LOCKS":  "0",
			"GIT_TERMINAL_PROMPT": "0",
			"LC_ALL":              "C",
			"LANG":                "C",
			"LANGUAGE":            "C",
		},
		Remove: []string{
			"GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_ASKPASS",
			"GIT_CEILING_DIRECTORIES",
			"GIT_CONFIG_COUNT",
			"GIT_DIR",
			"GIT_DIFF_OPTS",
			"GIT_EDITOR",
			"GIT_EXTERNAL_DIFF",
			"GIT_INDEX_FILE",
			"GIT_OBJECT_DIRECTORY",
			"GIT_PAGER",
			"GIT_SSH_COMMAND",
			"GIT_WORK_TREE",
			"GIT_SEQUENCE_EDITOR",
			"GIT_TRACE",
			"GIT_TRACE2",
			"GIT_TRACE2_EVENT",
			"GIT_TRACE2_PERF",
			"PAGER",
			"EDITOR",
			"VISUAL",
		},
	}
}

// RenamePolicyV1 fixes bounded rename and changed-source copy detection.
type RenamePolicyV1 struct {
	Version                   uint32
	SimilarityPercent         uint8
	MaxDeleteSources          int
	MaxAddTargets             int
	DetectChangedSourceCopies bool
	FindCopiesHarder          bool
}

// DefaultRenamePolicyV1 returns the only v1 rename/copy policy admitted for
// deterministic capture and patch derivation.
func DefaultRenamePolicyV1() RenamePolicyV1 {
	return RenamePolicyV1{
		Version:                   currentGitPolicyVersion,
		SimilarityPercent:         60,
		MaxDeleteSources:          1000,
		MaxAddTargets:             1000,
		DetectChangedSourceCopies: true,
	}
}

// Validate rejects policy values that would change rename meaning or remove
// the bounded candidate search.
func (p RenamePolicyV1) Validate() error {
	want := DefaultRenamePolicyV1()
	if p != want {
		return ErrInvalidGitPolicy
	}
	return nil
}

// DiffArgs returns the exact Git flags for v1 rename and copy detection.
func (p RenamePolicyV1) DiffArgs() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []string{"--find-renames=60%", "--find-copies=60%"}, nil
}

// RenameDetectionOutcome records whether the bounded search completed.
type RenameDetectionOutcome string

const (
	RenameDetectionComplete RenameDetectionOutcome = "complete"
	RenameDetectionLimited  RenameDetectionOutcome = "rename_detection_limited"
	RenameDetectionSkipped  RenameDetectionOutcome = "skipped"
)

// RenamePolicyEvidenceV1 persists the policy, flags, candidate bounds, and
// outcome that produced one change set.
type RenamePolicyEvidenceV1 struct {
	Policy           RenamePolicyV1
	Outcome          RenameDetectionOutcome
	DeleteCandidates int
	AddCandidates    int
	Flags            []string
	EvidenceHash     string
}

// NewRenamePolicyEvidence creates bounded, reproducible rename evidence.
func NewRenamePolicyEvidence(policy RenamePolicyV1, outcome RenameDetectionOutcome, deleteCandidates, addCandidates int) (RenamePolicyEvidenceV1, error) {
	flags, err := policy.DiffArgs()
	if err != nil || deleteCandidates < 0 || addCandidates < 0 || deleteCandidates > policy.MaxDeleteSources || addCandidates > policy.MaxAddTargets {
		return RenamePolicyEvidenceV1{}, ErrInvalidPolicyEvidence
	}
	if outcome != RenameDetectionComplete && outcome != RenameDetectionLimited && outcome != RenameDetectionSkipped {
		return RenamePolicyEvidenceV1{}, ErrInvalidPolicyEvidence
	}
	if outcome == RenameDetectionLimited && deleteCandidates < policy.MaxDeleteSources && addCandidates < policy.MaxAddTargets {
		return RenamePolicyEvidenceV1{}, ErrInvalidPolicyEvidence
	}
	result := RenamePolicyEvidenceV1{
		Policy:           policy,
		Outcome:          outcome,
		DeleteCandidates: deleteCandidates,
		AddCandidates:    addCandidates,
		Flags:            append([]string(nil), flags...),
	}
	result.EvidenceHash = renamePolicyEvidenceHash(result)
	return result, nil
}

// Validate checks that persisted flags and outcome still match v1 policy.
func (e RenamePolicyEvidenceV1) Validate() error {
	expected, err := NewRenamePolicyEvidence(e.Policy, e.Outcome, e.DeleteCandidates, e.AddCandidates)
	if err != nil || !equalStrings(e.Flags, expected.Flags) || e.EvidenceHash != expected.EvidenceHash {
		return ErrInvalidPolicyEvidence
	}
	return nil
}

// AnchorMappingAllowed reports whether the outcome can supply a rename map.
func (e RenamePolicyEvidenceV1) AnchorMappingAllowed() bool {
	return e.Outcome == RenameDetectionComplete && e.Validate() == nil
}

func renamePolicyEvidenceHash(e RenamePolicyEvidenceV1) string {
	return repository.RenamePolicyEvidenceHash(e.Policy.Version, e.Policy.SimilarityPercent, e.Policy.MaxDeleteSources, e.Policy.MaxAddTargets, e.Policy.DetectChangedSourceCopies, e.Policy.FindCopiesHarder, string(e.Outcome), e.DeleteCandidates, e.AddCandidates, e.Flags)
}

// PatchFormatV1 fixes the byte-affecting Git diff format used for persisted
// proposal artifacts.
type PatchFormatV1 struct {
	Version           uint32
	QuotePath         bool
	DiffAlgorithm     string
	IndentHeuristic   bool
	OrderFile         string
	UnifiedContext    uint32
	SourcePrefix      string
	DestinationPrefix string
}

// DefaultPatchFormatV1 returns the controlled binary-capable patch format.
func DefaultPatchFormatV1() PatchFormatV1 {
	return PatchFormatV1{
		Version:           currentGitPolicyVersion,
		QuotePath:         true,
		DiffAlgorithm:     "myers",
		UnifiedContext:    3,
		SourcePrefix:      "a/",
		DestinationPrefix: "b/",
	}
}

// Validate rejects changes to patch bytes, quoting, ordering, or hunk
// context that would make persisted proposal artifacts non-reproducible.
func (p PatchFormatV1) Validate() error {
	if p != DefaultPatchFormatV1() {
		return ErrInvalidGitPolicy
	}
	return nil
}

// DiffConfigArgs returns the config assignments that prevent ambient diff
// configuration from changing patch bytes.
func (p PatchFormatV1) DiffConfigArgs() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []string{
		"-c", "core.quotePath=true",
		"-c", "diff.algorithm=myers",
		"-c", "diff.indentHeuristic=false",
		"-c", "diff.orderFile=" + os.DevNull,
		"-c", "core.attributesfile=",
		"-c", "core.excludesfile=",
	}, nil
}

// DiffArgs returns the exact patch flags plus the fixed rename policy.
func (p PatchFormatV1) DiffArgs(rename RenamePolicyV1) ([]string, error) {
	config, err := p.DiffConfigArgs()
	if err != nil {
		return nil, err
	}
	renameArgs, err := rename.DiffArgs()
	if err != nil {
		return nil, err
	}
	args := append(config,
		"--patch", "--binary", "--full-index", "--unified=3", "--no-color",
		"--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/",
		"--diff-algorithm=myers", "--no-indent-heuristic",
	)
	return append(args, renameArgs...), nil
}

// EmptyTreeArgs returns the no-write Git command used to derive an empty-tree
// object identity for either supported object format.
func EmptyTreeArgs() []string {
	return []string{"hash-object", "-t", "tree", "--stdin"}
}

// AttributeName identifies one conversion-affecting Git attribute.
type AttributeName string

const (
	AttributeText                AttributeName = "text"
	AttributeCRLF                AttributeName = "crlf"
	AttributeEOL                 AttributeName = "eol"
	AttributeWorkingTreeEncoding AttributeName = "working-tree-encoding"
	AttributeIdent               AttributeName = "ident"
	AttributeFilter              AttributeName = "filter"
)

// AttributeState is the parsed state of one Git attribute.
type AttributeState string

const (
	AttributeUnspecified AttributeState = "unspecified"
	AttributeUnset       AttributeState = "unset"
	AttributeSet         AttributeState = "set"
	AttributeValue       AttributeState = "value"
	AttributeUnknown     AttributeState = "unknown"
)

// AttributeSource identifies the attribute source whose bytes were resolved.
type AttributeSource string

const (
	AttributeSourceRepository AttributeSource = "repository_gitattributes"
	AttributeSourceWorktree   AttributeSource = "worktree_gitattributes"
	AttributeSourcePrivate    AttributeSource = "private_info_attributes"
	AttributeSourceConfig     AttributeSource = "controlled_config"
)

// AttributeObservation preserves one bounded attribute resolution result.
type AttributeObservation struct {
	Path   repository.RepoPath
	Name   AttributeName
	State  AttributeState
	Value  string
	Source AttributeSource
}

// Validate checks an observation without treating an unknown conversion rule
// as safe. Unknown names and states remain valid evidence that evaluates to
// review-only.
func (o AttributeObservation) Validate() error {
	if o.Name == "" || !validPolicyText(string(o.Name)) || !validAttributeSource(o.Source) {
		return ErrInvalidPolicyEvidence
	}
	if len(o.Path) > 0 && o.Path.Validate() != nil {
		return ErrInvalidPolicyEvidence
	}
	switch o.State {
	case AttributeUnspecified, AttributeUnset:
		if o.Value != "" {
			return ErrInvalidPolicyEvidence
		}
	case AttributeSet, AttributeUnknown:
		if o.Value != "" && !validPolicyText(o.Value) {
			return ErrInvalidPolicyEvidence
		}
	case AttributeValue:
		if o.Value == "" || !validPolicyText(o.Value) {
			return ErrInvalidPolicyEvidence
		}
	default:
		return ErrInvalidPolicyEvidence
	}
	return nil
}

// ConversionDecision states whether proposal mutation may rely on raw bytes.
type ConversionDecision string

const (
	ConversionByteNeutral ConversionDecision = "byte_neutral"
	ConversionReviewOnly  ConversionDecision = "review_only"
)

// ConversionReason identifies why a conversion decision is review-only.
type ConversionReason string

const (
	ConversionReasonNone                 ConversionReason = ""
	ConversionReasonAttributeUnsupported ConversionReason = "attribute_conversion_unsupported"
)

// ContentConversionPolicyV1 fixes the conversion configuration and the
// attribute states that may be treated as byte-neutral.
type ContentConversionPolicyV1 struct {
	Version      uint32
	CoreAutocrlf string
	CoreEOL      string
	CoreSafeCRLF string
}

// DefaultContentConversionPolicyV1 returns the explicit no-normalization
// conversion policy used for deterministic reads and application.
func DefaultContentConversionPolicyV1() ContentConversionPolicyV1 {
	return ContentConversionPolicyV1{
		Version:      currentGitPolicyVersion,
		CoreAutocrlf: "false",
		CoreEOL:      "lf",
		CoreSafeCRLF: "true",
	}
}

// Validate checks the exact v1 conversion settings.
func (p ContentConversionPolicyV1) Validate() error {
	if p != DefaultContentConversionPolicyV1() {
		return ErrInvalidGitPolicy
	}
	return nil
}

// ConfigArgs returns the fixed conversion settings used alongside controlled
// diff/status reads. Repository attributes are still recorded separately and
// never treated as proof of byte neutrality.
func (p ContentConversionPolicyV1) ConfigArgs() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []string{
		"-c", "core.autocrlf=" + p.CoreAutocrlf,
		"-c", "core.eol=" + p.CoreEOL,
		"-c", "core.safecrlf=" + p.CoreSafeCRLF,
	}, nil
}

// ContentConversionEvidenceV1 persists the policy, sorted observations,
// attribute-change signal, decision, and stable fingerprint.
type ContentConversionEvidenceV1 struct {
	PolicyVersion     uint32
	CoreAutocrlf      string
	CoreEOL           string
	CoreSafeCRLF      string
	Observations      []AttributeObservation
	AttributesChanged bool
	Decision          ConversionDecision
	Reason            ConversionReason
	Fingerprint       string
}

// Evaluate classifies conversion evidence. Only known attributes in the
// explicitly unset or unspecified state are byte-neutral in v1.
func (p ContentConversionPolicyV1) Evaluate(observations []AttributeObservation, attributesChanged bool) (ContentConversionEvidenceV1, error) {
	if err := p.Validate(); err != nil {
		return ContentConversionEvidenceV1{}, err
	}
	copyObservations := append([]AttributeObservation(nil), observations...)
	for i := range copyObservations {
		if err := copyObservations[i].Validate(); err != nil {
			return ContentConversionEvidenceV1{}, err
		}
		copyObservations[i].Path = repository.RepoPath(copyObservations[i].Path.Bytes())
	}
	sort.SliceStable(copyObservations, func(i, j int) bool {
		left, right := copyObservations[i], copyObservations[j]
		if string(left.Path) != string(right.Path) {
			return string(left.Path) < string(right.Path)
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		return left.Name < right.Name
	})
	decision := ConversionByteNeutral
	reason := ConversionReasonNone
	if attributesChanged {
		decision = ConversionReviewOnly
		reason = ConversionReasonAttributeUnsupported
	}
	for _, observation := range copyObservations {
		if !knownAttribute(observation.Name) || (observation.State != AttributeUnspecified && observation.State != AttributeUnset) {
			decision = ConversionReviewOnly
			reason = ConversionReasonAttributeUnsupported
			break
		}
	}
	evidence := ContentConversionEvidenceV1{
		PolicyVersion:     p.Version,
		CoreAutocrlf:      p.CoreAutocrlf,
		CoreEOL:           p.CoreEOL,
		CoreSafeCRLF:      p.CoreSafeCRLF,
		Observations:      copyObservations,
		AttributesChanged: attributesChanged,
		Decision:          decision,
		Reason:            reason,
	}
	evidence.Fingerprint = conversionFingerprint(evidence)
	return evidence, nil
}

// Validate checks that the evidence has an internally consistent decision
// and fingerprint.
func (e ContentConversionEvidenceV1) Validate() error {
	policy := ContentConversionPolicyV1{Version: e.PolicyVersion, CoreAutocrlf: e.CoreAutocrlf, CoreEOL: e.CoreEOL, CoreSafeCRLF: e.CoreSafeCRLF}
	if policy.Validate() != nil || e.Decision != ConversionByteNeutral && e.Decision != ConversionReviewOnly {
		return ErrInvalidPolicyEvidence
	}
	expected, err := policy.Evaluate(e.Observations, e.AttributesChanged)
	if err != nil || !equalObservations(e.Observations, expected.Observations) || e.Decision != expected.Decision || e.Reason != expected.Reason || e.Fingerprint != expected.Fingerprint {
		return ErrInvalidPolicyEvidence
	}
	return nil
}

// ApplyPhase identifies the non-mutating check or exact mutation invocation.
type ApplyPhase string

const (
	ApplyCheckPhase    ApplyPhase = "check"
	ApplyMutationPhase ApplyPhase = "mutation"
)

// ApplyPolicyV1 fixes the arguments shared by apply check and mutation.
type ApplyPolicyV1 struct {
	Version          uint32
	Whitespace       string
	IgnoreWhitespace bool
	CoreAutocrlf     string
	CoreEOL          string
	CoreSafeCRLF     string
}

// DefaultApplyPolicyV1 returns the exact no-stage, no-three-way apply policy.
func DefaultApplyPolicyV1() ApplyPolicyV1 {
	return ApplyPolicyV1{
		Version:      currentGitPolicyVersion,
		Whitespace:   "nowarn",
		CoreAutocrlf: "false",
		CoreEOL:      "lf",
		CoreSafeCRLF: "true",
	}
}

// Validate checks the fixed v1 apply configuration.
func (p ApplyPolicyV1) Validate() error {
	if p != DefaultApplyPolicyV1() {
		return ErrInvalidGitPolicy
	}
	return nil
}

// Args returns the exact apply argv for one phase. The mutation phase never
// receives --check, so check and mutation cannot silently diverge.
func (p ApplyPolicyV1) Args(phase ApplyPhase) ([]string, error) {
	if err := p.Validate(); err != nil || (phase != ApplyCheckPhase && phase != ApplyMutationPhase) {
		return nil, ErrInvalidGitPolicy
	}
	args := []string{
		"-c", "apply.whitespace=nowarn",
		"-c", "apply.ignoreWhitespace=false",
		"-c", "core.autocrlf=false",
		"-c", "core.eol=lf",
		"-c", "core.safecrlf=true",
		"apply", "--binary", "--whitespace=nowarn",
	}
	if phase == ApplyCheckPhase {
		args = append(args, "--check")
	}
	if err := ValidateApplyArgs(args); err != nil {
		return nil, err
	}
	return args, nil
}

// ValidateApplyArgs rejects apply options that could widen paths, alter
// whitespace, touch the index, or silently merge a stale patch.
func ValidateApplyArgs(args []string) error {
	for _, arg := range args {
		if arg == "--3way" || arg == "--recount" || arg == "--inaccurate-eof" || arg == "--index" || arg == "--cached" || arg == "--unsafe-paths" || arg == "--ignore-whitespace" || arg == "--ignore-space-change" || arg == "--ignore-space-at-eol" || arg == "--unidiff-zero" || arg == "--whitespace=fix" || arg == "--whitespace=error" || arg == "--whitespace=warn" || arg == "--whitespace=ignore" || arg == "--directory" || arg == "--include" || arg == "--exclude" || arg == "-p" || strings.HasPrefix(arg, "--directory=") || strings.HasPrefix(arg, "--include=") || strings.HasPrefix(arg, "--exclude=") {
			return ErrInvalidGitPolicy
		}
	}
	return nil
}

// PolicyEvidenceKey identifies one installed-Git conformance row.
type PolicyEvidenceKey struct {
	GitVersion   string
	ObjectFormat string
	Platform     string
	FixtureSet   string
}

// Validate checks that the key is specific enough to select evidence.
func (k PolicyEvidenceKey) Validate() error {
	if !validPolicyText(k.GitVersion) || !validPolicyText(k.ObjectFormat) || !validPolicyText(k.Platform) || !validPolicyText(k.FixtureSet) {
		return ErrInvalidPolicyEvidence
	}
	return nil
}

// ConformanceEvidenceV1 binds all deterministic policy versions to one
// installed-Git/platform/fixture evidence row.
type ConformanceEvidenceV1 struct {
	Version           uint32
	Key               PolicyEvidenceKey
	MachineRead       GitPolicySelection
	PatchDerivation   GitPolicySelection
	ContentConversion GitPolicySelection
	ApplyCheck        GitPolicySelection
	ApplyMutation     GitPolicySelection
	RenamePolicy      RenamePolicyV1
	PatchFormat       PatchFormatV1
	ConversionPolicy  ContentConversionPolicyV1
	ApplyPolicy       ApplyPolicyV1
}

// Validate proves that all policy classes are present and version aligned.
func (e ConformanceEvidenceV1) Validate() error {
	if e.Version != currentGitPolicyVersion || e.Key.Validate() != nil || e.MachineRead != Selection(GitPolicyMachineRead) || e.PatchDerivation != Selection(GitPolicyPatchDerivation) || e.ContentConversion != Selection(GitPolicyContentConversion) || e.ApplyCheck != Selection(GitPolicyApplyCheck) || e.ApplyMutation != Selection(GitPolicyApplyMutation) || e.RenamePolicy.Validate() != nil || e.PatchFormat.Validate() != nil || e.ConversionPolicy.Validate() != nil || e.ApplyPolicy.Validate() != nil {
		return ErrInvalidPolicyEvidence
	}
	return nil
}

// Digest returns a stable identity for the complete policy row.
func (e ConformanceEvidenceV1) Digest() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	return hashFields(
		[]byte(fmt.Sprint(e.Version)), []byte(e.Key.GitVersion), []byte(e.Key.ObjectFormat), []byte(e.Key.Platform), []byte(e.Key.FixtureSet),
		[]byte(fmt.Sprint(e.RenamePolicy)), []byte(fmt.Sprint(e.PatchFormat)), []byte(fmt.Sprint(e.ConversionPolicy)), []byte(fmt.Sprint(e.ApplyPolicy)),
	), nil
}

// ConformanceEvidenceRegistry is an immutable lookup of registered support
// evidence. Construct a new value to add rows; callers may safely share it.
type ConformanceEvidenceRegistry struct {
	entries map[PolicyEvidenceKey]ConformanceEvidenceV1
}

// NewConformanceEvidenceRegistry validates and registers evidence rows.
func NewConformanceEvidenceRegistry(rows []ConformanceEvidenceV1) (ConformanceEvidenceRegistry, error) {
	entries := make(map[PolicyEvidenceKey]ConformanceEvidenceV1, len(rows))
	for _, row := range rows {
		if err := row.Validate(); err != nil {
			return ConformanceEvidenceRegistry{}, err
		}
		if _, exists := entries[row.Key]; exists {
			return ConformanceEvidenceRegistry{}, ErrInvalidPolicyEvidence
		}
		entries[row.Key] = cloneEvidence(row)
	}
	return ConformanceEvidenceRegistry{entries: entries}, nil
}

// Lookup returns the registered evidence for an exact environment key.
func (r ConformanceEvidenceRegistry) Lookup(key PolicyEvidenceKey) (ConformanceEvidenceV1, error) {
	if err := key.Validate(); err != nil {
		return ConformanceEvidenceV1{}, err
	}
	value, ok := r.entries[key]
	if !ok {
		return ConformanceEvidenceV1{}, ErrUnknownPolicyEvidence
	}
	return cloneEvidence(value), nil
}

func knownAttribute(name AttributeName) bool {
	switch name {
	case AttributeText, AttributeCRLF, AttributeEOL, AttributeWorkingTreeEncoding, AttributeIdent, AttributeFilter:
		return true
	default:
		return false
	}
}

func validAttributeSource(source AttributeSource) bool {
	switch source {
	case AttributeSourceRepository, AttributeSourceWorktree, AttributeSourcePrivate, AttributeSourceConfig:
		return true
	default:
		return false
	}
}

func validPolicyText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

func conversionFingerprint(evidence ContentConversionEvidenceV1) string {
	fields := [][]byte{
		[]byte(fmt.Sprint(evidence.PolicyVersion)), []byte(evidence.CoreAutocrlf), []byte(evidence.CoreEOL), []byte(evidence.CoreSafeCRLF),
		[]byte(fmt.Sprint(evidence.AttributesChanged)), []byte(evidence.Decision), []byte(evidence.Reason),
	}
	for _, observation := range evidence.Observations {
		fields = append(fields, observation.Path.Bytes(), []byte(observation.Name), []byte(observation.State), []byte(observation.Value), []byte(observation.Source))
	}
	return hashFields(fields...)
}

func hashFields(fields ...[]byte) string {
	hash := sha256.New()
	for _, field := range fields {
		var length [8]byte
		binary.LittleEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalObservations(left, right []AttributeObservation) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if string(left[i].Path) != string(right[i].Path) || left[i].Name != right[i].Name || left[i].State != right[i].State || left[i].Value != right[i].Value || left[i].Source != right[i].Source {
			return false
		}
	}
	return true
}

func cloneEvidence(value ConformanceEvidenceV1) ConformanceEvidenceV1 {
	return value
}
