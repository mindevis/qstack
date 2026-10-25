package app

import (
	"maps"

	"google.golang.org/protobuf/types/known/timestamppb"

	accessv1 "github.com/mindevis/qstack/gen/go/qstack/access/v1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
)

var (
	orgStatus = map[domain.OrganizationStatus]rmv1.Organization_Status{
		domain.OrgPendingApproval: rmv1.Organization_STATUS_PENDING_APPROVAL,
		domain.OrgActive:          rmv1.Organization_STATUS_ACTIVE,
		domain.OrgRejected:        rmv1.Organization_STATUS_REJECTED,
		domain.OrgSuspended:       rmv1.Organization_STATUS_SUSPENDED,
		domain.OrgDeleting:        rmv1.Organization_STATUS_DELETING,
	}
	projectStatus = map[domain.ProjectStatus]rmv1.Project_Status{
		domain.ProjectActive:    rmv1.Project_STATUS_ACTIVE,
		domain.ProjectSuspended: rmv1.Project_STATUS_SUSPENDED,
		domain.ProjectDeleting:  rmv1.Project_STATUS_DELETING,
		domain.ProjectDeleted:   rmv1.Project_STATUS_DELETED,
	}
	iacTools = map[domain.IacTool]rmv1.OrganizationSettings_IacTool{
		domain.IacUnspecified: rmv1.OrganizationSettings_IAC_TOOL_UNSPECIFIED,
		domain.IacOpenTofu:    rmv1.OrganizationSettings_IAC_TOOL_OPENTOFU,
		domain.IacTerraform:   rmv1.OrganizationSettings_IAC_TOOL_TERRAFORM,
	}
	subjectTypes = map[domain.SubjectType]accessv1.Subject_Type{
		domain.SubjectUser:           accessv1.Subject_TYPE_USER_ACCOUNT,
		domain.SubjectServiceAccount: accessv1.Subject_TYPE_SERVICE_ACCOUNT,
		domain.SubjectGroup:          accessv1.Subject_TYPE_GROUP,
		domain.SubjectSystem:         accessv1.Subject_TYPE_SYSTEM,
	}
)

func invert[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	iacToolOf     = invert(iacTools)
	subjectTypeOf = invert(subjectTypes)
	orgStatusOf   = invert(orgStatus)
)

// OrganizationProto is the API view of o.
func OrganizationProto(o domain.Organization) *rmv1.Organization {
	return &rmv1.Organization{
		Id:               o.ID,
		Name:             o.Name,
		Description:      o.Description,
		Labels:           maps.Clone(o.Labels),
		CreateTime:       timestamppb.New(o.CreatedAt),
		UpdateTime:       timestamppb.New(o.UpdatedAt),
		Etag:             o.Etag(),
		Status:           orgStatus[o.Status],
		DisplayName:      o.DisplayName,
		OwnerUserId:      o.OwnerUserID,
		BillingAccountId: o.BillingAccountID,
		Settings:         &rmv1.OrganizationSettings{IacTool: iacTools[o.IacTool]},
	}
}

// ProjectProto is the API view of p.
func ProjectProto(p domain.Project) *rmv1.Project {
	return &rmv1.Project{
		Id:             p.ID,
		OrganizationId: p.OrganizationID,
		Name:           p.Name,
		Description:    p.Description,
		Labels:         maps.Clone(p.Labels),
		CreateTime:     timestamppb.New(p.CreatedAt),
		UpdateTime:     timestamppb.New(p.UpdatedAt),
		Etag:           p.Etag(),
		Status:         projectStatus[p.Status],
	}
}

// BindingProto is the API view of b.
func BindingProto(b domain.Binding) *accessv1.AccessBinding {
	return &accessv1.AccessBinding{
		Id:      b.ID,
		RoleId:  b.RoleID,
		Subject: &accessv1.Subject{Id: b.Subject.ID, Type: subjectTypes[b.Subject.Type]},
	}
}

// BindingsProto converts a binding list.
func BindingsProto(bs []domain.Binding) []*accessv1.AccessBinding {
	out := make([]*accessv1.AccessBinding, len(bs))
	for i, b := range bs {
		out[i] = BindingProto(b)
	}
	return out
}

// BindingOf is the domain view of an API binding; its ID is ignored.
func BindingOf(b *accessv1.AccessBinding) domain.Binding {
	return domain.Binding{
		RoleID:  b.GetRoleId(),
		Subject: domain.Subject{Type: subjectTypeOf[b.GetSubject().GetType()], ID: b.GetSubject().GetId()},
	}
}

// IacToolOf is the domain view of an API IaC tool ("" if unknown).
func IacToolOf(t rmv1.OrganizationSettings_IacTool) domain.IacTool { return iacToolOf[t] }

// OrgStatusOf is the domain view of an API organization status ("" if unknown).
func OrgStatusOf(s rmv1.Organization_Status) domain.OrganizationStatus { return orgStatusOf[s] }
