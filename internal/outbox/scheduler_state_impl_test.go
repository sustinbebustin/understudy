package outbox

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// schedulerEntry is a pending Scheduler delivery to a FIFO queue, as the
// fire write hands it over.
func schedulerEntry() Entry {
	at := clock.FromWire(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC))
	schedule := aws.ARN{Partition: "aws", Service: "scheduler", Region: "us-east-1", Account: "000000000000", Resource: "schedule/default/tick"}
	queue := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "orders.fifo"}
	role := aws.ARN{Partition: "aws", Service: "iam", Account: "000000000000", Resource: "role/app/sch-role"}
	return Entry{ID: "d-1", Accepted: at, NextAt: at, Attempt: 2, Request: delivery.Request{
		Source: schedule, Mechanism: delivery.SchedulerToSQS,
		Policy: delivery.EventBridgeTarget{MaxAge: time.Hour, MaxAttempts: 3},
		Target: delivery.SQSQueue{ARN: queue, Body: "n <aws.scheduler.attempt-number>", GroupID: "g"},
		As:     delivery.ExecutionRole{Role: role, RoleID: "AROA2DZE3W4CTL5ZR7EIN", Session: "ff00212d8c453aaaae644bc6846d4723"},
		Invocation: &delivery.SchedulerInvocation{
			ScheduleARN: schedule, TargetARN: queue.String(), Scheduled: at.Wire(),
			ExecutionID: "ad06616e51cdf74a", Template: "n <aws.scheduler.attempt-number>", OnPermanent: delivery.PermanentRetried,
		},
	}}
}

// A pending Scheduler delivery survives a snapshot with its role session
// and invocation, so a loaded retry still runs as the role and renders its
// own attempt number (scheduler.md 14.4.2, 14.4.4).
func TestSchedulerEntryRoundTrip(t *testing.T) {
	c := codec()
	st := newState(aws.Scope{})
	st.Entries["d-1"] = schedulerEntry()
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		`"as":{"role":"arn:aws:iam::000000000000:role/app/sch-role","roleId":"AROA2DZE3W4CTL5ZR7EIN","session":"ff00212d8c453aaaae644bc6846d4723"}`,
		`"onPermanent":"retried"`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("snapshot\n%s\ndoes not hold %s", data, want)
		}
	}
	back := newState(aws.Scope{})
	if err := c.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, st) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", back, st)
	}
}

// A loaded Scheduler delivery goes through Enqueue's checks: one whose
// role was edited to a user, or whose invocation was lost, is refused
// naming the member at fault rather than delivered as a service principal
// or with its template unrendered.
func TestLoadRefusesASchedulerDeliveryWithoutItsRoleOrInvocation(t *testing.T) {
	c := codec()
	st := newState(aws.Scope{})
	st.Entries["d-1"] = schedulerEntry()
	data, err := c.Marshal(&st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	start := bytes.Index(data, []byte(`,"invocation":`))
	end := bytes.Index(data[start:], []byte("}")) + start + 1
	for name, c2 := range map[string]struct {
		edited []byte
		field  string
	}{
		"a user as the role": {bytes.Replace(data, []byte("role/app/sch-role"), []byte("user/alice"), 1), "As.Role"},
		"no invocation":      {append(append([]byte{}, data[:start]...), data[end:]...), "Invocation"},
	} {
		back := newState(aws.Scope{})
		err := c.Unmarshal(c2.edited, &back)
		ee, ok := errors.AsType[*EntryError](err)
		if !ok || ee.ID != "d-1" || ee.Err.Field != c2.field {
			t.Errorf("%s: Unmarshal = %v, want an *EntryError for d-1's %s", name, err, c2.field)
		}
	}
}

// rendered substitutes the attempt number into each target kind's payload
// and leaves a delivery without an invocation untouched (scheduler.md 6.3,
// 14.4.2).
func TestRenderedPayloadPerTarget(t *testing.T) {
	inv := &delivery.SchedulerInvocation{Template: "n=<aws.scheduler.attempt-number>"}
	bus := aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/default"}
	entries := []delivery.EventEntry{{Source: "s", DetailType: "d", Detail: "x"}}
	cases := []struct {
		target delivery.Target
		want   delivery.Target
	}{
		{delivery.SQSQueue{Body: "x"}, delivery.SQSQueue{Body: "n=3"}},
		{delivery.LambdaFunction{Event: []byte("x")}, delivery.LambdaFunction{Event: []byte("n=3")}},
		{delivery.SNSTopic{Message: "x"}, delivery.SNSTopic{Message: "n=3"}},
		{delivery.EventBus{ARN: bus, Entries: entries}, delivery.EventBus{ARN: bus, Entries: []delivery.EventEntry{{Source: "s", DetailType: "d", Detail: "n=3"}}}},
	}
	for _, tc := range cases {
		if got := rendered(delivery.Request{Target: tc.target, Invocation: inv}, 3); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("rendered(%#v) = %#v, want %#v", tc.target, got, tc.want)
		}
	}
	if entries[0].Detail != "x" {
		t.Errorf("rendering changed the stored entry to %q; it must render into a copy", entries[0].Detail)
	}
	plain := delivery.SQSQueue{Body: "<aws.scheduler.attempt-number>"}
	if got := rendered(delivery.Request{Target: plain}, 3); !reflect.DeepEqual(got, plain) {
		t.Errorf("rendered without an invocation = %#v, want the target unchanged", got)
	}
}
