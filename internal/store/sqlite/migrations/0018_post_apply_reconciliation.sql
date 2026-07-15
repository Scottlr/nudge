CREATE TABLE post_apply_reconciliations (
    apply_operation_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    previous_generation INTEGER NOT NULL CHECK (previous_generation > 0),
    new_generation INTEGER NOT NULL DEFAULT 0 CHECK (new_generation >= 0),
    capture_id TEXT NOT NULL DEFAULT '',
    manifest_hash TEXT NOT NULL DEFAULT '',
    provenance TEXT NOT NULL,
    phase TEXT NOT NULL,
    validity_epoch INTEGER NOT NULL DEFAULT 0 CHECK (validity_epoch >= 0),
    validity_cursor TEXT NOT NULL DEFAULT '',
    processed_proposals INTEGER NOT NULL DEFAULT 0 CHECK (processed_proposals >= 0),
    processed_preconditions INTEGER NOT NULL DEFAULT 0 CHECK (processed_preconditions >= 0),
    evidence_bytes INTEGER NOT NULL DEFAULT 0 CHECK (evidence_bytes >= 0),
    repair_reason TEXT NOT NULL DEFAULT '',
    target_json BLOB,
    destination_json BLOB,
    record_json BLOB NOT NULL,
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE INDEX post_apply_reconciliations_session_idx
    ON post_apply_reconciliations(session_id, phase, updated_at);

CREATE TABLE proposal_validity_epochs (
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    worktree_id TEXT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    target_kind TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    apply_operation_id TEXT NOT NULL REFERENCES post_apply_reconciliations(apply_operation_id) ON DELETE CASCADE,
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    state TEXT NOT NULL CHECK (state IN ('pending', 'complete')),
    PRIMARY KEY (session_id, worktree_id, target_kind)
);

CREATE TABLE proposal_validity_results (
    apply_operation_id TEXT NOT NULL REFERENCES post_apply_reconciliations(apply_operation_id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    source_status TEXT NOT NULL,
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL,
    conflict_path BLOB,
    evidence_bytes INTEGER NOT NULL CHECK (evidence_bytes > 0),
    result_json BLOB NOT NULL,
    PRIMARY KEY (apply_operation_id, proposal_id, version)
);

CREATE INDEX proposal_validity_results_generation_idx
    ON proposal_validity_results(apply_operation_id, generation, proposal_id, version);
