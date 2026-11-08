DELETE FROM webauthn_challenges WHERE purpose='recovery_approval';
ALTER TABLE webauthn_challenges DROP CONSTRAINT webauthn_challenges_purpose_check;
ALTER TABLE webauthn_challenges ADD CONSTRAINT webauthn_challenges_purpose_check CHECK (purpose IN ('registration','authentication'));
