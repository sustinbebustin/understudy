package kernel

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// An execution role call runs as a session of the role: a Scheduler
// delivery's carries the role id and the invocation's session name
// (scheduler.md 7.2, 14.4.4), and one that names neither, as event source
// mapping polls and Lambda log writes do, keeps the session "understudy"
// and no id.
func TestExecutionRoleSession(t *testing.T) {
	role := aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/app/sch-role"}
	account, err := aws.ParseAccountID("000000000000")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode AsExecutionRole
		want identity.Principal
	}{
		{
			AsExecutionRole{Role: role, ID: "AROA2DZE3W4CTL5ZR7EIN", Session: "ff00212d8c453aaaae644bc6846d4723"},
			identity.AssumedRole{Account: account, Role: identity.RoleRef{Name: "sch-role", Path: "/app/", ID: "AROA2DZE3W4CTL5ZR7EIN"}, Session: "ff00212d8c453aaaae644bc6846d4723", Source: identity.FromAssumeRole{}},
		},
		{
			AsExecutionRole{Role: role},
			identity.AssumedRole{Account: account, Role: identity.RoleRef{Name: "sch-role", Path: "/app/"}, Session: "understudy", Source: identity.FromAssumeRole{}},
		},
		{
			AsExecutionRole{Role: aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "user/alice"}, Session: "s"},
			identity.Anonymous{},
		},
	} {
		if got := resolvePrincipal(nil, tc.mode); got != tc.want {
			t.Errorf("resolvePrincipal(%+v) = %#v, want %#v", tc.mode, got, tc.want)
		}
	}
}
