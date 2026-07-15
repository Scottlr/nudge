package tui

import (
	"errors"
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// CommandID identifies one keyboard action implemented by the current TUI.
type CommandID string

const (
	CommandQuit               CommandID = "quit"
	CommandCloseOverlay       CommandID = "close_overlay"
	CommandEditorCancel       CommandID = "editor_cancel"
	CommandEditorSubmit       CommandID = "editor_submit"
	CommandFocusNext          CommandID = "focus_next"
	CommandFocusPrevious      CommandID = "focus_previous"
	CommandMoveUp             CommandID = "move_up"
	CommandMoveDown           CommandID = "move_down"
	CommandActivate           CommandID = "activate"
	CommandToggleSelection    CommandID = "toggle_selection"
	CommandToggleFileFilter   CommandID = "toggle_file_filter"
	CommandReply              CommandID = "reply"
	CommandResolve            CommandID = "resolve"
	CommandHelp               CommandID = "help"
	CommandApproveRuntimeOnce CommandID = "approve_runtime_once"
	CommandDenyRuntime        CommandID = "deny_runtime"
)

// CommandContext identifies the active input owner. Contexts are ordered from
// the most modal owner to the ordinary pane/global shell.
type CommandContext string

const (
	ContextOverlay         CommandContext = "overlay"
	ContextEditor          CommandContext = "editor"
	ContextPane            CommandContext = "pane"
	ContextGlobal          CommandContext = "global"
	ContextRuntimeApproval CommandContext = "runtime_approval"
)

var (
	// ErrCommandCollision reports two bindings that could resolve in one
	// context.
	ErrCommandCollision = errors.New("command binding collision")
	// ErrDuplicateCommand reports a repeated command identity.
	ErrDuplicateCommand = errors.New("duplicate command")
	// ErrMissingCommandHandler reports a required command without a handler.
	ErrMissingCommandHandler = errors.New("missing required command handler")
)

// CommandSpec contains the stable metadata and physical binding for one
// registered action.
type CommandSpec struct {
	ID          CommandID
	Context     CommandContext
	Label       string
	Description string
	Required    bool
	Destructive bool
	Binding     key.Binding
}

// CommandHandler is a run-scoped root handler. It may update only frontend
// state and return asynchronous commands for application-owned work.
type CommandHandler func(*Model) tea.Cmd

// CommandRegistration combines metadata, availability, and the handler that
// is actually implemented by this run of the product.
type CommandRegistration struct {
	Spec      CommandSpec
	Available func(*Model) bool
	Handler   CommandHandler
}

// CommandRegistry is the single source for keyboard dispatch and help.
type CommandRegistry struct {
	registrations map[CommandID]CommandRegistration
	byContext     map[CommandContext][]CommandID
}

// NewCommandRegistry creates an empty validated registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		registrations: make(map[CommandID]CommandRegistration),
		byContext:     make(map[CommandContext][]CommandID),
	}
}

// Register adds one implemented command and rejects ambiguous composition.
func (r *CommandRegistry) Register(registration CommandRegistration) error {
	if r == nil || registration.Spec.ID == "" || registration.Spec.Context == "" || registration.Spec.Label == "" || registration.Spec.Description == "" {
		return fmt.Errorf("%w: incomplete spec", ErrMissingCommandHandler)
	}
	if !registration.Spec.Binding.Enabled() || len(registration.Spec.Binding.Keys()) == 0 {
		return fmt.Errorf("%w: %s has no binding", ErrMissingCommandHandler, registration.Spec.ID)
	}
	if registration.Handler == nil {
		return fmt.Errorf("%w: %s", ErrMissingCommandHandler, registration.Spec.ID)
	}
	if _, exists := r.registrations[registration.Spec.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateCommand, registration.Spec.ID)
	}
	for _, existingID := range r.byContext[registration.Spec.Context] {
		existing := r.registrations[existingID]
		if bindingsOverlap(existing.Spec.Binding, registration.Spec.Binding) {
			return fmt.Errorf("%w: %s and %s in %s", ErrCommandCollision, existing.Spec.ID, registration.Spec.ID, registration.Spec.Context)
		}
	}
	r.registrations[registration.Spec.ID] = registration
	r.byContext[registration.Spec.Context] = append(r.byContext[registration.Spec.Context], registration.Spec.ID)
	return nil
}

// Registration returns one registration by stable identity.
func (r *CommandRegistry) Registration(id CommandID) (CommandRegistration, bool) {
	if r == nil {
		return CommandRegistration{}, false
	}
	registration, ok := r.registrations[id]
	return registration, ok
}

// Resolve finds at most one available command in the explicit context stack.
// A modal context intentionally prevents fallthrough by supplying a stack
// that contains only its own context.
func (r *CommandRegistry) Resolve(model *Model, message tea.KeyPressMsg, contexts []CommandContext) (CommandRegistration, bool, error) {
	if r == nil {
		return CommandRegistration{}, false, nil
	}
	for _, context := range contexts {
		var match *CommandRegistration
		for _, id := range r.byContext[context] {
			registration := r.registrations[id]
			if registration.Available != nil && !registration.Available(model) {
				continue
			}
			if !key.Matches(message, registration.Spec.Binding) {
				continue
			}
			if match != nil {
				return CommandRegistration{}, false, fmt.Errorf("%w: %s and %s in %s", ErrCommandCollision, match.Spec.ID, registration.Spec.ID, context)
			}
			copyValue := registration
			match = &copyValue
		}
		if match != nil {
			return *match, true, nil
		}
	}
	return CommandRegistration{}, false, nil
}

func bindingsOverlap(left, right key.Binding) bool {
	keys := make(map[string]struct{}, len(left.Keys()))
	for _, value := range left.Keys() {
		keys[value] = struct{}{}
	}
	for _, value := range right.Keys() {
		if _, ok := keys[value]; ok {
			return true
		}
	}
	return false
}
