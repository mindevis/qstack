// Package authz enforces IAM permissions in every public API (spec 03 §4,
// REQ-IAM-011, REQ-IAM-012).
//
// Every public method declares one permission with (qstack.api.permission).
// The interceptor resolves it, requires an authenticated subject and hands
// the handler a check bound to that permission; the handler, which knows the
// resource path, calls Check (Create/List: the project) or CheckResource
// (Get/Update/Delete: the loaded resource). The interceptor fails closed:
//
//   - a method without a permission is rejected (and CheckServer reports it
//     in tests);
//   - a handler that returns success without an allowing check gets
//     INTERNAL instead of its response, and before a check only
//     INVALID_ARGUMENT and NOT_FOUND errors pass, which reveal nothing
//     about other tenants;
//   - if the access service fails, the call fails with UNAVAILABLE.
//
// Decisions — allow and deny — are cached for 5 seconds (Client).
package authz

import (
	"context"
	"errors"
	"sync"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	"github.com/mindevis/qstack/pkg/apierr"
)

// Resource is one element of a resource path.
type Resource struct {
	Type string // "organization", "project", "instance", …
	ID   string
}

// Path is a resource with its ancestors, root first.
type Path []Resource

// ProjectPath is the path of a project, the target of Create and List.
func ProjectPath(orgID, projectID string) Path {
	return Path{{"organization", orgID}, {"project", projectID}}
}

// Child returns p extended with a resource.
func (p Path) Child(typ, id string) Path {
	return append(append(Path(nil), p...), Resource{typ, id})
}

// Authorizer decides whether subject holds permission on path.
type Authorizer interface {
	Authorize(ctx context.Context, subject, permission string, path Path) (bool, error)
}

// Subject is the authenticated caller, set by the authentication layer.
type Subject struct {
	ID   string // usr-… or sa-…
	Type string // "user" or "sa"
}

type subjectKey struct{}

// WithSubject returns ctx carrying the authenticated subject.
func WithSubject(ctx context.Context, s Subject) context.Context {
	return context.WithValue(ctx, subjectKey{}, s)
}

// SubjectFrom returns the authenticated subject, if any.
func SubjectFrom(ctx context.Context) (Subject, bool) {
	s, ok := ctx.Value(subjectKey{}).(Subject)
	return s, ok && s.ID != ""
}

// SubjectID returns the subject ID or "" (for pkg/idempotency Options.Subject).
func SubjectID(ctx context.Context) string {
	s, _ := SubjectFrom(ctx)
	return s.ID
}

// call is the authorization state of one request.
type call struct {
	authz      Authorizer
	subject    string
	permission string
	onError    func(ctx context.Context, err error)

	mu      sync.Mutex
	checked bool
	denied  bool // one denial taints the request even if a later check allows
}

type callKey struct{}

func callFrom(ctx context.Context) (*call, error) {
	c, ok := ctx.Value(callKey{}).(*call)
	if !ok {
		// Not behind the interceptor: a wiring bug.
		return nil, errors.New("authz: no authorization context; is the interceptor installed?")
	}
	return c, nil
}

func (c *call) record(allowed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked = true
	c.denied = c.denied || !allowed
}

// allowed reports whether the request passed a check and failed none.
func (c *call) allowed() (checked, allowed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checked, c.checked && !c.denied
}

func (c *call) authorize(ctx context.Context, permission string, path Path) (bool, error) {
	ok, err := c.authz.Authorize(ctx, c.subject, permission, path)
	if err != nil {
		if c.onError != nil {
			c.onError(ctx, err)
		}
		return false, apierr.Unavailable("authorization is temporarily unavailable; retry")
	}
	return ok, nil
}

// Check authorizes the method's permission on path: the project for Create
// and List, the resource for custom methods on it. Denial is
// PERMISSION_DENIED.
func Check(ctx context.Context, path Path) error {
	c, err := callFrom(ctx)
	if err != nil {
		return err
	}
	ok, err := c.authorize(ctx, c.permission, path)
	if err != nil {
		c.record(false)
		return err
	}
	c.record(ok)
	if !ok {
		return apierr.PermissionDenied(c.permission)
	}
	return nil
}

// CheckResource authorizes the method's permission on an existing resource
// (the last element of path). If it is denied and the subject may not even
// read the resource (getPermission, e.g. "compute.instances.get"), the
// answer is NOT_FOUND so that the resource's existence is not revealed.
func CheckResource(ctx context.Context, path Path, getPermission string) error {
	if len(path) == 0 {
		return errors.New("authz: empty resource path")
	}
	err := Check(ctx, path)
	if !apierr.IsReason(err, apipb.ErrorReason_QS_PERMISSION_DENIED) {
		return err
	}
	c, _ := callFrom(ctx)
	target := path[len(path)-1]
	if c.permission == getPermission {
		return apierr.NotFound(target.Type, target.ID)
	}
	canGet, aerr := c.authorize(ctx, getPermission, path)
	if aerr != nil {
		return aerr
	}
	if !canGet {
		return apierr.NotFound(target.Type, target.ID)
	}
	return err
}

// Require checks an additional permission on a dependent resource, e.g.
// vpc.subnets.use on the subnet a new instance attaches to, or
// iam.serviceAccounts.actAs. It does not replace Check.
func Require(ctx context.Context, permission string, path Path) error {
	c, err := callFrom(ctx)
	if err != nil {
		return err
	}
	ok, err := c.authorize(ctx, permission, path)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.PermissionDenied(permission)
	}
	return nil
}
