package server_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/topology"
)

// A queue created under --persist is there after a restart, and a second
// serve cannot share the data directory while the first runs.
func TestPersistedStateSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	cfg := server.Config{Persist: server.PersistDir{Path: dir}}
	first := launch(t, cfg)
	mustSQS(t, first.endpoint, "CreateQueue", `{"QueueName":"kept"}`)

	_, err := server.New(server.Config{Persist: server.PersistDir{Path: dir}, Docker: server.DockerDisabled{}, LogOutput: io.Discard})
	if locked, ok := errors.AsType[*server.DataDirLockedError](err); !ok || !strings.Contains(locked.Error(), "in use by another understudy serve") {
		t.Fatalf("New on a data directory in use = %v, want a DataDirLockedError", err)
	}
	if err := first.stop(); err != nil {
		t.Fatalf("first Serve = %v", err)
	}

	second := launch(t, cfg)
	second.stopOnCleanup(t)
	if out := mustSQS(t, second.endpoint, "ListQueues", `{}`); !strings.Contains(out, "/000000000000/kept") {
		t.Fatalf("ListQueues after a restart = %s, want the queue kept", out)
	}
}

// While serving, a change is checkpointed to the data directory without
// waiting for shutdown. That an unchanged state is not written again is
// TestCheckpointSkipsAnUnchangedState's.
func TestPersistenceCheckpointsWhileServing(t *testing.T) {
	dir := t.TempDir()
	const every = 10 * time.Millisecond
	r := launch(t, server.Config{Persist: server.PersistDir{Path: dir, SaveEvery: every}})
	r.stopOnCleanup(t)
	state := filepath.Join(dir, "state.snapshot")
	if _, err := os.Stat(state); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a state file before any change: %v", err)
	}
	mustSQS(t, r.endpoint, "CreateQueue", `{"QueueName":"q"}`)
	var saved os.FileInfo
	waitFor(t, r, "a checkpoint after CreateQueue", func() bool {
		var err error
		saved, err = os.Stat(state)
		return err == nil
	})
	mustSQS(t, r.endpoint, "CreateQueue", `{"QueueName":"r"}`)
	waitFor(t, r, "a checkpoint after the second CreateQueue", func() bool {
		again, err := os.Stat(state)
		return err == nil && !os.SameFile(saved, again)
	})
}

// readyDoc is the ready file a TLS-serving server writes.
type readyDoc struct {
	Endpoint    string `json:"endpoint"`
	TLSEndpoint string `json:"tlsEndpoint"`
}

// The TLS listener serves the same API with a self-signed certificate
// for localhost, *.localhost and 127.0.0.1, which a client verifies once
// it trusts the certificate.
func TestTLSListenerServesTheAPI(t *testing.T) {
	r := launch(t, server.Config{TLS: server.TLSSelfSigned{Listen: netip.MustParseAddrPort("127.0.0.1:0")}})
	r.stopOnCleanup(t)
	data, err := os.ReadFile(r.ready)
	if err != nil {
		t.Fatal(err)
	}
	var doc readyDoc
	if err := json.Unmarshal(data, &doc); err != nil || !strings.HasPrefix(doc.TLSEndpoint, "https://127.0.0.1:") {
		t.Fatalf("ready file = %s, %v; want a tlsEndpoint", data, err)
	}
	addr := strings.TrimPrefix(doc.TLSEndpoint, "https://")
	dialer := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // reads the certificate to trust it below
	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("tls.Dialer returned %T", conn)
	}
	cert := tlsConn.ConnectionState().PeerCertificates[0]
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	for _, name := range []string{"127.0.0.1", "localhost", "bucket.localhost"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("the certificate does not verify for %s: %v", name, err)
		}
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, doc.TLSEndpoint+"/_understudy/v1/health", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("health over TLS: %v", err)
	}
	if err := resp.Body.Close(); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health over TLS = %d, %v", resp.StatusCode, err)
	}
}

// ContainerPublished stands in for an admin token only inside a
// container.
func TestContainerPublishedNeedsAContainer(t *testing.T) {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			t.Skipf("%s exists: this test runs in a container", marker)
		}
	}
	_, err := server.New(server.Config{Listen: netip.MustParseAddrPort("0.0.0.0:0"), Exposure: server.ContainerPublished{}, LogOutput: io.Discard})
	le, ok := errors.AsType[*server.ListenError](err)
	if !ok || le.Reason != server.RefusedNotInContainer || !strings.Contains(err.Error(), "inside a container") {
		t.Fatalf("New with ContainerPublished outside a container = %v", err)
	}
}

