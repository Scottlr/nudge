CREATE TABLE message_body_chunks (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    byte_length INTEGER NOT NULL CHECK (byte_length > 0 AND byte_length <= 262144),
    chunk_sha256 TEXT NOT NULL,
    bytes BLOB NOT NULL,
    PRIMARY KEY (message_id, ordinal)
);

CREATE TABLE message_body_identities (
    message_id TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    chunk_count INTEGER NOT NULL CHECK (chunk_count >= 0),
    byte_length INTEGER NOT NULL CHECK (byte_length >= 0 AND byte_length <= 8388608),
    body_sha256 TEXT NOT NULL,
    terminal_status TEXT NOT NULL,
    failure_phase TEXT NOT NULL,
    error_code TEXT NOT NULL,
    completed_at TEXT NOT NULL
);

CREATE INDEX message_body_chunks_order_idx ON message_body_chunks(message_id, ordinal ASC);
