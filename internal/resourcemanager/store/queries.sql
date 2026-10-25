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

-- name: LockLiveProject :one
SELECT status FROM projects WHERE id = $1 AND deleted_at IS NULL FOR SHARE;

-- name: GetReservationForUpdate :one
SELECT * FROM quota_reservations WHERE operation_id = $1 FOR UPDATE;

-- name: ListReservationDeltas :many
SELECT metric, amount FROM quota_reservation_deltas WHERE operation_id = $1 ORDER BY metric;

-- name: InsertReservation :exec
INSERT INTO quota_reservations (operation_id, project_id, state, expire_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5);

-- name: InsertReservationDelta :exec
INSERT INTO quota_reservation_deltas (operation_id, project_id, metric, amount) VALUES ($1, $2, $3, $4);

-- name: SetReservationState :exec
UPDATE quota_reservations SET state = $2, updated_at = $3 WHERE operation_id = $1;

-- name: EnsureUsage :exec
INSERT INTO quota_usage (project_id, metric, used, updated_at) VALUES ($1, $2, 0, $3)
ON CONFLICT DO NOTHING;

-- name: LockUsage :many
SELECT * FROM quota_usage WHERE project_id = $1 AND metric = ANY(@metrics::text[])
ORDER BY metric FOR UPDATE;

-- name: AddUsage :exec
UPDATE quota_usage SET used = GREATEST(used + @amount, 0), updated_at = @updated_at
WHERE project_id = @project_id AND metric = @metric;

-- name: ReservedByMetric :many
SELECT d.metric, sum(d.amount)::bigint AS reserved
FROM quota_reservation_deltas d JOIN quota_reservations r ON r.operation_id = d.operation_id
WHERE d.project_id = @project_id AND d.amount > 0 AND r.state = 'RESERVED' AND r.expire_at > @now
GROUP BY d.metric;

-- name: ListLimits :many
SELECT metric, "limit" FROM quota_limits WHERE project_id = $1;

-- name: UpsertLimit :exec
INSERT INTO quota_limits (project_id, metric, "limit", updated_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, metric) DO UPDATE SET "limit" = EXCLUDED."limit", updated_at = EXCLUDED.updated_at;

-- name: ListUsage :many
SELECT * FROM quota_usage WHERE project_id = $1;

-- name: ProjectInUse :one
SELECT EXISTS (SELECT 1 FROM quota_usage u WHERE u.project_id = @project_id AND u.used > 0)
    OR EXISTS (
        SELECT 1 FROM quota_reservation_deltas d JOIN quota_reservations r ON r.operation_id = d.operation_id
        WHERE d.project_id = @project_id AND d.amount > 0 AND r.state = 'RESERVED' AND r.expire_at > @now);

-- name: LiveProjects :many
SELECT id FROM projects WHERE id = ANY(@ids::text[]) AND deleted_at IS NULL;

-- name: ServiceUsage :many
SELECT * FROM quota_usage WHERE metric LIKE @prefix::text || '%' AND used > 0;

-- name: CorrectUsage :execrows
-- Sets used to the snapshot value unless the row changed after the
-- snapshot was counted (REQ-RM-002).
INSERT INTO quota_usage (project_id, metric, used, updated_at) VALUES (@project_id, @metric, @used, @updated_at)
ON CONFLICT (project_id, metric) DO UPDATE SET used = EXCLUDED.used, updated_at = EXCLUDED.updated_at
WHERE quota_usage.updated_at < @snapshot_time AND quota_usage.used <> EXCLUDED.used;

-- name: PurgeReservations :execrows
DELETE FROM quota_reservations WHERE updated_at < @before AND (state <> 'RESERVED' OR expire_at < @before);
