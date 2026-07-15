CREATE TABLE proposal_result_snapshots (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL UNIQUE REFERENCES proposal_attempts(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    manifest_hash TEXT NOT NULL,
    snapshot_json BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX proposal_result_snapshots_proposal_idx
    ON proposal_result_snapshots(proposal_id, created_at ASC, id ASC);
