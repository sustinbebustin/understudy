package lambda_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
	"github.com/sustinbebustin/understudy/internal/store"
)

// runESM runs fn against Lambda with its Execution and pollers started,
// the real SQS service, and fake containers whose invocations rec answers.
func runESM(t *testing.T, rec *recorder, fn func(h *execHarness)) {
	t.Helper()
	runESMWithQueues(t, rec, func(h *execHarness, _ *sqs.Service) { fn(h) })
}

// runESMWithQueues is runESM that also hands fn the SQS service.
func runESMWithQueues(t *testing.T, rec *recorder, fn func(h *execHarness, queues *sqs.Service)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		queues := sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		withExec(k, rec.handle, nil, func(h *execHarness) {
			h.x.StartPollers()
			fn(h, queues)
		})
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service(), sqsapi.Service()))
}

// snapshot is the Lambda and SQS state as a snapshot holds it.
type snapshot struct{ mappings, queues []store.Section }

func (h *execHarness) snapshot(queues *sqs.Service) snapshot {
	h.Helper()
	mappings, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	q, err := queues.Export()
	if err != nil {
		h.Fatal(err)
	}
	return snapshot{mappings: mappings, queues: q}
}

// load replaces the Lambda and SQS state with s in the order a snapshot
// load takes: drain, reset and import, a cleared scheduler, then every
// rearm, the pollers' last.
func (h *execHarness) load(queues *sqs.Service, s snapshot) {
	h.Helper()
	if err := h.x.Drain(h.Context(), kernel.ResetScope{}); err != nil {
		h.Fatal(err)
	}
	h.svc.Reset(kernel.ResetScope{})
	queues.Reset(kernel.ResetScope{})
	if err := h.svc.Import(s.mappings); err != nil {
		h.Fatal(err)
	}
	if err := queues.Import(s.queues); err != nil {
		h.Fatal(err)
	}
	h.Kernel.Timers().Scheduler().Clear()
	now := h.Clock.Now()
	h.svc.Rearm(now)
	queues.Rearm(now)
	h.x.RearmPollers()
}

// sqsEvent is the event an SQS mapping invokes a function with.
type sqsEvent struct {
	Records []esm.Record
}

// recorder keeps the events the function received and answers each with
// answer, or with a null response when answer is nil.
type recorder struct {
	mu     sync.Mutex
	events []sqsEvent
	// sizes are the lengths of the events' payloads.
	sizes  []int
	answer func(ctx context.Context, ev sqsEvent) runtimeapi.Outcome
}

// gated has the function answer each invocation with the outcome sent on
// the channel it returns. Call it inside the bubble: synctest.Wait sees the
// function as durably blocked only on a channel made there.
func (r *recorder) gated() chan<- runtimeapi.Outcome {
	gate := make(chan runtimeapi.Outcome)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = func(ctx context.Context, _ sqsEvent) runtimeapi.Outcome {
		select {
		case o := <-gate:
			return o
		case <-ctx.Done():
			return runtimeapi.FunctionError{Type: "Cancelled"}
		}
	}
	return gate
}

func (r *recorder) handle(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	var ev sqsEvent
	if err := json.Unmarshal(inv.Payload, &ev); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.sizes = append(r.sizes, len(inv.Payload))
	answer := r.answer
	r.mu.Unlock()
	if answer == nil {
		return runtimeapi.Success{Payload: []byte("null")}, nil
	}
	return answer(ctx, ev), nil
}

// bodies returns the bodies of each invocation's records.
func (r *recorder) bodies() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.events))
	for i, ev := range r.events {
		out[i] = make([]string, len(ev.Records))
		for j := range ev.Records {
			out[i][j] = ev.Records[j].Body
		}
	}
	return out
}

// payloadSizes returns the length of each invocation's payload.
func (r *recorder) payloadSizes() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sizes)
}

func (r *recorder) event(i int) sqsEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[i]
}

// quiesce waits until every goroutine of the bubble is blocked and the
// kernel has settled: the pollers' idle waits are not tracked work.
func (h *execHarness) quiesce() {
	h.Helper()
	synctest.Wait()
	h.settle()
}

// advancePolled moves the emulator clock and quiesces, so the pollers
// woken by what the advance fired have run.
func (h *execHarness) advancePolled(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
	h.quiesce()
}

// queue creates a queue and returns its URL and ARN.
func (h *execHarness) queue(name string, attrs map[sqsapi.QueueAttributeName]string) (url, arn string) {
	h.Helper()
	out := call(h.harness, sqsapi.OpCreateQueue, &sqsapi.CreateQueueInput{QueueName: name, Attributes: attrs})
	return *out.QueueUrl, "arn:aws:sqs:us-east-1:000000000000:" + name
}

