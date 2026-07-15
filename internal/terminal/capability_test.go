package terminal

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestResolvePresentationPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   Input
		color   ColorCapability
		ascii   bool
		motion  MotionPolicy
		reasons []ReasonCode
	}{
		{
			name:    "true color interactive",
			input:   Input{Profile: colorprofile.TrueColor, Preferences: Preferences{Unicode: true}},
			color:   ColorTrue,
			motion:  MotionAnimated,
			reasons: []ReasonCode{ReasonDetected},
		},
		{
			name:    "no color disables styling only",
			input:   Input{Profile: colorprofile.TrueColor, Environment: Environment{NoColor: true}, Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			motion:  MotionAnimated,
			reasons: []ReasonCode{ReasonNoColor},
		},
		{
			name:    "ascii color profile disables styling only",
			input:   Input{Profile: colorprofile.ASCII, Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			motion:  MotionAnimated,
			reasons: []ReasonCode{ReasonDetected},
		},
		{
			name:    "dumb terminal is static ascii",
			input:   Input{Profile: colorprofile.TrueColor, Environment: Environment{TermDumb: true}, Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			ascii:   true,
			motion:  MotionStatic,
			reasons: []ReasonCode{ReasonTermDumb},
		},
		{
			name:    "noninteractive is static ascii",
			input:   Input{Profile: colorprofile.NoTTY, Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			ascii:   true,
			motion:  MotionStatic,
			reasons: []ReasonCode{ReasonNonInteractive},
		},
		{
			name:    "unknown is conservative",
			input:   Input{Profile: colorprofile.Unknown, Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			ascii:   true,
			motion:  MotionStatic,
			reasons: []ReasonCode{ReasonUnknown},
		},
		{
			name:    "unicode preference only disables glyphs",
			input:   Input{Profile: colorprofile.TrueColor, Preferences: Preferences{Unicode: false}},
			color:   ColorTrue,
			ascii:   true,
			motion:  MotionAnimated,
			reasons: []ReasonCode{ReasonUnicodeDisabled},
		},
		{
			name:    "reduced motion only disables animation",
			input:   Input{Profile: colorprofile.TrueColor, Preferences: Preferences{Unicode: true, ReducedMotion: true}},
			color:   ColorTrue,
			motion:  MotionStatic,
			reasons: []ReasonCode{ReasonReducedMotion},
		},
		{
			name:    "unsupported profile is conservative",
			input:   Input{Profile: colorprofile.Profile(99), Preferences: Preferences{Unicode: true}},
			color:   ColorNone,
			ascii:   true,
			motion:  MotionStatic,
			reasons: []ReasonCode{ReasonUnsupportedProfile},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := Resolve(test.input)
			if policy.Color != test.color || policy.ASCII != test.ascii || policy.Motion != test.motion {
				t.Fatalf("policy = %#v, want color=%v ascii=%v motion=%v", policy, test.color, test.ascii, test.motion)
			}
			if !reflect.DeepEqual(policy.Reasons, test.reasons) {
				t.Fatalf("reasons = %#v, want %#v", policy.Reasons, test.reasons)
			}
		})
	}
}

func TestNormalizeEnvironmentDoesNotRetainRawValues(t *testing.T) {
	t.Parallel()
	evidence := NormalizeEnvironment([]string{"TERM=xterm-256color", "TERM=dumb", "NO_COLOR=", "PRIVATE=secret\x1b[31m", "UNRELATED=hidden"})
	if !evidence.TermDumb || !evidence.NoColor {
		t.Fatalf("normalized environment = %#v", evidence)
	}
}

func TestDetectHealthIsQueryOnlyAndBounded(t *testing.T) {
	t.Parallel()
	evidence := DetectHealth(&bytes.Buffer{}, []string{"TERM=dumb", "NO_COLOR=", "PRIVATE=not exposed"}, Preferences{Unicode: true})
	if !evidence.QueryOnly || evidence.Policy.Color != ColorNone || !evidence.Policy.ASCII || evidence.Policy.Motion != MotionStatic {
		t.Fatalf("health evidence = %#v", evidence)
	}
	if len(evidence.Policy.Reasons) == 0 {
		t.Fatal("health evidence has no reason codes")
	}
}
