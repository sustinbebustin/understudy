package lambda_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// The test account's layer ARNs.
const layerPrefix = "arn:aws:lambda:us-east-1:000000000000:layer:"

// layerArchive is a zip archive holding files, by name.
func layerArchive(tb testing.TB, files map[string]string) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		f, err := w.Create(name)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := f.Write([]byte(files[name])); err != nil {
			tb.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// pythonLayer is a layer archive with a Python module in python/.
func pythonLayer(tb testing.TB) []byte {
	tb.Helper()
	return layerArchive(tb, map[string]string{"python/greet.py": "def hello(): return 'hi'\n"})
}

// publish publishes archive as a version of the layer name, adjusted by
// opts.
func (h *harness) publish(name string, archive []byte, opts ...func(*lambdaapi.PublishLayerVersionInput)) *lambdaapi.PublishLayerVersionOutput {
	h.Helper()
	in := &lambdaapi.PublishLayerVersionInput{LayerName: name, Content: lambdaapi.LayerVersionContentInput{ZipFile: archive}}
	for _, opt := range opts {
		opt(in)
	}
	return call(h, lambdaapi.OpPublishLayerVersion, in)
}

// codeSha256 is the base64 SHA-256 of b, Lambda's CodeSha256.
func codeSha256(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// createdDate is the emulator clock's now as CreatedDate writes it:
// milliseconds and +0000 (lambda-destinations-layers.md 6.3, from the
// Smithy model's PublishLayerVersion example).
func (h *harness) createdDate() string {
	return h.Clock.Now().Wire().UTC().Format("2006-01-02T15:04:05.000-0700")
}

// locationOf is where understudy serves version of the layer name, whose
// archive is archive: a link beside function code's on the addressed host
// (lambda-destinations-layers.md 11.5.3), the digest unpadded base64url.
func (h *harness) locationOf(name string, version int64, archive []byte) string {
	sum := sha256.Sum256(archive)
	return h.Endpoint.Scheme + "://" + h.Endpoint.Host + "/_understudy/lambda-code/layer/us-east-1/000000000000/" +
		url.PathEscape(name) + "/" + strconv.FormatInt(version, 10) + "/" + base64.RawURLEncoding.EncodeToString(sum[:])
}

func runtimes(r ...lambdaapi.Runtime) []lambdaapi.Runtime { return r }

func archs(a ...lambdaapi.Architecture) []lambdaapi.Architecture { return a }

// PublishLayerVersion answers the new version: number 1 for a new layer,
// the layer and version ARNs, the description, license and compatible
// lists as sent, CreatedDate with milliseconds and +0000, and Content with
// the archive's base64 SHA-256 and size (lambda-destinations-layers.md
// 6.3: the Smithy model's example; test_lambda_api.snapshot.json:L8565-L8587),
// lists in the order sent (L13488-L13491), and never a signing ARN or
// ResolvedS3Object (11.8).
func TestPublishLayerVersionAnswersTheVersion(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		archive := pythonLayer(t)
		out := h.publish("shared", archive, func(in *lambdaapi.PublishLayerVersionInput) {
			in.Description = new("My Python layer")
			in.LicenseInfo = new("MIT")
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython313, lambdaapi.RuntimePython312)
			in.CompatibleArchitectures = archs(lambdaapi.ArchitectureArm64, lambdaapi.ArchitectureX8664)
		})
		if out.Version != 1 || deref(out.LayerArn) != layerPrefix+"shared" || deref(out.LayerVersionArn) != layerPrefix+"shared:1" {
			t.Errorf("version %d, LayerArn %s, LayerVersionArn %s; want 1, %sshared, %sshared:1", out.Version, deref(out.LayerArn), deref(out.LayerVersionArn), layerPrefix, layerPrefix)
		}
		if deref(out.Description) != "My Python layer" || deref(out.LicenseInfo) != "MIT" || deref(out.CreatedDate) != h.createdDate() {
			t.Errorf("Description %q, LicenseInfo %v, CreatedDate %q; want as sent and %s", deref(out.Description), out.LicenseInfo, deref(out.CreatedDate), h.createdDate())
		}
		if !slices.Equal(out.CompatibleRuntimes, runtimes(lambdaapi.RuntimePython313, lambdaapi.RuntimePython312)) ||
			!slices.Equal(out.CompatibleArchitectures, archs(lambdaapi.ArchitectureArm64, lambdaapi.ArchitectureX8664)) {
			t.Errorf("compatible lists %v %v, want them in the order sent", out.CompatibleRuntimes, out.CompatibleArchitectures)
		}
		c := out.Content
		if c == nil || deref(c.CodeSha256) != codeSha256(archive) || c.CodeSize != int64(len(archive)) || deref(c.Location) != h.locationOf("shared", 1, archive) {
			t.Errorf("Content = %+v, want CodeSha256 %s, CodeSize %d and Location %s", c, codeSha256(archive), len(archive), h.locationOf("shared", 1, archive))
		}
		if c != nil && (c.SigningJobArn != nil || c.SigningProfileVersionArn != nil || c.ResolvedS3Object != nil) {
			t.Errorf("Content = %+v, want no signing ARN and no ResolvedS3Object", c)
		}
	})
}

