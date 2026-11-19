DELETE FROM recovery_mail_queue WHERE kind = 'inactive_account';
ALTER TABLE recovery_mail_queue DROP CONSTRAINT recovery_mail_queue_kind_check;
ALTER TABLE recovery_mail_queue ADD CONSTRAINT recovery_mail_queue_kind_check
    CHECK (kind IN ('verify_email', 'password_reset'));

DROP INDEX IF EXISTS users_last_seen_idx;
DROP TABLE IF EXISTS inactive_account_warnings;
