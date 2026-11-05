CREATE TABLE group_role_events (
    id         BIGSERIAL   PRIMARY KEY,
    chat_id    BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    actor_id   BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    target_id  BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX group_role_events_chat ON group_role_events (chat_id, id DESC);
CREATE INDEX group_role_events_actor ON group_role_events (actor_id);
CREATE INDEX group_role_events_target ON group_role_events (target_id);
