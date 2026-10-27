package lambda_test

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// withLayers is fn's CreateFunction input naming layers.
func withLayers(tb testing.TB, name string, layers ...string) *lambdaapi.CreateFunctionInput {
	tb.Helper()
	in := zipFunction(tb, name)
	in.Layers = layers
	return in
}

// layerRefs is a configuration's Layers as "<arn> <size>".
func layerRefs(c *lambdaapi.FunctionConfiguration) []string {
	out := make([]string, 0, len(c.Layers))
	for _, l := range c.Layers {
		out = append(out, deref(l.Arn)+" "+itoa(l.CodeSize))
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A function's layers are resolved when it is created and render as
// Layers [{Arn, CodeSize}] in the order given, on every read of the
// configuration and in ListFunctions, and a published version keeps them
// (lambda-destinations-layers.md 7.2: test_lambda_api.snapshot.json:L8631-L8687;
// order as Terraform's acceptance test against AWS requires, 9.2).
func TestCreateFunctionResolvesItsLayers(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		a := pythonLayer(t)
		b := layerArchive(t, map[string]string{"bin/tool": "#!/bin/sh\necho tool\n"})
		h.publish("a", a)
		h.publish("b", b)
		want := []string{layerPrefix + "b:1 " + itoa(int64(len(b))), layerPrefix + "a:1 " + itoa(int64(len(a)))}
		created := call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"b:1", layerPrefix+"a:1"))
		if got := layerRefs(created); !slices.Equal(got, want) {
			t.Errorf("CreateFunction Layers = %v, want %v", got, want)
		}
		for _, l := range created.Layers {
			if l.SigningJobArn != nil || l.SigningProfileVersionArn != nil {
				t.Errorf("layer %+v carries a signing ARN, want none (11.8)", l)
			}
		}
		h.settle()
		reads := map[string]*lambdaapi.FunctionConfiguration{
			"GetFunctionConfiguration": call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"}),
			"GetFunction":              call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn"}).Configuration,
		}
		if fns := call(h, lambdaapi.OpListFunctions, &lambdaapi.ListFunctionsInput{}).Functions; len(fns) == 1 {
			reads["ListFunctions"] = &fns[0]
		} else {
			t.Errorf("ListFunctions = %d functions, want 1", len(fns))
		}
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		reads["version 1"] = call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn", Qualifier: new("1")})
		for name, c := range reads {
			if got := layerRefs(c); !slices.Equal(got, want) {
				t.Errorf("%s Layers = %v, want %v", name, got, want)
			}
		}
	})
}

// A function without layers has no Layers key (lambda-destinations-layers.md
// 7.2: test_lambda_api.snapshot.json:L8469-L8519); UpdateFunctionConfiguration
// replaces the list whole and an empty one clears it, which Terraform sends
// to remove every layer and reads back as none (9.2).
func TestUpdateFunctionConfigurationReplacesTheLayers(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("a", pythonLayer(t))
		h.publish("b", pythonLayer(t))
		if created := h.createFunction("fn"); created.Layers != nil {
			t.Errorf("a function created without layers has Layers %+v, want none", created.Layers)
		}
		h.settle()
		updated := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "a:1", layerPrefix + "b:1"}})
		if got := layerRefs(updated); len(got) != 2 || !strings.HasPrefix(got[0], layerPrefix+"a:1 ") || !strings.HasPrefix(got[1], layerPrefix+"b:1 ") {
			t.Errorf("update to a and b = %v", got)
		}
		h.settle()
		updated = call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "b:1"}})
		if got := layerRefs(updated); len(got) != 1 || !strings.HasPrefix(got[0], layerPrefix+"b:1 ") {
			t.Errorf("update to b alone = %v", got)
		}
		h.settle()
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{}})
		h.settle()
		if got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"}); len(got.Layers) != 0 {
			t.Errorf("after Layers [] the configuration has %+v, want none", got.Layers)
		}
	})
}

