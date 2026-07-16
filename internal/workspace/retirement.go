package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

var (
	ErrWorkspaceRetirementOwnership = errors.New("proposal workspace retirement ownership mismatch")
	ErrWorkspaceRetirementContents  = errors.New("proposal workspace retirement contents are ambiguous")
)

// RetirementExecutor verifies and removes one exact T108 workspace. It never
// accepts a path from the caller; the path is recovered from durable creation
// evidence after the plan identity has been matched.
type RetirementExecutor struct {
	allocator *Allocator
	store     WorkspaceStore
}

// WorkspaceCleanupOwner adapts the existing verified workspace owner to the
// repository cleanup coordinator. It does not expose a generic path delete.
type WorkspaceCleanupOwner struct {
	Executor *RetirementExecutor
}

func (o WorkspaceCleanupOwner) Remove(ctx context.Context, resource app.CleanupResource) error {
	if o.Executor == nil || resource.Kind != app.CleanupResourceWorkspace || resource.ID == "" {
		return app.ErrCleanupInvalid
	}
	return o.Executor.RemoveOwned(ctx, resource)
}

// NewRetirementExecutor constructs the T109 filesystem owner boundary.
func NewRetirementExecutor(allocator *Allocator, store WorkspaceStore) (*RetirementExecutor, error) {
	if allocator == nil || store == nil {
		return nil, ErrWorkspaceRetirementOwnership
	}
	return &RetirementExecutor{allocator: allocator, store: store}, nil
}

// OwnershipDigest returns the stable digest bound to a verified creation
// record. The digest is persisted instead of exposing native paths to app
// retention metadata.
func OwnershipDigest(evidence WorkspaceCreationEvidence) (string, error) {
	if evidence.Validate() != nil {
		return "", ErrWorkspaceRetirementOwnership
	}
	value, err := json.Marshal(struct {
		WorkspaceID               domain.WorkspaceID
		Parent                    RootIdentity
		Roots                     RootSet
		MarkerVersion             uint32
		IsolationVersion          uint32
		MarkerSHA256              string
		CapacityReservationMarker string
		Nonce                     string
	}{evidence.WorkspaceID, evidence.Parent, evidence.Roots, evidence.MarkerVersion, evidence.IsolationVersion, evidence.MarkerSHA256, evidence.CapacityReservationMarker, evidence.Nonce})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

// Verify proves that the plan still refers to the same owned workspace.
func (e *RetirementExecutor) Verify(ctx context.Context, plan app.WorkspaceRetirement) (app.WorkspaceRetirementProof, error) {
	return e.verify(ctx, plan, false)
}

// Remove verifies ownership while holding the workspace lock, then removes
// only the marker and the three exact Nudge-owned roots. A changed entry or
// unexpected top-level child fails closed as repair-required.
func (e *RetirementExecutor) Remove(ctx context.Context, plan app.WorkspaceRetirement) (app.WorkspaceRetirementProof, error) {
	return e.verify(ctx, plan, true)
}

// RemoveOwned verifies one durable creation record and removes its exact
// marker-bound roots. Cleanup uses this path after its repository plan has
// acquired the maintenance and session locks.
func (e *RetirementExecutor) RemoveOwned(ctx context.Context, resource app.CleanupResource) error {
	if e == nil || ctx == nil || resource.Kind != app.CleanupResourceWorkspace || resource.ID == "" {
		return app.ErrCleanupInvalid
	}
	evidence, err := e.store.LoadWorkspaceCreation(ctx, domain.WorkspaceID(resource.ID))
	if err != nil {
		return err
	}
	if evidence.RepositoryID != resource.RepositoryID || evidence.Nonce != resource.MarkerNonce || filepath.Dir(evidence.Roots.Baseline.Path) != resource.CanonicalPath || evidence.Parent.CanonicalPath != resource.ParentRoot {
		return ErrWorkspaceRetirementOwnership
	}
	if evidence.Phase != WorkspaceVerified {
		return ErrWorkspaceRetirementOwnership
	}
	digest, err := OwnershipDigest(evidence)
	if err != nil || resource.NativeIdentity != string(evidence.Parent.NativeIdentity) && resource.NativeIdentity != digest {
		return ErrWorkspaceRetirementOwnership
	}
	lock, err := e.allocator.acquireWorkspaceLock(ctx, evidence.WorkspaceID)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := verifyRetirementFilesystem(filepath.Dir(evidence.Roots.Baseline.Path), evidence); err != nil {
		return err
	}
	if err := removeOwnedWorkspace(filepath.Dir(evidence.Roots.Baseline.Path), evidence); err != nil {
		return err
	}
	return nil
}

func (e *RetirementExecutor) verify(ctx context.Context, plan app.WorkspaceRetirement, remove bool) (app.WorkspaceRetirementProof, error) {
	if e == nil || ctx == nil || plan.Candidate.Validate() != nil || plan.Decision.Kind != app.WorkspaceRetentionEligible || plan.Phase.Validate() != nil {
		return app.WorkspaceRetirementProof{}, ErrWorkspaceRetirementOwnership
	}
	evidence, err := e.store.LoadWorkspaceCreation(ctx, plan.Candidate.WorkspaceID)
	if err != nil {
		return app.WorkspaceRetirementProof{}, err
	}
	if evidence.WorkspaceID != plan.Candidate.WorkspaceID || evidence.Nonce != plan.Candidate.MarkerNonce || evidence.Phase != WorkspaceVerified || evidence.RepositoryID != plan.Candidate.RepositoryID || evidence.WorktreeID != plan.Candidate.WorktreeID || evidence.ThreadID != plan.Candidate.ThreadID {
		return app.WorkspaceRetirementProof{}, ErrWorkspaceRetirementOwnership
	}
	digest, err := OwnershipDigest(evidence)
	if err != nil || digest != plan.Candidate.OwnershipDigest {
		return app.WorkspaceRetirementProof{}, ErrWorkspaceRetirementOwnership
	}
	lock, err := e.allocator.acquireWorkspaceLock(ctx, plan.Candidate.WorkspaceID)
	if err != nil {
		return app.WorkspaceRetirementProof{}, err
	}
	defer lock.Close()
	workspaceDir := filepath.Dir(evidence.Roots.Baseline.Path)
	if filepath.Base(workspaceDir) != string(plan.Candidate.WorkspaceID) || filepath.Dir(workspaceDir) != e.allocator.root {
		return app.WorkspaceRetirementProof{}, ErrWorkspaceRetirementOwnership
	}
	if _, statErr := os.Lstat(workspaceDir); errors.Is(statErr, os.ErrNotExist) {
		return app.WorkspaceRetirementProof{WorkspaceID: plan.Candidate.WorkspaceID, OwnershipDigest: digest, MarkerNonce: evidence.Nonce, AlreadyRemoved: true}, nil
	} else if statErr != nil {
		return app.WorkspaceRetirementProof{}, statErr
	}
	if err := verifyRetirementFilesystem(workspaceDir, evidence); err != nil {
		return app.WorkspaceRetirementProof{}, err
	}
	if !remove {
		return app.WorkspaceRetirementProof{WorkspaceID: plan.Candidate.WorkspaceID, OwnershipDigest: digest, MarkerNonce: evidence.Nonce}, nil
	}
	if err := removeOwnedWorkspace(workspaceDir, evidence); err != nil {
		return app.WorkspaceRetirementProof{}, err
	}
	if _, err := os.Lstat(workspaceDir); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return app.WorkspaceRetirementProof{}, ErrWorkspaceRetirementContents
		}
		return app.WorkspaceRetirementProof{}, err
	}
	return app.WorkspaceRetirementProof{WorkspaceID: plan.Candidate.WorkspaceID, OwnershipDigest: digest, MarkerNonce: evidence.Nonce}, nil
}

