package lambda_test

import (
	"bytes"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// imageFunction is the CreateFunction input of an image function.
func imageFunction(name, uri string) *lambdaapi.CreateFunctionInput {
	return &lambdaapi.CreateFunctionInput{
		FunctionName: name,
		Role:         role,
		Code:         lambdaapi.FunctionCode{ImageUri: new(uri)},
		PackageType:  new(lambdaapi.PackageTypeImage),
	}
}

// imageLauncher is a launcher over a fake Docker daemon holding the local
// images, each with its own ID.
func imageLauncher(t *testing.T, locals ...string) (*launcher.Launcher, *docker.Client) {
	t.Helper()
	d := dockertest.New(t, dockertest.Options{})
	for i, local := range locals {
		d.AddImage(local, docker.Image{Architecture: "amd64", OS: "linux", ID: "sha256:" + strings.Repeat(strconv.FormatInt(int64(0xa0+i), 16), 32)})
	}
	client := d.Client(t)
	l, err := launcher.New(client, launcher.Options{
		Ownership:    docker.Ownership{Instance: "lambda-test", Owner: "lambda-test"},
		Test:         true,
		Topology:     topology.LinuxBridge{Gateway: netip.MustParseAddr("172.17.0.1")},
		EndpointPort: 4566,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l, client
}

// TestImageFunctionStates: an image function is Pending while its image
// is made present, then Active with the image's digest; one whose pull
// fails is Failed with a message naming the image (ARCHITECTURE.md 14.3).
func TestImageFunctionStates(t *testing.T) {
	images, client := imageLauncher(t, "local/fn:latest")
	run(t, lambda.Options{Images: images}, func(h *harness) {
		defer client.Close()
		created := call(h, lambdaapi.OpCreateFunction, imageFunction("good", "local/fn:latest"))
		if *created.State != lambdaapi.StatePending || *created.PackageType != lambdaapi.PackageTypeImage || created.Runtime != nil {
			t.Fatalf("created = %s %s %v; want a Pending image function without a runtime", *created.State, *created.PackageType, created.Runtime)
		}
		call(h, lambdaapi.OpCreateFunction, imageFunction("bad", "registry.example/missing:1"))
		h.settle()

		good := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "good"})
		if *good.Configuration.State != lambdaapi.StateActive || *good.Code.RepositoryType != "ECR" || *good.Code.ImageUri != "local/fn:latest" {
			t.Errorf("good = %s %+v", *good.Configuration.State, good.Code)
		}
		bad := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "bad"})
		if *bad.State != lambdaapi.StateFailed || *bad.StateReasonCode != lambdaapi.StateReasonCodeImageAccessDenied || !strings.Contains(*bad.StateReason, "registry.example/missing:1") {
			t.Errorf("bad = %s %v %v; want Failed, ImageAccessDenied and a reason naming the image", *bad.State, bad.StateReasonCode, bad.StateReason)
		}
		errorIs(t, fail(h, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "bad", Timeout: new(int32(5))}),
			"ResourceConflictException", "The operation cannot be performed at this time. The function is currently in the following state: Failed")
	})
}

