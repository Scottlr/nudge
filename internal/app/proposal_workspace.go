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

const WorkspaceManifestVersion uint32 = 1

var (
	ErrInvalidProposalWorkspaceLifecycle  = errors.New("invalid proposal workspace lifecycle")
	ErrProposalWorkspaceLifecycleConflict = errors.New("proposal workspace lifecycle conflict")
)

// ProposalWorkspaceLifecyclePhase is the durable journal phase for one
// non-retirement baseline/result operation.
type ProposalWorkspaceLifecyclePhase string

const (
	WorkspaceBaselineInstalling ProposalWorkspaceLifecyclePhase = "baseline_installing"
	WorkspaceResultPreparing    ProposalWorkspaceLifecyclePhase = "result_preparing"
	WorkspaceResultResetting    ProposalWorkspaceLifecyclePhase = "result_resetting"
	WorkspaceBaselineAdvancing  ProposalWorkspaceLifecyclePhase = "baseline_advancing"
	WorkspaceLifecycleReady     ProposalWorkspaceLifecyclePhase = "ready"
	WorkspaceLifecycleRepair    ProposalWorkspaceLifecyclePhase = "repair_required"
)

func (p ProposalWorkspaceLifecyclePhase) Validate() error {
	switch p {
	case WorkspaceBaselineInstalling, WorkspaceResultPreparing, WorkspaceResultResetting, WorkspaceBaselineAdvancing, WorkspaceLifecycleReady, WorkspaceLifecycleRepair:
		return nil
	default:
		return ErrInvalidProposalWorkspaceLifecycle
	}
}

// CanTransitionTo permits idempotent writes for active phases but never
// silently reopens a completed or repair-required operation.
func (p ProposalWorkspaceLifecyclePhase) CanTransitionTo(next ProposalWorkspaceLifecyclePhase) bool {
	if p == next {
		return p != WorkspaceLifecycleReady && p != WorkspaceLifecycleRepair
	}
	switch p {
	case WorkspaceBaselineInstalling:
		return next == WorkspaceResultPreparing || next == WorkspaceLifecycleRepair
	case WorkspaceResultPreparing, WorkspaceResultResetting, WorkspaceBaselineAdvancing:
		return next == WorkspaceLifecycleReady || next == WorkspaceLifecycleRepair
	case WorkspaceLifecycleReady:
		return next == WorkspaceLifecycleRepair
	default:
		return false
	}
}

// ProposalWorkspaceLifecyclePurpose identifies the mutation represented by a
// journal row. The operation remains separate from the workspace identity.
type ProposalWorkspaceLifecyclePurpose string

const (
	WorkspacePurposeInstallBaseline ProposalWorkspaceLifecyclePurpose = "install_baseline"
	WorkspacePurposeResetResult     ProposalWorkspaceLifecyclePurpose = "reset_result"
	WorkspacePurposeAdvanceBaseline ProposalWorkspaceLifecyclePurpose = "advance_baseline"
)

func (p ProposalWorkspaceLifecyclePurpose) Validate() error {
	switch p {
	case WorkspacePurposeInstallBaseline, WorkspacePurposeResetResult, WorkspacePurposeAdvanceBaseline:
		return nil
	default:
		return ErrInvalidProposalWorkspaceLifecycle
	}
}

// WorkspaceSourceIdentity binds materialization to accepted immutable input.
type WorkspaceSourceIdentity struct {
	Kind         string
	ID           string
	ManifestHash string
	Generation   repository.TargetGeneration
	Fingerprint  string
}

