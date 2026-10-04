CREATE TABLE two_factor_challenges (
    jti_hash   TEXT PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    attempts   INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    used_at    TIMESTAMPTZ
);

CREATE INDEX two_factor_challenges_user_idx ON two_factor_challenges (user_id);
CREATE INDEX two_factor_challenges_expires_idx ON two_factor_challenges (expires_at);

DROP INDEX user_sessions_previous_refresh_token_idx;

ALTER TABLE user_sessions
    DROP COLUMN rotated_at,
    DROP COLUMN previous_refresh_token_hash;
