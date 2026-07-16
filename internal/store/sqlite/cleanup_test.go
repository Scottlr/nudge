package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
)

func TestCleanupInventoryAndExplicitRepositoryDeletion(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	repo, worktree, _, _, _ := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatalf("upsert repository: %v", err)
	}
	inventory, err := store.LoadCleanupInventory(ctx, repo.ID)
	if err != nil {
		t.Fatalf("load cleanup inventory: %v", err)
	}
	plan, err := app.NewCleanupPlan("cleanup-plan-1", inventory, time.Now().UTC())
	if err != nil {
		t.Fatalf("new cleanup plan: %v", err)
	}
	if err := store.SaveCleanupPlan(ctx, plan); err != nil {
		t.Fatalf("save cleanup plan: %v", err)
	}
	loadedPlan, err := store.LoadCleanupPlan(ctx, plan.ID)
	if err != nil || loadedPlan.ManifestHash != plan.ManifestHash {
		t.Fatalf("load cleanup plan = %#v, err=%v", loadedPlan, err)
	}
	if err := store.DeleteRepositoryRows(ctx, repo.ID, plan); err != nil {
		t.Fatalf("delete repository rows: %v", err)
	}
	if _, err := store.LoadCleanupInventory(ctx, repo.ID); err != app.ErrCleanupNotFound {
		t.Fatalf("load deleted repository = %v, want cleanup not found", err)
	}
}
