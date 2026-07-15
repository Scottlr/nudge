package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

const ResultSnapshotVersion uint32 = 1

var (
	ErrInvalidResultSnapshot  = errors.New("invalid proposal result snapshot")
	ErrResultSnapshotNotFound = errors.New("proposal result snapshot not found")
	ErrResultSnapshotConflict = errors.New("proposal result snapshot conflict")
	ErrResultSnapshotNotReady = errors.New("proposal result snapshot is not ready")
)

// ResultSnapshotState distinguishes a complete, proposal-capable result from
// complete evidence that must remain visible but non-ready.
type ResultSnapshotState string

const (
	ResultSnapshotReady    ResultSnapshotState = "ready"
	ResultSnapshotNonReady ResultSnapshotState = "non_ready"
)

func (s ResultSnapshotState) Validate() error {
	switch s {
	case ResultSnapshotReady, ResultSnapshotNonReady:
		return nil
	default:
		return ErrInvalidResultSnapshot
	}
}

// ResultSnapshotReason is bounded, machine-readable evidence explaining why
// an otherwise complete result cannot become proposal-capable.
type ResultSnapshotReason string

const (
	ResultReasonNone             ResultSnapshotReason = ""
	ResultReasonUnsupportedEntry ResultSnapshotReason = "unsupported_entry"
	ResultReasonNativeIdentity   ResultSnapshotReason = "native_identity_unavailable"
	ResultReasonSharedIdentity   ResultSnapshotReason = "shared_identity"
	ResultReasonPathAlias        ResultSnapshotReason = "path_alias"
	ResultReasonGitAdminPath     ResultSnapshotReason = "git_admin_path"
	ResultReasonRootChanged      ResultSnapshotReason = "root_changed"
	ResultReasonResultRace       ResultSnapshotReason = "result_race"
	ResultReasonQuiescence       ResultSnapshotReason = "quiescence_unproven"
	ResultReasonLimit            ResultSnapshotReason = "limit_exceeded"
)

func (r ResultSnapshotReason) Validate() error {
	switch r {
	case ResultReasonNone, ResultReasonUnsupportedEntry, ResultReasonNativeIdentity,
		ResultReasonSharedIdentity, ResultReasonPathAlias, ResultReasonGitAdminPath,
		ResultReasonRootChanged, ResultReasonResultRace, ResultReasonQuiescence,
		ResultReasonLimit:
		return nil
	default:
		return ErrInvalidResultSnapshot
	}
}

// ResultSnapshotEntry is one independently observed result-root entry. An
// unknown entry is retained as bounded evidence but never makes a snapshot
// ready. Complete regular entries may carry native link/identity evidence.
type ResultSnapshotEntry struct {
	Path               []byte                          `json:"path"`
	Kind               repository.FileKind             `json:"kind"`
	Mode               uint32                          `json:"mode"`
	Bytes              uint64                          `json:"bytes"`
	SHA256             string                          `json:"sha256"`
	ContentClass       repository.ContentClassV1       `json:"content_class,omitempty"`
	TextSemantics      *repository.TextByteSemantics   `json:"text_semantics,omitempty"`
	LinkTarget         []byte                          `json:"link_target,omitempty"`
	NativeIdentityHash string                          `json:"native_identity_hash,omitempty"`
	NativeAlias        *repository.NativeAliasEvidence `json:"native_alias,omitempty"`
	Complete           bool                            `json:"complete"`
	Reason             ResultSnapshotReason            `json:"reason"`
}

