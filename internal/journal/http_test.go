package journal

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The classes of an HTTP attempt's answer under sns.http.status-classes
// (sns-http-delivery.md 6.1), and egress's refusal (7.3), by their wire
// names.
var httpClassNames = []struct {
	c    HTTPClass
	name string
}{
	{HTTPSuccess, "success"},
	{HTTPRetryable, "retryable"},
	{HTTPPermanent, "permanent"},
	{HTTPRefused, "refused"},
}

func TestHTTPClassNames(t *testing.T) {
	for _, c := range httpClassNames {
		if got := c.c.String(); got != c.name {
			t.Errorf("HTTPClass %d String() = %q, want %q", c.c, got, c.name)
		}
		if got, err := c.c.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("HTTPClass %d MarshalText() = %q, %v; want %q", c.c, got, err, c.name)
		}
		if got, err := ParseHTTPClass(c.name); err != nil || got != c.c {
			t.Errorf("ParseHTTPClass(%q) = %v, %v; want %v", c.name, got, err, c.c)
		}
		var back HTTPClass
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.c {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.c)
		}
	}
	for _, undeclared := range []HTTPClass{0, HTTPRefused + 1} {
		if got, want := undeclared.String(), "HTTPClass("+strconv.Itoa(int(undeclared))+")"; got != want {
			t.Errorf("String() of %d = %q, want %q", undeclared, got, want)
		}
		if _, err := undeclared.MarshalText(); err == nil {
			t.Errorf("MarshalText of %d succeeded", undeclared)
		}
	}
}

func TestParseHTTPClassRefusesEveryOtherText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.OneOf(rapid.String(), rapid.SampledFrom([]string{"Success", "RETRYABLE", "", "refused "})).Draw(t, "text")
		for _, c := range httpClassNames {
			if c.name == text {
				return
			}
		}
		_, err := ParseHTTPClass(text)
		ce, ok := errors.AsType[*HTTPClassError](err)
		if !ok {
			t.Fatalf("ParseHTTPClass(%q) error = %v, want *HTTPClassError", text, err)
		}
		// Quoted as Go quotes it, like the package's other enum errors.
		want := strconv.Quote(text) + " names no HTTP class; known names are success, retryable, permanent and refused"
		if ce.Text != text || err.Error() != want {
			t.Fatalf("error = %q (Text %q)\nwant    %q", err, ce.Text, want)
		}
	})
}

func httpDelivery(exchange *HTTPExchange, outcome DeliveryOutcome) Delivery {
	return Delivery{
		From: arn("sns", "events"), To: aws.ARN{}, Endpoint: "127.0.0.1:8080", Mechanism: delivery.SNSToHTTP,
		Attempt: 2, Outcome: outcome, HTTP: exchange,
	}
}

// encodeDetailLine encodes an event of detail d and returns its NDJSON
// line, newline included.
func encodeDetailLine(t *testing.T, d Detail) string {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, Event{ID: 9, Cause: ref.Cause{Parent: 3, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: d}); err != nil {
		t.Fatalf("EncodeNDJSON: %v", err)
	}
	return buf.String()
}

// An HTTP attempt's line carries its exchange: the status, the class and
// the wall duration in nanoseconds, as invocation.duration is written; the
// line decodes back to the same event.
func TestDeliveryLineCarriesTheHTTPExchange(t *testing.T) {
	cases := []struct {
		name     string
		exchange *HTTPExchange
		outcome  DeliveryOutcome
		want     string
	}{
		{
			"a retryable answer",
			&HTTPExchange{Status: 503, Class: HTTPRetryable, Duration: 1234567},
			Retrying{Next: epoch.Add(20 * time.Second)},
			`"http":{"status":503,"class":"retryable","duration":1234567}`,
		},
		{"a success", &HTTPExchange{Status: 204, Class: HTTPSuccess, Duration: time.Millisecond}, Delivered{}, `"http":{"status":204,"class":"success","duration":1000000}`},
		// No answer came and no time was spent: egress refused the host.
		{"a refusal", &HTTPExchange{Class: HTTPRefused}, Refused{Reason: "not in sns.httpDelivery.allow"}, `"http":{"class":"refused"}`},
		// An injected status has no duration.
		{"an injected status", &HTTPExchange{Status: 500, Class: HTTPRetryable}, Retrying{Next: epoch.Add(time.Minute)}, `"http":{"status":500,"class":"retryable"}`},
		// A timed-out exchange has a duration and no status.
		{"a timeout", &HTTPExchange{Class: HTTPRetryable, Duration: 15 * time.Second}, Retrying{Next: epoch.Add(time.Minute)}, `"http":{"class":"retryable","duration":15000000000}`},
	}
	for _, c := range cases {
		d := httpDelivery(c.exchange, c.outcome)
		line := encodeDetailLine(t, d)
		if !strings.Contains(line, c.want) {
			t.Errorf("%s: line\n%s\ndoes not contain %s", c.name, line, c.want)
		}
		e := Event{ID: 9, Cause: ref.Cause{Parent: 3, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: d}
		if got := roundTrip(t, e); len(got) != 1 || !reflect.DeepEqual(got[0], e) {
			t.Errorf("%s: round trip\n got %#v\nwant %#v", c.name, got, e)
		}
	}
}

// A delivery without an exchange (another target, a decision before any
// attempt, a dropped attempt) writes no "http" member, so lines of earlier
// builds read the same.
func TestDeliveryLineWithoutAnExchangeHasNoHTTPMember(t *testing.T) {
	for _, d := range []Delivery{
		{From: arn("sns", "events"), To: arn("sqs", "orders"), Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{}},
		httpDelivery(nil, Filtered{Rule: `{"store":["north"]}`}),
	} {
		if line := encodeDetailLine(t, d); strings.Contains(line, `"http"`) {
			t.Errorf("a delivery with no exchange wrote one:\n%s", line)
		}
	}
}

// An unknown class is a decode error for that line; the lines around it
// still decode.
func TestDecodeRefusesAnUnknownHTTPClass(t *testing.T) {
	good := strings.TrimSpace(encodeDetailLine(t, httpDelivery(&HTTPExchange{Status: 503, Class: HTTPRetryable}, Retrying{Next: epoch.Add(time.Minute)})))
	bad := strings.Replace(good, `"class":"retryable"`, `"class":"sometimes"`, 1)
	if bad == good {
		t.Fatalf("line %s has no class to replace", good)
	}
	var decoded int
	var errs []*DecodeError
	for _, err := range DecodeNDJSON(strings.NewReader(good + "\n" + bad + "\n" + good + "\n")) {
		if err == nil {
			decoded++
			continue
		}
		de, ok := errors.AsType[*DecodeError](err)
		if !ok {
			t.Fatalf("error %v is not a *DecodeError", err)
		}
		errs = append(errs, de)
	}
	if decoded != 2 || len(errs) != 1 || errs[0].Line != 2 {
		t.Fatalf("decoded %d events and errors %v; want 2 events and one *DecodeError on line 2", decoded, errs)
	}
	if !strings.Contains(errs[0].Error(), "sometimes") {
		t.Errorf("error %q does not name the unknown class", errs[0])
	}
}
