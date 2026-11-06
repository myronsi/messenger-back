ALTER TABLE user_sessions ADD COLUMN location JSONB;
ALTER TABLE user_security_events ADD COLUMN location JSONB;
ALTER TABLE recovery_requests DROP COLUMN ip_hint, ADD COLUMN location JSONB;