func (e ResultSnapshotEntry) Validate() error {
	if _, err := repository.NewRepoPath(e.Path); err != nil || e.Reason.Validate() != nil {
		return ErrInvalidResultSnapshot
	}
	switch e.Kind {
	case repository.FileKindDirectory:
		if e.Mode == 0 || e.Bytes != 0 || e.SHA256 != "" || len(e.LinkTarget) != 0 || !validResultHash(e.NativeIdentityHash) || e.NativeAlias != nil {
			return ErrInvalidResultSnapshot
		}
	case repository.FileKindRegular:
		if e.Mode == 0 || !validResultHash(e.SHA256) || len(e.LinkTarget) != 0 || e.ContentClass != "" && e.ContentClass.Validate() != nil || e.TextSemantics != nil && (e.ContentClass != repository.ContentClassRegularTextUTF8 || e.TextSemantics.Validate() != nil || e.TextSemantics.ByteLength != e.Bytes || e.TextSemantics.SHA256 != e.SHA256) || !e.Complete || !validResultHash(e.NativeIdentityHash) {
			return ErrInvalidResultSnapshot
		}
		if e.NativeAlias != nil && e.NativeAlias.Validate() != nil {
			return ErrInvalidResultSnapshot
		}
	case repository.FileKindSymlink:
		if e.Mode == 0 || len(e.LinkTarget) == 0 || e.Bytes != uint64(len(e.LinkTarget)) || !validResultHash(e.SHA256) || e.ContentClass != "" || e.TextSemantics != nil || e.NativeIdentityHash != "" || e.NativeAlias != nil || !e.Complete {
			return ErrInvalidResultSnapshot
		}
	case repository.FileKindUnknown:
		if e.Mode != 0 || e.Bytes != 0 || e.SHA256 != "" || e.ContentClass != "" || e.TextSemantics != nil || len(e.LinkTarget) != 0 || e.NativeIdentityHash != "" || e.NativeAlias != nil || e.Complete || e.Reason == ResultReasonNone {
			return ErrInvalidResultSnapshot
		}
	default:
		return ErrInvalidResultSnapshot
	}
	if !e.Complete && e.Kind != repository.FileKindUnknown && e.Reason == ResultReasonNone {
		return ErrInvalidResultSnapshot
	}
	return nil
}

// ResultManifest is the independently hashed enumeration of the complete
// result root. Complete false means traversal stopped before all evidence was
// safely framed and must never be adopted as a snapshot.
type ResultManifest struct {
	Version       uint32                `json:"version"`
	PolicyVersion ResourcePolicyVersion `json:"policy_version"`
	Entries       []ResultSnapshotEntry `json:"entries"`
	Hash          string                `json:"hash"`
	TotalBytes    uint64                `json:"total_bytes"`
	Complete      bool                  `json:"complete"`
	Reason        ResultSnapshotReason  `json:"reason"`
}

func NewResultManifest(entries []ResultSnapshotEntry, policyVersion ResourcePolicyVersion, complete bool, reason ResultSnapshotReason) (ResultManifest, error) {
	if policyVersion == 0 || reason.Validate() != nil || !complete && reason == ResultReasonNone {
		return ResultManifest{}, ErrInvalidResultSnapshot
	}
	copyEntries := cloneResultEntries(entries)
	sort.Slice(copyEntries, func(i, j int) bool { return bytes.Compare(copyEntries[i].Path, copyEntries[j].Path) < 0 })
	manifest := ResultManifest{Version: ResultSnapshotVersion, PolicyVersion: policyVersion, Entries: copyEntries, Complete: complete, Reason: reason}
	for index, entry := range copyEntries {
		if err := entry.Validate(); err != nil {
			return ResultManifest{}, err
		}
		if index > 0 && bytes.Equal(copyEntries[index-1].Path, entry.Path) {
			return ResultManifest{}, ErrInvalidResultSnapshot
		}
		if entry.Kind != repository.FileKindDirectory {
			if manifest.TotalBytes > ^uint64(0)-entry.Bytes {
				return ResultManifest{}, ErrInvalidResultSnapshot
			}
			manifest.TotalBytes += entry.Bytes
		}
		if !entry.Complete || entry.Reason != ResultReasonNone {
			manifest.Reason = firstResultReason(manifest.Reason, entry.Reason)
		}
	}
	manifest.Hash = resultManifestHash(manifest)
	return manifest, nil
}

func (m ResultManifest) Validate() error {
	if m.Version != ResultSnapshotVersion || m.PolicyVersion == 0 || m.Reason.Validate() != nil || !validResultHash(m.Hash) {
		return ErrInvalidResultSnapshot
	}
	computed, err := NewResultManifest(m.Entries, m.PolicyVersion, m.Complete, m.Reason)
	if err != nil || computed.Hash != m.Hash || computed.TotalBytes != m.TotalBytes {
		return ErrInvalidResultSnapshot
	}
	if !m.Complete && m.Reason == ResultReasonNone {
		return ErrInvalidResultSnapshot
	}
	return nil
}

