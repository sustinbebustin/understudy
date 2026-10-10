package sqs

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindMessages binds the message data plane.
func (s *Service) bindMessages(b sqsapi.Binder) {
	b.SendMessage(s.sendMessage,
		registry.Partial{Note: "not validated: message attribute names, Number attribute values, the format and length of attribute data types, the characters of String attribute values and the data type of AWSTraceHeader; MessageDeduplicationId is ignored on standard queues"},
		kernel.WithCheck(checkSendMessage),
		queueByURL(func(in *sqsapi.SendMessageInput) string { return in.QueueUrl }))
	b.ReceiveMessage(s.receiveMessage,
		registry.Partial{Note: "no redrive to a dead-letter queue; ReceiveRequestAttemptId is ignored"},
		kernel.WithCheck(checkReceiveMessage),
		queueByURL(func(in *sqsapi.ReceiveMessageInput) string { return in.QueueUrl }))
	b.DeleteMessage(s.deleteMessage, registry.Full{},
		queueByURL(func(in *sqsapi.DeleteMessageInput) string { return in.QueueUrl }))
}

// checkSendMessage runs SendMessage's stateless checks before fault rules
// (M7): the body's characters, the MessageGroupId, then the message
// attributes. Their codes or texts are not the generated policy's, so they
// are checks, not validators.
func checkSendMessage(in *sqsapi.SendMessageInput) *awserr.Error {
	if !validBody(in.MessageBody) {
		return errInvalidMessageContents()
	}
	if g := in.MessageGroupId; g != nil && !validGroupID(*g) {
		return errInvalidGroupID(*g)
	}
	return checkMessageAttributes(in.MessageAttributes)
}

// validGroupID reports whether id is 1 to 128 ASCII alphanumeric and
// punctuation characters, the rule a MessageGroupId follows on standard
// and FIFO queues alike (sqs.md 6.1).
func validGroupID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		b := id[i]
		alnum := (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
		if !alnum && !strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", rune(b)) {
			return false
		}
	}
	return true
}

// checkReceiveMessage checks the VisibilityTimeout range (sqs.md 7.1),
// whose hand text no overlay template renders yet.
func checkReceiveMessage(in *sqsapi.ReceiveMessageInput) *awserr.Error {
	if v := in.VisibilityTimeout; v != nil && (*v < 0 || *v > 43200) {
		return errVisibilityTimeout(*v)
	}
	return nil
}

// sent is what SendMessage reports about the enqueued message.
type sent struct {
	id                  ids.MessageID
	bodyMD5, attrs, sys string
}

