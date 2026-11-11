package sqs_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// modelMessage is what the model knows of one message in the queue.
type modelMessage struct {
	id        string
	sent      clock.Instant
	visibleAt clock.Instant
	// handle is the latest receipt handle, empty before the first receive.
	handle string
	// older are the handles earlier receives issued, each of which still
	// deletes the message and changes its visibility (sqs/R01
	// 03-recv-after-6s, 05-cmv-stale).
	older []string
}

// handles lists every handle of msg, latest first; none before the first
// receive.
func (msg *modelMessage) handles() []string {
	if msg.handle == "" {
		return nil
	}
	return append([]string{msg.handle}, msg.older...)
}

// visibilityMachine drives one queue through random sends with delays,
// receives, deletes, visibility changes, purges and clock advances against
// a model of each message's visibility (ADR 0005). After every step the
// armed sqs.delay timers of the queue are exactly the messages hidden at
// now and never received, the armed sqs.visibility timers exactly the
// received ones hidden at now, each due when it becomes visible, every
// message has its retention timer, and the queue holds exactly the
// model's messages; every receive returns the model's messages, so none is
// lost, duplicated or brought back after a delete or a purge.
type visibilityMachine struct {
	h   *harness
	url string
	arn string
	// messages are the queue's messages in enqueue order.
	messages []modelMessage
	// gone are the handles of messages deleted already, which delete
	// nothing.
	gone    []string
	deleted map[string]bool
	// purged is when the last PurgeQueue ran; purgedOnce says one has.
	purged     clock.Instant
	purgedOnce bool
}

func newVisibilityMachine(t *rapid.T, h *harness, name string) *visibilityMachine {
	out, err := h.create(&sqsapi.CreateQueueInput{QueueName: name})
	if err != nil || out.QueueUrl == nil {
		t.Fatalf("CreateQueue(%s) = %+v, %+v", name, out, err)
	}
	return &visibilityMachine{
		h:       h,
		url:     *out.QueueUrl,
		arn:     "arn:aws:sqs:us-east-1:000000000000:" + name,
		deleted: map[string]bool{},
	}
}

func (m *visibilityMachine) send(t *rapid.T) {
	delay := rapid.Ptr(rapid.Int32Range(0, 30), true).Draw(t, "delay")
	out, err := m.h.send(&sqsapi.SendMessageInput{QueueUrl: m.url, MessageBody: "m", DelaySeconds: delay})
	if err != nil {
		t.Fatalf("SendMessage = %s: %s", err.ID.QueryCode, err.Message)
	}
	var d int32
	if delay != nil {
		d = *delay
	}
	now := m.h.Clock.Now()
	m.messages = append(m.messages, modelMessage{id: val(out.MessageId), sent: now, visibleAt: now.Add(time.Duration(d) * time.Second)})
}

func (m *visibilityMachine) receive(t *rapid.T) {
	vt := rapid.Int32Range(0, 60).Draw(t, "visibilityTimeout")
	limit := rapid.Int32Range(1, 10).Draw(t, "maxNumberOfMessages")
	out, err := m.h.receive(&sqsapi.ReceiveMessageInput{
		QueueUrl: m.url, MaxNumberOfMessages: &limit, VisibilityTimeout: &vt, WaitTimeSeconds: new(int32(0)),
	})
	if err != nil {
		t.Fatalf("ReceiveMessage = %s: %s", err.ID.QueryCode, err.Message)
	}
	now := m.h.Clock.Now()
	var want []int
	for i, msg := range m.messages {
		if len(want) < int(limit) && msg.visibleAt.Compare(now) <= 0 {
			want = append(want, i)
		}
	}
	got := make([]string, len(out.Messages))
	for i, msg := range out.Messages {
		got[i] = val(msg.MessageId)
		if m.deleted[got[i]] {
			t.Fatalf("ReceiveMessage returned %s, which was deleted", got[i])
		}
	}
	wantIDs := make([]string, len(want))
	for i, j := range want {
		wantIDs[i] = m.messages[j].id
	}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("ReceiveMessage(max %d) at %s = %q, want the visible messages %q", limit, now, got, wantIDs)
	}
	for i, j := range want {
		msg := &m.messages[j]
		handle := val(out.Messages[i].ReceiptHandle)
		if m.minted(handle) {
			t.Fatalf("receive of %s reused the receipt handle %s", msg.id, handle)
		}
		if msg.handle != "" {
			msg.older = append(msg.older, msg.handle)
		}
		msg.handle = handle
		msg.visibleAt = now.Add(time.Duration(vt) * time.Second)
	}
}

