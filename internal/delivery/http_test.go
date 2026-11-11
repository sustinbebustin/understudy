package delivery_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// The three kinds are named by the x-amz-sns-message-type values AWS sends
// (sns-http-delivery.md 3.2, aws-doc: "SubscriptionConfirmation",
// "Notification", "UnsubscribeConfirmation"); snapshots persist them by
// name.
var httpKindNames = []struct {
	k    delivery.HTTPKind
	name string
}{
	{delivery.HTTPNotification, "Notification"},
	{delivery.HTTPSubscriptionConfirmation, "SubscriptionConfirmation"},
	{delivery.HTTPUnsubscribeConfirmation, "UnsubscribeConfirmation"},
}

func TestHTTPKindNamesAreTheMessageTypes(t *testing.T) {
	for _, c := range httpKindNames {
		if got := c.k.String(); got != c.name {
			t.Errorf("HTTPKind %d String() = %q, want %q", c.k, got, c.name)
		}
		if got, err := c.k.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("HTTPKind %d MarshalText() = %q, %v; want %q", c.k, got, err, c.name)
		}
		if got, err := delivery.ParseHTTPKind(c.name); err != nil || got != c.k {
			t.Errorf("ParseHTTPKind(%q) = %v, %v; want %v", c.name, got, err, c.k)
		}
		var back delivery.HTTPKind
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.k {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.k)
		}
	}
	for _, undeclared := range []delivery.HTTPKind{0, delivery.HTTPUnsubscribeConfirmation + 1, 255} {
		if got, want := undeclared.String(), "HTTPKind("+strconv.Itoa(int(undeclared))+")"; got != want {
			t.Errorf("String() of %d = %q, want %q", undeclared, got, want)
		}
		if _, err := undeclared.MarshalText(); !isError[*delivery.HTTPKindError](err) {
			t.Errorf("MarshalText of %d error = %v, want *HTTPKindError", undeclared, err)
		}
	}
}

// Parsing is exact: any other text, including another casing of a declared
// name, is refused with the list of known names.
func TestParseHTTPKindRefusesEveryOtherText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.OneOf(
			rapid.String(),
			rapid.SampledFrom([]string{"notification", "NOTIFICATION", "subscriptionconfirmation", " Notification", "Notification ", ""}),
		).Draw(t, "text")
		for _, c := range httpKindNames {
			if c.name == text {
				return
			}
		}
		_, err := delivery.ParseHTTPKind(text)
		ke, ok := errors.AsType[*delivery.HTTPKindError](err)
		if !ok {
			t.Fatalf("ParseHTTPKind(%q) error = %v, want *HTTPKindError", text, err)
		}
		// The text is quoted as Go quotes it, like MechanismError's.
		want := strconv.Quote(text) + ` names no HTTP delivery kind; known names are Notification, SubscriptionConfirmation and UnsubscribeConfirmation`
		if ke.Text != text || err.Error() != want {
			t.Fatalf("error = %q (Text %q)\nwant    %q", err, ke.Text, want)
		}
		var k delivery.HTTPKind
		if err := k.UnmarshalText([]byte(text)); !isError[*delivery.HTTPKindError](err) {
			t.Fatalf("UnmarshalText(%q) error = %v, want *HTTPKindError", text, err)
		}
	})
}

// The backoff functions are the healthyRetryPolicy backoffFunction values
// (sns-http-delivery.md 6.3, aws-doc: linear, arithmetic, geometric and
// exponential).
var backoffNames = []struct {
	f    delivery.BackoffFunction
	name string
}{
	{delivery.BackoffLinear, "linear"},
	{delivery.BackoffArithmetic, "arithmetic"},
	{delivery.BackoffGeometric, "geometric"},
	{delivery.BackoffExponential, "exponential"},
}

func TestBackoffFunctionNames(t *testing.T) {
	for _, c := range backoffNames {
		if got := c.f.String(); got != c.name {
			t.Errorf("BackoffFunction %d String() = %q, want %q", c.f, got, c.name)
		}
		if got, err := c.f.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("BackoffFunction %d MarshalText() = %q, %v; want %q", c.f, got, err, c.name)
		}
		if got, err := delivery.ParseBackoffFunction(c.name); err != nil || got != c.f {
			t.Errorf("ParseBackoffFunction(%q) = %v, %v; want %v", c.name, got, err, c.f)
		}
		var back delivery.BackoffFunction
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.f {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.f)
		}
	}
	for _, undeclared := range []delivery.BackoffFunction{0, delivery.BackoffExponential + 1} {
		if got, want := undeclared.String(), "BackoffFunction("+strconv.Itoa(int(undeclared))+")"; got != want {
			t.Errorf("String() of %d = %q, want %q", undeclared, got, want)
		}
		if _, err := undeclared.MarshalText(); !isError[*delivery.BackoffFunctionError](err) {
			t.Errorf("MarshalText of %d error = %v, want *BackoffFunctionError", undeclared, err)
		}
	}
}

