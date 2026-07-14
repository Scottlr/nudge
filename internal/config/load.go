package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Scottlr/nudge/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

// Load applies defaults, the protected TOML file, environment overlays, and
// typed CLI overlays atomically. A failed layer never returns a partial config.
func Load(ctx context.Context, locations paths.Locations, environ map[string]string, flags CLIOverrides) (LoadedConfig, error) {
	if ctx == nil {
		return LoadedConfig{}, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return LoadedConfig{}, err
	}
	if locations == (paths.Locations{}) {
		var err error
		locations, err = paths.Resolve(environ)
		if err != nil {
			return LoadedConfig{}, err
		}
	} else if err := locations.Validate(); err != nil {
		return LoadedConfig{}, err
	}
	if environ == nil {
		environ = map[string]string{}
	}

	value := Defaults()
	sources := DefaultSources()
	data, err := paths.ReadProtectedFile(locations.ConfigRoot, "config.toml")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return LoadedConfig{}, fmt.Errorf("%w: configuration file", ErrInvalidConfig)
	}
	if err == nil {
		if err := applyFile(data, &value, sources); err != nil {
			return LoadedConfig{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return LoadedConfig{}, err
	}
	if err := applyEnvironment(environ, &value, sources); err != nil {
		return LoadedConfig{}, err
	}
	if err := applyCLI(flags, &value, sources); err != nil {
		return LoadedConfig{}, err
	}
	if err := value.Validate(); err != nil {
		return LoadedConfig{}, err
	}
	return LoadedConfig{Config: value, Sources: sources}, nil
}

type fileOverlay struct {
	Version     *int                `toml:"version"`
	Review      *reviewOverlay      `toml:"review"`
	Codex       *codexOverlay       `toml:"codex"`
	UI          *uiOverlay          `toml:"ui"`
	Persistence *persistenceOverlay `toml:"persistence"`
	Logging     *loggingOverlay     `toml:"logging"`
}

type reviewOverlay struct {
	DefaultMode              *string `toml:"default_mode"`
	DefaultBaseBranch        *string `toml:"default_base_branch"`
	DiffContextLines         *int    `toml:"diff_context_lines"`
	ShowAllFiles             *bool   `toml:"show_all_files"`
	LargeFileBytes           *int64  `toml:"large_file_bytes"`
	HighlightFileBytes       *int64  `toml:"highlight_file_bytes"`
	FocusedRefreshMaxSeconds *int    `toml:"focused_refresh_max_seconds"`
}

type codexOverlay struct {
	Executable *string `toml:"executable"`
	Model      *string `toml:"model"`
}

type uiOverlay struct {
	Theme         *string `toml:"theme"`
	ReducedMotion *bool   `toml:"reduced_motion"`
	Unicode       *bool   `toml:"unicode"`
}

type persistenceOverlay struct {
	Enabled                *bool `toml:"enabled"`
	StoreAnchorSnippets    *bool `toml:"store_anchor_snippets"`
	WorkspaceRetentionDays *int  `toml:"workspace_retention_days"`
}

type loggingOverlay struct {
	Level *string `toml:"level"`
}

func applyFile(data []byte, value *Config, sources map[string]Source) error {
	var overlay fileOverlay
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&overlay); err != nil {
		return fmt.Errorf("%w: configuration file", ErrInvalidConfig)
	}
	if overlay.Version == nil || *overlay.Version != 1 {
		return ErrUnsupportedSchemaVersion
	}
	value.Version = *overlay.Version
	sources["version"] = SourceFile
	if overlay.Review != nil {
		if field := overlay.Review.DefaultMode; field != nil {
			value.Review.DefaultMode, sources["review.default_mode"] = *field, SourceFile
		}
		if field := overlay.Review.DefaultBaseBranch; field != nil {
			value.Review.DefaultBaseBranch, sources["review.default_base_branch"] = *field, SourceFile
		}
		if field := overlay.Review.DiffContextLines; field != nil {
			value.Review.DiffContextLines, sources["review.diff_context_lines"] = *field, SourceFile
		}
		if field := overlay.Review.ShowAllFiles; field != nil {
			value.Review.ShowAllFiles, sources["review.show_all_files"] = *field, SourceFile
		}
		if field := overlay.Review.LargeFileBytes; field != nil {
			value.Review.LargeFileBytes, sources["review.large_file_bytes"] = *field, SourceFile
		}
		if field := overlay.Review.HighlightFileBytes; field != nil {
			value.Review.HighlightFileBytes, sources["review.highlight_file_bytes"] = *field, SourceFile
		}
		if field := overlay.Review.FocusedRefreshMaxSeconds; field != nil {
			value.Review.FocusedRefreshMaxSeconds, sources["review.focused_refresh_max_seconds"] = *field, SourceFile
		}
	}
	if overlay.Codex != nil {
		if field := overlay.Codex.Executable; field != nil {
			value.Codex.Executable, sources["codex.executable"] = *field, SourceFile
		}
		if field := overlay.Codex.Model; field != nil {
			value.Codex.Model, sources["codex.model"] = *field, SourceFile
		}
	}
	if overlay.UI != nil {
		if field := overlay.UI.Theme; field != nil {
			value.UI.Theme, sources["ui.theme"] = *field, SourceFile
		}
		if field := overlay.UI.ReducedMotion; field != nil {
			value.UI.ReducedMotion, sources["ui.reduced_motion"] = *field, SourceFile
		}
		if field := overlay.UI.Unicode; field != nil {
			value.UI.Unicode, sources["ui.unicode"] = *field, SourceFile
		}
	}
	if overlay.Persistence != nil {
		if field := overlay.Persistence.Enabled; field != nil {
			value.Persistence.Enabled, sources["persistence.enabled"] = *field, SourceFile
		}
		if field := overlay.Persistence.StoreAnchorSnippets; field != nil {
			value.Persistence.StoreAnchorSnippets, sources["persistence.store_anchor_snippets"] = *field, SourceFile
		}
		if field := overlay.Persistence.WorkspaceRetentionDays; field != nil {
			value.Persistence.WorkspaceRetentionDays, sources["persistence.workspace_retention_days"] = *field, SourceFile
		}
	}
	if overlay.Logging != nil && overlay.Logging.Level != nil {
		value.Logging.Level, sources["logging.level"] = *overlay.Logging.Level, SourceFile
	}
	return nil
}