// A deleted layer version stays on the functions that use it, but a new
// attach of it is refused as a version that does not exist
// (lambda-destinations-layers.md 6.6, 7.2: test_lambda_api.snapshot.json:L8801-L8851;
// https://docs.aws.amazon.com/lambda/latest/dg/adding-layers.html).
func TestDeletedLayerVersionStaysButCannotBeAttached(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("a", pythonLayer(t))
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"a:1"))
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		h.settle()
		if got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"}); len(got.Layers) != 1 || deref(got.Layers[0].Arn) != layerPrefix+"a:1" {
			t.Errorf("after the delete fn has Layers %+v, want a:1 still", got.Layers)
		}
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "other", layerPrefix+"a:1")),
			"InvalidParameterValueException", "Layer version "+layerPrefix+"a:1 does not exist.")
	})
}

// Attaching layers is refused as AWS refuses it, with the recorded texts
// (lambda-destinations-layers.md 7.1: test_lambda_api.snapshot.json:L9336-L9417,
// L12313-L12329).
func TestLayerAttachRefusals(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		for range 3 {
			h.publish("shared", pythonLayer(t))
		}
		six := make([]string, 0, 6)
		for _, name := range []string{"l1", "l2", "l3", "l4", "l5", "l6"} {
			six = append(six, deref(h.publish(name, pythonLayer(t)).LayerVersionArn))
		}
		h.createFunction("fn")
		h.settle()
		update := func(layers ...string) *lambdaapi.UpdateFunctionConfigurationInput {
			return &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: layers}
		}
		same := func(a, b string) string {
			return "Two different versions of the same layer are not allowed to be referenced in the same function. " + a + " and " + b + " are versions of the same layer."
		}
		v := func(n string) string { return layerPrefix + "shared:" + n }
		cases := []struct {
			name  string
			err   func() *awserr.Error
			shape string
			text  string
		}{
			{
				"six layers", func() *awserr.Error { return fail(h, lambdaapi.OpCreateFunction, withLayers(t, "six", six...)) },
				"InvalidParameterValueException", "Cannot reference more than 5 layers.",
			},
			{
				"two versions of one layer", func() *awserr.Error { return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(v("1"), v("2"))) },
				"InvalidParameterValueException", same(v("1"), v("2")),
			},
			{
				"three versions of one layer", func() *awserr.Error {
					return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(v("1"), v("2"), v("3")))
				},
				"InvalidParameterValueException", same(v("1"), v("2")),
			},
			{
				"one version twice", func() *awserr.Error { return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(v("1"), v("1"))) },
				"InvalidParameterValueException", same(v("1"), v("1")),
			},
			{"a layer that does not exist", func() *awserr.Error {
				return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(layerPrefix+"doesnotexist:1"))
			}, "InvalidParameterValueException", "Layer version " + layerPrefix + "doesnotexist:1 does not exist."},
			{
				"a version that does not exist", func() *awserr.Error { return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(v("9"))) },
				"InvalidParameterValueException", "Layer version " + v("9") + " does not exist.",
			},
			{
				"a layer ARN without a version", func() *awserr.Error {
					return fail(h, lambdaapi.OpUpdateFunctionConfiguration, update(layerPrefix+"shared"))
				},
				"ValidationException", "1 validation error detected: Value '[" + layerPrefix + "shared]' at 'layers' failed to satisfy constraint: Member must satisfy constraint: " +
					"[Member must have length less than or equal to 2048, Member must have length greater than or equal to 1, Member must satisfy regular expression pattern: " +
					`(arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)|(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)` +
					", Member must not be null]",
			},
			{"a layer in another region", func() *awserr.Error {
				return fail(h, lambdaapi.OpCreateFunction, withLayers(t, "elsewhere", "arn:aws:lambda:eu-west-1:000000000000:layer:shared:1"))
			}, "InvalidParameterValueException", "Layers are not in the same region as the function. Layers are expected to be in region us-east-1."},
		}
		for _, c := range cases {
			err := c.err()
			errorIs(t, err, c.shape, c.text)
			if err.ID.Status != 400 {
				t.Errorf("%s: status %d, want 400", c.name, err.ID.Status)
			}
		}
		if got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn"}); len(got.Layers) != 0 {
			t.Errorf("after the refusals fn has Layers %+v, want none", got.Layers)
		}
	})
}

