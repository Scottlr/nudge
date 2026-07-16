CREATE TABLE workspace_retirements (
    operation_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL UNIQUE REFERENCES proposal_workspaces(id) ON DELETE RESTRICT,
    phase TEXT NOT NULL,
    retirement_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX workspace_retirements_phase_idx
    ON workspace_retirements(phase, updated_at ASC, workspace_id ASC);

CREATE TABLE workspace_retention_cursor (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    after_workspace_id TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
