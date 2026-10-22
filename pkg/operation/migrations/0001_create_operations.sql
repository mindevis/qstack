-- +goose Up
-- Long-running operations of the owner service (spec 02 §5.3, REQ-PLT-042).
CREATE TABLE operations (
    id               text        PRIMARY KEY,
    project_id       text        NOT NULL DEFAULT '',
    resource_id      text        NOT NULL,
    description      text        NOT NULL,
    created_by       text        NOT NULL DEFAULT '',
    metadata         bytea       NOT NULL,           -- google.protobuf.Any
    done             boolean     NOT NULL DEFAULT false,
    error            bytea,                          -- google.rpc.Status
    response         bytea,                          -- google.protobuf.Any
    cancel_requested boolean     NOT NULL DEFAULT false,
    create_time      timestamptz NOT NULL,
    modify_time      timestamptz NOT NULL,
    expire_at        timestamptz,                    -- set when done: retention 30 days
    CHECK (NOT done OR (error IS NULL) <> (response IS NULL))
);

-- REQ-PLT-043: at most one running mutating operation per resource.
CREATE UNIQUE INDEX operations_resource_running ON operations (resource_id) WHERE NOT done;
CREATE INDEX operations_project ON operations (project_id, create_time DESC, id DESC);
CREATE INDEX operations_resource ON operations (resource_id, create_time DESC, id DESC);
CREATE INDEX operations_expire ON operations (expire_at) WHERE done;

-- Idempotency keys (spec 02 §5.4, REQ-PLT-050), written with the operation.
CREATE TABLE idempotency_keys (
    subject      text        NOT NULL,
    key          text        NOT NULL,
    request_hash bytea       NOT NULL,
    operation_id text        NOT NULL REFERENCES operations (id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    expire_at    timestamptz NOT NULL,
    PRIMARY KEY (subject, key)
);

CREATE INDEX idempotency_keys_expire ON idempotency_keys (expire_at);
CREATE INDEX idempotency_keys_operation ON idempotency_keys (operation_id);
