DROP TABLE IF EXISTS call_start_requests;
DROP TABLE IF EXISTS call_key_envelopes;
DROP TABLE IF EXISTS call_identities;
DROP TABLE IF EXISTS call_participants;
DROP TABLE IF EXISTS calls;

ALTER TABLE user_privacy_settings DROP COLUMN IF EXISTS calls_enabled;
