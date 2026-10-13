package lambda_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// buildDir is a build output directory holding a bootstrap.
func buildDir(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bootstrap"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// codeDir is the /var/task source of a started spec.
func codeDir(tb testing.TB, spec *launcher.Spec) string {
	tb.Helper()
	code, ok := spec.Code.(launcher.ZipCode)
	if !ok {
		tb.Fatalf("spec code = %T, want zip code", spec.Code)
	}
	return code.Dir
}

func TestHotReloadRunsTheDirectoryAndAnEditColdStartsTheNextInvoke(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		before := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		dir := buildDir(t)
		if err := h.x.HotReload(h.Context(), h.Scope, "fn", dir); err != nil {
			t.Fatalf("HotReload: %s %s", err.ID.Shape.Name(), err.Message)
		}
		h.invoke(invokeInput("fn", `{}`))
		h.invoke(invokeInput("fn", `{}`))
		specs := h.started()
		if len(specs) != 1 || codeDir(t, &specs[0]) != dir {
			t.Fatalf("specs = %+v, want one warm environment running %s", specs, dir)
		}

		if err := os.WriteFile(filepath.Join(dir, "lib.py"), []byte("x = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		h.invoke(invokeInput("fn", `{}`))
		specs = h.started()
		if len(specs) != 2 || specs[1].Generation <= specs[0].Generation || codeDir(t, &specs[1]) != dir {
			t.Fatalf("after an edit: specs %+v, want a cold start of a newer generation running %s", specs, dir)
		}
		if live := h.launcher.Live(); len(live) != 1 {
			t.Errorf("live containers = %v, want the old generation's removed", live)
		}
		after := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if *after.CodeSha256 != *before.CodeSha256 || *after.LastModified != *before.LastModified {
			t.Errorf("the control plane changed under hot reload: CodeSha256 %s -> %s, LastModified %s -> %s",
				*before.CodeSha256, *after.CodeSha256, *before.LastModified, *after.LastModified)
		}

		// A reset ends an overlay bound through the admin API.
		h.drainAll()
		h.invoke(invokeInput("fn", `{}`))
		if specs = h.started(); codeDir(t, &specs[len(specs)-1]) == dir {
			t.Error("the overlay outlived the reset of its scope")
		}
	})
}

func TestHotReloadRefusesMissingFunctionsAndUnusableDirectories(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		err := h.x.HotReload(h.Context(), h.Scope, "nope", buildDir(t))
		errorIs(t, err, "ResourceNotFoundException", "Function not found: arn:aws:lambda:us-east-1:000000000000:function:nope")
		err = h.x.HotReload(h.Context(), h.Scope, "fn", "build")
		if err == nil || err.ID.Shape.Name() != "InvalidParameterValueException" {
			t.Fatalf("HotReload of a relative path = %v, want InvalidParameterValueException", err)
		}
	})
}

// An overlay wins over UpdateFunctionCode: the next invocation still runs
// the directory, and the update's own call records that it is shadowed,
// before anything invokes the function, once per update.
func TestUpdateFunctionCodeUnderHotReloadIsShadowedWithAnAdvisory(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		dir := buildDir(t)
		if err := h.x.HotReload(h.Context(), h.Scope, "fn", dir); err != nil {
			t.Fatal(err.Message)
		}
		call(h.harness, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "#!/bin/sh\necho v2\n")})
		advisories := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
		if len(advisories) != 1 {
			t.Fatalf("advisories right after UpdateFunctionCode = %+v, want one", advisories)
		}
		if a, ok := advisories[0].Detail.(journal.Advisory); !ok || a.Kind != journal.AdvisoryShadowedCodeUpdate || a.Resource.String() != "arn:aws:lambda:us-east-1:000000000000:function:fn" {
			t.Errorf("advisory = %+v, want a shadowed code update of fn", advisories[0].Detail)
		}
		update := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
		if parent := advisories[0].Cause.Parent; len(update) == 0 || parent != update[len(update)-1].ID {
			t.Errorf("advisory parent = %v, want the UpdateFunctionCode call's event", parent)
		}
		h.invoke(invokeInput("fn", `{}`))
		h.invoke(invokeInput("fn", `{}`))
		specs := h.started()
		if got := codeDir(t, &specs[len(specs)-1]); got != dir {
			t.Errorf("after UpdateFunctionCode the function runs %s, want the overlay %s", got, dir)
		}
		if n := len(h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events); n != 1 {
			t.Errorf("advisories after two invocations = %d, want still the update's one", n)
		}
	})
}

// A binding from the configuration applies to a function created after
// startup and survives resets.
func TestConfiguredHotReloadAppliesToFunctionsCreatedLater(t *testing.T) {
	dir := buildDir(t)
	scope := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		h.drainAll()
		h.invoke(invokeInput("fn", `{}`))
		for i, spec := range h.started() {
			if got := codeDir(t, &spec); got != dir {
				t.Errorf("invocation %d ran %s, want the configured directory %s", i, got, dir)
			}
		}
	}, func(c *execConfig) {
		c.exec.HotReload = []lambda.HotReloadBinding{{Scope: scope, Function: "fn", Dir: dir}}
	})
}
