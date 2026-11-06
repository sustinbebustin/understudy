package sfn_test

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
)

// v4UUID is the form of States.UUID (stepfunctions-asl.md 5.2).
var v4UUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// CreateStateMachine accepts intrinsic calls once they run; an execution
// draws States.UUID and unseeded States.MathRandom from the service's ids
// kinds, a fresh value each time (stepfunctions-asl.md 5.2).
func TestIntrinsicsRunThroughTheService(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("intrinsics", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"id.$":"States.UUID()","n.$":"States.MathRandom(10, 20)","sum.$":"States.MathAdd($.a, 1.5)"},"End":true}}}`)
		ids := make([]string, 0, 2)
		for _, name := range []string{"one", "two"} {
			out := h.describeExecution(h.start(machine, name, new(`{"a":1}`)))
			if out.Status != sfnapi.ExecutionStatusSucceeded {
				h.Fatalf("execution %s: status %s, error %s, cause %s", name, out.Status, val(out.Error), val(out.Cause))
			}
			v := decode(h, val(out.Output))
			id, idOK := at(v, "id").(document.String)
			n, nOK := at(v, "n").(document.Number)
			i, err := strconv.Atoi(string(n))
			if !idOK || !nOK || !v4UUID.MatchString(string(id)) || err != nil || i < 10 || i >= 20 || !equal(at(v, "sum"), document.Number("3")) {
				h.Fatalf("execution %s output = %s, want a v4 UUID, an integer in [10, 20) and sum 3", name, val(out.Output))
			}
			ids = append(ids, string(id))
		}
		if ids[0] == ids[1] {
			h.Errorf("two executions drew the same States.UUID %s", ids[0])
		}
	})
}

// A failing function fails the execution with the switch's error and AWS's
// cause (stepfunctions-asl.md 5.4, switch sfn.intrinsic.failure-error;
// ASL-05, recorded 2026-11-03).
func TestIntrinsicFailureThroughTheService(t *testing.T) {
	run(t, func(h *harness) {
		machine := h.create("fails", `{"StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"States.ArrayRange(0, 10, 0)"},"End":true}}}`)
		out := h.describeExecution(h.start(machine, "e", new(`{}`)))
		const cause = "An error occurred while executing the state 'P' (entered at the event id #2). " +
			"There was an error while evaluating the intrinsic function: States.ArrayRange(0, 10, 0). Invalid arguments in States.ArrayRange"
		if out.Status != sfnapi.ExecutionStatusFailed || val(out.Error) != sfn.IntrinsicFailure().Value.Name || val(out.Cause) != cause {
			h.Fatalf("execution: status %s, error %q, cause %q; want FAILED with %s", out.Status, val(out.Error), val(out.Cause), sfn.IntrinsicFailure().Value.Name)
		}
	})
}
