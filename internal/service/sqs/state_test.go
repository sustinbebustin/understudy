package sqs

import (
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// bodiesOf lists the ids and bodies of ms, what the tests below compare.
func bodiesOf(ms []Message) []string {
	out := make([]string, len(ms))
	for i := range ms {
		out[i] = string(ms[i].ID) + "=" + ms[i].Body
	}
	return out
}

// A working copy's changes (Queue.clone) reach the stored record only when
// the write stores the copy back: rounds of appends, changes in place and
// removals, each committed or discarded, leave the stored messages exactly
// as a plain slice model says, and a discarded round changes nothing.
func TestWorkingCopiesNeverChangeTheStoredQueue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var stored Queue
		var model []Message
		next := 0
		for round := range rapid.IntRange(1, 8).Draw(t, "rounds") {
			before := bodiesOf(stored.Messages)
			work, want := stored.clone(), slices.Clone(model)
			for range rapid.IntRange(0, 12).Draw(t, "ops") {
				switch op := rapid.IntRange(0, 2).Draw(t, "op"); {
				case op == 0 || len(want) == 0:
					m := Message{ID: ids.MessageID("m" + strconv.Itoa(next)), Body: "sent"}
					next++
					work.Messages = append(work.Messages, m)
					want = append(want, m)
				case op == 1:
					i := rapid.IntRange(0, len(want)-1).Draw(t, "change")
					body := "changed in round " + strconv.Itoa(round)
					work.message(i).Body = body
					want[i].Body = body
				default:
					var at []int
					for i := range want {
						if rapid.Bool().Draw(t, "remove") {
							at = append(at, i)
						}
					}
					work.remove(at)
					for _, i := range slices.Backward(at) {
						want = slices.Delete(want, i, i+1)
					}
				}
			}
			if got := bodiesOf(stored.Messages); !slices.Equal(got, before) {
				t.Fatalf("round %d: the working copy changed the stored record: %q, was %q", round, got, before)
			}
			if got := bodiesOf(work.Messages); !slices.Equal(got, bodiesOf(want)) {
				t.Fatalf("round %d: working copy = %q, want %q", round, got, bodiesOf(want))
			}
			if rapid.Bool().Draw(t, "commit") {
				stored, model = work, want
			}
		}
	})
}

// A queue reference in the state is stored as the queue's ARN and read
// back as the same queue; one that is not an SQS queue ARN fails the
// load instead of quietly naming no queue.
func TestQueueReferencesLoadOnlyAsQueueARNs(t *testing.T) {
	c := codec()
	policy := func(target string) []byte {
		return []byte(`{"queues":{"src":{"name":"src","created":"2026-01-01T00:00:00Z","modified":"2026-01-01T00:00:00Z","attributes":{"maximumMessageSize":1048576,"messageRetentionPeriod":345600,"visibilityTimeout":30,"sqsManagedSseEnabled":true,"kmsDataKeyReusePeriodSeconds":300,"redrivePolicy":{"deadLetterTargetArn":"` + target + `","maxReceiveCount":3}}}}}`)
	}
	var st State
	if err := c.Unmarshal(policy("arn:aws:sqs:us-east-1:000000000000:dlq"), &st); err != nil {
		t.Fatal(err)
	}
	if got := st.Queues["src"].Attributes.RedrivePolicy.DeadLetterTarget.arn().String(); got != "arn:aws:sqs:us-east-1:000000000000:dlq" {
		t.Errorf("loaded target = %s", got)
	}
	for _, bad := range []string{"dlq", "arn:aws:sns:us-east-1:000000000000:dlq", "arn:aws:sqs:us-east-1:0:dlq", "arn:aws:sqs:us-east-1:000000000000:dlq:extra"} {
		if err := c.Unmarshal(policy(bad), &State{}); err == nil {
			t.Errorf("a redrive target of %q loaded", bad)
		}
	}
}

// DedupCache answers like a plain map with windows, whatever generations
// and prunes it went through, through a snapshot round trip too, and a
// working copy's additions reach the stored cache only when committed. As
// SendMessage does, a key is added only while no open entry holds it.
func TestDedupCacheActsAsAMapOfWindows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := clock.FromWire(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		now := start
		var stored DedupCache
		model := map[string]Dedup{}
		keys := rapid.IntRange(1, 3*dedupGenerationSize).Draw(t, "keys")
		check := func(c *DedupCache, m map[string]Dedup, what string) {
			for k := range keys {
				key := strconv.Itoa(k)
				d, open := m[key]
				open = open && now.Before(d.Expires)
				got, ok := c.get(key, now)
				if ok != open || (ok && got != d) {
					t.Fatalf("%s: get(%s) at %s = %+v, %v; want %+v, %v", what, key, now, got, ok, d, open)
				}
			}
		}
		for range rapid.IntRange(1, 6).Draw(t, "rounds") {
			work, want := stored, map[string]Dedup{}
			maps.Copy(want, model)
			work.lend()
			for range rapid.IntRange(0, 4*dedupGenerationSize).Draw(t, "ops") {
				now = now.Add(time.Duration(rapid.IntRange(0, 3).Draw(t, "advance")) * time.Second)
				key := strconv.Itoa(rapid.IntRange(0, keys-1).Draw(t, "key"))
				if _, open := work.get(key, now); open {
					continue
				}
				d := Dedup{MessageID: ids.MessageID(key + "@" + now.String()), Expires: now.Add(time.Duration(rapid.IntRange(1, 600).Draw(t, "window")) * time.Second)}
				work.put(key, d)
				want[key] = d
				if rapid.IntRange(0, 9).Draw(t, "prune") == 0 {
					work.prune(now)
				}
			}
			check(&work, want, "working copy")
			if rapid.Bool().Draw(t, "commit") {
				stored, model = work, want
			}
			check(&stored, model, "stored")
		}
		c := codec()
		data, err := c.Marshal(&State{Queues: map[string]Queue{"q.fifo": {Name: "q.fifo", FIFO: &FIFO{Dedup: stored}}}})
		if err != nil {
			t.Fatal(err)
		}
		var loaded State
		if err := c.Unmarshal(data, &loaded); err != nil {
			t.Fatal(err)
		}
		dedup := loaded.Queues["q.fifo"].FIFO.Dedup
		check(&dedup, model, "loaded")
	})
}
