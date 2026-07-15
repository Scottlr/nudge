package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const GitModePolicyVersion = "v1"

var (
	ErrInvalidGitMode        = errors.New("invalid Git mode")
	ErrInvalidModeTransition = errors.New("invalid Git mode transition")
)

// GitModeClass is the platform-neutral semantic class of one Git tree mode.
// It intentionally does not expose host permission bits.
type GitModeClass string

const (
	ModeRegularNonExecutable GitModeClass = "regular_non_executable"
	ModeRegularExecutable    GitModeClass = "regular_executable"
	ModeSymlink              GitModeClass = "symlink"
	ModeGitlink              GitModeClass = "gitlink"
	ModeTree                 GitModeClass = "tree"
	ModeUnsupported          GitModeClass = "unsupported"
)

func (c GitModeClass) Validate() error {
	switch c {
	case ModeRegularNonExecutable, ModeRegularExecutable, ModeSymlink, ModeGitlink, ModeTree, ModeUnsupported:
		return nil
	default:
		return ErrInvalidGitMode
	}
}

// ModeTransitionKind is the semantic relationship between two exact Git
// modes. A type change remains one entry; it is never represented as delete
// plus add by this layer.
type ModeTransitionKind string

const (
	ModeUnchanged       ModeTransitionKind = "unchanged"
	ModeExecutableOn    ModeTransitionKind = "executable_on"
	ModeExecutableOff   ModeTransitionKind = "executable_off"
	ModeTypeChanged     ModeTransitionKind = "type_changed"
	ModeUnsupportedMove ModeTransitionKind = "unsupported"
)

func (k ModeTransitionKind) Validate() error {
	switch k {
	case ModeUnchanged, ModeExecutableOn, ModeExecutableOff, ModeTypeChanged, ModeUnsupportedMove:
		return nil
	default:
		return ErrInvalidModeTransition
	}
}

// ModeTransition is immutable, exact old/new Git mode evidence carried with
// one changed entry and its derived proposal metadata.
type ModeTransition struct {
	OldMode       uint32
	NewMode       uint32
	OldClass      GitModeClass
	NewClass      GitModeClass
	Kind          ModeTransitionKind
	EvidenceHash  string
	PolicyVersion string
}

// ClassifyGitMode recognizes only Git's v1 semantic modes. Unknown permission
// combinations never become a regular file by masking their high bits.
func ClassifyGitMode(mode uint32) GitModeClass {
	switch mode {
	case 0o100644:
		return ModeRegularNonExecutable
	case 0o100755:
		return ModeRegularExecutable
	case 0o120000:
		return ModeSymlink
	case 0o160000:
		return ModeGitlink
	case 0o040000:
		return ModeTree
	default:
		return ModeUnsupported
	}
}

func ValidateGitMode(mode uint32) error {
	if ClassifyGitMode(mode) == ModeUnsupported {
		return fmt.Errorf("%w: %o", ErrInvalidGitMode, mode)
	}
	return nil
}

// NewModeTransition derives deterministic mode semantics from two present
// Git endpoints. Absent sides are represented by the surrounding change, not
// by a fabricated zero mode here.
func NewModeTransition(oldMode, newMode uint32) (ModeTransition, error) {
	oldClass, newClass := ClassifyGitMode(oldMode), ClassifyGitMode(newMode)
	if ValidateGitMode(oldMode) != nil || ValidateGitMode(newMode) != nil {
		return ModeTransition{}, ErrInvalidModeTransition
	}
	transition := ModeTransition{OldMode: oldMode, NewMode: newMode, OldClass: oldClass, NewClass: newClass, PolicyVersion: GitModePolicyVersion}
	switch {
	case oldMode == newMode:
		transition.Kind = ModeUnchanged
	case oldClass == ModeRegularNonExecutable && newClass == ModeRegularExecutable:
		transition.Kind = ModeExecutableOn
	case oldClass == ModeRegularExecutable && newClass == ModeRegularNonExecutable:
		transition.Kind = ModeExecutableOff
	case oldClass != newClass:
		transition.Kind = ModeTypeChanged
	default:
		transition.Kind = ModeUnsupportedMove
	}
	transition.EvidenceHash = modeTransitionHash(transition)
	return transition, nil
}

func (t ModeTransition) Validate() error {
	if t.PolicyVersion != GitModePolicyVersion || t.Kind.Validate() != nil || ValidateGitMode(t.OldMode) != nil || ValidateGitMode(t.NewMode) != nil || t.OldClass != ClassifyGitMode(t.OldMode) || t.NewClass != ClassifyGitMode(t.NewMode) || len(t.EvidenceHash) != sha256.Size*2 {
		return ErrInvalidModeTransition
	}
	expected, err := NewModeTransition(t.OldMode, t.NewMode)
	if err != nil || expected.Kind != t.Kind || t.EvidenceHash != expected.EvidenceHash {
		return ErrInvalidModeTransition
	}
	return nil
}

func (t ModeTransition) IsExecutableChange() bool {
	return t.Kind == ModeExecutableOn || t.Kind == ModeExecutableOff
}

func (t ModeTransition) IsTypeChange() bool {
	return t.Kind == ModeTypeChanged
}

func modeTransitionHash(t ModeTransition) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%o\x00%o\x00%s\x00%s\x00%s\x00", t.PolicyVersion, t.OldMode, t.NewMode, t.OldClass, t.NewClass, t.Kind)
	return hex.EncodeToString(h.Sum(nil))
}
