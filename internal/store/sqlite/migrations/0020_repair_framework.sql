CREATE TABLE repair_plans (
    id TEXT PRIMARY KEY,
    health_code TEXT NOT NULL,
    health_revision TEXT NOT NULL,
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    summary TEXT NOT NULL,
    effect TEXT NOT NULL,
    owned_resource_refs_json TEXT NOT NULL,
    preconditions_hash TEXT NOT NULL,
    confirmation_text TEXT NOT NULL,
    handler_kind TEXT NOT NULL,
    handler_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX repair_plans_health_idx
    ON repair_plans(health_revision, health_code, id);

CREATE TABLE repair_operations (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL REFERENCES repair_plans(id),
    handler_kind TEXT NOT NULL,
    handler_version TEXT NOT NULL,
    health_revision TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    phase TEXT NOT NULL,
    outcome TEXT NOT NULL,
    error_code TEXT NOT NULL DEFAULT '',
    preconditions_hash TEXT NOT NULL,
    lock_proof TEXT NOT NULL DEFAULT '',
    journal_id TEXT NOT NULL DEFAULT '',
    effect_id TEXT NOT NULL DEFAULT '',
    postcondition_hash TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX repair_operations_plan_idx
    ON repair_operations(plan_id, updated_at DESC, id ASC);

CREATE TABLE repair_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id TEXT NOT NULL REFERENCES repair_operations(id),
    plan_id TEXT NOT NULL REFERENCES repair_plans(id),
    phase TEXT NOT NULL,
    code TEXT NOT NULL,
    at TEXT NOT NULL
);

CREATE INDEX repair_audit_operation_idx
    ON repair_audit(operation_id, id ASC);