// TestPublishWithImageUpdate: UpdateFunctionCode with a new ImageUri and
// Publish publishes that image as a new version, which waits for the image
// as $LATEST does, never the version before it (lambda-api.md 7.1).
func TestPublishWithImageUpdate(t *testing.T) {
	images, client := imageLauncher(t, "local/fn:1", "local/fn:2")
	run(t, lambda.Options{Images: images}, func(h *harness) {
		defer client.Close()
		call(h, lambdaapi.OpCreateFunction, imageFunction("fn", "local/fn:1"))
		h.settle()
		v1 := call(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})

		out := call(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "fn", ImageUri: new("local/fn:2"), Publish: true})
		if *out.Version != "2" || *out.State != lambdaapi.StatePending || *out.CodeSha256 == *v1.CodeSha256 {
			t.Fatalf("published = version %s %s %s; want version 2, Pending, not version 1's code %s", *out.Version, *out.State, *out.CodeSha256, *v1.CodeSha256)
		}
		h.settle()
		for _, q := range []string{"2", "$LATEST"} {
			got := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: new(q)})
			if *got.Configuration.State != lambdaapi.StateActive || *got.Code.ImageUri != "local/fn:2" || *got.Configuration.CodeSha256 == *v1.CodeSha256 {
				t.Errorf("%s = %s %s %s; want Active on local/fn:2", q, *got.Configuration.State, *got.Code.ImageUri, *got.Configuration.CodeSha256)
			}
		}
		versions := call(h, lambdaapi.OpListVersionsByFunction, &lambdaapi.ListVersionsByFunctionInput{FunctionName: "fn"}).Versions
		if len(versions) != 3 {
			t.Errorf("ListVersionsByFunction = %d versions, want $LATEST, 1 and 2", len(versions))
		}
		v1Now := call(h, lambdaapi.OpGetFunction, &lambdaapi.GetFunctionInput{FunctionName: "fn", Qualifier: new("1")})
		if *v1Now.Code.ImageUri != "local/fn:1" {
			t.Errorf("version 1 = %s, want its own image local/fn:1", *v1Now.Code.ImageUri)
		}
	})
}

// TestImageURIIsAnImageReference: an ImageUri the Docker Engine would not
// accept is refused by the call that sets it, not when the image is first
// pulled.
func TestImageURIIsAnImageReference(t *testing.T) {
	images, client := imageLauncher(t, "local/fn:latest")
	run(t, lambda.Options{Images: images}, func(h *harness) {
		defer client.Close()
		const bad = "local/../../containers/fn"
		err := fail(h, lambdaapi.OpCreateFunction, imageFunction("img", bad))
		if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, "Source image "+bad+" is not valid") {
			t.Errorf("CreateFunction with ImageUri %q = %s %q", bad, err.ID.Shape.Name(), err.Message)
		}
		call(h, lambdaapi.OpCreateFunction, imageFunction("img", "local/fn:latest"))
		h.settle()
		err = fail(h, lambdaapi.OpUpdateFunctionCode, &lambdaapi.UpdateFunctionCodeInput{FunctionName: "img", ImageUri: new("local/FN:1")})
		if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, "is not valid") {
			t.Errorf("UpdateFunctionCode with an uppercase repository = %s %q", err.ID.Shape.Name(), err.Message)
		}
	})
}

// TestPackageEntriesAreBoundedBeforeParsing: a package with more central
// directory headers than the code store extracts is refused before the
// zip reader allocates for each of them.
func TestPackageEntriesAreBoundedBeforeParsing(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		in := zipFunction(h, "fn")
		in.Code.ZipFile = bytes.Repeat([]byte("PK\x01\x02"), int(codestore.DefaultMaxEntries)+1)
		err := fail(h, lambdaapi.OpCreateFunction, in)
		if err.ID.Shape.Name() != "InvalidParameterValueException" || !strings.Contains(err.Message, "Package fewer files") {
			t.Errorf("CreateFunction with too many entries = %s %q", err.ID.Shape.Name(), err.Message)
		}
	})
}

// TestImageFunctionWithoutDocker: with no Docker daemon an image function
// fails at once, naming its image, while zip functions are unaffected.
func TestImageFunctionWithoutDocker(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		call(h, lambdaapi.OpCreateFunction, imageFunction("img", "local/fn:latest"))
		h.settle()
		got := call(h, lambdaapi.OpGetFunctionConfiguration, &lambdaapi.GetFunctionConfigurationInput{FunctionName: "img"})
		if *got.State != lambdaapi.StateFailed || !strings.Contains(*got.StateReason, "local/fn:latest") {
			t.Errorf("img = %s %v", *got.State, got.StateReason)
		}
	})
}
