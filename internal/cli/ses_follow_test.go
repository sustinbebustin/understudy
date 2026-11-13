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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/cli"
	"github.com/sustinbebustin/understudy/internal/config"
)

// followSES runs 'ses messages --follow --output json' with args against
// endpoint in the background; stop cancels it and returns its exit code.
func followSES(t *testing.T, endpoint string, args ...string) (out *syncBuffer, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	out = &syncBuffer{}
	var errOut syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, append([]string{"ses", "messages", "--follow", "--output", "json"}, args...), cli.Options{
			Stdout: out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + endpoint}, ReadFile: os.ReadFile,
		})
	}()
	t.Cleanup(cancel)
	return out, func() int {
		cancel()
		code := <-done
		if code != admin.ExitOK {
			t.Errorf("ses messages --follow after cancel = %d, stderr %s", code, errOut.String())
		}
		return code
	}
}

// subjects reads the NDJSON lines out printed as the subjects of the
// messages they hold.
func subjects(t *testing.T, out string) []string {
	t.Helper()
	var got []string
	for line := range strings.Lines(out) {
		var m admin.SESMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		got = append(got, m.Subject.Data)
	}
	return got
}

// ses messages --follow prints the messages recorded before it, once, then
// each send accepted after it started, and exits 0 when cancelled.
func TestSESMessagesFollowPrintsOldAndNewMailOnce(t *testing.T) {
	s := serveSES(t)
	sendSES(t, s.endpoint, "before")
	out, stop := followSES(t, s.endpoint)
	waitLines(t, out, 1)
	sendSES(t, s.endpoint, "after")
	waitLines(t, out, 2)
	stop()
	if got := subjects(t, out.String()); len(got) != 2 || got[0] != "before" || got[1] != "after" {
		t.Errorf("followed subjects %v, want [before after]", got)
	}
}

// Filters apply to followed messages: a send to another recipient is not
// printed.
func TestSESMessagesFollowAppliesFilters(t *testing.T) {
	s := serveSES(t)
	out, stop := followSES(t, s.endpoint, "--to", "bounce@simulator.amazonses.com")
	sesCall(t, s.endpoint, url.Values{
		"Action":                           {"SendEmail"},
		"Source":                           {"sender@example.com"},
		"Destination.ToAddresses.member.1": {"bounce@simulator.amazonses.com"},
		"Message.Subject.Data":             {"wanted"},
		"Message.Body.Text.Data":           {"body"},
	})
	waitLines(t, out, 1)
	// The unwanted send comes last, so once the wanted one after it shows
	// the unwanted was seen and skipped.
	sendSES(t, s.endpoint, "unwanted")
	sesCall(t, s.endpoint, url.Values{
		"Action":                           {"SendEmail"},
		"Source":                           {"sender@example.com"},
		"Destination.ToAddresses.member.1": {"bounce@simulator.amazonses.com"},
		"Message.Subject.Data":             {"wanted too"},
		"Message.Body.Text.Data":           {"body"},
	})
	waitLines(t, out, 2)
	stop()
	if got := subjects(t, out.String()); len(got) != 2 || got[0] != "wanted" || got[1] != "wanted too" {
		t.Errorf("followed subjects %v, want [wanted \"wanted too\"]", got)
	}
}

// A message recorded after the listing answered and before the stream
// delivers anything is printed exactly once.
func TestSESMessagesFollowLosesNothingBetweenListingAndStream(t *testing.T) {
	s := serveSES(t)
	sendSES(t, s.endpoint, "before")
	target, err := url.Parse(s.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	var answered atomic.Bool
	// The first listing's answer is held until the late send is recorded.
	listed, late := make(chan struct{}), make(chan struct{})
	go func() {
		<-listed
		sendSES(t, s.endpoint, "late")
		close(late)
	}()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != admin.Prefix+"/ses/messages" {
			proxy.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, r)
		if answered.CompareAndSwap(false, true) {
			listed <- struct{}{}
			<-late
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		if _, err := w.Write(rec.Body.Bytes()); err != nil {
			t.Errorf("relaying the listing's answer: %v", err)
		}
	}))
	t.Cleanup(front.Close)
	out, stop := followSES(t, front.URL)
	waitLines(t, out, 2)
	stop()
	if got := subjects(t, out.String()); len(got) != 2 || got[0] != "before" || got[1] != "late" {
		t.Errorf("followed subjects %v, want [before late] once each", got)
	}
}
