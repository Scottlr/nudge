CREATE TABLE review_snapshots (
    id TEXT PRIMARY KEY,
    capture_id TEXT NOT NULL UNIQUE,
    repository_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    root TEXT NOT NULL,
    marker_nonce TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    evidence_version INTEGER NOT NULL CHECK (evidence_version > 0),
    state TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX review_snapshots_repository_idx
    ON review_snapshots(repository_id, worktree_id, updated_at DESC, id ASC);

CREATE TABLE review_snapshot_leases (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES review_snapshots(id) ON DELETE CASCADE,
    capture_id TEXT NOT NULL,
    root TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    process_nonce TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    released_at TEXT
);

CREATE INDEX review_snapshot_leases_snapshot_idx
    ON review_snapshot_leases(snapshot_id, released_at, acquired_at ASC, id ASC);
