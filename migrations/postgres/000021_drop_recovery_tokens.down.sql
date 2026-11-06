CREATE TABLE recovery_tokens (
    jti_hash   TEXT PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE INDEX recovery_tokens_user_idx ON recovery_tokens (user_id);
CREATE INDEX recovery_tokens_expires_idx ON recovery_tokens (expires_at);
