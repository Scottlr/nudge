package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

func TestReviewTargetSpecValidation(t *testing.T) {
	tests := []struct {
		name string
		new  func() (ReviewTargetSpec, error)
	}{
		{name: "local", new: func() (ReviewTargetSpec, error) { return NewLocalTargetSpec() }},
		{name: "commit", new: func() (ReviewTargetSpec, error) { return NewCommitTargetSpec("HEAD~1", "") }},
		{name: "branch", new: func() (ReviewTargetSpec, error) { return NewBranchTargetSpec("main") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := test.new()
			if err != nil {
				t.Fatalf("construct target: %v", err)
			}
			if err := spec.Validate(); err != nil {
				t.Fatalf("validate target: %v", err)
			}
		})
	}

	invalid := []ReviewTargetSpec{
		{Kind: TargetLocal, CommitExpression: "HEAD"},
		{Kind: TargetCommit},
		{Kind: TargetCommit, CommitExpression: "HEAD", BaseBranch: "main"},
		{Kind: TargetBranch},
		{Kind: TargetBranch, BaseBranch: "main", CommitExpression: "HEAD"},
		{Kind: TargetKind("unknown")},
	}
	for _, spec := range invalid {
		if err := spec.Validate(); !errors.Is(err, ErrInvalidTargetSpec) {
			t.Errorf("spec %+v error = %v, want ErrInvalidTargetSpec", spec, err)
		}
	}
}

func TestSnapshotRefValidation(t *testing.T) {
	worktreeID, err := domain.NewWorktreeID("worktree-1")
	if err != nil {
		t.Fatal(err)
	}
	commitObject, err := NewObjectID("0123456789012345678901234567890123456789")
	if err != nil {
		t.Fatal(err)
	}
	sha256Object, err := NewObjectID("0123456789012345678901234567890123456789012345678901234567890123")
	if err != nil {
		t.Fatal(err)
	}

	valid := []SnapshotRef{
		{Kind: SnapshotCommit, ObjectID: commitObject},
		{Kind: SnapshotTree, ObjectID: sha256Object},
		{Kind: SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: "fingerprint"},
		{Kind: SnapshotEmpty},
	}
	for _, ref := range valid {
		if _, err := NewSnapshotRef(ref); err != nil {
			t.Errorf("snapshot %+v rejected: %v", ref, err)
		}
	}

	invalid := []SnapshotRef{
		{Kind: SnapshotWorkingTree, ObjectID: commitObject, WorktreeID: worktreeID, Fingerprint: "fingerprint"},
		{Kind: SnapshotCommit},
		{Kind: SnapshotWorkingTree, WorktreeID: worktreeID},
		{Kind: SnapshotKind("other")},
		{Kind: SnapshotCommit, ObjectID: ObjectID("bad\nobject")},
	}
	for _, ref := range invalid {
		if _, err := NewSnapshotRef(ref); !errors.Is(err, ErrInvalidSnapshotRef) {
			t.Errorf("snapshot %+v error = %v, want ErrInvalidSnapshotRef", ref, err)
		}
	}
}

func TestResolvedTargetRequiresDestinationWhenEditable(t *testing.T) {
	spec, err := NewLocalTargetSpec()
	if err != nil {
		t.Fatal(err)
	}
	worktreeID, err := domain.NewWorktreeID("worktree-1")
	if err != nil {
		t.Fatal(err)
	}
	head, err := NewSnapshotRef(SnapshotRef{Kind: SnapshotWorkingTree, WorktreeID: worktreeID, Fingerprint: "fingerprint"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewSnapshotRef(SnapshotRef{Kind: SnapshotEmpty})
	if err != nil {
		t.Fatal(err)
	}

	target := ResolvedTarget{
		Spec:       spec,
		Generation: 1,
		Base:       base,
		Head:       head,
		Editable:   true,
		ResolvedAt: time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC),
	}
	if _, err := NewResolvedTarget(target); !errors.Is(err, ErrInvalidTargetSpec) {
		t.Fatalf("editable target error = %v, want ErrInvalidTargetSpec", err)
	}
	target.EditDestination = &worktreeID
	if _, err := NewResolvedTarget(target); err != nil {
		t.Fatalf("target with destination rejected: %v", err)
	}
}

func TestRepositoryAndLinkedWorktreeKeepBindingPathsSeparate(t *testing.T) {
	repositoryID, err := domain.NewRepositoryID("repository-1")
	if err != nil {
		t.Fatal(err)
	}
	worktreeID, err := domain.NewWorktreeID("worktree-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC)
	repository := Repository{
		ID:           repositoryID,
		CommonGitDir: "/repo/.git",
		Binding: RepositoryBindingEvidence{
			Version:      1,
			ObjectFormat: "sha256",
			CommonGitDir: "/repo/.git",
		},
		DisplayName: "repo",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := repository.Validate(); err != nil {
		t.Fatalf("repository rejected: %v", err)
	}
	worktree := WorktreeRef{
		ID:           worktreeID,
		RepositoryID: repositoryID,
		RootPath:     "/worktrees/feature",
		GitDir:       "/repo/.git/worktrees/feature",
		Binding: WorktreeBindingEvidence{
			Version:      1,
			ObjectFormat: "sha256",
			RootPath:     "/worktrees/feature",
			GitDir:       "/repo/.git/worktrees/feature",
		},
		BranchName:  "feature",
		LaunchFocus: "internal/domain",
	}
	if err := worktree.Validate(); err != nil {
		t.Fatalf("linked worktree rejected: %v", err)
	}
	if repository.CommonGitDir == worktree.GitDir {
		t.Fatal("common and per-worktree Git directories were conflated")
	}
}
