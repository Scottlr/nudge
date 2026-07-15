PRAGMA foreign_keys=OFF;

ALTER TABLE review_snapshot_leases RENAME TO review_snapshot_leases_v1;
ALTER TABLE review_snapshots RENAME TO review_snapshots_v1;

DROP INDEX IF EXISTS review_snapshots_repository_idx;
DROP INDEX IF EXISTS review_snapshot_leases_snapshot_idx;

CREATE TABLE review_snapshots (
    id TEXT PRIMARY KEY,
    capture_id TEXT UNIQUE,
    repository_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    target_kind TEXT NOT NULL DEFAULT '',
    head_object_id TEXT NOT NULL DEFAULT '',
    base_object_id TEXT NOT NULL DEFAULT '',
    parent_label TEXT NOT NULL DEFAULT '',
    object_format TEXT NOT NULL DEFAULT '',
    format_version INTEGER NOT NULL DEFAULT 0 CHECK (format_version >= 0),
    root TEXT NOT NULL,
    marker_nonce TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    evidence_version INTEGER NOT NULL CHECK (evidence_version > 0),
    state TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO review_snapshots(
    id, capture_id, repository_id, worktree_id, root, marker_nonce,
    manifest_hash, policy_version, evidence_version, state, created_at, updated_at
)
SELECT id, capture_id, repository_id, worktree_id, root, marker_nonce,
    manifest_hash, policy_version, evidence_version, state, created_at, updated_at
FROM review_snapshots_v1;

CREATE TABLE review_snapshot_leases (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES review_snapshots(id) ON DELETE CASCADE,
    capture_id TEXT,
    root TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    process_nonce TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    released_at TEXT
);

INSERT INTO review_snapshot_leases(id, snapshot_id, capture_id, root, manifest_hash, process_nonce, acquired_at, released_at)
SELECT id, snapshot_id, capture_id, root, manifest_hash, process_nonce, acquired_at, released_at
FROM review_snapshot_leases_v1;

DROP TABLE review_snapshot_leases_v1;
DROP TABLE review_snapshots_v1;

CREATE INDEX review_snapshots_repository_idx
    ON review_snapshots(repository_id, worktree_id, updated_at DESC, id ASC);

CREATE UNIQUE INDEX review_snapshots_object_idx
    ON review_snapshots(repository_id, head_object_id, policy_version, format_version)
    WHERE head_object_id <> '';

CREATE INDEX review_snapshot_leases_snapshot_idx
    ON review_snapshot_leases(snapshot_id, released_at, acquired_at ASC, id ASC);

PRAGMA foreign_keys=ON;
