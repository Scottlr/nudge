package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/filelock"
	"github.com/Scottlr/nudge/internal/paths"
)

// RepositoryCleanupOwner enumerates and removes only owner-marked logs under
// one hashed repository scope. Global startup logs are outside this owner.
type RepositoryCleanupOwner struct {
	root         string
	repositoryID domain.RepositoryID
	scopeRoot    string
	ownerRoot    string
}

// NewRepositoryCleanupOwner binds cleanup to one protected log root and one
// repository identity. The repository identity is never used as a pathname.
func NewRepositoryCleanupOwner(root string, repositoryID domain.RepositoryID) (*RepositoryCleanupOwner, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || !validRepositoryID(repositoryID) {
		return nil, app.ErrCleanupInvalid
	}
	scopeRoot := filepath.Join(root, "repositories", repositoryScopeID(repositoryID))
	return &RepositoryCleanupOwner{root: root, repositoryID: repositoryID, scopeRoot: scopeRoot, ownerRoot: filepath.Join(scopeRoot, "owners")}, nil
}

// Resources returns exact marker-backed cleanup resources and redacted
// blockers. It never interprets filenames as ownership evidence.
func (o *RepositoryCleanupOwner) Resources(ctx context.Context, repositoryID domain.RepositoryID) ([]app.CleanupResource, []string, error) {
	if o == nil || ctx == nil || repositoryID != o.repositoryID {
		return nil, nil, app.ErrCleanupInvalid
	}
	if err := contextErr(ctx); err != nil {
		return nil, nil, err
	}
	if err := paths.ValidatePrivateDir(o.ownerRoot); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	} else if err != nil {
		return nil, []string{"repository log owner root is not a verified private directory"}, nil
	}
	entries, err := os.ReadDir(o.ownerRoot)
	if err != nil {
		return nil, nil, err
	}
	resources := make([]app.CleanupResource, 0, len(entries))
	blockers := make([]string, 0)
	for _, entry := range entries {
		if err := contextErr(ctx); err != nil {
			return nil, nil, err
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".lock") && !entry.IsDir() {
			continue
		}
		if entry.IsDir() || filepath.Ext(name) != ".json" || entry.Type()&os.ModeSymlink != 0 {
			blockers = append(blockers, "repository log owner contains an unexpected entry")
			continue
		}
		data, readErr := paths.ReadProtectedFileBounded(o.ownerRoot, name, maxOwnerMarkerBytes)
		if readErr != nil {
			blockers = append(blockers, "repository log owner marker cannot be read")
			continue
		}
		var marker ownerMarker
		if json.Unmarshal(data, &marker) != nil || !validRepositoryLogMarker(marker, o.repositoryID) || name != marker.ProcessID+".json" {
			blockers = append(blockers, "repository log owner marker is ambiguous")
			continue
		}
		digest := digestBytes(data)
		resources = append(resources, app.CleanupResource{
			ID: marker.ProcessID, Kind: app.CleanupResourceLog, OwnerID: marker.ProcessID,
			RepositoryID: o.repositoryID, CanonicalPath: o.scopeRoot, ParentRoot: filepath.Dir(o.scopeRoot),
			MarkerNonce: marker.ProcessID, ManifestHash: digest, NativeIdentity: digest,
		})
	}
	return resources, blockers, nil
}

// Remove acquires the exact process owner lock and removes only the files
// listed by its verified marker. A live writer returns a conflict.
func (o *RepositoryCleanupOwner) Remove(ctx context.Context, resource app.CleanupResource) error {
	if o == nil || ctx == nil || resource.Kind != app.CleanupResourceLog || resource.RepositoryID != o.repositoryID || resource.ID == "" || resource.ID != resource.OwnerID || resource.MarkerNonce != resource.ID || resource.CanonicalPath != o.scopeRoot || resource.ParentRoot != filepath.Dir(o.scopeRoot) {
		return app.ErrCleanupInvalid
	}
	if !validOwnerToken(resource.ID) || !validCleanupDigest(resource.ManifestHash) || resource.NativeIdentity != resource.ManifestHash {
		return app.ErrCleanupInvalid
	}
	name := resource.ID + ".json"
	data, err := paths.ReadProtectedFileBounded(o.ownerRoot, name, maxOwnerMarkerBytes)
	if err != nil {
		return app.ErrCleanupConflict
	}
	if digestBytes(data) != resource.ManifestHash {
		return app.ErrCleanupStalePlan
	}
	var marker ownerMarker
	if json.Unmarshal(data, &marker) != nil || !validRepositoryLogMarker(marker, o.repositoryID) {
		return app.ErrCleanupConflict
	}
	lock, err := filelock.TryAcquire(ctx, filepath.Join(o.ownerRoot, resource.ID+".lock"))
	if err != nil {
		return err
	}
	for _, fileName := range marker.Files {
		if err := contextErr(ctx); err != nil {
			_ = lock.Close()
			return err
		}
		if err := removeOwnedFile(o.scopeRoot, fileName); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = lock.Close()
			return err
		}
	}
	if err := removeOwnedFile(o.ownerRoot, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = lock.Close()
		return err
	}
	if err := lock.Close(); err != nil {
		return err
	}
	if err := removeOwnedFile(o.ownerRoot, resource.ID+".lock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validRepositoryLogMarker(marker ownerMarker, repositoryID domain.RepositoryID) bool {
	if marker.Version != logSchemaVersion || !validOwnerToken(marker.ProcessID) || marker.RepositoryID != repositoryID || (marker.State != "active" && marker.State != "closed") || len(marker.Files) == 0 || len(marker.Files) > maxLogFileCount {
		return false
	}
	for _, name := range marker.Files {
		if !validOwnedLogName(marker.ProcessID, name) {
			return false
		}
	}
	return true
}

func validCleanupDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var _ app.CleanupResourceEnumerator = (*RepositoryCleanupOwner)(nil)
var _ app.CleanupResourceOwner = (*RepositoryCleanupOwner)(nil)