func (m ResultManifest) Clone() ResultManifest {
	m.Entries = cloneResultEntries(m.Entries)
	return m
}

// ResultDeltaKind is a deterministic, path-preserving classification. Rename
// and copy candidates are additional evidence; the delete/add entries remain.
type ResultDeltaKind string

const (
	ResultDeltaAdded       ResultDeltaKind = "added"
	ResultDeltaDeleted     ResultDeltaKind = "deleted"
	ResultDeltaContent     ResultDeltaKind = "content_changed"
	ResultDeltaMode        ResultDeltaKind = "mode_changed"
	ResultDeltaType        ResultDeltaKind = "type_changed"
	ResultDeltaLink        ResultDeltaKind = "link_changed"
	ResultDeltaUnsupported ResultDeltaKind = "unsupported"
)

func (k ResultDeltaKind) Validate() error {
	switch k {
	case ResultDeltaAdded, ResultDeltaDeleted, ResultDeltaContent, ResultDeltaMode, ResultDeltaType, ResultDeltaLink, ResultDeltaUnsupported:
		return nil
	default:
		return ErrInvalidResultSnapshot
	}
}

// ResultRenameCandidate retains only deterministic source/target evidence. It
// does not assert Git rename identity or hide the underlying path changes.
type ResultRenameCandidate struct {
	Source repository.RepoPath `json:"source"`
	Target repository.RepoPath `json:"target"`
	Copy   bool                `json:"copy"`
}

func (c ResultRenameCandidate) Validate() error {
	if c.Source.Validate() != nil || c.Target.Validate() != nil || bytes.Equal(c.Source, c.Target) {
		return ErrInvalidResultSnapshot
	}
	return nil
}

// ResultDeltaEntry records every raw path present on either side.
type ResultDeltaEntry struct {
	Path       repository.RepoPath     `json:"path"`
	Baseline   *WorkspaceManifestEntry `json:"baseline,omitempty"`
	Result     *ResultSnapshotEntry    `json:"result,omitempty"`
	Kinds      []ResultDeltaKind       `json:"kinds"`
	Complete   bool                    `json:"complete"`
	Reason     ResultSnapshotReason    `json:"reason"`
	Candidates []ResultRenameCandidate `json:"candidates,omitempty"`
}

func (e ResultDeltaEntry) Validate() error {
	if e.Path.Validate() != nil || e.Reason.Validate() != nil || e.Baseline == nil && e.Result == nil || len(e.Kinds) == 0 {
		return ErrInvalidResultSnapshot
	}
	for _, kind := range e.Kinds {
		if kind.Validate() != nil {
			return ErrInvalidResultSnapshot
		}
	}
	if e.Baseline != nil && e.Baseline.Validate() != nil || e.Result != nil && e.Result.Validate() != nil {
		return ErrInvalidResultSnapshot
	}
	for _, candidate := range e.Candidates {
		if candidate.Validate() != nil {
			return ErrInvalidResultSnapshot
		}
	}
	if !e.Complete && e.Reason == ResultReasonNone {
		return ErrInvalidResultSnapshot
	}
	return nil
}

// ResultDelta compares one trusted baseline with one independently observed
// result. It is complete only when both source sets are complete and every
// path has been represented.
type ResultDelta struct {
	Version    uint32                  `json:"version"`
	Entries    []ResultDeltaEntry      `json:"entries"`
	Candidates []ResultRenameCandidate `json:"candidates,omitempty"`
	Hash       string                  `json:"hash"`
	Complete   bool                    `json:"complete"`
	Reason     ResultSnapshotReason    `json:"reason"`
}