func (s WorkspaceSourceIdentity) Validate() error {
	if s.Kind == "" || s.ID == "" || !validWorkspaceHash(s.ManifestHash) {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	if (s.Generation == 0) != (s.Fingerprint == "") || s.Fingerprint != "" && !validWorkspaceHash(s.Fingerprint) {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	return nil
}

// WorkspaceManifestEntry is the path-free, independently verified identity of
// one baseline/result entry. Path and LinkTarget use []byte so JSON preserves
// raw repository bytes through SQLite without UTF-8 replacement.
type WorkspaceManifestEntry struct {
	Path       []byte              `json:"path"`
	Kind       repository.FileKind `json:"kind"`
	Mode       uint32              `json:"mode"`
	Bytes      uint64              `json:"bytes"`
	SHA256     string              `json:"sha256"`
	LinkTarget []byte              `json:"link_target,omitempty"`
}

func (e WorkspaceManifestEntry) Validate() error {
	if _, err := repository.NewRepoPath(e.Path); err != nil || e.Mode == 0 {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	switch e.Kind {
	case repository.FileKindDirectory:
		if e.Bytes != 0 || e.SHA256 != "" || len(e.LinkTarget) != 0 {
			return ErrInvalidProposalWorkspaceLifecycle
		}
	case repository.FileKindRegular:
		if !validWorkspaceHash(e.SHA256) || len(e.LinkTarget) != 0 {
			return ErrInvalidProposalWorkspaceLifecycle
		}
	case repository.FileKindSymlink:
		if len(e.LinkTarget) == 0 || e.Bytes != uint64(len(e.LinkTarget)) || !validWorkspaceHash(e.SHA256) || len(e.LinkTarget) > 32<<20 {
			return ErrInvalidProposalWorkspaceLifecycle
		}
	default:
		return ErrInvalidProposalWorkspaceLifecycle
	}
	return nil
}

// WorkspaceManifest is a deterministic identity for the complete contents of
// one trusted baseline or result root.
type WorkspaceManifest struct {
	Version    uint32                   `json:"version"`
	Entries    []WorkspaceManifestEntry `json:"entries"`
	Hash       string                   `json:"hash"`
	TotalBytes uint64                   `json:"total_bytes"`
}

func NewWorkspaceManifest(entries []WorkspaceManifestEntry) (WorkspaceManifest, error) {
	copyEntries := cloneWorkspaceManifestEntries(entries)
	sort.Slice(copyEntries, func(i, j int) bool { return bytes.Compare(copyEntries[i].Path, copyEntries[j].Path) < 0 })
	manifest := WorkspaceManifest{Version: WorkspaceManifestVersion, Entries: copyEntries}
	for _, entry := range copyEntries {
		if err := entry.Validate(); err != nil {
			return WorkspaceManifest{}, err
		}
		if entry.Kind != repository.FileKindDirectory {
			if manifest.TotalBytes > ^uint64(0)-entry.Bytes {
				return WorkspaceManifest{}, ErrInvalidProposalWorkspaceLifecycle
			}
			manifest.TotalBytes += entry.Bytes
		}
	}
	for i := 1; i < len(copyEntries); i++ {
		if bytes.Equal(copyEntries[i-1].Path, copyEntries[i].Path) {
			return WorkspaceManifest{}, ErrInvalidProposalWorkspaceLifecycle
		}
	}
	manifest.Hash = workspaceManifestHash(manifest.Version, manifest.Entries)
	return manifest, nil
}

func (m WorkspaceManifest) Validate() error {
	if m.Version != WorkspaceManifestVersion || !validWorkspaceHash(m.Hash) {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	computed, err := NewWorkspaceManifest(m.Entries)
	if err != nil || computed.Hash != m.Hash || computed.TotalBytes != m.TotalBytes {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	return nil
}

func (m WorkspaceManifest) Clone() WorkspaceManifest {
	m.Entries = cloneWorkspaceManifestEntries(m.Entries)
	return m
}

// ProposalWorkspaceLifecycle is the fenced durable journal and positive
// ownership claim for one baseline/result operation.
type ProposalWorkspaceLifecycle struct {
	WorkspaceID               domain.WorkspaceID
	RepositoryID              domain.RepositoryID
	WorktreeID                domain.WorktreeID
	SessionID                 domain.ReviewSessionID
	ThreadID                  domain.ReviewThreadID
	OperationID               domain.OperationID
	Owner                     string
	Nonce                     string
	CapacityReservationMarker string
	Purpose                   ProposalWorkspaceLifecyclePurpose
	Phase                     ProposalWorkspaceLifecyclePhase
	Source                    WorkspaceSourceIdentity
	Baseline                  WorkspaceManifest
	Result                    WorkspaceManifest
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

func (l ProposalWorkspaceLifecycle) Validate() error {
	if l.WorkspaceID == "" || l.RepositoryID == "" || l.WorktreeID == "" || l.SessionID == "" || l.ThreadID == "" || l.OperationID == "" || l.Owner == "" || l.CapacityReservationMarker == "" || !validWorkspaceNonce(l.Nonce) || l.Purpose.Validate() != nil || l.Phase.Validate() != nil || l.CreatedAt.IsZero() || l.UpdatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	if l.Purpose == WorkspacePurposeInstallBaseline || l.Purpose == WorkspacePurposeAdvanceBaseline {
		if l.Source.Validate() != nil {
			return ErrInvalidProposalWorkspaceLifecycle
		}
	}
	if l.Phase == WorkspaceResultPreparing || l.Phase == WorkspaceResultResetting || l.Phase == WorkspaceBaselineAdvancing || l.Phase == WorkspaceLifecycleReady {
		if l.Baseline.Validate() != nil {
			return ErrInvalidProposalWorkspaceLifecycle
		}
	}
	if l.Phase == WorkspaceLifecycleReady && l.Result.Validate() != nil {
		return ErrInvalidProposalWorkspaceLifecycle
	}
	return nil
}

// ProposalWorkspaceLifecycleStore is the read boundary used by recovery.
type ProposalWorkspaceLifecycleStore interface {
	LoadProposalWorkspaceLifecycle(context.Context, domain.WorkspaceID, domain.OperationID) (ProposalWorkspaceLifecycle, error)
	LoadLatestProposalWorkspaceLifecycle(context.Context, domain.WorkspaceID) (ProposalWorkspaceLifecycle, error)
}

// ProposalWorkspaceLifecycleStoreTx is kept optional so existing review-store
// fakes do not inherit filesystem lifecycle responsibilities.
type ProposalWorkspaceLifecycleStoreTx interface {
	CreateProposalWorkspaceLifecycle(context.Context, ProposalWorkspaceLifecycle) error
	UpdateProposalWorkspaceLifecycle(context.Context, ProposalWorkspaceLifecycle) error
	UpdateProposalWorkspace(context.Context, review.ProposalWorkspace) error
}

func cloneWorkspaceManifestEntries(entries []WorkspaceManifestEntry) []WorkspaceManifestEntry {
	copyEntries := make([]WorkspaceManifestEntry, len(entries))
	for i, entry := range entries {
		copyEntries[i] = entry
		copyEntries[i].Path = append([]byte(nil), entry.Path...)
		copyEntries[i].LinkTarget = append([]byte(nil), entry.LinkTarget...)
	}
	return copyEntries
}

func workspaceManifestHash(version uint32, entries []WorkspaceManifestEntry) string {
	h := sha256.New()
	writeWorkspaceHashUint(h, uint64(version))
	for _, entry := range entries {
		writeWorkspaceHashBytes(h, entry.Path)
		writeWorkspaceHashString(h, string(entry.Kind))
		writeWorkspaceHashUint(h, uint64(entry.Mode))
		writeWorkspaceHashUint(h, entry.Bytes)
		writeWorkspaceHashString(h, entry.SHA256)
		writeWorkspaceHashBytes(h, entry.LinkTarget)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeWorkspaceHashBytes(writer interface{ Write([]byte) (int, error) }, value []byte) {
	writeWorkspaceHashUint(writer, uint64(len(value)))
	_, _ = writer.Write(value)
}

func writeWorkspaceHashString(writer interface{ Write([]byte) (int, error) }, value string) {
	writeWorkspaceHashBytes(writer, []byte(value))
}

func writeWorkspaceHashUint(writer interface{ Write([]byte) (int, error) }, value uint64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-1-index] = byte(value >> (index * 8))
	}
	_, _ = writer.Write(encoded[:])
}

func validWorkspaceHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validWorkspaceNonce(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
