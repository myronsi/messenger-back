ALTER TABLE users
    ADD COLUMN terms_version TEXT NULL,
    ADD COLUMN terms_accepted_at TIMESTAMPTZ NULL,
    ADD COLUMN age_confirmed_at TIMESTAMPTZ NULL;
