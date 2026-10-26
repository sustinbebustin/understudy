package iam_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// GetRole's in-handler authorization beyond the specification tests
// (authorize_test.go).

// An allowed GetRole asks once about the role's ARN with its path, which
// only the read learns, supplies no condition keys of its own, and answers
// the role as without an Authorizer.
func TestAnAllowedGetRoleAsksAboutTheRolesPathARN(t *testing.T) {
	gate := &roleGate{}
	runGated(t, gate, func(h *harness) {
		created := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "worker", Path: new("/team/"), AssumeRolePolicyDocument: trustLambda}).Role
		role := parseARN(h, "arn:aws:iam::000000000000:role/team/worker")
		gate.denyRole(parseARN(h, "arn:aws:iam::000000000000:role/worker"), "denied")

		got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "worker"}).Role
		if got.Arn != created.Arn || got.Arn != role.String() || got.Path != "/team/" {
			h.Errorf("GetRole = %s (path %s), want %s", got.Arn, got.Path, role)
		}
		want := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: getRole, Resource: kernel.ResourceARN{ARN: role}}}}
		if asked := gate.askedAbout(getRole, role); len(asked) != 1 || !reflect.DeepEqual(asked[0], want) {
			h.Errorf("questions about the role = %+v, want once %+v", asked, want)
		}
	})
}
