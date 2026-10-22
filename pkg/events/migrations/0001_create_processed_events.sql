-- +goose Up
-- Events already handled by a consumer of this service (spec 02 §8): the row
-- is written in the transaction of the handler's changes, so an event is
-- applied exactly once despite at-least-once delivery.
CREATE TABLE processed_events (
    consumer     text        NOT NULL,
    event_id     text        NOT NULL,
    processed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, event_id)
);

CREATE INDEX processed_events_processed_at ON processed_events (processed_at);
