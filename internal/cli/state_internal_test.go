package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/config"
)

// slowServer answers every admin call after delay with answer as JSON, as
// a server busy draining invocations would.
func slowServer(t *testing.T, delay time.Duration, answer string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(answer)); err != nil {
			t.Errorf("writing the scripted answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runAnswering runs args against endpoint with answer as the bound on a
// prompt call's answer.
func runAnswering(t *testing.T, endpoint string, answer time.Duration, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(t.Context(), args, Options{Stdout: &out, Stderr: &errOut, Env: []string{config.EndpointEnv + "=" + endpoint}}, answer)
	return code, out.String(), errOut.String()
}

// reset waits for its answer past the bound on prompt calls, since the
// server answers only once the invocations in its scope have drained, and
// says which mode the clock returned to and how many fault rules it
// removed when asked to reset them.
func TestResetWaitsForTheDrain(t *testing.T) {
	endpoint := slowServer(t, 300*time.Millisecond, `{"services":["sqs","sns"],"clock":"frozen","faults":3}`)
	code, stdout, stderr := runAnswering(t, endpoint, 50*time.Millisecond, "reset", "--clock", "--faults")
	want := "reset: sqs, sns; clock returned to frozen; 3 fault rules removed\n"
	if code != admin.ExitOK || stdout != want {
		t.Errorf("reset --clock --faults answered after 300ms = %d, stdout %q, stderr %q; want 0 and %q", code, stdout, stderr, want)
	}
}

// A prompt call the server took but did not answer in time exits 3 saying
// so, with the next step, rather than telling the user to start a server
// that is running.
func TestNoAnswerIsNotCalledUnreachable(t *testing.T) {
	endpoint := slowServer(t, 2*time.Second, `{}`)
	code, _, stderr := runAnswering(t, endpoint, 50*time.Millisecond, "clock", "now")
	if code != admin.ExitUnreachable || !strings.Contains(stderr, "took the call but sent no answer within 50ms") ||
		!strings.Contains(stderr, "understudy health") || strings.Contains(stderr, "start it with") {
		t.Errorf("clock now against a server that answers late = %d, stderr %q; want 3 saying no answer came, without 'start it with'", code, stderr)
	}
}

// health names a frozen clock's instant and a scaled clock's factor, which
// a test may have left that way; a running clock gets no line.
func TestWriteHealthNamesAClockThatIsNotRunning(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for clock, want := range map[admin.ClockState]string{
		{Mode: admin.Frozen, Now: at}:              "clock: frozen at 2026-01-01T00:00:00Z\n",
		{Mode: admin.Scaled, Now: at, Factor: 60}:  "clock: scaled x60, now 2026-01-01T00:00:00Z\n",
		{Mode: admin.Running, Now: at, Wall: at}:   "",
		{Mode: admin.Scaled, Now: at, Factor: 0.5}: "clock: scaled x0.5, now 2026-01-01T00:00:00Z\n",
	} {
		var b strings.Builder
		if err := writeHealth(&b, &admin.Health{Ready: true, Version: "test", Model: "pin", Clock: clock}); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); !strings.HasPrefix(got, "ready: understudy test, up 0s, models pin\n"+want+"\n") {
			t.Errorf("health with clock %+v:\n%q\nwant the line %q", clock, got, want)
		}
	}
}

// The health table gives a reason once, on the row of the component that
// causes it: a row down for want of Docker or DynamoDB Local names that
// component. No row ends with padding.
func TestWriteHealthNamesTheCause(t *testing.T) {
	const noDocker = "Docker is disabled (--docker disabled)"
	const controlPlane = "control plane only: functions can be created, read and changed, but Invoke cannot run them: Docker is disabled"
	const turnedOff = "disabled by configuration"
	cases := []struct {
		name string
		h    *admin.Health
		rows string
	}{
		{
			name: "docker disabled",
			h: &admin.Health{
				Services: map[string]admin.ServiceStatus{
					"dynamodb": {Status: admin.Disabled, Reason: noDocker},
					"lambda":   {Status: admin.Degraded, Reason: controlPlane},
					"sqs":      {Status: admin.Enabled},
				},
				Docker:        admin.ServiceStatus{Status: admin.Disabled, Reason: noDocker},
				DynamoDBLocal: admin.ServiceStatus{Status: admin.Disabled, Reason: noDocker},
			},
			rows: "COMPONENT       STATUS    REASON\n" +
				"dynamodb        disabled  needs docker, which is disabled\n" +
				"lambda          degraded  " + controlPlane + "\n" +
				"sqs             enabled\n" +
				"docker          disabled  " + noDocker + "\n" +
				"dynamodb-local  disabled  needs docker, which is disabled\n",
		},
		{
			name: "dynamodb turned off",
			h: &admin.Health{
				Services: map[string]admin.ServiceStatus{
					"dynamodb": {Status: admin.Disabled, Reason: turnedOff},
				},
				Docker:        admin.ServiceStatus{Status: admin.Enabled},
				DynamoDBLocal: admin.ServiceStatus{Status: admin.Disabled, Reason: turnedOff},
			},
			rows: "COMPONENT       STATUS    REASON\n" +
				"dynamodb        disabled  needs dynamodb-local, which is disabled\n" +
				"docker          enabled\n" +
				"dynamodb-local  disabled  " + turnedOff + "\n",
		},
	}
	for _, c := range cases {
		c.h.Ready, c.h.Version, c.h.Uptime, c.h.Model = true, "test", admin.Duration(time.Minute), "pin"
		var b strings.Builder
		if err := writeHealth(&b, c.h); err != nil {
			t.Fatal(err)
		}
		want := "ready: understudy test, up 1m0s, models pin\n\n" + c.rows
		if b.String() != want {
			t.Errorf("%s: health table:\n%q\nwant\n%q", c.name, b.String(), want)
		}
	}
}
