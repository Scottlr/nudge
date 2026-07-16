package app

import (
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestNewCleanupPlanBindsInventoryIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	inventory := CleanupInventory{
		RepositoryID:      domain.RepositoryID("repo-1"),
		RepositoryDisplay: "repo",
		ObservedRevision:  cleanupTestHash('a'),
		Rows:              CleanupRowCounts{Repositories: 1, Sessions: 2},
		Resources: []CleanupResource{{
			ID: "snapshot-1", Kind: CleanupResourceReviewSnapshot, OwnerID: "snapshot-1",
			RepositoryID: domain.RepositoryID("repo-1"), CanonicalPath: `C:\state\snapshots\snapshot-1`,
			ParentRoot: `C:\state\snapshots`, MarkerNonce: cleanupTestHash('b'), ManifestHash: cleanupTestHash('c'),
		}},
		Exclusions: []string{"user worktree"},
		Effects:    []string{"remove verified resource"},
	}
	plan, err := NewCleanupPlan("plan-1", inventory, now)
	if err != nil {
		t.Fatalf("new cleanup plan: %v", err)
	}
	if plan.ManifestHash == "" || plan.Validate() != nil {
		t.Fatalf("invalid plan: %#v", plan)
	}

	changed := inventory
	changed.Rows.Sessions++
	if changed.ManifestHash() == plan.ManifestHash {
		t.Fatal("changed inventory retained the original manifest hash")
	}
}

func cleanupTestHash(char byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = char
	}
	return string(value)
}
