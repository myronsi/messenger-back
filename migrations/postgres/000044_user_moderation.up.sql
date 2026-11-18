CREATE TABLE user_reports (
    reporter_id BIGINT       NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    target_id   BIGINT       NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    reason      TEXT         NOT NULL CHECK (reason IN ('spam', 'scam', 'harassment', 'impersonation', 'inappropriate_profile', 'other')),
    note        TEXT         CHECK (char_length(note) <= 500),
    context     TEXT         NOT NULL CHECK (context IN ('target_initiated', 'shared_chat', 'none')),
    weight      NUMERIC(3,2) NOT NULL CHECK (weight >= 0 AND weight <= 1),
    snapshot    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    withdrawn_at TIMESTAMPTZ,
    PRIMARY KEY (reporter_id, target_id),
    CHECK (reporter_id <> target_id)
);

CREATE INDEX user_reports_target_idx ON user_reports (target_id, created_at);
CREATE INDEX user_reports_reporter_idx ON user_reports (reporter_id, created_at);

CREATE TABLE user_moderation (
    user_id              BIGINT       PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    status               TEXT         NOT NULL DEFAULT 'good' CHECK (status IN ('good', 'warned', 'limited', 'restricted', 'suspended')),
    score                NUMERIC(12,2) NOT NULL DEFAULT 0,
    status_since         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    probation_until      TIMESTAMPTZ,
    appeal_blocked_until TIMESTAMPTZ,
    last_appeal_at       TIMESTAMPTZ
);

CREATE INDEX user_moderation_flagged_idx ON user_moderation (user_id) WHERE status <> 'good' OR probation_until IS NOT NULL;

CREATE TABLE user_moderation_events (
    id          BIGSERIAL    PRIMARY KEY,
    user_id     BIGINT       NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    from_status TEXT         NOT NULL,
    to_status   TEXT         NOT NULL,
    score       NUMERIC(12,2) NOT NULL,
    cause       TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX user_moderation_events_user_idx ON user_moderation_events (user_id, created_at);
