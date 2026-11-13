package lambda_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/store"
)

// What was not sent stays apart from what was sent empty across a
// snapshot: no lists and no LicenseInfo for the first, empty lists and ""
// for the second (lambda-destinations-layers.md 6.3: test_lambda_api.snapshot.json:L8426-L8443,
// L9201-L9221; 11.7).
func TestLayerVersionKeepsWhatWasSentEmptyAcrossASnapshot(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("bare", pythonLayer(t))
		h.publish("empty", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = []lambdaapi.Runtime{}
			in.CompatibleArchitectures = []lambdaapi.Architecture{}
			in.LicenseInfo = new("")
		})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		bare := call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: "bare", VersionNumber: 1})
		if bare.CompatibleRuntimes != nil || bare.CompatibleArchitectures != nil || bare.LicenseInfo != nil {
			t.Errorf("bare after the import = lists %v %v, LicenseInfo %v; want none", bare.CompatibleRuntimes, bare.CompatibleArchitectures, bare.LicenseInfo)
		}
		empty := call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: "empty", VersionNumber: 1})
		if empty.CompatibleRuntimes == nil || empty.CompatibleArchitectures == nil || empty.LicenseInfo == nil || *empty.LicenseInfo != "" {
			t.Errorf("empty after the import = lists %v %v, LicenseInfo %v; want both empty and \"\"", empty.CompatibleRuntimes, empty.CompatibleArchitectures, empty.LicenseInfo)
		}
	})
}

// A LayerName that passes the model's pattern, whose two alternatives are
// unanchored, without being a name or an unversioned layer ARN is refused
// by PublishLayerVersion, which stores nothing; what AWS answers is
// unrecorded (lambda-destinations-layers.md 6.2).
func TestPublishLayerVersionRefusesANameNoLayerCanHave(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		for _, name := range []string{"my layer", layerPrefix + "shared:1"} {
			err := fail(h, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{LayerName: name, Content: lambdaapi.LayerVersionContentInput{ZipFile: pythonLayer(t)}})
			if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, "The layer name "+name) {
				t.Errorf("publish under %q = %s %q, want InvalidParameterValueException naming it", name, err.ID.Shape.Name(), err.Message)
			}
		}
		if out := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{}); len(out.Layers) != 0 {
			t.Errorf("refused publishes stored %+v", out.Layers)
		}
	})
}

// Content names exactly one archive: inline, or an S3 bucket and key.
func TestPublishLayerVersionNeedsOneArchive(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		for name, content := range map[string]lambdaapi.LayerVersionContentInput{
			"none":      {},
			"both":      {ZipFile: pythonLayer(t), S3Bucket: new("bucket"), S3Key: new("key")},
			"no S3 key": {S3Bucket: new("bucket")},
		} {
			err := fail(h, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{LayerName: "shared", Content: content})
			if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, "Content.ZipFile, or Content.S3Bucket and Content.S3Key") {
				t.Errorf("Content %s = %s %q, want InvalidParameterValueException naming the members", name, err.ID.Shape.Name(), err.Message)
			}
		}
	})
}

// An archive that is not a zip is refused with function code's text
// (lambda-destinations-layers.md 11.5.2; the layer-side text is
// unrecorded, LDL-14).
func TestPublishLayerVersionRefusesWhatIsNotAZip(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		errorIs(h.T, fail(h, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{LayerName: "shared", Content: lambdaapi.LayerVersionContentInput{ZipFile: []byte("not a zip")}}),
			"InvalidParameterValueException", "Could not unzip uploaded file. Please check your file, then try to upload again.")
	})
}

// A bad architecture alone is the one rolled-up violation of the recorded
// pair (lambda-destinations-layers.md 6.3: test_lambda_api.snapshot.json:L8444-L8463).
func TestPublishLayerVersionRefusesABadArchitectureAlone(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		errorIs(h.T, fail(h, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{
			LayerName: "shared", Content: lambdaapi.LayerVersionContentInput{ZipFile: pythonLayer(h.T)},
			CompatibleRuntimes: runtimes("go1.x"), CompatibleArchitectures: archs("invalidarch"),
		}), "ValidationException", "1 validation error detected: Value '[invalidarch]' at 'compatibleArchitectures' failed to satisfy constraint: "+
			"Member must satisfy constraint: [Member must satisfy enum value set: [x86_64, arm64], Member must not be null]")
	})
}

