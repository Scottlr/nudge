package theme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Scottlr/nudge/internal/paths"
	"github.com/pelletier/go-toml/v2"
)

const (
	// SchemaVersion is the first version of the protected user-theme format.
	SchemaVersion = 1
	// MaxThemeBytes is the T070 theme-document bound applied before decoding.
	MaxThemeBytes = 256 * 1024
	// MaxThemeEntries is the T070 structured-document entry bound.
	MaxThemeEntries = 512
	// MaxThemeScalarBytes is the T070 scalar bound for one theme value.
	MaxThemeScalarBytes = 8 * 1024
	// MaxThemeDepth is the T070 structured-document nesting bound.
	MaxThemeDepth = 8
)

// Source identifies where the resolved theme came from.
type Source string

const (
	SourceBuiltin  Source = "builtin"
	SourceUser     Source = "user"
	SourceFallback Source = "fallback"
)

// FailureCode is a stable, payload-free reason for a fallback. It is safe to
// expose in health output without exposing theme content or filesystem errors.
type FailureCode string

const (
	FailureNone               FailureCode = ""
	FailureInvalidName        FailureCode = "invalid_name"
	FailureNotFound           FailureCode = "not_found"
	FailureProtectedPath      FailureCode = "protected_path"
	FailureOversize           FailureCode = "oversize"
	FailureDecode             FailureCode = "decode"
	FailureMissingVersion     FailureCode = "missing_version"
	FailureUnsupportedVersion FailureCode = "unsupported_version"
	FailureUnknownRole        FailureCode = "unknown_role"
	FailureInvalidColor       FailureCode = "invalid_color"
	FailureInvalidGlyph       FailureCode = "invalid_glyph"
	FailureInvalidValue       FailureCode = "invalid_value"
	FailureLimit              FailureCode = "limit_exceeded"
	FailureValidation         FailureCode = "validation"
)

// Health is the safe theme evidence exposed to presentation and diagnostics.
// It intentionally excludes raw theme text, paths, and decoder errors.
type Health struct {
	ThemeID       string
	SchemaVersion int
	Source        Source
	Failure       FailureCode
}

// Healthy reports whether resolution used the requested source without a
// fallback.
func (h Health) Healthy() bool {
	return h.Failure == FailureNone && h.Source != SourceFallback
}

// Resolution contains the complete theme and safe health evidence for that
// selection. A user-theme failure always returns a usable built-in theme.
type Resolution struct {
	Theme  Theme
	Health Health
}

type themeDocument struct {
	Version     *int                   `toml:"version"`
	Name        *string                `toml:"name"`
	Syntax      *string                `toml:"syntax_style"`
	Roles       map[string]styleConfig `toml:"roles"`
	Glyphs      map[string]string      `toml:"glyphs"`
	ASCIIGlyphs map[string]string      `toml:"ascii_glyphs"`
}

type styleConfig struct {
	Foreground *string `toml:"foreground"`
	Background *string `toml:"background"`
	Border     *string `toml:"border"`
	Bold       *bool   `toml:"bold"`
	Italic     *bool   `toml:"italic"`
	Underline  *bool   `toml:"underline"`
}

