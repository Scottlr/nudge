// Package tui owns disposable Bubble Tea frontend state and projects the
// immutable application snapshot into a bounded responsive shell.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/theme"
)

// Pane identifies one product surface without carrying its workflow data.
type Pane string

const (
	PaneRepository Pane = "repository"
	PaneCode       Pane = "code"
	PaneThreads    Pane = "threads"
	PaneDiscussion Pane = "discussion"
)

// Model is the Bubble Tea root. It holds only frontend state, stream handles,
// and cloned application projections.
type Model struct {
	client    app.ApplicationClient
	snapshots <-chan app.AppSnapshot
	events    <-chan app.Event
	ctx       context.Context

	snapshot   app.AppSnapshot
	dimensions Dimensions
	layout     Layout
	focus      Pane
	lowerPane  Pane
	narrowPane Pane
	overlays   OverlayStack
	theme      theme.Theme

	altScreen   bool
	reportFocus bool
	lastError   string

	snapshotClosed bool
	eventsClosed   bool
}

// ModelOption configures frontend state without changing application policy.
type ModelOption func(*Model)

// WithContext supplies the cancellation context used for root-dispatched
// application commands.
func WithContext(ctx context.Context) ModelOption {
	return func(model *Model) {
		if ctx != nil {
			model.ctx = ctx
		}
	}
}

// WithInitialSnapshot seeds the shell before the first stream projection.
func WithInitialSnapshot(snapshot app.AppSnapshot) ModelOption {
	return func(model *Model) {
		model.snapshot = snapshot.Clone()
	}
}

// WithTheme supplies an already-resolved semantic theme. Theme selection and
// user-theme loading remain outside this task's root shell.
func WithTheme(value theme.Theme) ModelOption {
	return func(model *Model) {
		if value.Validate() == nil {
			model.theme = value
		}
	}
}

// WithAltScreen controls the declarative Bubble Tea view flag.
func WithAltScreen(enabled bool) ModelOption {
	return func(model *Model) {
		model.altScreen = enabled
	}
}

// WithReportFocus controls the declarative Bubble Tea focus-reporting flag.
func WithReportFocus(enabled bool) ModelOption {
	return func(model *Model) {
		model.reportFocus = enabled
	}
}

// WithDimensions seeds pure layout state for tests or embedding callers.
func WithDimensions(width, height int) ModelOption {
	return func(model *Model) {
		model.dimensions = Dimensions{Width: width, Height: height}
		model.layout = CalculateLayout(model.dimensions)
	}
}

// NewModel creates a root model and subscribes to the client's bounded
// snapshot/event streams without performing blocking application work.
func NewModel(client app.ApplicationClient, options ...ModelOption) *Model {
	model := &Model{
		client:     client,
		ctx:        context.Background(),
		focus:      PaneRepository,
		lowerPane:  PaneThreads,
		narrowPane: PaneRepository,
		theme:      theme.BuiltinTerminalDefault(),
		layout:     CalculateLayout(Dimensions{}),
	}
	if client != nil {
		model.snapshots = client.Snapshots()
		model.events = client.Events()
	}
	for _, option := range options {
		if option != nil {
			option(model)
		}
	}
	if model.layout.Dimensions != model.dimensions {
		model.layout = CalculateLayout(model.dimensions)
	}
	return model
}

// New is a concise constructor for the default shell configuration.
func New(client app.ApplicationClient) *Model {
	return NewModel(client)
}

// Snapshot returns a defensive copy of the current application projection.
func (m *Model) Snapshot() app.AppSnapshot {
	if m == nil {
		return app.AppSnapshot{}
	}
	return m.snapshot.Clone()
}

// CurrentLayout returns the current pure layout result.
func (m *Model) CurrentLayout() Layout {
	if m == nil {
		return Layout{}
	}
	return m.layout
}

// Focus returns the focused pane.
func (m *Model) Focus() Pane {
	if m == nil {
		return ""
	}
	return m.focus
}

// Init starts one bounded receive command for each application stream.
func (m *Model) Init() tea.Cmd {
	if m == nil {
		return nil
	}
	var commands []tea.Cmd
	if m.snapshots != nil && !m.snapshotClosed {
		commands = append(commands, receiveSnapshot(m.snapshots))
	}
	if m.events != nil && !m.eventsClosed {
		commands = append(commands, receiveEvent(m.events))
	}
	return tea.Batch(commands...)
}

var _ tea.Model = (*Model)(nil)
