CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    common_git_dir TEXT NOT NULL,
    common_git_dir_identity TEXT NOT NULL,
    binding_version INTEGER NOT NULL CHECK (binding_version > 0),
    object_format TEXT NOT NULL,
    display_name TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    first_verified_at TEXT NOT NULL,
    last_verified_at TEXT NOT NULL,
    binding_status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE worktrees (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    root_path TEXT NOT NULL,
    git_dir TEXT NOT NULL,
    root_identity TEXT NOT NULL,
    git_dir_identity TEXT NOT NULL,
    binding_version INTEGER NOT NULL CHECK (binding_version > 0),
    object_format TEXT NOT NULL,
    current_object_id TEXT,
    branch_name TEXT NOT NULL,
    detached INTEGER NOT NULL CHECK (detached IN (0, 1)),
    launch_focus TEXT NOT NULL,
    first_verified_at TEXT NOT NULL,
    last_verified_at TEXT NOT NULL,
    binding_status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX worktrees_repository_idx ON worktrees(repository_id, updated_at DESC, id ASC);

CREATE TABLE review_sessions (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    worktree_id TEXT REFERENCES worktrees(id),
    target_kind TEXT NOT NULL,
    session_key_json TEXT NOT NULL,
    session_key_hash TEXT NOT NULL,
    target_json BLOB NOT NULL,
    current_generation INTEGER NOT NULL CHECK (current_generation > 0),
    active_reconciliation_operation_id TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    writer_epoch INTEGER NOT NULL,
    writer_lease_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT
);

CREATE INDEX review_sessions_compatible_idx
    ON review_sessions(repository_id, worktree_id, target_kind, session_key_hash, closed_at, updated_at DESC, id ASC);

CREATE TABLE target_generations (
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    capture_id TEXT,
    capture_generation_json BLOB NOT NULL,
    capture_manifest_json BLOB NOT NULL,
    target_json BLOB,
    fingerprint TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    policy_evaluation_json BLOB NOT NULL DEFAULT 'null',
    retention_reference TEXT NOT NULL DEFAULT '',
    accepted_at TEXT NOT NULL,
    PRIMARY KEY (session_id, generation),
    UNIQUE (session_id, capture_id)
);

CREATE TABLE reconciliation_operations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    from_generation INTEGER NOT NULL CHECK (from_generation > 0),
    to_generation INTEGER NOT NULL CHECK (to_generation > 0),
    state TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
    UNIQUE (session_id, to_generation)
);

CREATE TABLE review_threads (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    resolution TEXT NOT NULL,
    conversation TEXT NOT NULL,
    proposal TEXT NOT NULL,
    read_state TEXT NOT NULL,
    provider_conversation_id TEXT,
    latest_proposal_id TEXT,
    failure_phase TEXT NOT NULL,
    error_code TEXT NOT NULL,
    current_anchor_version INTEGER NOT NULL CHECK (current_anchor_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX review_threads_session_idx ON review_threads(session_id, updated_at ASC, id ASC);

CREATE TABLE anchor_versions (
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    anchor_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (thread_id, version)
);

CREATE TABLE reconciliation_anchor_results (
    operation_id TEXT NOT NULL REFERENCES reconciliation_operations(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    result_json BLOB NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL,
    PRIMARY KEY (operation_id, thread_id)
);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content BLOB NOT NULL,
    provider_id TEXT NOT NULL,
    status TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    body_length INTEGER NOT NULL CHECK (body_length >= 0),
    body_sha256 TEXT NOT NULL,
    failure_phase TEXT NOT NULL,
    error_code TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (thread_id, ordinal)
);

CREATE INDEX messages_thread_idx ON messages(thread_id, updated_at ASC, id ASC);