func CompareResultManifest(baseline WorkspaceManifest, result ResultManifest) (ResultDelta, error) {
	if baseline.Validate() != nil || result.Validate() != nil {
		return ResultDelta{}, ErrInvalidResultSnapshot
	}
	base := make(map[repository.RepoPathKey]WorkspaceManifestEntry, len(baseline.Entries))
	for _, entry := range baseline.Entries {
		base[repository.RepoPath(entry.Path).Key()] = entry
	}
	observed := make(map[repository.RepoPathKey]ResultSnapshotEntry, len(result.Entries))
	for _, entry := range result.Entries {
		observed[repository.RepoPath(entry.Path).Key()] = entry
	}
	keys := make([]repository.RepoPathKey, 0, len(base)+len(observed))
	seen := make(map[repository.RepoPathKey]struct{}, len(base)+len(observed))
	for key := range base {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range observed {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i]) < string(keys[j]) })
	delta := ResultDelta{Version: ResultSnapshotVersion, Complete: result.Complete, Reason: result.Reason}
	for _, key := range keys {
		baseEntry, hasBase := base[key]
		resultEntry, hasResult := observed[key]
		entry := ResultDeltaEntry{Path: repository.RepoPath([]byte(string(key))), Complete: result.Complete}
		if hasBase {
			value := baseEntry
			value.Path = append([]byte(nil), baseEntry.Path...)
			entry.Baseline = &value
		}
		if hasResult {
			value := resultEntry
			value.Path = append([]byte(nil), resultEntry.Path...)
			value.LinkTarget = append([]byte(nil), resultEntry.LinkTarget...)
			entry.Result = &value
		}
		switch {
		case !hasBase:
			entry.Kinds = []ResultDeltaKind{ResultDeltaAdded}
		case !hasResult:
			entry.Kinds = []ResultDeltaKind{ResultDeltaDeleted}
		default:
			entry.Kinds = compareResultEntry(baseEntry, resultEntry)
		}
		if !hasResult || resultEntry.Complete {
			if resultEntry.Reason != ResultReasonNone {
				entry.Reason = resultEntry.Reason
			}
		} else {
			entry.Kinds = appendUniqueDeltaKind(entry.Kinds, ResultDeltaUnsupported)
			entry.Reason = resultEntry.Reason
		}
		if len(entry.Kinds) == 0 {
			continue
		}
		if entry.Reason != ResultReasonNone {
			entry.Complete = result.Complete
		}
		if entry.Validate() != nil {
			return ResultDelta{}, ErrInvalidResultSnapshot
		}
		delta.Entries = append(delta.Entries, entry)
	}
	delta.Candidates = resultRenameCandidates(delta.Entries)
	delta.Candidates = appendUniqueCandidates(delta.Candidates, resultCopyCandidates(base, observed, delta.Entries)...)
	sort.Slice(delta.Candidates, func(i, j int) bool {
		if string(delta.Candidates[i].Source) == string(delta.Candidates[j].Source) {
			if string(delta.Candidates[i].Target) == string(delta.Candidates[j].Target) {
				return !delta.Candidates[i].Copy && delta.Candidates[j].Copy
			}
			return string(delta.Candidates[i].Target) < string(delta.Candidates[j].Target)
		}
		return string(delta.Candidates[i].Source) < string(delta.Candidates[j].Source)
	})
	delta.Hash = resultDeltaHash(delta)
	return delta, nil
}

func (d ResultDelta) Validate() error {
	if d.Version != ResultSnapshotVersion || d.Reason.Validate() != nil || !validResultHash(d.Hash) {
		return ErrInvalidResultSnapshot
	}
	for index, entry := range d.Entries {
		if entry.Validate() != nil || index > 0 && bytes.Compare(d.Entries[index-1].Path, entry.Path) >= 0 {
			return ErrInvalidResultSnapshot
		}
	}
	for _, candidate := range d.Candidates {
		if candidate.Validate() != nil {
			return ErrInvalidResultSnapshot
		}
	}
	if resultDeltaHash(d) != d.Hash {
		return ErrInvalidResultSnapshot
	}
	return nil
}

