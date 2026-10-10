package journal

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// benchModes are the journals each benchmark measures: in memory only, and
// also appending to a file.
var benchModes = []string{"memory", "file"}

// benchStates are the states each benchmark starts from. "empty" never
// evicts. "full-ring-open-call" is the steady state of a running emulator:
// the ring is at Capacity, so every recorded event evicts the oldest, while
// one call that began before all of them, such as a long poll, stays open
// and holds the lowest id.
var benchStates = []string{"empty", "full-ring-open-call"}

// benchJournal returns a journal at the default bounds for mode, brought to
// state by beginning open and then calling record Capacity times.
func benchJournal(b *testing.B, mode, state string, open Event, record func(*Journal) error) *Journal {
	b.Helper()
	var o Options
	if mode == "file" {
		o.File = &FileOptions{Path: filepath.Join(b.TempDir(), "journal.ndjson")}
	}
	j := newTestJournal(b, o)
	b.Cleanup(func() {
		if err := j.Close(); err != nil {
			b.Error(err)
		}
	})
	if state == "full-ring-open-call" {
		if _, err := j.Begin(open); err != nil {
			b.Fatal(err)
		}
		for range DefaultCapacity {
			if err := record(j); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportAllocs()
	return j
}

// runBench measures record in every mode and state.
func runBench(b *testing.B, open Event, record func(*Journal) error) {
	b.Helper()
	for _, mode := range benchModes {
		for _, state := range benchStates {
			b.Run(mode+"/"+state, func(b *testing.B) {
				j := benchJournal(b, mode, state, open, record)
				for b.Loop() {
					if err := record(j); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkBeginEnd measures the journal's share of one API call.
func BenchmarkBeginEnd(b *testing.B) {
	queue := arn("sqs", "orders")
	input, err := NewRedacted([]byte(`{"QueueUrl":"http://127.0.0.1:4566/000000000000/orders","MessageBody":"***"}`))
	if err != nil {
		b.Fatal(err)
	}
	call := Event{Scope: scope(), Detail: APICall{
		Op:        ref.OperationID{Service: "sqs", Name: "SendMessage"},
		RequestID: "req-bench",
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Principal: "arn:aws:iam::000000000000:root",
		Input:     input,
	}}
	runBench(b, call, func(j *Journal) error {
		id, err := j.Begin(call)
		if err != nil {
			return err
		}
		return j.End(id, OK{}, time.Millisecond, queue)
	})
}

// BenchmarkQueryResourceGlob measures a wildcard resource query that matches
// nothing, the case that would read every event, on a full ring whose events
// touch 100 queues. Query holds the journal lock, so this is time every call
// in flight waits.
func BenchmarkQueryResourceGlob(b *testing.B) {
	j := newTestJournal(b, Options{})
	topic := arn("sns", "events")
	for i := range DefaultCapacity {
		if _, err := j.Append(Event{Scope: scope(), Detail: Delivery{
			From: topic, To: arn("sqs", fmt.Sprintf("orders-%d", i%100)), Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{},
		}}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if page := j.Query(Filter{ResourceGlob: "arn:aws:sqs:*:billing"}); len(page.Events) != 0 {
			b.Fatalf("Query found %d events, want none", len(page.Events))
		}
	}
}

// BenchmarkAppend measures recording one cross-service delivery.
func BenchmarkAppend(b *testing.B) {
	e := Event{Scope: scope(), Detail: Delivery{
		From: arn("sns", "events"), To: arn("sqs", "orders"), Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{},
	}}
	runBench(b, callEvent(0), func(j *Journal) error {
		_, err := j.Append(e)
		return err
	})
}
