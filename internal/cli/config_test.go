package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathDoesNotCreateFilesystemState(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	t.Setenv("NUDGE_CONFIG_HOME", configRoot)

	var output bytes.Buffer
	command := NewRootCommand(BuildInfo{})
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"config", "path"})
	if err := command.Execute(); err != nil {
		t.Fatalf("execute config path: %v", err)
	}
	if output.String() != filepath.Join(configRoot, "config.toml")+"\n" {
		t.Fatalf("config path output = %q", output.String())
	}
	if _, err := os.Stat(configRoot); !os.IsNotExist(err) {
		t.Fatalf("config path created root: stat error = %v", err)
	}
}
