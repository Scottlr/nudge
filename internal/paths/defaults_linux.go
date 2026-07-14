//go:build linux

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
	config := environ["XDG_CONFIG_HOME"]
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	state := environ["XDG_STATE_HOME"]
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	cache := environ["XDG_CACHE_HOME"]
	if cache == "" {
		cache = filepath.Join(home, ".cache")
	}
	return nativeLocationDefaults{
		ConfigRoot: filepath.Join(config, "nudge"),
		StateRoot:  filepath.Join(state, "nudge"),
		CacheRoot:  filepath.Join(cache, "nudge"),
		LogRoot:    filepath.Join(state, "nudge", "log"),
	}, nil
}
