CREATE TABLE user_webauthn_handles (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    handle BYTEA NOT NULL UNIQUE
);

CREATE TABLE user_passkeys (
    credential_id BYTEA PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential TEXT NOT NULL,
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE INDEX user_passkeys_user_idx ON user_passkeys(user_id);

CREATE TABLE webauthn_challenges (
    id_hash TEXT PRIMARY KEY,
    user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    session_id UUID REFERENCES user_sessions(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('registration','authentication')),
    payload JSONB NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX webauthn_challenges_user_idx ON webauthn_challenges(user_id);
CREATE INDEX webauthn_challenges_session_idx ON webauthn_challenges(session_id);
CREATE INDEX webauthn_challenges_expiry_idx ON webauthn_challenges(expires_at);

CREATE TABLE recovery_emails (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    address TEXT NOT NULL,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX recovery_emails_verified_address_idx ON recovery_emails(lower(address)) WHERE verified_at IS NOT NULL;

CREATE TABLE email_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    address TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('verify_email','password_reset')),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);
CREATE INDEX email_tokens_user_idx ON email_tokens(user_id);
CREATE INDEX email_tokens_expiry_idx ON email_tokens(expires_at);

CREATE TABLE recovery_mail_queue (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('verify_email','password_reset')),
    payload_sealed TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX recovery_mail_queue_ready_idx ON recovery_mail_queue(available_at,id);
CREATE INDEX recovery_mail_queue_created_idx ON recovery_mail_queue(created_at);