// ResultSnapshot is the immutable T110 handoff consumed by T111/T038.
// Provider references are provenance only and do not participate in identity.
type ResultSnapshot struct {
	Version          uint32                  `json:"version"`
	ID               domain.ReviewSnapshotID `json:"id"`
	SessionID        domain.ReviewSessionID  `json:"session_id"`
	ProposalID       domain.ProposalID       `json:"proposal_id"`
	WorkspaceID      domain.WorkspaceID      `json:"workspace_id"`
	WorktreeID       domain.WorktreeID       `json:"worktree_id"`
	AttemptID        domain.OperationID      `json:"attempt_id"`
	ThreadID         domain.ReviewThreadID   `json:"thread_id"`
	ProviderTurnID   domain.ProviderTurnID   `json:"provider_turn_id"`
	ProviderTurnRef  string                  `json:"provider_turn_ref"`
	Baseline         review.SnapshotIdentity `json:"baseline"`
	Result           review.SnapshotIdentity `json:"result"`
	Manifest         ResultManifest          `json:"manifest"`
	Delta            ResultDelta             `json:"delta"`
	PolicyVersion    ResourcePolicyVersion   `json:"policy_version"`
	IsolationVersion uint32                  `json:"isolation_version"`
	LeaseNonce       string                  `json:"lease_nonce"`
	State            ResultSnapshotState     `json:"state"`
	Reason           ResultSnapshotReason    `json:"reason"`
	CreatedAt        time.Time               `json:"created_at"`
}

func (s ResultSnapshot) Validate() error {
	if s.Version != ResultSnapshotVersion || s.ID == "" || s.SessionID == "" || s.ProposalID == "" || s.WorkspaceID == "" || s.WorktreeID == "" || s.AttemptID == "" || s.ThreadID == "" || s.ProviderTurnRef == "" || s.Baseline.Validate() != nil || s.Result.Validate() != nil || s.Manifest.Validate() != nil || s.Delta.Validate() != nil || s.PolicyVersion == 0 || s.IsolationVersion == 0 || s.LeaseNonce == "" || s.State.Validate() != nil || s.Reason.Validate() != nil || s.CreatedAt.IsZero() {
		return ErrInvalidResultSnapshot
	}
	if s.Manifest.PolicyVersion != s.PolicyVersion || s.Result.ManifestHash != s.Manifest.Hash || s.Result.Ref.Kind != repository.SnapshotWorkingTree || s.Result.Ref.WorktreeID != s.WorktreeID {
		return ErrInvalidResultSnapshot
	}
	if s.Manifest.Complete != s.Delta.Complete {
		return ErrInvalidResultSnapshot
	}
	if expectedReason := firstResultReason(s.Manifest.Reason, s.Delta.Reason); expectedReason != s.Reason {
		return ErrInvalidResultSnapshot
	}
	if s.State == ResultSnapshotReady && (!s.Manifest.Complete || !s.Delta.Complete || s.Manifest.Reason != ResultReasonNone || s.Delta.Reason != ResultReasonNone) {
		return ErrInvalidResultSnapshot
	}
	if expected := resultSnapshotID(s); expected != s.ID {
		return ErrInvalidResultSnapshot
	}
	return nil
}

// ResultSnapshotStore is the restart boundary for adopted immutable result
// evidence. Implementations must not reread the mutable result root.
type ResultSnapshotStore interface {
	LoadResultSnapshot(context.Context, domain.ReviewSnapshotID) (ResultSnapshot, error)
	LoadResultSnapshotForAttempt(context.Context, domain.OperationID) (ResultSnapshot, error)
}

// ResultSnapshotStoreTx atomically adopts one complete manifest/delta pair.
type ResultSnapshotStoreTx interface {
	AdoptResultSnapshot(context.Context, ResultSnapshot) error
}

func NewResultSnapshot(value ResultSnapshot) (ResultSnapshot, error) {
	if value.Version == 0 {
		value.Version = ResultSnapshotVersion
	}
	if value.ID == "" {
		value.ID = resultSnapshotID(value)
	}
	if err := value.Validate(); err != nil {
		return ResultSnapshot{}, err
	}
	value.Manifest = value.Manifest.Clone()
	value.Delta.Entries = cloneDeltaEntries(value.Delta.Entries)
	value.Delta.Candidates = append([]ResultRenameCandidate(nil), value.Delta.Candidates...)
	return value, nil
}

