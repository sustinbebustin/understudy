package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
)

// A text journal line summarizes each kind's detail, outcome included, in
// the journal's wire form; only a detail the CLI cannot read is printed
// raw, cut, pointing at --output json.
func TestJournalLinesSummarizeEveryKind(t *testing.T) {
	const (
		topic = "arn:aws:sns:us-east-1:000000000000:t1"
		queue = "arn:aws:sqs:us-east-1:000000000000:q1"
		fn    = "arn:aws:lambda:us-east-1:000000000000:function:worker"
	)
	longText := strings.Repeat("the queue has no resource policy, which AWS would refuse; ", 4)
	cases := []struct {
		kind, detail, want string
	}{
		{
			"delivery", `{"from":"` + topic + `","to":"` + queue + `","mechanism":"sns-to-sqs","attempt":1,"outcome":{"kind":"delivered"}}`,
			"sns-to-sqs t1 -> q1 attempt 1 delivered",
		},
		{
			"delivery", `{"from":"` + topic + `","to":"` + queue + `","mechanism":"sns-to-sqs","attempt":3,"outcome":{"kind":"dropped","reason":"the queue does not exist"}}`,
			"sns-to-sqs t1 -> q1 attempt 3 dropped: the queue does not exist",
		},
		{
			"delivery", `{"from":"` + topic + `","to":"` + queue + `","mechanism":"sns-to-sqs","attempt":2,"outcome":{"kind":"retrying","next":"2026-01-01T00:00:20Z"}}`,
			"sns-to-sqs t1 -> q1 attempt 2 retrying, next attempt at 2026-01-01T00:00:20Z",
		},
		{
			"delivery", `{"from":"` + queue + `","to":"` + fn + `","mechanism":"sqs-to-lambda","attempt":5,"outcome":{"kind":"dead-lettered","dlq":"arn:aws:sqs:us-east-1:000000000000:dlq"}}`,
			"sqs-to-lambda q1 -> worker attempt 5 dead-lettered to dlq",
		},
		{
			"delivery", `{"from":"` + topic + `","to":"` + queue + `","mechanism":"sns-to-sqs","attempt":1,"outcome":{"kind":"filtered","rule":"filter-policy"}}`,
			"sns-to-sqs t1 -> q1 attempt 1 filtered by filter-policy",
		},
		{
			"invocation", `{"function":"` + fn + `","version":"$LATEST","cold":true,"duration":1500000000,"outcome":{"kind":"function-error","type":"Unhandled"}}`,
			"worker function-error Unhandled cold 1.5s",
		},
		{
			"invocation", `{"function":"` + fn + `","version":"3","duration":20000000,"outcome":{"kind":"exit-error","code":137}}`,
			"worker:3 exit-error code 137 20ms",
		},
		{"timer-fired", `{"timerKind":"sqs-visibility","timerId":"q1/m-1"}`, "sqs-visibility q1/m-1"},
		{"state-change", `{"resource":"` + fn + `","from":"Pending","to":"Active"}`, "worker Pending -> Active"},
		{"clock-changed", `{"from":{"kind":"running"},"to":{"kind":"frozen","at":"2026-01-01T00:00:00Z"}}`, "running -> frozen at 2026-01-01T00:00:00Z"},
		{"advisory", `{"advisory":"policy-would-deny","resource":"` + queue + `","text":"` + longText + `"}`, "policy-would-deny: " + longText},
		{"unclaimed", `{"method":"GET","host":"localhost:4566","path":"/x","reasons":["no service claims the path"]}`, "GET localhost:4566/x: no service claims the path"},
		{"gap", `{"missed":12}`, "missed 12 event(s)"},
		{
			"from-a-newer-server", `{"field":"` + strings.Repeat("x", 200) + `"}`,
			`{"field":"` + strings.Repeat("x", 147) + "... (--output json has the full detail)",
		},
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range cases {
		e := admin.JournalEvent{ID: 7, At: at, Kind: c.kind, Detail: json.RawMessage(c.detail)}
		line := eventLine(&e)
		if !strings.HasSuffix(line, " "+c.want) {
			t.Errorf("%s %s:\nline %q\nwant it to end %q", c.kind, c.detail, line, c.want)
		}
	}
}

// The time column has one width whatever the instant's nanoseconds, so
// the columns after it line up.
func TestJournalLinesAlign(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 13, 0, time.UTC)
	nanos := []time.Duration{0, 500000000, 69646416, 503010830}
	kindAt := make([]int, len(nanos))
	for i, ns := range nanos {
		e := admin.JournalEvent{ID: 1, At: base.Add(ns), Kind: "gap", Detail: json.RawMessage(`{"missed":1}`)}
		kindAt[i] = strings.Index(eventLine(&e), "gap")
	}
	for _, i := range kindAt[1:] {
		if i != kindAt[0] {
			t.Fatalf("the kind column starts at %v; want one column", kindAt)
		}
	}
}
