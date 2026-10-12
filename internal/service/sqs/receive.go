package sqs

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// maxVisibility is the longest visibility timeout, and the ceiling of a
// message's visibility counted from its receive (sqs.md 7.2).
const maxVisibility = 43200

// bindReceives binds ReceiveMessage.
func (s *Service) bindReceives(b sqsapi.Binder) {
	b.ReceiveMessage(s.receiveMessage, registry.Full{},
		kernel.WithCheck(checkReceiveMessage),
		queueByURL(func(in *sqsapi.ReceiveMessageInput) string { return in.QueueUrl }))
}

// checkReceiveMessage checks the VisibilityTimeout range (sqs.md 7.1) and
// the form of ReceiveRequestAttemptId (sqs.md 8.3), whose hand texts no
// overlay template renders.
func checkReceiveMessage(in *sqsapi.ReceiveMessageInput) *awserr.Error {
	if v := in.VisibilityTimeout; v != nil && (*v < 0 || *v > maxVisibility) {
		return errVisibilityTimeout(*v)
	}
	if id := in.ReceiveRequestAttemptId; id != nil && !validFIFOID(*id) {
		return errInvalidAttemptID(*id)
	}
	return nil
}

// receiveParams are a ReceiveMessage's limits; nil pointers take the
// queue's attribute.
type receiveParams struct {
	max        int
	wait       *int32
	visibility *int32
	attempt    *string
}

// received is one receive attempt: the messages it took, copied out of the
// state, the wait time that applies and the dead-letter queue it moved
// messages to, nil when it moved none.
type received struct {
	messages     []Message
	wait         int32
	deadLettered *aws.ARN
}

