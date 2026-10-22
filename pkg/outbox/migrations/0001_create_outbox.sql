-- +goose Up
-- Transactional outbox (spec 02 §8, REQ-PLT-090): events are inserted in the
-- transaction of the change and deleted by the relay once NATS acknowledged
-- them.
CREATE TABLE outbox (
    seq        bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- relay order
    event_id   text        NOT NULL UNIQUE,
    subject    text        NOT NULL,                                 -- NATS subject
    payload    bytea       NOT NULL,                                 -- CloudEvents JSON envelope
    created_at timestamptz NOT NULL
);
