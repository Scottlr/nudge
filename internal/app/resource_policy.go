package app

import (
	"errors"
	"math"
	"time"
)

var (
	// ErrInvalidResourcePolicy reports an impossible policy relationship.
	ErrInvalidResourcePolicy = errors.New("invalid resource policy")
	// ErrLimitArithmeticOverflow reports checked size/count arithmetic overflow.
	ErrLimitArithmeticOverflow = errors.New("resource limit arithmetic overflow")
	// ErrLimitExceeded reports an observation beyond an admitted limit.
	ErrLimitExceeded = errors.New("resource limit exceeded")
)

// ResourcePolicyVersion identifies the limit semantics used by an artifact or
// event. A change to a hard maximum requires a new policy version.
type ResourcePolicyVersion uint32

const CurrentResourcePolicyVersion ResourcePolicyVersion = 1

// ByteSize is a non-negative byte quantity with checked arithmetic.
type ByteSize uint64

const (
	KiB ByteSize = 1024
	MiB ByteSize = 1024 * KiB
	GiB ByteSize = 1024 * MiB
)

// Add returns the sum or fails before overflow.
func (b ByteSize) Add(other ByteSize) (ByteSize, error) {
	if uint64(other) > math.MaxUint64-uint64(b) {
		return 0, ErrLimitArithmeticOverflow
	}
	return b + other, nil
}

// Mul returns the product or fails before overflow.
func (b ByteSize) Mul(count Count) (ByteSize, error) {
	if count != 0 && uint64(b) > math.MaxUint64/uint64(count) {
		return 0, ErrLimitArithmeticOverflow
	}
	return b * ByteSize(count), nil
}

// Count is a non-negative bounded item quantity with checked arithmetic.
type Count uint64

// Add returns the sum or fails before overflow.
func (c Count) Add(other Count) (Count, error) {
	if uint64(other) > math.MaxUint64-uint64(c) {
		return 0, ErrLimitArithmeticOverflow
	}
	return c + other, nil
}

// PageSize is a UI page item count.
type PageSize Count

// PageLimits separates a usable default from an immutable hard ceiling.
type PageLimits struct {
	Default PageSize
	Hard    PageSize
}