// receiveMessage is the long poll of ARCHITECTURE.md 8: subscribe, try a
// receive, and wait for a send or a visibility expiry until WaitTimeSeconds
// of emulator time passes. The same wait on the wall clock caps it, so a
// frozen clock still ends the HTTP request (sqs.md 13.11).
func (s *Service) receiveMessage(ctx context.Context, c *kernel.Call, in *sqsapi.ReceiveMessageInput) (*sqsapi.ReceiveMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	p := receiveParams{max: 1, wait: in.WaitTimeSeconds, visibility: in.VisibilityTimeout, attempt: in.ReceiveRequestAttemptId}
	if in.MaxNumberOfMessages != nil {
		p.max = int(*in.MaxNumberOfMessages)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var expired <-chan struct{}
	for {
		woke := s.k.Waiters().Wait(ctx, q.waitKey())
		got, err := writeQueue(s, c, q, func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (received, *awserr.Error) {
			return s.receive(c, st, q, queue, p, now, tx), nil
		})
		if err != nil {
			return nil, err
		}
		if got.deadLettered != nil {
			c.Touch(*got.deadLettered)
		}
		if len(got.messages) > 0 {
			return &sqsapi.ReceiveMessageOutput{Messages: render(got.messages, in)}, nil
		}
		if expired == nil {
			wait := seconds(got.wait)
			expired = s.k.Clock().Until(ctx, clock.Deadline{Emu: c.Admitted().Add(wait), WallCap: s.k.Clock().Wall().Add(wait)})
		}
		select {
		case <-woke:
		case <-expired:
			if ctx.Err() == nil {
				return &sqsapi.ReceiveMessageOutput{}, nil
			}
			return nil, awserr.RequestAborted()
		case <-ctx.Done():
			return nil, awserr.RequestAborted()
		}
	}
}

// receive drops the messages past retention, then answers a FIFO retry of
// a ReceiveRequestAttemptId with what that attempt received, or takes up
// to p.max receivable messages: each gets a new receipt handle and hides
// for the visibility timeout, and a message whose redrive policy it would
// exceed moves to the dead-letter queue instead (sqs.md 9.2). It runs
// inside kernel.Write.
func (s *Service) receive(c *kernel.Call, st *State, q queueRef, queue *Queue, p receiveParams, now clock.Instant, tx *kernel.Tx) received {
	out := received{wait: queue.Attributes.ReceiveMessageWaitTimeSeconds}
	if p.wait != nil {
		out.wait = *p.wait
	}
	vt := queue.Attributes.VisibilityTimeout
	if p.visibility != nil {
		vt = *p.visibility
	}
	changed := s.dropExpired(tx, q, queue, now)
	fifo := queue.FIFO
	if fifo != nil && p.attempt != nil {
		changed = fifo.pruneDedup(now) || changed
		again, forgot := s.retryAttempt(tx, q, queue, *p.attempt, vt, now)
		if again != nil {
			out.messages = again
			return out
		}
		changed = forgot || changed
	}
	take, move := plan(st, q, queue, p.max, now)
	for _, i := range take {
		m := queue.message(i)
		m.receive(s.ids.ReceiptHandle(), now)
		m.VisibleAt = now.Add(seconds(vt))
		s.armVisibility(tx, q, m, now)
		out.messages = append(out.messages, m.clone())
	}
	if len(move) > 0 {
		out.deadLettered = new(s.deadLetter(c, st, q, queue, move, now, tx))
	}
	if fifo != nil && p.attempt != nil && len(out.messages) > 0 {
		fifo.remember(*p.attempt, out.messages, now)
	}
	if !changed && len(take) == 0 && len(move) == 0 {
		// An empty poll changes nothing, so an idle long poller never marks
		// the cell dirty.
		tx.Unchanged()
	}
	return out
}

// dropExpired deletes the messages past the queue's retention at now, as
// their retention timers would (sqs.md 7.3), and reports whether any was.
func (s *Service) dropExpired(tx *kernel.Tx, q queueRef, queue *Queue, now clock.Instant) bool {
	retention := seconds(queue.Attributes.MessageRetentionPeriod)
	var expired []int
	for i := range queue.Messages {
		if queue.Messages[i].expired(retention, now) {
			expired = append(expired, i)
		}
	}
	s.removeMessages(tx, q, queue, expired)
	return len(expired) > 0
}

// plan picks the messages a receive takes, in delivery order, up to limit,
// and the ones it moves to the dead-letter queue on the way. A standard
// queue offers its receivable messages oldest first. A FIFO queue offers
// group by group, in the order the groups first appear, each group's
// messages in send order until the first that is not receivable: a group
// with a message in flight ahead of the rest is closed (sqs.md 8.1).
func plan(st *State, q queueRef, queue *Queue, limit int, now clock.Instant) (take, move []int) {
	redrive := redriveLimit(st, q, queue)
	offer := func(i int) bool {
		m := &queue.Messages[i]
		if !m.visible(now) {
			return false
		}
		if redrive > 0 && m.receiveCount() >= redrive {
			move = append(move, i)
			return true
		}
		if len(take) < limit {
			take = append(take, i)
		}
		return true
	}
	if queue.FIFO == nil {
		for i := range queue.Messages {
			if len(take) == limit {
				break
			}
			offer(i)
		}
		return take, move
	}
	for _, group := range groups(queue.Messages) {
		for _, i := range group {
			if len(take) == limit || !offer(i) {
				break
			}
		}
	}
	return take, move
}

// groups returns the indices of a FIFO queue's messages by group, the
// groups in the order their first message appears.
func groups(ms []Message) [][]int {
	var out [][]int
	at := map[string]int{}
	for i := range ms {
		g, ok := at[ms[i].GroupID]
		if !ok {
			g = len(out)
			at[ms[i].GroupID] = g
			out = append(out, nil)
		}
		out[g] = append(out[g], i)
	}
	return out
}

// redriveLimit is the maxReceiveCount of the queue's redrive policy, or 0
// when the queue has none or its dead-letter queue does not exist; a
// policy that names the queue itself moves nothing.
func redriveLimit(st *State, q queueRef, queue *Queue) int {
	p := queue.Attributes.RedrivePolicy
	if p == nil {
		return 0
	}
	if dlq := p.DeadLetterTarget; dlq.scope != q.scope || dlq.name == q.name {
		return 0
	}
	if _, ok := st.Queues[p.DeadLetterTarget.name]; !ok {
		return 0
	}
	return p.MaxReceiveCount
}

// deadLetter moves the messages at the indices move from queue to its
// dead-letter queue, in queue order, and returns the dead-letter queue's
// ARN. A moved message keeps its id, body, attributes and receive count
// and gains DeadLetterQueueSourceArn; a FIFO one restarts its retention
// and takes its id as its deduplication id (sqs.md 9.2). The receive is
// the cause of its enqueue there. The dead-letter queue changes through a
// working copy stored back at the end; redriveLimit made sure it exists
// and is not queue itself.
func (s *Service) deadLetter(c *kernel.Call, st *State, q queueRef, queue *Queue, move []int, now clock.Instant, tx *kernel.Tx) aws.ARN {
	dq := queue.Attributes.RedrivePolicy.DeadLetterTarget
	stored := st.Queues[dq.name]
	dlq := stored.clone()
	slices.Sort(move)
	for _, i := range move {
		m := queue.Messages[i].clone()
		m.DeadLetterSource = &q
		m.dropHandle()
		m.VisibleAt, m.Enqueued, m.EnqueuedTrace = now, c.Event(), c.Cause().Trace
		if dlq.FIFO != nil {
			m.Sent = now
			m.FIFO = &FIFOMessage{DedupID: string(m.ID), Sequence: dlq.FIFO.nextSequence()}
		}
		s.append(tx, dq, &dlq, m, now)
	}
	st.Queues[dq.name] = dlq
	s.removeMessages(tx, q, queue, move)
	return dq.arn()
}

// retryAttempt answers a retry of the ReceiveRequestAttemptId id with the
// messages and receipt handles that attempt received, while each still
// holds the handle the attempt gave it, and restarts their visibility
// timeout (sqs.md 8.3); again is nil when there is nothing to retry. It
// forgets an attempt whose messages changed, reporting forgot, so the
// receive goes on as a new attempt.
func (s *Service) retryAttempt(tx *kernel.Tx, q queueRef, queue *Queue, id string, vt int32, now clock.Instant) (again []Message, forgot bool) {
	a, ok := queue.FIFO.Attempts[id]
	if !ok {
		return nil, false
	}
	at := make([]int, len(a.Received))
	for k, r := range a.Received {
		i := queue.find(r.ID)
		if i < 0 || !queue.Messages[i].holds(string(r.Receipt)) {
			delete(queue.FIFO.attempts(), id)
			return nil, true
		}
		at[k] = i
	}
	out := make([]Message, len(at))
	for k, i := range at {
		m := queue.message(i)
		m.VisibleAt = now.Add(seconds(vt))
		s.armVisibility(tx, q, m, now)
		out[k] = m.clone()
	}
	return out, true
}

// remember records what the attempt id received, ms, each just received,
// for its retries within the deduplication window.
func (f *FIFO) remember(id string, ms []Message, now clock.Instant) {
	a := Attempt{Expires: now.Add(dedupWindow), Received: make([]AttemptReceived, len(ms))}
	for i := range ms {
		r, _ := ms[i].latest()
		a.Received[i] = AttemptReceived{ID: ms[i].ID, Receipt: r.Handle}
	}
	f.attempts()[id] = a
}

// forget drops the receive attempts that received the message id, whose
// visibility a caller changed.
func (f *FIFO) forget(id ids.MessageID) {
	received := func(_ string, a Attempt) bool {
		return slices.ContainsFunc(a.Received, func(r AttemptReceived) bool { return r.ID == id })
	}
	if anyEntry(f.Attempts, received) {
		maps.DeleteFunc(f.attempts(), received)
	}
}

// anyEntry reports whether some entry of m satisfies pred.
func anyEntry[V any](m map[string]V, pred func(string, V) bool) bool {
	for k, v := range m {
		if pred(k, v) {
			return true
		}
	}
	return false
}

// render builds the received messages, each just received, with the
// system and message attributes the request selected (sqs.md 7.1);
// MD5OfMessageAttributes covers the attributes returned.
func render(ms []Message, in *sqsapi.ReceiveMessageInput) []sqsapi.Message {
	sysNames := make([]string, 0, len(in.AttributeNames)+len(in.MessageSystemAttributeNames))
	for _, n := range in.AttributeNames {
		sysNames = append(sysNames, string(n))
	}
	for _, n := range in.MessageSystemAttributeNames {
		sysNames = append(sysNames, string(n))
	}
	out := make([]sqsapi.Message, len(ms))
	for i := range ms {
		m := &ms[i]
		attrs := selectAttributes(m.Attributes, in.MessageAttributeNames)
		r, _ := m.latest()
		out[i] = sqsapi.Message{
			MessageId:              new(string(m.ID)),
			ReceiptHandle:          new(string(r.Handle)),
			MD5OfBody:              new(m.BodyMD5),
			Body:                   new(m.Body),
			Attributes:             systemView(m, sysNames),
			MessageAttributes:      wireAttributes(attrs),
			MD5OfMessageAttributes: optional(attributesMD5(attrs)),
		}
	}
	return out
}

// systemView returns the system attributes names selects; "All" selects
// every one, and a name that does not apply to the message is left out.
func systemView(m *Message, names []string) map[sqsapi.MessageSystemAttributeName]string {
	if len(names) == 0 {
		return nil
	}
	all := map[sqsapi.MessageSystemAttributeName]string{
		sqsapi.MessageSystemAttributeNameSenderId:                         m.SenderID,
		sqsapi.MessageSystemAttributeNameSentTimestamp:                    millis(m.Sent),
		sqsapi.MessageSystemAttributeNameApproximateReceiveCount:          strconv.Itoa(m.receiveCount()),
		sqsapi.MessageSystemAttributeNameApproximateFirstReceiveTimestamp: millis(m.firstReceived()),
	}
	optionalAttr := func(name sqsapi.MessageSystemAttributeName, v string) {
		if v != "" {
			all[name] = v
		}
	}
	optionalAttr(sqsapi.MessageSystemAttributeNameMessageGroupId, m.GroupID)
	if f := m.FIFO; f != nil {
		all[sqsapi.MessageSystemAttributeNameMessageDeduplicationId] = f.DedupID
		all[sqsapi.MessageSystemAttributeNameSequenceNumber] = f.Sequence
	}
	if from := m.DeadLetterSource; from != nil {
		all[sqsapi.MessageSystemAttributeNameDeadLetterQueueSourceArn] = from.arn().String()
	}
	optionalAttr(sqsapi.MessageSystemAttributeNameAWSTraceHeader, m.System[string(sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader)].String)
	if slices.Contains(names, string(sqsapi.MessageSystemAttributeNameAll)) {
		return all
	}
	out := map[sqsapi.MessageSystemAttributeName]string{}
	for _, n := range names {
		if v, ok := all[sqsapi.MessageSystemAttributeName(n)]; ok {
			out[sqsapi.MessageSystemAttributeName(n)] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// selectAttributes returns the message attributes names selects: All, *
// and .* select every one, "prefix.*" those starting with prefix, any other
// name itself. Names that match nothing are not errors.
func selectAttributes(attrs map[string]Attribute, names []string) map[string]Attribute {
	out := map[string]Attribute{}
	for _, n := range names {
		prefix, wildcard := strings.CutSuffix(n, ".*")
		for name, a := range attrs {
			switch {
			case n == "All" || n == "*" || n == ".*":
				out[name] = a
			case wildcard && strings.HasPrefix(name, prefix):
				out[name] = a
			case name == n:
				out[name] = a
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func wireAttributes(attrs map[string]Attribute) map[string]sqsapi.MessageAttributeValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]sqsapi.MessageAttributeValue, len(attrs))
	for name, a := range attrs {
		v := sqsapi.MessageAttributeValue{DataType: a.DataType}
		if binaryType(a.DataType) {
			v.BinaryValue = slices.Clone(a.Binary)
		} else {
			v.StringValue = new(a.String)
		}
		out[name] = v
	}
	return out
}
