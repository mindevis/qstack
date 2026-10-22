// Package protoopts reads qStack proto options declared in
// api/proto/qstack/api/annotations.proto: the IAM permission of an RPC and the
// sensitive marker of a field.
package protoopts

import (
	"fmt"
	"regexp"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
)

// permissionRe matches `<service>.<resource>.<verb>`, e.g.
// `compute.instances.create` or `iam.serviceAccounts.actAs`.
var permissionRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z0-9]*$`)

// Permission returns the IAM permission declared on the method with
// (qstack.api.permission), and whether one is declared.
func Permission(m protoreflect.MethodDescriptor) (string, bool) {
	p, ok := proto.GetExtension(m.Options(), apipb.E_Permission).(string)
	if !ok || p == "" {
		return "", false
	}
	return p, true
}

// IsSensitive reports whether the field is marked with (qstack.api.sensitive).
func IsSensitive(f protoreflect.FieldDescriptor) bool {
	s, _ := proto.GetExtension(f.Options(), apipb.E_Sensitive).(bool)
	return s
}

// CheckServices returns one problem per method that has no permission or a
// malformed one. An empty result means every RPC is covered by IAM.
func CheckServices(services ...protoreflect.ServiceDescriptor) []string {
	var problems []string
	for _, s := range services {
		methods := s.Methods()
		for i := range methods.Len() {
			m := methods.Get(i)
			p, ok := Permission(m)
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s: missing (qstack.api.permission)", m.FullName()))
			case !permissionRe.MatchString(p):
				problems = append(problems, fmt.Sprintf("%s: permission %q is not <service>.<resource>.<verb>", m.FullName(), p))
			}
		}
	}
	return problems
}