func (h *execHarness) send(url string, bodies ...string) {
	h.Helper()
	for _, b := range bodies {
		call(h.harness, sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: b})
	}
}

// depth returns how many messages url holds receivable and in flight.
func (h *execHarness) depth(url string) (visible, inFlight int) {
	h.Helper()
	out := call(h.harness, sqsapi.OpGetQueueAttributes, &sqsapi.GetQueueAttributesInput{QueueUrl: url, AttributeNames: []sqsapi.QueueAttributeName{
		sqsapi.QueueAttributeNameApproximateNumberOfMessages, sqsapi.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	}})
	visible, err := strconv.Atoi(out.Attributes[sqsapi.QueueAttributeNameApproximateNumberOfMessages])
	if err != nil {
		h.Fatal(err)
	}
	inFlight, err = strconv.Atoi(out.Attributes[sqsapi.QueueAttributeNameApproximateNumberOfMessagesNotVisible])
	if err != nil {
		h.Fatal(err)
	}
	return visible, inFlight
}

// receiveNow is a client's short poll of url.
func (h *execHarness) receiveNow(url string) []sqsapi.Message {
	h.Helper()
	return call(h.harness, sqsapi.OpReceiveMessage, &sqsapi.ReceiveMessageInput{QueueUrl: url, WaitTimeSeconds: new(int32(0)), MaxNumberOfMessages: new(int32(10))}).Messages
}

// mapQueue creates the function worker and a mapping from the queue arn
// to it.
func (h *execHarness) mapQueue(arn string, in lambdaapi.CreateEventSourceMappingInput) *lambdaapi.EventSourceMappingConfiguration {
	h.Helper()
	h.createFunction("worker")
	in.FunctionName, in.EventSourceArn = "worker", new(arn)
	return call(h.harness, lambdaapi.OpCreateEventSourceMapping, &in)
}

func wantBodies(tb testing.TB, got, want [][]string) {
	tb.Helper()
	if !slices.EqualFunc(got, want, slices.Equal) {
		tb.Fatalf("invocations received %q, want %q", got, want)
	}
}

// With no batching window the function is invoked as soon as a message is
// visible, with the SQS record shape of lambda-events.md 3, and the
// message is deleted after it succeeds.
func TestESMWindowZeroInvokesOnFirstMessage(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		h.send(url, "first")
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"first"}})
		r := rec.event(0).Records[0]
		if r.EventSource != "aws:sqs" || r.EventSourceARN != arn || r.AWSRegion != "us-east-1" || r.MD5OfBody != "8b04d5e3775d298e78455efc5ca404d5" ||
			r.Attributes["ApproximateReceiveCount"] != "1" || r.Attributes["SenderId"] == "" || r.MD5OfMessageAttributes != nil {
			h.Fatalf("record = %+v", r)
		}
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight after success, want none", visible, inFlight)
		}
	})
}

// A 5 second window collects the messages sent while it is open and
// invokes once the window's timer fires under Advance.
func TestESMWindowCollectsUntilTimerFires(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(5))})
		h.quiesce()
		h.send(url, "a")
		h.quiesce()
		h.advancePolled(2 * time.Second)
		h.send(url, "b")
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		h.advancePolled(3 * time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"a", "b"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
	})
}

// A BatchSize of 25 fills its batch with three receives of 10, 10 and 5
// and invokes at once, without waiting for the window.
func TestESMBatchSizeLoopsReceives(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		bodies := make([]string, 25)
		for i := range bodies {
			bodies[i] = "m" + strconv.Itoa(i)
		}
		h.send(url, bodies...)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(25)), MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		got := rec.bodies()
		if len(got) != 1 || len(got[0]) != 25 {
			h.Fatalf("invocations received %d batches %v, want one of 25", len(got), got)
		}
		inv := firstInvocation(h)
		receives := 0
		for _, e := range esmCalls(h, sqsapi.OpReceiveMessage.Descriptor().ID) {
			if e.ID < inv.ID {
				receives++
			}
		}
		if receives != 3 {
			h.Fatalf("the batch took %d receives, want 3", receives)
		}
	})
}

