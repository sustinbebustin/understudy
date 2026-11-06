package server_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Enforcement binds no operation of a policy type it does not evaluate:
// the IAM policy simulator, IAM groups and KMS grants stay unbound, as do
// the resource-policy writes of iam-enforcement-surfaces.md 12.7, since
// binding one without evaluating it would be a StoredOnly that looks
// enforced. Under enforce such a call answers 501 like any unbound
// operation, not a denial.
func TestEnforcementLeavesUnevaluatedPolicyTypesUnbound(t *testing.T) {
	c, endpoint, user := enforced(t, server.IAMEnforceEnforce)
	unbound := map[string][]string{
		"iam": {
			"SimulateCustomPolicy", "SimulatePrincipalPolicy",
			"AddUserToGroup", "AttachGroupPolicy", "CreateGroup", "DeleteGroup", "DeleteGroupPolicy", "DetachGroupPolicy",
			"GetGroup", "GetGroupPolicy", "ListAttachedGroupPolicies", "ListGroupPolicies", "ListGroups", "PutGroupPolicy",
			"RemoveUserFromGroup", "UpdateGroup",
		},
		"kms":      {"CreateGrant", "ListGrants", "ListRetirableGrants", "RetireGrant", "RevokeGrant"},
		"sts":      {"DecodeAuthorizationMessage"},
		"logs":     {"PutResourcePolicy", "DescribeResourcePolicies"},
		"dynamodb": {"PutResourcePolicy", "GetResourcePolicy", "DeleteResourcePolicy"},
		"lambda":   {"PutResourcePolicy", "GetResourcePolicy", "DeleteResourcePolicy"},
	}
	data, err := c.Coverage(t.Context(), "json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]coverage.Status{}
	for _, s := range report.Services {
		for _, op := range s.Operations {
			status[string(s.Service)+"."+op.Name] = op.Status
		}
	}
	for service, ops := range unbound {
		for _, op := range ops {
			got, ok := status[service+"."+op]
			if !ok {
				t.Errorf("%s.%s is not in the coverage report", service, op)
				continue
			}
			if _, isUnbound := got.(coverage.Unbound); !isUnbound {
				t.Errorf("%s.%s = %#v, want unbound", service, op, got)
			}
		}
	}
	got := query(t, user, endpoint, "iam", url.Values{
		"Action": {"SimulatePrincipalPolicy"}, "Version": {"2010-05-08"},
		"PolicySourceArn": {userU}, "ActionNames.member.1": {"sqs:ListQueues"},
	})
	if got.status != http.StatusNotImplemented {
		t.Errorf("SimulatePrincipalPolicy under enforce = %d %s, want 501: an unbound operation is not authorized", got.status, got.body)
	}
}
