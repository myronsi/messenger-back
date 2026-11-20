ALTER TABLE notification_devices ADD COLUMN failures SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE notification_jobs ADD COLUMN deferred BOOLEAN NOT NULL DEFAULT false;
