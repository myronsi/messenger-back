DROP INDEX recovery_requests_approval_hash_idx;
ALTER TABLE recovery_requests DROP COLUMN approval_hash;
