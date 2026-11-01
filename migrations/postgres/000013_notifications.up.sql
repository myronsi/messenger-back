CREATE TABLE notification_preferences (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT true,
    sound BOOLEAN NOT NULL DEFAULT false,
    previews BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE notification_mutes (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id BIGINT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    until_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, chat_id)
);
CREATE TABLE notification_devices (
    id UUID PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('web', 'android', 'ios')),
    destination TEXT NOT NULL UNIQUE,
    p256dh TEXT NOT NULL DEFAULT '',
    auth TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notification_devices_user ON notification_devices(user_id);
CREATE INDEX notification_devices_session ON notification_devices(session_id);
CREATE INDEX notification_mutes_chat ON notification_mutes(chat_id);
CREATE TABLE notification_activity (
    device_id UUID NOT NULL REFERENCES notification_devices(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    client_id UUID NOT NULL,
    active_until TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (device_id, client_id)
);
CREATE INDEX notification_activity_expiry ON notification_activity(active_until);
CREATE INDEX notification_activity_session ON notification_activity(session_id);
-- Intent is committed before the Scylla write. Workers verify the message exists before sending.
-- A failed or interrupted write produces no notification and expires after fifteen minutes.
CREATE TABLE notification_jobs (
    message_id BIGINT PRIMARY KEY,
    chat_id BIGINT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    recipients BIGINT[] NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '5 seconds',
    attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notification_jobs_due ON notification_jobs(next_at);
CREATE INDEX notification_jobs_chat ON notification_jobs(chat_id);
CREATE TABLE notification_deliveries (
    message_id BIGINT NOT NULL REFERENCES notification_jobs(message_id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES notification_devices(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, device_id)
);
CREATE INDEX notification_deliveries_device ON notification_deliveries(device_id);
