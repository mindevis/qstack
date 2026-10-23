-- +goose Up
-- Shard leases of controllers (spec 02 §9). Times come from the database
-- clock, so replicas never compare their own clocks.
CREATE TABLE controller_leases (
    controller text        NOT NULL,
    shard      integer     NOT NULL,
    holder     text        NOT NULL DEFAULT '', -- '' = free
    expire_at  timestamptz NOT NULL DEFAULT '-infinity',
    PRIMARY KEY (controller, shard)
);

-- Live replicas of a controller; each takes ceil(shards / members) shards.
CREATE TABLE controller_members (
    controller text        NOT NULL,
    member     text        NOT NULL,
    expire_at  timestamptz NOT NULL,
    PRIMARY KEY (controller, member)
);
