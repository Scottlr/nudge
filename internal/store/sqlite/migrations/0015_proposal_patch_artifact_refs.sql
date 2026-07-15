ALTER TABLE proposal_versions ADD COLUMN artifact_id TEXT;
ALTER TABLE proposal_versions ADD COLUMN artifact_spool_id TEXT;
ALTER TABLE proposal_versions ADD COLUMN artifact_manifest_hash TEXT;
ALTER TABLE proposal_versions ADD COLUMN artifact_index_hash TEXT;