func (p PageLimits) validate() error {
	if p.Default == 0 || p.Hard == 0 || p.Default > p.Hard {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// TreeSearchLimits bounds repository-wide path search before work begins.
// The entry ceiling limits one immutable enumeration; it is not a retained
// index and does not permit callers to allocate the whole tree.
type TreeSearchLimits struct {
	QueryBytes   ByteSize
	Page         PageLimits
	MatchRanges  Count
	CursorBytes  ByteSize
	EntryCeiling Count
	BatchEntries Count
	Deadline     time.Duration
}

func (s TreeSearchLimits) validate() error {
	if s.QueryBytes == 0 || s.Page.validate() != nil || s.MatchRanges == 0 || s.CursorBytes == 0 || s.EntryCeiling == 0 || s.BatchEntries == 0 || s.BatchEntries > s.EntryCeiling || s.Page.Hard > PageSize(s.EntryCeiling) || s.Deadline <= 0 {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// DiffLimits contains the two content thresholds referenced by the design.
type DiffLimits struct {
	HighlightBytes     ByteSize
	AutomaticOpenBytes ByteSize
}

func (d DiffLimits) validate() error {
	if d.HighlightBytes == 0 || d.AutomaticOpenBytes == 0 || d.HighlightBytes > d.AutomaticOpenBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// LargeContentLimits bounds explicit immutable content opens. The hard ceiling
// is deliberately separate from Diff.AutomaticOpenBytes: confirmation may
// lower these values, but no runtime tuning may raise them.
type LargeContentLimits struct {
	ExplicitOpenBytes  ByteSize
	ReadBytes          ByteSize
	LineSegmentBytes   ByteSize
	LineSegmentCells   Count
	LineWindowLines    Count
	LineWindowBytes    ByteSize
	IndexBytes         ByteSize
	CheckpointInterval ByteSize
	Deadline           time.Duration
}

func (l LargeContentLimits) validate(automaticOpenBytes ByteSize) error {
	if l.ExplicitOpenBytes == 0 || l.ReadBytes == 0 || l.LineSegmentBytes == 0 || l.LineSegmentCells == 0 || l.LineWindowLines == 0 || l.LineWindowBytes == 0 || l.IndexBytes == 0 || l.CheckpointInterval == 0 || l.Deadline <= 0 {
		return ErrInvalidResourcePolicy
	}
	if l.ExplicitOpenBytes < automaticOpenBytes || l.ReadBytes > l.ExplicitOpenBytes || l.LineSegmentBytes > l.ReadBytes || l.LineWindowBytes > l.ExplicitOpenBytes || l.CheckpointInterval > l.ExplicitOpenBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// ProcessLimits bounds finite child-process output.
type ProcessLimits struct {
	MaxOutputBytes ByteSize
	MaxStderrBytes ByteSize
}

func (p ProcessLimits) validate() error {
	if p.MaxOutputBytes == 0 || p.MaxStderrBytes == 0 {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// MetadataCacheLimits bounds repository metadata retained in memory.
type MetadataCacheLimits struct {
	MaxEntries Count
	MaxBytes   ByteSize
}

func (m MetadataCacheLimits) validate() error {
	if m.MaxEntries == 0 || m.MaxBytes == 0 {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// DocumentLimits bounds a structured TOML or metadata document before full
// allocation.
type DocumentLimits struct {
	MaxBytes       ByteSize
	MaxDepth       Count
	MaxEntries     Count
	MaxScalarBytes ByteSize
}

func (d DocumentLimits) validate() error {
	if d.MaxBytes == 0 || d.MaxDepth == 0 || d.MaxEntries == 0 || d.MaxScalarBytes == 0 || d.MaxScalarBytes > d.MaxBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// InputLimits bounds user, path, ref, config, and theme inputs.
type InputLimits struct {
	ProposalSummaryBytes      ByteSize
	ExpectedRawPathCount      Count
	ExpectedRawPathBytes      ByteSize
	ExpectedRawPathTotalBytes ByteSize
	RepoPathBytes             ByteSize
	RevisionBytes             ByteSize
	ExecutablePathBytes       ByteSize
	GitRecordBytes            ByteSize
	Config                    DocumentLimits
	Theme                     DocumentLimits
}

func (i InputLimits) validate() error {
	if i.ProposalSummaryBytes == 0 || i.ExpectedRawPathCount == 0 || i.ExpectedRawPathBytes == 0 || i.ExpectedRawPathTotalBytes == 0 || i.RepoPathBytes == 0 || i.RevisionBytes == 0 || i.ExecutablePathBytes == 0 || i.GitRecordBytes == 0 || i.ExpectedRawPathBytes > i.ExpectedRawPathTotalBytes {
		return ErrInvalidResourcePolicy
	}
	return i.validateOr(i.Theme)
}

func (i InputLimits) validateOr(theme DocumentLimits) error {
	if err := i.Config.validate(); err != nil {
		return err
	}
	return theme.validate()
}

// ProviderScalarLimits bounds provider-controlled names, refs, URLs, command
// arguments, metadata, and schema collections before admission.
type ProviderScalarLimits struct {
	OpaqueRefBytes        ByteSize
	MethodBytes           ByteSize
	DisplayBytes          ByteSize
	HumanErrorBytes       ByteSize
	URLBytes              ByteSize
	HostBytes             ByteSize
	CommandArgCount       Count
	CommandArgBytes       ByteSize
	CommandArgsTotalBytes ByteSize
	MetadataKeyBytes      ByteSize
	MetadataScalarBytes   ByteSize
	MetadataMembers       Count
	MetadataListElements  Count
	MetadataDepth         Count
	OtherArrayElements    Count
}

func (s ProviderScalarLimits) validate() error {
	if s.OpaqueRefBytes == 0 || s.MethodBytes == 0 || s.DisplayBytes == 0 || s.HumanErrorBytes == 0 || s.URLBytes == 0 || s.HostBytes == 0 || s.CommandArgCount == 0 || s.CommandArgBytes == 0 || s.CommandArgsTotalBytes == 0 || s.CommandArgBytes > s.CommandArgsTotalBytes || s.MetadataKeyBytes == 0 || s.MetadataScalarBytes == 0 || s.MetadataMembers == 0 || s.MetadataListElements == 0 || s.MetadataDepth == 0 || s.OtherArrayElements == 0 {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// ProviderLimits bounds serialized turns, JSONL frames, queues, chunks, and
// normalized provider content. NetworkAllowed is deliberately false-only in v1.
type ProviderLimits struct {
	SerializedTurnInputBytes ByteSize
	ConcernBytes             ByteSize
	SelectedAnchorBytes      ByteSize
	HunkContextBytes         ByteSize
	SelectedTranscriptBytes  ByteSize
	MaxFrameBytes            ByteSize
	DiagnosticStderrBytes    ByteSize
	MaxGitStdoutBytes        ByteSize
	QueueEvents              Count
	QueueResidentBytes       ByteSize
	CoalescedChunkBytes      ByteSize
	NormalizedMessageBytes   ByteSize
	TurnContentBytes         ByteSize
	Scalars                  ProviderScalarLimits
	NetworkAllowed           bool
}

func (p ProviderLimits) validate() error {
	if p.SerializedTurnInputBytes == 0 || p.ConcernBytes == 0 || p.SelectedAnchorBytes == 0 || p.HunkContextBytes == 0 || p.SelectedTranscriptBytes == 0 || p.MaxFrameBytes == 0 || p.DiagnosticStderrBytes == 0 || p.MaxGitStdoutBytes == 0 || p.QueueEvents == 0 || p.QueueResidentBytes == 0 || p.CoalescedChunkBytes == 0 || p.NormalizedMessageBytes == 0 || p.TurnContentBytes == 0 || p.NetworkAllowed {
		return ErrInvalidResourcePolicy
	}
	if p.ConcernBytes > p.SerializedTurnInputBytes || p.SelectedAnchorBytes > p.SerializedTurnInputBytes || p.HunkContextBytes > p.SerializedTurnInputBytes || p.SelectedTranscriptBytes > p.SerializedTurnInputBytes || p.CoalescedChunkBytes > p.QueueResidentBytes || p.NormalizedMessageBytes > p.TurnContentBytes {
		return ErrInvalidResourcePolicy
	}
	turnInput, err := p.ConcernBytes.Add(p.SelectedAnchorBytes)
	if err != nil {
		return ErrInvalidResourcePolicy
	}
	turnInput, err = turnInput.Add(p.HunkContextBytes)
	if err != nil {
		return ErrInvalidResourcePolicy
	}
	turnInput, err = turnInput.Add(p.SelectedTranscriptBytes)
	if err != nil || turnInput > p.SerializedTurnInputBytes {
		return ErrInvalidResourcePolicy
	}
	return p.Scalars.validate()
}

// SymlinkLimits keeps review, range, and native-action admission separate.
type SymlinkLimits struct {
	TrackedBlobBytes        ByteSize
	InlinePreviewBytes      ByteSize
	ImmutableRangeBytes     ByteSize
	NativeActionBytes       ByteSize
	LexicalComponents       Count
	NormalizedRelativeDepth Count
	ReferentFollowHops      Count
	NULAllowed              bool
}

func (s SymlinkLimits) validate(fileBytes ByteSize) error {
	if s.TrackedBlobBytes == 0 || s.InlinePreviewBytes == 0 || s.ImmutableRangeBytes == 0 || s.NativeActionBytes == 0 || s.LexicalComponents == 0 || s.NormalizedRelativeDepth == 0 || s.ReferentFollowHops != 0 || s.NULAllowed || s.InlinePreviewBytes > s.NativeActionBytes || s.NativeActionBytes > s.TrackedBlobBytes || s.TrackedBlobBytes > fileBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// ArtifactLimits bounds proposals, captures, snapshots, and complete result
// artifacts. Values describe bytes, not allocations that may exceed a cap.
type ArtifactLimits struct {
	ProposalFiles       Count
	ProposalFileBytes   ByteSize
	CompletePatchBytes  ByteSize
	PublishedDeltaBytes ByteSize
	CaptureEntries      Count
	CaptureDeltaBytes   ByteSize
	SnapshotEntries     Count
	SnapshotBytes       ByteSize
	SnapshotDeadline    time.Duration
}

func (a ArtifactLimits) validate() error {
	if a.ProposalFiles == 0 || a.ProposalFileBytes == 0 || a.CompletePatchBytes == 0 || a.PublishedDeltaBytes == 0 || a.CaptureEntries == 0 || a.CaptureDeltaBytes == 0 || a.SnapshotEntries == 0 || a.SnapshotBytes == 0 || a.SnapshotDeadline <= 0 || a.ProposalFileBytes > a.CompletePatchBytes || a.CompletePatchBytes > a.PublishedDeltaBytes || a.CaptureDeltaBytes > a.SnapshotBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// StorageLimits bounds observed free space, recovery reserve, and retained
// repository/global budgets.
type StorageLimits struct {
	MinimumFreeBytes    ByteSize
	RecoveryFileBytes   ByteSize
	RepositorySoftBytes ByteSize
	RepositoryHardBytes ByteSize
	GlobalSoftBytes     ByteSize
	GlobalHardBytes     ByteSize
}

func (s StorageLimits) validate() error {
	if s.MinimumFreeBytes == 0 || s.RecoveryFileBytes == 0 || s.RepositorySoftBytes == 0 || s.RepositoryHardBytes == 0 || s.GlobalSoftBytes == 0 || s.GlobalHardBytes == 0 || s.RecoveryFileBytes > s.MinimumFreeBytes || s.RepositorySoftBytes > s.RepositoryHardBytes || s.GlobalSoftBytes > s.GlobalHardBytes {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// StoragePressure describes a retained-storage state.
type StoragePressure string

const (
	StoragePressureNone StoragePressure = "none"
	StoragePressureSoft StoragePressure = "soft"
	StoragePressureHard StoragePressure = "hard"
)

// ClassifyStoragePressure maps used bytes to the configured repository/global
// budget without deleting accepted history.
func ClassifyStoragePressure(used, soft, hard ByteSize) (StoragePressure, error) {
	if soft == 0 || hard == 0 || soft > hard {
		return "", ErrInvalidResourcePolicy
	}
	switch {
	case used >= hard:
		return StoragePressureHard, nil
	case used >= soft:
		return StoragePressureSoft, nil
	default:
		return StoragePressureNone, nil
	}
}

// BatchLimits bounds one staged reconciliation or validity batch, not total history.
type BatchLimits struct {
	AnchorCount           Count
	AnchorPaths           Count
	AnchorSourceBytes     ByteSize
	AnchorEvidenceBytes   ByteSize
	ProposalSummaries     Count
	ProposalPaths         Count
	ProposalEvidenceBytes ByteSize
}

func (b BatchLimits) validate() error {
	if b.AnchorCount == 0 || b.AnchorPaths == 0 || b.AnchorSourceBytes == 0 || b.AnchorEvidenceBytes == 0 || b.ProposalSummaries == 0 || b.ProposalPaths == 0 || b.ProposalEvidenceBytes == 0 {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// ResourcePolicy is the immutable v1 policy injected into resource-consuming
// adapters and recorded with admitted evidence.
type ResourcePolicy struct {
	Version                 ResourcePolicyVersion
	TreePage                PageLimits
	TreeSearch              TreeSearchLimits
	HistoryPage             PageLimits
	HistoryPageEncodedBytes ByteSize
	MetadataCache           MetadataCacheLimits
	Diff                    DiffLimits
	LargeContent            LargeContentLimits
	Process                 ProcessLimits
	Input                   InputLimits
	Provider                ProviderLimits
	Symlink                 SymlinkLimits
	Artifact                ArtifactLimits
	Storage                 StorageLimits
	Batch                   BatchLimits
}

// Limits is the concise compatibility name for ResourcePolicy consumers.
type Limits = ResourcePolicy

// DefaultResourcePolicy returns the concrete version-1 policy from T070.
func DefaultResourcePolicy() ResourcePolicy {
	return ResourcePolicy{
		Version:                 CurrentResourcePolicyVersion,
		TreePage:                PageLimits{Default: 200, Hard: 1000},
		TreeSearch:              TreeSearchLimits{QueryBytes: 4 * KiB, Page: PageLimits{Default: 50, Hard: 200}, MatchRanges: 1024, CursorBytes: 4 * KiB, EntryCeiling: 100_000, BatchEntries: 256, Deadline: 5 * time.Second},
		HistoryPage:             PageLimits{Default: 100, Hard: 200},
		HistoryPageEncodedBytes: 4 * MiB,
		MetadataCache:           MetadataCacheLimits{MaxEntries: 50_000, MaxBytes: 64 * MiB},
		Diff:                    DiffLimits{HighlightBytes: 1_000_000, AutomaticOpenBytes: 2_000_000},
		LargeContent:            LargeContentLimits{ExplicitOpenBytes: 256 * MiB, ReadBytes: 256 * KiB, LineSegmentBytes: 64 * KiB, LineSegmentCells: 8_192, LineWindowLines: 4_096, LineWindowBytes: 4 * MiB, IndexBytes: 8 * MiB, CheckpointInterval: 1 * MiB, Deadline: 30 * time.Second},
		Process:                 ProcessLimits{MaxOutputBytes: 256 * MiB, MaxStderrBytes: 1 * MiB},
		Input: InputLimits{
			ProposalSummaryBytes:      64 * KiB,
			ExpectedRawPathCount:      5_000,
			ExpectedRawPathBytes:      16 * KiB,
			ExpectedRawPathTotalBytes: 512 * KiB,
			RepoPathBytes:             16 * KiB,
			RevisionBytes:             4 * KiB,
			ExecutablePathBytes:       32 * KiB,
			GitRecordBytes:            1 * MiB,
			Config:                    DocumentLimits{MaxBytes: 1 * MiB, MaxDepth: 16, MaxEntries: 4_096, MaxScalarBytes: 64 * KiB},
			Theme:                     DocumentLimits{MaxBytes: 256 * KiB, MaxDepth: 8, MaxEntries: 512, MaxScalarBytes: 8 * KiB},
		},
		Provider: ProviderLimits{
			SerializedTurnInputBytes: 2 * MiB,
			ConcernBytes:             64 * KiB,
			SelectedAnchorBytes:      256 * KiB,
			HunkContextBytes:         512 * KiB,
			SelectedTranscriptBytes:  1 * MiB,
			MaxFrameBytes:            16 * MiB,
			DiagnosticStderrBytes:    1 * MiB,
			MaxGitStdoutBytes:        256 * MiB,
			QueueEvents:              512,
			QueueResidentBytes:       32 * MiB,
			CoalescedChunkBytes:      256 * KiB,
			NormalizedMessageBytes:   8 * MiB,
			TurnContentBytes:         32 * MiB,
			Scalars: ProviderScalarLimits{
				OpaqueRefBytes:        4 * KiB,
				MethodBytes:           256,
				DisplayBytes:          1 * KiB,
				HumanErrorBytes:       64 * KiB,
				URLBytes:              8 * KiB,
				HostBytes:             1 * KiB,
				CommandArgCount:       256,
				CommandArgBytes:       32 * KiB,
				CommandArgsTotalBytes: 256 * KiB,
				MetadataKeyBytes:      256,
				MetadataScalarBytes:   16 * KiB,
				MetadataMembers:       256,
				MetadataListElements:  1_024,
				MetadataDepth:         32,
				OtherArrayElements:    4_096,
			},
		},
		Symlink: SymlinkLimits{
			TrackedBlobBytes:        32 * MiB,
			InlinePreviewBytes:      4 * KiB,
			ImmutableRangeBytes:     64 * KiB,
			NativeActionBytes:       32 * KiB,
			LexicalComponents:       256,
			NormalizedRelativeDepth: 128,
			ReferentFollowHops:      0,
		},
		Artifact: ArtifactLimits{
			ProposalFiles:       5_000,
			ProposalFileBytes:   32 * MiB,
			CompletePatchBytes:  256 * MiB,
			PublishedDeltaBytes: 1 * GiB,
			CaptureEntries:      100_000,
			CaptureDeltaBytes:   2 * GiB,
			SnapshotEntries:     250_000,
			SnapshotBytes:       8 * GiB,
			SnapshotDeadline:    5 * time.Minute,
		},
		Storage: StorageLimits{
			MinimumFreeBytes:    2 * GiB,
			RecoveryFileBytes:   256 * MiB,
			RepositorySoftBytes: 32 * GiB,
			RepositoryHardBytes: 64 * GiB,
			GlobalSoftBytes:     128 * GiB,
			GlobalHardBytes:     256 * GiB,
		},
		Batch: BatchLimits{
			AnchorCount:           100,
			AnchorPaths:           32,
			AnchorSourceBytes:     64 * MiB,
			AnchorEvidenceBytes:   4 * MiB,
			ProposalSummaries:     100,
			ProposalPaths:         1_000,
			ProposalEvidenceBytes: 4 * MiB,
		},
	}
}

// Validate checks every policy boundary and cross-subsystem relationship.
func (p ResourcePolicy) Validate() error {
	if p.Version != CurrentResourcePolicyVersion || p.TreePage.validate() != nil || p.TreeSearch.validate() != nil || p.HistoryPage.validate() != nil || p.HistoryPageEncodedBytes == 0 || p.MetadataCache.validate() != nil || p.Diff.validate() != nil || p.LargeContent.validate(p.Diff.AutomaticOpenBytes) != nil || p.Process.validate() != nil || p.Input.validate() != nil || p.Provider.validate() != nil || p.Artifact.validate() != nil || p.Storage.validate() != nil || p.Batch.validate() != nil {
		return ErrInvalidResourcePolicy
	}
	if p.Symlink.validate(p.Artifact.ProposalFileBytes) != nil {
		return ErrInvalidResourcePolicy
	}
	return nil
}

// PolicyTuning contains lower-only work limits and higher-only storage
// reserves. Nil values preserve the policy default.
type PolicyTuning struct {
	TreePageDefault            *PageSize
	HistoryPageDefault         *PageSize
	HighlightBytes             *ByteSize
	AutomaticOpenBytes         *ByteSize
	ProviderQueueEvents        *Count
	ProviderQueueResidentBytes *ByteSize
	MinimumFreeBytes           *ByteSize
	RecoveryFileBytes          *ByteSize
}

// WithTuning applies the safe runtime tuning rules without changing the
// policy version or any hard maximum.
func (p ResourcePolicy) WithTuning(tuning PolicyTuning) (ResourcePolicy, error) {
	if err := p.Validate(); err != nil {
		return ResourcePolicy{}, err
	}
	result := p
	if tuning.TreePageDefault != nil {
		if *tuning.TreePageDefault == 0 || *tuning.TreePageDefault > result.TreePage.Default {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.TreePage.Default = *tuning.TreePageDefault
	}
	if tuning.HistoryPageDefault != nil {
		if *tuning.HistoryPageDefault == 0 || *tuning.HistoryPageDefault > result.HistoryPage.Default {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.HistoryPage.Default = *tuning.HistoryPageDefault
	}
	if tuning.HighlightBytes != nil {
		if *tuning.HighlightBytes == 0 || *tuning.HighlightBytes > result.Diff.HighlightBytes {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Diff.HighlightBytes = *tuning.HighlightBytes
	}
	if tuning.AutomaticOpenBytes != nil {
		if *tuning.AutomaticOpenBytes == 0 || *tuning.AutomaticOpenBytes > result.Diff.AutomaticOpenBytes {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Diff.AutomaticOpenBytes = *tuning.AutomaticOpenBytes
	}
	if tuning.ProviderQueueEvents != nil {
		if *tuning.ProviderQueueEvents == 0 || *tuning.ProviderQueueEvents > result.Provider.QueueEvents {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Provider.QueueEvents = *tuning.ProviderQueueEvents
	}
	if tuning.ProviderQueueResidentBytes != nil {
		if *tuning.ProviderQueueResidentBytes == 0 || *tuning.ProviderQueueResidentBytes > result.Provider.QueueResidentBytes {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Provider.QueueResidentBytes = *tuning.ProviderQueueResidentBytes
	}
	if tuning.MinimumFreeBytes != nil {
		if *tuning.MinimumFreeBytes < result.Storage.MinimumFreeBytes {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Storage.MinimumFreeBytes = *tuning.MinimumFreeBytes
	}
	if tuning.RecoveryFileBytes != nil {
		if *tuning.RecoveryFileBytes < result.Storage.RecoveryFileBytes {
			return ResourcePolicy{}, ErrInvalidResourcePolicy
		}
		result.Storage.RecoveryFileBytes = *tuning.RecoveryFileBytes
	}
	if err := result.Validate(); err != nil {
		return ResourcePolicy{}, err
	}
	return result, nil
}

// LimitID identifies a policy cell in safe evidence.
type LimitID string

// LimitOutcome describes the exact behavior after a limit is reached.
type LimitOutcome string

const (
	LimitAccepted      LimitOutcome = "accepted"
	LimitNonReady      LimitOutcome = "non_ready"
	LimitReviewOnly    LimitOutcome = "review_only"
	LimitPressure      LimitOutcome = "pressure"
	LimitPartialFailed LimitOutcome = "partial_failed"
)

// LimitEvidence is payload-free observed/configured evidence attached to a
// bounded result or artifact.
type LimitEvidence struct {
	PolicyVersion    ResourcePolicyVersion
	Limit            LimitID
	Configured       uint64
	Observed         uint64
	Outcome          LimitOutcome
	Complete         bool
	PriorStateUsable bool
}

// Binding returns the policy version that consumers must persist with evidence.
func (p ResourcePolicy) Binding() ResourcePolicyVersion {
	return p.Version
}

// Admit returns accepted evidence or a precise non-ready limit hit.
func (p ResourcePolicy) Admit(id LimitID, observed, configured uint64, outcome LimitOutcome, priorStateUsable bool) (LimitEvidence, error) {
	if err := p.Validate(); err != nil || id == "" || configured == 0 || !validLimitOutcome(outcome) {
		return LimitEvidence{}, ErrInvalidResourcePolicy
	}
	if observed <= configured {
		return LimitEvidence{PolicyVersion: p.Version, Limit: id, Configured: configured, Observed: observed, Outcome: LimitAccepted, Complete: true, PriorStateUsable: priorStateUsable}, nil
	}
	return LimitEvidence{PolicyVersion: p.Version, Limit: id, Configured: configured, Observed: observed, Outcome: outcome, Complete: false, PriorStateUsable: priorStateUsable}, ErrLimitExceeded
}

func validLimitOutcome(outcome LimitOutcome) bool {
	switch outcome {
	case LimitNonReady, LimitReviewOnly, LimitPressure, LimitPartialFailed:
		return true
	default:
		return false
	}
}
