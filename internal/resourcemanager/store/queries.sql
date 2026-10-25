-- name: InsertOrganization :exec
INSERT INTO organizations (id, name, display_name, description, labels, owner_user_id, billing_account_id, status, iac_tool, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: GetOrganization :one
SELECT * FROM organizations WHERE id = $1 AND deleted_at IS NULL;

-- name: GetOrganizationForUpdate :one
SELECT * FROM organizations WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: GetOrganizationByName :one
SELECT * FROM organizations WHERE name = $1 AND deleted_at IS NULL;

-- name: UpdateOrganization :execrows
UPDATE organizations
SET display_name = @display_name, description = @description, labels = @labels, iac_tool = @iac_tool,
    version = @version, updated_at = @updated_at
WHERE id = @id AND version = @prev_version AND deleted_at IS NULL;

-- name: InsertProject :exec
INSERT INTO projects (id, organization_id, name, description, labels, status, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetProject :one
SELECT * FROM projects WHERE id = $1 AND deleted_at IS NULL;

-- name: GetProjectForUpdate :one
SELECT * FROM projects WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;

-- name: UpdateProject :execrows
UPDATE projects
SET name = @name, description = @description, labels = @labels, status = @status,
    version = @version, updated_at = @updated_at, deleted_at = @deleted_at
WHERE id = @id AND version = @prev_version AND deleted_at IS NULL;

-- name: ListBindings :many
SELECT * FROM access_bindings
WHERE resource_id = $1 ORDER BY id;

-- name: ListBindingsPage :many
SELECT * FROM access_bindings
WHERE resource_id = @resource_id AND id > @after_id ORDER BY id LIMIT @lim;

-- name: InsertBinding :exec
INSERT INTO access_bindings (id, resource_type, resource_id, organization_id, role_id, subject_type, subject_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: DeleteBinding :exec
DELETE FROM access_bindings WHERE id = $1;

-- name: DeleteResourceBindings :exec
DELETE FROM access_bindings WHERE resource_id = $1;
