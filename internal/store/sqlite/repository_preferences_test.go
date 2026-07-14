package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
)

func TestRepositoryBasePreferenceRoundTripAndCAS(t *testing.T) {
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
	first := app.BaseBranchPreference{RepositoryID: repo.ID, Expression: "refs/heads/main", Revision: 1, UpdatedAt: time.Unix(100, 0).UTC()}
	if err := store.SaveBaseBranchPreference(ctx, first, 0); err != nil {
		t.Fatalf("save first preference: %v", err)
	}
	loaded, err := store.LoadBaseBranchPreference(ctx, repo.ID)
	if err != nil {
		t.Fatalf("load first preference: %v", err)
	}
	if loaded.Expression != first.Expression || loaded.Revision != first.Revision || !loaded.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("loaded preference = %#v, want %#v", loaded, first)
	}
	second := app.BaseBranchPreference{RepositoryID: repo.ID, Expression: "refs/heads/release", Revision: 2, UpdatedAt: time.Unix(200, 0).UTC()}
	if err := store.SaveBaseBranchPreference(ctx, second, 1); err != nil {
		t.Fatalf("replace preference: %v", err)
	}
	stale := app.BaseBranchPreference{RepositoryID: repo.ID, Expression: "refs/heads/stale", Revision: 2, UpdatedAt: time.Unix(300, 0).UTC()}
	if err := store.SaveBaseBranchPreference(ctx, stale, 1); !errors.Is(err, app.ErrPreferenceRevisionConflict) {
		t.Fatalf("stale save error = %v, want preference conflict", err)
	}
	if err := store.ClearBaseBranchPreference(ctx, repo.ID, 1); !errors.Is(err, app.ErrPreferenceRevisionConflict) {
		t.Fatalf("stale clear error = %v, want preference conflict", err)
	}
	if err := store.ClearBaseBranchPreference(ctx, repo.ID, 2); err != nil {
		t.Fatalf("clear preference: %v", err)
	}
	if _, err := store.LoadBaseBranchPreference(ctx, repo.ID); !errors.Is(err, app.ErrReviewStoreNotFound) {
		t.Fatalf("cleared preference error = %v, want not found", err)
	}
}

func TestRepositoryBasePreferenceIsBoundToRepositoryID(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	repo, worktree, _, _, _ := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatalf("upsert original repository: %v", err)
	}
	preference := app.BaseBranchPreference{RepositoryID: repo.ID, Expression: "main", Revision: 1, UpdatedAt: time.Unix(100, 0).UTC()}
	if err := store.SaveBaseBranchPreference(ctx, preference, 0); err != nil {
		t.Fatalf("save original preference: %v", err)
	}
	replacement := repo
	replacement.ID = domain.RepositoryID("repo-replacement")
	replacement.Binding.CommonGitDirIdentity = "common-replacement"
	replacement.CreatedAt = replacement.CreatedAt.Add(time.Second)
	replacement.UpdatedAt = replacement.UpdatedAt.Add(time.Second)
	replacementWorktree := worktree
	replacementWorktree.ID = domain.WorktreeID("worktree-replacement")
	replacementWorktree.RepositoryID = replacement.ID
	replacementWorktree.Binding.RootIdentity = "root-replacement"
	replacementWorktree.Binding.GitDirIdentity = "git-replacement"
	if err := store.UpsertRepository(ctx, replacement, replacementWorktree); err != nil {
		t.Fatalf("upsert replacement repository: %v", err)
	}
	if _, err := store.LoadBaseBranchPreference(ctx, replacement.ID); !errors.Is(err, app.ErrReviewStoreNotFound) {
		t.Fatalf("replacement preference error = %v, want not found", err)
	}
	original, err := store.LoadBaseBranchPreference(ctx, repo.ID)
	if err != nil || original.Expression != "main" {
		t.Fatalf("original preference = %#v, err=%v", original, err)
	}
}

func TestRepositoryBasePreferenceRejectsUnsafeExpressionBeforeWrite(t *testing.T) {
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
	unsafe := app.BaseBranchPreference{RepositoryID: repo.ID, Expression: "-main", Revision: 1, UpdatedAt: time.Unix(100, 0).UTC()}
	if err := store.SaveBaseBranchPreference(ctx, unsafe, 0); !errors.Is(err, app.ErrReviewStoreInput) {
		t.Fatalf("unsafe save error = %v, want store input", err)
	}
	if _, err := store.LoadBaseBranchPreference(ctx, repo.ID); !errors.Is(err, app.ErrReviewStoreNotFound) {
		t.Fatalf("unsafe preference lookup = %v, want not found", err)
	}
}
