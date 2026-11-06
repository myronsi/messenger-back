ALTER TABLE recovery_requests DROP COLUMN location, ADD COLUMN ip_hint TEXT NOT NULL DEFAULT 'Unknown network';
ALTER TABLE recovery_requests ALTER COLUMN ip_hint DROP DEFAULT;
ALTER TABLE user_security_events DROP COLUMN location;
ALTER TABLE user_sessions DROP COLUMN location;
