CREATE TABLE capture_ownership (
    capture_id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    manifest_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX capture_ownership_repository_idx
    ON capture_ownership(repository_id, created_at ASC, capture_id ASC);