func applyEnvironment(environ map[string]string, value *Config, sources map[string]Source) error {
	keys := make([]string, 0, len(environ))
	for key := range environ {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := environ[key]
		switch key {
		case "NUDGE_REVIEW_DEFAULT_MODE":
			value.Review.DefaultMode, sources["review.default_mode"] = raw, SourceEnv
		case "NUDGE_REVIEW_DEFAULT_BASE_BRANCH":
			value.Review.DefaultBaseBranch, sources["review.default_base_branch"] = raw, SourceEnv
		case "NUDGE_REVIEW_DIFF_CONTEXT_LINES":
			parsed, err := parseInt(raw)
			if err != nil {
				return invalidField("review.diff_context_lines")
			}
			value.Review.DiffContextLines, sources["review.diff_context_lines"] = parsed, SourceEnv
		case "NUDGE_REVIEW_SHOW_ALL_FILES":
			parsed, err := parseBool(raw)
			if err != nil {
				return invalidField("review.show_all_files")
			}
			value.Review.ShowAllFiles, sources["review.show_all_files"] = parsed, SourceEnv
		case "NUDGE_REVIEW_LARGE_FILE_BYTES":
			parsed, err := parseInt64(raw)
			if err != nil {
				return invalidField("review.large_file_bytes")
			}
			value.Review.LargeFileBytes, sources["review.large_file_bytes"] = parsed, SourceEnv
		case "NUDGE_REVIEW_HIGHLIGHT_FILE_BYTES":
			parsed, err := parseInt64(raw)
			if err != nil {
				return invalidField("review.highlight_file_bytes")
			}
			value.Review.HighlightFileBytes, sources["review.highlight_file_bytes"] = parsed, SourceEnv
		case "NUDGE_REVIEW_FOCUSED_REFRESH_MAX_SECONDS":
			parsed, err := parseInt(raw)
			if err != nil {
				return invalidField("review.focused_refresh_max_seconds")
			}
			value.Review.FocusedRefreshMaxSeconds, sources["review.focused_refresh_max_seconds"] = parsed, SourceEnv
		case "NUDGE_CODEX_EXECUTABLE":
			value.Codex.Executable, sources["codex.executable"] = raw, SourceEnv
		case "NUDGE_CODEX_MODEL":
			value.Codex.Model, sources["codex.model"] = raw, SourceEnv
		case "NUDGE_UI_THEME":
			value.UI.Theme, sources["ui.theme"] = raw, SourceEnv
		case "NUDGE_UI_REDUCED_MOTION":
			parsed, err := parseBool(raw)
			if err != nil {
				return invalidField("ui.reduced_motion")
			}
			value.UI.ReducedMotion, sources["ui.reduced_motion"] = parsed, SourceEnv
		case "NUDGE_UI_UNICODE":
			parsed, err := parseBool(raw)
			if err != nil {
				return invalidField("ui.unicode")
			}
			value.UI.Unicode, sources["ui.unicode"] = parsed, SourceEnv
		case "NUDGE_PERSISTENCE_ENABLED":
			parsed, err := parseBool(raw)
			if err != nil {
				return invalidField("persistence.enabled")
			}
			value.Persistence.Enabled, sources["persistence.enabled"] = parsed, SourceEnv
		case "NUDGE_PERSISTENCE_STORE_ANCHOR_SNIPPETS":
			parsed, err := parseBool(raw)
			if err != nil {
				return invalidField("persistence.store_anchor_snippets")
			}
			value.Persistence.StoreAnchorSnippets, sources["persistence.store_anchor_snippets"] = parsed, SourceEnv
		case "NUDGE_PERSISTENCE_WORKSPACE_RETENTION_DAYS":
			parsed, err := parseInt(raw)
			if err != nil {
				return invalidField("persistence.workspace_retention_days")
			}
			value.Persistence.WorkspaceRetentionDays, sources["persistence.workspace_retention_days"] = parsed, SourceEnv
		case "NUDGE_LOGGING_LEVEL":
			value.Logging.Level, sources["logging.level"] = raw, SourceEnv
		default:
			if strings.HasPrefix(key, "NUDGE_REVIEW_") || strings.HasPrefix(key, "NUDGE_CODEX_") || strings.HasPrefix(key, "NUDGE_UI_") || strings.HasPrefix(key, "NUDGE_PERSISTENCE_") || strings.HasPrefix(key, "NUDGE_LOGGING_") {
				return fmt.Errorf("%w: %s", ErrUnknownField, strings.ToLower(key))
			}
		}
	}
	return nil
}

