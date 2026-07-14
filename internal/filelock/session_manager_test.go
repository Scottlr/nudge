package filelock

import (
	"context"
	"errors"
	"testing"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
)

func TestSessionLeaseManagerUsesStableAndDistinctNativeLocks(t *testing.T) {
	manager, err := NewSessionLeaseManager(t.TempDir())
	if err != nil {
		t.Fatalf("new session lease manager: %v", err)
	}
	key := review.SessionKey{RepositoryID: domain.RepositoryID("repo"), WorktreeID: domain.WorktreeID("worktree"), TargetKind: repository.TargetLocal, BaseIdentity: "base"}
	request := app.SessionLeaseRequest{Key: key, SessionID: domain.ReviewSessionID("session-1"), LeaseID: domain.SessionLeaseID("lease-1")}
	first, err := manager.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	secondRequest := request
	secondRequest.SessionID = "session-2"
	secondRequest.LeaseID = "lease-2"
	if _, err := manager.Acquire(context.Background(), secondRequest); !errors.Is(err, app.ErrSessionBusy) {
		t.Fatalf("second stable lease error = %v, want ErrSessionBusy", err)
	}
	distinct := secondRequest
	distinct.Distinct = true
	other, err := manager.Acquire(context.Background(), distinct)
	if err != nil {
		t.Fatalf("acquire explicit distinct lease: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatalf("close distinct lease: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first lease: %v", err)
	}
	reacquired, err := manager.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("close reacquired lease: %v", err)
	}
}
