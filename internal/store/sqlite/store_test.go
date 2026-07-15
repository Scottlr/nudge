package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/paths"
	_ "modernc.org/sqlite"
)

func TestStoreMigrationAndReviewRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := testDatabasePath(t)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	status, err := store.MigrationStatus(ctx)
	if err != nil {
		t.Fatalf("migration status: %v", err)
	}
	if status.Version != 5 || !validSHA256(status.Checksum) {
		t.Fatalf("migration status = %#v", status)
	}
	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, err=%v", foreignKeys, err)
	}

	repo, worktree, session, thread, message := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatalf("upsert repository: %v", err)
	}
	guard, err := store.CreateSession(ctx, session, domain.SessionLeaseID("lease-1"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	key, err := review.SessionKeyFor(session)
	if err != nil {
		t.Fatalf("session key: %v", err)
	}
	restored, err := store.FindCompatibleSession(ctx, key)
	if err != nil {
		t.Fatalf("find compatible session: %v", err)
	}
	if restored.ID != session.ID || !reflect.DeepEqual(restored.Target, session.Target) {
		t.Fatalf("restored session = %#v, want %#v", restored, session)
	}

	next, err := store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		if err := tx.SaveThread(ctx, thread); err != nil {
			return err
		}
		return tx.SaveMessage(ctx, message)
	})
	if err != nil {
		t.Fatalf("save thread and message: %v", err)
	}
	if next.ExpectedRevision != guard.ExpectedRevision+1 {
		t.Fatalf("next revision = %d, want %d", next.ExpectedRevision, guard.ExpectedRevision+1)
	}
	if _, err := store.WithSessionTx(ctx, guard, func(app.ReviewStoreTx) error { return nil }); !errors.Is(err, app.ErrSessionRevisionConflict) {
		t.Fatalf("stale guard error = %v, want ErrSessionRevisionConflict", err)
	}

	loaded, err := store.LoadThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("load thread: %v", err)
	}
	if loaded.ID != thread.ID || loaded.Anchor.Path.Key() != thread.Anchor.Path.Key() || loaded.Proposal != thread.Proposal {
		t.Fatalf("loaded thread = %#v", loaded)
	}
	threadPage, err := store.ListThreadSummaries(ctx, session.ID, app.ThreadPage{Limit: 1})
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threadPage.Items) != 1 || threadPage.Items[0].ID != thread.ID {
		t.Fatalf("thread page = %#v", threadPage)
	}
	messagePage, err := store.ListMessages(ctx, thread.ID, app.MessagePage{Limit: 1})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messagePage.Items) != 1 || messagePage.Items[0].ID != message.ID || messagePage.Items[0].ByteLength != uint64(len(message.Content)) {
		t.Fatalf("message page = %#v", messagePage)
	}
	digest := sha256.Sum256([]byte(message.Content))
	chunk, err := store.ReadMessageBody(ctx, app.BodyRange{MessageID: message.ID, ExpectedLength: uint64(len(message.Content)), ExpectedSHA256: hex.EncodeToString(digest[:]), Offset: 1, Length: 3})
	if err != nil {
		t.Fatalf("read message body: %v", err)
	}
	if string(chunk.Bytes) != message.Content[1:4] || chunk.Complete {
		t.Fatalf("body chunk = %#v", chunk)
	}

	relocated := thread.Anchor
	relocated.State = review.AnchorRelocated
	operation := app.ReconciliationOperation{ID: domain.OperationID("reconcile-1"), SessionID: session.ID, FromGeneration: 1, ToGeneration: 1, CaptureID: domain.CaptureID("capture-2"), ManifestHash: strings.Repeat("a", 64), State: app.ReconciliationStaged, Progress: app.ReconciliationProgress{Phase: app.ReconciliationPhaseStaging, TotalAnchors: 1}, StartedAt: thread.CreatedAt}
	result := app.ReconciliationAnchorResult{OperationID: operation.ID, ThreadID: thread.ID, Anchor: relocated, State: review.AnchorRelocated, Reason: "exact evidence", ReportID: operation.ID, AlgorithmVersion: review.AnchorReconciliationAlgorithmVersion}
	staged, err := store.WithSessionTx(ctx, next, func(tx app.ReviewStoreTx) error {
		if err := tx.CreateReconciliation(ctx, operation); err != nil {
			return err
		}
		return tx.StageReconciliationResult(ctx, result)
	})
	if err != nil {
		t.Fatalf("stage reconciliation: %v", err)
	}
	if _, err := store.WithSessionTx(ctx, staged, func(tx app.ReviewStoreTx) error {
		return tx.ActivateReconciliation(ctx, operation.ID)
	}); !errors.Is(err, app.ErrSessionRevisionConflict) {
		t.Fatalf("incomplete activation error = %v, want ErrSessionRevisionConflict", err)
	}
	completed, err := store.WithSessionTx(ctx, staged, func(tx app.ReviewStoreTx) error {
		operation.Progress.Phase = app.ReconciliationPhaseCommitting
		operation.Progress.ProcessedAnchors = 1
		if err := tx.UpdateReconciliation(ctx, operation); err != nil {
			return err
		}
		if err := tx.CompleteReconciliation(ctx, operation.ID, thread.CreatedAt); err != nil {
			return err
		}
		return tx.ActivateReconciliation(ctx, operation.ID)
	})
	if err != nil {
		t.Fatalf("complete reconciliation: %v", err)
	}
	if completed.ExpectedRevision != staged.ExpectedRevision+1 {
		t.Fatalf("completed revision = %d, want %d", completed.ExpectedRevision, staged.ExpectedRevision+1)
	}
	loaded, err = store.LoadThread(ctx, thread.ID)
	if err != nil || loaded.Anchor.State != review.AnchorRelocated {
		t.Fatalf("activated anchor = %#v, err=%v", loaded.Anchor, err)
	}
	if _, err := store.ClaimSessionWriter(ctx, session.ID, domain.SessionLeaseID("lease-2")); err != nil {
		t.Fatalf("claim replacement writer: %v", err)
	}
	if _, err := store.WithSessionTx(ctx, completed, func(app.ReviewStoreTx) error { return nil }); !errors.Is(err, app.ErrSessionLeaseLost) {
		t.Fatalf("stale lease error = %v, want ErrSessionLeaseLost", err)
	}
}

