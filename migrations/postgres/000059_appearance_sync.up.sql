ALTER TABLE attachments DROP CONSTRAINT attachments_purpose_check;
ALTER TABLE attachments ADD CONSTRAINT attachments_purpose_check
    CHECK (purpose IN ('message', 'avatar', 'sticker', 'profile_music', 'profile_background', 'emoji', 'chat_wallpaper'));

CREATE TABLE user_client_settings (
    user_id          BIGINT      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    appearance       JSONB,
    recent_emoji     TEXT[]      NOT NULL DEFAULT '{}' CHECK (cardinality(recent_emoji) <= 24),
    recent_reactions TEXT[]      NOT NULL DEFAULT '{}' CHECK (cardinality(recent_reactions) <= 8),
    version          BIGINT      NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE chat_wallpapers (
    user_id       BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    chat_id       BIGINT      REFERENCES chats (id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL CHECK (kind IN ('preset', 'color', 'media')),
    preset        TEXT,
    colors        TEXT[],
    pattern       TEXT        CHECK (pattern IN ('none', 'dots', 'grid')),
    attachment_id UUID        REFERENCES attachments (id) ON DELETE CASCADE,
    blur          SMALLINT    CHECK (blur BETWEEN 0 AND 20),
    dim           SMALLINT    CHECK (dim BETWEEN 0 AND 60),
    set_by        BIGINT      NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((kind = 'media') = (attachment_id IS NOT NULL)),
    CHECK ((kind = 'preset') = (preset IS NOT NULL)),
    CHECK ((kind = 'color') = (colors IS NOT NULL))
);

CREATE UNIQUE INDEX chat_wallpapers_user_chat_idx ON chat_wallpapers (user_id, chat_id) NULLS NOT DISTINCT;
CREATE INDEX chat_wallpapers_chat_idx ON chat_wallpapers (chat_id);
CREATE INDEX chat_wallpapers_attachment_idx ON chat_wallpapers (attachment_id);

CREATE TABLE playlists (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    version    BIGINT      NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX playlists_user_idx ON playlists (user_id, created_at DESC, id DESC);

CREATE TABLE playlist_tracks (
    playlist_id      BIGINT           NOT NULL REFERENCES playlists (id) ON DELETE CASCADE,
    position         INTEGER          NOT NULL CHECK (position BETWEEN 0 AND 999),
    attachment_id    UUID             NOT NULL,
    chat_id          BIGINT,
    message_id       BIGINT,
    title            TEXT             NOT NULL,
    artist           TEXT,
    duration_seconds DOUBLE PRECISION,
    PRIMARY KEY (playlist_id, position),
    UNIQUE (playlist_id, attachment_id)
);