// understudy holds no AWS-managed layer, so GetLayerVersionByArn of an
// awslayer ARN finds nothing (lambda-destinations-layers.md 8.4, 11.8).
func TestGetLayerVersionByArnOfAnAWSManagedLayerFindsNothing(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		errorIs(h.T, failByArn(h, "arn:aws:lambda:::awslayer:AWSLambda-Python-AWS-SDK"), "ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// ListLayers pages by layer name and answers an empty list when there is
// none (aws-recorded, testdata/parity/lambda/LDL-11/01-list-before.json;
// lambda-destinations-layers.md 6.5).
func TestListLayersPagesByName(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		if out := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{}); out.Layers == nil || len(out.Layers) != 0 {
			t.Errorf("ListLayers with no layer = %+v, want an empty list", out)
		}
		for _, name := range []string{"c", "a", "b", "gone"} {
			h.publish(name, pythonLayer(t))
		}
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "gone", VersionNumber: 1})
		var names []string
		var marker *string
		for range 3 {
			out := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{MaxItems: new(int32(2)), Marker: marker})
			for _, l := range out.Layers {
				names = append(names, deref(l.LayerName))
			}
			if marker = out.NextMarker; marker == nil {
				break
			}
		}
		if !slices.Equal(names, []string{"a", "b", "c"}) {
			t.Errorf("paged layers = %v, want [a b c]", names)
		}
	})
}

// ListLayers leaves out a layer whose only version was deleted and lists
// the other (aws-recorded, testdata/parity/lambda/LDL-11/02-list-after-delete.json).
func TestListLayersLeavesOutALayerWithEveryVersionDeleted(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("us1103-ldl-x", pythonLayer(t))
		h.publish("us1103-ldl-del", pythonLayer(t))
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "us1103-ldl-del", VersionNumber: 1})
		layers := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{}).Layers
		names := make([]string, 0, len(layers))
		for _, l := range layers {
			names = append(names, deref(l.LayerName))
		}
		if !slices.Equal(names, []string{"us1103-ldl-x"}) {
			t.Errorf("listed layers = %v, want [us1103-ldl-x]", names)
		}
	})
}

// The listings' runtime filter is checked against AWS's recorded 56-value
// list, not the model's enum: python2.7-greengrass is taken, ruby4.0 is not
// (lambda-destinations-layers.md 6.5: test_lambda_api.snapshot.json:L8296).
func TestLayerFiltersTakeTheRecordedRuntimeList(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython312)
		})
		if out := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{CompatibleRuntime: new(lambdaapi.Runtime("python2.7-greengrass"))}); len(out.Layers) != 0 {
			t.Errorf("ListLayers for python2.7-greengrass = %+v, want none", out.Layers)
		}
		err := fail(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared", CompatibleRuntime: new(lambdaapi.RuntimeRuby40)})
		if err.ID.Shape.Name() != "ValidationException" || !strings.HasPrefix(err.Message, "1 validation error detected: Value 'ruby4.0' at 'compatibleRuntime'") {
			t.Errorf("ListLayerVersions for ruby4.0 = %s %q, want the enum ValidationException", err.ID.Shape.Name(), err.Message)
		}
	})
}

// The snapshot lists one blob reference per stored layer version, the same
// archive twice included, and a deleted version's is gone; a layer whose
// versions are all deleted stays in the snapshot for its numbering
// (lambda-destinations-layers.md 11.5.1, 11.5.2).
func TestSnapshotHoldsOneReferencePerLayerVersion(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		archive := pythonLayer(t)
		sum := sha256.Sum256(archive)
		digest := hex.EncodeToString(sum[:])
		h.publish("shared", archive)
		h.publish("shared", archive)
		count := func() int {
			sections, err := h.svc.Export()
			if err != nil {
				t.Fatal(err)
			}
			if len(sections) != 1 {
				t.Fatalf("Export wrote %d sections, want the scope's one", len(sections))
			}
			n := 0
			for _, b := range sections[0].Blobs {
				if b == digest {
					n++
				}
			}
			return n
		}
		if got := count(); got != 2 {
			t.Errorf("references after two publishes = %d, want 2", got)
		}
		for v := range int64(2) {
			call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: v + 1})
			if got, want := count(), 1-int(v); got != want {
				t.Errorf("references after deleting version %d = %d, want %d", v+1, got, want)
			}
		}
	})
}

