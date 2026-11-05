ALTER TABLE user_security_settings ADD COLUMN recovery_requests_blocked_until TIMESTAMPTZ;

CREATE TABLE recovery_requests (
    id_hash TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    match_number SMALLINT NOT NULL CHECK (match_number BETWEEN 10 AND 99),
    choices JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','used')),
    user_agent TEXT NOT NULL,
    ip_hint TEXT NOT NULL,
    approved_by_session UUID REFERENCES user_sessions(id) ON DELETE SET NULL,
    approved_proof JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX recovery_requests_user_status_idx ON recovery_requests(user_id,status,expires_at);
CREATE INDEX recovery_requests_expires_idx ON recovery_requests(expires_at);
CREATE INDEX recovery_requests_approved_session_idx ON recovery_requests(approved_by_session);