// A batch ends at 6 MB whatever its BatchSize: seven messages of 1 MiB
// make an invocation of the five that fit and one of the two left, each
// within the limit, every message delivered once and deleted. Under a
// frozen clock the messages past the first batch are held pinned until
// their own invocation; under a running clock nothing is pinned.
func TestESMBatchEndsAt6MB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
	}{{"frozen", false}, {"running", true}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			runESM(t, rec, func(h *execHarness) {
				if tc.running {
					if _, err := h.Clock.Resume(); err != nil {
						h.Fatal(err)
					}
				}
				url, arn := h.queue("orders", nil)
				sent := make([]string, 7)
				for i := range sent {
					prefix := strconv.Itoa(i) + ":"
					sent[i] = prefix + strings.Repeat("a", 1<<20-len(prefix))
				}
				h.send(url, sent...)
				h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(10))})
				h.quiesce()
				got := rec.bodies()
				if len(got) != 2 || len(got[0]) != 5 || len(got[1]) != 2 {
					sizes := make([]int, len(got))
					for i := range got {
						sizes[i] = len(got[i])
					}
					h.Fatalf("invocations received batches of %v messages, want 5 then 2", sizes)
				}
				for i, n := range rec.payloadSizes() {
					if n > esm.MaxPayload {
						h.Fatalf("invocation %d's event is %d bytes, over the %d limit", i, n, esm.MaxPayload)
					}
				}
				delivered := slices.Concat(got...)
				slices.Sort(delivered)
				if !slices.Equal(delivered, sent) {
					h.Fatal("the function did not receive each message exactly once")
				}
				if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
					h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
				}
			})
		})
	}
}

// Updating a mapping's filter criteria changes which messages its poller
// invokes the function with from the next message on.
func TestESMUpdatedFiltersApply(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(`{"body":{"kind":["first"]}}`)}},
		}})
		h.quiesce()
		h.send(url, `{"kind":"first"}`, `{"kind":"second"}`)
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(`{"body":{"kind":["second"]}}`)}},
		}})
		h.quiesce()
		h.send(url, `{"kind":"first"}`, `{"kind":"second"}`)
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{`{"kind":"first"}`}, {`{"kind":"second"}`}})
	})
}

// With ReportBatchItemFailures only the successes are deleted; the failed
// message returns after the visibility timeout with its receive count
// raised, and moves to the dead-letter queue on the receive past
// maxReceiveCount (lambda-events.md 2.3).
func TestESMPartialFailureRedrivesToDLQ(t *testing.T) {
	rec := &recorder{answer: func(_ context.Context, ev sqsEvent) runtimeapi.Outcome {
		var failures []map[string]string
		for _, r := range ev.Records {
			if r.Body == "bad" {
				failures = append(failures, map[string]string{"itemIdentifier": r.MessageID})
			}
		}
		payload, err := json.Marshal(map[string]any{"batchItemFailures": failures})
		if err != nil {
			return runtimeapi.FunctionError{Type: "Marshal", Payload: []byte(err.Error())}
		}
		return runtimeapi.Success{Payload: payload}
	}}
	runESM(t, rec, func(h *execHarness) {
		dlqURL, dlqARN := h.queue("orders-dlq", nil)
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{
			sqsapi.QueueAttributeNameVisibilityTimeout: "30",
			sqsapi.QueueAttributeNameRedrivePolicy:     `{"deadLetterTargetArn":"` + dlqARN + `","maxReceiveCount":"2"}`,
		})
		h.send(url, "good1", "bad", "good2")
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{
			FunctionResponseTypes: []lambdaapi.FunctionResponseType{lambdaapi.FunctionResponseTypeReportBatchItemFailures},
		})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"good1", "bad", "good2"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 1 {
			h.Fatalf("after the partial failure the queue holds %d visible and %d in flight, want only the failed one in flight", visible, inFlight)
		}
		h.advancePolled(29 * time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"good1", "bad", "good2"}})
		h.advancePolled(time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"good1", "bad", "good2"}, {"bad"}})
		if n := rec.event(1).Records[0].Attributes["ApproximateReceiveCount"]; n != "2" {
			h.Fatalf("redelivered ApproximateReceiveCount = %s, want 2", n)
		}
		h.advancePolled(30 * time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"good1", "bad", "good2"}, {"bad"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("the source queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
		if visible, _ := h.depth(dlqURL); visible != 1 {
			h.Fatalf("the dead-letter queue holds %d messages, want the failed one", visible)
		}
	})
}

