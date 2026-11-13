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
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/events"
	"github.com/sustinbebustin/understudy/internal/service/kms"
)

// sink is the kernel's delivery sink: it records every accepted delivery
// as the outbox would take it in, and hands it to box when the harness
// has an outbox.
type sink struct {
	box *outbox.Outbox

	mu  sync.Mutex
	got []kernel.Delivery
	// gate, when set, holds the next delivery (holdNext).
	gate *gate
}

// gate holds one delivery between its write's unlock and the sink: held is
// closed once the delivery waits, and closing release lets it on.
type gate struct {
	held    chan struct{}
	release chan struct{}
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	g := s.gate
	s.gate = nil
	s.mu.Unlock()
	if g != nil {
		close(g.held)
		<-g.release
	}
	s.mu.Lock()
	s.got = append(s.got, d)
	s.mu.Unlock()
	if s.box == nil {
		return nil
	}
	return s.box.Deliver(d)
}

// holdNext makes the next delivery handed over wait before the sink takes
// it, as a slow goroutine would after its write unlocked.
func (s *sink) holdNext() *gate {
	g := &gate{held: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = g
	return g
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
	// sent records what a stub SQS SendMessage received, in call order;
	// nil without an outbox (runWithOutbox).
	sent *sent
}

// sent collects the SendMessage calls the outbox's attempts make.
type sent struct {
	mu  sync.Mutex
	got []sqsapi.SendMessageInput
}

func (s *sent) all() []sqsapi.SendMessageInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
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
	runWith(t, fn)
}

// runWithOutbox is run with the outbox behind the sink and a stub SQS
// SendMessage that records what each attempt sends (harness.sent).
func runWithOutbox(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, fn, withOutbox)
}

// runWithKMS is run with understudy's KMS served beside EventBridge, which
// CreateEventBus and UpdateEventBus look a KmsKeyIdentifier up in.
func runWithKMS(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, fn, withKMS)
}

// feature is what a harness serves beside EventBridge.
type feature int

const (
	withOutbox feature = iota + 1 // the outbox behind the sink and a stub SQS SendMessage
	withKMS                       // understudy's KMS
)

func runWith(t *testing.T, fn func(h *harness), features ...feature) {
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
		outboxed, keyed := slices.Contains(features, withOutbox), slices.Contains(features, withKMS)
		svcs := []*registry.Service{eventbridgeapi.Service()}
		if outboxed {
			svcs = append(svcs, sqsapi.Service())
		}
		if keyed {
			svcs = append(svcs, kmsapi.Service())
		}
		reg, err := registry.New(svcs...)
		if err != nil {
			t.Fatal(err)
		}
		src := ids.NewSeeded(1)
		idStore := identity.New(src, aws.DefaultAccount())
		d := &sink{}
		if outboxed {
			d.box = outbox.New()
		}
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
		h := &harness{K: kt, svc: svc, sink: d, logs: l}
		if keyed {
			kms.New(kt.Deps(kmsapi.Service()), kms.Options{})
		}
		if outboxed {
			if err := d.box.Bind(k, outbox.Options{}); err != nil {
				t.Fatal(err)
			}
			h.sent = &sent{}
			kernel.Bind(k.Binder(sqsapi.Service()), sqsapi.OpSendMessage, func(_ context.Context, _ *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
				h.sent.mu.Lock()
				defer h.sent.mu.Unlock()
				h.sent.got = append(h.sent.got, *in)
				return &sqsapi.SendMessageOutput{MessageId: new("stub")}, nil
			}, registry.Full{})
		}
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
		fn(h)
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
	case delivery.EventBus, delivery.S3Object, delivery.HTTPEndpoint, delivery.StateMachine:
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
		case delivery.EventBus:
			out[i] = target.ARN.String()
		case delivery.S3Object, delivery.HTTPEndpoint, delivery.StateMachine:
			// No rule target names one; the empty ARN fails the comparison.
		}
	}
	return out
}

// A rate(5 minutes) rule and an hour's advance deliver thirteen scheduled
// events, accepted 0, 5, ... 60 minutes after the rule was created, each
// the documented scheduled event stamped with its fire instant.
func TestRateRuleFiresOnTheEmulatorClock(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now()
		h.schedule("every-five", "rate(5 minutes)")
		h.advance(time.Hour)
		got := h.sink.take()
		if len(got) != 13 {
			t.Fatalf("%d deliveries, want 13", len(got))
		}
		eventIDs := map[string]bool{}
		for i, d := range got {
			at := created.Add(time.Duration(i) * 5 * time.Minute)
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
		if len(eventIDs) != 13 {
			t.Errorf("%d distinct event ids, want 13", len(eventIDs))
		}
	})
}

// A rate rule first fires at the instant it is created, then every
// interval, its events' time at second precision: AWS's ran creation +
// k*60s from k = 0 (events/R14 02-created.time, 02-arrivals.jsonl). AWS's
// 1.7 s offset from creation is latency, which understudy does not model.
func TestRateRuleFirstFiresAtCreation(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(314 * time.Millisecond)
		created := h.Clock.Now()
		h.schedule("r", "rate(1 minute)")
		h.advance(2*time.Minute + 30*time.Second)
		got := h.sink.take()
		want := []string{"2000-01-01T00:00:00Z", "2000-01-01T00:01:00Z", "2000-01-01T00:02:00Z"}
		times := make([]string, len(got))
		for i, d := range got {
			times[i] = decode(t, d).Time
			if at := created.Add(time.Duration(i) * time.Minute); d.Accepted.Compare(at) != 0 {
				t.Errorf("delivery %d accepted at %s, want %s", i, d.Accepted, at)
			}
		}
		if !slices.Equal(times, want) {
			t.Errorf("event times %v, want %v", times, want)
		}
	})
}