// sendMessage enqueues a message, visible after its delay (sqs.md 6.1).
func (s *Service) sendMessage(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	attrs := messageAttributes(in.MessageAttributes)
	sentSystem := sentSystemAttributes(in.MessageSystemAttributes)
	system := withTraceHeader(c, sentSystem)
	size := len(in.MessageBody) + attributesSize(attrs)
	out, err := kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (sent, *awserr.Error) {
		queue, ok := st.Queues[q.name]
		if !ok {
			return sent{}, errQueueDoesNotExist()
		}
		if size > int(queue.Attributes.MaximumMessageSize) {
			return sent{}, errMessageTooLong(queue.Attributes.MaximumMessageSize)
		}
		delay := queue.Attributes.DelaySeconds
		if in.DelaySeconds != nil {
			delay = *in.DelaySeconds
		}
		m := Message{
			ID:         s.ids.MessageID(),
			Body:       in.MessageBody,
			BodyMD5:    bodyMD5(in.MessageBody),
			Attributes: attrs,
			System:     system,
			SenderID:   senderID(c.Principal()),
			Enqueued:   c.Event(),
			Sent:       now,
			VisibleAt:  now.Add(seconds(delay)),
		}
		if in.MessageGroupId != nil {
			m.GroupID = *in.MessageGroupId
		}
		queue.Messages = append(slices.Clip(queue.Messages), m)
		st.Queues[q.name] = queue
		if delay > 0 {
			tx.Arm(s.visibility, visibilityID(q, m.ID), m.VisibleAt)
		} else {
			tx.Wake(q.waitKey())
		}
		// The system digest covers only what the request sent (sqs.md 6.1):
		// an AWSTraceHeader taken from X-Amzn-Trace-Id is stored, not
		// digested.
		return sent{id: m.ID, bodyMD5: m.BodyMD5, attrs: attributesMD5(attrs), sys: attributesMD5(sentSystem)}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.SendMessageOutput{
		MessageId:                    new(string(out.id)),
		MD5OfMessageBody:             new(out.bodyMD5),
		MD5OfMessageAttributes:       optional(out.attrs),
		MD5OfMessageSystemAttributes: optional(out.sys),
	}, nil
}

// receiveParams are a ReceiveMessage's limits; nil pointers take the
// queue's attribute.
type receiveParams struct {
	max        int
	wait       *int32
	visibility *int32
}

// received is one receive attempt: the messages it took, copied out of the
// state, and the wait time that applies.
type received struct {
	messages []Message
	wait     int32
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
	p := receiveParams{max: 1, wait: in.WaitTimeSeconds, visibility: in.VisibilityTimeout}
	if in.MaxNumberOfMessages != nil {
		p.max = int(*in.MaxNumberOfMessages)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var expired <-chan struct{}
	for {
		woke := s.k.Waiters().Wait(ctx, q.waitKey())
		got, err := kernel.Write(c, s.cells, q.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (received, *awserr.Error) {
			return s.receive(st, q, p, now, tx)
		})
		if err != nil {
			return nil, err
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

// receive takes up to p.max visible messages in enqueue order, giving each
// a new receipt handle and hiding it for the visibility timeout, and drops
// messages past the retention period. It runs inside kernel.Write.
func (s *Service) receive(st *State, q queueRef, p receiveParams, now clock.Instant, tx *kernel.Tx) (received, *awserr.Error) {
	queue, ok := st.Queues[q.name]
	if !ok {
		return received{}, errQueueDoesNotExist()
	}
	wait := queue.Attributes.ReceiveMessageWaitTimeSeconds
	if p.wait != nil {
		wait = *p.wait
	}
	vt := queue.Attributes.VisibilityTimeout
	if p.visibility != nil {
		vt = *p.visibility
	}
	retention := seconds(queue.Attributes.MessageRetentionPeriod)
	kept := make([]Message, 0, len(queue.Messages))
	var out []Message
	dropped := false
	for i := range queue.Messages {
		m := queue.Messages[i]
		timer := visibilityID(q, m.ID)
		if m.Sent.Add(retention).Compare(now) <= 0 {
			tx.Disarm(s.visibility, timer)
			dropped = true
			continue
		}
		if len(out) < p.max && m.visible(now) {
			m.ReceiveCount++
			if m.FirstReceived.IsZero() {
				m.FirstReceived = now
			}
			m.Receipt = s.ids.ReceiptHandle()
			m.VisibleAt = now.Add(seconds(vt))
			if vt > 0 {
				tx.Arm(s.visibility, timer, m.VisibleAt)
			} else {
				tx.Disarm(s.visibility, timer)
			}
			out = append(out, m.clone())
		}
		kept = append(kept, m)
	}
	if len(out) == 0 && !dropped {
		// An empty poll changes nothing, so an idle long poller never marks
		// the cell dirty.
		tx.Unchanged()
		return received{wait: wait}, nil
	}
	queue.Messages = kept
	st.Queues[q.name] = queue
	return received{messages: out, wait: wait}, nil
}

// deleteMessage deletes the message whose latest receipt handle is given.
// A handle of the right form that matches no message succeeds: the message
// was deleted already, or it was received again since (staleReceipt).
func (s *Service) deleteMessage(_ context.Context, c *kernel.Call, in *sqsapi.DeleteMessageInput) (*sqsapi.DeleteMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, q.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		queue, ok := st.Queues[q.name]
		if !ok {
			return struct{}{}, errQueueDoesNotExist()
		}
		// The queue is resolved before the handle is read, as AWS and
		// LocalStack do: a bad handle to a missing queue is NonExistentQueue.
		if _, ok := ids.ParseReceiptHandle(in.ReceiptHandle); !ok {
			return struct{}{}, errReceiptHandleIsInvalid(in.ReceiptHandle)
		}
		i := slices.IndexFunc(queue.Messages, func(m Message) bool { return string(m.Receipt) == in.ReceiptHandle })
		if i < 0 {
			return struct{}{}, nil
		}
		tx.Disarm(s.visibility, visibilityID(q, queue.Messages[i].ID))
		queue.Messages = slices.Delete(slices.Clone(queue.Messages), i, i+1)
		st.Queues[q.name] = queue
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.DeleteMessageOutput{}, nil
}

// onVisibility wakes the queue's receivers when the timer's message is
// still there and receivable; a deleted message's timer does nothing.
func (s *Service) onVisibility(_ *kernel.Call, key clock.TimerKey, due clock.Instant) {
	q, id, ok := parseVisibilityKey(key.ID)
	if !ok {
		return
	}
	cell, ok := s.cells.Lookup(q.scope)
	if !ok {
		return
	}
	visible := store.Read(cell, func(st *State) bool {
		queue := st.Queues[q.name]
		i := slices.IndexFunc(queue.Messages, func(m Message) bool { return m.ID == id })
		return i >= 0 && queue.Messages[i].visible(due)
	})
	if visible {
		s.k.Waiters().Wake(q.waitKey())
	}
}

// visibilityID is the visibility timer's id of message id in q:
// "<queue-arn>/<message-id>".
func visibilityID(q queueRef, id ids.MessageID) string {
	return q.arn().String() + "/" + string(id)
}

func parseVisibilityKey(key string) (queueRef, ids.MessageID, bool) {
	i := strings.LastIndexByte(key, '/')
	if i < 0 {
		return queueRef{}, "", false
	}
	arn, err := aws.ParseARN(key[:i])
	if err != nil {
		return queueRef{}, "", false
	}
	q, ok := queueOf(arn)
	return q, ids.MessageID(key[i+1:]), ok
}

// queueOf reads a queue ARN back into its scope and name.
func queueOf(arn aws.ARN) (queueRef, bool) {
	account, err := aws.ParseAccountID(arn.Account)
	if err != nil {
		return queueRef{}, false
	}
	var region aws.Region
	if err := region.UnmarshalText([]byte(arn.Region)); err != nil {
		return queueRef{}, false
	}
	return queueRef{scope: aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region}, name: arn.Resource}, true
}

// render builds the received messages with the system and message
// attributes the request selected (sqs.md 7.1); MD5OfMessageAttributes
// covers the attributes returned.
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
		out[i] = sqsapi.Message{
			MessageId:              new(string(m.ID)),
			ReceiptHandle:          new(string(m.Receipt)),
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
// every one, and a name that does not apply is left out.
func systemView(m *Message, names []string) map[sqsapi.MessageSystemAttributeName]string {
	if len(names) == 0 {
		return nil
	}
	all := map[sqsapi.MessageSystemAttributeName]string{
		sqsapi.MessageSystemAttributeNameSenderId:                         m.SenderID,
		sqsapi.MessageSystemAttributeNameSentTimestamp:                    millis(m.Sent),
		sqsapi.MessageSystemAttributeNameApproximateReceiveCount:          strconv.Itoa(m.ReceiveCount),
		sqsapi.MessageSystemAttributeNameApproximateFirstReceiveTimestamp: millis(m.FirstReceived),
	}
	if m.GroupID != "" {
		all[sqsapi.MessageSystemAttributeNameMessageGroupId] = m.GroupID
	}
	if trace, ok := m.System[string(sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader)]; ok {
		all[sqsapi.MessageSystemAttributeNameAWSTraceHeader] = trace.String
	}
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

// checkMessageAttributes checks a send's message attributes: at most 10,
// each with a String, Number or Binary type and a non-empty value of that
// type, reported for the first offending name in name order.
func checkMessageAttributes(in map[string]sqsapi.MessageAttributeValue) *awserr.Error {
	if len(in) > 10 {
		return errTooManyAttributes(len(in))
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		v := in[name]
		base, _, _ := strings.Cut(v.DataType, ".")
		switch {
		case base == "Binary" && len(v.BinaryValue) == 0:
			return errEmptyAttributeValue(name, base)
		case base == "Binary":
		case (base == "String" || base == "Number") && (v.StringValue == nil || *v.StringValue == ""):
			return errEmptyAttributeValue(name, base)
		case base == "String" || base == "Number":
		default:
			return errAttributeDataType(name)
		}
	}
	return nil
}

// messageAttributes converts a send's message attributes, which
// checkMessageAttributes passed: a Binary type keeps its bytes, the others
// their text.
func messageAttributes(in map[string]sqsapi.MessageAttributeValue) map[string]Attribute {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Attribute, len(in))
	for name, v := range in {
		a := Attribute{DataType: v.DataType}
		switch {
		case binaryType(v.DataType):
			a.Binary = slices.Clone(v.BinaryValue)
		case v.StringValue != nil:
			a.String = *v.StringValue
		}
		out[name] = a
	}
	return out
}

// sentSystemAttributes keeps the AWSTraceHeader system attribute the
// request sent, the only one SQS accepts (sqs.md 6.1).
func sentSystemAttributes(in map[sqsapi.MessageSystemAttributeNameForSends]sqsapi.MessageSystemAttributeValue) map[string]Attribute {
	name := sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader
	if v, ok := in[name]; ok && v.StringValue != nil {
		return map[string]Attribute{string(name): {DataType: v.DataType, String: *v.StringValue}}
	}
	return nil
}

// withTraceHeader returns the system attributes a message keeps: those
// sent, or, when none were, AWSTraceHeader from the request's
// X-Amzn-Trace-Id header (sqs.md 6.1).
func withTraceHeader(c *kernel.Call, sent map[string]Attribute) map[string]Attribute {
	if sent != nil {
		return sent
	}
	if trace := c.Trace(); trace != "" {
		return map[string]Attribute{string(sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader): {DataType: "String", String: trace}}
	}
	return nil
}

// attributesSize is what message attributes add to the size limit: each
// name, data type and value in bytes (sqs.md 6.1).
func attributesSize(attrs map[string]Attribute) int {
	n := 0
	for name, a := range attrs {
		n += len(name) + len(a.DataType) + len(a.String) + len(a.Binary)
	}
	return n
}

// validBody reports whether every character is one SQS accepts: #x9, #xA,
// #xD, #x20 to #xD7FF, #xE000 to #xFFFD and #x10000 to #x10FFFF.
func validBody(body string) bool {
	for i, r := range body {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(body[i:]); size == 1 {
				return false
			}
		}
		ok := r == 0x9 || r == 0xA || r == 0xD ||
			(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
		if !ok {
			return false
		}
	}
	return true
}

// senderID is the SenderId system attribute: the account of a root caller,
// the unique id of a user, role-id:session for a role session.
func senderID(p identity.Principal) string {
	switch p := p.(type) {
	case identity.Root:
		return p.Account.String()
	case identity.User:
		return p.ID
	case identity.AssumedRole:
		return p.Role.ID + ":" + p.Session
	case identity.ServicePrincipal:
		return p.Name
	case identity.Anonymous, nil:
	}
	return ""
}

// clone copies the message's maps and slices, so a value handed out of a
// cell never aliases it.
func (m *Message) clone() Message {
	c := *m
	c.Attributes = cloneAttributes(m.Attributes)
	c.System = cloneAttributes(m.System)
	return c
}

func cloneAttributes(in map[string]Attribute) map[string]Attribute {
	if in == nil {
		return nil
	}
	out := make(map[string]Attribute, len(in))
	for k, a := range in {
		a.Binary = slices.Clone(a.Binary)
		out[k] = a
	}
	return out
}

func seconds(n int32) time.Duration { return time.Duration(n) * time.Second }

// millis renders an instant as epoch milliseconds, as SQS's timestamp
// attributes are; the zero instant renders as 0.
func millis(i clock.Instant) string {
	if i.IsZero() {
		return "0"
	}
	return strconv.FormatInt(i.Wire().UnixMilli(), 10)
}

// optional is nil for the empty string, which the outputs omit.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