func TestStoreRejectsAlteredMigration(t *testing.T) {
	ctx := context.Background()
	path := testDatabasePath(t)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open raw test connection: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'altered'"); err != nil {
		db.Close()
		t.Fatalf("alter migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw connection: %v", err)
	}
	_, err = Open(ctx, path)
	if !errors.Is(err, ErrMigrationChecksum) {
		t.Fatalf("altered migration error = %v, want ErrMigrationChecksum", err)
	}
}

func TestStoreSessionLifecycleAndBindingFence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testDatabasePath(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	repo, worktree, session, _, _ := testStoreValues()
	if err := store.UpsertRepository(ctx, repo, worktree); err != nil {
		t.Fatalf("upsert repository: %v", err)
	}
	guard, err := store.CreateSession(ctx, session, domain.SessionLeaseID("lease-1"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	changed := session
	changed.Target.Generation = 2
	changed.Target.Fingerprint = strings.Repeat("b", 64)
	changed.Target.Head.Fingerprint = changed.Target.Fingerprint
	changed.UpdatedAt = session.UpdatedAt.Add(time.Second)
	guard, err = store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		return tx.SaveSession(ctx, changed)
	})
	if err != nil {
		t.Fatalf("save changed generation: %v", err)
	}
	key, err := review.SessionKeyFor(changed)
	if err != nil {
		t.Fatalf("changed session key: %v", err)
	}
	restored, err := store.FindCompatibleSession(ctx, key)
	if err != nil || restored.Target.Generation != 2 || restored.Target.Fingerprint != changed.Target.Fingerprint {
		t.Fatalf("restored changed session = %#v, err=%v", restored, err)
	}
	closed := changed
	closedAt := changed.UpdatedAt.Add(time.Second)
	if err := closed.Close(closedAt); err != nil {
		t.Fatalf("close session value: %v", err)
	}
	closedGuard, err := store.WithSessionTx(ctx, guard, func(tx app.ReviewStoreTx) error {
		return tx.SaveSession(ctx, closed)
	})
	if err != nil {
		t.Fatalf("persist closed session: %v", err)
	}
	if _, err := store.FindCompatibleSession(ctx, key); !errors.Is(err, app.ErrReviewStoreNotFound) {
		t.Fatalf("closed session lookup error = %v, want not found", err)
	}
	if _, err := store.WithSessionTx(ctx, closedGuard, func(app.ReviewStoreTx) error { return nil }); !errors.Is(err, app.ErrSessionLeaseLost) {
		t.Fatalf("closed session mutation error = %v, want lease lost", err)
	}
	replaced := repo
	replaced.Binding.CommonGitDirIdentity = repository.NativeIdentity("different-native")
	if err := store.UpsertRepository(ctx, replaced, worktree); !errors.Is(err, app.ErrRepositoryBindingChanged) {
		t.Fatalf("binding replacement error = %v, want binding changed", err)
	}
}

