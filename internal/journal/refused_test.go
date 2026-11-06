package journal

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// A refused delivery is journaled as Refused{Reason}
// (sns-http-delivery.md 7.3) and an HTTP delivery names its endpoint's
// host:port; both survive a journal file, and a delivery without an
// endpoint is written as before, so files of earlier builds read the same.
func TestNDJSONRoundTripsRefusedHTTPDeliveries(t *testing.T) {
	refused := Delivery{
		From: arn("sns", "events"), To: aws.ARN{}, Endpoint: "127.0.0.1:8080", Mechanism: delivery.SNSToSQS, Attempt: 1,
		Outcome: Refused{Reason: `egress refused 127.0.0.1:8080: add "127.0.0.1:8080" to sns.httpDelivery.allow`},
	}
	delivered := Delivery{From: arn("sns", "events"), To: arn("sqs", "orders"), Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{}}
	for _, d := range []Delivery{refused, delivered} {
		e := Event{ID: 7, Cause: ref.Cause{Parent: 3, Trace: 1}, At: epoch, Wall: wall0, Scope: scope(), Detail: d}
		got := roundTrip(t, e)
		if len(got) != 1 || !reflect.DeepEqual(got[0], e) {
			t.Errorf("round trip of %+v\n got %#v\nwant %#v", d, got, e)
		}
	}

	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, Event{ID: 1, At: epoch, Wall: wall0, Scope: scope(), Detail: refused}); err != nil {
		t.Fatalf("EncodeNDJSON: %v", err)
	}
	line := buf.String()
	for _, want := range []string{`"endpoint":"127.0.0.1:8080"`, `"outcome":{"kind":"refused","reason":`} {
		if !strings.Contains(line, want) {
			t.Errorf("line\n%s\ndoes not contain %s", line, want)
		}
	}
	buf.Reset()
	if err := EncodeNDJSON(&buf, Event{ID: 1, At: epoch, Wall: wall0, Scope: scope(), Detail: delivered}); err != nil {
		t.Fatalf("EncodeNDJSON: %v", err)
	}
	if strings.Contains(buf.String(), `"endpoint"`) {
		t.Errorf("a delivery with no endpoint wrote one:\n%s", buf.String())
	}
}
