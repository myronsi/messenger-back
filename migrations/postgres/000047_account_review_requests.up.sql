CREATE TABLE account_review_requests (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status       TEXT        NOT NULL CHECK (status IN ('warned', 'limited', 'restricted', 'suspended')),
    text         TEXT        NOT NULL CHECK (char_length(text) BETWEEN 1 AND 500),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at    TIMESTAMPTZ,
    close_reason TEXT        CHECK (close_reason IN ('decided', 'status_good')),
    CHECK ((closed_at IS NULL) = (close_reason IS NULL))
);

CREATE UNIQUE INDEX account_review_requests_open_key ON account_review_requests (user_id) WHERE closed_at IS NULL;
CREATE INDEX account_review_requests_user_idx ON account_review_requests (user_id, created_at);
