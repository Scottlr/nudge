//go:build windows

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

func nativeDefaults(_ map[string]string) (nativeLocationDefaults, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return nativeLocationDefaults{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nativeLocationDefaults{}, err
	}
	configRoot := filepath.Join(config, "nudge")
	stateRoot := filepath.Join(cache, "nudge")
	return nativeLocationDefaults{
		ConfigRoot: configRoot,
		StateRoot:  stateRoot,
		CacheRoot:  stateRoot,
		LogRoot:    filepath.Join(stateRoot, "log"),
	}, nil
}
