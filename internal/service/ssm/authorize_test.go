package ssm_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// gate is a fake Authorizer that records the step-8 questions about SSM
// calls and answers each with verdict.
type gate struct {
	mu      sync.Mutex
	verdict kernel.Verdict
	asked   map[ref.OperationID][]kernel.Pair
}

func (g *gate) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.Op() != nil && c.Op().ID.Service == "ssm" {
		g.asked[c.Op().ID] = append(g.asked[c.Op().ID], q.Pairs...)
	}
	return g.verdict
}

// pairs returns "action resource" for each pair asked about op.
func (g *gate) pairs(op ref.OperationID) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.asked[op]))
	for _, p := range g.asked[op] {
		res := "*"
		if r, ok := p.Resource.(kernel.ResourceARN); ok {
			res = r.ARN.String()
		}
		out = append(out, p.Action.String()+" "+res)
	}
	slices.Sort(out)
	return out
}

func runGated(t *testing.T, verdict kernel.Verdict, fn func(h *harness, g *gate)) {
	t.Helper()
	g := &gate{verdict: verdict, asked: map[ref.OperationID][]kernel.Pair{}}
	run(t, func(h *harness) { fn(h, g) }, kerneltest.Authorizer(g))
}

// Step 8 asks about each operation's ssm: action on the parameter ARN it
// names (contract 7; ssm-parameters.md 3.3 for the ARN): one ARN per name
// for GetParameters and DeleteParameters, and ssm:AddTagsToResource beside
// ssm:PutParameter only when the put carries Tags (iam-enforcement-surfaces.md
// 12.3; spec enforce matrix). Every binding declares its actions, so no call
// falls back to the whole generated list.
func TestStep8AsksAboutTheParameterARNs(t *testing.T) {
	runGated(t, kernel.Allowed{}, func(h *harness, g *gate) {
		if undeclared := kerneltest.UndeclaredActions(h.Kernel, ssmapi.Service()); len(undeclared) != 0 {
			h.Errorf("bindings without declared actions: %v", undeclared)
		}
		id := func(op string) ref.OperationID { return ref.OperationID{Service: "ssm", Name: op} }
		h.put("/iam/a", "v")
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/iam/tagged", Value: "v", Type: new(ssmapi.ParameterTypeString), Tags: []ssmapi.Tag{{Key: "k", Value: "v"}}})
		h.mustGet("/iam/a", false)
		must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{"/iam/a", "/iam/tagged"}})
		must(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/iam/a"})

		for op, want := range map[string][]string{
			"PutParameter": {
				"ssm:AddTagsToResource " + arnPrefix + "iam/tagged",
				"ssm:PutParameter " + arnPrefix + "iam/a",
				"ssm:PutParameter " + arnPrefix + "iam/tagged",
			},
			"GetParameter":    {"ssm:GetParameter " + arnPrefix + "iam/a"},
			"GetParameters":   {"ssm:GetParameters " + arnPrefix + "iam/a", "ssm:GetParameters " + arnPrefix + "iam/tagged"},
			"DeleteParameter": {"ssm:DeleteParameter " + arnPrefix + "iam/a"},
		} {
			if got := g.pairs(id(op)); !slices.Equal(got, want) {
				h.Errorf("%s asked %v, want %v", op, got, want)
			}
		}
	})
}

// A call denied at step 8 answers SSM's denial identity, read from the
// descriptor (AccessDeniedException, 400; spec conflict 5, inferred, IS
// 12.3), and nothing is written.
func TestDeniedPutStoresNothing(t *testing.T) {
	runGated(t, kernel.Denied{Message: "User: arn:aws:iam::000000000000:user/u is not authorized to perform: ssm:PutParameter"}, func(h *harness, _ *gate) {
		denial := ssmapi.Service().Denial.Identity
		if denial.Shape != "com.amazonaws.ssm#AccessDeniedException" || denial.Status != 400 {
			h.Errorf("ssm denial identity = %s %d, want com.amazonaws.ssm#AccessDeniedException 400", denial.Shape, denial.Status)
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/denied/a", Value: "v", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "a denied PutParameter", err, denial)
		if got := h.svc.Resources(h.Scope); len(got) != 0 {
			h.Errorf("a denied PutParameter stored %+v", got)
		}
	})
}