func testDatabasePath(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := paths.EnsurePrivateDir(root); err != nil {
		t.Fatalf("prepare private test state: %v", err)
	}
	return filepath.Join(root, "nudge.db")
}

func testStoreValues() (repository.Repository, repository.WorktreeRef, review.ReviewSession, review.ReviewThread, review.Message) {
	now := time.Date(2026, time.July, 14, 19, 0, 0, 0, time.UTC)
	repo := repository.Repository{
		ID:           domain.RepositoryID("repo-1"),
		CommonGitDir: "C:\\repo\\.git",
		Binding: repository.RepositoryBindingEvidence{
			Version:              1,
			ObjectFormat:         "sha1",
			CommonGitDir:         "C:\\repo\\.git",
			CommonGitDirIdentity: repository.NativeIdentity("common-1"),
		},
		DisplayName: "repo",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	worktree := repository.WorktreeRef{
		ID:           domain.WorktreeID("worktree-1"),
		RepositoryID: repo.ID,
		RootPath:     "C:\\repo",
		GitDir:       "C:\\repo\\.git\\worktrees\\nudge",
		Binding: repository.WorktreeBindingEvidence{
			Version:        1,
			ObjectFormat:   "sha1",
			RootPath:       "C:\\repo",
			GitDir:         "C:\\repo\\.git\\worktrees\\nudge",
			RootIdentity:   repository.NativeIdentity("root-1"),
			GitDirIdentity: repository.NativeIdentity("git-1"),
		},
		Detached: true,
	}
	spec, _ := repository.NewLocalTargetSpec()
	editDestination := worktree.ID
	target := repository.ResolvedTarget{
		Spec:            spec,
		Generation:      1,
		Base:            repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:            repository.SnapshotRef{Kind: repository.SnapshotWorkingTree, WorktreeID: worktree.ID, Fingerprint: "head-fingerprint"},
		Editable:        true,
		EditDestination: &editDestination,
		Fingerprint:     "target-fingerprint",
		ResolvedAt:      now,
	}
	session, _ := review.NewOpenReviewSession(domain.ReviewSessionID("session-1"), repo.ID, spec, target, now)
	anchor := review.CodeAnchor{
		Path:             repository.RepoPath([]byte("internal/example.go")),
		Side:             repository.DiffHead,
		StartLine:        2,
		EndLine:          2,
		TargetGeneration: 1,
		Base:             repository.SnapshotRef{Kind: repository.SnapshotEmpty},
		Head:             target.Head,
		HunkFingerprint:  "hunk-fingerprint",
		SelectionHash:    "selection-hash",
		SelectedText:     "return value",
		State:            review.AnchorValid,
		CreatedAt:        now,
	}
	thread, _ := review.NewOpenReviewThread(domain.ReviewThreadID("thread-1"), session.ID, anchor, now)
	message, _ := review.NewPendingMessage(domain.MessageID("message-1"), thread.ID, review.RoleUser, 1, now)
	message.Content = "hello"
	return repo, worktree, session, thread, message
}