// minted reports whether a receive issued handle.
func (m *visibilityMachine) minted(handle string) bool {
	return slices.Contains(m.gone, handle) || slices.ContainsFunc(m.messages, func(msg modelMessage) bool { return slices.Contains(msg.handles(), handle) })
}

// drawHandle draws a received message and one of its handles, the latest
// or an older one.
func (m *visibilityMachine) drawHandle(t *rapid.T) (i int, handle string) {
	var received []int
	for i, msg := range m.messages {
		if msg.handle != "" {
			received = append(received, i)
		}
	}
	if len(received) == 0 {
		t.Skip("no message has been received")
	}
	i = rapid.SampledFrom(received).Draw(t, "message")
	return i, rapid.SampledFrom(m.messages[i].handles()).Draw(t, "handle")
}

// deleteReceived deletes a received message with any of its handles,
// visible again or not (sqs.md 7.2; sqs/R01 03-recv-after-6s).
func (m *visibilityMachine) deleteReceived(t *rapid.T) {
	i, handle := m.drawHandle(t)
	msg := m.messages[i]
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: handle}); err != nil {
		t.Fatalf("DeleteMessage(%s) = %s: %s", msg.id, err.ID.QueryCode, err.Message)
	}
	m.messages = slices.Delete(m.messages, i, i+1)
	m.deleted[msg.id] = true
	m.gone = append(m.gone, msg.handles()...)
}

// deleteGone deletes with a handle of a deleted message: it succeeds and
// deletes nothing (sqs/R01 04-delete-stale-gone).
func (m *visibilityMachine) deleteGone(t *rapid.T) {
	if len(m.gone) == 0 {
		t.Skip("no handle of a deleted message")
	}
	handle := rapid.SampledFrom(m.gone).Draw(t, "handle")
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: handle}); err != nil {
		t.Fatalf("DeleteMessage with a deleted message's handle = %s: %s", err.ID.QueryCode, err.Message)
	}
}

// deleteUnknown deletes with a well-formed handle no receive minted: it
// succeeds and deletes nothing.
func (m *visibilityMachine) deleteUnknown(t *rapid.T) {
	handle := "AQEB" + rapid.StringMatching(`[A-Za-z0-9+/]{405}[AQgw]==`).Draw(t, "handle")
	if m.minted(handle) {
		t.Skip("the drawn handle is a minted one")
	}
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: handle}); err != nil {
		t.Fatalf("DeleteMessage with an unknown handle = %s: %s", err.ID.QueryCode, err.Message)
	}
}

// changeReceived sets a received message's visibility timeout from now
// with any of its handles, receivable again or not (sqs.md 7.2; sqs/R01
// 05-cmv-stale).
func (m *visibilityMachine) changeReceived(t *rapid.T) {
	i, handle := m.drawHandle(t)
	vt := rapid.Int32Range(0, 60).Draw(t, "visibilityTimeout")
	msg := &m.messages[i]
	if _, err := m.h.Invoke(m.h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: m.url, ReceiptHandle: handle, VisibilityTimeout: vt}); err != nil {
		t.Fatalf("ChangeMessageVisibility(%s) = %s: %s", msg.id, err.ID.QueryCode, err.Message)
	}
	msg.visibleAt = m.h.Clock.Now().Add(time.Duration(vt) * time.Second)
}

// changeGone changes visibility with a handle of a deleted message: it
// fails and changes nothing (sqs/R01 05-cmv-stale-deleted).
func (m *visibilityMachine) changeGone(t *rapid.T) {
	if len(m.gone) == 0 {
		t.Skip("no handle of a deleted message")
	}
	handle := rapid.SampledFrom(m.gone).Draw(t, "handle")
	_, err := m.h.Invoke(m.h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: m.url, ReceiptHandle: handle, VisibilityTimeout: 10})
	if err == nil || err.ID.QueryCode != "InvalidParameterValue" {
		t.Fatalf("ChangeMessageVisibility with a deleted message's handle = %v, want InvalidParameterValue", err)
	}
}