// Load resolves a built-in or protected user theme. It does not create
// directories, probe terminal capabilities, or mutate the selected theme.
func Load(ctx context.Context, locations paths.Locations, name string, policy RenderPolicy) (Resolution, error) {
	if ctx == nil {
		return Resolution{}, fmt.Errorf("%w: nil context", ErrInvalidTheme)
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	requested := strings.TrimSpace(name)
	if requested == "" || requested == "default" || requested == "terminal-default" {
		requested = "terminal"
	}
	if builtIn, ok := Builtin(requested); ok {
		return Resolution{Theme: builtIn.WithPolicy(normalizePolicy(policy)), Health: Health{ThemeID: builtIn.Name, SchemaVersion: SchemaVersion, Source: SourceBuiltin}}, nil
	}
	if !validIdentifier(requested) {
		return fallbackResolution(FailureInvalidName, 0, policy), nil
	}
	if err := locations.Validate(); err != nil {
		return fallbackResolution(FailureProtectedPath, 0, policy), nil
	}
	data, err := paths.ReadProtectedFileBounded(locations.ThemesRoot, requested+".toml", MaxThemeBytes)
	if err != nil {
		code := FailureProtectedPath
		switch {
		case errors.Is(err, os.ErrNotExist):
			code = FailureNotFound
		case errors.Is(err, paths.ErrProtectedTooLarge):
			code = FailureOversize
		}
		return fallbackResolution(code, 0, policy), nil
	}
	if len(data) > MaxThemeBytes {
		return fallbackResolution(FailureOversize, 0, policy), nil
	}
	if !withinTableDepth(data) {
		return fallbackResolution(FailureLimit, 0, policy), nil
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	var document themeDocument
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fallbackResolution(FailureDecode, 0, policy), nil
	}
	if !withinDocumentLimits(document) {
		return fallbackResolution(FailureLimit, documentVersion(document), policy), nil
	}
	if document.Version == nil {
		return fallbackResolution(FailureMissingVersion, 0, policy), nil
	}
	if *document.Version != SchemaVersion {
		return fallbackResolution(FailureUnsupportedVersion, *document.Version, policy), nil
	}
	base := BuiltinTerminalDefault()
	if document.Name != nil && !validIdentifier(*document.Name) {
		return fallbackResolution(FailureInvalidValue, *document.Version, policy), nil
	}
	if document.Syntax != nil {
		if !validSafeText(*document.Syntax, 128) {
			return fallbackResolution(FailureInvalidValue, *document.Version, policy), nil
		}
		base.SyntaxStyle = *document.Syntax
	}
	for rawRole, override := range document.Roles {
		role := Role(rawRole)
		if !containsRole(role) {
			return fallbackResolution(FailureUnknownRole, *document.Version, policy), nil
		}
		style, ok := base.Roles[role]
		if !ok {
			return fallbackResolution(FailureUnknownRole, *document.Version, policy), nil
		}
		if override.Foreground != nil {
			if !validOverrideColor(*override.Foreground) {
				return fallbackResolution(FailureInvalidColor, *document.Version, policy), nil
			}
			style.Foreground = *override.Foreground
		}
		if override.Background != nil {
			if !validOverrideColor(*override.Background) {
				return fallbackResolution(FailureInvalidColor, *document.Version, policy), nil
			}
			style.Background = *override.Background
		}
		if override.Border != nil {
			if !validOverrideColor(*override.Border) {
				return fallbackResolution(FailureInvalidColor, *document.Version, policy), nil
			}
			style.Border = *override.Border
		}
		if override.Bold != nil {
			style.Bold = *override.Bold
		}
		if override.Italic != nil {
			style.Italic = *override.Italic
		}
		if override.Underline != nil {
			style.Underline = *override.Underline
		}
		base.Roles[role] = style
	}
	if err := applyGlyphOverrides(&base, document.Glyphs, false); err != nil {
		return fallbackResolution(FailureInvalidGlyph, *document.Version, policy), nil
	}
	if err := applyGlyphOverrides(&base, document.ASCIIGlyphs, true); err != nil {
		return fallbackResolution(FailureInvalidGlyph, *document.Version, policy), nil
	}
	base.Name = requested
	base = base.WithPolicy(normalizePolicy(policy))
	if err := base.Validate(); err != nil {
		return fallbackResolution(FailureValidation, *document.Version, policy), nil
	}
	return Resolution{Theme: base, Health: Health{ThemeID: requested, SchemaVersion: *document.Version, Source: SourceUser}}, nil
}

// Resolve is a descriptive alias for Load used by composition code.
func Resolve(ctx context.Context, locations paths.Locations, name string, policy RenderPolicy) (Resolution, error) {
	return Load(ctx, locations, name, policy)
}

func fallbackResolution(code FailureCode, schemaVersion int, policy RenderPolicy) Resolution {
	base := BuiltinTerminalDefault()
	base = base.WithPolicy(normalizePolicy(policy))
	return Resolution{Theme: base, Health: Health{ThemeID: base.Name, SchemaVersion: schemaVersion, Source: SourceFallback, Failure: code}}
}

func normalizePolicy(policy RenderPolicy) RenderPolicy {
	if !policy.Explicit {
		return DefaultRenderPolicy()
	}
	return policy
}

func containsRole(role Role) bool {
	for _, required := range requiredRoles {
		if required == role {
			return true
		}
	}
	return false
}

func documentVersion(document themeDocument) int {
	if document.Version == nil {
		return 0
	}
	return *document.Version
}

func withinDocumentLimits(document themeDocument) bool {
	entries := 2 + len(document.Roles) + len(document.Glyphs) + len(document.ASCIIGlyphs)
	if entries > MaxThemeEntries {
		return false
	}
	if document.Name != nil && len(*document.Name) > MaxThemeScalarBytes || document.Syntax != nil && len(*document.Syntax) > MaxThemeScalarBytes {
		return false
	}
	for _, value := range document.Glyphs {
		if len(value) > MaxThemeScalarBytes {
			return false
		}
	}
	for _, value := range document.ASCIIGlyphs {
		if len(value) > MaxThemeScalarBytes {
			return false
		}
	}
	for _, value := range document.Roles {
		for _, scalar := range []*string{value.Foreground, value.Background, value.Border} {
			if scalar != nil && len(*scalar) > MaxThemeScalarBytes {
				return false
			}
		}
	}
	return true
}

func withinTableDepth(data []byte) bool {
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if len(line) < 2 || line[0] != '[' || line[len(line)-1] != ']' {
			continue
		}
		line = strings.Trim(line, "[]")
		line = strings.TrimPrefix(line, "[")
		line = strings.TrimSuffix(line, "]")
		if line == "" {
			continue
		}
		if strings.Count(line, ".")+1 > MaxThemeDepth {
			return false
		}
	}
	return true
}

func applyGlyphOverrides(target *Theme, values map[string]string, ascii bool) error {
	if target == nil {
		return ErrInvalidTheme
	}
	for rawPurpose, value := range values {
		purpose := GlyphPurpose(rawPurpose)
		if !containsGlyph(purpose) || !validGlyph(value, ascii) {
			return ErrInvalidTheme
		}
		if ascii {
			target.ASCIIGlyphs[purpose] = value
		} else {
			target.Glyphs[purpose] = value
		}
	}
	return nil
}

func containsGlyph(purpose GlyphPurpose) bool {
	for _, required := range requiredGlyphs {
		if required == purpose {
			return true
		}
	}
	return false
}