// A rate rule's first target restarts its rate, so a PutTargets after the
// PutRule receives the fire AWS made at creation (events/R14 02: PutTargets
// at 07:04:33Z, first event 07:04:34Z); a further target keeps the phase.
// Restarting at the first target is inferred.
func TestRateRestartsAtItsFirstTarget(t *testing.T) {
	run(t, func(h *harness) {
		must(h, eventbridgeapi.OpPutRule, &eventbridgeapi.PutRuleInput{Name: "r", ScheduleExpression: new("rate(1 minute)")})
		h.advance(30 * time.Second)
		targeted := h.Clock.Now()
		h.target("r", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		h.advance(40 * time.Second)
		h.target("r", eventbridgeapi.Target{Id: "q2", Arn: queueARN})
		h.advance(time.Minute)
		got := acceptedAt(h.sink.take())
		want := []clock.Instant{targeted, targeted.Add(time.Minute), targeted.Add(time.Minute)}
		if !slices.EqualFunc(got, want, func(a, b clock.Instant) bool { return a.Compare(b) == 0 }) {
			t.Errorf("accepted at %v, want %v", got, want)
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

// Disabling a scheduled rule stops it; enabling it starts the rate again,
// firing at the enable and every interval after: AWS's first event after
// an enable at 07:11:21.433Z carried 07:11:22Z, then every minute in that
// phase, and none arrived while it was disabled (events/R14
// 02-enable.time, 02-arrivals-after-enable.jsonl,
// 02-arrivals-while-disabled.jsonl).
func TestDisabledScheduleDoesNotFire(t *testing.T) {
	run(t, func(h *harness) {
		h.schedule("r", "rate(1 minute)")
		h.advance(90 * time.Second)
		if n := len(h.sink.take()); n != 2 {
			t.Fatalf("%d deliveries before disabling, want 2", n)
		}
		must(h, eventbridgeapi.OpDisableRule, &eventbridgeapi.DisableRuleInput{Name: "r"})
		h.advance(10 * time.Minute)
		if n := len(h.sink.take()); n != 0 {
			t.Fatalf("%d deliveries while disabled", n)
		}
		h.advance(433 * time.Millisecond)
		enabled := h.Clock.Now()
		must(h, eventbridgeapi.OpEnableRule, &eventbridgeapi.EnableRuleInput{Name: "r"})
		h.advance(90 * time.Second)
		got := h.sink.take()
		want := []clock.Instant{enabled, enabled.Add(time.Minute)}
		if !slices.EqualFunc(acceptedAt(got), want, func(a, b clock.Instant) bool { return a.Compare(b) == 0 }) {
			t.Fatalf("after enabling: accepted at %v, want %v", acceptedAt(got), want)
		}
		times := []string{decode(t, got[0]).Time, decode(t, got[1]).Time}
		if wantTimes := []string{"2000-01-01T00:11:30Z", "2000-01-01T00:12:30Z"}; !slices.Equal(times, wantTimes) {
			t.Errorf("after enabling: event times %v, want %v", times, wantTimes)
		}
	})
}

// PutRule with the schedule unchanged, as Terraform sends it to update a
// rule's description, keeps the rate's phase; a changed schedule restarts
// it, firing at the PutRule (inferred, as the enable restart is recorded).
func TestPutRuleKeepsAnUnchangedSchedulesPhase(t *testing.T) {
	run(t, func(h *harness) {
		created := h.Clock.Now()
		h.schedule("r", "rate(5 minutes)")
		h.advance(3 * time.Minute)
		if got := h.sink.take(); len(got) != 1 || got[0].Accepted.Compare(created) != 0 {
			t.Fatalf("creation: %d deliveries, want one at %s; got %v", len(got), created, acceptedAt(got))
		}
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
		want := []clock.Instant{changed, changed.Add(10 * time.Minute)}
		if !slices.EqualFunc(acceptedAt(got), want, func(a, b clock.Instant) bool { return a.Compare(b) == 0 }) {
			t.Errorf("changed schedule: accepted at %v, want %v", acceptedAt(got), want)
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
		h.advance(30 * time.Second)
		if n := len(h.sink.take()); n != 1 {
			t.Fatalf("%d deliveries at the rate(1 minute) rule's creation, want 1", n)
		}
		must(h, eventbridgeapi.OpRemoveTargets, &eventbridgeapi.RemoveTargetsInput{Rule: "r", Ids: []string{"q"}})
		must(h, eventbridgeapi.OpDeleteRule, &eventbridgeapi.DeleteRuleInput{Name: "r"})
		recreated := h.Clock.Now()
		h.schedule("r", "rate(5 minutes)")
		h.advance(4 * time.Minute)
		if got := h.sink.take(); len(got) != 1 || got[0].Accepted.Compare(recreated) != 0 {
			t.Errorf("accepted at %v, want only the rate(5 minutes) rule's fire at its creation, %s", acceptedAt(got), recreated)
		}
	})
}