// What is not sent is not echoed: Description is "" (test_lambda_api.snapshot.json:L8284),
// and LicenseInfo and the compatible lists are absent
// (test_lambda_api.snapshot.json:L9201-L9221). Empty lists sent come back
// empty (test_lambda_api.snapshot.json:L8426-L8443, validated 2026-01-12).
func TestPublishLayerVersionEchoesWhatWasSent(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		bare := h.publish("bare", pythonLayer(t))
		if bare.Description == nil || *bare.Description != "" || bare.LicenseInfo != nil || bare.CompatibleRuntimes != nil || bare.CompatibleArchitectures != nil {
			t.Errorf("publish with nothing optional = Description %v, LicenseInfo %v, lists %v %v; want \"\" and nothing else",
				bare.Description, bare.LicenseInfo, bare.CompatibleRuntimes, bare.CompatibleArchitectures)
		}
		empty := h.publish("empty", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = []lambdaapi.Runtime{}
			in.CompatibleArchitectures = []lambdaapi.Architecture{}
		})
		if empty.CompatibleRuntimes == nil || len(empty.CompatibleRuntimes) != 0 || empty.CompatibleArchitectures == nil || len(empty.CompatibleArchitectures) != 0 {
			t.Errorf("publish with empty lists = %v %v, want both empty", empty.CompatibleRuntimes, empty.CompatibleArchitectures)
		}
		got := call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: "empty", VersionNumber: 1})
		if got.CompatibleRuntimes == nil || got.CompatibleArchitectures == nil {
			t.Errorf("GetLayerVersion of the empty lists = %v %v, want both empty", got.CompatibleRuntimes, got.CompatibleArchitectures)
		}
	})
}

// Each publish is a new version, the same content included, and numbers
// are never reused: publishing after every version is deleted gives the
// next one (lambda-destinations-layers.md finding 9, 6.2:
// test_lambda_api.snapshot.json:L8565-L8610, L8865-L8887).
func TestLayerVersionNumbersAreNeverReused(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		archive := pythonLayer(t)
		first, second := h.publish("shared", archive), h.publish("shared", archive)
		if first.Version != 1 || second.Version != 2 || deref(first.Content.CodeSha256) != deref(second.Content.CodeSha256) {
			t.Fatalf("two publishes of one archive = versions %d, %d with %s, %s; want 1 and 2 with one CodeSha256",
				first.Version, second.Version, deref(first.Content.CodeSha256), deref(second.Content.CodeSha256))
		}
		for v := range int64(2) {
			call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: v + 1})
		}
		if third := h.publish("shared", archive); third.Version != 3 || deref(third.LayerVersionArn) != layerPrefix+"shared:3" {
			t.Errorf("publish after deleting every version = %d %s, want 3", third.Version, deref(third.LayerVersionArn))
		}
	})
}

// GetLayerVersion takes the layer's name or its ARN, and GetLayerVersionByArn
// the version ARN; each answers what PublishLayerVersion did
// (lambda-destinations-layers.md 6.2, 6.4: test_lambda_api.snapshot.json:L8715-L8760).
func TestGetLayerVersionAnswersWhatPublishDid(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		published := h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.Description = new("d")
			in.LicenseInfo = new("MIT")
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython312)
		})
		want := lambdaapi.GetLayerVersionResponse(*published)
		for name, got := range map[string]*lambdaapi.GetLayerVersionResponse{
			"by name":  call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: "shared", VersionNumber: 1}),
			"by ARN":   call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: layerPrefix + "shared", VersionNumber: 1}),
			"by vers.": call(h, lambdaapi.OpGetLayerVersionByArn, &lambdaapi.GetLayerVersionByArnInput{Arn: layerPrefix + "shared:1"}),
		} {
			if !jsonEqual(t, got, &want) {
				t.Errorf("GetLayerVersion %s = %s, want %s", name, mustJSON(t, got), mustJSON(t, &want))
			}
		}
	})
}

