package iam_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// GetRole's in-handler authorization beyond the specification tests
// (authorize_test.go).

// An allowed GetRole asks exactly one question: iam:GetRole on the role's
// ARN with its path, which only the read learns, with no condition keys of
// its own. Step 8 asks nothing (kernel.AuthorizedInHandler), since on
// AnyResource it would deny a caller whose policy grants iam:GetRole on
// that role alone (iam-evaluation.md 2.1). The pair names the role as
// "role <name>" (iam-enforcement-surfaces.md 3.2, IAM-ENF-10). It answers
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
		want := []kernel.AuthzQuery{{Pairs: []kernel.Pair{{Action: getRole, Resource: kernel.ResourceARN{ARN: role}, DenialResource: "role worker"}}}}
		gate.mu.Lock()
		asked := slices.Clone(gate.asked)
		gate.mu.Unlock()
		if !reflect.DeepEqual(asked, want) {
			h.Errorf("GetRole asked %+v, want only %+v", asked, want)
		}
	})
}
