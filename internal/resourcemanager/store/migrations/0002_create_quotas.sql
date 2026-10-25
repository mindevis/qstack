-- +goose Up
-- Project quotas (spec 03 §5). Limits default to the platform values from
-- the configuration; rows here are operator overrides.
CREATE TABLE quota_limits (
    project_id text        NOT NULL REFERENCES projects (id),
    metric     text        NOT NULL,
    "limit"    bigint      NOT NULL CHECK ("limit" >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, metric)
);

-- Committed usage. Reserve and Commit lock these rows (REQ-RM-001);
-- updated_at tells the snapshot reconciliation whether a row changed after
-- the snapshot was counted (REQ-RM-002).
CREATE TABLE quota_usage (
    project_id text        NOT NULL REFERENCES projects (id),
    metric     text        NOT NULL,
    used       bigint      NOT NULL DEFAULT 0 CHECK (used >= 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, metric)
);

-- Reservations of operations; an unconfirmed one stops holding quota at
-- expire_at but can still be committed.
CREATE TABLE quota_reservations (
    operation_id text        PRIMARY KEY,
    project_id   text        NOT NULL REFERENCES projects (id),
    state        text        NOT NULL CHECK (state IN ('RESERVED', 'COMMITTED', 'RELEASED')),
    expire_at    timestamptz NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);

CREATE INDEX quota_reservations_purge ON quota_reservations (updated_at);

CREATE TABLE quota_reservation_deltas (
    operation_id text   NOT NULL REFERENCES quota_reservations (operation_id) ON DELETE CASCADE,
    project_id   text   NOT NULL,
    metric       text   NOT NULL,
    amount       bigint NOT NULL,
    PRIMARY KEY (operation_id, metric)
);

-- Sum of active positive reservations per project metric.
CREATE INDEX quota_reservation_deltas_metric ON quota_reservation_deltas (project_id, metric) WHERE amount > 0;
