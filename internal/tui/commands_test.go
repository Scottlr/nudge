package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestCommandRegistryRejectsCollisionAndMissingHandler(t *testing.T) {
	t.Parallel()

	registry := NewCommandRegistry()
	first := commandRegistration("first", ContextGlobal, "first", "first command", []string{"x"}, nil, false, func(*Model) tea.Cmd { return nil })
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	collision := commandRegistration("second", ContextGlobal, "second", "second command", []string{"x"}, nil, false, func(*Model) tea.Cmd { return nil })
	if err := registry.Register(collision); !errors.Is(err, ErrCommandCollision) {
		t.Fatalf("collision error = %v", err)
	}
	missing := CommandRegistration{Spec: CommandSpec{ID: "missing", Context: ContextGlobal, Label: "missing", Description: "missing", Required: true, Binding: key.NewBinding(key.WithKeys("m"))}}
	if err := registry.Register(missing); !errors.Is(err, ErrMissingCommandHandler) {
		t.Fatalf("missing handler error = %v", err)
	}
}

func TestCommandResolutionUsesBubbleTeaV2BindingsAndContextPrecedence(t *testing.T) {
	t.Parallel()

	registry, err := newDefaultCommandRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message := tea.KeyPressMsg{Code: 'k', Text: "k"}
	registration, matched, err := registry.Resolve(nil, message, []CommandContext{ContextPane})
	if err != nil || !matched || registration.Spec.ID != CommandMoveUp {
		t.Fatalf("move-up resolution = %#v, matched=%v, err=%v", registration.Spec, matched, err)
	}
	modal := NewModel(nil, WithDimensions(120, 30))
	modal.showOverlay(Overlay{ID: "modal", Dismissible: true})
	registration, matched, err = modal.commands.Resolve(modal, tea.KeyPressMsg{Code: 'q', Text: "q"}, modal.activeContexts())
	if err != nil || !matched || registration.Spec.ID != CommandCloseOverlay {
		t.Fatalf("modal resolution = %#v, matched=%v, err=%v", registration.Spec, matched, err)
	}
}

func TestHelpEntriesUseRegisteredBindingsAndOmitUnimplementedCommands(t *testing.T) {
	t.Parallel()

	model := NewModel(nil, WithDimensions(120, 30))
	entries := model.commands.Entries(model, []CommandContext{ContextPane, ContextGlobal})
	if len(entries) == 0 {
		t.Fatal("no help entries")
	}
	for _, entry := range entries {
		registration, ok := model.commands.Registration(entry.ID)
		if !ok || len(entry.Keys) != len(registration.Spec.Binding.Keys()) {
			t.Fatalf("help entry has no matching binding: %#v", entry)
		}
		for index, value := range entry.Keys {
			if value != registration.Spec.Binding.Keys()[index] {
				t.Fatalf("help key %q differs from registered key %q", value, registration.Spec.Binding.Keys()[index])
			}
		}
	}
	for _, absent := range []CommandID{"request_proposal", "approve_proposal", "reject_proposal", "open_theme_selector"} {
		if _, ok := model.commands.Registration(absent); ok {
			t.Fatalf("unimplemented command %q was registered", absent)
		}
	}
}

func TestRootDispatchesKeyboardCommandsAndRendersHelpOverlay(t *testing.T) {
	t.Parallel()

	model := NewModel(nil, WithDimensions(120, 30))
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(*Model)
	if command != nil || model.Focus() != PaneCode || model.FocusTarget() != FocusTargetCode {
		t.Fatalf("tab focus = pane %q target %q command=%v", model.Focus(), model.FocusTarget(), command)
	}
	updated, command = model.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	model = updated.(*Model)
	if command != nil || model.overlays.Len() != 1 || !strings.Contains(ansi.Strip(model.View().Content), "Keyboard commands") {
		t.Fatalf("help command did not open a safe overlay: overlays=%d command=%v view=%q", model.overlays.Len(), command, model.View().Content)
	}
	updated, command = model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	model = updated.(*Model)
	if command != nil || model.overlays.Len() != 0 || model.FocusTarget() != FocusTargetCode {
		t.Fatalf("modal close did not restore focus: overlays=%d target=%q command=%v", model.overlays.Len(), model.FocusTarget(), command)
	}
	_, command = model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if command == nil {
		t.Fatal("q did not resolve to quit outside a modal")
	}
}
