package server

import (
	"io"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/docker/dockertest"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
)

// Config.DockerLabels reaches every container serve creates: a function
// environment's and DynamoDB Local's carry the labels beside the own ones,
// so a Testcontainers reaper selecting its session label removes both
// (testcontainers.md 9.3, 8.6). New keeps its own copy of the map.
func TestDockerLabelsReachEveryContainerServeCreates(t *testing.T) {
	labels := map[string]string{"org.testcontainers": "true", "org.testcontainers.session-id": "6f1b8c2e-1d7a-4c39-9a51-0d2f3b4c5e6f"}
	want := maps.Clone(labels)
	const fnImage = "public.ecr.aws/lambda/python:3.13"
	// A bridge whose gateway is loopback stands in for docker0, so
	// functions share the main listener and no container listener opens.
	daemon := dockertest.New(t, dockertest.Options{BridgeSubnet: netip.MustParsePrefix("127.0.0.0/8")})
	daemon.AddImage(fnImage, docker.Image{Architecture: "amd64", OS: "linux"})
	daemon.AddImage(ddblocal.Reference(), docker.Image{ID: ddblocal.Digest})
	daemon.SetProgram(ddblocal.Reference(), dockertest.Exit{Code: 1})
	srv, err := New(Config{
		Docker:        DockerSocket{Path: daemon.Socket()},
		LambdaNetwork: NetworkBridge{Gateway: netip.MustParseAddr("127.0.0.1")},
		DockerLabels:  labels,
		LogOutput:     io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	labels["com.example.late"] = "set after New"
	main, err := listen(t.Context(), netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.begin(); err != nil {
		t.Fatal(err)
	}
	sess, err := srv.start(t.Context(), main, nil)
	t.Cleanup(func() {
		if err := srv.shutdown(t.Context(), sess); err != nil {
			t.Errorf("shutdown = %v", err)
		}
	})
	if err != nil {
		t.Fatalf("start = %v", err)
	}

	fn, err := srv.wired.host.Start(t.Context(), functionSpec(t, fnImage), launcher.Slot{
		ID:         "0123456789abcdef0123456789abcdef",
		RuntimeAPI: netip.MustParseAddrPort("127.0.0.1:41234"),
		LogStream:  "2026/10/26/[$LATEST]0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("starting a function container = %v", err)
	}
	if _, ok := daemon.Container(fn.ID); !ok {
		t.Errorf("function container %s is not on the daemon", fn.ID)
	}
	// start begins DynamoDB Local in the background, since its image is
	// present; its program exits at once, so that start fails after the
	// create, and the created container is what is checked. The test does
	// not start DynamoDB Local itself: a second start after the background
	// one has failed would create a third container, and whether it had
	// failed by then depends on timing.
	if !daemon.WaitCreated(t.Context(), 2) {
		t.Fatal("DynamoDB Local's container was not created in the background")
	}

	created := daemon.Created()
	if len(created) != 2 {
		t.Fatalf("created %d containers, want the function's and DynamoDB Local's", len(created))
	}
	for _, c := range created {
		got := c.Config.Labels
		for k, v := range want {
			if got[k] != v {
				t.Errorf("container %s (%s): label %s = %q, want %q", c.Name, c.Config.Image, k, got[k], v)
			}
		}
		if _, late := got["com.example.late"]; late {
			t.Errorf("container %s carries a label added to the map after New", c.Name)
		}
		if got[docker.LabelManaged] != "true" {
			t.Errorf("container %s: %s = %q, want true, so sweeps still find it", c.Name, docker.LabelManaged, got[docker.LabelManaged])
		}
	}
}

// functionSpec is a python function environment on image.
func functionSpec(t *testing.T, image string) launcher.Spec {
	t.Helper()
	account, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("def handler(e, c): return e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return launcher.Spec{
		Function:           launcher.Function{Name: "worker", Version: "$LATEST", Account: account, Region: region},
		Generation:         1,
		Image:              image,
		Architecture:       launcher.X86,
		Code:               launcher.ZipCode{Dir: dir, Handler: "handler.handler"},
		MemoryMB:           128,
		EphemeralStorageMB: 512,
		Credentials:        launcher.Credentials{AccessKeyID: "ASIAQAAAAAAAAEXAMPLE", SecretAccessKey: "secret", SessionToken: "token"},
		LogGroup:           "/aws/lambda/worker",
	}
}