// Without ReportBatchItemFailures a batch item failure response is
// ignored and the batch succeeds; a function error fails it whole.
func TestESMFunctionErrorFailsWholeBatch(t *testing.T) {
	rec := &recorder{answer: func(_ context.Context, ev sqsEvent) runtimeapi.Outcome {
		if ev.Records[0].Body == "boom" {
			return runtimeapi.FunctionError{Type: "Error", Payload: []byte(`{"errorMessage":"boom"}`)}
		}
		return runtimeapi.Success{Payload: []byte(`{"batchItemFailures":[{"itemIdentifier":"` + ev.Records[0].MessageID + `"}]}`)}
	}}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		h.send(url, "ok", "fine")
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("an ignored failure response left %d visible and %d in flight, want none", visible, inFlight)
		}
		h.send(url, "boom", "fine")
		h.quiesce()
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 2 {
			h.Fatalf("a function error left %d visible and %d in flight, want both in flight", visible, inFlight)
		}
	})
}

// Under a frozen clock the poller pins what it holds: an Advance past the
// visibility timeout during the invocation does not make the message
// receivable. Released after a failed invocation, the message is
// receivable at once. After Resume, a held message follows its visibility
// timeout as on AWS.
func TestESMFrozenClockPinsHeldMessages(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		gate := rec.gated()
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		h.send(url, "held")
		synctest.Wait()
		if _, err := h.Clock.Advance(time.Minute); err != nil {
			h.Fatal(err)
		}
		synctest.Wait()
		if got := h.receiveNow(url); len(got) != 0 {
			h.Fatalf("a pinned message was received by a client: %v", *got[0].Body)
		}
		gate <- runtimeapi.FunctionError{Type: "Error", Payload: []byte(`{}`)}
		// The send returns once the redelivered message's invocation runs.
		gate <- runtimeapi.Success{Payload: []byte(`null`)}
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"held"}, {"held"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
		if _, err := h.Clock.Resume(); err != nil {
			h.Fatal(err)
		}
		h.send(url, "running")
		synctest.Wait()
		if _, err := h.Clock.Advance(time.Minute); err != nil {
			h.Fatal(err)
		}
		synctest.Wait()
		got := h.receiveNow(url)
		if len(got) != 1 || *got[0].Body != "running" {
			h.Fatalf("under a running clock a client received %d messages after the visibility timeout, want the held one", len(got))
		}
		gate <- runtimeapi.Success{Payload: []byte(`null`)}
		synctest.Wait()
	})
}

// Filter criteria delete non-matching messages without invoking, and the
// journal reads SendMessage, then the poller's ReceiveMessage in the
// poll's trace, then the Invocation and its DeleteMessageBatch in the
// SendMessage's trace, which the batch continues (ADR 0007).
func TestESMFilterAndJournalTrace(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		h.send(url, `{"kind":"keep"}`, `{"kind":"drop"}`, "plain text")
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(`{"body":{"kind":["keep"]}}`)}},
		}})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{`{"kind":"keep"}`}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want the filtered messages deleted", visible, inFlight)
		}
		inv := firstInvocation(h)
		events := h.Journal.Query(journal.Filter{Limit: 10000}).Events
		var send, receive, del journal.Event
		for _, e := range events {
			call, ok := e.Detail.(journal.APICall)
			switch {
			case !ok:
			case call.Op == sqsapi.OpSendMessage.Descriptor().ID && send.ID == 0:
				send = e
			case call.Op == sqsapi.OpReceiveMessage.Descriptor().ID && esmOrigin(call) && receive.ID == 0:
				receive = e
			case call.Op == sqsapi.OpDeleteMessageBatch.Descriptor().ID && esmOrigin(call) && e.ID > inv.ID && del.ID == 0:
				del = e
			}
		}
		if send.ID == 0 || (send.ID >= receive.ID || receive.ID >= inv.ID || inv.ID >= del.ID) {
			h.Fatalf("journal order SendMessage %d, ReceiveMessage %d, Invocation %d, DeleteMessageBatch %d; want increasing", send.ID, receive.ID, inv.ID, del.ID)
		}
		if inv.Cause.Trace != send.Cause.Trace || del.Cause.Trace != send.Cause.Trace || receive.Cause.Trace == send.Cause.Trace {
			h.Fatalf("traces: SendMessage %d, ReceiveMessage %d, Invocation %d, DeleteMessageBatch %d; want the invocation and the delete in the send's, the receive in the poll's",
				send.Cause.Trace, receive.Cause.Trace, inv.Cause.Trace, del.Cause.Trace)
		}
		if mode := journalMode(receive); mode != "execution-role" {
			h.Fatalf("ReceiveMessage ran as %q, want the execution role", mode)
		}
	})
}

