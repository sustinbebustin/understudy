package ruler

import (
	"encoding/json"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// capture is the part of a testdata/parity step file these tests read: the
// first exchange's request and response bodies.
type capture struct {
	Exchanges []struct {
		Request struct {
			Body string `json:"body"`
		} `json:"request"`
		Response struct {
			Body string `json:"body"`
		} `json:"response"`
	} `json:"exchanges"`
}

func readCapture(t *testing.T, step string) capture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "parity", filepath.FromSlash(step)))
	if err != nil {
		t.Fatalf("read capture %s: %v", step, err)
	}
	var c capture
	if err := json.Unmarshal(raw, &c); err != nil || len(c.Exchanges) == 0 {
		t.Fatalf("capture %s has no exchange (%v)", step, err)
	}
	return c
}

var xmlMessage = regexp.MustCompile(`(?s)<Message>(.*)</Message>`)

// subscribeReason is the error message after "Attributes Reason: " that a
// Subscribe capture's ErrorResponse holds.
func subscribeReason(t *testing.T, step, body string) string {
	t.Helper()
	m := xmlMessage.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("capture %s answered no error message", step)
	}
	reason, ok := strings.CutPrefix(html.UnescapeString(m[1]), "Invalid parameter: Attributes Reason: ")
	if !ok {
		t.Fatalf("capture %s message %q lacks the Subscribe prefix", step, m[1])
	}
	return reason
}

// subscribeAttributes returns the FilterPolicy and its scope that a
// Subscribe capture sent.
func subscribeAttributes(t *testing.T, step, body string) (string, SNSScope) {
	t.Helper()
	form, err := url.ParseQuery(body)
	if err != nil {
		t.Fatalf("capture %s request body: %v", step, err)
	}
	attrs := map[string]string{}
	for i := 1; form.Has("Attributes.entry." + strconv.Itoa(i) + ".key"); i++ {
		n := "Attributes.entry." + strconv.Itoa(i)
		attrs[form.Get(n+".key")] = form.Get(n + ".value")
	}
	policy, ok := attrs["FilterPolicy"]
	if !ok {
		t.Fatalf("capture %s sent no FilterPolicy", step)
	}
	if attrs["FilterPolicyScope"] == "MessageBody" {
		return policy, MessageBody
	}
	return policy, MessageAttributes
}

// TestSNSRecordedReasons compiles the filter policy of each sns/R09
// Subscribe capture that AWS refused and compares Message with the refusal
// byte for byte, Jackson's location line included (matching-engines.md 8
// H5, H7, H10, H11; sns/R09 notes).
func TestSNSRecordedReasons(t *testing.T) {
	t.Parallel()
	for _, step := range []string{
		"H5-07-attr-subscribe", "H5-07-body-subscribe",
		"H5-08-attr-subscribe", "H5-08-body-subscribe",
		"H7b-08-subscribe",
		"H10-01-subscribe", "H10-02-subscribe", "H10-03-subscribe",
		"H10-04-subscribe", "H10-05-subscribe", "H10-06-subscribe",
		"H11-11-single-subscribe", "H11-4-wildcards-subscribe", "H11-5-two-wildcard-subscribe",
		"T13-subscribe",
	} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			c := readCapture(t, "sns/R09/"+step+".json")
			policy, scope := subscribeAttributes(t, step, c.Exchanges[0].Request.Body)
			want := subscribeReason(t, step, c.Exchanges[0].Response.Body)
			_, err := Compile([]byte(policy), SNS{Scope: scope})
			if err == nil {
				t.Fatalf("Compile accepted %s; AWS answered %q", policy, want)
			}
			if got := err.Message(); got != want {
				t.Fatalf("Compile(%s).Message() =\n%q\nwant\n%q", policy, got, want)
			}
		})
	}
}

// TestSNSPolicySizeText: sns/R09/T16-subscribe.json omits its 273862-byte
// request, a policy over 262144 bytes; AWS's answer names the byte limit.
func TestSNSPolicySizeText(t *testing.T) {
	t.Parallel()
	c := readCapture(t, "sns/R09/T16-subscribe.json")
	want := subscribeReason(t, "T16-subscribe", c.Exchanges[0].Response.Body)
	policy := `{"k":["` + strings.Repeat("v", snsMaxBytes) + `"]}`
	_, err := Compile([]byte(policy), SNS{Scope: MessageAttributes})
	if err == nil || err.Message() != want {
		t.Fatalf("Compile of a %d-byte policy = %v, want %q", len(policy), err, want)
	}
}

// TestEventBridgeRecordedReasons compiles the event pattern of each
// events/R14 capture that AWS refused and compares Message with the
// InvalidEventPatternException message byte for byte (matching-engines.md
// 8 H10, H12). EventBridge reads a malformed "$or" again with "$or" as an
// ordinary key, as its service profile is set.
func TestEventBridgeRecordedReasons(t *testing.T) {
	t.Parallel()
	profile := EventBridge{Options: Options{MalformedOr: MalformedOrOrdinaryKey}}
	for _, step := range []string{"H10-01", "H10-02", "H10-03", "H10-04", "H10-05", "H10-06", "H12-02-7x11x13"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			c := readCapture(t, "events/R14/"+step+".json")
			var req struct{ EventPattern string }
			var resp struct{ Message string }
			if err := json.Unmarshal([]byte(c.Exchanges[0].Request.Body), &req); err != nil {
				t.Fatalf("request body: %v", err)
			}
			if err := json.Unmarshal([]byte(c.Exchanges[0].Response.Body), &resp); err != nil {
				t.Fatalf("response body: %v", err)
			}
			_, err := Compile([]byte(req.EventPattern), profile)
			if err == nil {
				t.Fatalf("Compile accepted %s; AWS answered %q", req.EventPattern, resp.Message)
			}
			if got := err.Message(); got != resp.Message {
				t.Fatalf("Compile(%s).Message() =\n%q\nwant\n%q", req.EventPattern, got, resp.Message)
			}
		})
	}
}
