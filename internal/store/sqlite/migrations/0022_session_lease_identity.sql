ALTER TABLE review_sessions ADD COLUMN writer_lock_identity TEXT NOT NULL DEFAULT '';
ALTER TABLE review_sessions ADD COLUMN writer_lock_distinct INTEGER NOT NULL DEFAULT 0;
