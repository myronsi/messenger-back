-- Schema v2, part 6: refresh token reuse detection.
-- A refresh rotates the token; the hash of the previous token stays on the session so that presenting it again
-- (a stolen or replayed token) is recognised and the session can be revoked.

ALTER TABLE user_sessions
    ADD COLUMN previous_refresh_token_hash TEXT,
    ADD COLUMN rotated_at                  TIMESTAMPTZ;

CREATE INDEX user_sessions_previous_refresh_token_idx ON user_sessions (previous_refresh_token_hash)
    WHERE previous_refresh_token_hash IS NOT NULL;

-- Login challenges of the second factor live in Redis (five minutes, attempt counter), not here.
DROP TABLE two_factor_challenges;
