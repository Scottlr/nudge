package repository

import "testing"

func TestClassifyGitModeUsesExactV1Modes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		mode  uint32
		class GitModeClass
	}{
		{name: "regular", mode: 0o100644, class: ModeRegularNonExecutable},
		{name: "executable", mode: 0o100755, class: ModeRegularExecutable},
		{name: "symlink", mode: 0o120000, class: ModeSymlink},
		{name: "gitlink", mode: 0o160000, class: ModeGitlink},
		{name: "tree", mode: 0o040000, class: ModeTree},
		{name: "unsupported permissions", mode: 0o100600, class: ModeUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyGitMode(test.mode); got != test.class {
				t.Fatalf("ClassifyGitMode(%o) = %q, want %q", test.mode, got, test.class)
			}
			if test.class == ModeUnsupported {
				if err := ValidateGitMode(test.mode); err == nil {
					t.Fatalf("ValidateGitMode(%o) unexpectedly succeeded", test.mode)
				}
				return
			}
			if err := ValidateGitMode(test.mode); err != nil {
				t.Fatalf("ValidateGitMode(%o) = %v", test.mode, err)
			}
		})
	}
}

func TestNewModeTransitionClassifiesExecutableAndTypeChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		old  uint32
		new  uint32
		kind ModeTransitionKind
	}{
		{name: "unchanged", old: 0o100644, new: 0o100644, kind: ModeUnchanged},
		{name: "executable on", old: 0o100644, new: 0o100755, kind: ModeExecutableOn},
		{name: "executable off", old: 0o100755, new: 0o100644, kind: ModeExecutableOff},
		{name: "regular to symlink", old: 0o100644, new: 0o120000, kind: ModeTypeChanged},
		{name: "symlink to regular", old: 0o120000, new: 0o100644, kind: ModeTypeChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transition, err := NewModeTransition(test.old, test.new)
			if err != nil {
				t.Fatalf("NewModeTransition() error = %v", err)
			}
			if transition.Kind != test.kind {
				t.Fatalf("transition kind = %q, want %q", transition.Kind, test.kind)
			}
			if err := transition.Validate(); err != nil {
				t.Fatalf("transition invalid: %v", err)
			}
		})
	}
	if _, err := NewModeTransition(0o100600, 0o100644); err == nil {
		t.Fatal("NewModeTransition() accepted an unsupported old mode")
	}
}
