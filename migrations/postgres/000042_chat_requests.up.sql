ALTER TABLE participants ADD COLUMN is_request BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX participants_requests_idx ON participants (user_id) WHERE is_request;
