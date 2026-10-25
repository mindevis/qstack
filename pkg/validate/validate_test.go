package validate_test

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/validate"
)

func TestValidate(t *testing.T) {
	v, err := validate.New()
	if err != nil {
		t.Fatal(err)
	}
	ok := &rmv1.CreateProjectRequest{OrganizationId: "org-01m5hqpzg00000000000000000", Name: "web"}
	if err := v.Validate(ok); err != nil {
		t.Errorf("valid request: %v", err)
	}

	bad := &rmv1.CreateProjectRequest{OrganizationId: "org-1", Name: "Web", Labels: map[string]string{"Env": "x"}}
	err = v.Validate(bad)
	if status.Code(err) != codes.InvalidArgument || !apierr.IsReason(err, apipb.ErrorReason_QS_INVALID_FIELD) {
		t.Fatalf("invalid request: %v, want INVALID_ARGUMENT / QS_INVALID_FIELD", err)
	}
	fields := map[string]bool{}
	for _, d := range status.Convert(err).Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, fv := range br.GetFieldViolations() {
				fields[fv.GetField()] = true
			}
		}
	}
	for _, f := range []string{"organization_id", "name"} {
		if !fields[f] {
			t.Errorf("no violation for %s in %v", f, fields)
		}
	}
	if len(fields) != 3 {
		t.Errorf("violations = %v, want 3 (organization_id, name, labels key)", fields)
	}
}