func applyCLI(flags CLIOverrides, value *Config, sources map[string]Source) error {
	if flags.DefaultMode != nil {
		value.Review.DefaultMode, sources["review.default_mode"] = *flags.DefaultMode, SourceCLI
	}
	if flags.DefaultBaseBranch != nil {
		value.Review.DefaultBaseBranch, sources["review.default_base_branch"] = *flags.DefaultBaseBranch, SourceCLI
	}
	if flags.DiffContextLines != nil {
		value.Review.DiffContextLines, sources["review.diff_context_lines"] = *flags.DiffContextLines, SourceCLI
	}
	if flags.ShowAllFiles != nil {
		value.Review.ShowAllFiles, sources["review.show_all_files"] = *flags.ShowAllFiles, SourceCLI
	}
	if flags.LargeFileBytes != nil {
		value.Review.LargeFileBytes, sources["review.large_file_bytes"] = *flags.LargeFileBytes, SourceCLI
	}
	if flags.HighlightFileBytes != nil {
		value.Review.HighlightFileBytes, sources["review.highlight_file_bytes"] = *flags.HighlightFileBytes, SourceCLI
	}
	if flags.FocusedRefreshMaxSeconds != nil {
		value.Review.FocusedRefreshMaxSeconds, sources["review.focused_refresh_max_seconds"] = *flags.FocusedRefreshMaxSeconds, SourceCLI
	}
	if flags.CodexExecutable != nil {
		value.Codex.Executable, sources["codex.executable"] = *flags.CodexExecutable, SourceCLI
	}
	if flags.CodexModel != nil {
		value.Codex.Model, sources["codex.model"] = *flags.CodexModel, SourceCLI
	}
	if flags.UITheme != nil {
		value.UI.Theme, sources["ui.theme"] = *flags.UITheme, SourceCLI
	}
	if flags.ReducedMotion != nil {
		value.UI.ReducedMotion, sources["ui.reduced_motion"] = *flags.ReducedMotion, SourceCLI
	}
	if flags.Unicode != nil {
		value.UI.Unicode, sources["ui.unicode"] = *flags.Unicode, SourceCLI
	}
	if flags.PersistenceEnabled != nil {
		value.Persistence.Enabled, sources["persistence.enabled"] = *flags.PersistenceEnabled, SourceCLI
	}
	if flags.StoreAnchorSnippets != nil {
		value.Persistence.StoreAnchorSnippets, sources["persistence.store_anchor_snippets"] = *flags.StoreAnchorSnippets, SourceCLI
	}
	if flags.WorkspaceRetentionDays != nil {
		value.Persistence.WorkspaceRetentionDays, sources["persistence.workspace_retention_days"] = *flags.WorkspaceRetentionDays, SourceCLI
	}
	if flags.LoggingLevel != nil {
		value.Logging.Level, sources["logging.level"] = *flags.LoggingLevel, SourceCLI
	}
	return nil
}

func parseBool(value string) (bool, error) {
	return strconv.ParseBool(value)
}

func parseInt(value string) (int, error) {
	parsed, err := strconv.ParseInt(value, 10, 0)
	return int(parsed), err
}

func parseInt64(value string) (int64, error) {
	return strconv.ParseInt(value, 10, 64)
}
