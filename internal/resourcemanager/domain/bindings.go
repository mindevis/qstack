package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// OwnerRole is granted to the creator of an organization; the last owner
// binding of an organization cannot be removed (spec 03 §2).
const OwnerRole = "resource-manager.organizations.owner"

// AllAuthenticatedUsers is the only system subject.
const AllAuthenticatedUsers = "allAuthenticatedUsers"

// Resource types that hold bindings in Resource Manager.
const (
	ResourceOrganization = "organization"
	ResourceProject      = "project"
)

// SubjectType is the kind of a binding subject (spec 03 §2).
type SubjectType string

// Subject types, in the spelling of spec 03 §2 (`userAccount:<id>`, …).
const (
	SubjectUser           SubjectType = "userAccount"
	SubjectServiceAccount SubjectType = "serviceAccount"
	SubjectGroup          SubjectType = "group"
	SubjectSystem         SubjectType = "system"
)

var (
	roleRe     = regexp.MustCompile(`^[a-z][a-zA-Z0-9.-]{0,99}$`)
	subjectIDs = map[SubjectType]*regexp.Regexp{
		SubjectUser:           regexp.MustCompile(`^usr-[0-9a-hjkmnp-tv-z]{26}$`),
		SubjectServiceAccount: regexp.MustCompile(`^sa-[0-9a-hjkmnp-tv-z]{26}$`),
		SubjectGroup:          regexp.MustCompile(`^grp-[0-9a-hjkmnp-tv-z]{26}$`),
		SubjectSystem:         regexp.MustCompile(`^` + AllAuthenticatedUsers + `$`),
	}
)

// Subject is who a binding grants a role to.
type Subject struct {
	Type SubjectType
	ID   string
}

// Binding grants RoleID to Subject on the resource holding it.
type Binding struct {
	ID      string
	RoleID  string
	Subject Subject
}

func (b Binding) key() string {
	return b.RoleID + "\x00" + string(b.Subject.Type) + "\x00" + b.Subject.ID
}

// ErrLastOwner means a change would leave an organization without owners.
var ErrLastOwner = errors.New("the last owner of an organization cannot be removed")

// validateBinding checks one binding held by a resource of resourceType.
func validateBinding(resourceType, field string, b Binding) error {
	if !roleRe.MatchString(b.RoleID) {
		return &InvalidFieldError{field + ".role_id", "invalid role id"}
	}
	re, ok := subjectIDs[b.Subject.Type]
	if !ok {
		return &InvalidFieldError{field + ".subject.type", "unknown subject type"}
	}
	if !re.MatchString(b.Subject.ID) {
		return &InvalidFieldError{field + ".subject.id", fmt.Sprintf("not a valid %s id", b.Subject.Type)}
	}
	if b.RoleID == OwnerRole {
		if resourceType != ResourceOrganization {
			return &InvalidFieldError{field + ".role_id", OwnerRole + " is granted on organizations only"}
		}
		if b.Subject.Type == SubjectSystem {
			return &InvalidFieldError{field + ".subject", OwnerRole + " cannot be granted to " + AllAuthenticatedUsers}
		}
	}
	return nil
}

// DeltaAction adds or removes a binding.
type DeltaAction int

// Delta actions.
const (
	DeltaAdd DeltaAction = iota + 1
	DeltaRemove
)

// Delta is one change of a binding set.
type Delta struct {
	Action  DeltaAction
	Binding Binding
}

// BindingChange is the effect of a change on a binding set.
type BindingChange struct {
	// Added get new IDs; Removed carry their stored IDs.
	Added, Removed []Binding
	// Result is the binding set after the change, ordered by ID.
	Result []Binding
}

// Changed reports whether the set changed.
func (c BindingChange) Changed() bool { return len(c.Added) > 0 || len(c.Removed) > 0 }

// ApplyDeltas applies deltas in order to current. Adding a present binding
// and removing an absent one are no-ops, so retries are harmless.
func ApplyDeltas(resourceType string, current []Binding, deltas []Delta, newID func() string) (BindingChange, error) {
	next := index(current)
	for i, d := range deltas {
		field := fmt.Sprintf("access_binding_deltas[%d].access_binding", i)
		if err := validateBinding(resourceType, field, d.Binding); err != nil {
			return BindingChange{}, err
		}
		switch d.Action {
		case DeltaAdd:
			if _, ok := next[d.Binding.key()]; !ok {
				next[d.Binding.key()] = d.Binding
			}
		case DeltaRemove:
			delete(next, d.Binding.key())
		default:
			return BindingChange{}, &InvalidFieldError{fmt.Sprintf("access_binding_deltas[%d].action", i), "must be ADD or REMOVE"}
		}
	}
	return diff(resourceType, current, next, newID)
}

// Replace makes desired the binding set (SetAccessBindings); duplicates
// collapse.
func Replace(resourceType string, current, desired []Binding, newID func() string) (BindingChange, error) {
	next := map[string]Binding{}
	for i, b := range desired {
		if err := validateBinding(resourceType, fmt.Sprintf("access_bindings[%d]", i), b); err != nil {
			return BindingChange{}, err
		}
		next[b.key()] = b
	}
	return diff(resourceType, current, next, newID)
}

func index(bs []Binding) map[string]Binding {
	m := make(map[string]Binding, len(bs))
	for _, b := range bs {
		m[b.key()] = b
	}
	return m
}

func diff(resourceType string, current []Binding, next map[string]Binding, newID func() string) (BindingChange, error) {
	if len(next) > maxBindingsLimit {
		return BindingChange{}, &InvalidFieldError{"access_bindings", fmt.Sprintf("at most %d bindings per resource", maxBindingsLimit)}
	}
	cur := index(current)
	var ch BindingChange
	for _, b := range current {
		if _, ok := next[b.key()]; !ok {
			ch.Removed = append(ch.Removed, b)
		}
	}
	for k, b := range next {
		if old, ok := cur[k]; ok {
			ch.Result = append(ch.Result, old)
			continue
		}
		b.ID = newID()
		ch.Added = append(ch.Added, b)
		ch.Result = append(ch.Result, b)
	}
	slices.SortFunc(ch.Added, byID)
	slices.SortFunc(ch.Result, byID)
	if resourceType == ResourceOrganization && hasOwner(current) && !hasOwner(ch.Result) {
		return BindingChange{}, ErrLastOwner
	}
	return ch, nil
}

func hasOwner(bs []Binding) bool {
	return slices.ContainsFunc(bs, func(b Binding) bool { return b.RoleID == OwnerRole })
}

func byID(a, b Binding) int { return strings.Compare(a.ID, b.ID) }
