CREATE TABLE provider_conversations (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL UNIQUE REFERENCES review_threads(id) ON DELETE CASCADE,
    provider_name TEXT NOT NULL,
    provider_conversation_ref TEXT,
    provider_session_ref TEXT,
    provider_version TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    state TEXT NOT NULL,
    error_code TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX provider_conversations_thread_idx
    ON provider_conversations(thread_id, updated_at DESC, id ASC);

CREATE TABLE provider_turns (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES review_threads(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES provider_conversations(id) ON DELETE CASCADE,
    provider_turn_ref TEXT,
    operation_id TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    state TEXT NOT NULL,
    provider_version TEXT NOT NULL,
    request_expression_version TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    error_code TEXT NOT NULL
);

CREATE INDEX provider_turns_thread_idx
    ON provider_turns(thread_id, started_at ASC, id ASC);
