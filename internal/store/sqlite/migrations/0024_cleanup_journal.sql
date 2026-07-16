CREATE TABLE cleanup_plans (
    plan_id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL,
    observed_revision TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    plan_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX cleanup_plans_repository_idx
    ON cleanup_plans(repository_id, created_at DESC, plan_id ASC);

CREATE TABLE cleanup_operations (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL UNIQUE,
    repository_id TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    observed_revision TEXT NOT NULL,
    phase TEXT NOT NULL,
    outcome TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    error_code TEXT NOT NULL DEFAULT '',
    evidence_hash TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE INDEX cleanup_operations_repository_idx
    ON cleanup_operations(repository_id, updated_at DESC, id ASC);
