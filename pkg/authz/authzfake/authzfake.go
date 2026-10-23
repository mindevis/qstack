// Package authzfake is an in-memory AccessService for tests: grants are
// (subject, permission, resource) triples and, as in IAM, a grant on a
// resource covers everything below it in the path (spec 03 §4.1).
package authzfake

import (
	"context"
	"sync"

	iaminternalv1 "github.com/mindevis/qstack/gen/go/qstack/iam/internalv1"
	"github.com/mindevis/qstack/pkg/authz"
)

// AllAuthenticatedUsers matches every authenticated subject.
const AllAuthenticatedUsers = "allAuthenticatedUsers"

// AccessService implements authz.Authorizer and the gRPC AccessService.
type AccessService struct {
	iaminternalv1.UnimplementedAccessServiceServer

	mu     sync.Mutex
	grants map[grant]bool
	err    error
	calls  int
}

type grant struct {
	subject, permission string
	resource            authz.Resource
}

// New returns an AccessService that denies everything.
func New() *AccessService { return &AccessService{grants: map[grant]bool{}} }

// Grant allows subject to use permission on resource and everything below it.
func (s *AccessService) Grant(subject, permission string, resource authz.Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[grant{subject, permission, resource}] = true
}

// Revoke removes a grant.
func (s *AccessService) Revoke(subject, permission string, resource authz.Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.grants, grant{subject, permission, resource})
}

// FailWith makes every call fail with err (nil restores normal answers).
func (s *AccessService) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Calls is the number of decisions requested so far.
func (s *AccessService) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Authorize implements authz.Authorizer.
func (s *AccessService) Authorize(_ context.Context, subject, permission string, path authz.Path) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	for _, r := range path {
		if s.grants[grant{subject, permission, r}] || s.grants[grant{AllAuthenticatedUsers, permission, r}] {
			return true, nil
		}
	}
	return false, nil
}

// AuthorizeRPC answers the gRPC Authorize call.
func (s *AccessService) authorizeRPC(ctx context.Context, req *iaminternalv1.AuthorizeRequest) (*iaminternalv1.AuthorizeResponse, error) {
	var path authz.Path
	for _, r := range req.GetResource().GetResources() {
		path = append(path, authz.Resource{Type: r.GetType(), ID: r.GetId()})
	}
	ok, err := s.Authorize(ctx, req.GetSubjectId(), req.GetPermission(), path)
	if err != nil {
		return nil, err
	}
	return &iaminternalv1.AuthorizeResponse{Allowed: ok}, nil
}

// Server returns the gRPC server side of the fake.
func (s *AccessService) Server() iaminternalv1.AccessServiceServer { return server{s} }

type server struct {
	*AccessService
}

func (s server) Authorize(ctx context.Context, req *iaminternalv1.AuthorizeRequest) (*iaminternalv1.AuthorizeResponse, error) {
	return s.authorizeRPC(ctx, req)
}

func (s server) BatchAuthorize(ctx context.Context, req *iaminternalv1.BatchAuthorizeRequest) (*iaminternalv1.BatchAuthorizeResponse, error) {
	out := &iaminternalv1.BatchAuthorizeResponse{}
	for _, r := range req.GetRequests() {
		resp, err := s.authorizeRPC(ctx, r)
		if err != nil {
			return nil, err
		}
		out.Responses = append(out.Responses, resp)
	}
	return out, nil
}
