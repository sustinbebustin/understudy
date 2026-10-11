package lambda_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// TestSnapshotRoundTripRearmsTransitions: a state exported mid-transition
// reads back equal after a reset, and Rearm lets the transitions settle,
// since timers are never saved.
func TestSnapshotRoundTripRearmsTransitions(t *testing.T) {
	run(t, lambda.Options{StateTransitions: lambda.TransitionsClock}, func(h *harness) {
		h.createFunction("fn")
		h.passSettleDelay()
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{
			FunctionName: "fn", Name: "live", FunctionVersion: "1",
			RoutingConfig: &lambdaapi.AliasRoutingConfiguration{AdditionalVersionWeights: map[string]float64{}},
		})
		mapping := call(h, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{
			FunctionName: "fn", EventSourceArn: new(h.queue("q")), ScalingConfig: &lambdaapi.ScalingConfig{MaximumConcurrency: new(int32(3))},
		})
		before := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: new("1")})
		if *before.Configuration.State != lambdaapi.StatePending {
			t.Fatalf("published version = %s, want Pending until the settle delay", *before.Configuration.State)
		}

		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		fail(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		after := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: new("1")})
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("after Import:\n%+v\nwant\n%+v", after.Configuration, before.Configuration)
		}
		h.svc.Rearm(h.Clock.Now())
		h.passSettleDelay()
		settled := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: new("1")})
		if *settled.Configuration.State != lambdaapi.StateActive {
			t.Errorf("version after Rearm and the settle delay = %s, want Active", *settled.Configuration.State)
		}
		got := call(h, lambdaapi.OpGetEventSourceMapping, &lambdaapi.GetEventSourceMappingInput{UUID: *mapping.UUID})
		if *got.State != "Enabled" || *got.ScalingConfig.MaximumConcurrency != 3 {
			t.Errorf("mapping after Rearm = %s %+v", *got.State, got.ScalingConfig)
		}
		if resources := h.svc.Resources(h.Scope); len(resources) != 2 || resources[0].Summary != "Active" || resources[1].Summary != "Enabled" {
			t.Errorf("Resources = %+v", resources)
		}
	})
}
