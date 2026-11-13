package lambda_test

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/store"
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

// TenancyConfig (tenant isolation) and DurableConfig (durable functions)
// configure features understudy does not implement: CreateFunction and
// UpdateFunctionConfiguration refuse them with 501 rather than drop them,
// and change nothing.
func TestTenancyAndDurableConfigAreNotImplemented(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		tenant := zipFunction(h, "tenant")
		tenant.TenancyConfig = &lambdaapi.TenancyConfig{TenantIsolationMode: lambdaapi.TenantIsolationModePerTenant}
		durable := zipFunction(h, "durable")
		durable.DurableConfig = &lambdaapi.DurableConfig{ExecutionTimeout: new(int32(60))}
		before := h.createFunction("fn")
		update := &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Description: new("changed"), DurableConfig: &lambdaapi.DurableConfig{}}
		for _, c := range []struct {
			op, member string
			err        *awserr.Error
		}{
			{"CreateFunction", "TenancyConfig", fail(h, lambdaapi.OpCreateFunction, tenant)},
			{"CreateFunction", "DurableConfig", fail(h, lambdaapi.OpCreateFunction, durable)},
			{"UpdateFunctionConfiguration", "DurableConfig", fail(h, lambdaapi.OpUpdateFunctionConfiguration, update)},
		} {
			if c.err.ID.Status != http.StatusNotImplemented || !strings.HasPrefix(c.err.Message, c.op+" with "+c.member+" ") ||
				!strings.Contains(c.err.Message, "which understudy does not implement; nothing was changed.") {
				t.Errorf("%s with %s = %d %q, want 501 naming the member", c.op, c.member, c.err.ID.Status, c.err.Message)
			}
		}
		for _, name := range []string{"tenant", "durable"} {
			if _, err := h.Invoke(h.Context(), lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: name}); err == nil {
				t.Errorf("a refused CreateFunction created %s", name)
			}
		}
		after := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if *after.RevisionId != *before.RevisionId || *after.Description != *before.Description {
			t.Errorf("a refused UpdateFunctionConfiguration changed fn: revision %s -> %s, description %q -> %q",
				*before.RevisionId, *after.RevisionId, *before.Description, *after.Description)
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

// publishThree publishes versions of fn from three distinct packages, so
// each publish adds a version.
func publishThree(h *harness, fn string) {
	h.Helper()
	for _, body := range []string{"one", "two", "three"} {
		call(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: fn, ZipFile: zipOf(h, body), Publish: true})
	}
}

// A function deleted and created again under its name publishes from the
// number after the highest its name ever had: AWS ran versions 4 and 5 of
// a function recreated after versions 1 to 3 (lambda/R13/notes.md
// "Re-recorded 2026-10-14", lambda/R13/04-create-weighted-alias.json).
func TestVersionNumbersContinueAfterTheFunctionIsRecreated(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.createFunction("fn")
		publishThree(h, "fn")
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		h.createFunction("fn")
		if v := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"}); *v.Version != "4" {
			t.Errorf("first publish of the recreated function = version %s, want 4", *v.Version)
		}
	})
}

// The highest number a deleted function's name had is saved: a function
// created under the name after a snapshot round trip still publishes from
// the next one.
func TestRetiredVersionNumbersSurviveASnapshot(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.createFunction("fn")
		publishThree(h, "fn")
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		h.createFunction("fn")
		if v := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"}); *v.Version != "4" {
			t.Errorf("first publish of the function recreated after the import = version %s, want 4", *v.Version)
		}
	})
}

// A version 6 section, saved before image codes recorded their digest's
// kind and stream mappings their self-disable, still loads.
func TestSnapshotLoadsAVersion6Section(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.createFunction("fn")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Version = 6
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import(a version 6 section) = %v, want it loaded", err)
		}
		call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
	})
}

// A version 4 section, saved before deleted functions' numbers were kept,
// still loads, and a function in it keeps its own numbering.
func TestSnapshotLoadsAVersion4Section(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.createFunction("fn")
		publishThree(h, "fn")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		for i := range sections {
			sections[i].Version = 4
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import(a version 4 section) = %v, want it loaded", err)
		}
		call(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(h, "four")})
		if v := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"}); *v.Version != "4" {
			t.Errorf("publish after loading a version 4 section = version %s, want 4", *v.Version)
		}
	})
}

// State version 2 added each mapping's source kind, version 3 the layer
// store and each function version's layers, version 4 moved an SQS
// mapping's scaling into its source kind, version 5 added deleted
// functions' version numbers, version 6 optional layer-policy fields,
// version 7 image digests' kinds, version 8 a stream mapping's optional
// self-disable, and version 9 directory code. Before 1.0 a section an older
// binary wrote is refused naming both versions rather than migrated (ADR
// 0009); version 3 is migrated (the ADR's amendment of 2026-10-27) and
// versions 4 to 8 read as is (the amendments of 2026-11-13).
func TestSnapshotRefusesAnEarlierVersion(t *testing.T) {
	for _, earlier := range []int{1, 2} {
		run(t, lambda.Options{}, func(h *harness) {
			t := h.T
			h.createFunction("a")
			sections, err := h.svc.Export()
			if err != nil {
				t.Fatal(err)
			}
			for i := range sections {
				sections[i].Version = earlier
			}
			err = h.svc.Import(sections)
			vm, ok := errors.AsType[*store.VersionMismatchError](err)
			if !ok {
				t.Fatalf("Import(a version %d section) = %v, want a *store.VersionMismatchError", earlier, err)
			}
			if vm.Section != earlier || vm.Running != 9 || h.svc.SchemaVersion() != 9 {
				t.Errorf("mismatch %+v, schema version %d; want section %d refused by running 9", vm, h.svc.SchemaVersion(), earlier)
			}
		})
	}
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