// ParseBackoffFunction takes the lower-case names only; the delivery policy
// parser in the SNS service compares them case-insensitively before it
// builds an SNSHTTP.
func TestParseBackoffFunctionRefusesEveryOtherText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.OneOf(
			rapid.String(),
			rapid.SampledFrom([]string{"Linear", "EXPONENTIAL", "linear ", "", "Geometric"}),
		).Draw(t, "text")
		for _, c := range backoffNames {
			if c.name == text {
				return
			}
		}
		_, err := delivery.ParseBackoffFunction(text)
		fe, ok := errors.AsType[*delivery.BackoffFunctionError](err)
		if !ok {
			t.Fatalf("ParseBackoffFunction(%q) error = %v, want *BackoffFunctionError", text, err)
		}
		want := strconv.Quote(text) + ` names no backoff function; known names are linear, arithmetic, geometric and exponential`
		if fe.Text != text || err.Error() != want {
			t.Fatalf("error = %q (Text %q)\nwant    %q", err, fe.Text, want)
		}
	})
}

// The persisted form of both enums is the name, inside JSON too.
func TestHTTPEnumsJSON(t *testing.T) {
	type persisted struct {
		Kind     delivery.HTTPKind
		Function delivery.BackoffFunction
	}
	data, err := json.Marshal(persisted{Kind: delivery.HTTPUnsubscribeConfirmation, Function: delivery.BackoffGeometric})
	if want := `{"Kind":"UnsubscribeConfirmation","Function":"geometric"}`; err != nil || string(data) != want {
		t.Fatalf("Marshal = %s, %v; want %s", data, err, want)
	}
	var out persisted
	if err := json.Unmarshal(data, &out); err != nil || out.Kind != delivery.HTTPUnsubscribeConfirmation || out.Function != delivery.BackoffGeometric {
		t.Fatalf("Unmarshal = %+v, %v", out, err)
	}
	if _, err := json.Marshal(persisted{Function: delivery.BackoffLinear}); err == nil {
		t.Error("Marshal of the zero HTTPKind succeeded")
	}
}

// The default healthyRetryPolicy is three retries 20 s apart, so four
// attempts: "up to three retries with a delay between failed attempts set
// at 20 seconds" (sns-http-delivery.md 6.3, aws-doc; attempts = 1 +
// numRetries, 6.3). Its three retries are backoff retries, the phase
// between minDelayTarget 20 and maxDelayTarget 20 (6.2, the system
// default policy).
func TestDefaultPolicyRetriesThreeTimesTwentySecondsApart(t *testing.T) {
	p := delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
	if got := p.Retries(); got != 3 {
		t.Fatalf("Retries() = %d, want 3", got)
	}
	for retry := 1; retry <= 3; retry++ {
		if got, ok := p.Delay(retry); !ok || got != 20*time.Second {
			t.Errorf("Delay(%d) = %v, %v; want 20s", retry, got, ok)
		}
	}
	for _, retry := range []int{-1, 0, 4} {
		if got, ok := p.Delay(retry); ok {
			t.Errorf("Delay(%d) = %v, true; want no retry %d", retry, got, retry)
		}
	}
}

// Every phase holds its count of retries in order: numNoDelayRetries "with
// no delay", then numMinDelayRetries at minDelayTarget, the backoff phase,
// then numMaxDelayRetries at maxDelayTarget (sns-http-delivery.md 6.3,
// aws-doc).
func TestDelayPhasesInOrder(t *testing.T) {
	p := delivery.SNSHTTP{
		NoDelay: 2, MinDelay: 3, Backoff: 0, MaxDelay: 2,
		MinDelayTarget: 5 * time.Second, MaxDelayTarget: time.Minute, BackoffFunction: delivery.BackoffExponential,
	}
	want := []time.Duration{0, 0, 5 * time.Second, 5 * time.Second, 5 * time.Second, time.Minute, time.Minute}
	if got := p.Retries(); got != len(want) {
		t.Fatalf("Retries() = %d, want %d", got, len(want))
	}
	for i, w := range want {
		if got, ok := p.Delay(i + 1); !ok || got != w {
			t.Errorf("Delay(%d) = %v, %v; want %v", i+1, got, ok, w)
		}
	}
	if got, ok := p.Delay(len(want) + 1); ok {
		t.Errorf("Delay(%d) = %v, true; want none past the last retry", len(want)+1, got)
	}
}

