package server_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// The iam-loop's (d) D4 note rests on this: with identityPolicies [] only a
// resource-based policy can allow, so a pair still allowed then was decided
// by the resource's policy. A same-account queue policy naming the user
// needs no identity policy; one naming the account sends the decision to
// the identity policy, which here is none (iam-evaluation.md 2.3).
func TestIAMEvaluateWithNoIdentityPolicyAllowsOnlyWhatAResourcePolicyGrantsTheUser(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceOff)
	for _, q := range []struct{ name, principal string }{
		{"names-user", userU},
		{"names-account", "arn:aws:iam::000000000000:root"},
	} {
		mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+q.name+`"}`)
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + q.principal +
			`"},"Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:000000000000:` + q.name + `"}]}`
		mustSQS(t, endpoint, "SetQueueAttributes", `{"QueueUrl":"`+endpoint+`/000000000000/`+q.name+
			`","Attributes":{"Policy":`+strconv.Quote(policy)+`}}`)
	}

	none := []json.RawMessage{}
	got, err := c.IAMEvaluate(t.Context(), admin.EvaluateRequest{Principal: userU, IdentityPolicies: &none, Requests: []admin.EvaluateItem{
		{Action: "sqs:SendMessage", Resource: "arn:aws:sqs:us-east-1:000000000000:names-user"},
		{Action: "sqs:SendMessage", Resource: "arn:aws:sqs:us-east-1:000000000000:names-account"},
	}})
	if err != nil {
		t.Fatalf("IAMEvaluate = %v", err)
	}
	want := []string{"allowed", "implicitDeny"}
	if len(got.Results) != len(want) {
		t.Fatalf("results = %+v, want %d", got.Results, len(want))
	}
	for i, w := range want {
		if got.Results[i].Decision != w {
			t.Errorf("%s on %s with no identity policy = %+v, want %s", got.Results[i].Action, got.Results[i].Resource, got.Results[i], w)
		}
	}
}