// An invocation's parent is the enqueue event of its batch's earliest
// message, and the others' enqueue events are its links, so the trace of a
// SendMessage reaches the invocation it fed (ARCHITECTURE.md 10).
func TestESMInvocationParentIsEarliestEnqueue(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		h.send(url, "first", "second")
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"first", "second"}})
		sends := h.Journal.Query(journal.Filter{Operation: sqsapi.OpSendMessage.Descriptor().ID, Limit: 10}).Events
		if len(sends) != 2 {
			h.Fatalf("journaled %d SendMessage calls, want 2", len(sends))
		}
		inv := firstInvocation(h)
		if inv.Cause.Parent != sends[0].ID || inv.Cause.Trace != sends[0].Cause.Trace {
			h.Fatalf("invocation cause = %+v, want parent %d in trace %d: the earliest message's SendMessage", inv.Cause, sends[0].ID, sends[0].Cause.Trace)
		}
		if !slices.Equal(inv.Links, []ref.EventID{sends[1].ID}) {
			h.Fatalf("invocation links = %v, want the other message's SendMessage %d", inv.Links, sends[1].ID)
		}
	})
}

// A stored filter pattern that no longer compiles, which only a loaded
// snapshot can hold, stops its mapping: it receives nothing and invokes
// nothing, and the messages stay in the queue until an update replaces the
// pattern.
func TestESMUncompilableStoredFilterStopsMapping(t *testing.T) {
	rec := &recorder{}
	runESMWithQueues(t, rec, func(h *execHarness, queues *sqs.Service) {
		const valid = `{"body":{"kind":["keep"]}}`
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(valid)}},
		}})
		h.quiesce()
		saved := h.snapshot(queues)
		old, err := json.Marshal(valid)
		if err != nil {
			h.Fatal(err)
		}
		// Lambda's filter engine rejects an empty array of values.
		broken, err := json.Marshal(`{"body":{"kind":[]}}`)
		if err != nil {
			h.Fatal(err)
		}
		replaced := false
		for i := range saved.mappings {
			data := saved.mappings[i].Data
			saved.mappings[i].Data = bytes.ReplaceAll(data, old, broken)
			replaced = replaced || !bytes.Equal(data, saved.mappings[i].Data)
		}
		if !replaced {
			h.Fatal("the exported Lambda state does not hold the mapping's filter pattern")
		}
		h.load(queues, saved)
		h.quiesce()
		receives := len(esmCalls(h, sqsapi.OpReceiveMessage.Descriptor().ID))
		h.send(url, `{"kind":"keep"}`)
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		if visible, inFlight := h.depth(url); visible != 1 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want the message left visible", visible, inFlight)
		}
		if n := len(esmCalls(h, sqsapi.OpReceiveMessage.Descriptor().ID)); n != receives {
			h.Fatalf("the mapping received %d times with an uncompilable filter, want none", n-receives)
		}
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(valid)}},
		}})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{`{"kind":"keep"}`}})
		h.send(url, `{"kind":"keep"}`, `{"kind":"drop"}`)
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{`{"kind":"keep"}`}, {`{"kind":"keep"}`}})
	})
}

// A mapping outlives its function: with the function gone its messages
// stay in the queue and the journal records that polling stopped; once the
// function exists again the poller's retry processes the messages the
// queue holds, with no new message to wake it.
func TestESMMappingOutlivesFunction(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: "worker"})
		h.send(url, "waiting")
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		if visible, _ := h.depth(url); visible != 1 {
			h.Fatalf("queue holds %d visible messages with the function gone, want 1", visible)
		}
		if got := pollerStates(h, *m.EventSourceMappingArn); !slices.Equal(got, []string{"Polling->FunctionMissing"}) {
			h.Fatalf("poller state changes = %v", got)
		}
		h.createFunction("worker")
		h.advancePolled(time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"waiting"}})
		if got := pollerStates(h, *m.EventSourceMappingArn); !slices.Equal(got, []string{"Polling->FunctionMissing", "FunctionMissing->Polling"}) {
			h.Fatalf("poller state changes = %v", got)
		}
	})
}

// A disabled mapping stops polling; enabling it again picks up what the
// queue gathered meanwhile.
func TestESMDisabledMappingDoesNotPoll(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		h.send(url, "queued")
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"queued"}})
	})
}

// A FIFO queue's batching window is ignored: the function is invoked at
// once, with a group's messages in the order they were sent.
func TestESMFIFOIgnoresWindow(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders.fifo", map[sqsapi.QueueAttributeName]string{
			sqsapi.QueueAttributeNameFifoQueue:                 "true",
			sqsapi.QueueAttributeNameContentBasedDeduplication: "true",
		})
		for _, body := range []string{"first", "second", "third"} {
			call(h.harness, sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: body, MessageGroupId: new("g")})
		}
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"first", "second", "third"}})
	})
}

