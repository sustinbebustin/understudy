package cli_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/cli"
	"github.com/sustinbebustin/understudy/internal/config"
)

// A journal query prints the newest --limit events, oldest first, and says
// on stderr when it left older ones out or none matched.
func TestJournalQueryPrintsTheNewest(t *testing.T) {
	inst := start(t)
	for _, name := range []string{"q0", "q1", "q2", "q3", "q4"} {
		createQueue(t, inst.Endpoint(), name)
	}
	page, err := inst.Admin().JournalEvents(t.Context(), admin.JournalQuery{Operation: "sqs.CreateQueue"})
	if err != nil || len(page.Events) != 5 {
		t.Fatalf("journal holds %+v, %v; want the 5 CreateQueue calls", page, err)
	}
	all := make([]uint64, len(page.Events))
	for i, e := range page.Events {
		all[i] = e.ID
	}
	js := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--operation", "sqs.CreateQueue", "--limit", "3", "--output", "json")
	var got []uint64
	for line := range strings.Lines(js.stdout) {
		var e admin.JournalEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		got = append(got, e.ID)
	}
	if js.code != admin.ExitOK || !slices.Equal(got, all[2:]) || js.stderr != "" {
		t.Errorf("journal --limit 3 --output json = %d, events %v, stderr %q; want the newest %v and a quiet stderr", js.code, got, js.stderr, all[2:])
	}
	text := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--operation", "sqs.CreateQueue", "--limit", "3")
	if text.code != admin.ExitOK || strings.Count(text.stdout, "\n") != 3 || !strings.Contains(text.stderr, "showing the newest 3 of 5 matching events") {
		t.Errorf("journal --limit 3 = %d\nstdout %s\nstderr %q", text.code, text.stdout, text.stderr)
	}
	whole := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--operation", "sqs.CreateQueue", "--limit", "5")
	if whole.code != admin.ExitOK || strings.Count(whole.stdout, "\n") != 5 || whole.stderr != "" {
		t.Errorf("journal --limit 5 of 5 = %d\nstdout %s\nstderr %q", whole.code, whole.stdout, whole.stderr)
	}
	none := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--operation", "sqs.DeleteQueue")
	if none.code != admin.ExitOK || none.stdout != "" || !strings.Contains(none.stderr, "no events matched") {
		t.Errorf("journal matching nothing = %d, stdout %q, stderr %q", none.code, none.stdout, none.stderr)
	}
}

// A call no service operation claims is summarized like any other call,
// as the service's unknown operation, rather than printed as raw JSON.
func TestJournalLineOfAnUnknownOperation(t *testing.T) {
	inst := start(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, inst.Endpoint()+"/", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--kind", "api-call")
	if r.code != admin.ExitOK || !strings.Contains(r.stdout, ".<unknown operation> failed") || strings.Contains(r.stdout, `{"service"`) {
		t.Errorf("journal of a call to no operation = %d:\n%s", r.code, r.stdout)
	}
}

// A text journal line of a call a fault failed names the error, the rule
// and the request id 'journal trace --request-id' takes; the fault's own
// event names the rule, the hit and the call it hit.
func TestJournalLinesNameTheFailure(t *testing.T) {
	inst := start(t)
	rule := admin.FaultRule{
		ID: "throttle-create", Phase: admin.RequestPhase,
		Match:  admin.FaultMatch{Operations: []string{"sqs.CreateQueue"}},
		Action: admin.Throttle{}, Trigger: admin.NextN{N: 1},
	}
	if _, err := inst.Admin().PutFault(t.Context(), rule); err != nil {
		t.Fatal(err)
	}
	if err := sendCreateQueue(t.Context(), inst.Endpoint(), "throttled"); err == nil {
		t.Fatal("CreateQueue under a throttle rule succeeded")
	}
	calls := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--operation", "sqs.CreateQueue")
	if calls.code != admin.ExitOK || !strings.Contains(calls.stdout, "sqs.CreateQueue injected ") ||
		!strings.Contains(calls.stdout, "by rule throttle-create request-id ") || strings.Contains(calls.stdout, "#") {
		t.Errorf("journal of the throttled call = %d:\n%s", calls.code, calls.stdout)
	}
	hits := runCLI(t.Context(), inst.Endpoint(), nil, "journal", "--kind", "fault-injected")
	if hits.code != admin.ExitOK || !strings.Contains(hits.stdout, "rule throttle-create hit 1 of 1, on event ") {
		t.Errorf("journal --kind fault-injected = %d:\n%s", hits.code, hits.stdout)
	}
}

// journal --follow prints the matching events, then the new ones as they
// are recorded, and ends cleanly when its context does.
func TestJournalFollow(t *testing.T) {
	inst := start(t)
	createQueue(t, inst.Endpoint(), "first")
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + inst.Endpoint()}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 1)
	createQueue(t, inst.Endpoint(), "second")
	waitLines(t, &out, 2)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	var ids []uint64
	for line := range strings.Lines(out.String()) {
		var e admin.JournalEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		ids = append(ids, e.ID)
	}
	if len(ids) != 2 || ids[0] >= ids[1] {
		t.Errorf("followed events %v, want the two CreateQueue calls in order", ids)
	}
}

// journal --follow prints every matching event recorded before it, whatever
// --limit says, then the new ones.
func TestJournalFollowIgnoresTheLimit(t *testing.T) {
	inst := start(t)
	createQueue(t, inst.Endpoint(), "first")
	createQueue(t, inst.Endpoint(), "second")
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--limit", "1", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + inst.Endpoint()}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 2)
	createQueue(t, inst.Endpoint(), "third")
	waitLines(t, &out, 3)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	if n := strings.Count(out.String(), "\n"); n != 3 {
		t.Errorf("journal --limit 1 --follow printed %d events, want all 3:\n%s", n, out.String())
	}
}

// journal --follow loses nothing recorded between its query and the
// stream: with nothing matching at first, a call recorded after the query
// answered, before the CLI could act on the answer, is printed.
func TestJournalFollowLosesNothingAfterAnEmptyQuery(t *testing.T) {
	inst := start(t)
	target, err := url.Parse(inst.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	var answered atomic.Bool
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != admin.Prefix+"/journal/events" {
			proxy.ServeHTTP(w, r)
			return
		}
		// Hold the query's answer until a matching call is recorded.
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, r)
		if answered.CompareAndSwap(false, true) {
			if err := sendCreateQueue(r.Context(), inst.Endpoint(), "late"); err != nil {
				t.Errorf("recording the late call: %v", err)
			}
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		if _, err := w.Write(rec.Body.Bytes()); err != nil {
			t.Errorf("relaying the query's answer: %v", err)
		}
	}))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(t.Context())
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"journal", "--operation", "sqs.CreateQueue", "--follow", "--output", "json"}, cli.Options{
			Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + front.URL}, ReadFile: os.ReadFile,
		})
	}()
	waitLines(t, &out, 1)
	cancel()
	if code := <-done; code != admin.ExitOK {
		t.Fatalf("journal --follow after cancel = %d, stderr %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"CreateQueue"`) || strings.Count(out.String(), "\n") != 1 {
		t.Errorf("followed %q, want the one late CreateQueue call", out.String())
	}
}
