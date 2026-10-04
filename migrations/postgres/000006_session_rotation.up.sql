-- Schema v2, part 6: refresh token reuse detection.
-- Every refresh rotates the token. The hashes of all tokens a session has used stay in a child table, so that
-- presenting any of them again (a stolen or replayed token) is recognised and the session can be revoked,
-- however many rotations ago it was replaced.

CREATE TABLE user_session_rotated_tokens (
    token_hash TEXT        PRIMARY KEY,
    session_id UUID        NOT NULL REFERENCES user_sessions (id) ON DELETE CASCADE,
    rotated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX user_session_rotated_tokens_session_idx ON user_session_rotated_tokens (session_id);
-- The worker prunes old history rows by age.
CREATE INDEX user_session_rotated_tokens_rotated_idx ON user_session_rotated_tokens (rotated_at);
-- Ended sessions are deleted by age as well (expires_at is already indexed); this one serves revoked_at.
CREATE INDEX user_sessions_revoked_idx ON user_sessions (revoked_at) WHERE revoked_at IS NOT NULL;

-- Login challenges of the second factor live in Redis (five minutes, attempt counter), not here.
DROP TABLE two_factor_challenges;