// purge deletes every message, delayed and in flight ones too, once per
// 60 s of emulator time (sqs.md 5.3).
func (m *visibilityMachine) purge(t *rapid.T) {
	now := m.h.Clock.Now()
	_, err := m.h.Invoke(m.h.Context(), sqsapi.OpPurgeQueue, &sqsapi.PurgeQueueInput{QueueUrl: m.url})
	if m.purgedOnce && now.Before(m.purged.Add(time.Minute)) {
		if err == nil || err.ID.QueryCode != "AWS.SimpleQueueService.PurgeQueueInProgress" {
			t.Fatalf("PurgeQueue %s after the last = %v, want PurgeQueueInProgress", now.Sub(m.purged), err)
		}
		return
	}
	if err != nil {
		t.Fatalf("PurgeQueue = %s: %s", err.ID.QueryCode, err.Message)
	}
	for _, msg := range m.messages {
		m.deleted[msg.id] = true
		m.gone = append(m.gone, msg.handles()...)
	}
	m.messages = nil
	m.purged, m.purgedOnce = now, true
}

func (m *visibilityMachine) advance(t *rapid.T) {
	d := time.Duration(rapid.IntRange(0, 45).Draw(t, "seconds")) * time.Second
	if _, err := m.h.Clock.Advance(d); err != nil {
		t.Fatal(err)
	}
}

func (m *visibilityMachine) check(t *rapid.T) {
	now := m.h.Clock.Now()
	delayed, hidden := map[string]clock.Instant{}, map[string]clock.Instant{}
	for _, msg := range m.messages {
		if msg.visibleAt.Compare(now) <= 0 {
			continue
		}
		if msg.handle == "" {
			delayed[m.arn+"/"+msg.id] = msg.visibleAt
		} else {
			hidden[m.arn+"/"+msg.id] = msg.visibleAt
		}
	}
	retention := map[string]clock.Instant{}
	for _, msg := range m.messages {
		retention[m.arn+"/"+msg.id] = msg.sent.Add(4 * 24 * time.Hour)
	}
	m.checkTimers(t, sqs.TimerDelay, delayed)
	m.checkTimers(t, sqs.TimerVisibility, hidden)
	m.checkTimers(t, sqs.TimerRetention, retention)
	summary := fmt.Sprintf("%d messages", len(m.messages))
	res := m.h.svc.Resources(m.h.Scope)
	i := slices.IndexFunc(res, func(r kernel.Resource) bool { return r.ARN.String() == m.arn })
	if i < 0 || res[i].Summary != summary {
		t.Fatalf("Resources = %+v, want %s with %s", res, m.arn, summary)
	}
}

// checkTimers fails unless the armed timers of kind for the queue are
// exactly want, by id and due instant.
func (m *visibilityMachine) checkTimers(t *rapid.T, kind clock.TimerKind, want map[string]clock.Instant) {
	got := map[string]clock.Instant{}
	for _, a := range m.h.Kernel.Timers().Scheduler().Snapshot() {
		if a.Key.Kind == kind && strings.HasPrefix(a.Key.ID, m.arn+"/") {
			got[a.Key.ID] = a.Due
		}
	}
	if !maps.EqualFunc(got, want, func(a, b clock.Instant) bool { return a.Compare(b) == 0 }) {
		t.Fatalf("at %s armed %s timers = %v, want %v", m.h.Clock.Now(), kind, got, want)
	}
}

// receivedTwice sends one message to url, whose VisibilityTimeout is 5,
// and receives it twice, the second time after the first timeout ran out,
// as sqs/R01 steps 2 and 3 do. It returns the older handle and the latest.
func receivedTwice(h *harness, url string, send func()) (older, latest string) {
	h.Helper()
	send()
	first := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
	h.advance(6 * time.Second)
	second := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: url})
	if len(first) != 1 || len(second) != 1 {
		h.Fatalf("receives = %d then %d messages, want one each", len(first), len(second))
	}
	return val(first[0].ReceiptHandle), val(second[0].ReceiptHandle)
}

