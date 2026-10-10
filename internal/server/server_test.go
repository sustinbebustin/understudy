package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
)

var update = flag.Bool("update", false, "rewrite the response goldens in testdata")

// start runs a seeded, frozen server on an ephemeral loopback port and
// returns it with its endpoint, read from the ready file. Cleanup shuts it
// down and checks the shutdown was clean.
func start(t *testing.T) (srv *server.Server, endpoint string) {
	t.Helper()
	seed := uint64(7)
	at, err := time.Parse(time.RFC3339, "2026-10-09T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	r := launch(t, server.Config{ClockStart: server.ClockFrozen{At: at}, Seed: &seed, Version: "test"})
	r.stopOnCleanup(t)
	return r.srv, r.endpoint
}

// running is a server Serve is running.
type running struct {
	srv      *server.Server
	endpoint string
	ready    string
	cancel   context.CancelFunc
	// served is closed when Serve returns, with its error in err.
	served chan struct{}
	err    error
}

// stop ends Serve and returns its error.
func (r *running) stop() error {
	r.cancel()
	<-r.served
	return r.err
}

// stopOnCleanup stops the server when the test ends and checks the
// shutdown was clean and removed the ready file.
func (r *running) stopOnCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := r.stop(); err != nil {
			t.Errorf("Serve = %v, want a clean shutdown", err)
		}
		if _, err := os.Stat(r.ready); !os.IsNotExist(err) {
			t.Errorf("the ready file outlived the server: %v", err)
		}
	})
}

// waitTimeout bounds every wait on a server in these tests.
const waitTimeout = 10 * time.Second

// build creates a server for cfg on an ephemeral loopback port with a ready
// file and discarded logs, and returns it with the ready file's path.
func build(t *testing.T, cfg server.Config) (srv *server.Server, ready string) {
	t.Helper()
	cfg.Listen = netip.MustParseAddrPort("127.0.0.1:0")
	cfg.ReadyFile = filepath.Join(t.TempDir(), "ready.json")
	cfg.LogOutput = io.Discard
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv, cfg.ReadyFile
}

// launch builds and serves cfg. The caller stops the server.
func launch(t *testing.T, cfg server.Config) *running {
	t.Helper()
	srv, ready := build(t, cfg)
	return serve(t, srv, ready)
}