// Without Docker, health reports why, and DynamoDB, which runs on it, in
// Docker's state for the same reason: disabled when Docker is not there,
// degraded when it is configured but does not answer. Lambda is degraded
// to its control plane either way.
func TestHealthReportsMissingDocker(t *testing.T) {
	// A regular file where the socket should be is there, but answers
	// nothing.
	mute := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(mute, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		docker server.Docker
		state  admin.ServiceState
		reason string
	}{
		{"disabled", server.DockerDisabled{}, admin.Disabled, "disabled by configuration (--docker disabled"},
		{"missing socket", server.DockerSocket{Path: filepath.Join(t.TempDir(), "docker.sock")}, admin.Disabled, "point --docker (UNDERSTUDY_DOCKER, which wins over DOCKER_HOST)"},
		{"no answer", server.DockerSocket{Path: mute}, admin.Degraded, "does not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := adminServer(t, server.Config{Docker: tc.docker})
			h, err := c.Health(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for what, s := range map[string]admin.ServiceStatus{"docker": h.Docker, "dynamodbLocal": h.DynamoDBLocal, "dynamodb": h.Services["dynamodb"]} {
				if s.Status != tc.state || !strings.Contains(s.Reason, tc.reason) {
					t.Errorf("%s = %+v, want %s naming %q", what, s, tc.state, tc.reason)
				}
			}
			// Lambda's control plane works without Docker; only Invoke
			// cannot run functions.
			if s := h.Services["lambda"]; s.Status != admin.Degraded || !strings.Contains(s.Reason, "control plane only") || !strings.Contains(s.Reason, "Docker is "+string(tc.state)) {
				t.Errorf("lambda = %+v, want degraded to its control plane, naming Docker as %s", s, tc.state)
			}
			if h.Services["sqs"].Status != admin.Enabled {
				t.Errorf("sqs = %+v, want enabled without Docker", h.Services["sqs"])
			}
		})
	}
}

// syncBuffer is a log output the server writes from its goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The banner names the endpoint, the admin URL, the environment for an
// SDK, the services with those that cannot serve marked, the DynamoDB
// Local licence line, and warns when Docker is not there or does not
// answer and when lambda.network keeps functions from running.
func TestBannerAnnouncesTheEndpoint(t *testing.T) {
	// A regular file where the socket should be is there, but answers
	// nothing.
	mute := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(mute, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  server.Config
		want []string
	}{
		{"Docker disabled", server.Config{}, []string{
			"warning: Docker is disabled by configuration",
			"lambda (degraded: control plane only)",
			"dynamodb (disabled)",
		}},
		{"Docker does not answer", server.Config{Docker: server.DockerSocket{Path: mute}}, []string{
			"warning: Docker at " + mute + " does not answer",
			"lambda (degraded: control plane only)",
		}},
		{"lambda.network disabled", server.Config{LambdaNetwork: server.NetworkDisabled{}}, []string{
			"warning: Docker is disabled by configuration",
			"warning: lambda.network is disabled by configuration",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &syncBuffer{}
			tc.cfg.LogOutput, tc.cfg.Version = out, "test"
			r := launch(t, tc.cfg)
			if err := r.stop(); err != nil {
				t.Fatal(err)
			}
			banner := out.String()
			for _, want := range append([]string{
				"understudy test is serving",
				"endpoint  " + r.endpoint,
				"admin     " + r.endpoint + "/_understudy/v1/",
				"export AWS_ENDPOINT_URL=" + r.endpoint,
				"--accept-dynamodb-local-license",
			}, tc.want...) {
				if !strings.Contains(banner, want) {
					t.Errorf("banner lacks %q:\n%s", want, banner)
				}
			}
			if strings.Contains(banner, "sqs (") {
				t.Errorf("banner marks sqs, which serves without Docker:\n%s", banner)
			}
		})
	}
}

