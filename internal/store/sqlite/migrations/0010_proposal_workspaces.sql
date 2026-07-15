CREATE TABLE proposal_workspaces (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    worktree_id TEXT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    source_thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    state TEXT NOT NULL,
    workspace_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX proposal_workspaces_thread_idx
    ON proposal_workspaces(source_thread_id, updated_at ASC, id ASC);

CREATE TABLE proposal_intents (
    proposal_id TEXT PRIMARY KEY REFERENCES proposals(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    intent_json BLOB NOT NULL,
    confirmed_at TEXT NOT NULL
);

CREATE TABLE proposals (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    current_version INTEGER,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX proposals_thread_idx ON proposals(thread_id, updated_at ASC, id ASC);

CREATE TABLE proposal_attempts (
    id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    outcome TEXT NOT NULL,
    result_disposition TEXT NOT NULL,
    attempt_json BLOB NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE INDEX proposal_attempts_lineage_idx
    ON proposal_attempts(proposal_id, started_at ASC, id ASC);

CREATE TABLE proposal_versions (
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    attempt_id TEXT NOT NULL REFERENCES proposal_attempts(id),
    status TEXT NOT NULL,
    patch_sha256 TEXT NOT NULL,
    patch_bytes BLOB NOT NULL,
    patch_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (proposal_id, version),
    UNIQUE (attempt_id)
);

CREATE TABLE proposal_files (
    proposal_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    path BLOB NOT NULL,
    file_json BLOB NOT NULL,
    PRIMARY KEY (proposal_id, version, ordinal),
    FOREIGN KEY (proposal_id, version) REFERENCES proposal_versions(proposal_id, version) ON DELETE CASCADE
);

CREATE TABLE proposal_preconditions (
    proposal_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    path BLOB NOT NULL,
    precondition_json BLOB NOT NULL,
    PRIMARY KEY (proposal_id, version, ordinal),
    FOREIGN KEY (proposal_id, version) REFERENCES proposal_versions(proposal_id, version) ON DELETE CASCADE
);
