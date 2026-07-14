package artifactspool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/paths"
)

func removeOwnedSpool(spoolPath, payloadPath string) error {
	if err := paths.EnsurePrivateDir(spoolPath); err != nil {
		return fmt.Errorf("%w: spool root", app.ErrSpoolResidueAmbiguous)
	}
	if info, err := os.Lstat(payloadPath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: payload root", app.ErrSpoolResidueAmbiguous)
		}
		if err := removePayloadTree(payloadPath); err != nil {
			return err
		}
		if err := os.Remove(payloadPath); err != nil {
			return fmt.Errorf("%w: payload root removal", app.ErrSpoolResidueAmbiguous)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: payload lookup", app.ErrSpoolResidueAmbiguous)
	}
	if err := removeMarkerOnly(spoolPath); err != nil {
		return fmt.Errorf("%w: marker removal", app.ErrSpoolResidueAmbiguous)
	}
	if err := os.Remove(spoolPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: root removal", app.ErrSpoolResidueAmbiguous)
	}
	return nil
}

func removePayloadTree(root string) error {
	pathsToRemove := make([]string, 0, 8)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("%w: payload walk", app.ErrSpoolResidueAmbiguous)
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			if err := paths.EnsurePrivateDir(path); err != nil {
				return fmt.Errorf("%w: payload directory", app.ErrSpoolResidueAmbiguous)
			}
		} else {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return fmt.Errorf("%w: payload relative path", app.ErrSpoolResidueAmbiguous)
			}
			file, err := openVerifiedFile(root, relative)
			if err != nil {
				return err
			}
			closeErr := file.Close()
			if closeErr != nil {
				return fmt.Errorf("%w: payload close", app.ErrSpoolResidueAmbiguous)
			}
		}
		pathsToRemove = append(pathsToRemove, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(pathsToRemove, func(i, j int) bool { return len(pathsToRemove[i]) > len(pathsToRemove[j]) })
	for _, path := range pathsToRemove {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("%w: payload entry removal", app.ErrSpoolResidueAmbiguous)
		}
	}
	return nil
}
