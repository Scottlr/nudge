package paths

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

var (
	ErrProtectedPermissionMissing   = errors.New("protected permission target is missing")
	ErrProtectedPermissionAlias     = errors.New("protected permission target is an alias")
	ErrProtectedPermissionOwnership = errors.New("protected permission target ownership is not proven")
	ErrProtectedPermissionIdentity  = errors.New("protected permission target identity changed")
)

type protectedRootSpec struct {
	id   string
	kind app.ProtectedPermissionRootKind
	path string
}

// ProtectedPermissionService is the T092 path-owned source and native effect
// adapter. It retains resolved locations but publishes only path-free plans.
type ProtectedPermissionService struct {
	specs []protectedRootSpec
}

// NewProtectedPermissionService binds the exact T005 roots eligible for
// permission repair. It never creates or repairs a missing root.
func NewProtectedPermissionService(locations Locations) (*ProtectedPermissionService, error) {
	if err := locations.Validate(); err != nil {
		return nil, err
	}
	return &ProtectedPermissionService{specs: []protectedRootSpec{
		{id: "config-root", kind: app.ProtectedConfigRoot, path: locations.ConfigRoot},
		{id: "state-root", kind: app.ProtectedStateRoot, path: locations.StateRoot},
		{id: "cache-root", kind: app.ProtectedCacheRoot, path: locations.CacheRoot},
		{id: "log-root", kind: app.ProtectedLogRoot, path: locations.LogRoot},
		{id: "workspace-root", kind: app.ProtectedWorkspaceRoot, path: locations.WorkspaceRoot},
	}}, nil
}

