package lambda_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// runBlobs is run with the blob store the Lambda service keeps archives in.
func runBlobs(t *testing.T, opts lambda.Options, fn func(h *harness, blobs *blob.Store)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory("", 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		fn(&harness{K: k, svc: lambda.New(deps, opts)}, blobs)
	}, kerneltest.Services(lambdaapi.Service(), sqsapi.Service()))
}

// archiveRef is the blob an archive is kept as: its SHA-256.
func archiveRef(tb testing.TB, archive []byte) blob.Ref {
	tb.Helper()
	sum := sha256.Sum256(archive)
	ref, err := blob.ParseRef(hex.EncodeToString(sum[:]))
	if err != nil {
		tb.Fatal(err)
	}
	return ref
}

// kept reports whether the blob store still holds ref.
func kept(tb testing.TB, blobs *blob.Store, ref blob.Ref) bool {
	tb.Helper()
	_, err := blobs.Size(ref)
	if _, gone := errors.AsType[*blob.NotFoundError](err); gone {
		return false
	}
	if err != nil {
		tb.Fatal(err)
	}
	return true
}

// uniqueLayer is a layer archive no other test archive shares, so its blob
// counts only the references to it.
func uniqueLayer(tb testing.TB, name string) []byte {
	tb.Helper()
	return layerArchive(tb, map[string]string{"python/" + name + ".py": name + " = 1\n"})
}

// Every function version that lists a layer keeps its archive, so the
// archive outlives the layer version's delete until the last such version
// goes, and no version keeps it longer (lambda-destinations-layers.md 7.2,
// 11.5.2, 11.5.4).
func TestFunctionVersionsKeepTheirLayersArchives(t *testing.T) {
	runBlobs(t, lambda.Options{}, func(h *harness, blobs *blob.Store) {
		t := h.T
		archive := uniqueLayer(t, "kept")
		ref := archiveRef(t, archive)
		h.publish("a", archive)
		in := withLayers(t, "fn", layerPrefix+"a:1")
		in.Publish = true
		call(h, lambdaapi.OpCreateFunction, in)
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		h.settle()
		if !kept(t, blobs, ref) {
			t.Fatal("the archive is gone after the layer version's delete, while $LATEST and version 1 use it")
		}
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{}})
		h.settle()
		call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		if !kept(t, blobs, ref) {
			t.Fatal("the archive is gone while version 1 still uses it")
		}
		if got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn", Qualifier: new("2")}); got.Layers != nil {
			t.Errorf("version 2, published without layers, has Layers %+v", got.Layers)
		}
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn", Qualifier: new("1")})
		if kept(t, blobs, ref) {
			t.Error("the archive outlives every version that used it")
		}
	})
}

// UpdateFunctionCode with Publish publishes $LATEST's layers with the new
// code, a deleted layer version included; a publish that adds no version
// takes no reference, so deleting the function frees the archive.
func TestUpdateFunctionCodePublishesTheLayers(t *testing.T) {
	runBlobs(t, lambda.Options{}, func(h *harness, blobs *blob.Store) {
		t := h.T
		archive := uniqueLayer(t, "code")
		ref := archiveRef(t, archive)
		h.publish("a", archive)
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"a:1"))
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		h.settle()
		for range 2 {
			call(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zipOf(t, "v2"), Publish: true})
			h.settle()
		}
		if got := layerRefs(call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn", Qualifier: new("1")})); len(got) != 1 ||
			!strings.HasPrefix(got[0], layerPrefix+"a:1 ") {
			t.Errorf("version 1 Layers = %v, want a:1", got)
		}
		if _, err := h.Invoke(h.Context(), lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn", Qualifier: new("2")}); err == nil {
			t.Error("the second publish of unchanged code added version 2")
		}
		call(h, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "fn"})
		if kept(t, blobs, ref) {
			t.Error("the archive outlives the function")
		}
	})
}

// Under lambda.stateTransitions clock, an update in progress serves the
// configuration it replaces, layers included, so their archives stay until
// it settles.
func TestAnUpdateInProgressKeepsTheLayersItServes(t *testing.T) {
	runBlobs(t, lambda.Options{StateTransitions: lambda.TransitionsClock}, func(h *harness, blobs *blob.Store) {
		t := h.T
		old := uniqueLayer(t, "old")
		ref := archiveRef(t, old)
		h.publish("a", old)
		h.publish("b", uniqueLayer(t, "new"))
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"a:1"))
		h.passSettleDelay()
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		updating := call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "b:1"}})
		if *updating.LastUpdateStatus != lambdaapi.LastUpdateStatusInProgress {
			t.Fatalf("update = %s, want InProgress", *updating.LastUpdateStatus)
		}
		if !kept(t, blobs, ref) {
			t.Fatal("the replaced layer's archive is gone while the update in progress serves it")
		}
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if n := blobEntries(sections[0].Blobs, ref); n != 1 {
			t.Errorf("the snapshot lists the replaced layer's archive %d times, want once, for the update in progress", n)
		}
		h.passSettleDelay()
		if kept(t, blobs, ref) {
			t.Error("the replaced layer's archive outlives the update that served it")
		}
	})
}

// blobEntries counts ref among a section's blob entries.
func blobEntries(entries []string, ref blob.Ref) int {
	n := 0
	for _, e := range entries {
		if e == ref.String() {
			n++
		}
	}
	return n
}