// After a snapshot load the loaded mappings poll the loaded queues: a
// mapping that had no poller before the load processes the messages its
// queue holds without waiting for a new one.
func TestESMRearmPollsLoadedMappings(t *testing.T) {
	rec := &recorder{}
	runESMWithQueues(t, rec, func(h *execHarness, queues *sqs.Service) {
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		enabled := h.snapshot(queues)
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		h.quiesce()
		h.send(url, "waiting")
		h.quiesce()
		h.load(queues, snapshot{mappings: enabled.mappings, queues: h.snapshot(queues).queues})
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"waiting"}})
	})
}

// A batch held in its batching window when a load replaces its mapping is
// dropped with the state it came from: the function never receives a
// message the loaded queue does not hold, and polls the loaded one.
func TestESMLoadDropsHeldBatch(t *testing.T) {
	rec := &recorder{}
	runESMWithQueues(t, rec, func(h *execHarness, queues *sqs.Service) {
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		empty := h.snapshot(queues)
		h.send(url, "before-load")
		h.quiesce()
		h.load(queues, empty)
		h.quiesce()
		h.advancePolled(time.Minute)
		wantBodies(h, rec.bodies(), nil)
		h.send(url, "after-load")
		h.quiesce()
		h.advancePolled(time.Minute)
		wantBodies(h, rec.bodies(), [][]string{{"after-load"}})
	})
}

// A mapping disabled and enabled again while the step that saw it disabled
// still runs keeps polling: the release of its held batch is delayed so the
// enable lands inside that step.
func TestESMReenabledDuringDisablingStepKeepsPolling(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", nil)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		h.send(url, "held")
		h.quiesce()
		h.faultESM(sqsapi.OpHoldVisibility.Descriptor().ID, fault.Latency{Wall: time.Second})
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(false)})
		synctest.Wait() // the disabling step is held in the release of its batch
		call(h.harness, lambdaapi.OpUpdateEventSourceMapping, &lambdaapi.UpdateEventSourceMappingInput{UUID: *m.UUID, Enabled: new(true)})
		synctest.Wait()
		time.Sleep(time.Second)
		h.quiesce()
		h.send(url, "after")
		h.quiesce()
		h.advancePolled(time.Minute)
		got := slices.Concat(rec.bodies()...)
		slices.Sort(got)
		if !slices.Equal(got, []string{"after", "held"}) {
			h.Fatalf("after the mapping was enabled again the function received %q, want the held message and the new one", got)
		}
	})
}

// A receive that fails after earlier receives filled part of a batch ends
// the filling: the batch waits for its window, which still closes, and
// the function receives every message of the queue once.
func TestESMReceiveFaultInvokesWhatWasReceived(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", nil)
		bodies := make([]string, 25)
		for i := range bodies {
			bodies[i] = "m" + strconv.Itoa(i)
		}
		h.send(url, bodies...)
		h.faultESM(sqsapi.OpHoldVisibility.Descriptor().ID, fault.Latency{Wall: time.Second})
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(25)), MaximumBatchingWindowInSeconds: new(int32(5))})
		synctest.Wait() // the pin of the first receive's messages is held
		h.faultESM(sqsapi.OpReceiveMessage.Descriptor().ID, fault.Throttle{})
		time.Sleep(time.Second)
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		h.advancePolled(5 * time.Second)
		all := slices.Concat(rec.bodies()...)
		slices.Sort(all)
		if want := slices.Sorted(slices.Values(bodies)); !slices.Equal(all, want) {
			h.Fatalf("the function received %q, want each message once", all)
		}
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
	})
}

// Under a frozen clock a message whose delete failed after a successful
// invocation is released: it returns to the queue when its visibility
// timeout ends and is delivered again.
func TestESMFailedDeleteReleasesItsPin(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameVisibilityTimeout: "30"})
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		h.faultESM(sqsapi.OpDeleteMessageBatch.Descriptor().ID, fault.Throttle{})
		h.send(url, "once")
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"once"}})
		h.advancePolled(30 * time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"once"}, {"once"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
	})
}

