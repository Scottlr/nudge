package theme

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scottlr/nudge/internal/paths"
	"github.com/charmbracelet/x/ansi"
)

func TestBuiltinsCoverRolesAndRenderPolicies(t *testing.T) {
	for name, value := range Builtins() {
		if err := value.Validate(); err != nil {
			t.Fatalf("theme %q: %v", name, err)
		}
		for _, role := range RequiredRoles() {
			if _, ok := value.StyleFor(role); !ok {
				t.Fatalf("theme %q missing role %q", name, role)
			}
		}
	}
	if _, ok := BuiltinHighContrast().StyleFor(RoleOverlayTitle); !ok {
		t.Fatal("high-contrast theme is incomplete")
	}

	monochrome := BuiltinDark().WithPolicy(MonochromeRenderPolicy())
	style, ok := monochrome.StyleFor(RoleFocus)
	if !ok || style != (Style{}) || strings.Contains(style.Lipgloss().Render("focus"), "\x1b[") {
		t.Fatalf("monochrome focus style retained SGR: %#v", style)
	}
	ascii := BuiltinDark().WithPolicy(ASCIIRenderPolicy())
	rail := ascii.Glyph(GlyphMarkerRail)
	ellipsis := ascii.Glyph(GlyphEllipsis)
	if rail != "|" || ellipsis != "..." {
		t.Fatalf("ASCII glyphs = %q, %q", rail, ellipsis)
	}
	for _, field := range []string{ascii.Border().Top, ascii.Border().Bottom, ascii.Border().Left, ascii.Border().Right, ascii.Border().TopLeft, ascii.Border().TopRight, ascii.Border().BottomLeft, ascii.Border().BottomRight} {
		if ansi.StringWidth(field) > 1 || strings.ContainsAny(field, "│─┌┐└┘") {
			t.Fatalf("ASCII border contains non-ASCII geometry: %q", field)
		}
	}
}

func TestLoadProtectedUserThemeAndFallbackHealth(t *testing.T) {
	locations := testThemeLocations(t)
	writeTheme(t, locations, "reviewer.toml", "version = 1\nname = \"reviewer\"\nsyntax_style = \"nudge-reviewer\"\n\n[roles.focus]\nforeground = \"#123456\"\nbold = false\n\n[glyphs]\nmarker_rail = \"!\"\n\n[ascii_glyphs]\nellipsis = \"..\"\n")

	resolved, err := Load(context.Background(), locations, "reviewer", ASCIIRenderPolicy())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if resolved.Health.Source != SourceUser || resolved.Health.Failure != FailureNone || resolved.Health.ThemeID != "reviewer" || resolved.Health.SchemaVersion != SchemaVersion {
		t.Fatalf("user theme health = %#v", resolved.Health)
	}
	if got, _ := resolved.Theme.StyleFor(RoleFocus); got.Foreground != "#123456" || got.Bold {
		t.Fatalf("user focus override = %#v", got)
	}
	if got := resolved.Theme.Glyph(GlyphMarkerRail); got != "|" {
		t.Fatalf("ASCII policy ignored for user theme: %q", got)
	}
	if got := resolved.Theme.Glyph(GlyphEllipsis); got != ".." {
		t.Fatalf("user ASCII glyph override = %q", got)
	}

	tests := []struct {
		name string
		id   string
		data string
		code FailureCode
	}{
		{name: "unknown role", id: "unknown_role", data: "version = 1\n[roles.not_a_role]\nforeground = \"#123456\"\n", code: FailureUnknownRole},
		{name: "invalid color", id: "invalid_color", data: "version = 1\n[roles.focus]\nforeground = \"not-a-color\"\n", code: FailureInvalidColor},
		{name: "unsafe glyph", id: "unsafe_glyph", data: "version = 1\n[glyphs]\nmarker_rail = \"\\u001b[31m\"\n", code: FailureInvalidGlyph},
		{name: "future version", id: "future_version", data: "version = 99\n", code: FailureUnsupportedVersion},
		{name: "duplicate version", id: "duplicate_version", data: "version = 1\nversion = 1\n", code: FailureDecode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeTheme(t, locations, test.id+".toml", test.data)
			resolved, err := Load(context.Background(), locations, test.id, DefaultRenderPolicy())
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if resolved.Health.Source != SourceFallback || resolved.Health.Failure != test.code || resolved.Theme.Validate() != nil {
				t.Fatalf("fallback = %#v", resolved)
			}
			if strings.Contains(resolved.Theme.Name, "\\x1b") || strings.Contains(string(resolved.Health.Failure), "not-a-color") {
				t.Fatal("untrusted theme content entered fallback evidence")
			}
		})
	}

	writeTheme(t, locations, "oversize.toml", strings.Repeat("x", MaxThemeBytes+1))
	resolved, err = Load(context.Background(), locations, "oversize", DefaultRenderPolicy())
	if err != nil || resolved.Health.Failure != FailureOversize {
		t.Fatalf("oversize fallback = %#v, err = %v", resolved, err)
	}

	resolved, err = Load(context.Background(), locations, "../hostile", DefaultRenderPolicy())
	if err != nil || resolved.Health.Failure != FailureInvalidName || resolved.Health.ThemeID != "terminal" {
		t.Fatalf("hostile-name fallback = %#v, err = %v", resolved, err)
	}
}

func testThemeLocations(t *testing.T) paths.Locations {
	t.Helper()
	root := t.TempDir()
	locations, err := paths.Resolve(map[string]string{
		"NUDGE_CONFIG_HOME": filepath.Join(root, "config"),
		"NUDGE_STATE_HOME":  filepath.Join(root, "state"),
		"NUDGE_CACHE_HOME":  filepath.Join(root, "cache"),
		"NUDGE_LOG_HOME":    filepath.Join(root, "logs"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(locations.ConfigRoot); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(locations.ThemesRoot); err != nil {
		t.Fatal(err)
	}
	return locations
}

func writeTheme(t *testing.T, locations paths.Locations, name, data string) {
	t.Helper()
	file, err := paths.OpenProtectedFile(locations.ThemesRoot, name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
