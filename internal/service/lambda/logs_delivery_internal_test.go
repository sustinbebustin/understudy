package lambda

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
)

// Requests carry every message once, in order, and each stays within
// PutLogEvents' limits unless a single message alone exceeds the byte one.
func TestLogRequestsKeepOrderWithinTheBatchLimits(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		messages := rapid.SliceOfN(rapid.StringN(0, 300_000, -1), 0, 30).Draw(rt, "messages")
		requests := logRequests(messages, 42)
		var flat []string
		for i, events := range requests {
			if len(events) == 0 || len(events) > maxLogBatchEvents {
				rt.Fatalf("request %d holds %d events", i, len(events))
			}
			size := 0
			for _, e := range events {
				if e.Timestamp != 42 {
					rt.Fatalf("event stamped %d, want the batch's 42", e.Timestamp)
				}
				size += len(e.Message) + logEventOverhead
				flat = append(flat, e.Message)
			}
			if size > maxLogBatchBytes && len(events) > 1 {
				rt.Fatalf("request %d is %d bytes over %d events", i, size, len(events))
			}
		}
		if !slices.Equal(flat, messages) && (len(flat) != 0 || len(messages) != 0) {
			rt.Fatalf("requests carry %d messages out of order or lost, want %d", len(flat), len(messages))
		}
	})
}

// A line the launcher cut at its limit, with no newline of its own, stays
// an event one PutLogEvents request can carry.
func TestALineCutAtTheLimitFitsOneRequest(t *testing.T) {
	for _, n := range []int{launcher.MaxLineBytes - 1, launcher.MaxLineBytes} {
		message := lineMessage(strings.Repeat("x", n))
		for i, events := range logRequests([]string{message, "next\n"}, 0) {
			size := 0
			for _, e := range events {
				size += len(e.Message) + logEventOverhead
			}
			if size > maxLogBatchBytes {
				t.Errorf("a %d-byte line: request %d is %d bytes, over %d", n, i, size, maxLogBatchBytes)
			}
		}
	}
}

// Output of an open invocation is cut once it reaches a request's size,
// before the invocation ends, and the tail X-Amz-Log-Result carries is
// still the last maxTail bytes of the invocation's own log.
func TestOpenInvocationOutputIsCutAtTheRequestSize(t *testing.T) {
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}})
	if err != nil {
		t.Fatal(err)
	}
	s := &logStream{clock: clk}
	parent := &kernel.Call{}
	s.begin(true, "INIT_START\n", "START\n", parent)
	line := strings.Repeat("y", 1000) + "\n"
	deliverAt := -1
	for i := range 2000 {
		if s.add(line) && deliverAt < 0 {
			deliverAt = i
		}
	}
	if deliverAt < 0 {
		t.Fatal("2 MB of output during an invocation was never cut")
	}
	s.mu.Lock()
	queued, held := len(s.queue), s.pendingBytes
	s.mu.Unlock()
	if queued == 0 || held >= maxLogBatchBytes {
		t.Errorf("after 2 MB of output: %d batches queued, %d bytes held; want a batch queued and less than a request held", queued, held)
	}
	tail, _ := s.end("END\n", "REPORT\n")
	want := strings.Repeat(line, 5)[5*len(line)-(maxTail-len("END\nREPORT\n")):] + "END\nREPORT\n"
	if string(tail) != want {
		t.Errorf("tail = %d bytes ending %q, want the last %d bytes of the invocation's log", len(tail), lastBytes(tail, 40), maxTail)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.queue {
		if b.parent != parent {
			t.Fatal("a batch is written on behalf of another call than the invocation's")
		}
	}
}

func TestLogRequestsSplitAtTenThousandEvents(t *testing.T) {
	messages := make([]string, 25_000)
	got := logRequests(messages, 0)
	if len(got) != 3 || len(got[0]) != 10_000 || len(got[2]) != 5_000 {
		t.Errorf("25,000 empty messages split into %d requests, want 10,000 + 10,000 + 5,000", len(got))
	}
}
