ALTER TABLE cleanup_operations
    ADD COLUMN completed_resources_json BLOB NOT NULL DEFAULT '[]';
