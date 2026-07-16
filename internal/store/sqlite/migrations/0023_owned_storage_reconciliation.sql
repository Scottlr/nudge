CREATE TABLE storage_reconciliation_epochs (
    epoch_id TEXT PRIMARY KEY,
    repository_id TEXT,
    ledger_revision INTEGER NOT NULL CHECK (ledger_revision >= 0),
    policy_version INTEGER NOT NULL CHECK (policy_version > 0),
    cursor TEXT NOT NULL,
    next_cursor TEXT NOT NULL,
    batch_key TEXT NOT NULL,
    processed_items INTEGER NOT NULL CHECK (processed_items >= 0),
    discrepancy_count INTEGER NOT NULL CHECK (discrepancy_count >= 0),
    evidence_bytes INTEGER NOT NULL CHECK (evidence_bytes >= 0),
    uncertainty_count INTEGER NOT NULL CHECK (uncertainty_count >= 0),
    complete INTEGER NOT NULL CHECK (complete IN (0, 1)),
    updated_at TEXT NOT NULL
);

CREATE INDEX storage_reconciliation_epochs_scope_idx
    ON storage_reconciliation_epochs(repository_id, ledger_revision, updated_at);

CREATE TABLE storage_reconciliation_discrepancies (
    epoch_id TEXT NOT NULL REFERENCES storage_reconciliation_epochs(epoch_id) ON DELETE CASCADE,
    batch_key TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL,
    owner_kind TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    repository_id TEXT,
    volume_id TEXT NOT NULL,
    marker_nonce TEXT NOT NULL,
    expected_manifest_hash TEXT NOT NULL,
    observed_manifest_hash TEXT NOT NULL,
    expected_bytes INTEGER NOT NULL CHECK (expected_bytes >= 0),
    observed_bytes INTEGER NOT NULL CHECK (observed_bytes >= 0),
    evidence_code TEXT NOT NULL,
    plan_eligible INTEGER NOT NULL CHECK (plan_eligible IN (0, 1)),
    handler_kind TEXT NOT NULL,
    handler_version TEXT NOT NULL,
    preconditions_hash TEXT NOT NULL,
    PRIMARY KEY (epoch_id, batch_key, ordinal)
);

CREATE INDEX storage_reconciliation_discrepancies_kind_idx
    ON storage_reconciliation_discrepancies(epoch_id, kind, owner_id, artifact_id, reservation_id);