func mustJSON(tb testing.TB, v any) string {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

func jsonEqual(tb testing.TB, a, b any) bool {
	tb.Helper()
	return mustJSON(tb, a) == mustJSON(tb, b)
}

// A missing layer or version answers 404 ResourceNotFoundException "The
// resource you requested does not exist."; a version below 1, 400
// InvalidParameterValueException "Layer Version Cannot be less than 1", on
// Get and Delete alike; and GetLayerVersionByArn of an unversioned ARN the
// pattern ValidationException (lambda-destinations-layers.md 6.4, 6.8:
// test_lambda_api.snapshot.json:L8320-L8425, validated 2026-01-12).
func TestLayerVersionLookupErrors(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t))
		const notFound = "The resource you requested does not exist."
		for name, err := range map[string]*awserr.Error{
			"a layer that does not exist":   failGet(h, "doesnotexist", 1),
			"a version that does not exist": failGet(h, "shared", 2),
			"by ARN, a missing version":     failByArn(h, layerPrefix+"shared:2"),
		} {
			errorIs(t, err, "ResourceNotFoundException", notFound)
			if err.ID.Status != 404 {
				t.Errorf("%s: status %d, want 404", name, err.ID.Status)
			}
		}
		const below = "Layer Version Cannot be less than 1"
		for name, err := range map[string]*awserr.Error{
			"Get of version 0":          failGet(h, "shared", 0),
			"Get of version -1":         failGet(h, "shared", -1),
			"Delete of version 0":       fail(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: 0}),
			"Add permission, version 0": fail(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{LayerName: "shared", VersionNumber: 0, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: "*"}),
		} {
			errorIs(t, err, "InvalidParameterValueException", below)
			if err.ID.Status != 400 {
				t.Errorf("%s: status %d, want 400", name, err.ID.Status)
			}
		}
		errorIs(t, failByArn(h, layerPrefix+"shared"), "ValidationException",
			"1 validation error detected: Value '"+layerPrefix+"shared' at 'arn' failed to satisfy constraint: Member must satisfy regular expression pattern: "+
				`(arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)|(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)`)
	})
}

func failGet(h *harness, name string, version int64) *awserr.Error {
	h.Helper()
	return fail(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: name, VersionNumber: version})
}

func failByArn(h *harness, arn string) *awserr.Error {
	h.Helper()
	return fail(h, lambdaapi.OpGetLayerVersionByArn, &lambdaapi.GetLayerVersionByArnInput{Arn: arn})
}

// DeleteLayerVersion answers 204 also for a layer or version that never
// existed and when repeated (lambda-destinations-layers.md 6.6:
// test_lambda_api.snapshot.json:L8390-L8413); a deleted version can no
// longer be viewed (6.6, the Smithy model's DeleteLayerVersion).
func TestDeleteLayerVersionIsIdempotent(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t))
		for _, in := range []*lambdaapi.DeleteLayerVersionInput{
			{LayerName: "doesnotexist", VersionNumber: 1},
			{LayerName: "shared", VersionNumber: 9},
			{LayerName: "shared", VersionNumber: 1},
			{LayerName: "shared", VersionNumber: 1},
		} {
			call(h, lambdaapi.OpDeleteLayerVersion, in)
		}
		errorIs(t, failGet(h, "shared", 1), "ResourceNotFoundException", "The resource you requested does not exist.")
	})
}

// listItem is the list rendering of a published version: the version's
// fields without LayerArn or Content (lambda-destinations-layers.md 6.5:
// test_lambda_api.snapshot.json:L8762-L8789).
func listItem(p *lambdaapi.PublishLayerVersionOutput) lambdaapi.LayerVersionsListItem {
	return lambdaapi.LayerVersionsListItem{
		LayerVersionArn: p.LayerVersionArn, Version: p.Version, Description: p.Description, CreatedDate: p.CreatedDate,
		CompatibleArchitectures: p.CompatibleArchitectures, CompatibleRuntimes: p.CompatibleRuntimes, LicenseInfo: p.LicenseInfo,
	}
}

// ListLayerVersions lists newest first and leaves deleted versions out
// (lambda-destinations-layers.md 6.5, 9.5: test_lambda_api.snapshot.json:L8761-L8794);
// a layer that never existed, or whose versions are all deleted, answers
// an empty list (L8313-L8319, L8858-L8864).
func TestListLayerVersionsListsNewestFirst(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		published := make([]*lambdaapi.PublishLayerVersionOutput, 0, 3)
		for i := range 3 {
			published = append(published, h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
				in.Description = new("v" + strconv.Itoa(i+1))
				in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython312)
			}))
		}
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: 2})
		out := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared"})
		want := []lambdaapi.LayerVersionsListItem{listItem(published[2]), listItem(published[0])}
		if !jsonEqual(t, out.LayerVersions, want) {
			t.Errorf("ListLayerVersions = %s\nwant %s", mustJSON(t, out.LayerVersions), mustJSON(t, want))
		}
		for _, name := range []string{"doesnotexist"} {
			if got := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: name}); got.LayerVersions == nil || len(got.LayerVersions) != 0 {
				t.Errorf("ListLayerVersions of %s = %+v, want an empty list", name, got)
			}
		}
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: 1})
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: 3})
		if got := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared"}); got.LayerVersions == nil || len(got.LayerVersions) != 0 {
			t.Errorf("ListLayerVersions with every version deleted = %+v, want an empty list", got)
		}
	})
}

