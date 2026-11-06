CREATE TABLE qr_login_requests (
    id_hash TEXT PRIMARY KEY,
    poll_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','scanned','approved','denied')),
    user_agent TEXT CHECK (char_length(user_agent) <= 512),
    location JSONB,
    scanned_by BIGINT REFERENCES users(id) ON DELETE CASCADE,
    approved_by BIGINT REFERENCES users(id) ON DELETE CASCADE,
    approved_session UUID REFERENCES user_sessions(id) ON DELETE SET NULL,
    password_fingerprint TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);
CREATE INDEX qr_login_requests_expires_idx ON qr_login_requests(expires_at);
CREATE INDEX qr_login_requests_scanned_by_idx ON qr_login_requests(scanned_by);
CREATE INDEX qr_login_requests_approved_by_idx ON qr_login_requests(approved_by);
CREATE INDEX qr_login_requests_approved_session_idx ON qr_login_requests(approved_session);
