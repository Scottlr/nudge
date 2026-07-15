CREATE TABLE runtime_approval_records (
    id TEXT PRIMARY KEY,
    turn_id TEXT NOT NULL REFERENCES provider_turns(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    scope_class TEXT NOT NULL,
    executable_name TEXT NOT NULL,
    argument_hash TEXT NOT NULL,
    network_host_class TEXT NOT NULL,
    decision TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    resolved_at TEXT NOT NULL
);

CREATE INDEX runtime_approval_records_turn_idx ON runtime_approval_records(turn_id, resolved_at ASC);
