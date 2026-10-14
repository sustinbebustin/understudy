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
}

// visibilityMachine drives one queue through random sends with delays,
// receives, deletes, visibility changes, purges and clock advances against
// a model of each message's visibility (ADR 0005). After every step the
// armed sqs.delay timers of the queue are exactly the messages hidden at
// now and never received, the armed sqs.visibility timers exactly the
// received ones hidden at now, each due when it becomes visible, every
// message has its retention timer, and the queue holds exactly the
// model's messages; every receive
// returns the model's messages, so none is lost, duplicated or brought
// back after a delete or a purge.
type visibilityMachine struct {
	h   *harness
	url string
	arn string
	// messages are the queue's messages in enqueue order.
	messages []modelMessage
	// stale are handles that delete nothing: superseded by a later receive,
	// or of a message deleted already.
	stale   []string
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
		if handle == msg.handle || slices.Contains(m.stale, handle) {
			t.Fatalf("receive of %s reused the receipt handle %s", msg.id, handle)
		}
		if msg.handle != "" {
			m.stale = append(m.stale, msg.handle)
		}
		msg.handle = handle
		msg.visibleAt = now.Add(time.Duration(vt) * time.Second)
	}
}

// deleteCurrent deletes a received message with its latest handle, visible
// again or not (sqs.md 7.2).
func (m *visibilityMachine) deleteCurrent(t *rapid.T) {
	var received []int
	for i, msg := range m.messages {
		if msg.handle != "" {
			received = append(received, i)
		}
	}
	if len(received) == 0 {
		t.Skip("no message has been received")
	}
	i := rapid.SampledFrom(received).Draw(t, "message")
	msg := m.messages[i]
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: msg.handle}); err != nil {
		t.Fatalf("DeleteMessage(%s) = %s: %s", msg.id, err.ID.QueryCode, err.Message)
	}
	m.messages = slices.Delete(m.messages, i, i+1)
	m.deleted[msg.id] = true
	m.stale = append(m.stale, msg.handle)
}

// deleteStale deletes with a handle a later receive superseded, or one of
// a deleted message: it succeeds and deletes nothing.
func (m *visibilityMachine) deleteStale(t *rapid.T) {
	if len(m.stale) == 0 {
		t.Skip("no stale handle")
	}
	handle := rapid.SampledFrom(m.stale).Draw(t, "handle")
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: handle}); err != nil {
		t.Fatalf("DeleteMessage with a stale handle = %s: %s", err.ID.QueryCode, err.Message)
	}
}

// deleteUnknown deletes with a well-formed handle no receive minted: it
// succeeds and deletes nothing.
func (m *visibilityMachine) deleteUnknown(t *rapid.T) {
	handle := "AQEB" + rapid.StringMatching(`[A-Za-z0-9]{32}`).Draw(t, "handle")
	if slices.Contains(m.stale, handle) || slices.ContainsFunc(m.messages, func(msg modelMessage) bool { return msg.handle == handle }) {
		t.Skip("the drawn handle is a minted one")
	}
	if err := m.h.remove(&sqsapi.DeleteMessageInput{QueueUrl: m.url, ReceiptHandle: handle}); err != nil {
		t.Fatalf("DeleteMessage with an unknown handle = %s: %s", err.ID.QueryCode, err.Message)
	}
}

// changeCurrent sets a received message's visibility timeout from now with
// its latest handle, receivable again or not (sqs.md 7.2).
func (m *visibilityMachine) changeCurrent(t *rapid.T) {
	var received []int
	for i, msg := range m.messages {
		if msg.handle != "" {
			received = append(received, i)
		}
	}
	if len(received) == 0 {
		t.Skip("no message has been received")
	}
	i := rapid.SampledFrom(received).Draw(t, "message")
	vt := rapid.Int32Range(0, 60).Draw(t, "visibilityTimeout")
	msg := &m.messages[i]
	if _, err := m.h.Invoke(m.h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: m.url, ReceiptHandle: msg.handle, VisibilityTimeout: vt}); err != nil {
		t.Fatalf("ChangeMessageVisibility(%s) = %s: %s", msg.id, err.ID.QueryCode, err.Message)
	}
	msg.visibleAt = m.h.Clock.Now().Add(time.Duration(vt) * time.Second)
}

// changeStale changes visibility with a handle that matches no message:
// it fails and changes nothing.
func (m *visibilityMachine) changeStale(t *rapid.T) {
	if len(m.stale) == 0 {
		t.Skip("no stale handle")
	}
	handle := rapid.SampledFrom(m.stale).Draw(t, "handle")
	_, err := m.h.Invoke(m.h.Context(), sqsapi.OpChangeMessageVisibility, &sqsapi.ChangeMessageVisibilityInput{QueueUrl: m.url, ReceiptHandle: handle, VisibilityTimeout: 10})
	if err == nil || err.ID.QueryCode != "InvalidParameterValue" {
		t.Fatalf("ChangeMessageVisibility with a stale handle = %v, want InvalidParameterValue", err)
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
		if msg.handle != "" {
			m.stale = append(m.stale, msg.handle)
		}
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
				"send":          m.send,
				"receive":       m.receive,
				"deleteCurrent": m.deleteCurrent,
				"deleteStale":   m.deleteStale,
				"deleteUnknown": m.deleteUnknown,
				"changeCurrent": m.changeCurrent,
				"changeStale":   m.changeStale,
				"purge":         m.purge,
				"advance":       m.advance,
				"":              m.check,
			})
		})
	})
}
