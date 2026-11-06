package sfn_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// gate is a fake Authorizer: it records every question with the principal
// mode it was asked under and denies any question that names an action in
// deny.
type gate struct {
	mu    sync.Mutex
	deny  []string
	asked []question
}

type question struct {
	op   string
	mode kernel.PrincipalMode // nil for the caller's own questions; set by kernel.AuthorizeAs
	q    kernel.AuthzQuery
}

func (g *gate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, question{op: c.Op().ID.Name, mode: c.Mode(), q: q})
	for _, p := range q.Pairs {
		if slices.Contains(g.deny, p.Action.String()) || slices.Contains(g.deny, "*") {
			return kernel.Denied{Message: "User is not authorized to perform: " + p.Action.String()}
		}
	}
	return kernel.Allowed{}
}

// questions returns what was asked during op.
func (g *gate) questions(op string) []kernel.AuthzQuery {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []kernel.AuthzQuery
	for _, a := range g.asked {
		if a.op == op {
			out = append(out, a.q)
		}
	}
	return out
}

// actions lists every action the caller was asked about during op, leaving
// out questions about another principal, such as the role trust question
// asked as states.amazonaws.com (kernel.AuthorizeAs).
func (g *gate) actions(op string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for _, a := range g.asked {
		if a.op != op || a.mode != nil {
			continue
		}
		for _, p := range a.q.Pairs {
			if a := p.Action.String(); !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	return out
}

var passRole = registry.IAMAction{Prefix: "iam", Name: "PassRole"}

// A denied caller gets SFN's denial identity, AccessDeniedException at the
// status of switch sfn.denial.status (iam-enforcement-surfaces.md 12.3;
// recording case SFN-15).
func TestDeniedCall(t *testing.T) {
	g := &gate{deny: []string{"*"}}
	run(t, func(h *harness) {
		_, err := call(h, sfnapi.OpListStateMachines, &sfnapi.ListStateMachinesInput{})
		if err == nil || err.ID.Shape.Name() != "AccessDeniedException" || err.ID.Status != sfn.DenialStatus().Value {
			h.Fatalf("ListStateMachines = %+v, want AccessDeniedException at %d", err, sfn.DenialStatus().Value)
		}
	}, kerneltest.Authorizer(g))
}

// CreateStateMachine asks for states:CreateStateMachine and, with tags,
// states:TagResource, never the whole generated action list; and it asks
// for iam:PassRole on the role with iam:PassedToService
// states.amazonaws.com (stepfunctions-api.md 5.1, 9.5; aws-doc
// service-authorization list_stepfunctions; spec "Enforce matrix").
func TestCreateStateMachineAuthorization(t *testing.T) {
	g := &gate{}
	run(t, func(h *harness) {
		h.create("plain", resultMachine)
		if got := g.actions("CreateStateMachine"); !slices.Equal(got, []string{"iam:PassRole", "states:CreateStateMachine"}) {
			h.Errorf("untagged create asked about %v, want iam:PassRole and states:CreateStateMachine", got)
		}
		passed := false
		for _, q := range g.questions("CreateStateMachine") {
			for _, p := range q.Pairs {
				if p.Action != passRole {
					continue
				}
				r, ok := p.Resource.(kernel.ResourceARN)
				if !ok || r.ARN.String() != h.roleArn() || !slices.Equal(q.Keys["iam:PassedToService"], []string{"states.amazonaws.com"}) {
					h.Errorf("PassRole question = %+v, want the role with iam:PassedToService=states.amazonaws.com", q)
				}
				passed = true
			}
		}
		if !passed {
			h.Error("CreateStateMachine never asked about iam:PassRole")
		}
		g.mu.Lock()
		g.asked = nil
		g.mu.Unlock()
		must(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{
			Name: "tagged", Definition: resultMachine, RoleArn: h.roleArn(),
			Tags: []sfnapi.Tag{{Key: new("k"), Value: new("v")}},
		})
		if got := g.actions("CreateStateMachine"); !slices.Equal(got, []string{"iam:PassRole", "states:CreateStateMachine", "states:TagResource"}) {
			h.Errorf("tagged create asked about %v, want PassRole, CreateStateMachine and TagResource", got)
		}
	}, kerneltest.Authorizer(g))
}

// A denied iam:PassRole refuses the create with SFN's denial and creates
// nothing (spec "Enforce matrix").
func TestPassRoleDenied(t *testing.T) {
	g := &gate{deny: []string{"iam:PassRole"}}
	run(t, func(h *harness) {
		_, err := call(h, sfnapi.OpCreateStateMachine, &sfnapi.CreateStateMachineInput{Name: "m", Definition: resultMachine, RoleArn: h.roleArn()})
		if err == nil || err.ID.Shape.Name() != "AccessDeniedException" || err.ID.Status != sfn.DenialStatus().Value {
			h.Fatalf("create with PassRole denied = %+v, want AccessDeniedException", err)
		}
		if _, err := call(h, sfnapi.OpDescribeStateMachine, &sfnapi.DescribeStateMachineInput{StateMachineArn: h.machineArn("m")}); err == nil {
			h.Error("a refused create left a machine")
		}
	}, kerneltest.Authorizer(g))
}

// A Lambda task whose role may not invoke the function fails with the
// unmodeled Lambda name and an AccessDeniedException cause, and the
// function never runs (spec "Enforce matrix" last row; switch
// sfn.integration.lambda-error; recording case SFN-15).
func TestLambdaInvokeDenied(t *testing.T) {
	g := &gate{deny: []string{"lambda:InvokeFunction"}}
	run(t, func(h *harness) {
		arn := h.start(h.invokeTaskMachine("m", h.functionArn("f")), "x", new(`{}`))
		d := h.describeExecution(arn)
		if d.Status != sfnapi.ExecutionStatusFailed || val(d.Error) != sfn.LambdaError().Value.Unmodeled || !strings.Contains(val(d.Cause), "Error Code: AccessDeniedException") {
			h.Errorf("denied invoke: %s %s %q, want FAILED %s with an AccessDeniedException cause", d.Status, val(d.Error), val(d.Cause), sfn.LambdaError().Value.Unmodeled)
		}
		if calls := h.invoked(); len(calls) != 0 {
			h.Errorf("the function ran %d times, want never", len(calls))
		}
	}, kerneltest.Authorizer(g))
}

// What /iam/policy builds on: a create without tags records the caller's
// states:CreateStateMachine and iam:PassRole, and a Lambda task records
// lambda:InvokeFunction for the execution role (spec "Enforce matrix").
func TestAccessRecorded(t *testing.T) {
	run(t, func(h *harness) {
		h.Access.Reset()
		machine := h.legacyTaskMachine("m", h.functionArn("f"))
		root := access.Root{Account: h.Scope.Account}
		var got []string
		for _, u := range h.Access.Usage(access.Filter{Principal: root}) {
			if a := u.Action.String(); !slices.Contains(got, a) {
				got = append(got, a)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, []string{"iam:PassRole", "states:CreateStateMachine"}) {
			h.Errorf("create recorded %v for the caller, want iam:PassRole and states:CreateStateMachine", got)
		}
		h.start(machine, "x", new(`{}`))
		role := access.Role{Account: h.Scope.Account, Name: "sfn-role"}
		invoked := false
		for _, u := range h.Access.Usage(access.Filter{Principal: role, Service: "lambda"}) {
			if u.Action.String() == "lambda:InvokeFunction" {
				invoked = true
			}
		}
		if !invoked {
			h.Error("no lambda:InvokeFunction recorded for the execution role")
		}
	})
}
