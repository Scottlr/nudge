CREATE TABLE capacity_reservations (
    reservation_id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    repository_id TEXT,
    plan_digest TEXT NOT NULL,
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    accounting_version INTEGER NOT NULL CHECK (accounting_version > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'consumed', 'released')),
    retained_bytes INTEGER NOT NULL CHECK (retained_bytes >= 0),
    idempotency_key TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX capacity_reservations_idempotency_idx
    ON capacity_reservations(idempotency_key);
CREATE INDEX capacity_reservations_repository_idx
    ON capacity_reservations(repository_id, state, created_at, reservation_id);

CREATE TABLE capacity_reservation_volumes (
    reservation_id TEXT NOT NULL REFERENCES capacity_reservations(reservation_id) ON DELETE CASCADE,
    volume_id TEXT NOT NULL,
    peak_bytes INTEGER NOT NULL CHECK (peak_bytes >= 0),
    retained_bytes INTEGER NOT NULL CHECK (retained_bytes >= 0),
    PRIMARY KEY (reservation_id, volume_id)
);

CREATE TABLE owned_artifact_ledger (
    artifact_id TEXT PRIMARY KEY,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    reservation_id TEXT NOT NULL REFERENCES capacity_reservations(reservation_id),
    repository_id TEXT,
    class TEXT NOT NULL,
    lifecycle TEXT NOT NULL CHECK (lifecycle IN ('accepted', 'accounting_uncertain')),
    logical_bytes INTEGER NOT NULL CHECK (logical_bytes >= 0),
    observed_bytes INTEGER NOT NULL CHECK (observed_bytes >= 0),
    charged_bytes INTEGER NOT NULL CHECK (charged_bytes >= 0),
    volume_id TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    accounting_version INTEGER NOT NULL CHECK (accounting_version > 0),
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    complete INTEGER NOT NULL CHECK (complete IN (0, 1)),
    created_at TEXT NOT NULL,
    UNIQUE (reservation_id, artifact_id)
);

CREATE INDEX owned_artifact_ledger_repository_idx
    ON owned_artifact_ledger(repository_id, lifecycle, created_at, artifact_id);
CREATE INDEX owned_artifact_ledger_reservation_idx
    ON owned_artifact_ledger(reservation_id, artifact_id);

CREATE TABLE storage_totals (
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global', 'repository')),
    scope_id TEXT NOT NULL,
    logical_bytes INTEGER NOT NULL CHECK (logical_bytes >= 0),
    observed_bytes INTEGER NOT NULL CHECK (observed_bytes >= 0),
    charged_bytes INTEGER NOT NULL CHECK (charged_bytes >= 0),
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    uncertain_count INTEGER NOT NULL CHECK (uncertain_count >= 0),
    ledger_revision INTEGER NOT NULL CHECK (ledger_revision >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope_kind, scope_id)
);

CREATE TABLE storage_ledger_operations (
    kind TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (kind, idempotency_key)
);

CREATE INDEX storage_ledger_operations_reservation_idx
    ON storage_ledger_operations(reservation_id, kind, idempotency_key);