// A mapping invokes its function through Invoke, so a fault rule on
// lambda.Invoke for the function's ARN and internal calls reaches it (ADR
// 0008): the throttled batch fails, its message returns after its
// visibility timeout and is delivered then, and the journal records the
// hit. The Invoke runs as Lambda on its own behalf, which no policy grants,
// so the access recorder leaves it out.
func TestESMThrottledInvokeIsRetried(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameVisibilityTimeout: "30"})
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		if _, err := h.Faults.Add(fault.Rule{
			Match: fault.Match{
				Operations: []ref.OperationID{lambdaapi.OpInvoke.Descriptor().ID},
				Resource:   fault.NewARNGlob(fnARN("worker") + "*"),
				Origin:     fault.InternalOnly,
			},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.NextN{N: 1},
		}); err != nil {
			h.Fatal(err)
		}
		h.send(url, "once")
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		if hits := h.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected, Limit: 10}).Events; len(hits) != 1 {
			h.Fatalf("journaled %d fault hits, want the mapping's throttled Invoke", len(hits))
		}
		h.advancePolled(30 * time.Second)
		wantBodies(h, rec.bodies(), [][]string{{"once"}})
		invokes := esmCalls(h, lambdaapi.OpInvoke.Descriptor().ID)
		if len(invokes) != 2 || journalMode(invokes[0]) != "platform:lambda.amazonaws.com" {
			h.Fatalf("the mapping made %d Invoke calls, the first as %q; want 2 as Lambda on its own behalf", len(invokes), journalModeOf(invokes))
		}
	})
}

// journalModeOf is the mode of the first event of events, "" for none.
func journalModeOf(events []journal.Event) string {
	if len(events) == 0 {
		return ""
	}
	return journalMode(events[0])
}

// Under a scaled clock the poller pins what it holds: wall time that takes
// emulator time past the visibility timeout during the invocation does not
// make the message receivable.
func TestESMScaledClockPinsHeldMessages(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		gate := rec.gated()
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameVisibilityTimeout: "30"})
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		if _, err := h.Clock.Scale(100); err != nil {
			h.Fatal(err)
		}
		h.send(url, "held")
		synctest.Wait()
		time.Sleep(time.Second) // 100 s of emulator time
		if got := h.receiveNow(url); len(got) != 0 {
			h.Fatalf("a pinned message was received by a client: %v", *got[0].Body)
		}
		gate <- runtimeapi.Success{Payload: []byte(`null`)}
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"held"}})
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
	})
}

// Under a running clock a message whose visibility timeout ends while its
// batch waits for the window is received again into the same batch, which
// holds it once, with the newer receive.
func TestESMRunningClockWindowHoldsAMessageOnce(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		if _, err := h.Clock.Resume(); err != nil {
			h.Fatal(err)
		}
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameVisibilityTimeout: "10"})
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(15))})
		h.quiesce()
		h.send(url, "slow")
		h.quiesce()
		time.Sleep(16 * time.Second)
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"slow"}})
		if n := rec.event(0).Records[0].Attributes["ApproximateReceiveCount"]; n != "2" {
			h.Fatalf("ApproximateReceiveCount = %s, want 2: the batch keeps the newer receive", n)
		}
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want none", visible, inFlight)
		}
	})
}

// A message the filters cannot decide within the matcher's work bound
// matches none of them: it is deleted without an invocation.
func TestESMUndecidableFilterDeletesTheMessage(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		url, arn := h.queue("orders", nil)
		// Four elements of 32 joined into one component, with "z" never
		// matching, make the search try every combination.
		elem := `{"b":"1","c":"1","d":"1","e":"1","z":"1"}`
		array := `[` + strings.Repeat(elem+",", 31) + elem + `]`
		h.send(url, `{`+strings.Repeat(`"a":`+array+",", 3)+`"a":`+array+`}`)
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{FilterCriteria: &lambdaapi.FilterCriteria{
			Filters: []lambdaapi.Filter{{Pattern: new(`{"body":{"a":{"b":["1"],"c":["1"],"d":["1"],"e":["1"],"z":["2"]}}}`)}},
		}})
		h.quiesce()
		wantBodies(h, rec.bodies(), nil)
		if visible, inFlight := h.depth(url); visible != 0 || inFlight != 0 {
			h.Fatalf("queue holds %d visible and %d in flight, want the message deleted", visible, inFlight)
		}
		// The engine's bound has no AWS counterpart, so the journal says
		// why the message was dropped.
		var advisories []journal.Advisory
		events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 1000}).Events
		for i := range events {
			if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryFilterUndecided {
				advisories = append(advisories, a)
			}
		}
		if len(advisories) != 1 || advisories[0].Resource.String() != *m.EventSourceMappingArn {
			h.Fatalf("filter-undecided advisories = %+v, want one on the mapping %s", advisories, *m.EventSourceMappingArn)
		}
	})
}

