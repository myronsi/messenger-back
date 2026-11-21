CREATE TABLE message_pins (
    chat_id    BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL,
    pinned_by  BIGINT      NOT NULL,
    pinned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (chat_id, message_id)
);

CREATE INDEX message_pins_order_idx ON message_pins (chat_id, pinned_at DESC, message_id DESC);

CREATE TABLE message_pins_personal (
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    chat_id    BIGINT      NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL,
    pinned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_id, message_id)
);

CREATE INDEX message_pins_personal_message_idx ON message_pins_personal (chat_id, message_id);

CREATE TABLE message_pin_hides (
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    chat_id    BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    PRIMARY KEY (user_id, chat_id, message_id),
    FOREIGN KEY (chat_id, message_id) REFERENCES message_pins (chat_id, message_id) ON DELETE CASCADE
);

CREATE INDEX message_pin_hides_message_idx ON message_pin_hides (chat_id, message_id);
