package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store/sqlcdb"
	"github.com/mindevis/qstack/pkg/filter"
	"github.com/mindevis/qstack/pkg/pagination"
)

// Filterable and sortable fields of List (spec 02 §5.2).
var (
	ProjectFilter = filter.Schema{
		"name":   {Column: "name"},
		"status": {Column: "status", Values: []string{"ACTIVE", "SUSPENDED", "DELETING"}},
		"labels": {Column: "labels", Labels: true},
	}
	OrganizationFilter = filter.Schema{
		"name":   {Column: "name"},
		"status": {Column: "status", Values: []string{"PENDING_APPROVAL", "ACTIVE", "REJECTED", "SUSPENDED", "DELETING"}},
		"labels": {Column: "labels", Labels: true},
	}
	SortFields = map[string]pagination.SortField{
		"id":          {Column: "id", Type: "text"},
		"name":        {Column: "name", Type: "text"},
		"create_time": {Column: "created_at", Type: "timestamptz"},
	}
)

// ListQuery is one page of a List.
type ListQuery struct {
	Filter filter.Filter
	Order  pagination.Order
	// Cursor is the decoded page token; empty for the first page.
	Cursor []string
	// Limit is the number of rows to fetch (page size + 1).
	Limit int
}

// Cursor returns the sort key values of a row for the next page token.
func Cursor(o pagination.Order, id, name string, created time.Time) []string {
	out := make([]string, len(o))
	for i, k := range o {
		switch k.Field {
		case "id":
			out[i] = id
		case "name":
			out[i] = name
		case "create_time":
			out[i] = pagination.TimeKey(created)
		}
	}
	return out
}

// where joins the fixed conditions with the filter and keyset conditions,
// numbering placeholders after args.
func (q ListQuery) where(fixed []string, args []any) (string, []any, error) {
	fsql, fargs := q.Filter.SQL(len(args) + 1)
	args = append(args, fargs...)
	after, aargs, err := q.Order.After(q.Cursor, len(args)+1)
	if err != nil {
		return "", nil, err
	}
	args = append(args, aargs...)
	conds := append(fixed, "("+fsql+")", after)
	return strings.Join(conds, " AND ") + " ORDER BY " + q.Order.SQL() + " LIMIT " + strconv.Itoa(q.Limit), args, nil
}

const projectColumns = `id, organization_id, name, description, labels, status, version, created_at, updated_at, deleted_at`

// ListProjects returns the live projects of an organization.
func (s *Store) ListProjects(ctx context.Context, orgID string, q ListQuery) ([]domain.Project, error) {
	where, args, err := q.where([]string{"deleted_at IS NULL", "organization_id = $1"}, []any{orgID})
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	recs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sqlcdb.Project, error) {
		var p sqlcdb.Project
		err := r.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.Labels, &p.Status,
			&p.Version, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]domain.Project, len(recs))
	for i, r := range recs {
		if out[i], err = project(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

const organizationColumns = `id, name, display_name, description, labels, owner_user_id, billing_account_id, status, iac_tool, version, created_at, updated_at, deleted_at`

// ListOrganizationsOf returns the live organizations in which one of
// subjects holds a binding, on the organization or one of its projects.
func (s *Store) ListOrganizationsOf(ctx context.Context, subjects []string, q ListQuery) ([]domain.Organization, error) {
	where, args, err := q.where([]string{
		"deleted_at IS NULL",
		"id IN (SELECT organization_id FROM access_bindings WHERE subject_id = ANY($1))",
	}, []any{subjects})
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+organizationColumns+` FROM organizations WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	recs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sqlcdb.Organization, error) {
		var o sqlcdb.Organization
		err := r.Scan(&o.ID, &o.Name, &o.DisplayName, &o.Description, &o.Labels, &o.OwnerUserID,
			&o.BillingAccountID, &o.Status, &o.IacTool, &o.Version, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt)
		return o, err
	})
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	out := make([]domain.Organization, len(recs))
	for i, r := range recs {
		if out[i], err = organization(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}
