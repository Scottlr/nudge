package tui

import (
	"fmt"
	"strings"
)

// HelpEntry is a presentation-safe view of one currently available command.
// Keys are copied from the registered Bubbles binding and never handwritten.
type HelpEntry struct {
	ID          CommandID
	Context     CommandContext
	Keys        []string
	Label       string
	Description string
	Destructive bool
}

// Entries derives help from exactly the same registrations and availability
// predicates used by dispatch.
func (r *CommandRegistry) Entries(model *Model, contexts []CommandContext) []HelpEntry {
	if r == nil {
		return nil
	}
	result := make([]HelpEntry, 0)
	seen := make(map[CommandID]struct{})
	for _, context := range contexts {
		for _, id := range r.byContext[context] {
			if _, ok := seen[id]; ok {
				continue
			}
			registration := r.registrations[id]
			if registration.Available != nil && !registration.Available(model) {
				continue
			}
			keys := append([]string(nil), registration.Spec.Binding.Keys()...)
			result = append(result, HelpEntry{ID: registration.Spec.ID, Context: registration.Spec.Context, Keys: keys, Label: registration.Spec.Label, Description: registration.Spec.Description, Destructive: registration.Spec.Destructive})
			seen[id] = struct{}{}
		}
	}
	return result
}

// Help returns entries for the root's current active context stack.
func (r *CommandRegistry) Help(model *Model) []HelpEntry {
	if model == nil {
		return nil
	}
	return r.Entries(model, model.activeContexts())
}

// StatusHints derives concise status-bar hints from the active registrations.
func (r *CommandRegistry) StatusHints(model *Model) []string {
	if model == nil {
		return nil
	}
	entries := r.Help(model)
	priority := []CommandID{CommandFocusNext, CommandHelp, CommandQuit, CommandEditorSubmit, CommandEditorCancel, CommandCloseOverlay}
	result := make([]string, 0, 3)
	for _, id := range priority {
		for _, entry := range entries {
			if entry.ID != id {
				continue
			}
			result = append(result, formatHint(entry))
			break
		}
		if len(result) == 3 {
			break
		}
	}
	if len(result) == 0 {
		for _, entry := range entries {
			result = append(result, formatHint(entry))
			if len(result) == 3 {
				break
			}
		}
	}
	return result
}

func formatHint(entry HelpEntry) string {
	return strings.Join(entry.Keys, "/") + " " + entry.Label
}

func formatHelpEntry(entry HelpEntry) string {
	return fmt.Sprintf("%-18s %s - %s", strings.Join(entry.Keys, "/"), entry.Label, entry.Description)
}

func (m *Model) openHelp() {
	if m == nil || m.commands == nil {
		return
	}
	entries := m.commands.Entries(m, []CommandContext{ContextPane, ContextGlobal})
	lines := []string{"Keyboard commands"}
	for _, entry := range entries {
		lines = append(lines, formatHelpEntry(entry))
	}
	if len(entries) == 0 {
		lines = append(lines, "No commands are available in this view.")
	}
	m.showOverlay(Overlay{ID: "help", Title: "Keyboard help", Body: strings.Join(lines, "\n"), Dismissible: true})
}
