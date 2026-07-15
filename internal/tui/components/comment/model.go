// Package comment owns the initial review-thread concern editor. It emits
// inert intents and never persists or creates a review thread itself.
package comment

import (
	"errors"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/Scottlr/nudge/internal/domain/review"
)

const MaxCommentBytes = 64 << 10

var (
	ErrEmptyComment       = errors.New("comment is empty")
	ErrCommentTooLarge    = errors.New("comment exceeds 64 KiB")
	ErrCommentInvalidUTF8 = errors.New("comment is not valid UTF-8")
)

// CreateThreadIntent is emitted only after a non-empty concern is explicitly
// submitted for an already-built anchor.
type CreateThreadIntent struct {
	Anchor  review.CodeAnchor
	Comment string
}

// Intent is the component-to-root boundary for the editor modal.
type Intent struct {
	CreateThread *CreateThreadIntent
	Cancelled    bool
}

// Model wraps the Bubbles multiline editor while keeping draft text alive
// across cancellation and modal focus changes.
type Model struct {
	textarea textarea.Model
	anchor   review.CodeAnchor
	lastErr  error
	hints    []string
	width    int
	height   int
}

// NewModel constructs an unfocused editor for one immutable anchor.
func NewModel(anchor review.CodeAnchor) *Model {
	input := textarea.New()
	// Use Bubbles' documented virtual-cursor mode until the root modal owns a
	// tea.View cursor; this keeps the component's editor cursor single-sourced.
	input.SetVirtualCursor(true)
	input.Prompt = "> "
	input.Placeholder = "Describe the concern..."
	model := &Model{textarea: input, anchor: anchor, width: 80, height: 6}
	model.textarea.SetWidth(model.width)
	model.textarea.SetHeight(model.height)
	model.validateDraft()
	return model
}

// SetAnchor replaces the anchor associated with future submissions. Existing
// draft text is intentionally retained so modal interruptions do not erase a
// user's concern.
func (m *Model) SetAnchor(anchor review.CodeAnchor) {
	m.anchor = anchor
}

// Anchor returns the immutable anchor attached to the editor.
func (m *Model) Anchor() review.CodeAnchor {
	return m.anchor
}

// SetSize updates the bounded editor dimensions.
func (m *Model) SetSize(width, height int) {
	if width > 0 {
		m.width = width
		m.textarea.SetWidth(width)
	}
	if height > 0 {
		m.height = height
		m.textarea.SetHeight(height)
	}
}

// Focus focuses the underlying real-cursor textarea.
func (m *Model) Focus() tea.Cmd {
	return m.textarea.Focus()
}

// Blur leaves the draft in place while removing editor focus.
func (m *Model) Blur() {
	m.textarea.Blur()
}

// Focused reports whether the underlying editor owns focus.
func (m *Model) Focused() bool {
	return m.textarea.Focused()
}

// SetActionHints supplies root-derived editor hints without giving the
// component ownership of the keyboard map.
func (m *Model) SetActionHints(values []string) {
	if m == nil {
		return
	}
	m.hints = append([]string(nil), values...)
}

// SetValue replaces the current draft, retaining it until an explicit send.
func (m *Model) SetValue(value string) {
	m.textarea.SetValue(value)
	m.validateDraft()
}

// Value returns the current unsent draft exactly as held by the textarea.
func (m *Model) Value() string {
	return m.textarea.Value()
}

// ByteCount returns the UTF-8 byte count of the current draft.
func (m *Model) ByteCount() int {
	return len([]byte(m.Value()))
}

// RemainingBytes returns the remaining concern capacity, never below zero.
func (m *Model) RemainingBytes() int {
	remaining := MaxCommentBytes - m.ByteCount()
	if remaining < 0 {
		return 0
	}
	return remaining
}

// LastError reports the visible validation state of the draft.
func (m *Model) LastError() error {
	return m.lastErr
}

// CanSubmit reports whether the current draft can produce a thread intent.
func (m *Model) CanSubmit() bool {
	return m.validateDraft() == nil
}

// Cursor exposes the Bubbles cursor for callers that later project it into a
// root tea.View.
func (m *Model) Cursor() *tea.Cursor {
	return m.textarea.Cursor()
}

func (m *Model) validateDraft() error {
	value := m.Value()
	switch {
	case !utf8.ValidString(value):
		m.lastErr = ErrCommentInvalidUTF8
	case len([]byte(value)) > MaxCommentBytes:
		m.lastErr = ErrCommentTooLarge
	case trimBlankLines(value) == "":
		m.lastErr = ErrEmptyComment
	default:
		m.lastErr = nil
	}
	return m.lastErr
}
