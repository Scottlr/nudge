CREATE TABLE apply_operations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES review_sessions(id) ON DELETE CASCADE,
    proposal_id TEXT NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    proposal_version INTEGER NOT NULL CHECK (proposal_version > 0),
    idempotency_key TEXT NOT NULL,
    phase TEXT NOT NULL,
    operation_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    prepared_at TEXT NOT NULL,
    UNIQUE(session_id, idempotency_key),
    UNIQUE(proposal_id, proposal_version)
);

CREATE INDEX apply_operations_destination_idx
    ON apply_operations(workspace_id, phase, prepared_at ASC, id ASC);
