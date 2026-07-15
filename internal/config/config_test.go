package config

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Scottlr/nudge/internal/paths"
)

func TestLoadPrecedenceAndSources(t *testing.T) {
	root := t.TempDir()
	locations, err := testLocations(root)
	if err != nil {
		t.Fatalf("test locations: %v", err)
	}
	if err := paths.EnsurePrivateDir(locations.ConfigRoot); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	writeConfig(t, locations, "version = 1\n\n[review]\ndiff_context_lines = 7\n\n[codex]\nmodel = \"from-file\"\n")

	flagMode := "branch"
	loaded, err := Load(context.Background(), locations, map[string]string{
		"NUDGE_REVIEW_DIFF_CONTEXT_LINES":            "8",
		"NUDGE_CODEX_MODEL":                          "from-env",
		"NUDGE_PERSISTENCE_ANCHOR_EXCERPT_RETENTION": "session",
	}, CLIOverrides{DefaultMode: &flagMode})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Config.Review.DefaultMode != "branch" || loaded.Config.Review.DiffContextLines != 8 || loaded.Config.Codex.Model != "from-env" {
		t.Fatalf("loaded config = %+v", loaded.Config)
	}
	if loaded.Config.Persistence.AnchorExcerptRetention != "session" {
		t.Fatalf("anchor excerpt retention = %q", loaded.Config.Persistence.AnchorExcerptRetention)
	}
	if loaded.Sources["review.default_mode"] != SourceCLI || loaded.Sources["review.diff_context_lines"] != SourceEnv || loaded.Sources["codex.model"] != SourceEnv {
		t.Fatalf("source metadata = %#v", loaded.Sources)
	}
}

func TestPrivacyPolicyHonorsAnchorExcerptOptOut(t *testing.T) {
	value := Defaults()
	value.Persistence.StoreAnchorSnippets = false
	policy, err := value.PrivacyPolicy()
	if err != nil {
		t.Fatalf("PrivacyPolicy() error = %v", err)
	}
	if policy.StoresAnchorExcerpt() || policy.SupportsReattachment() {
		t.Fatal("anchor excerpt opt-out still permits durable retention")
	}
}

func TestConfigRejectsUnknownAnchorExcerptRetention(t *testing.T) {
	value := Defaults()
	value.Persistence.AnchorExcerptRetention = "forever"
	if err := value.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid anchor excerpt retention error = %v", err)
	}
}

func TestLoadRejectsUnknownAndUnsupportedFileFields(t *testing.T) {
	tests := []struct {
		name string
		data string
		want error
	}{
		{name: "unknown field", data: "version = 1\n\n[review]\nretired = true\n", want: ErrInvalidConfig},
		{name: "unsupported version", data: "version = 2\n", want: ErrUnsupportedSchemaVersion},
		{name: "invalid bound", data: "version = 1\n\n[review]\nfocused_refresh_max_seconds = 31\n", want: ErrInvalidConfig},
		{name: "raised content hard maximum", data: "version = 1\n\n[review]\nlarge_file_bytes = 2000001\n", want: ErrInvalidConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			locations, err := testLocations(root)
			if err != nil {
				t.Fatalf("test locations: %v", err)
			}
			if err := paths.EnsurePrivateDir(locations.ConfigRoot); err != nil {
				t.Fatalf("EnsurePrivateDir() error = %v", err)
			}
			writeConfig(t, locations, test.data)
			_, err = Load(context.Background(), locations, nil, CLIOverrides{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Load() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestConfigValidationRejectsContradictoryPersistence(t *testing.T) {
	value := Defaults()
	value.Persistence.Enabled = false
	if err := value.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("contradictory persistence error = %v", err)
	}
}

func TestConfigValidationRejectsThemePathIdentity(t *testing.T) {
	value := Defaults()
	value.UI.Theme = "../hostile"
	if err := value.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unsafe theme identity error = %v", err)
	}
}

func testLocations(root string) (paths.Locations, error) {
	return paths.Resolve(map[string]string{
		"NUDGE_CONFIG_HOME": filepath.Join(root, "config"),
		"NUDGE_STATE_HOME":  filepath.Join(root, "state"),
		"NUDGE_CACHE_HOME":  filepath.Join(root, "cache"),
		"NUDGE_LOG_HOME":    filepath.Join(root, "logs"),
	})
}

func writeConfig(t *testing.T, locations paths.Locations, data string) {
	t.Helper()
	file, err := paths.OpenProtectedFile(locations.ConfigRoot, "config.toml", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("OpenProtectedFile() error = %v", err)
	}
	if _, err := io.WriteString(file, data); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}
}
