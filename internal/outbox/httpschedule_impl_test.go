package outbox

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// An HTTP attempt's retry delay runs from its end (sns-http-delivery.md
// 6.3, inferred): a frozen clock stood still while it ran, a running one
// moved with wall time, a scaled one Factor times as fast.
func TestAttemptEnd(t *testing.T) {
	due := clock.FromWire(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	cases := []struct {
		mode clock.Mode
		want time.Duration
	}{
		{clock.Frozen{At: due}, 0},
		{clock.Running{}, 15 * time.Second},
		{clock.Scaled{Factor: 4}, time.Minute},
	}
	for _, c := range cases {
		if got := attemptEnd(c.mode, due, 15*time.Second); got != due.Add(c.want) {
			t.Errorf("attemptEnd(%T, due, 15s) = due%+v, want due+%v", c.mode, got.Sub(due), c.want)
		}
	}
}

// A loaded sns-http policy record lacking a member is refused, naming it.
func TestSNSHTTPPolicyRecordNamesAMissingMember(t *testing.T) {
	c, err := unions()
	if err != nil {
		t.Fatal(err)
	}
	p := delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
	hook, err := url.Parse("http://127.0.0.1:8080/hook")
	if err != nil {
		t.Fatal(err)
	}
	target := targetRecordOf(delivery.HTTPEndpoint{URL: *hook, Kind: delivery.HTTPNotification, Body: []byte("b")})
	data, err := c.Marshal(&requestRecord{Source: "arn:aws:sns:us-east-1:000000000000:orders", Mechanism: delivery.SNSToHTTP, Target: target, Policy: policyRecordOf(p)})
	if err != nil {
		t.Fatal(err)
	}
	missing := strings.Replace(string(data), `"maxDelay":0,`, ``, 1)
	if missing == string(data) {
		t.Fatalf("record %s has no maxDelay to remove", data)
	}
	var back requestRecord
	if err := c.Unmarshal([]byte(missing), &back); err != nil {
		t.Fatalf("Unmarshal(%s): %v", missing, err)
	}
	_, rerr := requestOf(&back)
	if rerr == nil || rerr.Field != "Policy" || !strings.HasSuffix(rerr.Reason, "member maxDelay") {
		t.Errorf("requestOf(record without maxDelay) = %v, want a Policy refusal naming maxDelay", rerr)
	}
}

// An edited record's delay far beyond time.Duration saturates rather than
// wrapping into the accepted range, so the checks refuse it.
func TestMillisSaturates(t *testing.T) {
	for _, ms := range []int64{1 << 62, -(1 << 62)} {
		p := delivery.SNSHTTP{Backoff: 1, MinDelayTarget: time.Second, MaxDelayTarget: millis(ms), BackoffFunction: delivery.BackoffLinear}
		if err := checkSNSHTTP(p); err == nil {
			t.Errorf("maxDelayTargetMillis %d accepted as %v", ms, p.MaxDelayTarget)
		}
	}
	if got := millis(20000); got != 20*time.Second {
		t.Errorf("millis(20000) = %v, want 20s", got)
	}
}
