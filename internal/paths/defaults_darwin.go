//go:build darwin

package paths

import (
	"os"
	"path/filepath"
)

type nativeLocationDefaults struct {
	ConfigRoot string
	StateRoot  string
	CacheRoot  string
	LogRoot    string
}

func nativeDefaults(environ map[string]string) (nativeLocationDefaults, error) {
	home := environ["HOME"]
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nativeLocationDefaults{}, err
		}
	}
	applicationSupport := filepath.Join(home, "Library", "Application Support", "nudge")
	state := applicationSupport
	cache := filepath.Join(home, "Library", "Caches", "nudge")
	return nativeLocationDefaults{
		ConfigRoot: applicationSupport,
		StateRoot:  state,
		CacheRoot:  cache,
		LogRoot:    filepath.Join(state, "log"),
	}, nil
}