// Health reports the bound main listener and the serving topology: before
// any container detects one, in-container inside a container and host
// otherwise.
func TestHealthReportsListenerAndTopology(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	h, err := c.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(endpoint, "http://"); h.Listener != want {
		t.Errorf("listener = %q, want %s", h.Listener, want)
	}
	want := admin.TopologyHost
	if topology.RunsInContainer(os.DirFS("/")) {
		want = admin.TopologyInContainer
	}
	if h.Topology != want {
		t.Errorf("topology = %q, want %s", h.Topology, want)
	}
}

// Health reports the clock as GET /clock does: frozen at the instant serve
// was started at.
func TestHealthReportsTheClock(t *testing.T) {
	c, _ := adminServer(t, server.Config{})
	h, err := c.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if h.Clock.Mode != admin.Frozen || !h.Clock.Now.Equal(frozenAt) {
		t.Errorf("health clock = %+v, want frozen at %s", h.Clock, frozenAt)
	}
}

// Configured fault rules are active from the start, and one the fault
// store refuses fails New with the configuration setting at fault, worded
// as the configuration's own refusals are.
func TestConfiguredFaultsArePreloaded(t *testing.T) {
	throttle := fault.Rule{
		ID: "throttle-sends", Phase: fault.Request,
		Match:  fault.Match{Operations: []ref.OperationID{{Service: "sqs", Name: "SendMessage"}}},
		Action: fault.Throttle{}, Trigger: fault.Always{},
	}
	c, _ := adminServer(t, server.Config{Faults: []fault.Rule{throttle}})
	faults, err := c.Faults(t.Context())
	if err != nil || len(faults) != 1 || faults[0].Rule.ID != "throttle-sends" {
		t.Fatalf("faults = %+v, %v; want the configured rule", faults, err)
	}
	bad := throttle
	bad.Phase = 0
	_, err = server.New(server.Config{Faults: []fault.Rule{throttle, bad}, Docker: server.DockerDisabled{}, LogOutput: io.Discard})
	se, ok := errors.AsType[*server.SettingError](err)
	if !ok || !strings.HasPrefix(se.Setting, "faults[1]") {
		t.Fatalf("New with an invalid rule = %v, want a SettingError for faults[1]", err)
	}
	if re, ok := errors.AsType[*fault.RuleError](err); !ok || !strings.Contains(err.Error(), re.Reason) {
		t.Fatalf("New with an invalid rule = %v, want the fault store's RuleError inside", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "config: faults[1]") || strings.Contains(msg, "server:") || strings.Contains(msg, "no rule was added") {
		t.Fatalf("New with an invalid rule = %q, want it to read as a configuration refusal of faults[1]", msg)
	}
}

// A configured hot-reload directory that does not exist fails New with the
// binding at fault and the next step.
func TestUnwatchableHotReloadDirNamesTheBinding(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	_, err := server.New(server.Config{
		HotReload: []server.HotReload{{Function: "ok", Dir: t.TempDir()}, {Function: "worker", Dir: missing}},
		Docker:    server.DockerDisabled{}, LogOutput: io.Discard,
	})
	se, ok := errors.AsType[*server.SettingError](err)
	if !ok || se.Setting != "lambda.hotReload[1].dir" {
		t.Fatalf("New with a missing hot-reload directory = %v, want a SettingError for lambda.hotReload[1].dir", err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "config: lambda.hotReload[1].dir: ") || !strings.Contains(msg, missing) || !strings.Contains(msg, "create the directory") {
		t.Fatalf("New with a missing hot-reload directory = %q, want the binding, the directory and the next step", msg)
	}
}

// An unsigned request on a Lambda date path is Lambda until a bucket of
// that name exists; then it is that bucket's S3 path-style request.
func TestABucketNamedLikeALambdaDateTakesItsPath(t *testing.T) {
	srv, _ := build(t, server.Config{})
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:4566/2015-03-31/functions/", http.NoBody))
		return rec
	}
	if rec := get(); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"Functions"`) {
		t.Fatalf("GET /2015-03-31/functions/ without the bucket = %d %s, want Lambda ListFunctions", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPut, "http://localhost:4566/2015-03-31", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("CreateBucket 2015-03-31 = %d %s", rec.Code, rec.Body)
	}
	if rec := get(); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "<Code>NoSuchKey</Code>") {
		t.Fatalf("GET /2015-03-31/functions/ with the bucket = %d %s, want S3 NoSuchKey", rec.Code, rec.Body)
	}
}