// A runtime or architecture filter lists only versions whose compatible
// list holds it (lambda-destinations-layers.md 6.5, from
// https://docs.aws.amazon.com/lambda/latest/api/API_ListLayerVersions.html),
// and MaxItems pages the newest-first list with NextMarker.
func TestListLayerVersionsFiltersAndPages(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython312)
			in.CompatibleArchitectures = archs(lambdaapi.ArchitectureX8664)
		})
		h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimeNodejs22x)
			in.CompatibleArchitectures = archs(lambdaapi.ArchitectureArm64)
		})
		h.publish("shared", pythonLayer(t))
		versions := func(in *lambdaapi.ListLayerVersionsInput) []int64 {
			out := call(h, lambdaapi.OpListLayerVersions, in)
			got := make([]int64, 0, len(out.LayerVersions))
			for _, v := range out.LayerVersions {
				got = append(got, v.Version)
			}
			return got
		}
		if got := versions(&lambdaapi.ListLayerVersionsInput{LayerName: "shared", CompatibleRuntime: new(lambdaapi.RuntimePython312)}); !slices.Equal(got, []int64{1}) {
			t.Errorf("python3.12 versions = %v, want [1]", got)
		}
		if got := versions(&lambdaapi.ListLayerVersionsInput{LayerName: "shared", CompatibleArchitecture: new(lambdaapi.ArchitectureArm64)}); !slices.Equal(got, []int64{2}) {
			t.Errorf("arm64 versions = %v, want [2]", got)
		}
		var paged []int64
		var marker *string
		for range 4 {
			out := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared", MaxItems: new(int32(1)), Marker: marker})
			if len(out.LayerVersions) > 1 {
				t.Fatalf("a page of MaxItems 1 holds %d versions", len(out.LayerVersions))
			}
			for _, v := range out.LayerVersions {
				paged = append(paged, v.Version)
			}
			if marker = out.NextMarker; marker == nil {
				break
			}
		}
		if !slices.Equal(paged, []int64{3, 2, 1}) {
			t.Errorf("paged versions = %v, want [3 2 1]", paged)
		}
	})
}

// ListLayers shows each layer with its latest version; with a filter, the
// latest version compatible with it, and only layers that have one
// (lambda-destinations-layers.md 6.5, from
// https://docs.aws.amazon.com/lambda/latest/api/API_ListLayers.html and the
// Smithy model's example).
func TestListLayersShowsTheLatestMatchingVersion(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("py", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython312)
		})
		pyLatest := h.publish("py", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimePython313)
		})
		node := h.publish("node", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes(lambdaapi.RuntimeNodejs22x)
		})
		latest := func(in *lambdaapi.ListLayersInput) map[string]string {
			out := call(h, lambdaapi.OpListLayers, in)
			got := map[string]string{}
			for _, l := range out.Layers {
				if deref(l.LayerArn) != layerPrefix+deref(l.LayerName) || l.LatestMatchingVersion == nil {
					t.Errorf("layer %+v, want its ARN and a latest matching version", l)
					continue
				}
				got[deref(l.LayerName)] = deref(l.LatestMatchingVersion.LayerVersionArn)
			}
			return got
		}
		if got, want := latest(&lambdaapi.ListLayersInput{}), map[string]string{"py": deref(pyLatest.LayerVersionArn), "node": deref(node.LayerVersionArn)}; !maps.Equal(got, want) {
			t.Errorf("ListLayers = %v, want %v", got, want)
		}
		if got, want := latest(&lambdaapi.ListLayersInput{CompatibleRuntime: new(lambdaapi.RuntimePython312)}), map[string]string{"py": layerPrefix + "py:1"}; !maps.Equal(got, want) {
			t.Errorf("ListLayers for python3.12 = %v, want %v", got, want)
		}
		out := call(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{CompatibleRuntime: new(lambdaapi.RuntimeNodejs22x)})
		if len(out.Layers) != 1 || !jsonEqual(t, out.Layers[0].LatestMatchingVersion, listItem(node)) {
			t.Errorf("ListLayers for nodejs22.x = %s, want node with %s", mustJSON(t, out.Layers), mustJSON(t, listItem(node)))
		}
	})
}

// A filter value outside the enum is refused with AWS's recorded
// ValidationException texts (lambda-destinations-layers.md 6.5:
// test_lambda_api.snapshot.json:L8293-L8312). The runtime list is a
// 56-value internal one, so its prefix and suffix are checked here.
func TestListFiltersRefuseValuesOutsideTheEnum(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		for name, err := range map[string]*awserr.Error{
			"ListLayers":        fail(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{CompatibleRuntime: new(lambdaapi.Runtime("runtimedoesnotexist"))}),
			"ListLayerVersions": fail(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared", CompatibleRuntime: new(lambdaapi.Runtime("runtimedoesnotexist"))}),
		} {
			const prefix = "1 validation error detected: Value 'runtimedoesnotexist' at 'compatibleRuntime' failed to satisfy constraint: Member must satisfy enum value set: ["
			if err.ID.Shape.Name() != "ValidationException" || !strings.HasPrefix(err.Message, prefix) || !strings.HasSuffix(err.Message, "]") ||
				!strings.Contains(err.Message, "python2.7-greengrass") {
				t.Errorf("%s with a bad runtime = %s %q, want ValidationException starting %q", name, err.ID.Shape.Name(), err.Message, prefix)
			}
		}
		errorIs(t, fail(h, lambdaapi.OpListLayers, &lambdaapi.ListLayersInput{CompatibleArchitecture: new(lambdaapi.Architecture("archdoesnotexist"))}),
			"ValidationException", "1 validation error detected: Value 'archdoesnotexist' at 'compatibleArchitecture' failed to satisfy constraint: Member must satisfy enum value set: [x86_64, arm64]")
	})
}

