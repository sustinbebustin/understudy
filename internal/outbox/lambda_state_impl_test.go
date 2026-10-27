package outbox

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// A loaded Lambda delivery goes through Enqueue's sender checks, so a
// snapshot whose sender was edited to a user, or lost, is refused naming
// the member at fault rather than delivered as a service principal.
func TestLoadRefusesALambdaDeliveryWithoutItsExecutionRole(t *testing.T) {
	c := codec()
	role := aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/worker"}
	queue := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "records"}
	fn := aws.ARN{Partition: "aws", Service: "lambda", Region: "us-east-1", Account: "000000000000", Resource: "function:worker"}
	at := clock.FromWire(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	st := newState(aws.Scope{})
	st.Entries["d-1"] = Entry{ID: "d-1", Accepted: at, NextAt: at, Request: delivery.Request{
		Source: fn, Mechanism: delivery.LambdaToSQS, Policy: delivery.None{},
		Target: delivery.SQSQueue{ARN: queue, Body: "{}"},
		Sender: &delivery.LambdaSender{Role: role, Carries: delivery.DestinationRecord},
	}}
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	sender := []byte(`,"sender":{"carries":"destination-record","role":"arn:aws:iam::000000000000:role/worker"}`)
	if !bytes.Contains(data, sender) {
		t.Fatalf("snapshot\n%s\ndoes not hold %s", data, sender)
	}
	for name, c2 := range map[string]struct {
		edited []byte
		field  string
	}{
		"a user as the sender": {bytes.Replace(data, []byte("role/worker"), []byte("user/alice"), 1), "Sender.Role"},
		"no sender":            {bytes.Replace(data, sender, nil, 1), "Sender"},
	} {
		back := newState(aws.Scope{})
		err := c.Unmarshal(c2.edited, &back)
		ee, ok := errors.AsType[*EntryError](err)
		if !ok || ee.ID != "d-1" || ee.Err.Field != c2.field {
			t.Errorf("%s: Unmarshal = %v, want an *EntryError for d-1's %s", name, err, c2.field)
		}
	}
}