func verifyRetirementFilesystem(workspaceDir string, evidence WorkspaceCreationEvidence) error {
	info, err := os.Lstat(workspaceDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrWorkspaceRetirementOwnership
	}
	parent, err := inspectRoot(RootParent, filepath.Dir(workspaceDir), true)
	if err != nil || parent != evidence.Parent {
		return ErrWorkspaceRetirementOwnership
	}
	if err := verifyMarker(workspaceDir, markerFromEvidence(evidence, evidence.Roots), true); err != nil {
		return err
	}
	actual, err := inspectRootSet(evidence.Roots)
	if err != nil || actual != evidence.Roots {
		return ErrWorkspaceRetirementOwnership
	}
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		return err
	}
	allowed := map[string]struct{}{workspaceMarkerName: {}, filepath.Base(evidence.Roots.Baseline.Path): {}, filepath.Base(evidence.Roots.Admin.Path): {}, filepath.Base(evidence.Roots.Result.Path): {}}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return ErrWorkspaceRetirementContents
		}
	}
	return nil
}

func removeOwnedWorkspace(workspaceDir string, evidence WorkspaceCreationEvidence) error {
	for _, root := range []string{evidence.Roots.Baseline.Path, evidence.Roots.Admin.Path, evidence.Roots.Result.Path} {
		if err := removeRetirementTree(root); err != nil {
			return err
		}
	}
	if err := removeRetirementTree(filepath.Join(workspaceDir, workspaceMarkerName)); err != nil {
		return err
	}
	return os.Remove(workspaceDir)
}

// removeOwnedTree is deliberately limited to a path already proven by the
// marker/root identity. It uses Lstat and direct child operations so symlink
// entries are removed as leaves and never traversed.
func removeRetirementTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := removeRetirementTree(filepath.Join(path, entry.Name())); err != nil {
				return err
			}
		}
	}
	return os.Remove(path)
}
