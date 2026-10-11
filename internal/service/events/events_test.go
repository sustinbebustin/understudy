package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/events"
)

// sink is the kernel's delivery sink: it records every accepted delivery
// as the outbox would take it in.
type sink struct {
	mu  sync.Mutex
	got []kernel.Delivery
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return nil
}

// take returns the deliveries recorded since the last take.
func (s *sink) take() []kernel.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

// logs records the kernel's log as JSON lines.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// records decodes every log record written so far.
func (l *logs) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for line := range bytes.Lines(l.buf.Bytes()) {
		var r map[string]any
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("log line %s: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// harness is a running kernel with EventBridge, a recording delivery sink
// and a recorded log, in a synctest bubble on a frozen clock.
type harness struct {
	*kerneltest.K
	svc  *events.Service
	sink *sink
	logs *logs
}

const (
	account  = "000000000000"
	queueARN = "arn:aws:sqs:us-east-1:000000000000:q"
	ruleARN  = "arn:aws:events:us-east-1:000000000000:rule/"
)

// run builds the kernel as kerneltest.Run does, with a delivery sink, which
// kerneltest does not wire.
func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		timers := kernel.NewTimers()
		clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
		if err != nil {
			t.Fatal(err)
		}
		j, err := journal.New(journal.Options{Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		reg, err := registry.New(eventbridgeapi.Service())
		if err != nil {
			t.Fatal(err)
		}
		src := ids.NewSeeded(1)
		idStore := identity.New(src, aws.DefaultAccount())
		d := &sink{}
		l := &logs{}
		k, err := kernel.New(kernel.Options{
			Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j,
			Identity: idStore, Deliver: d, Log: slog.New(slog.NewJSONHandler(l, nil)), Strict: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		kt := &kerneltest.K{
			T: t, Kernel: k, Clock: clk, Journal: j, Identity: idStore,
			Scope:    aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
			Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
		}
		svc := events.New(kt.Deps(eventbridgeapi.Service()), events.Options{})
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- k.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := k.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		fn(&harness{K: kt, svc: svc, sink: d, logs: l})
	})
}

// call runs op as a client request.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.JSONType, err.Message)
	}
	return out
}

func wantError(t *testing.T, err *awserr.Error, jsonType, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s %q", jsonType, message)
	}
	if err.ID.JSONType != jsonType || err.Message != message {
		t.Errorf("error = %s %q, want %s %q", err.ID.JSONType, err.Message, jsonType, message)
	}
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// schedule puts a scheduled rule with one SQS target.
func (h *harness) schedule(name, expr string) {
	h.Helper()
	must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: name, ScheduleExpression: new(expr)})
	h.target(name, eventbridgeapi.Target{Id: "q", Arn: queueARN})
}

// target puts one target on a rule, failing on a failed entry.
func (h *harness) target(rule string, t eventbridgeapi.Target) {
	h.Helper()
	out := must(h, eventbridgeapi.OpPutTargets, &eventbridgeapi.PutTargetsInput{Rule: rule, Targets: []eventbridgeapi.Target{t}})
	if out.FailedEntryCount != 0 {
		h.Fatalf("PutTargets failed entries: %+v", out.FailedEntries)
	}
}

// envelope is a delivered event, decoded.
type envelope struct {
	Version    string          `json:"version"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account"`
	Time       string          `json:"time"`
	Region     string          `json:"region"`
	Resources  []string        `json:"resources"`
	Detail     json.RawMessage `json:"detail"`
}

// body returns what a delivery sends: an SQS body, a Lambda event or an SNS
// message.
func body(t *testing.T, d kernel.Delivery) string {
	t.Helper()
	switch target := d.Request.Target.(type) {
	case delivery.SQSQueue:
		return target.Body
	case delivery.LambdaFunction:
		return string(target.Event)
	case delivery.SNSTopic:
		return target.Message
	}
	t.Fatalf("delivery target %T", d.Request.Target)
	return ""
}

func decode(t *testing.T, d kernel.Delivery) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal([]byte(body(t, d)), &e); err != nil {
		t.Fatalf("delivered body %s: %v", body(t, d), err)
	}
	return e
}

// targetARNs lists the target ARN of each delivery.
func targetARNs(ds []kernel.Delivery) []string {
	out := make([]string, len(ds))
	for i := range ds {
		switch target := ds[i].Request.Target.(type) {
		case delivery.SQSQueue:
			out[i] = target.ARN.String()
		case delivery.LambdaFunction:
			out[i] = target.ARN.String()
		case delivery.SNSTopic:
			out[i] = target.ARN.String()
		}
	}
	return out
}

// Advancing an hour across a rate(5 minutes) rule delivers twelve scheduled
// events, accepted 5, 10, ... 60 minutes after the rule was created, each
// the documented scheduled event stamped with its fire instant.
func TestRateRuleFiresOnTheEmulatorClock(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now()
		h.schedule("every-five", "rate(5 minutes)")
		h.advance(time.Hour)
		got := h.sink.take()
		if len(got) != 12 {
			t.Fatalf("%d deliveries, want 12", len(got))
		}
		eventIDs := map[string]bool{}
		for i, d := range got {
			at := created.Add(time.Duration(i+1) * 5 * time.Minute)
			if d.Accepted.Compare(at) != 0 {
				t.Errorf("delivery %d accepted at %s, want %s", i, d.Accepted, at)
			}
			e := decode(t, d)
			want := envelope{
				Version: "0", ID: e.ID, DetailType: "Scheduled Event", Source: "aws.events", Account: account,
				Time: at.Wire().UTC().Format(time.RFC3339), Region: "us-east-1", Resources: []string{ruleARN + "every-five"},
			}
			if string(e.Detail) != "{}" || !equalEnvelope(e, want) {
				t.Errorf("delivery %d = %+v (detail %s), want %+v with detail {}", i, e, e.Detail, want)
			}
			eventIDs[e.ID] = true
			p, ok := d.Request.Policy.(delivery.EventBridgeTarget)
			if d.Request.Mechanism != delivery.EventsToSQS || d.Request.Source.String() != ruleARN+"every-five" ||
				!ok || p.MaxAge != 24*time.Hour || p.MaxAttempts != 185 {
				t.Errorf("delivery %d request = %+v", i, d.Request)
			}
		}
		if len(eventIDs) != 12 {
			t.Errorf("%d distinct event ids, want 12", len(eventIDs))
		}
	})
}

