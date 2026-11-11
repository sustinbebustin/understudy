package journal

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// A redrive's NDJSON detail names both queues, the moved messages and its
// reason, a nested sum tagged by "kind" like the journal's other nested
// sums (ADR 0007, amendment 2026-10-13: Redrive{From, To, Messages,
// Reason}, Reason MaxReceiveCount{Count} | MoveTask{TaskHandle}).
func TestRedriveWireForm(t *testing.T) {
	src, dlq := arn("sqs", "orders"), arn("sqs", "orders-dlq")
	cases := []struct {
		detail Redrive
		want   string
	}{
		{
			Redrive{From: src, To: dlq, Messages: []ids.MessageID{"m-1", "m-2"}, Reason: MaxReceiveCount{Count: 3}},
			`{"from":"arn:aws:sqs:us-east-1:000000000000:orders","to":"arn:aws:sqs:us-east-1:000000000000:orders-dlq","messages":["m-1","m-2"],"reason":{"kind":"max-receive-count","count":3}}`,
		},
		{
			Redrive{From: dlq, To: src, Messages: []ids.MessageID{"m-1"}, Reason: MoveTask{TaskHandle: "eyJ0YXNrSWQiOiJ0In0="}},
			`{"from":"arn:aws:sqs:us-east-1:000000000000:orders-dlq","to":"arn:aws:sqs:us-east-1:000000000000:orders","messages":["m-1"],"reason":{"kind":"move-task","taskHandle":"eyJ0YXNrSWQiOiJ0In0="}}`,
		},
	}
	for _, c := range cases {
		e := Event{ID: 9, Cause: ref.Cause{Parent: 8, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: c.detail}
		line := encodeLine(t, e)
		var got struct {
			Kind   string          `json:"kind"`
			Detail json.RawMessage `json:"detail"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %s: %v", line, err)
		}
		if got.Kind != "redrive" || !sameJSON(t, got.Detail, []byte(c.want)) {
			t.Errorf("%#v encodes as kind %q detail %s, want kind redrive detail %s", c.detail, got.Kind, got.Detail, c.want)
		}
		back, err := decodeOne(t, line)
		if err != nil || !reflect.DeepEqual(back, e) {
			t.Errorf("%#v round trip = %#v, %v; want the event back", c.detail, back, err)
		}
	}
}

// The decoder reads a redrive's reason strictly: a reason kind it does not
// know, a missing reason or a member the other reason owns is a malformed
// line, never a silently different event.
func TestRedriveDecodeIsStrict(t *testing.T) {
	base := encodeLine(t, Event{
		ID: 9, Cause: ref.Cause{Parent: 8, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(),
		Detail: Redrive{From: arn("sqs", "orders"), To: arn("sqs", "orders-dlq"), Reason: MaxReceiveCount{Count: 1}},
	})
	for _, detail := range []string{
		`{"from":"","to":""}`,
		`{"from":"","to":"","reason":{}}`,
		`{"from":"","to":"","reason":{"kind":"expired"}}`,
		`{"from":"","to":"","reason":{"kind":"max-receive-count","count":1,"taskHandle":"h"}}`,
		`{"from":"","to":"","reason":{"kind":"move-task","taskHandle":"h","count":1}}`,
		`{"from":"","to":"","reason":{"kind":"move-task","taskHandle":"h"},"mechanism":"sns-to-sqs"}`,
	} {
		line := withDetail(t, base, "redrive", detail)
		if e, err := decodeOne(t, line); !errorIs[*DecodeError](err) {
			t.Errorf("decoding redrive detail %s = %s, %v; want a *DecodeError", detail, detailName(e.Detail), err)
		}
	}
}

// A redrive is about both queues, so a resource or service filter on either
// selects it, as does the kind filter; it has no result (doc.go: filters).
func TestRedriveIsFoundByEitherQueue(t *testing.T) {
	j := newTestJournal(t, Options{})
	id := mustAppend(t, j, Event{Scope: scope(), Detail: Redrive{
		From: arn("sqs", "orders"), To: arn("sqs", "orders-dlq"), Messages: []ids.MessageID{"m-1"}, Reason: MaxReceiveCount{Count: 2},
	}})
	for _, f := range []Filter{
		{Kind: KindRedrive},
		{ResourceGlob: arn("sqs", "orders").String()},
		{ResourceGlob: arn("sqs", "orders-dlq").String()},
		{Service: Service{ID: "sqs", ARNNamespace: "sqs"}},
	} {
		if got := eventIDs(j.Query(f).Events); !slices.Equal(got, []ref.EventID{id}) {
			t.Errorf("Query(%+v) = %v, want [%d]", f, got, id)
		}
	}
	for r := ResultOK; r <= ResultPending; r++ {
		if got := j.Query(Filter{Outcome: r}).Events; len(got) != 0 {
			t.Errorf("Query(outcome %v) = %v, want nothing: a redrive has no result", r, eventIDs(got))
		}
	}
}

// Append refuses a redrive without a reason: it has no wire form.
func TestAppendRefusesARedriveWithoutReason(t *testing.T) {
	j := newTestJournal(t, Options{})
	id, err := j.Append(Event{Detail: Redrive{From: arn("sqs", "orders"), To: arn("sqs", "orders-dlq")}})
	if recErr, ok := errors.AsType[*RecordError](err); !ok || recErr.Reason != NoWireForm || recErr.Kind != KindRedrive || id != 0 {
		t.Fatalf("Append(Redrive without reason) = %d, %v; want a *RecordError for redrive with reason NoWireForm", id, err)
	}
}

// The memory estimate charges a redrive for its message ids and task
// handle, so a step that moves a whole queue counts as more than one that
// moves a message.
func TestEstimateChargesARedrivesMessages(t *testing.T) {
	long := strings.Repeat("x", 1000)
	for _, tc := range []struct{ short, long Redrive }{
		{Redrive{Reason: MaxReceiveCount{Count: 1}}, Redrive{Messages: []ids.MessageID{ids.MessageID(long)}, Reason: MaxReceiveCount{Count: 1}}},
		{Redrive{Reason: MoveTask{}}, Redrive{Reason: MoveTask{TaskHandle: long}}},
	} {
		short, grown := Estimate(Event{Detail: tc.short}), Estimate(Event{Detail: tc.long})
		if grown-short < len(long) {
			t.Errorf("Estimate of %#v grows by %d bytes for %d bytes of text, want at least the text", tc.long.Reason, grown-short, len(long))
		}
	}
}
