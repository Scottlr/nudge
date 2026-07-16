package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

func TestWorkspaceRetentionCursorRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.LoadWorkspaceRetentionCursor(ctx); !errors.Is(err, app.ErrWorkspaceRetentionCursorNotFound) {
		t.Fatalf("initial cursor error = %v", err)
	}
	want := app.WorkspaceRetentionCursor{AfterID: domain.WorkspaceID("workspace-9"), UpdatedAt: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)}
	if err := store.SaveWorkspaceRetentionCursor(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadWorkspaceRetentionCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}