// PublishLayerVersion refuses runtimes and architectures outside the enum
// with one ValidationException naming both members, AWS's 25-value
// runtime list verbatim (lambda-destinations-layers.md 6.3:
// test_lambda_api.snapshot.json:L8444-L8463, validated 2026-01-12).
func TestPublishLayerVersionRefusesValuesOutsideTheEnum(t *testing.T) {
	const runtimeList = "[nodejs20.x, python3.14, provided.al2023, python3.12, python3.13, nodejs24.x, nodejs22.x, java25, python3.10, python3.11, java21, ruby3.3, ruby3.4, ruby3.2, python3.8, python3.9, java17, nodejs16.x, dotnet10, dotnet8, java11, dotnet6, nodejs18.x, provided.al2, java8.al2]"
	const archConstraint = "Member must satisfy constraint: [Member must satisfy enum value set: [x86_64, arm64], Member must not be null]"
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		publishFails := func(rs []lambdaapi.Runtime, as []lambdaapi.Architecture) *awserr.Error {
			return fail(h, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{
				LayerName: "shared", Content: lambdaapi.LayerVersionContentInput{ZipFile: pythonLayer(t)}, CompatibleRuntimes: rs, CompatibleArchitectures: as,
			})
		}
		errorIs(t, publishFails(runtimes("invalidruntime"), archs("invalidarch")), "ValidationException",
			"2 validation errors detected: Value '[invalidruntime]' at 'compatibleRuntimes' failed to satisfy constraint: Member must satisfy enum value set: "+runtimeList+
				"; Value '[invalidarch]' at 'compatibleArchitectures' failed to satisfy constraint: "+archConstraint)
		errorIs(t, publishFails(runtimes("invalidruntime", "invalidruntime2", lambdaapi.RuntimeNodejs20x), archs("invalidarch", lambdaapi.ArchitectureX8664)), "ValidationException",
			"2 validation errors detected: Value '[invalidruntime, invalidruntime2, nodejs20.x]' at 'compatibleRuntimes' failed to satisfy constraint: Member must satisfy enum value set: "+runtimeList+
				"; Value '[invalidarch, x86_64]' at 'compatibleArchitectures' failed to satisfy constraint: "+archConstraint)
		if got := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared"}); len(got.LayerVersions) != 0 {
			t.Errorf("a refused publish stored %+v", got.LayerVersions)
		}
	})
}

// Deprecated runtimes are accepted as compatibility metadata
// (lambda-destinations-layers.md finding 11: test_lambda_api.snapshot.json:L13526-L13560).
func TestPublishLayerVersionTakesDeprecatedRuntimes(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		out := h.publish("legacy", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) {
			in.CompatibleRuntimes = runtimes("go1.x", "java8", "provided")
		})
		if !slices.Equal(out.CompatibleRuntimes, runtimes("go1.x", "java8", "provided")) {
			t.Errorf("CompatibleRuntimes = %v, want the deprecated runtimes as sent", out.CompatibleRuntimes)
		}
	})
}

// statement is the JSON of an AddLayerVersionPermission statement, keys in
// AWS's order (lambda-destinations-layers.md 6.7: the Smithy model's
// example; test_lambda_api.snapshot.json:L8927-L8940).
func statement(sid, principal, resource string) string {
	return `{"Sid":"` + sid + `","Effect":"Allow","Principal":` + principal + `,"Action":"lambda:GetLayerVersion","Resource":"` + resource + `"}`
}