func equalEnvelope(a, b envelope) bool {
	return a.Version == b.Version && a.ID == b.ID && a.DetailType == b.DetailType && a.Source == b.Source &&
		a.Account == b.Account && a.Time == b.Time && a.Region == b.Region && slices.Equal(a.Resources, b.Resources)
}

// A cron rule fires at the instants its expression names (UTC), not at an
// interval from its creation.
func TestCronRuleFiresAtItsInstants(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(7 * time.Minute)
		start := h.Clock.Now().Wire()
		h.schedule("quarter-hours", "cron(0/15 * * * ? *)")
		h.advance(time.Hour)
		fired := h.sink.take()
		got := make([]time.Time, len(fired))
		for i, d := range fired {
			got[i] = d.Accepted.Wire()
		}
		hour := start.Truncate(time.Hour)
		want := []time.Time{hour.Add(15 * time.Minute), hour.Add(30 * time.Minute), hour.Add(45 * time.Minute), hour.Add(time.Hour)}
		if !slices.EqualFunc(got, want, time.Time.Equal) {
			t.Errorf("fired at %v, want %v", got, want)
		}
	})
}

// Disabling a scheduled rule stops it; enabling it starts the rate again
// from the enable.
func TestDisabledScheduleDoesNotFire(t *testing.T) {
	run(t, func(h *harness) {
		h.schedule("r", "rate(1 minute)")
		h.advance(90 * time.Second)
		if n := len(h.sink.take()); n != 1 {
			t.Fatalf("%d deliveries before disabling, want 1", n)
		}
		must(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "r"})
		h.advance(10 * time.Minute)
		if n := len(h.sink.take()); n != 0 {
			t.Fatalf("%d deliveries while disabled", n)
		}
		enabled := h.Clock.Now()
		must(h, eventbridgeapi.OpEnableRule, &eventbridgeapi.EnableRuleInput{Name: "r"})
		h.advance(time.Minute)
		got := h.sink.take()
		if len(got) != 1 || got[0].Accepted.Compare(enabled.Add(time.Minute)) != 0 {
			t.Errorf("after enabling: %d deliveries, want one at %s", len(got), enabled.Add(time.Minute))
		}
	})
}

// PutRule with the schedule unchanged, as Terraform sends it to update a
// rule's description, keeps the rate's phase; a changed schedule restarts
// it from the PutRule.
func TestPutRuleKeepsAnUnchangedSchedulesPhase(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now()
		h.schedule("r", "rate(5 minutes)")
		h.advance(3 * time.Minute)
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", ScheduleExpression: new("rate(5 minutes)"), Description: new("changed")})
		h.advance(2 * time.Minute)
		got := h.sink.take()
		if len(got) != 1 || got[0].Accepted.Compare(created.Add(5*time.Minute)) != 0 {
			t.Fatalf("unchanged schedule: %d deliveries, want one at %s; got %v", len(got), created.Add(5*time.Minute), acceptedAt(got))
		}
		h.advance(time.Minute)
		changed := h.Clock.Now()
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", ScheduleExpression: new("rate(10 minutes)")})
		h.advance(10 * time.Minute)
		got = h.sink.take()
		if len(got) != 1 || got[0].Accepted.Compare(changed.Add(10*time.Minute)) != 0 {
			t.Errorf("changed schedule: %d deliveries, want one at %s; got %v", len(got), changed.Add(10*time.Minute), acceptedAt(got))
		}
	})
}

func acceptedAt(ds []kernel.Delivery) []clock.Instant {
	out := make([]clock.Instant, len(ds))
	for i := range ds {
		out[i] = ds[i].Accepted
	}
	return out
}

// A scheduled rule without targets sends nothing and draws no event id, so
// it leaves the ids of a seeded run unchanged.
func TestScheduleWithoutTargetsDrawsNoID(t *testing.T) {
	firstID := func(setup func(h *harness)) string {
		var id string
		run(t, func(h *harness) {
			setup(h)
			id = *h.putEvents(entry("s", "t", `{}`)).Entries[0].EventId
		})
		return id
	}
	quiet := firstID(func(*harness) {})
	fired := firstID(func(h *harness) {
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "idle", ScheduleExpression: new("rate(1 minute)")})
		h.advance(time.Hour)
	})
	if quiet != fired {
		t.Errorf("EventId after an hour of a targetless schedule = %s, want %s as without it", fired, quiet)
	}
}

// Deleting a scheduled rule stops its timer.
func TestDeletedScheduleDoesNotFire(t *testing.T) {
	run(t, func(h *harness) {
		h.schedule("r", "rate(1 minute)")
		must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", Ids: []string{"q"}})
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r"})
		h.schedule("r", "rate(5 minutes)")
		h.advance(4 * time.Minute)
		if n := len(h.sink.take()); n != 0 {
			t.Errorf("%d deliveries from the deleted rate(1 minute) rule", n)
		}
	})
}
