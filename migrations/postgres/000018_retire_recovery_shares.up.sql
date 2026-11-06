-- The retired Python stack is archived. Recovery keys have replaced Shamir
-- shares; the application no longer reads this table. Deployment takes the
-- normal PostgreSQL backup before applying this migration.
DROP TABLE user_recovery_shares;
