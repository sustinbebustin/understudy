package server_test

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A long poll parked when shutdown begins ends with the shutdown, which
// does not wait out its 20 s WaitTimeSeconds.
func TestShutdownEndsParkedLongPolls(t *testing.T) {
	r := launch(t, server.Config{ClockStart: server.ClockFrozen{}})
	post := func(target, body string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.endpoint+"/", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-amz-json-1.0")
		req.Header.Set("X-Amz-Target", "AmazonSQS."+target)
		return http.DefaultClient.Do(req)
	}
	resp, err := post("CreateQueue", `{"QueueName":"q"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		if resp, err := post("ReceiveMessage", `{"QueueUrl":"`+r.endpoint+`/000000000000/q","WaitTimeSeconds":20}`); err == nil {
			_ = resp.Body.Close()
		}
	}()
	// Shut down only once the poll is parked: its call is in the journal,
	// still pending. Journal streams carry only ended calls, so this polls
	// the journal's query.
	waitFor(t, r, "a parked ReceiveMessage", func() bool { return parked(r.srv.Journal()) })
	began := time.Now()
	if err := r.stop(); err != nil {
		t.Fatalf("Serve = %v", err)
	}
	select {
	case <-polled:
	case <-time.After(waitTimeout):
		t.Fatalf("the long poll was still open %s after shutdown", waitTimeout)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Errorf("shutdown took %s with a long poll parked", took)
	}
}

// A connection that never sends a request, like the spare one Go's
// Transport dials when no idle connection is free, does not hold shutdown
// for the grace period or make it fail.
func TestShutdownClosesConnectionsThatNeverSentARequest(t *testing.T) {
	r := launch(t, server.Config{})
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(r.endpoint, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Shut down only once the server has accepted the connection: a request
	// on another connection is answered only after the earlier accept.
	resp, err := http.Get(r.endpoint + "/_understudy/v1/health") //nolint:noctx // test request, bounded by the server's shutdown
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := r.stop(); err != nil {
		t.Fatalf("Serve = %v, want a clean shutdown", err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("shutdown took %s with an unused connection open", took)
	}
}

// parked reports whether a ReceiveMessage call has begun and not ended.
func parked(j *journal.Journal) bool {
	page := j.Query(journal.Filter{Kind: journal.KindAPICall})
	for i := range page.Events {
		call, ok := page.Events[i].Detail.(journal.APICall)
		if _, pending := call.Outcome.(journal.Pending); ok && pending && call.Op.Name == "ReceiveMessage" {
			return true
		}
	}
	return false
}
