package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/Scottlr/nudge/internal/presentation"
)

const (
	maxOverlayCount = 4
	maxOverlayBytes = 64 * 1024
)

// Overlay is a bounded, display-only root overlay. Workflow authorization
// remains owned by application commands and is never inferred from this value.
type Overlay struct {
	ID          string
	Title       string
	Body        string
	Dismissible bool
}

// OverlayStack keeps only a small, newest-on-top set of transient overlays.
type OverlayStack struct {
	items []Overlay
}

// Push admits one overlay after terminal-safe projection. It returns false for
// an empty identity or when the stack is already at its explicit bound.
func (s *OverlayStack) Push(overlay Overlay) bool {
	if s == nil || strings.TrimSpace(overlay.ID) == "" || len(s.items) >= maxOverlayCount {
		return false
	}
	overlay.ID = boundedOverlayText(overlay.ID, presentation.TerminalTextScalar)
	overlay.Title = boundedOverlayText(overlay.Title, presentation.TerminalTextScalar)
	overlay.Body = boundedOverlayText(overlay.Body, presentation.TerminalTextMultiline)
	s.items = append(s.items, overlay)
	return true
}

// Pop removes and returns the newest overlay.
func (s *OverlayStack) Pop() (Overlay, bool) {
	if s == nil || len(s.items) == 0 {
		return Overlay{}, false
	}
	last := len(s.items) - 1
	overlay := s.items[last]
	s.items = s.items[:last]
	return overlay, true
}

// Top returns a copy of the newest overlay without changing the stack.
func (s OverlayStack) Top() (Overlay, bool) {
	if len(s.items) == 0 {
		return Overlay{}, false
	}
	return s.items[len(s.items)-1], true
}

// Len reports the number of retained overlays.
func (s OverlayStack) Len() int {
	return len(s.items)
}

func boundedOverlayText(value string, mode presentation.TerminalTextMode) string {
	value = presentation.ProjectTerminalText(value, mode)
	if len(value) <= maxOverlayBytes {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && len(string(runes))+len("…") > maxOverlayBytes {
		runes = runes[:len(runes)-1]
	}
	result := string(runes) + "…"
	if !utf8.ValidString(result) {
		return "…"
	}
	return result
}
