CREATE TABLE proposal_workspace_creation (
    workspace_id TEXT PRIMARY KEY REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL UNIQUE,
    nonce TEXT NOT NULL,
    phase TEXT NOT NULL,
    marker_version INTEGER NOT NULL CHECK (marker_version > 0),
    isolation_version INTEGER NOT NULL CHECK (isolation_version > 0),
    marker_sha256 TEXT NOT NULL,
    evidence_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX proposal_workspace_creation_phase_idx
    ON proposal_workspace_creation(phase, updated_at ASC, workspace_id ASC);
