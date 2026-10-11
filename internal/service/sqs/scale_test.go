package sqs_test

import (
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
)

// allocatedPerCall is the heap bytes each of n calls to fn allocates, on
// average.
func allocatedPerCall(n uint64, fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		fn()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / n
}

// A send copies none of the messages already queued, so a send to a queue
// holding a backlog costs what a send to an empty queue costs and filling
// a queue is linear: the brief promises backlog tests run instantly. A
// copy of the backlog per send costs about 2 MB here.
func TestSendCostDoesNotGrowWithTheBacklog(t *testing.T) {
	const backlog, sample = 10_000, 200
	for _, tt := range []struct {
		name  string
		attrs map[sqsapi.QueueAttributeName]string
	}{
		{"standard", nil},
		{"fifo", map[sqsapi.QueueAttributeName]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				name := "backlog"
				if tt.attrs != nil {
					name += ".fifo"
				}
				url := h.queue(name, tt.attrs)
				group := "g"
				sent := 0
				send := func() {
					in := &sqsapi.SendMessageInput{QueueUrl: url, MessageBody: "m" + strconv.Itoa(sent)}
					if tt.attrs != nil {
						in.MessageGroupId = &group
					}
					if _, err := h.send(in); err != nil {
						h.Fatalf("SendMessage = %s: %s", err.ID.QueryCode, err.Message)
					}
					sent++
				}
				empty := allocatedPerCall(sample, send)
				for range backlog {
					send()
				}
				full := allocatedPerCall(sample, send)
				if full > 4*empty {
					h.Errorf("a send to a queue of %d messages allocates %d bytes, one to an empty queue %d: sends copy the backlog", backlog, full, empty)
				}
			})
		})
	}
}

// sqs.md 9.3: a task without a rate moves the whole dead-letter queue in
// one step. The step takes the moved messages out of the source in one
// pass, so moving a backlog is linear; removing them one at a time made
// this test take tens of seconds.
func TestMoveTaskMovesABacklogInOnePass(t *testing.T) {
	const backlog = 20_000
	start := time.Now()
	run(t, func(h *harness) {
		dlq := h.queue("dlq", nil)
		h.queue("src", map[sqsapi.QueueAttributeName]string{"RedrivePolicy": redrivePolicy("dlq", 1)})
		dest := h.queue("dest", nil)
		for i := 0; i < backlog; i += 10 {
			entries := make([]sqsapi.SendMessageBatchRequestEntry, 10)
			for k := range entries {
				id := strconv.Itoa(k)
				entries[k] = entry(id, "m"+strconv.Itoa(i+k))
			}
			if out, err := h.sendBatch(dlq, entries...); err != nil || len(out.Failed) > 0 {
				h.Fatalf("SendMessageBatch = %+v, %v", out, err)
			}
		}
		if _, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", DestinationArn: new(arnPrefix + "dest")}); err != nil {
			h.Fatal(err.Message)
		}
		task := h.tasks("dlq", 1)[0]
		if val(task.Status) != "COMPLETED" || task.ApproximateNumberOfMessagesMoved != backlog {
			h.Fatalf("task = %+v, want COMPLETED with %d moved", task, backlog)
		}
		if h.count(dlq) != "0" || h.count(dest) != strconv.Itoa(backlog) {
			h.Errorf("dead-letter queue holds %s and the destination %s, want 0 and %d", h.count(dlq), h.count(dest), backlog)
		}
	})
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("filling and moving %d messages took %s of wall time, want well under 20s", backlog, took)
	}
}
