-- A high-entropy recovery key is shown once. Only its keyed hash is retained.
CREATE TABLE user_recovery_keys (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    key_hash TEXT NOT NULL CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE user_security_settings ADD COLUMN recovery_locked_until TIMESTAMPTZ;
