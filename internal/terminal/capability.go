// Package terminal resolves conservative, run-scoped terminal presentation
// capabilities without changing workflow or action policy.
package terminal

import (
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/colorprofile"
)

const (
	maxEnvironmentEntries = 64
	maxEnvironmentValue   = 128
)

// ColorCapability is Nudge's product-neutral color capability.
type ColorCapability uint8

const (
	// ColorNone disables color styling.
	ColorNone ColorCapability = iota
	// ColorUnknown means that detection did not provide a supported profile.
	ColorUnknown
	// ColorBasic supports the terminal's basic ANSI colors.
	ColorBasic
	// Color256 supports the terminal's 256-color palette.
	Color256
	// ColorTrue supports true-color styling.
	ColorTrue
)

// String returns the stable, payload-free capability label.
func (c ColorCapability) String() string {
	switch c {
	case ColorNone:
		return "none"
	case ColorUnknown:
		return "unknown"
	case ColorBasic:
		return "basic"
	case Color256:
		return "256"
	case ColorTrue:
		return "true"
	default:
		return "unknown"
	}
}

// MotionPolicy controls whether the root scheduler may animate visible work.
type MotionPolicy uint8

const (
	// MotionStatic disables animation.
	MotionStatic MotionPolicy = iota
	// MotionAnimated permits the root scheduler to animate visible work.
	MotionAnimated
)

// String returns the stable motion-policy label.
func (m MotionPolicy) String() string {
	if m == MotionAnimated {
		return "animated"
	}
	return "static"
}

// ReasonCode identifies one safe reason that affected the resolved policy.
type ReasonCode string

const (
	ReasonDetected           ReasonCode = "detected"
	ReasonUnknown            ReasonCode = "unknown"
	ReasonNonInteractive     ReasonCode = "noninteractive"
	ReasonTermDumb           ReasonCode = "term_dumb"
	ReasonNoColor            ReasonCode = "no_color"
	ReasonUnicodeDisabled    ReasonCode = "unicode_disabled"
	ReasonReducedMotion      ReasonCode = "reduced_motion"
	ReasonUnsupportedProfile ReasonCode = "unsupported_profile"
)

// Environment contains only normalized facts needed for presentation policy.
// It never carries the process environment or raw control-bearing values.
type Environment struct {
	TermDumb bool
	NoColor  bool
}

// Preferences contains disable-only user presentation preferences.
type Preferences struct {
	Unicode       bool
	ReducedMotion bool
}

// Input is the bounded evidence used to resolve one run-scoped policy.
type Input struct {
	Profile     colorprofile.Profile
	Environment Environment
	Preferences Preferences
}

// Policy is the frontend-neutral presentation decision for one run.
type Policy struct {
	Color   ColorCapability
	ASCII   bool
	Motion  MotionPolicy
	Reasons []ReasonCode
}

// Clone returns an independent policy value for callers that retain it.
func (p Policy) Clone() Policy {
	p.Reasons = append([]ReasonCode(nil), p.Reasons...)
	return p
}

// NormalizeEnvironment extracts only bounded, recognized environment facts.
func NormalizeEnvironment(values []string) Environment {
	var result Environment
	for _, value := range values {
		key, raw, ok := splitEnvironment(value)
		if !ok {
			continue
		}
		switch key {
		case "TERM":
			result.TermDumb = strings.EqualFold(raw, "dumb")
		case "NO_COLOR":
			result.NoColor = true
		}
	}
	return result
}

// Resolve applies disable-only precedence to one set of evidence. Unknown or
// unsupported profiles resolve to colorless ASCII static output.
func Resolve(input Input) Policy {
	policy := Policy{Color: colorCapability(input.Profile), Motion: MotionAnimated}
	if !input.Preferences.Unicode {
		policy.ASCII = true
	}

	if input.Profile == colorprofile.Unknown {
		policy.Color = ColorNone
		policy.ASCII = true
		policy.Motion = MotionStatic
		policy.Reasons = append(policy.Reasons, ReasonUnknown)
	} else if !knownProfile(input.Profile) {
		policy.Color = ColorNone
		policy.ASCII = true
		policy.Motion = MotionStatic
		policy.Reasons = append(policy.Reasons, ReasonUnsupportedProfile)
	}
	if input.Profile == colorprofile.NoTTY {
		policy.Color = ColorNone
		policy.ASCII = true
		policy.Motion = MotionStatic
		policy.Reasons = append(policy.Reasons, ReasonNonInteractive)
	}
	if input.Environment.TermDumb {
		policy.Color = ColorNone
		policy.ASCII = true
		policy.Motion = MotionStatic
		policy.Reasons = append(policy.Reasons, ReasonTermDumb)
	}
	if input.Environment.NoColor {
		policy.Color = ColorNone
		policy.Reasons = append(policy.Reasons, ReasonNoColor)
	}
	if !input.Preferences.Unicode {
		policy.ASCII = true
		policy.Reasons = append(policy.Reasons, ReasonUnicodeDisabled)
	}
	if input.Preferences.ReducedMotion {
		policy.Motion = MotionStatic
		policy.Reasons = append(policy.Reasons, ReasonReducedMotion)
	}
	if len(policy.Reasons) == 0 {
		policy.Reasons = []ReasonCode{ReasonDetected}
	}
	return policy
}

// HealthEvidence is safe query-only terminal evidence for doctor or support
// projections. It contains no raw environment or terminal control values.
type HealthEvidence struct {
	Policy    Policy
	QueryOnly bool
}

// DetectHealth uses colorprofile's passive detection and returns normalized
// evidence. It does not enter raw mode, emit probes, or expose environment
// values. A nil writer is treated as a noninteractive sink.
func DetectHealth(output io.Writer, values []string, preferences Preferences) HealthEvidence {
	if output == nil {
		output = io.Discard
	}
	environment := normalizedEnvironment(values)
	profile := colorprofile.Detect(output, environment)
	return HealthEvidence{
		Policy:    Resolve(Input{Profile: profile, Environment: NormalizeEnvironment(environment), Preferences: preferences}),
		QueryOnly: true,
	}
}

func colorCapability(profile colorprofile.Profile) ColorCapability {
	switch profile {
	case colorprofile.ASCII:
		return ColorNone
	case colorprofile.ANSI:
		return ColorBasic
	case colorprofile.ANSI256:
		return Color256
	case colorprofile.TrueColor:
		return ColorTrue
	default:
		return ColorUnknown
	}
}

func knownProfile(profile colorprofile.Profile) bool {
	switch profile {
	case colorprofile.NoTTY, colorprofile.ASCII, colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor:
		return true
	default:
		return false
	}
}

func normalizedEnvironment(values []string) []string {
	allowed := map[string]string{}
	for _, value := range values {
		key, raw, ok := splitEnvironment(value)
		if !ok {
			continue
		}
		switch key {
		case "CLICOLOR", "CLICOLOR_FORCE", "COLORTERM", "NO_COLOR", "TERM":
			allowed[key] = raw
		}
	}
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxEnvironmentEntries {
		keys = keys[:maxEnvironmentEntries]
	}
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+allowed[key])
	}
	return result
}

func splitEnvironment(value string) (string, string, bool) {
	index := strings.IndexByte(value, '=')
	if index <= 0 || index > maxEnvironmentValue {
		return "", "", false
	}
	key := value[:index]
	raw := value[index+1:]
	if len(raw) > maxEnvironmentValue {
		return "", "", false
	}
	for _, char := range raw {
		if unicode.IsControl(char) || unicode.Is(unicode.Bidi_Control, char) {
			return "", "", false
		}
	}
	return key, raw, true
}