func resultSnapshotID(s ResultSnapshot) domain.ReviewSnapshotID {
	h := sha256.New()
	writeResultHashString(h, string(s.SessionID))
	writeResultHashString(h, string(s.ProposalID))
	writeResultHashString(h, string(s.WorkspaceID))
	writeResultHashString(h, string(s.WorktreeID))
	writeResultHashString(h, string(s.AttemptID))
	writeResultHashString(h, s.Manifest.Hash)
	writeResultHashString(h, s.Delta.Hash)
	writeResultHashString(h, s.LeaseNonce)
	writeResultHashUint(h, uint64(s.PolicyVersion))
	writeResultHashUint(h, uint64(s.IsolationVersion))
	return domain.ReviewSnapshotID("result-" + hex.EncodeToString(h.Sum(nil)))
}

func compareResultEntry(base WorkspaceManifestEntry, result ResultSnapshotEntry) []ResultDeltaKind {
	kinds := make([]ResultDeltaKind, 0, 3)
	if base.Kind != result.Kind {
		kinds = append(kinds, ResultDeltaType)
	}
	if base.Mode != result.Mode {
		kinds = append(kinds, ResultDeltaMode)
	}
	if base.Bytes != result.Bytes || base.SHA256 != result.SHA256 || base.ContentClass != "" && result.ContentClass != "" && base.ContentClass != result.ContentClass || base.TextSemantics != nil && result.TextSemantics != nil && *base.TextSemantics != *result.TextSemantics {
		kinds = append(kinds, ResultDeltaContent)
	}
	if !bytes.Equal(base.LinkTarget, result.LinkTarget) {
		kinds = append(kinds, ResultDeltaLink)
	}
	if result.Reason != ResultReasonNone || !result.Complete {
		kinds = appendUniqueDeltaKind(kinds, ResultDeltaUnsupported)
	}
	return kinds
}

