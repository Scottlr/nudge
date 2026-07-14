// Package tui owns disposable Bubble Tea frontend state and projects the
// immutable application snapshot into a bounded responsive shell.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/theme"
	codepane "github.com/Scottlr/nudge/internal/tui/components/code"
	discussionpane "github.com/Scottlr/nudge/internal/tui/components/discussion"
	threadpane "github.com/Scottlr/nudge/internal/tui/components/threads"
	treepane "github.com/Scottlr/nudge/internal/tui/components/tree"
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
	local     <-chan app.LocalReviewSnapshot
	ctx       context.Context

	snapshot       app.AppSnapshot
	localReview    app.LocalReviewSnapshot
	repositoryPane *treepane.Model
	codePane       *codepane.Model
	threadPane     *threadpane.Model
	discussionPane *discussionpane.Model
	dimensions     Dimensions
	layout         Layout
	focus          Pane
	lowerPane      Pane
	narrowPane     Pane
	overlays       OverlayStack
	theme          theme.Theme
	scheduler      *RenderScheduler
	animationFrame uint64

	altScreen   bool
	reportFocus bool
	lastError   string

	snapshotClosed bool
	eventsClosed   bool
	sessionGuard   *app.SessionWriteGuard
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
			if model.repositoryPane != nil {
				model.repositoryPane.SetTheme(value)
			}
			if model.codePane != nil {
				model.codePane.SetTheme(value)
			}
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

// WithLocalReviewStream supplies the bounded asynchronous local-review
// projection used by the initial command surface.
func WithLocalReviewStream(stream <-chan app.LocalReviewSnapshot) ModelOption {
	return func(model *Model) {
		model.local = stream
	}
}

// WithSessionWriteGuard supplies the current guarded session fence for typed
// thread mutations emitted by the discussion projection. The application
// remains authoritative and rejects stale fences.
func WithSessionWriteGuard(guard app.SessionWriteGuard) ModelOption {
	return func(model *Model) {
		copyValue := guard
		model.sessionGuard = &copyValue
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
		client:         client,
		ctx:            context.Background(),
		focus:          PaneRepository,
		lowerPane:      PaneThreads,
		narrowPane:     PaneRepository,
		repositoryPane: treepane.NewModel(),
		codePane:       codepane.NewModel(),
		threadPane:     threadpane.NewModel(),
		discussionPane: discussionpane.NewModel(),
		theme:          theme.BuiltinTerminalDefault(),
		layout:         CalculateLayout(Dimensions{}),
		scheduler:      DefaultRenderScheduler(),
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
	model.resizeChildPanes()
	model.syncReviewSnapshot()
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

// AnimationFrame returns the root-owned animation frame used by visible
// projections. It changes only when an accepted scheduler tick arrives.
func (m *Model) AnimationFrame() uint64 {
	if m == nil {
		return 0
	}
	return m.animationFrame
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
	if m.local != nil {
		commands = append(commands, receiveLocalReview(m.local))
	}
	return tea.Batch(commands...)
}

var _ tea.Model = (*Model)(nil)
