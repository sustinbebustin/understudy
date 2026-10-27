package access_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// iam:PassRole recorded with the iam:PassedToService its call sent is
// granted on that condition, and passes of one role to two services are
// two statements; a pass that sent none stays unconditional
// (iam-enforcement-surfaces.md 12.6).
func TestPolicyGrantsPassRoleToTheServiceItWasPassedTo(t *testing.T) {
	var r access.Recorder
	role := arn(t, roleARN)
	sm := arn(t, "arn:aws:states:us-east-1:111111111111:stateMachine:m")
	pass := func(service string) access.Entry {
		return access.Entry{
			Principal: app, Mode: access.Direct{}, Service: "states",
			Actions:   []registry.IAMAction{act("states", "CreateStateMachine"), act("iam", "PassRole")},
			Resources: []aws.ARN{role},
			Pairs: []access.Pair{
				{Action: act("states", "CreateStateMachine"), Resource: access.OnResource{ARN: sm}},
				{Action: act("iam", "PassRole"), Resource: access.OnResource{ARN: role}, PassedToService: service},
			},
			Outcome: access.Succeeded{}, At: t0,
		}
	}
	record(t, &r, pass("states.amazonaws.com"), pass("states.amazonaws.com"), pass("lambda.amazonaws.com"), pass(""))
	want := `{"Version":"2012-10-17","Statement":[` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + roleARN + `"},` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + roleARN + `","Condition":{"StringEquals":{"iam:PassedToService":"lambda.amazonaws.com"}}},` +
		`{"Effect":"Allow","Action":"iam:PassRole","Resource":"` + roleARN + `","Condition":{"StringEquals":{"iam:PassedToService":"states.amazonaws.com"}}},` +
		`{"Effect":"Allow","Action":"states:CreateStateMachine","Resource":"` + sm.String() + `"}]}`
	if got := userPolicy(t, &r); got != want {
		t.Errorf("Policy =\n%s\nwant\n%s", got, want)
	}
	var states int
	for _, u := range r.Usage(access.Filter{Principal: app, Service: "iam"}) {
		if u.PassedToService == "states.amazonaws.com" {
			states = u.Calls
		}
	}
	if states != 2 {
		t.Errorf("iam:PassRole to states.amazonaws.com counted %d calls, want 2", states)
	}
}
