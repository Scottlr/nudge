package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestRootRejectsStaleSnapshotsAndClonesAcceptedState(t *testing.T) {
	t.Parallel()

	path, err := repository.NewRepoPath([]byte("src/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := app.AppSnapshot{
		Revision: 3,
		Repository: app.RepositorySummary{
			BranchName: "feature/demo",
		},
		Tree: app.TreeProjection{Entries: []app.TreeEntrySummary{{Path: path}}},
	}
	model := NewModel(nil, WithDimensions(120, 30))
	updated, _ := model.Update(SnapshotMsg{Snapshot: snapshot})
	model = updated.(*Model)
	path[0] = 'X'

	stale := snapshot
	stale.Revision = 2
	stale.Repository.BranchName = "stale"
	updated, _ = model.Update(SnapshotMsg{Snapshot: stale})
	model = updated.(*Model)
	current := model.Snapshot()
	if current.Revision != 3 || current.Repository.BranchName != "feature/demo" {
		t.Fatalf("stale snapshot replaced current state: %#v", current)
	}
	if string(current.Tree.Entries[0].Path.Bytes()) != "src/main.go" {
		t.Fatalf("accepted snapshot retained mutable path alias: %q", current.Tree.Entries[0].Path.Bytes())
	}
}

func TestRootDispatchesTypedIntentOutsideUpdate(t *testing.T) {
	t.Parallel()

	client := &testClient{}
	model := NewModel(client)
	updated, command := model.Update(ApplicationIntentMsg{Command: app.OpenRepository{Path: "C:/repo"}})
	if command == nil {
		t.Fatal("typed application intent did not return a command")
	}
	if client.dispatched {
		t.Fatal("application dispatch happened synchronously inside Update")
	}
	message := command()
	if _, ok := message.(DispatchResultMsg); !ok || !client.dispatched {
		t.Fatalf("dispatch command did not produce a result: %#v", message)
	}
	if updated.(*Model).lastError != "" {
		t.Fatal("successful dispatch unexpectedly set an error")
	}
}

func TestViewSanitizesSnapshotTextAndUsesDeclarativeView(t *testing.T) {
	t.Parallel()

	model := NewModel(nil,
		WithDimensions(120, 30),
		WithInitialSnapshot(app.AppSnapshot{Repository: app.RepositorySummary{BranchName: "feature/\x1b[31munsafe"}}),
	)
	view := model.View()
	if strings.ContainsRune(view.Content, '\x1b') || !strings.Contains(view.Content, "Repository") || view.MouseMode != tea.MouseModeNone {
		t.Fatalf("view was not safe or declarative: %#v", view)
	}
}

type testClient struct {
	dispatched bool
}

func (c *testClient) Dispatch(context.Context, app.Command) (domain.OperationID, error) {
	c.dispatched = true
	return "operation-1", nil
}

func (*testClient) Snapshots() <-chan app.AppSnapshot { return nil }
func (*testClient) Events() <-chan app.Event          { return nil }
func (*testClient) Query(context.Context, app.Query) (app.QueryResult, error) {
	return app.QueryResult{}, nil
}
func (*testClient) Close() error { return nil }

var _ app.ApplicationClient = (*testClient)(nil)
