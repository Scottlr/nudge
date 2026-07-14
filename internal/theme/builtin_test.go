package theme

import (
	"strings"
	"testing"
)

func TestBuiltinThemesCoverRolesAndPalettePolicy(t *testing.T) {
	t.Parallel()

	palette := PaletteTokens()
	allowed := map[string]bool{inherit: true}
	for _, value := range palette {
		allowed[value] = true
	}
	for name, builtIn := range Builtins() {
		if err := builtIn.Validate(); err != nil {
			t.Fatalf("theme %q: %v", name, err)
		}
		for _, role := range RequiredRoles() {
			style, ok := builtIn.StyleFor(role)
			if !ok {
				t.Fatalf("theme %q missing role %q", name, role)
			}
			for field, value := range map[string]string{
				"foreground": style.Foreground,
				"background": style.Background,
				"border":     style.Border,
			} {
				if value != "" && !allowed[value] {
					t.Fatalf("theme %q role %q %s uses unapproved value %q", name, role, field, value)
				}
				lower := strings.ToLower(value)
				for _, forbidden := range []string{"blue", "cyan", "teal", "orange", "amber", "copper"} {
					if strings.Contains(lower, forbidden) {
						t.Fatalf("theme %q role %q contains forbidden palette name %q", name, role, forbidden)
					}
				}
			}
		}
	}
}