// sqs/R01 03-delete-stale, 03-recv-after-stale-delete and 03-recv-after-6s:
// on a standard queue an older handle of a message received again since
// deletes it, so no receive finds it, even after the visibility timeout.
func TestOlderStandardHandleDeletesTheMessage(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("older-delete", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "5"})
		older, _ := receivedTwice(h, url, func() { h.mustSend(url, "m") })
		if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: older}); err != nil {
			h.Fatalf("DeleteMessage with the older handle = %s: %s", err.ID.QueryCode, err.Message)
		}
		if got := h.receiveBodies(url, 10); len(got) != 0 {
			h.Fatalf("receive after the delete = %q, want none", got)
		}
		h.advance(6 * time.Second)
		if got := h.receiveBodies(url, 10); len(got) != 0 {
			h.Fatalf("receive after the visibility timeout = %q, want none", got)
		}
	})
}

// sqs/R01 05-cmv-stale and 06-cmv-batch: on a standard queue a visibility
// change with an older handle of a message received again since answers
// 200, alone and as a batch entry, and changes the message's visibility.
func TestOlderStandardHandleChangesVisibility(t *testing.T) {
	run(t, func(h *harness) {
		url := h.queue("older-change", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "5"})
		older, _ := receivedTwice(h, url, func() { h.mustSend(url, "m") })
		if _, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: url, ReceiptHandle: older, VisibilityTimeout: 30}); err != nil {
			h.Fatalf("ChangeMessageVisibility with the older handle = %s: %s", err.ID.QueryCode, err.Message)
		}
		h.advance(6 * time.Second)
		if got := h.receiveBodies(url, 10); len(got) != 0 {
			h.Fatalf("receive 6 s into the changed 30 s timeout = %q, want none", got)
		}
		out, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibilityBatch, &sqsapi.ChangeMessageVisibilityBatchInput{QueueUrl: url, Entries: []sqsapi.ChangeMessageVisibilityBatchRequestEntry{
			{Id: "stale", ReceiptHandle: older, VisibilityTimeout: new(int32(0))},
		}})
		if err != nil || len(out.Failed) != 0 || len(out.Successful) != 1 || out.Successful[0].Id != "stale" {
			h.Fatalf("ChangeMessageVisibilityBatch with the older handle = %+v, %v", out, err)
		}
		if got := h.receiveBodies(url, 10); len(got) != 1 {
			h.Fatalf("receive after a zero timeout = %q, want the message", got)
		}
	})
}

// sqs/R01 07-fifo-delete-h1-after-rereceive: on a FIFO queue an older
// handle of a message received again since has expired, for DeleteMessage
// and, as sqs.fifo.receipt-expiry infers, ChangeMessageVisibility; the
// latest still deletes (07-fifo-delete-h2).
func TestOlderFIFOHandleHasExpired(t *testing.T) {
	run(t, func(h *harness) {
		url := h.fifoQueue("older.fifo", map[sqsapi.QueueAttributeName]string{"ContentBasedDeduplication": "true", "VisibilityTimeout": "5"})
		older, latest := receivedTwice(h, url, func() { h.sendTo(url, "g", "m", nil) })
		expired := "Value " + older + " for parameter ReceiptHandle is invalid. Reason: The receipt handle has expired."
		wantError(h.T, h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: older}), "InvalidParameterValue", expired)
		_, err := h.Invoke(h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: url, ReceiptHandle: older, VisibilityTimeout: 10})
		wantError(h.T, err, "InvalidParameterValue", expired)
		if err := h.remove(&sqsapi.DeleteMessageInput{QueueUrl: url, ReceiptHandle: latest}); err != nil {
			h.Fatalf("DeleteMessage with the latest handle = %s: %s", err.ID.QueryCode, err.Message)
		}
	})
}

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

func TestVisibilityStateMachine(t *testing.T) {
	run(t, func(h *harness) {
		queues := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			queues++
			m := newVisibilityMachine(t, h, fmt.Sprintf("machine-%d", queues))
			t.Repeat(map[string]func(*rapid.T){
				"send":           m.send,
				"receive":        m.receive,
				"deleteReceived": m.deleteReceived,
				"deleteGone":     m.deleteGone,
				"deleteUnknown":  m.deleteUnknown,
				"changeReceived": m.changeReceived,
				"changeGone":     m.changeGone,
				"purge":          m.purge,
				"advance":        m.advance,
				"":               m.check,
			})
		})
	})
}