// A layer understudy never received keeps AWS's sentence for a version
// that does not exist, then says why: understudy never fetches a layer from
// AWS, so another account's must be published to it, and an AWS-managed
// one cannot be (lambda-destinations-layers.md 8.4, 11.5.6).
func TestLayersUnderstudyNeverReceivedSayWhy(t *testing.T) {
	const why = " understudy has only the layers published to it and never fetches layers from AWS; "
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		const powertools = "arn:aws:lambda:us-east-1:017000801446:layer:AWSLambdaPowertoolsPythonV3-python313-x86_64:40"
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", powertools)), "InvalidParameterValueException",
			"Layer version "+powertools+" does not exist."+why+"publish the archive to understudy with credentials for account 017000801446 and use the version that returns.")
		const managed = "arn:aws:lambda:::awslayer:AWSLambda-Python-AWS-SDK"
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", managed)), "InvalidParameterValueException",
			"Layer version "+managed+" does not exist."+why+"an AWS-managed layer cannot be published to understudy, so publish its content as a layer of your own.")
	})
}

// Compatible runtimes and architectures are metadata: whether an attach of
// a layer marked for another runtime or architecture is refused is
// unrecorded, and the switch lambda.layers.compatibility says what
// understudy does (lambda-destinations-layers.md finding 11, 7.3, 11.2).
func TestLayerCompatibilityFollowsTheSwitch(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		if v := h.switchValue("lambda.layers.compatibility"); v != "not-enforced" {
			t.Fatalf("lambda.layers.compatibility = %q, a value this test does not know", v)
		}
		h.publish("node", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimeNodejs22x)
		})
		h.publish("arm", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleArchitectures = archs(lambdaapi.ArchitectureArm64)
		})
		created := call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"node:1", layerPrefix+"arm:1"))
		if len(created.Layers) != 2 {
			t.Errorf("a provided.al2023 x86_64 function with nodejs22.x and arm64 layers has Layers %+v, want both attached", created.Layers)
		}
	})
}

// policyWouldDeny returns the policy-would-deny advisories naming resource.
func policyWouldDeny(h *harness, resource string) []journal.Advisory {
	h.Helper()
	var out []journal.Advisory
	events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 100}).Events
	for i := range events {
		if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryPolicyWouldDeny && a.Resource.String() == resource {
			out = append(out, a)
		}
	}
	return out
}

// asAccount runs fn with client calls made in account in the test region.
func (h *harness) asAccount(account string, fn func()) {
	h.Helper()
	id, err := aws.ParseAccountID(account)
	if err != nil {
		h.Fatal(err)
	}
	saved := h.Scope
	h.Scope.Account = id
	defer func() { h.Scope = saved }()
	fn()
}

// Another account's layer that understudy holds attaches, and when the
// owner's version policy grants the caller's account nothing, the journal
// says AWS would refuse it: the switch lambda.layers.cross-account-policy
// (lambda-destinations-layers.md 7.4, 11.2). A statement for the caller's
// account, for everyone, or for an organization counts as a grant.
func TestAnotherAccountsLayerFollowsTheCrossAccountSwitch(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		if v := h.switchValue("lambda.layers.cross-account-policy"); v != "advise" {
			t.Fatalf("lambda.layers.cross-account-policy = %q, a value this test does not know", v)
		}
		const owner = "111111111111"
		const theirs = "arn:aws:lambda:us-east-1:" + owner + ":layer:"
		h.asAccount(owner, func() {
			for _, name := range []string{"private", "granted", "public", "org"} {
				h.publish(name, pythonLayer(t))
			}
			for name, in := range map[string]*lambdaapi.AddLayerVersionPermissionInput{
				"granted": {Principal: "000000000000"},
				"public":  {Principal: "*"},
				"org":     {Principal: "*", OrganizationId: new("o-0123456789")},
			} {
				in.LayerName, in.VersionNumber, in.StatementId, in.Action = name, 1, "s1", "lambda:GetLayerVersion"
				call(h, lambdaapi.OpAddLayerVersionPermission, in)
			}
		})
		created := call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", theirs+"private:1"))
		if len(created.Layers) != 1 || deref(created.Layers[0].Arn) != theirs+"private:1" {
			t.Fatalf("Layers = %+v, want account %s's private:1 attached", created.Layers, owner)
		}
		advisories := policyWouldDeny(h, theirs+"private:1")
		if len(advisories) != 1 {
			t.Fatalf("policy-would-deny advisories for private:1 = %+v, want one", advisories)
		}
		for _, want := range []string{owner, "000000000000", "lambda.layers.cross-account-policy"} {
			if !strings.Contains(advisories[0].Text, want) {
				t.Errorf("advisory %q does not name %s", advisories[0].Text, want)
			}
		}
		for _, name := range []string{"granted", "public", "org"} {
			call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn-"+name, theirs+name+":1"))
			if got := policyWouldDeny(h, theirs+name+":1"); len(got) != 0 {
				t.Errorf("attaching %s:1, which the owner granted, journaled %+v", name, got)
			}
		}
	})
}

