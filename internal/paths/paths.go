// Package paths resolves Nudge-owned native storage locations. It does not
// create directories or interpret repository-relative paths.
package paths

import (
	"errors"
	"os"
	"path/filepath"
)

var (
	// ErrInvalidLocation reports a relative, empty, or contradictory location.
	ErrInvalidLocation = errors.New("invalid Nudge location")
)

// Locations contains the Nudge-owned roots used by configuration and later
// platform services. Resolve never creates any of them.
type Locations struct {
	ConfigRoot    string
	StateRoot     string
	CacheRoot     string
	WorkspaceRoot string
	LogRoot       string
	ThemesRoot    string
	ConfigFile    string
}

// Resolve computes platform defaults and applies explicit NUDGE_* home
// overrides. Override values are complete Nudge roots, not parent XDG roots.
func Resolve(environ map[string]string) (Locations, error) {
	if environ == nil {
		environ = environment(os.Environ())
	}
	defaults, err := nativeDefaults(environ)
	if err != nil {
		return Locations{}, err
	}
	configRoot, err := overrideRoot(environ, "NUDGE_CONFIG_HOME", defaults.ConfigRoot)
	if err != nil {
		return Locations{}, err
	}
	stateRoot, err := overrideRoot(environ, "NUDGE_STATE_HOME", defaults.StateRoot)
	if err != nil {
		return Locations{}, err
	}
	cacheRoot, err := overrideRoot(environ, "NUDGE_CACHE_HOME", defaults.CacheRoot)
	if err != nil {
		return Locations{}, err
	}
	logFallback := filepath.Join(stateRoot, "log")
	if defaults.LogRoot != filepath.Join(defaults.StateRoot, "log") {
		logFallback = defaults.LogRoot
	}
	logRoot, err := overrideRoot(environ, "NUDGE_LOG_HOME", logFallback)
	if err != nil {
		return Locations{}, err
	}
	locations := Locations{
		ConfigRoot:    configRoot,
		StateRoot:     stateRoot,
		CacheRoot:     cacheRoot,
		WorkspaceRoot: filepath.Join(cacheRoot, "workspaces"),
		LogRoot:       logRoot,
		ThemesRoot:    filepath.Join(configRoot, "themes"),
		ConfigFile:    filepath.Join(configRoot, "config.toml"),
	}
	if err := locations.Validate(); err != nil {
		return Locations{}, err
	}
	return locations, nil
}

// Validate ensures every location is absolute and that derived paths stay
// beneath their owning roots.
func (l Locations) Validate() error {
	for _, value := range []string{l.ConfigRoot, l.StateRoot, l.CacheRoot, l.WorkspaceRoot, l.LogRoot, l.ThemesRoot, l.ConfigFile} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return ErrInvalidLocation
		}
	}
	if !contained(l.CacheRoot, l.WorkspaceRoot) || !contained(l.ConfigRoot, l.ThemesRoot) || !contained(l.ConfigRoot, l.ConfigFile) {
		return ErrInvalidLocation
	}
	return nil
}

// ConfigPath returns the path to the human-readable TOML configuration file.
func (l Locations) ConfigPath() string {
	return l.ConfigFile
}

func environment(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, raw, ok := splitEnvironment(value)
		if ok {
			result[key] = raw
		}
	}
	return result
}

func splitEnvironment(value string) (string, string, bool) {
	for index := 0; index < len(value); index++ {
		if value[index] == '=' {
			if index == 0 {
				return "", "", false
			}
			return value[:index], value[index+1:], true
		}
	}
	return "", "", false
}

func overrideRoot(environ map[string]string, key, fallback string) (string, error) {
	value, ok := environ[key]
	if !ok || value == "" {
		return fallback, nil
	}
	if !filepath.IsAbs(value) {
		return "", ErrInvalidLocation
	}
	return filepath.Clean(value), nil
}

func contained(root, child string) bool {
	relative, err := filepath.Rel(root, child)
	return err == nil && relative != ".." && relative != "" && relative != "." && relative != string(filepath.Separator)+".." && !isParentRelative(relative)
}

func isParentRelative(relative string) bool {
	return len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator)
}
