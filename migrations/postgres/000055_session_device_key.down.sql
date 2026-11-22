ALTER TABLE user_sessions
    DROP CONSTRAINT IF EXISTS user_sessions_device_key_check,
    DROP COLUMN IF EXISTS device_key_bound_at,
    DROP COLUMN IF EXISTS device_key_thumbprint;
