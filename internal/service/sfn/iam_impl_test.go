package sfn_test

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// moded is a fake Authorizer that records each question of
// CreateStateMachine and UpdateStateMachine with its mode, and denies
// those whose first action is in deny.
type moded struct {
	mu    sync.Mutex
	deny  []string
	asked []modedQuestion
}

type modedQuestion struct {
	op   string
	mode kernel.PrincipalMode
	q    kernel.AuthzQuery
}

func (m *moded) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	m.mu.Lock()
	defer m.mu.Unlock()
	if op := c.Op().ID.Name; op == "CreateStateMachine" || op == "UpdateStateMachine" {
		m.asked = append(m.asked, modedQuestion{op: op, mode: c.Mode(), q: q})
	}
	if len(q.Pairs) > 0 && slices.Contains(m.deny, q.Pairs[0].Action.String()) {
		return kernel.Denied{Message: "denied by the test"}
	}
	return kernel.Allowed{}
}

func (m *moded) take() []modedQuestion {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.asked
	m.asked = nil
	return out
}

func mustARNOf(h *harness, s string) aws.ARN {
	h.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

// A create asks, after step 8, whether states.amazonaws.com may assume the
// role, as a delivery from the machine in its account, then with logging
// on whether the role may perform each log-delivery action on "*"
// (stepfunctions-api.md 9.5); an update asks the trust only when it passes
// a role, and the logging question against the logging it leaves.
func TestExecutionRoleQuestions(t *testing.T) {
	m := &moded{}
	run(t, func(h *harness) {
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "g"})
		role := mustARNOf(h, h.roleArn())
		machine := mustARNOf(h, h.machineArn("m"))
		trust := modedQuestion{
			op:   "CreateStateMachine",
			mode: kernel.AsService{Name: "states.amazonaws.com", Source: machine, SourceAccount: h.Scope.Account},
			q:    kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}, Resource: kernel.ResourceARN{ARN: role}}}},
		}
		names := []string{"CreateLogDelivery", "GetLogDelivery", "UpdateLogDelivery", "DeleteLogDelivery", "ListLogDeliveries", "PutResourcePolicy", "DescribeResourcePolicies", "DescribeLogGroups"}
		logPairs := make([]kernel.Pair, 0, len(names))
		for _, a := range names {
			logPairs = append(logPairs, kernel.Pair{Action: registry.IAMAction{Prefix: "logs", Name: a}, Resource: kernel.AnyResource{}})
		}
		logging := modedQuestion{op: "CreateStateMachine", mode: kernel.AsExecutionRole{Role: role}, q: kernel.AuthzQuery{Pairs: logPairs}}

		m.take()
		group := "arn:aws:logs:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":log-group:g:*"
		must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "m", Definition: resultMachine, RoleArn: h.roleArn(),
			LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: new(sfnapi.LogLevelAll), Destinations: []sfnapi.LogDestination{
				{CloudWatchLogsLogGroup: &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(group)}},
			}},
		})
		got := m.take()
		if len(got) != 3 || !reflect.DeepEqual(got[1], trust) || !reflect.DeepEqual(got[2], logging) {
			h.Fatalf("create asked %+v\nwant step 8, then %+v, then %+v", got, trust, logging)
		}

		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine.String(), Definition: new(endMachine)})
		if got := m.take(); len(got) != 1 {
			h.Errorf("an update of the definition asked %+v, want step 8 alone", got)
		}
		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine.String(), RoleArn: new(h.roleArn())})
		trust.op, logging.op = "UpdateStateMachine", "UpdateStateMachine"
		if got := m.take(); len(got) != 3 || !reflect.DeepEqual(got[1], trust) || !reflect.DeepEqual(got[2], logging) {
			h.Errorf("an update passing the role asked %+v\nwant step 8, the trust and the stored logging's question", got)
		}
		must(h, sfnapi.OpUpdateStateMachine, &sfnapi.UpdateStateMachineInput{StateMachineArn: machine.String(), LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: new(sfnapi.LogLevelOff)}})
		if got := m.take(); len(got) != 1 {
			h.Errorf("an update turning logging off asked %+v, want step 8 alone", got)
		}
	}, kerneltest.Authorizer(m))
}

// A denied trust or log-delivery question refuses the create with SFN's
// denial in understudy's or AWS's documented words, and creates nothing.
// The trust text and checking at create are understudy's choice.
//
// inferred, SFN-08.
func TestExecutionRoleDenials(t *testing.T) {
	for _, tc := range []struct {
		deny, want string
	}{
		{"sts:AssumeRole", "Step Functions cannot assume the execution role "},
		{"logs:CreateLogDelivery", "The state machine IAM Role is not authorized to access the Log Destination"},
	} {
		t.Run(tc.deny, func(t *testing.T) {
			run(t, func(h *harness) {
				must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "g"})
				group := "arn:aws:logs:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":log-group:g:*"
				_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
					Name: "m", Definition: resultMachine, RoleArn: h.roleArn(),
					LoggingConfiguration: &sfnapi.LoggingConfiguration{Level: new(sfnapi.LogLevelError), Destinations: []sfnapi.LogDestination{
						{CloudWatchLogsLogGroup: &sfnapi.CloudWatchLogsLogGroup{LogGroupArn: new(group)}},
					}},
				})
				if err == nil || err.ID != sfnapi.Service().Denial.Identity || len(err.Message) < len(tc.want) || err.Message[:len(tc.want)] != tc.want {
					h.Fatalf("create with %s denied = %+v, want SFN's denial starting %q", tc.deny, err, tc.want)
				}
				if _, err := call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: h.machineArn("m")}); err == nil {
					h.Error("a refused create left a machine")
				}
			}, kerneltest.Authorizer(&moded{deny: []string{tc.deny}}))
		})
	}
}