func resultRenameCandidates(entries []ResultDeltaEntry) []ResultRenameCandidate {
	deleted := make([]ResultDeltaEntry, 0)
	added := make([]ResultDeltaEntry, 0)
	retained := make([]ResultDeltaEntry, 0)
	for _, entry := range entries {
		if len(entry.Kinds) != 1 || entry.Result == nil && entry.Baseline == nil {
			continue
		}
		if entry.Baseline != nil && entry.Result == nil {
			deleted = append(deleted, entry)
		}
		if entry.Result != nil && entry.Baseline == nil {
			added = append(added, entry)
		}
		if entry.Result != nil && entry.Baseline != nil {
			retained = append(retained, entry)
		}
	}
	candidates := make([]ResultRenameCandidate, 0)
	for _, source := range deleted {
		identity := manifestEntryIdentity(*source.Baseline)
		if identity == "" {
			continue
		}
		for _, target := range added {
			if identity != resultEntryIdentity(*target.Result) {
				continue
			}
			candidates = append(candidates, ResultRenameCandidate{Source: repository.RepoPath(source.Path.Bytes()), Target: repository.RepoPath(target.Path.Bytes())})
		}
	}
	for _, target := range added {
		for _, source := range retained {
			if manifestEntryIdentity(*source.Baseline) == resultEntryIdentity(*target.Result) {
				candidates = append(candidates, ResultRenameCandidate{Source: repository.RepoPath(source.Path.Bytes()), Target: repository.RepoPath(target.Path.Bytes()), Copy: true})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if string(candidates[i].Source) == string(candidates[j].Source) {
			if string(candidates[i].Target) == string(candidates[j].Target) {
				return !candidates[i].Copy && candidates[j].Copy
			}
			return string(candidates[i].Target) < string(candidates[j].Target)
		}
		return string(candidates[i].Source) < string(candidates[j].Source)
	})
	return candidates
}

func resultCopyCandidates(base map[repository.RepoPathKey]WorkspaceManifestEntry, observed map[repository.RepoPathKey]ResultSnapshotEntry, entries []ResultDeltaEntry) []ResultRenameCandidate {
	candidates := make([]ResultRenameCandidate, 0)
	for _, entry := range entries {
		if len(entry.Kinds) != 1 || entry.Kinds[0] != ResultDeltaAdded || entry.Result == nil {
			continue
		}
		identity := resultEntryIdentity(*entry.Result)
		if identity == "" {
			continue
		}
		for sourceKey, source := range base {
			if _, stillPresent := observed[sourceKey]; !stillPresent || manifestEntryIdentity(source) != identity || string(sourceKey) == string(entry.Path) {
				continue
			}
			candidates = append(candidates, ResultRenameCandidate{Source: repository.RepoPath([]byte(string(sourceKey))), Target: repository.RepoPath(entry.Path.Bytes()), Copy: true})
		}
	}
	return candidates
}

func appendUniqueCandidates(values []ResultRenameCandidate, additions ...ResultRenameCandidate) []ResultRenameCandidate {
	for _, addition := range additions {
		found := false
		for _, current := range values {
			if current.Copy == addition.Copy && bytes.Equal(current.Source, addition.Source) && bytes.Equal(current.Target, addition.Target) {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func manifestEntryIdentity(entry WorkspaceManifestEntry) string {
	if entry.Kind == repository.FileKindDirectory {
		return ""
	}
	return string(entry.Kind) + ":" + entry.SHA256
}

func resultEntryIdentity(entry ResultSnapshotEntry) string {
	if entry.Kind == repository.FileKindDirectory || !entry.Complete {
		return ""
	}
	return string(entry.Kind) + ":" + entry.SHA256
}

func appendUniqueDeltaKind(values []ResultDeltaKind, value ResultDeltaKind) []ResultDeltaKind {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func firstResultReason(current, next ResultSnapshotReason) ResultSnapshotReason {
	if current != ResultReasonNone {
		return current
	}
	return next
}

func cloneResultEntries(entries []ResultSnapshotEntry) []ResultSnapshotEntry {
	copyEntries := make([]ResultSnapshotEntry, len(entries))
	for index, entry := range entries {
		copyEntries[index] = entry
		copyEntries[index].Path = append([]byte(nil), entry.Path...)
		copyEntries[index].LinkTarget = append([]byte(nil), entry.LinkTarget...)
		if entry.TextSemantics != nil {
			semantics := *entry.TextSemantics
			copyEntries[index].TextSemantics = &semantics
		}
		if entry.NativeAlias != nil {
			alias := *entry.NativeAlias
			copyEntries[index].NativeAlias = &alias
		}
	}
	return copyEntries
}

func cloneDeltaEntries(entries []ResultDeltaEntry) []ResultDeltaEntry {
	copyEntries := make([]ResultDeltaEntry, len(entries))
	for index, entry := range entries {
		copyEntries[index] = entry
		copyEntries[index].Path = append([]byte(nil), entry.Path...)
		if entry.Baseline != nil {
			base := *entry.Baseline
			base.Path = append([]byte(nil), base.Path...)
			if base.TextSemantics != nil {
				semantics := *base.TextSemantics
				base.TextSemantics = &semantics
			}
			copyEntries[index].Baseline = &base
		}
		if entry.Result != nil {
			result := *entry.Result
			result.Path = append([]byte(nil), result.Path...)
			result.LinkTarget = append([]byte(nil), result.LinkTarget...)
			if result.NativeAlias != nil {
				alias := *result.NativeAlias
				result.NativeAlias = &alias
			}
			copyEntries[index].Result = &result
			if result.TextSemantics != nil {
				semantics := *result.TextSemantics
				copyEntries[index].Result.TextSemantics = &semantics
			}
		}
		copyEntries[index].Kinds = append([]ResultDeltaKind(nil), entry.Kinds...)
		copyEntries[index].Candidates = append([]ResultRenameCandidate(nil), entry.Candidates...)
	}
	return copyEntries
}

func resultManifestHash(manifest ResultManifest) string {
	h := sha256.New()
	writeResultHashUint(h, uint64(manifest.Version))
	writeResultHashUint(h, uint64(manifest.PolicyVersion))
	writeResultHashBool(h, manifest.Complete)
	writeResultHashString(h, string(manifest.Reason))
	for _, entry := range manifest.Entries {
		writeResultEntryHash(h, entry)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func resultDeltaHash(delta ResultDelta) string {
	h := sha256.New()
	writeResultHashUint(h, uint64(delta.Version))
	writeResultHashBool(h, delta.Complete)
	writeResultHashString(h, string(delta.Reason))
	for _, entry := range delta.Entries {
		writeResultHashBytes(h, entry.Path)
		if entry.Baseline == nil {
			writeResultHashBool(h, false)
		} else {
			writeResultHashBool(h, true)
			writeResultHashBytes(h, entry.Baseline.Path)
			writeResultHashString(h, string(entry.Baseline.Kind))
			writeResultHashUint(h, uint64(entry.Baseline.Mode))
			writeResultHashUint(h, entry.Baseline.Bytes)
			writeResultHashString(h, entry.Baseline.SHA256)
			writeResultHashString(h, string(entry.Baseline.ContentClass))
			writeResultTextSemanticsHash(h, entry.Baseline.TextSemantics)
			writeResultHashBytes(h, entry.Baseline.LinkTarget)
		}
		if entry.Result == nil {
			writeResultHashBool(h, false)
		} else {
			writeResultHashBool(h, true)
			writeResultEntryHash(h, *entry.Result)
		}
		for _, kind := range entry.Kinds {
			writeResultHashString(h, string(kind))
		}
		writeResultHashBool(h, entry.Complete)
		writeResultHashString(h, string(entry.Reason))
	}
	for _, candidate := range delta.Candidates {
		writeResultHashBytes(h, candidate.Source)
		writeResultHashBytes(h, candidate.Target)
		writeResultHashBool(h, candidate.Copy)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeResultEntryHash(h interface{ Write([]byte) (int, error) }, entry ResultSnapshotEntry) {
	writeResultHashBytes(h, entry.Path)
	writeResultHashString(h, string(entry.Kind))
	writeResultHashUint(h, uint64(entry.Mode))
	writeResultHashUint(h, entry.Bytes)
	writeResultHashString(h, entry.SHA256)
	writeResultHashString(h, string(entry.ContentClass))
	writeResultTextSemanticsHash(h, entry.TextSemantics)
	writeResultHashBytes(h, entry.LinkTarget)
	writeResultHashString(h, entry.NativeIdentityHash)
	if entry.NativeAlias == nil {
		writeResultHashBool(h, false)
	} else {
		writeResultHashBool(h, true)
		writeResultHashString(h, entry.NativeAlias.Platform)
		writeResultHashString(h, entry.NativeAlias.VolumeIdentityHash)
		writeResultHashString(h, entry.NativeAlias.FileIdentityHash)
		writeResultHashUint(h, entry.NativeAlias.LinkCount)
	}
	writeResultHashBool(h, entry.Complete)
	writeResultHashString(h, string(entry.Reason))
}

func writeResultTextSemanticsHash(h interface{ Write([]byte) (int, error) }, value *repository.TextByteSemantics) {
	if value == nil {
		writeResultHashBool(h, false)
		return
	}
	writeResultHashBool(h, true)
	writeResultHashString(h, string(value.Encoding))
	writeResultHashUint(h, value.ByteLength)
	writeResultHashString(h, value.SHA256)
	writeResultHashBool(h, value.HasBOM)
	writeResultHashUint(h, value.Endings.LFCount)
	writeResultHashUint(h, value.Endings.CRLFCount)
	writeResultHashUint(h, value.Endings.CRCount)
	writeResultHashBool(h, value.Endings.FinalLF)
	writeResultHashBool(h, value.Endings.Mixed)
	writeResultHashBool(h, value.Empty)
}

func writeResultHashBytes(h interface{ Write([]byte) (int, error) }, value []byte) {
	writeResultHashUint(h, uint64(len(value)))
	_, _ = h.Write(value)
}

func writeResultHashString(h interface{ Write([]byte) (int, error) }, value string) {
	writeResultHashBytes(h, []byte(value))
}

func writeResultHashUint(h interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-1-index] = byte(value >> (index * 8))
	}
	_, _ = h.Write(encoded[:])
}

func writeResultHashBool(h interface{ Write([]byte) (int, error) }, value bool) {
	if value {
		_, _ = h.Write([]byte{1})
		return
	}
	_, _ = h.Write([]byte{0})
}

func validResultHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