// ListProtectedPermissionTargets returns existing roots whose current policy
// differs from the desired owner-only policy. Ambiguous roots are omitted and
// remain visible through the existing protected-root health result.
func (s *ProtectedPermissionService) ListProtectedPermissionTargets(ctx context.Context) ([]app.ProtectedPermissionTarget, error) {
	if s == nil || ctx == nil {
		return nil, app.ErrProtectedPermissionRepair
	}
	targets := make([]app.ProtectedPermissionTarget, 0, len(s.specs))
	for _, spec := range s.specs {
		target, err := s.loadTarget(ctx, spec)
		if errors.Is(err, ErrProtectedPermissionMissing) || errors.Is(err, ErrProtectedPermissionAlias) || errors.Is(err, ErrProtectedPermissionOwnership) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if target.CurrentPermissionHash != target.DesiredPermissionHash {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// LoadProtectedPermissionTarget returns fresh identity and permission
// evidence for one stable root identity.
func (s *ProtectedPermissionService) LoadProtectedPermissionTarget(ctx context.Context, resourceID string) (app.ProtectedPermissionTarget, error) {
	if s == nil || ctx == nil || resourceID == "" {
		return app.ProtectedPermissionTarget{}, app.ErrProtectedPermissionTarget
	}
	for _, spec := range s.specs {
		if spec.id == resourceID {
			return s.loadTarget(ctx, spec)
		}
	}
	return app.ProtectedPermissionTarget{}, app.ErrProtectedPermissionTarget
}

// Inspect verifies the exact target identity and reports the current policy.
func (s *ProtectedPermissionService) Inspect(ctx context.Context, target app.ProtectedPermissionTarget) (app.ProtectedPermissionProof, error) {
	if s == nil || ctx == nil {
		return app.ProtectedPermissionProof{}, app.ErrProtectedPermissionRepair
	}
	if err := ctx.Err(); err != nil {
		return app.ProtectedPermissionProof{}, err
	}
	spec, err := s.spec(target.ResourceID)
	if err != nil || target.Validate() != nil {
		return app.ProtectedPermissionProof{}, app.ErrProtectedPermissionTarget
	}
	pathHash := protectedPathHash(spec.path)
	if pathHash != target.PathHash {
		return app.ProtectedPermissionProof{}, ErrProtectedPermissionIdentity
	}
	observation, err := inspectProtectedPermission(spec.path)
	if err != nil {
		return app.ProtectedPermissionProof{}, err
	}
	identityHash := nativeIdentityHash(observation.identity)
	if identityHash != target.NativeIdentityHash {
		return app.ProtectedPermissionProof{}, ErrProtectedPermissionIdentity
	}
	markerHash := protectedMarkerHash(spec.id, pathHash)
	if markerHash != target.OwnershipMarkerHash {
		return app.ProtectedPermissionProof{}, ErrProtectedPermissionIdentity
	}
	return app.ProtectedPermissionProof{ResourceID: target.ResourceID, PathHash: pathHash, NativeIdentityHash: identityHash, OwnershipMarkerHash: markerHash, BeforePermissionHash: observation.currentHash, AfterPermissionHash: observation.currentHash, DesiredPermissionHash: observation.desiredHash}, nil
}

// Repair reopens and verifies the exact native object immediately before the
// narrow permission transition, then returns a fresh postcondition proof. It
// never creates, replaces, or traverses roots.
func (s *ProtectedPermissionService) Repair(ctx context.Context, target app.ProtectedPermissionTarget, expected app.ProtectedPermissionProof) (app.ProtectedPermissionProof, error) {
	if s == nil || ctx == nil {
		return app.ProtectedPermissionProof{}, app.ErrProtectedPermissionRepair
	}
	if err := ctx.Err(); err != nil {
		return app.ProtectedPermissionProof{}, err
	}
	if target.Validate() != nil || expected.Validate() != nil || !proofMatchesPermissionTarget(expected, target) {
		return app.ProtectedPermissionProof{}, app.ErrProtectedPermissionProof
	}
	spec, err := s.spec(target.ResourceID)
	if err != nil {
		return app.ProtectedPermissionProof{}, err
	}
	if protectedPathHash(spec.path) != target.PathHash {
		return app.ProtectedPermissionProof{}, ErrProtectedPermissionIdentity
	}
	after, err := repairProtectedPermission(spec.path, target.NativeIdentityHash, target.DesiredPermissionHash)
	if err != nil {
		return app.ProtectedPermissionProof{}, err
	}
	identityHash := nativeIdentityHash(after.identity)
	if identityHash != target.NativeIdentityHash || after.currentHash != target.DesiredPermissionHash {
		return app.ProtectedPermissionProof{}, ErrProtectedPermissionIdentity
	}
	return app.ProtectedPermissionProof{ResourceID: target.ResourceID, PathHash: target.PathHash, NativeIdentityHash: identityHash, OwnershipMarkerHash: target.OwnershipMarkerHash, BeforePermissionHash: expected.BeforePermissionHash, AfterPermissionHash: after.currentHash, DesiredPermissionHash: after.desiredHash}, nil
}

func (s *ProtectedPermissionService) spec(resourceID string) (protectedRootSpec, error) {
	for _, spec := range s.specs {
		if spec.id == resourceID {
			return spec, nil
		}
	}
	return protectedRootSpec{}, app.ErrProtectedPermissionTarget
}

func (s *ProtectedPermissionService) loadTarget(ctx context.Context, spec protectedRootSpec) (app.ProtectedPermissionTarget, error) {
	if ctx == nil {
		return app.ProtectedPermissionTarget{}, app.ErrProtectedPermissionRepair
	}
	if err := ctx.Err(); err != nil {
		return app.ProtectedPermissionTarget{}, err
	}
	pathHash := protectedPathHash(spec.path)
	observation, err := inspectProtectedPermission(spec.path)
	if err != nil {
		return app.ProtectedPermissionTarget{}, err
	}
	target := app.ProtectedPermissionTarget{ResourceID: spec.id, Kind: spec.kind, PathHash: pathHash, NativeIdentityHash: nativeIdentityHash(observation.identity), OwnershipMarkerHash: protectedMarkerHash(spec.id, pathHash), CurrentPermissionHash: observation.currentHash, DesiredPermissionHash: observation.desiredHash, PolicyVersion: app.ProtectedPermissionPolicyVersion}
	if err := target.Validate(); err != nil {
		return app.ProtectedPermissionTarget{}, err
	}
	return target, nil
}

type protectedPermissionObservation struct {
	identity    repository.NativeIdentity
	currentHash string
	desiredHash string
}

func protectedPathHash(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(digest[:])
}

func nativeIdentityHash(identity repository.NativeIdentity) string {
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

func protectedMarkerHash(resourceID, pathHash string) string {
	digest := sha256.Sum256([]byte("nudge-protected-root-v1\x00" + resourceID + "\x00" + pathHash))
	return hex.EncodeToString(digest[:])
}

func proofMatchesPermissionTarget(proof app.ProtectedPermissionProof, target app.ProtectedPermissionTarget) bool {
	return proof.ResourceID == target.ResourceID && proof.PathHash == target.PathHash && proof.NativeIdentityHash == target.NativeIdentityHash && proof.OwnershipMarkerHash == target.OwnershipMarkerHash && proof.DesiredPermissionHash == target.DesiredPermissionHash
}

var _ app.ProtectedPermissionTargetStore = (*ProtectedPermissionService)(nil)
var _ app.ProtectedPermissionAdapter = (*ProtectedPermissionService)(nil)