// A version's policy collects its statements in order, each Add and Remove
// changing the RevisionId; Add and Remove take the current RevisionId
// (lambda-destinations-layers.md 6.7: test_lambda_api.snapshot.json:L9112-L9198).
func TestLayerVersionPermissionLifecycle(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		version := deref(h.publish("shared", pythonLayer(t)).LayerVersionArn)
		add := func(sid string, revision *string) *lambdaapi.AddLayerVersionPermissionOutput {
			return call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
				LayerName: "shared", VersionNumber: 1, StatementId: sid, Action: "lambda:GetLayerVersion", Principal: "*", RevisionId: revision,
			})
		}
		policy := func() *lambdaapi.GetLayerVersionPolicyOutput {
			return call(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "shared", VersionNumber: 1})
		}
		s1 := add("s1", nil)
		if deref(s1.Statement) != statement("s1", `"*"`, version) || deref(s1.RevisionId) == "" {
			t.Errorf("Add s1 = %s rev %q, want %s and a RevisionId", deref(s1.Statement), deref(s1.RevisionId), statement("s1", `"*"`, version))
		}
		p1 := policy()
		if want := `{"Version":"2012-10-17","Id":"default","Statement":[` + statement("s1", `"*"`, version) + `]}`; deref(p1.Policy) != want || deref(p1.RevisionId) != deref(s1.RevisionId) {
			t.Errorf("policy = %s rev %s, want %s rev %s", deref(p1.Policy), deref(p1.RevisionId), want, deref(s1.RevisionId))
		}
		s2 := add("s2", p1.RevisionId)
		p2 := policy()
		if want := `{"Version":"2012-10-17","Id":"default","Statement":[` + statement("s1", `"*"`, version) + "," + statement("s2", `"*"`, version) + `]}`; deref(p2.Policy) != want {
			t.Errorf("policy after s2 = %s, want %s", deref(p2.Policy), want)
		}
		if deref(s2.RevisionId) == deref(s1.RevisionId) || deref(p2.RevisionId) != deref(s2.RevisionId) {
			t.Errorf("RevisionIds %s then %s (policy %s), want a new one on Add", deref(s1.RevisionId), deref(s2.RevisionId), deref(p2.RevisionId))
		}
		call(h, lambdaapi.OpRemoveLayerVersionPermission, &lambdaapi.RemoveLayerVersionPermissionInput{LayerName: "shared", VersionNumber: 1, StatementId: "s2", RevisionId: p2.RevisionId})
		p3 := policy()
		if want := `{"Version":"2012-10-17","Id":"default","Statement":[` + statement("s1", `"*"`, version) + `]}`; deref(p3.Policy) != want || deref(p3.RevisionId) == deref(p2.RevisionId) {
			t.Errorf("policy after removing s2 = %s rev %s, want %s and a new RevisionId", deref(p3.Policy), deref(p3.RevisionId), want)
		}
	})
}

// An account principal renders as {"AWS":"arn:aws:iam::<account>:root"},
// a root ARN principal the same, and an organization adds a
// PrincipalOrgID condition (lambda-destinations-layers.md 6.7, 9.4: the
// Smithy model's example; https://docs.aws.amazon.com/lambda/latest/dg/permissions-layer-cross-account.html).
func TestAddLayerVersionPermissionRendersPrincipals(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		version := deref(h.publish("shared", pythonLayer(t)).LayerVersionArn)
		for _, c := range []struct {
			sid, principal string
			org            *string
			want           string
		}{
			{"account", "223456789012", nil, statement("account", `{"AWS":"arn:aws:iam::223456789012:root"}`, version)},
			{"root", "arn:aws:iam::223456789012:root", nil, statement("root", `{"AWS":"arn:aws:iam::223456789012:root"}`, version)},
			{"org", "*", new("o-0123456789"), `{"Sid":"org","Effect":"Allow","Principal":"*","Action":"lambda:GetLayerVersion","Resource":"` + version +
				`","Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-0123456789"}}}`},
		} {
			out := call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
				LayerName: "shared", VersionNumber: 1, StatementId: c.sid, Action: "lambda:GetLayerVersion", Principal: c.principal, OrganizationId: c.org,
			})
			if deref(out.Statement) != c.want {
				t.Errorf("Add %s = %s\nwant %s", c.sid, deref(out.Statement), c.want)
			}
		}
	})
}

// An account principal with an organization id is refused (aws-recorded,
// lambda-destinations-layers.md 6.7: testdata/parity/lambda/LDL-15/04-org-account.json).
func TestLayerPermissionOrganizationNeedsEveryone(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		h.publish("shared", pythonLayer(h.T))
		errorIs(h.T, fail(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
			LayerName: "shared", VersionNumber: 1, StatementId: "orgacct", Action: "lambda:GetLayerVersion", Principal: "223456789012", OrganizationId: new("o-abcdefghij"),
		}), "InvalidParameterValueException", "If organization id is specified then principal should be *")
	})
}

