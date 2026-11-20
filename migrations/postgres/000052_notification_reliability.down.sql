ALTER TABLE notification_jobs DROP COLUMN IF EXISTS deferred;
ALTER TABLE notification_devices DROP COLUMN IF EXISTS failures;