// A snapshot lists a layer's archive once for the layer store and once per
// function version listing it, and a function's layers survive the round
// trip (lambda-destinations-layers.md 11.5.1).
func TestFunctionLayersSurviveASnapshot(t *testing.T) {
	runBlobs(t, lambda.Options{}, func(h *harness, _ *blob.Store) {
		t := h.T
		archive := uniqueLayer(t, "snap")
		h.publish("a", archive)
		in := withLayers(t, "fn", layerPrefix+"a:1")
		in.Publish = true
		call(h, lambdaapi.OpCreateFunction, in)
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if n := blobEntries(sections[0].Blobs, archiveRef(t, archive)); n != 3 {
			t.Errorf("the snapshot lists the archive %d times, want 3: the layer version, $LATEST and version 1", n)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		want := layerPrefix + "a:1 " + itoa(int64(len(archive)))
		for _, q := range []*string{nil, new("1")} {
			got := layerRefs(call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "fn", Qualifier: q}))
			if len(got) != 1 || got[0] != want {
				t.Errorf("after the import, qualifier %v Layers = %v, want [%s]", q, got, want)
			}
		}
	})
}

// A refused call holds nothing: after creations and updates refused for
// every reason that comes once the layers are resolved, the layer
// version's delete frees its archive.
func TestRefusedCallsReleaseTheLayersTheyResolved(t *testing.T) {
	runBlobs(t, lambda.Options{}, func(h *harness, blobs *blob.Store) {
		t := h.T
		archive := uniqueLayer(t, "refused")
		ref := archiveRef(t, archive)
		h.publish("a", archive)
		h.publish("big", zeroZip(t, "data/big.bin", 262_144_000))
		h.createFunction("fn")
		h.settle()
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"a:1")),
			"ResourceConflictException", "Function already exist: fn")
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "other", layerPrefix+"a:1", layerPrefix+"missing:1")),
			"InvalidParameterValueException", "Layer version "+layerPrefix+"missing:1 does not exist.")
		if err := fail(h, lambdaapi.OpCreateFunction, withLayers(t, "other", layerPrefix+"a:1", layerPrefix+"big:1")); err.Message != layersTooLarge {
			t.Errorf("CreateFunction over the limit = %q", err.Message)
		}
		stale := &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "a:1"}, RevisionId: new("stale")}
		if err := fail(h, lambdaapi.OpUpdateFunctionConfiguration, stale); err.ID.Shape.Name() != "PreconditionFailedException" {
			t.Errorf("UpdateFunctionConfiguration with a stale RevisionId = %s %q", err.ID.Shape.Name(), err.Message)
		}
		if err := fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "a:1", layerPrefix + "big:1"}}); err.Message != layersTooLarge {
			t.Errorf("UpdateFunctionConfiguration over the limit = %q", err.Message)
		}
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		if kept(t, blobs, ref) {
			t.Error("a refused call kept a reference on the archive")
		}
	})
}

// Every entry's region is checked before any is looked up, and a version
// number past what any layer can reach names no version understudy holds
// (lambda-destinations-layers.md 7.1; the check order is unrecorded).
func TestLayerRegionComesBeforeLookup(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"missing:1", "arn:aws:lambda:eu-west-1:000000000000:layer:x:1")),
			"InvalidParameterValueException", "Layers are not in the same region as the function. Layers are expected to be in region us-east-1.")
		huge := layerPrefix + "missing:99999999999999999999"
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", huge)), "InvalidParameterValueException", "Layer version "+huge+" does not exist.")
	})
}

// An image function takes no layers, with AWS's text on CreateFunction
// (aws-recorded, lambda-destinations-layers.md 7.1: LDL-13 03) and the
// same on UpdateFunctionConfiguration (inferred).
func TestAnImageFunctionTakesNoLayers(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		const text = "Lambda layers are not supported for functions created with container images."
		h.publish("a", pythonLayer(t))
		in := imageFunction("img", "local/fn:latest")
		in.Layers = []string{layerPrefix + "a:1"}
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, in), "InvalidParameterValueException", text)
		call(h, lambdaapi.OpCreateFunction, imageFunction("img", "local/fn:latest"))
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "img", Layers: []string{layerPrefix + "a:1"}}),
			"InvalidParameterValueException", text)
	})
}

// A dry run of UpdateFunctionCode checks the package with the function's
// layers against the unzipped limit, as the update itself does
// (lambda-destinations-layers.md 11.5.4).
func TestDryRunCodeCountsTheLayers(t *testing.T) {
	half := zeroZip(t, "data/half.bin", 131_072_000)
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("big", half)
		call(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", layerPrefix+"big:1"))
		h.settle()
		err := fail(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ZipFile: zeroZip(t, "bootstrap", 131_072_001), DryRun: true})
		if err.Message != layersTooLarge {
			t.Errorf("dry run over the limit = %s %q", err.ID.Shape.Name(), err.Message)
		}
	})
}

// UpdateFunctionConfiguration attaching another account's layer whose
// policy grants the caller nothing journals the advisory too, and a
// refused call journals none (lambda-destinations-layers.md 7.4, switch
// lambda.layers.cross-account-policy).
func TestUpdateWithAnotherAccountsLayerAdvises(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		const theirs = "arn:aws:lambda:us-east-1:111111111111:layer:private:1"
		h.asAccount("111111111111", func() { h.publish("private", pythonLayer(t)) })
		h.createFunction("fn")
		h.settle()
		fail(h, lambdaapi.OpCreateFunction, withLayers(t, "fn", theirs))
		if got := policyWouldDeny(h, theirs); len(got) != 0 {
			t.Fatalf("a refused CreateFunction journaled %+v", got)
		}
		call(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{theirs}})
		if got := policyWouldDeny(h, theirs); len(got) != 1 {
			t.Errorf("policy-would-deny advisories after the update = %+v, want one", got)
		}
	})
}