// serve runs srv's Serve and waits for its ready file. It fails the test,
// rather than wait on, when Serve returns first or the wait passes
// waitTimeout. The caller stops the server.
func serve(t *testing.T, srv *server.Server, ready string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{srv: srv, ready: ready, cancel: cancel, served: make(chan struct{})}
	go func() {
		defer close(r.served)
		r.err = srv.Serve(ctx)
	}()
	waitFor(t, r, "the ready file", func() bool {
		data, err := os.ReadFile(r.ready)
		if err != nil {
			return false
		}
		var doc struct {
			Endpoint string `json:"endpoint"`
			PID      int    `json:"pid"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("ready file %q: %v", data, err)
		}
		if doc.PID != os.Getpid() || !strings.HasPrefix(doc.Endpoint, "http://127.0.0.1:") {
			t.Fatalf("ready file = %+v", doc)
		}
		r.endpoint = doc.Endpoint
		return true
	})
	return r
}

// waitFor polls cond until it holds. It fails the test when Serve returns
// first or waitTimeout passes, naming what it waited for.
func waitFor(t *testing.T, r *running, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(waitTimeout)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-r.served:
			t.Fatalf("Serve returned %v while the test waited for %s", r.err, what)
		case <-deadline:
			t.Fatalf("no %s after %s; Serve stopped with %v", what, waitTimeout, r.stop())
		case <-tick.C:
		}
	}
}

// The admin handler reads the escaped path, as the edge does, so an encoded
// spelling of the health path is no admin endpoint on either listener.
func TestHealthMatchesTheEscapedPath(t *testing.T) {
	srv, _ := build(t, server.Config{Version: "test"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_understudy/v1/heal%74h", http.NoBody))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Fatalf("GET /_understudy/v1/heal%%74h = %d %s, want 404 not_found", rec.Code, rec.Body)
	}
}

func TestHealthIsUnavailableUntilTheListenerAccepts(t *testing.T) {
	srv, ready := build(t, server.Config{Version: "test"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_understudy/v1/health", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"ready\":false,\"waiting\":[\"listener\"]}\n" {
		t.Fatalf("health before Serve = %d %s", rec.Code, rec.Body)
	}

	r := serve(t, srv, ready)
	r.stopOnCleanup(t)
	resp, err := http.Get(r.endpoint + "/_understudy/v1/health") //nolint:noctx // a test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Ready    bool                         `json:"ready"`
		Version  string                       `json:"version"`
		Uptime   string                       `json:"uptime"`
		Services map[string]map[string]string `json:"services"`
		Model    string                       `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !body.Ready || body.Version != "test" || body.Services["sqs"]["status"] != "enabled" || body.Uptime == "" {
		t.Fatalf("health after Serve = %d %+v", resp.StatusCode, body)
	}
	if !strings.HasPrefix(body.Model, "api-models-aws@") {
		t.Errorf("model pin = %q", body.Model)
	}
}

// exchange sends one signed awsJson1_0 SQS request over a raw connection
// and returns the response bytes exactly as the server wrote them, with the
// Date header's value checked and replaced by <date>.
func exchange(t *testing.T, endpoint, target, body string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint+"/", strings.NewReader(body)) //nolint:noctx // written to a raw connection
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS."+target)
	req.Header.Set("X-Amzn-Query-Mode", "true")
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sqs"}, time.Now())

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(endpoint, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// A server that never answers fails the read at the deadline instead of
	// hanging the test until go test's own timeout.
	if err := conn.SetDeadline(time.Now().Add(waitTimeout)); err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	resp, err := http.ReadResponse(bufio.NewReader(io.TeeReader(conn, &raw)), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := http.ParseTime(resp.Header.Get("Date")); err != nil || len(resp.Header.Values("Date")) != 1 {
		t.Fatalf("Date headers %q, want exactly one HTTP date", resp.Header.Values("Date"))
	}
	return dateLine.ReplaceAllString(raw.String(), "Date: <date>\r\n")
}

var dateLine = regexp.MustCompile(`Date: [^\r]*\r\n`)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s response:\n%s\nwant:\n%s", name, got, want)
	}
}

// TestSQSOverTheWire drives the tracer bullet: the four SQS operations as
// raw awsJson1_0 requests through the real listener, compared byte for byte
// with goldens in the shape of sqs.md 2.1 and 2.2, and traced in the
// journal.
func TestSQSOverTheWire(t *testing.T) {
	srv, endpoint := start(t)
	queueURL := endpoint + "/000000000000/MyQueue"

	created := exchange(t, endpoint, "CreateQueue", `{"QueueName":"MyQueue","Attributes":{"VisibilityTimeout":"40"},"tags":{"QueueType":"Production"}}`)
	golden(t, "create-queue", strings.ReplaceAll(created, endpoint, "<endpoint>"))

	sent := exchange(t, endpoint, "SendMessage", `{"QueueUrl":"`+queueURL+`","MessageBody":"message"}`)
	golden(t, "send-message", sent)

	received := exchange(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`","MaxNumberOfMessages":1}`)
	golden(t, "receive-message", received)
	handle := receiptHandle.FindStringSubmatch(received)
	if handle == nil {
		t.Fatalf("no receipt handle in %s", received)
	}

	deleted := exchange(t, endpoint, "DeleteMessage", `{"QueueUrl":"`+queueURL+`","ReceiptHandle":"`+handle[1]+`"}`)
	golden(t, "delete-message", deleted)

	empty := exchange(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`"}`)
	golden(t, "receive-message-empty", empty)

	missing := exchange(t, endpoint, "SendMessage", `{"QueueUrl":"`+endpoint+`/000000000000/missing","MessageBody":"m"}`)
	golden(t, "send-message-missing-queue", missing)

	assertTrace(t, srv.Journal())
}

var receiptHandle = regexp.MustCompile(`"ReceiptHandle":"([^"]+)"`)

// assertTrace checks the journal: one root API call per request, external
// over awsJson1_0, as the default account's root, with the outcomes the
// client saw and the queue touched.
func assertTrace(t *testing.T, j *journal.Journal) {
	t.Helper()
	page := j.Query(journal.Filter{Kind: journal.KindAPICall})
	got := make([]string, 0, len(page.Events))
	for i := range page.Events {
		e := &page.Events[i]
		call, ok := e.Detail.(journal.APICall)
		if !ok {
			t.Fatalf("event %d is %T", e.ID, e.Detail)
		}
		tree, ok := j.TraceByRequestID(call.RequestID)
		if !ok || tree.Event.ID != e.ID || e.Cause.Parent != 0 || len(tree.Children) != 0 {
			t.Errorf("%s: trace %+v, want a root with no children", call.Op, tree)
		}
		origin, ok := call.Origin.(ref.External)
		if !ok || origin.Protocol != ref.AWSJSON10 || origin.Listener != ref.ListenerMain {
			t.Errorf("%s: origin %+v", call.Op, call.Origin)
		}
		if call.Principal != "arn:aws:iam::000000000000:root" {
			t.Errorf("%s: principal %q", call.Op, call.Principal)
		}
		outcome := "ok"
		if f, ok := call.Outcome.(journal.Failed); ok {
			outcome = f.Identity.Shape
		}
		touched := make([]string, len(call.Resources))
		for i, arn := range call.Resources {
			touched[i] = arn.String()
		}
		got = append(got, call.Op.Name+" "+outcome+" "+strings.Join(touched, ","))
	}
	const queue = "arn:aws:sqs:us-east-1:000000000000:MyQueue"
	want := []string{
		"CreateQueue ok " + queue, "SendMessage ok " + queue, "ReceiveMessage ok " + queue,
		"DeleteMessage ok " + queue, "ReceiveMessage ok " + queue,
		"SendMessage com.amazonaws.sqs#QueueDoesNotExist arn:aws:sqs:us-east-1:000000000000:missing",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("journaled calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
