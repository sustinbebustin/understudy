package lambda_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// PublishTo LATEST_PUBLISHED publishes a Lambda Managed Instances version,
// which understudy does not implement: CreateFunction, UpdateFunctionCode
// and PublishVersion refuse it with 501 rather than ignore it, and change
// nothing.
func TestPublishToLatestPublishedIsNotImplemented(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		to := new(lambdaapi.FunctionVersionLatestPublished("LATEST_PUBLISHED"))
		created := zipFunction(h, "managed")
		created.PublishTo = to
		h.createFunction("fn")
		code := &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(h, "#!/bin/sh\necho 2\n"), PublishTo: to}
		for op, err := range map[string]*awserr.Error{
			"CreateFunction":     fail(h, lambdaapi.OpCreateFunction, created),
			"UpdateFunctionCode": fail(h, lambdaapi.OpUpdateFunctionCode, code),
			"PublishVersion":     fail(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn", PublishTo: to}),
		} {
			if err.ID.Status != http.StatusNotImplemented || !strings.HasPrefix(err.Message, op+" with PublishTo LATEST_PUBLISHED publishes the $LATEST.PUBLISHED version") {
				t.Errorf("%s with PublishTo = %d %q, want 501 naming the managed-instances version", op, err.ID.Status, err.Message)
			}
		}
		if _, err := h.Invoke(h.Context(), lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "managed"}); err == nil {
			t.Error("CreateFunction with PublishTo created the function")
		}
		versions := call(h, lambdaapi.OpListVersionsByFunction, &lambdaapi.ListVersionsByFunctionInput{FunctionName: "fn"})
		if len(versions.Versions) != 1 {
			t.Errorf("versions after the refusals = %d, want only $LATEST", len(versions.Versions))
		}
	})
}

// A scope whose functions are all deleted still saves the identifiers it
// minted, so a function created after a snapshot round trip gets no
// RevisionId an earlier one had.
func TestSnapshotOfAnEmptiedScopeKeepsItsIdentifiersUnique(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		first := h.createFunction("a")
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "a"})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		if next := h.createFunction("b"); *next.RevisionId == *first.RevisionId {
			t.Errorf("function b's RevisionId %s repeats deleted function a's", *next.RevisionId)
		}
	})
}

// Function URL ids are not derived from the account, region and a counter
// alone, which every install shares: they depend on the identifier source,
// random unless understudy runs under a seed, and repeat under one seed.
func TestFunctionURLIDsDependOnTheIdentifierSource(t *testing.T) {
	firstURL := func(seed uint64) string {
		var url string
		run(t, lambda.Options{}, func(h *harness) {
			h.createFunction("fn")
			url = call(h, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: "fn", AuthType: lambdaapi.FunctionUrlAuthTypeNone}).FunctionUrl
		}, kerneltest.Seed(seed))
		return url
	}
	one, two, again := firstURL(1), firstURL(2), firstURL(1)
	if one == two {
		t.Errorf("the first function URL is %s under seeds 1 and 2, want ids that differ", one)
	}
	if one != again {
		t.Errorf("the first function URL under seed 1 is %s, then %s; want the same", one, again)
	}
}

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