// CreateFunction and UpdateFunctionConfiguration declare
// lambda:GetLayerVersion on each layer version they attach, an action the
// model lists for CreateFunction (lambda-destinations-layers.md 7.4, the
// Smithy model's aws.iam#iamAction), so the access record holds it.
func TestAttachingALayerRecordsGetLayerVersion(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("a", pythonLayer(t))
		h.publish("b", pythonLayer(t))
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"a:1"))
		h.settle()
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "b:1"}})
		used := map[string]bool{}
		uses := h.Access.Usage(access.Filter{Principal: access.Root{Account: h.Scope.Account}, Service: "lambda"})
		for i := range uses {
			if r, ok := uses[i].Resource.(access.OnResource); ok && uses[i].Action == (registry.IAMAction{Prefix: "lambda", Name: "GetLayerVersion"}) {
				used[r.ARN.String()] = true
			}
		}
		for _, arn := range []string{layerPrefix + "a:1", layerPrefix + "b:1"} {
			if !used[arn] {
				t.Errorf("the access record has no lambda:GetLayerVersion on %s: %+v", arn, uses)
			}
		}
	})
}

// zeroZip is a zip holding one file of n zero bytes, deflated at the
// fastest level so a large unzipped size stays a small archive.
func zeroZip(tb testing.TB, name string, n int64) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	w.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) { return flate.NewWriter(out, flate.BestSpeed) })
	f, err := w.Create(name)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := io.CopyN(f, zeroReader{}, n); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// The function's code and its layers together may hold 262,144,000 bytes
// unzipped ("250 MB unzipped including layers",
// https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html):
// a create, a configuration update and a code update that pass it are
// refused with a message that begins as AWS's does
// (lambda-destinations-layers.md 7.1, 11.5.4; the rest of the text is
// unrecorded, LDL-14).
func TestCodeAndLayersTogetherHaveTheUnzippedLimit(t *testing.T) {
	const prefix = "Function code combined with layers exceeds the maximum allowed size"
	half := zeroZip(t, "data/half.bin", 131_072_000)
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("big-a", half)
		h.publish("big-b", half)
		over := func(err *awserr.Error, what string) {
			t.Helper()
			if err.ID.Shape.Name() != "InvalidParameterValueException" || err.ID.Status != 400 || !strings.HasPrefix(err.Message, prefix) {
				t.Errorf("%s = %d %s %q, want 400 InvalidParameterValueException beginning %q", what, err.ID.Status, err.ID.Shape.Name(), err.Message, prefix)
			}
		}
		over(fail(h, lambdaapi.OpCreateFunction, withLayers(t, "both", layerPrefix+"big-a:1", layerPrefix+"big-b:1")), "CreateFunction with both halves")
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"big-a:1"))
		h.settle()
		over(fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{
			FunctionName: "fn", Layers: []string{layerPrefix + "big-a:1", layerPrefix + "big-b:1"},
		}), "UpdateFunctionConfiguration adding the second half")
		over(fail(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zeroZip(t, "bootstrap", 131_072_001)}),
			"UpdateFunctionCode of a half beside the attached half")
	})
}
