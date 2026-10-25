// Package domain holds the Resource Manager entities and their invariants
// (spec 03 §2): organizations, projects and access bindings. Pure Go, no
// I/O (REQ-PLT-070).
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"
)

// Name of an organization or a project (spec 02 §4).
var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Label keys and limits (spec 02 §4).
var labelKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_./-]{0,62}$`)

const (
	maxLabels        = 64
	maxLabelValue    = 63
	maxDescription   = 256
	maxDisplayName   = 100
	maxBindingsLimit = 1000
)

// InvalidFieldError is a request field the domain rejects.
type InvalidFieldError struct{ Field, Description string }

func (e *InvalidFieldError) Error() string { return e.Field + ": " + e.Description }

// InvalidStateError means the resource state does not allow the action.
type InvalidStateError struct{ ID, State, Action string }

func (e *InvalidStateError) Error() string {
	return fmt.Sprintf("%s is %s: cannot %s", e.ID, e.State, e.Action)
}

// ValidateName checks a resource name.
func ValidateName(field, name string) error {
	if !nameRe.MatchString(name) {
		return &InvalidFieldError{field, "must match ^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$"}
	}
	return nil
}

// ValidateLabels checks labels against spec 02 §4.
func ValidateLabels(labels map[string]string) error {
	if len(labels) > maxLabels {
		return &InvalidFieldError{"labels", fmt.Sprintf("at most %d labels", maxLabels)}
	}
	for k, v := range labels {
		if !labelKeyRe.MatchString(k) {
			return &InvalidFieldError{"labels", fmt.Sprintf("invalid key %q", k)}
		}
		if len(v) > maxLabelValue {
			return &InvalidFieldError{"labels", fmt.Sprintf("value of %q longer than %d characters", k, maxLabelValue)}
		}
	}
	return nil
}

func validateDescription(d string) error {
	if len(d) > maxDescription {
		return &InvalidFieldError{"description", fmt.Sprintf("longer than %d characters", maxDescription)}
	}
	return nil
}

// Etag is the opaque etag of a resource version (REQ-PLT-022).
func Etag(id string, version int64) string {
	sum := sha256.Sum256([]byte(id + "/" + strconv.FormatInt(version, 10)))
	return hex.EncodeToString(sum[:8])
}

// CheckEtag returns ErrEtagMismatch if want is set and differs from the
// resource's etag.
func CheckEtag(id string, version int64, want string) error {
	if want != "" && want != Etag(id, version) {
		return ErrEtagMismatch
	}
	return nil
}

// ErrEtagMismatch means the client's etag is stale (ABORTED).
var ErrEtagMismatch = fmt.Errorf("etag mismatch")

// OrganizationStatus is the state of an organization.
type OrganizationStatus string

// Organization states (spec 03 §2).
const (
	OrgPendingApproval OrganizationStatus = "PENDING_APPROVAL"
	OrgActive          OrganizationStatus = "ACTIVE"
	OrgRejected        OrganizationStatus = "REJECTED"
	OrgSuspended       OrganizationStatus = "SUSPENDED"
	OrgDeleting        OrganizationStatus = "DELETING"
)

// IacTool is the infrastructure-as-code tool of console snippets.
type IacTool string

// IaC tools; unspecified means the installation default.
const (
	IacUnspecified IacTool = "UNSPECIFIED"
	IacOpenTofu    IacTool = "OPENTOFU"
	IacTerraform   IacTool = "TERRAFORM"
)

// Organization is the top of the resource hierarchy.
type Organization struct {
	ID               string
	Name             string
	DisplayName      string
	Description      string
	Labels           map[string]string
	OwnerUserID      string
	BillingAccountID string
	Status           OrganizationStatus
	IacTool          IacTool
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Etag of the organization.
func (o *Organization) Etag() string { return Etag(o.ID, o.Version) }

// NewOrganization returns a new organization owned by owner.
func NewOrganization(id, name, displayName, owner string, status OrganizationStatus, now time.Time) (Organization, error) {
	if err := ValidateName("name", name); err != nil {
		return Organization{}, err
	}
	if len(displayName) > maxDisplayName {
		return Organization{}, &InvalidFieldError{"display_name", fmt.Sprintf("longer than %d characters", maxDisplayName)}
	}
	if status != OrgPendingApproval && status != OrgActive {
		return Organization{}, &InvalidFieldError{"status", "must be PENDING_APPROVAL or ACTIVE"}
	}
	return Organization{
		ID: id, Name: name, DisplayName: displayName, Labels: map[string]string{},
		OwnerUserID: owner, Status: status, IacTool: IacUnspecified,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// OrganizationChanges are the new values of an update; Mask selects which
// apply (REQ-PLT-030).
type OrganizationChanges struct {
	Mask        []string
	DisplayName string
	Description string
	Labels      map[string]string
	IacTool     IacTool
}

// Update applies ch; it reports whether anything changed.
func (o *Organization) Update(ch OrganizationChanges, now time.Time) (bool, error) {
	if o.Status == OrgDeleting {
		return false, &InvalidStateError{o.ID, string(o.Status), "update"}
	}
	if len(ch.Mask) == 0 {
		return false, &InvalidFieldError{"update_mask", "must not be empty"}
	}
	next := *o
	for _, p := range ch.Mask {
		switch p {
		case "display_name":
			if len(ch.DisplayName) > maxDisplayName {
				return false, &InvalidFieldError{"display_name", fmt.Sprintf("longer than %d characters", maxDisplayName)}
			}
			next.DisplayName = ch.DisplayName
		case "description":
			if err := validateDescription(ch.Description); err != nil {
				return false, err
			}
			next.Description = ch.Description
		case "labels":
			if err := ValidateLabels(ch.Labels); err != nil {
				return false, err
			}
			next.Labels = cloneLabels(ch.Labels)
		case "settings", "settings.iac_tool":
			switch ch.IacTool {
			case IacUnspecified, IacOpenTofu, IacTerraform:
			default:
				return false, &InvalidFieldError{"settings.iac_tool", "unknown value"}
			}
			next.IacTool = ch.IacTool
		default:
			return false, &InvalidFieldError{"update_mask", fmt.Sprintf("cannot update %q", p)}
		}
	}
	if next.DisplayName == o.DisplayName && next.Description == o.Description &&
		maps.Equal(next.Labels, o.Labels) && next.IacTool == o.IacTool {
		return false, nil
	}
	next.Version++
	next.UpdatedAt = now
	*o = next
	return true, nil
}

// ProjectStatus is the state of a project.
type ProjectStatus string

// Project states (spec 03 §2).
const (
	ProjectActive    ProjectStatus = "ACTIVE"
	ProjectSuspended ProjectStatus = "SUSPENDED"
	ProjectDeleting  ProjectStatus = "DELETING"
	ProjectDeleted   ProjectStatus = "DELETED"
)

// Project groups resources of an organization.
type Project struct {
	ID             string
	OrganizationID string
	Name           string
	Description    string
	Labels         map[string]string
	Status         ProjectStatus
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Etag of the project.
func (p *Project) Etag() string { return Etag(p.ID, p.Version) }

// NewProject returns a new project of org; projects are created only in
// active organizations.
func NewProject(id string, org *Organization, name, description string, labels map[string]string, now time.Time) (Project, error) {
	if org.Status != OrgActive {
		return Project{}, &InvalidStateError{org.ID, string(org.Status), "create projects"}
	}
	if err := ValidateName("name", name); err != nil {
		return Project{}, err
	}
	if err := validateDescription(description); err != nil {
		return Project{}, err
	}
	if err := ValidateLabels(labels); err != nil {
		return Project{}, err
	}
	return Project{
		ID: id, OrganizationID: org.ID, Name: name, Description: description,
		Labels: cloneLabels(labels), Status: ProjectActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// ProjectChanges are the new values of an update; Mask selects which apply.
type ProjectChanges struct {
	Mask        []string
	Name        string
	Description string
	Labels      map[string]string
}

// Update applies ch; it reports whether anything changed.
func (p *Project) Update(ch ProjectChanges, now time.Time) (bool, error) {
	if p.Status == ProjectDeleting || p.Status == ProjectDeleted {
		return false, &InvalidStateError{p.ID, string(p.Status), "update"}
	}
	if len(ch.Mask) == 0 {
		return false, &InvalidFieldError{"update_mask", "must not be empty"}
	}
	next := *p
	for _, path := range ch.Mask {
		switch path {
		case "name":
			if err := ValidateName("name", ch.Name); err != nil {
				return false, err
			}
			next.Name = ch.Name
		case "description":
			if err := validateDescription(ch.Description); err != nil {
				return false, err
			}
			next.Description = ch.Description
		case "labels":
			if err := ValidateLabels(ch.Labels); err != nil {
				return false, err
			}
			next.Labels = cloneLabels(ch.Labels)
		default:
			return false, &InvalidFieldError{"update_mask", fmt.Sprintf("cannot update %q", path)}
		}
	}
	if next.Name == p.Name && next.Description == p.Description && maps.Equal(next.Labels, p.Labels) {
		return false, nil
	}
	next.Version++
	next.UpdatedAt = now
	*p = next
	return true, nil
}

// Delete marks the project deleted; its name becomes free.
func (p *Project) Delete(now time.Time) error {
	if p.Status == ProjectDeleting || p.Status == ProjectDeleted {
		return &InvalidStateError{p.ID, string(p.Status), "delete"}
	}
	p.Status = ProjectDeleted
	p.Version++
	p.UpdatedAt = now
	return nil
}

func cloneLabels(l map[string]string) map[string]string {
	if l == nil {
		return map[string]string{}
	}
	return maps.Clone(l)
}
