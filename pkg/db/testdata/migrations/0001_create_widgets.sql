-- +goose Up
-- Example resource table following spec 02 §7.
CREATE TABLE widgets (
    id          text        PRIMARY KEY,
    project_id  text        NOT NULL,
    name        text        NOT NULL,
    labels      jsonb       NOT NULL DEFAULT '{}',
    version     bigint      NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz
);

-- Names are unique among live resources of a project.
CREATE UNIQUE INDEX widgets_project_name_live ON widgets (project_id, name) WHERE deleted_at IS NULL;
