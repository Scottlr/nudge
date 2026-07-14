// Package config owns Nudge's schema-v1 configuration values and their
// precedence metadata. It does not store credentials or provider protocol.
package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

var (
	// ErrInvalidConfig reports a schema or value validation failure.
	ErrInvalidConfig = errors.New("invalid Nudge configuration")
	// ErrUnsupportedSchemaVersion reports a configuration version this binary cannot interpret.
	ErrUnsupportedSchemaVersion = errors.New("unsupported Nudge configuration version")
	// ErrUnknownField reports a misspelled or retired configuration field.
	ErrUnknownField = errors.New("unknown Nudge configuration field")
)

// Source identifies the winning configuration layer for one field.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
	SourceEnv     Source = "environment"
	SourceCLI     Source = "cli"
)

// Config is the complete schema-v1 configuration.
type Config struct {
	Version     int               `toml:"version"`
	Review      ReviewConfig      `toml:"review"`
	Codex       CodexConfig       `toml:"codex"`
	UI          UIConfig          `toml:"ui"`
	Persistence PersistenceConfig `toml:"persistence"`
	Logging     LoggingConfig     `toml:"logging"`
}

// ReviewConfig controls target defaults and bounded local review rendering.
type ReviewConfig struct {
	DefaultMode              string `toml:"default_mode"`
	DefaultBaseBranch        string `toml:"default_base_branch"`
	DiffContextLines         int    `toml:"diff_context_lines"`
	ShowAllFiles             bool   `toml:"show_all_files"`
	LargeFileBytes           int64  `toml:"large_file_bytes"`
	HighlightFileBytes       int64  `toml:"highlight_file_bytes"`
	FocusedRefreshMaxSeconds int    `toml:"focused_refresh_max_seconds"`
}

// CodexConfig contains only executable/model selection. Authentication and
// credentials remain owned by Codex and are never configuration fields here.
type CodexConfig struct {
	Executable string `toml:"executable"`
	Model      string `toml:"model"`
}

// UIConfig controls currently supported presentation preferences.
type UIConfig struct {
	Theme         string `toml:"theme"`
	ReducedMotion bool   `toml:"reduced_motion"`
	Unicode       bool   `toml:"unicode"`
}

// PersistenceConfig controls local review metadata retention.
type PersistenceConfig struct {
	Enabled                bool `toml:"enabled"`
	StoreAnchorSnippets    bool `toml:"store_anchor_snippets"`
	WorkspaceRetentionDays int  `toml:"workspace_retention_days"`
}

// LoggingConfig controls the bounded operational log level.
type LoggingConfig struct {
	Level string `toml:"level"`
}

// CLIOverrides contains typed command-line overlay values. Nil fields do not
// participate in precedence.
type CLIOverrides struct {
	DefaultMode              *string
	DefaultBaseBranch        *string
	DiffContextLines         *int
	ShowAllFiles             *bool
	LargeFileBytes           *int64
	HighlightFileBytes       *int64
	FocusedRefreshMaxSeconds *int
	CodexExecutable          *string
	CodexModel               *string
	UITheme                  *string
	ReducedMotion            *bool
	Unicode                  *bool
	PersistenceEnabled       *bool
	StoreAnchorSnippets      *bool
	WorkspaceRetentionDays   *int
	LoggingLevel             *string
}

// LoadedConfig is a validated configuration plus source metadata keyed by
// stable TOML field paths.
type LoadedConfig struct {
	Config  Config
	Sources map[string]Source
}

// Defaults returns the schema-v1 defaults from the product design.
func Defaults() Config {
	return Config{
		Version: 1,
		Review: ReviewConfig{
			DefaultMode:              "local",
			DefaultBaseBranch:        "main",
			DiffContextLines:         5,
			LargeFileBytes:           2_000_000,
			HighlightFileBytes:       1_000_000,
			FocusedRefreshMaxSeconds: 30,
		},
		Codex: CodexConfig{Executable: "codex"},
		UI: UIConfig{
			Theme:   "terminal",
			Unicode: true,
		},
		Persistence: PersistenceConfig{
			Enabled:                true,
			StoreAnchorSnippets:    true,
			WorkspaceRetentionDays: 14,
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

// DefaultSources returns source metadata for every configurable field.
func DefaultSources() map[string]Source {
	return map[string]Source{
		"version":                              SourceDefault,
		"review.default_mode":                  SourceDefault,
		"review.default_base_branch":           SourceDefault,
		"review.diff_context_lines":            SourceDefault,
		"review.show_all_files":                SourceDefault,
		"review.large_file_bytes":              SourceDefault,
		"review.highlight_file_bytes":          SourceDefault,
		"review.focused_refresh_max_seconds":   SourceDefault,
		"codex.executable":                     SourceDefault,
		"codex.model":                          SourceDefault,
		"ui.theme":                             SourceDefault,
		"ui.reduced_motion":                    SourceDefault,
		"ui.unicode":                           SourceDefault,
		"persistence.enabled":                  SourceDefault,
		"persistence.store_anchor_snippets":    SourceDefault,
		"persistence.workspace_retention_days": SourceDefault,
		"logging.level":                        SourceDefault,
	}
}

// Validate checks schema version, bounds, and contradictory settings.
func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("%w: %d", ErrUnsupportedSchemaVersion, c.Version)
	}
	if c.Review.DefaultMode != "local" && c.Review.DefaultMode != "commit" && c.Review.DefaultMode != "branch" {
		return invalidField("review.default_mode")
	}
	if !validConfigText(c.Review.DefaultBaseBranch) || c.Review.DefaultBaseBranch == "" || c.Review.DiffContextLines < 0 || c.Review.DiffContextLines > 1000 {
		return invalidField("review")
	}
	if c.Review.LargeFileBytes <= 0 || c.Review.HighlightFileBytes <= 0 || c.Review.HighlightFileBytes > c.Review.LargeFileBytes {
		return invalidField("review.file_limits")
	}
	if c.Review.FocusedRefreshMaxSeconds < 1 || c.Review.FocusedRefreshMaxSeconds > 30 {
		return invalidField("review.focused_refresh_max_seconds")
	}
	if !validConfigText(c.Codex.Executable) || c.Codex.Executable == "" || (c.Codex.Model != "" && !validConfigText(c.Codex.Model)) {
		return invalidField("codex")
	}
	if !validConfigText(c.UI.Theme) || c.UI.Theme == "" {
		return invalidField("ui.theme")
	}
	if c.Persistence.WorkspaceRetentionDays < 1 || c.Persistence.WorkspaceRetentionDays > 14 || (!c.Persistence.Enabled && c.Persistence.StoreAnchorSnippets) {
		return invalidField("persistence")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return invalidField("logging.level")
	}
	return nil
}

func invalidField(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, field)
}

func validConfigText(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return !strings.ContainsRune(value, '\uFFFD')
}
