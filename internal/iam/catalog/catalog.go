// Package catalog is the declarative catalog of permissions and predefined
// roles (spec 03 §2), embedded from roles.yaml and loaded at start.
package catalog

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed roles.yaml
var rolesYAML []byte

// Kind classifies a permission for the generated roles.
type Kind string

// Permission kinds (see roles.yaml).
const (
	Read   Kind = "read"
	Write  Kind = "write"
	Access Kind = "access"
	Use    Kind = "use"
)

// Stage is the maturity of a role.
type Stage string

// Stages.
const (
	GA   Stage = "GA"
	Beta Stage = "BETA"
)

// Role is a predefined set of permissions.
type Role struct {
	ID          string
	Title       string
	Description string
	// Permissions are sorted.
	Permissions []string
	Stage       Stage
}

// Catalog holds the permissions and roles.
type Catalog struct {
	perms map[string]Kind
	roles map[string]Role
	ids   []string // sorted role IDs
}

// excluded services are not part of viewer/editor/admin (spec 03 §2).
var excluded = map[string]bool{"billing": true}

var (
	serviceRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	permRe    = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z0-9]*$`)
	roleRe    = regexp.MustCompile(`^[a-z][a-zA-Z0-9.-]{0,99}$`)
)

type file struct {
	Services map[string]struct {
		Title       string          `yaml:"title"`
		Permissions map[string]Kind `yaml:"permissions"`
	} `yaml:"services"`
	Roles []struct {
		ID          string   `yaml:"id"`
		Title       string   `yaml:"title"`
		Description string   `yaml:"description"`
		Permissions []string `yaml:"permissions"`
		All         bool     `yaml:"all"`
		Stage       Stage    `yaml:"stage"`
	} `yaml:"roles"`
}

// Load returns the embedded catalog.
func Load() (*Catalog, error) { return Parse(rolesYAML) }

// Parse reads a catalog; unknown keys, unknown kinds and roles referring
// to undeclared permissions are errors.
func Parse(b []byte) (*Catalog, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	c := &Catalog{perms: map[string]Kind{}, roles: map[string]Role{}}
	type bucket struct{ read, write, access, use []string }
	all := map[string]*bucket{}
	for svc, s := range f.Services {
		if !serviceRe.MatchString(svc) {
			return nil, fmt.Errorf("catalog: invalid service %q", svc)
		}
		b := &bucket{}
		all[svc] = b
		for p, k := range s.Permissions {
			if !permRe.MatchString(p) {
				return nil, fmt.Errorf("catalog: %s: invalid permission %q", svc, p)
			}
			full := svc + "." + p
			switch k {
			case Read:
				b.read = append(b.read, full)
			case Write:
				b.write = append(b.write, full)
			case Access:
				b.access = append(b.access, full)
			case Use:
				b.use = append(b.use, full)
			default:
				return nil, fmt.Errorf("catalog: %s: unknown kind %q", full, k)
			}
			c.perms[full] = k
		}
		title := s.Title
		if title == "" {
			title = svc
		}
		c.add(Role{ID: svc + ".viewer", Title: title + " viewer", Description: "Read " + title + " resources.", Permissions: b.read})
		c.add(Role{ID: svc + ".editor", Title: title + " editor", Description: "Manage " + title + " resources, except access bindings.",
			Permissions: concat(b.read, b.write, b.use)})
		c.add(Role{ID: svc + ".admin", Title: title + " admin", Description: "Manage " + title + " resources and their access bindings.",
			Permissions: concat(b.read, b.write, b.use, b.access)})
	}
	var viewer, editor, admin []string
	for svc, b := range all {
		if excluded[svc] {
			continue
		}
		viewer = append(viewer, b.read...)
		editor = append(editor, concat(b.read, b.write, b.use)...)
		admin = append(admin, concat(b.read, b.write, b.use, b.access)...)
	}
	c.add(Role{ID: "viewer", Title: "Viewer", Description: "Read all resources, except billing.", Permissions: viewer})
	c.add(Role{ID: "editor", Title: "Editor", Description: "Viewer, and manage resources except access bindings and billing.", Permissions: editor})
	c.add(Role{ID: "admin", Title: "Admin", Description: "Editor, and manage access bindings.", Permissions: admin})

	for _, r := range f.Roles {
		if !roleRe.MatchString(r.ID) {
			return nil, fmt.Errorf("catalog: invalid role id %q", r.ID)
		}
		if _, dup := c.roles[r.ID]; dup {
			return nil, fmt.Errorf("catalog: role %s defined twice", r.ID)
		}
		perms := r.Permissions
		if r.All {
			if len(perms) > 0 {
				return nil, fmt.Errorf("catalog: role %s: all and permissions are exclusive", r.ID)
			}
			perms = slices.Collect(maps.Keys(c.perms))
		}
		if len(perms) == 0 {
			return nil, fmt.Errorf("catalog: role %s has no permissions", r.ID)
		}
		for _, p := range perms {
			if _, ok := c.perms[p]; !ok {
				return nil, fmt.Errorf("catalog: role %s: undeclared permission %q", r.ID, p)
			}
		}
		if r.Title == "" {
			return nil, fmt.Errorf("catalog: role %s has no title", r.ID)
		}
		c.add(Role{ID: r.ID, Title: r.Title, Description: r.Description, Permissions: perms, Stage: r.Stage})
	}
	for _, r := range c.roles {
		switch r.Stage {
		case GA, Beta:
		default:
			return nil, fmt.Errorf("catalog: role %s: unknown stage %q", r.ID, r.Stage)
		}
	}
	c.ids = slices.Sorted(maps.Keys(c.roles))
	return c, nil
}

func (c *Catalog) add(r Role) {
	if r.Stage == "" {
		r.Stage = GA
	}
	r.Permissions = slices.Compact(slices.Sorted(slices.Values(r.Permissions)))
	if len(r.Permissions) == 0 {
		return // a service with no permissions of the kinds
	}
	c.roles[r.ID] = r
}

func concat(parts ...[]string) []string { return slices.Concat(parts...) }

// Role returns a role by ID.
func (c *Catalog) Role(id string) (Role, bool) {
	r, ok := c.roles[id]
	return r, ok
}

// Roles returns all roles ordered by ID.
func (c *Catalog) Roles() []Role {
	out := make([]Role, len(c.ids))
	for i, id := range c.ids {
		out[i] = c.roles[id]
	}
	return out
}

// Kind returns the kind of a declared permission.
func (c *Catalog) Kind(permission string) (Kind, bool) {
	k, ok := c.perms[permission]
	return k, ok
}

// Permissions returns all declared permissions, sorted.
func (c *Catalog) Permissions() []string { return slices.Sorted(maps.Keys(c.perms)) }

// Grants reports whether role holds permission.
func (c *Catalog) Grants(role, permission string) bool {
	r, ok := c.roles[role]
	if !ok {
		return false
	}
	_, found := slices.BinarySearch(r.Permissions, permission)
	return found
}

// ErrUnknownRole is returned by Validate for a role not in the catalog.
var ErrUnknownRole = errors.New("unknown role")

// Validate checks that role exists, e.g. before binding it.
func (c *Catalog) Validate(role string) error {
	if _, ok := c.roles[role]; !ok {
		return fmt.Errorf("%w %q", ErrUnknownRole, strings.TrimSpace(role))
	}
	return nil
}
