-- Recreate the legacy shape for rollback; historical values remain in backups.
CREATE TABLE user_recovery_shares (
    user_id BIGINT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    encrypted_cloud_part TEXT,
    salt BYTEA,
    verification_ciphertext TEXT
);