// A Principal that only contains an account id, "*" or a root ARN passes
// the model's unanchored pattern but names no principal, and is refused
// with AddPermission's recorded text (lambda-api.md 7.3; the layer-side
// text is unrecorded).
func TestAddLayerVersionPermissionRefusesWhatOnlyContainsAPrincipal(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t))
		for _, p := range []string{"123456789012x", "**", "x" + "arn:aws:iam::123456789012:root"} {
			errorIs(t, fail(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
				LayerName: "shared", VersionNumber: 1, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: p,
			}), "InvalidParameterValueException", "The provided principal was invalid. Please check the principal and try again.")
		}
	})
}

// Removing the last statement leaves no policy: GetLayerVersionPolicy
// answers that the version does not have any resource policy, naming it
// (aws-recorded, testdata/parity/lambda/LDL-15/06-policy-emptied.json), the
// old RevisionId no longer matches, and ResourcePolicy reads no document
// (lambda-destinations-layers.md 6.7, 6.8).
func TestRemovingTheLastStatementLeavesNoPolicy(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		version, err := aws.ParseARN(deref(h.publish("shared", pythonLayer(t)).LayerVersionArn))
		if err != nil {
			t.Fatal(err)
		}
		added := call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
			LayerName: "shared", VersionNumber: 1, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: "*",
		})
		call(h, lambdaapi.OpRemoveLayerVersionPermission, &lambdaapi.RemoveLayerVersionPermissionInput{LayerName: "shared", VersionNumber: 1, StatementId: "s1"})
		errorIs(t, fail(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "shared", VersionNumber: 1}),
			"ResourceNotFoundException", "Layer version "+version.String()+" does not have any resource policy.")
		errorIs(t, fail(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
			LayerName: "shared", VersionNumber: 1, StatementId: "s2", Action: "lambda:GetLayerVersion", Principal: "*", RevisionId: added.RevisionId,
		}), "PreconditionFailedException", "The Revision Id provided does not match the latest Revision Id. Call the GetLayerPolicy API to retrieve the latest Revision Id")
		if doc, ok := h.svc.ResourcePolicy(version); !ok || doc != nil {
			t.Errorf("ResourcePolicy after the last statement went = %+v, %t; want no document for an existing version", doc, ok)
		}
	})
}

// A version whose policy was emptied still answers so after a snapshot
// round trip (LDL-15 06). A version 5 or 4 section, saved before versions
// recorded having had a policy, loads with every version as never having
// had one, which answers the text of a version that never had a statement.
func TestEmptiedLayerPolicySurvivesASnapshot(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		version := deref(h.publish("shared", pythonLayer(t)).LayerVersionArn)
		call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
			LayerName: "shared", VersionNumber: 1, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: "*",
		})
		call(h, lambdaapi.OpRemoveLayerVersionPermission, &lambdaapi.RemoveLayerVersionPermissionInput{LayerName: "shared", VersionNumber: 1, StatementId: "s1"})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		reload := func(ss []store.Section) *awserr.Error {
			h.svc.Reset(kernel.ResetScope{})
			if err := h.svc.Import(ss); err != nil {
				t.Fatalf("Import: %v", err)
			}
			return fail(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "shared", VersionNumber: 1})
		}
		errorIs(t, reload(sections), "ResourceNotFoundException", "Layer version "+version+" does not have any resource policy.")

		for _, earlier := range []int{5, 4} {
			older := make([]store.Section, len(sections))
			for i, sec := range sections {
				if !bytes.Contains(sec.Data, []byte(`"hadPolicy":true`)) {
					t.Fatalf("section %s does not record the emptied policy: %s", sec.Path, sec.Data)
				}
				sec.Version = earlier
				sec.Data = bytes.ReplaceAll(bytes.ReplaceAll(sec.Data, []byte(`,"hadPolicy":true`), nil), []byte(`"hadPolicy":true,`), nil)
				older[i] = sec
			}
			errorIs(t, reload(older), "ResourceNotFoundException", "No policy is associated with the given resource.")
		}
	})
}