// A message sent while the step that opened a batch's window still runs
// fills the batch, which is invoked at once with both messages, without
// waiting for the window to close (lambda-events.md 2.1).
func TestESMMessageDuringStepFillsBatchAtOnce(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{BatchSize: new(int32(2)), MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		h.faultESM(sqsapi.OpHoldVisibility.Descriptor().ID, fault.Latency{Wall: time.Second})
		h.send(url, "first")
		synctest.Wait() // the step is held in the pin of the first message
		h.send(url, "second")
		synctest.Wait()
		time.Sleep(time.Second)
		h.quiesce()
		wantBodies(h, rec.bodies(), [][]string{{"first", "second"}})
	})
}

// A mapping whose queue was deleted keeps polling, backing off to one
// receive per five minutes of emulator time instead of one per second.
func TestESMDeletedQueueBacksOff(t *testing.T) {
	rec := &recorder{}
	runESM(t, rec, func(h *execHarness) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", nil)
		h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{})
		h.quiesce()
		call(h.harness, sqsapi.OpDeleteQueue, &sqsapi.DeleteQueueInput{QueueUrl: url})
		h.quiesce()
		before := len(esmCalls(h, sqsapi.OpReceiveMessage.Descriptor().ID))
		for range 600 {
			h.advancePolled(time.Second)
		}
		// Retries 1, 2, 4, ... 256 s apart make 9 receives in 10 minutes,
		// where a fixed one-second retry makes 600.
		if receives := len(esmCalls(h, sqsapi.OpReceiveMessage.Descriptor().ID)) - before; receives < 1 || receives > 10 {
			h.Fatalf("the poller of a deleted queue received %d times in 10 minutes, want between 1 and 10", receives)
		}
	})
}

// A message held in a batching window when a snapshot is saved is not
// held by anyone once the snapshot is loaded: it returns to the queue when
// its visibility timeout ends.
func TestESMSnapshotDoesNotKeepPins(t *testing.T) {
	rec := &recorder{}
	runESMWithQueues(t, rec, func(h *execHarness, queues *sqs.Service) {
		h.Clock.Freeze()
		url, arn := h.queue("orders", map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNameVisibilityTimeout: "30"})
		m := h.mapQueue(arn, lambdaapi.CreateEventSourceMappingInput{MaximumBatchingWindowInSeconds: new(int32(60))})
		h.quiesce()
		h.send(url, "pinned")
		h.quiesce()
		saved := h.snapshot(queues)
		call(h.harness, lambdaapi.OpDeleteEventSourceMapping, &lambdaapi.DeleteEventSourceMappingInput{UUID: *m.UUID})
		h.quiesce()
		h.load(queues, snapshot{mappings: h.snapshot(queues).mappings, queues: saved.queues})
		h.quiesce()
		h.advancePolled(30 * time.Second)
		if got := h.receiveNow(url); len(got) != 1 || *got[0].Body != "pinned" {
			h.Fatalf("after the load a client received %d messages past the visibility timeout, want the one the snapshot held pinned", len(got))
		}
		wantBodies(h, rec.bodies(), nil)
	})
}

// faultESM fails or delays the next call the pollers make to op.
func (h *execHarness) faultESM(op ref.OperationID, action fault.Action) {
	h.Helper()
	if _, err := h.Faults.Add(fault.Rule{
		Match:   fault.Match{Operations: []ref.OperationID{op}, Origin: fault.InternalOnly},
		Phase:   fault.Request,
		Action:  action,
		Trigger: fault.NextN{N: 1},
	}); err != nil {
		h.Fatal(err)
	}
}

// firstInvocation is the journal's first Invocation event.
func firstInvocation(h *execHarness) journal.Event {
	h.Helper()
	inv := h.invocations()
	if len(inv) == 0 {
		h.Fatal("no invocation was journaled")
	}
	return inv[0]
}

// esmCalls lists the journal events of the calls of op the pollers made.
func esmCalls(h *execHarness, op ref.OperationID) []journal.Event {
	var out []journal.Event
	events := h.Journal.Query(journal.Filter{Operation: op, Limit: 10000}).Events
	for i := range events {
		if call, ok := events[i].Detail.(journal.APICall); ok && esmOrigin(call) {
			out = append(out, events[i])
		}
	}
	return out
}

func esmOrigin(call journal.APICall) bool {
	in, ok := call.Origin.(ref.Internal)
	return ok && in.Via == ref.LambdaESM
}

func journalMode(e journal.Event) string {
	if call, ok := e.Detail.(journal.APICall); ok {
		return call.Mode
	}
	return ""
}

// pollerStates lists the poller state changes journaled on a mapping.
func pollerStates(h *execHarness, mapping string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange, Limit: 1000}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == mapping {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}