// The permission operations' recorded refusals
// (lambda-destinations-layers.md 6.8: test_lambda_api.snapshot.json:L8915-L9070).
func TestLayerVersionPermissionErrors(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("shared", pythonLayer(t))
		addIn := func(name string, version int64, sid, action string, revision *string) *lambdaapi.AddLayerVersionPermissionInput {
			return &lambdaapi.AddLayerVersionPermissionInput{LayerName: name, VersionNumber: version, StatementId: sid, Action: action, Principal: "*", RevisionId: revision}
		}
		getPolicy := func(name string, version int64) *awserr.Error {
			return fail(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: name, VersionNumber: version})
		}
		remove := func(name string, version int64, sid string, revision *string) *awserr.Error {
			return fail(h, lambdaapi.OpRemoveLayerVersionPermission, &lambdaapi.RemoveLayerVersionPermissionInput{LayerName: name, VersionNumber: version, StatementId: sid, RevisionId: revision})
		}
		errorIs(t, getPolicy("shared", 1), "ResourceNotFoundException", "No policy is associated with the given resource.")
		errorIs(t, fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 1, "s0", "lambda:GetLayerVersion", new("any"))), "PreconditionFailedException",
			"The Revision Id provided does not match the latest Revision Id. Call the GetLayerPolicy API to retrieve the latest Revision Id")
		call(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 1, "s1", "lambda:GetLayerVersion", nil))
		errorIs(t, fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 1, "s-x", "*", nil)), "ValidationException",
			"1 validation error detected: Value '*' at 'action' failed to satisfy constraint: Member must satisfy regular expression pattern: lambda:GetLayerVersion")
		conflict := fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 1, "s1", "lambda:GetLayerVersion", nil))
		errorIs(t, conflict, "ResourceConflictException", "The statement id (s1) provided already exists. Please provide a new statement id, or remove the existing statement.")
		if conflict.ID.Status != 409 {
			t.Errorf("duplicate statement status %d, want 409", conflict.ID.Status)
		}
		const revision = "The Revision Id provided does not match the latest Revision Id. Call the GetLayerPolicy API to retrieve the latest Revision Id"
		wrong := fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 1, "s2", "lambda:GetLayerVersion", new("wrong")))
		errorIs(t, wrong, "PreconditionFailedException", revision)
		if wrong.ID.Status != 412 {
			t.Errorf("wrong revision status %d, want 412", wrong.ID.Status)
		}
		missingLayer := "Layer version " + layerPrefix + "shared-doesnotexist:1 does not exist."
		missingVersion := "Layer version " + layerPrefix + "shared:2 does not exist."
		for name, c := range map[string]struct {
			err  *awserr.Error
			want string
		}{
			"add, missing layer":        {fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared-doesnotexist", 1, "s3", "lambda:GetLayerVersion", nil)), missingLayer},
			"get, missing layer":        {getPolicy("shared-doesnotexist", 1), missingLayer},
			"remove, missing layer":     {remove("shared-doesnotexist", 1, "s1", nil), missingLayer},
			"add, missing version":      {fail(h, lambdaapi.OpAddLayerVersionPermission, addIn("shared", 2, "s3", "lambda:GetLayerVersion", nil)), missingVersion},
			"get, missing version":      {getPolicy("shared", 2), missingVersion},
			"remove, missing version":   {remove("shared", 2, "s1", nil), missingVersion},
			"remove, missing statement": {remove("shared", 1, "doesnotexist", nil), "Statement doesnotexist is not found in resource policy."},
		} {
			errorIs(t, c.err, "ResourceNotFoundException", c.want)
			if c.err.ID.Status != 404 {
				t.Errorf("%s: status %d, want 404", name, c.err.ID.Status)
			}
		}
		errorIs(t, remove("shared", 1, "s1", new("wrong")), "PreconditionFailedException", revision)
	})
}

// A version's policy is capped at 20480 bytes measured as compact JSON with
// each principal as the caller sent it: 85 statements of an account id
// principal are accepted, though the policy GetLayerVersionPolicy returns
// for them is 21983 bytes, and the 86th is refused at 20693 bytes
// (aws-recorded, testdata/parity/lambda/LDL-15/07-size-limit-at-086.txt,
// testdata/parity/lambda/LDL-15/08-size-policy.json). The Sids, the
// principal and the layer name are the recording's; the test account and
// Region are as long as the recording's, so every ARN is too.
func TestLayerPolicySizeIsMeasuredWithPrincipalsAsSent(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		h.publish("us1103-ldl-pol", pythonLayer(t))
		add := func(n int) *lambdaapi.AddLayerVersionPermissionInput {
			return &lambdaapi.AddLayerVersionPermissionInput{
				LayerName: "us1103-ldl-pol", VersionNumber: 1, StatementId: fmt.Sprintf("fill-%03d-", n) + strings.Repeat("x", 60),
				Action: "lambda:GetLayerVersion", Principal: "123456789012",
			}
		}
		for n := 1; n <= 85; n++ {
			call(h, lambdaapi.OpAddLayerVersionPermission, add(n))
		}
		policy := call(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "us1103-ldl-pol", VersionNumber: 1})
		if got := len(deref(policy.Policy)); got != 21983 {
			t.Errorf("policy of 85 statements is %d bytes, want the recorded 21983", got)
		}
		refused := fail(h, lambdaapi.OpAddLayerVersionPermission, add(86))
		errorIs(t, refused, "PolicyLengthExceededException", "The final policy size (20693) is bigger than the limit (20480).")
		if refused.ID.Status != 400 {
			t.Errorf("refused statement status %d, want 400", refused.ID.Status)
		}
		if after := call(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "us1103-ldl-pol", VersionNumber: 1}); deref(after.Policy) != deref(policy.Policy) {
			t.Errorf("the refused statement changed the policy")
		}
	})
}

