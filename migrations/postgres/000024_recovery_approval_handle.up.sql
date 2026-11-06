ALTER TABLE recovery_requests ADD COLUMN approval_hash TEXT;
CREATE UNIQUE INDEX recovery_requests_approval_hash_idx ON recovery_requests(approval_hash);
