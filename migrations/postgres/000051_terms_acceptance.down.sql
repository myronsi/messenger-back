ALTER TABLE users
    DROP COLUMN IF EXISTS age_confirmed_at,
    DROP COLUMN IF EXISTS terms_accepted_at,
    DROP COLUMN IF EXISTS terms_version;
