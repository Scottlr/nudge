CREATE TABLE proposal_workspace_lifecycle (
    operation_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES proposal_workspaces(id) ON DELETE CASCADE,
    owner TEXT NOT NULL,
    nonce TEXT NOT NULL,
    purpose TEXT NOT NULL,
    phase TEXT NOT NULL,
    capacity_reservation_marker TEXT NOT NULL,
    lifecycle_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX proposal_workspace_lifecycle_workspace_idx
    ON proposal_workspace_lifecycle(workspace_id, updated_at DESC, operation_id DESC);
