package review

import (
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestCommitSessionKeyIncludesParentAndDoesNotBecomeWorktreeScoped(t *testing.T) {
	spec, err := repository.NewCommitTargetSpec("HEAD", "")
	if err != nil {
		t.Fatal(err)
	}
	worktreeID := domain.WorktreeID("worktree")
	target := repository.ResolvedTarget{
		Spec:            spec,
		Generation:      1,
		Base:            repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "parent-1"},
		Head:            repository.SnapshotRef{Kind: repository.SnapshotCommit, ObjectID: "commit-1"},
		ResolvedCommit:  "commit-1",
		ResolvedParent:  "parent-1",
		ParentLabel:     "parent 1",
		Editable:        true,
		EditDestination: &worktreeID,
		ResolvedAt:      time.Unix(10, 0).UTC(),
	}
	sessionID := domain.ReviewSessionID("session")
	session, err := NewOpenReviewSession(sessionID, domain.RepositoryID("repo"), spec, target, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	key, err := SessionKeyFor(session)
	if err != nil {
		t.Fatal(err)
	}
	if key.WorktreeID != "" || key.FrozenCommit != "commit-1" {
		t.Fatalf("commit session key = %#v, want repository/commit scope", key)
	}

	target.Base.ObjectID = "parent-2"
	target.ResolvedParent = "parent-2"
	other, err := NewOpenReviewSession(domain.ReviewSessionID("session-2"), domain.RepositoryID("repo"), spec, target, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := SessionKeyFor(other)
	if err != nil {
		t.Fatal(err)
	}
	if key.BaseIdentity == otherKey.BaseIdentity {
		t.Fatal("commit session keys collided across comparison parents")
	}
}
