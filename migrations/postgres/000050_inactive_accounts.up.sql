CREATE TABLE inactive_account_warnings (
    user_id    BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    warned_at  TIMESTAMPTZ NOT NULL,
    deletes_at TIMESTAMPTZ NOT NULL,
    CHECK (deletes_at > warned_at)
);

CREATE INDEX users_last_seen_idx ON users (last_seen_at);

ALTER TABLE recovery_mail_queue DROP CONSTRAINT recovery_mail_queue_kind_check;
ALTER TABLE recovery_mail_queue ADD CONSTRAINT recovery_mail_queue_kind_check
    CHECK (kind IN ('verify_email', 'password_reset', 'inactive_account'));
