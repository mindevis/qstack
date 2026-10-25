-- +goose Up
-- Organizations (spec 03 §2). version backs the etag; access-binding
-- changes bump it too, so the etag also guards SetAccessBindings.
CREATE TABLE organizations (
    id                 text        PRIMARY KEY,
    name               text        NOT NULL,
    display_name       text        NOT NULL DEFAULT '',
    description        text        NOT NULL DEFAULT '',
    labels             jsonb       NOT NULL DEFAULT '{}',
    owner_user_id      text        NOT NULL,
    billing_account_id text        NOT NULL DEFAULT '',
    status             text        NOT NULL CHECK (status IN ('PENDING_APPROVAL', 'ACTIVE', 'REJECTED', 'SUSPENDED', 'DELETING')),
    iac_tool           text        NOT NULL DEFAULT 'UNSPECIFIED' CHECK (iac_tool IN ('UNSPECIFIED', 'OPENTOFU', 'TERRAFORM')),
    version            bigint      NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    deleted_at         timestamptz
);

CREATE UNIQUE INDEX organizations_name ON organizations (name) WHERE deleted_at IS NULL;
CREATE INDEX organizations_list ON organizations (created_at, id) WHERE deleted_at IS NULL;

-- Projects (spec 03 §2); deleted projects keep their row (deleted_at) and
-- free the name.
CREATE TABLE projects (
    id               text        PRIMARY KEY,
    organization_id  text        NOT NULL REFERENCES organizations (id),
    name             text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    labels           jsonb       NOT NULL DEFAULT '{}',
    status           text        NOT NULL CHECK (status IN ('ACTIVE', 'SUSPENDED', 'DELETING', 'DELETED')),
    version          bigint      NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    deleted_at       timestamptz,
    CHECK ((status = 'DELETED') = (deleted_at IS NOT NULL))
);

CREATE UNIQUE INDEX projects_name ON projects (organization_id, name) WHERE deleted_at IS NULL;
CREATE INDEX projects_list ON projects (organization_id, created_at, id) WHERE deleted_at IS NULL;

-- Access bindings held by organizations and projects (spec 03 §2, §4.1).
CREATE TABLE access_bindings (
    id              text        PRIMARY KEY,
    resource_type   text        NOT NULL CHECK (resource_type IN ('organization', 'project')),
    resource_id     text        NOT NULL,
    organization_id text        NOT NULL REFERENCES organizations (id),
    role_id         text        NOT NULL,
    subject_type    text        NOT NULL CHECK (subject_type IN ('userAccount', 'serviceAccount', 'group', 'system')),
    subject_id      text        NOT NULL,
    created_at      timestamptz NOT NULL,
    UNIQUE (resource_id, role_id, subject_type, subject_id)
);

CREATE INDEX access_bindings_resource ON access_bindings (resource_id, id);
-- Organizations a subject has bindings in (ListOrganizations).
CREATE INDEX access_bindings_subject ON access_bindings (subject_id, organization_id);