// genSNSHTTP draws a policy inside the documented bounds: phase counts at
// least 0 and numRetries at most 100, minDelayTarget from 1 s up to
// maxDelayTarget, maxDelayTarget at most 3600 s (sns-http-delivery.md 6.7,
// aws-doc).
func genSNSHTTP() *rapid.Generator[delivery.SNSHTTP] {
	return rapid.Custom(func(t *rapid.T) delivery.SNSHTTP {
		minS := rapid.IntRange(1, 3600).Draw(t, "minDelayTarget")
		maxS := rapid.IntRange(minS, 3600).Draw(t, "maxDelayTarget")
		return delivery.SNSHTTP{
			NoDelay:         rapid.IntRange(0, 25).Draw(t, "noDelay"),
			MinDelay:        rapid.IntRange(0, 25).Draw(t, "minDelay"),
			Backoff:         rapid.IntRange(0, 25).Draw(t, "backoff"),
			MaxDelay:        rapid.IntRange(0, 25).Draw(t, "maxDelay"),
			MinDelayTarget:  time.Duration(minS) * time.Second,
			MaxDelayTarget:  time.Duration(maxS) * time.Second,
			BackoffFunction: rapid.SampledFrom([]delivery.BackoffFunction{delivery.BackoffLinear, delivery.BackoffArithmetic, delivery.BackoffGeometric, delivery.BackoffExponential}).Draw(t, "function"),
		}
	})
}

// Over every policy in bounds: numRetries is "the total number of retries,
// including immediate, pre-backoff, backoff, and post-backoff retries"; each
// phase delays as documented; in the backoff phase minDelayTarget "sets the
// delay for the first retry attempt" and maxDelayTarget "the delay for the
// final retry attempt", and no curve the page describes leaves that range
// or steps back (sns-http-delivery.md 6.3, aws-doc). The single-retry
// backoff phase and the steps between are sns.http.backoff-curves, which
// the SNS service's tests check against the switch.
func TestDelayFollowsThePhasesForEveryPolicy(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genSNSHTTP().Draw(t, "policy")
		total := p.NoDelay + p.MinDelay + p.Backoff + p.MaxDelay
		if got := p.Retries(); got != total {
			t.Fatalf("Retries() = %d, want the phase sum %d", got, total)
		}
		a, b, c := p.NoDelay, p.NoDelay+p.MinDelay, p.NoDelay+p.MinDelay+p.Backoff
		var prev time.Duration
		for retry := 1; retry <= total; retry++ {
			got, ok := p.Delay(retry)
			if !ok {
				t.Fatalf("Delay(%d) of %d retries refused", retry, total)
			}
			switch {
			case retry <= a:
				if got != 0 {
					t.Fatalf("no-delay retry %d waits %v", retry, got)
				}
			case retry <= b:
				if got != p.MinDelayTarget {
					t.Fatalf("min-delay retry %d waits %v, want %v", retry, got, p.MinDelayTarget)
				}
			case retry <= c:
				if got < p.MinDelayTarget || got > p.MaxDelayTarget {
					t.Fatalf("backoff retry %d waits %v, outside [%v, %v]", retry, got, p.MinDelayTarget, p.MaxDelayTarget)
				}
				if retry > b+1 && got < prev {
					t.Fatalf("backoff retry %d waits %v, less than the retry before it, %v", retry, got, prev)
				}
				if p.Backoff >= 2 && retry == b+1 && got != p.MinDelayTarget {
					t.Fatalf("first backoff retry waits %v, want minDelayTarget %v", got, p.MinDelayTarget)
				}
				if p.Backoff >= 2 && retry == c && got != p.MaxDelayTarget {
					t.Fatalf("last backoff retry waits %v, want maxDelayTarget %v", got, p.MaxDelayTarget)
				}
			default:
				if got != p.MaxDelayTarget {
					t.Fatalf("max-delay retry %d waits %v, want %v", retry, got, p.MaxDelayTarget)
				}
			}
			prev = got
			if again, _ := p.Delay(retry); again != got {
				t.Fatalf("Delay(%d) = %v, then %v", retry, got, again)
			}
		}
		for _, retry := range []int{0, -1, total + 1} {
			if got, ok := p.Delay(retry); ok {
				t.Fatalf("Delay(%d) of %d retries = %v, true; want no such retry", retry, total, got)
			}
		}
	})
}

// An SNS http delivery is a policy of its own, beside the managed SQS and
// Lambda schedule.
func TestSNSHTTPIsARetryPolicy(t *testing.T) {
	var p delivery.RetryPolicy = delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second}
	if _, ok := p.(delivery.SNSManaged); ok {
		t.Error("SNSHTTP is SNSManaged")
	}
}

func isError[E error](err error) bool {
	_, ok := errors.AsType[E](err)
	return ok
}