// The layer store is part of the Lambda snapshot: after an export, a reset
// and an import the versions are back and the numbering continues past
// every version ever published (lambda-destinations-layers.md 11.5.1).
func TestLayersSurviveASnapshot(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		first := h.publish("shared", pythonLayer(t), func(in *lambdaapi.PublishLayerVersionInput) { in.Description = new("kept") })
		h.publish("shared", pythonLayer(t))
		call(h, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "shared", VersionNumber: 2})
		call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{LayerName: "shared", VersionNumber: 1, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: "*"})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := call(h, lambdaapi.OpListLayerVersions, &lambdaapi.ListLayerVersionsInput{LayerName: "shared"}); len(got.LayerVersions) != 0 {
			t.Fatalf("after the reset ListLayerVersions = %+v, want none", got.LayerVersions)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		got := call(h, lambdaapi.OpGetLayerVersion, &lambdaapi.GetLayerVersionInput{LayerName: "shared", VersionNumber: 1})
		if want := lambdaapi.GetLayerVersionResponse(*first); !jsonEqual(t, got, &want) {
			t.Errorf("after the import GetLayerVersion = %s, want %s", mustJSON(t, got), mustJSON(t, &want))
		}
		call(h, lambdaapi.OpGetLayerVersionPolicy, &lambdaapi.GetLayerVersionPolicyInput{LayerName: "shared", VersionNumber: 1})
		if next := h.publish("shared", pythonLayer(t)); next.Version != 3 {
			t.Errorf("publish after the import = version %d, want 3", next.Version)
		}
	})
}

// ResourcePolicy answers a layer version ARN with the policy
// GetLayerVersionPolicy returns, so an authorizer can read who may use the
// version (lambda-destinations-layers.md 7.4): none before a statement, the
// statements after, and nothing to read for a version that does not exist.
func TestResourcePolicyReadsALayerVersionsPermissions(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		t := h.T
		version, err := aws.ParseARN(deref(h.publish("shared", pythonLayer(t)).LayerVersionArn))
		if err != nil {
			t.Fatal(err)
		}
		if doc, ok := h.svc.ResourcePolicy(version); !ok || doc != nil {
			t.Fatalf("ResourcePolicy of a version without statements = %+v, %t; want no policy for an existing version", doc, ok)
		}
		call(h, lambdaapi.OpAddLayerVersionPermission, &lambdaapi.AddLayerVersionPermissionInput{
			LayerName: "shared", VersionNumber: 1, StatementId: "s1", Action: "lambda:GetLayerVersion", Principal: "111111111111",
		})
		if doc, ok := h.svc.ResourcePolicy(version); !ok || doc == nil || len(doc.Statements) != 1 || doc.Statements[0].Sid != "s1" {
			t.Errorf("ResourcePolicy after AddLayerVersionPermission = %+v, %t; want the one statement", doc, ok)
		}
		gone := version
		gone.Resource = "layer:shared:2"
		if doc, ok := h.svc.ResourcePolicy(gone); ok || doc != nil {
			t.Errorf("ResourcePolicy of a missing version = %+v, %t; want none", doc, ok)
		}
	})
}

// A function's layers fill /opt: the launcher gets the merged tree of the
// function's layers, in order, as an absolute directory holding every
// layer's files; functions naming the same layers in the same order share
// one tree; a function without layers leaves the image's /opt alone
// (lambda-destinations-layers.md 8.1, 11.5.5).
func TestInvokeGivesTheLauncherTheMergedLayers(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		t := h.T
		h.publish("a", layerArchive(t, map[string]string{"python/a.py": "A = 1\n"}))
		h.publish("b", layerArchive(t, map[string]string{"python/b.py": "B = 2\n", "bin/tool": "#!/bin/sh\n"}))
		for _, name := range []string{"fn", "twin"} {
			in := zipFunction(t, name)
			in.Layers = []string{layerPrefix + "a:1", layerPrefix + "b:1"}
			call(h.harness, lambdaapi.OpCreateFunction, in)
		}
		h.createFunction("plain")
		for _, name := range []string{"fn", "twin", "plain"} {
			h.invoke(invokeInput(name, `{}`))
		}
		opts := map[string]string{}
		for _, spec := range h.started() {
			code, ok := spec.Code.(launcher.ZipCode)
			if !ok {
				t.Fatalf("%s started with %T, want zip code", spec.Function.Name, spec.Code)
			}
			opts[spec.Function.Name] = code.Opt
		}
		opt := opts["fn"]
		if !filepath.IsAbs(opt) {
			t.Fatalf("fn's layer directory = %q, want an absolute path", opt)
		}
		root, err := os.OpenRoot(opt)
		if err != nil {
			t.Fatalf("opening fn's layer directory: %v", err)
		}
		defer root.Close()
		for name, want := range map[string]string{"python/a.py": "A = 1\n", "python/b.py": "B = 2\n", "bin/tool": "#!/bin/sh\n"} {
			if got, err := fs.ReadFile(root.FS(), name); err != nil || string(got) != want {
				t.Errorf("%s in fn's layer directory = %q, %v; want %q", name, got, err, want)
			}
		}
		if opts["twin"] != opt {
			t.Errorf("twin's layer directory = %q, want fn's %q: the same layers in the same order", opts["twin"], opt)
		}
		if opts["plain"] != "" {
			t.Errorf("plain's layer directory = %q, want none", opts["plain"])
		}
	})
}
