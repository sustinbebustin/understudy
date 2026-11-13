package lambda_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// buildDir is a build output directory holding a bootstrap.
func buildDir(tb testing.TB) string {
	tb.Helper()
	// Binding resolves symlinks (hotreload.Bindable), so the directory a
	// function runs is compared in resolved form: macOS's TempDir is under
	// the /var symlink to /private/var.
	dir, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
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

// bucketFunction is zipFunction fn with its code in the hot-reload bucket:
// S3Key names dir, as a LocalStack Terraform configuration does with
// s3_bucket = "hot-reload" (ADR 0010).
func bucketFunction(tb testing.TB, dir string) *lambdaapi.CreateFunctionInput {
	tb.Helper()
	in := zipFunction(tb, "fn")
	in.Code = lambdaapi.FunctionCode{S3Bucket: new("hot-reload"), S3Key: new(dir)}
	return in
}

// isPlaceholderSha checks that sha has CodeSha256's form, the base64 of a
// 32-byte digest, so SDKs and Terraform read it as any other.
func isPlaceholderSha(tb testing.TB, sha string) {
	tb.Helper()
	if raw, err := base64.StdEncoding.DecodeString(sha); err != nil || len(raw) != 32 {
		tb.Errorf("CodeSha256 %q is not the base64 of a 32-byte digest", sha)
	}
}

// A function created from the hot-reload bucket runs the directory its
// S3Key names, through a hot-reload binding: an edit cold-starts the next
// invocation with the new code, and the CodeSha256 the function reports,
// derived from the path, stays as it was, so a second terraform plan stays
// empty (ADR 0010).
func TestHotReloadBucketRunsTheDirectoryAndAnEditColdStartsTheNextInvoke(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		dir := buildDir(t)
		created := call(h.harness, lambdaapi.OpCreateFunction, bucketFunction(t, dir))
		isPlaceholderSha(t, *created.CodeSha256)
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
		after := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if *after.CodeSha256 != *created.CodeSha256 || *after.LastModified != *created.LastModified {
			t.Errorf("the control plane changed under an edit: CodeSha256 %s -> %s, LastModified %s -> %s",
				*created.CodeSha256, *after.CodeSha256, *created.LastModified, *after.LastModified)
		}
	})
}

// Directory code is saved with the state, and a snapshot load binds the
// directory again: after the load the function runs the directory, and an
// edit cold-starts the next invocation.
func TestHotReloadBucketCodeSurvivesASnapshotLoadAndIsBoundAgain(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		dir := buildDir(t)
		created := call(h.harness, lambdaapi.OpCreateFunction, bucketFunction(t, dir))
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		h.Kernel.Timers().Scheduler().Clear()
		h.svc.Rearm(h.Clock.Now())
		h.x.RearmPollers()

		loaded := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"})
		if *loaded.CodeSha256 != *created.CodeSha256 {
			t.Errorf("CodeSha256 after the load = %s, want %s as created", *loaded.CodeSha256, *created.CodeSha256)
		}
		h.invoke(invokeInput("fn", `{}`))
		if err := os.WriteFile(filepath.Join(dir, "lib.py"), []byte("x = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		h.invoke(invokeInput("fn", `{}`))
		specs := h.started()
		if len(specs) != 2 || specs[1].Generation <= specs[0].Generation || codeDir(t, &specs[0]) != dir || codeDir(t, &specs[1]) != dir {
			t.Fatalf("after the load and an edit: specs %+v, want %s run and then cold-started in a newer generation", specs, dir)
		}
	})
}

// The hot-reload bucket's directory passes the checks a binding made
// through the admin API passes, under lambda.hotReloadRoots, since the
// Lambda API may be reachable by more callers than the admin API
// (ARCHITECTURE.md 14.4): a refusal is InvalidParameterValueException
// naming the check, and neither CreateFunction nor UpdateFunctionCode
// stores anything.
func TestHotReloadBucketRefusesAnUnbindableDirectoryAndStoresNothing(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "fn")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := buildDir(t)
	type refusal struct{ name, dir, check string }
	unconfined := []refusal{
		{"relative", "build", "is not an absolute path"},
		{"file", filepath.Join(outside, "bootstrap"), "is not a directory"},
	}
	if os.Getuid() != 0 {
		unconfined = append(unconfined, refusal{"another user's", "/", "is not owned by the user understudy runs as"})
	}
	refuses := func(h *execHarness, cases []refusal) {
		t.Helper()
		h.createFunction("zip")
		before := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "zip"})
		for _, tc := range cases {
			refused := func(op string, err *awserr.Error) {
				t.Helper()
				if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, tc.check) || !strings.Contains(err.Message, "nothing was stored") {
					t.Errorf("%s of %s directory = %s %q, want InvalidParameterValueException naming %q and that nothing was stored",
						op, tc.name, err.ID.Shape.Name(), err.Message, tc.check)
				}
			}
			refused("CreateFunction", fail(h.harness, lambdaapi.OpCreateFunction, bucketFunction(t, tc.dir)))
			_, err := h.Invoke(h.Context(), lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"})
			if err == nil || err.ID.Shape.Name() != "ResourceNotFoundException" {
				t.Errorf("GetFunction after a refused CreateFunction of %s directory = %v, want ResourceNotFoundException", tc.name, err)
			}
			refused("UpdateFunctionCode", fail(h.harness, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{
				FunctionName: "zip", S3Bucket: new("hot-reload"), S3Key: new(tc.dir),
			}))
		}
		after := call(h.harness, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "zip"})
		if *after.CodeSha256 != *before.CodeSha256 || *after.RevisionId != *before.RevisionId {
			t.Errorf("refused UpdateFunctionCode calls changed the function: CodeSha256 %s -> %s, RevisionId %s -> %s",
				*before.CodeSha256, *after.CodeSha256, *before.RevisionId, *after.RevisionId)
		}
	}
	runExec(t, pooltest.Echo, func(h *execHarness) { refuses(h, unconfined) })
	runExec(t, pooltest.Echo, func(h *execHarness) {
		refuses(h, []refusal{{"outside the roots", outside, "is not under a directory lambda.hotReloadRoots lists"}})
		// A directory under a root passes.
		call(h.harness, lambdaapi.OpCreateFunction, bucketFunction(t, inside))
	}, func(c *execConfig) { c.svc.HotReloadRoots = []string{root} })
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
