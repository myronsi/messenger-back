ALTER TABLE user_sessions
    ADD COLUMN device_key_thumbprint TEXT,
    ADD COLUMN device_key_bound_at TIMESTAMPTZ,
    ADD CONSTRAINT user_sessions_device_key_check CHECK (
        (device_key_thumbprint IS NULL AND device_key_bound_at IS NULL)
        OR (char_length(device_key_thumbprint) = 43 AND device_key_bound_at IS NOT NULL)
    );
