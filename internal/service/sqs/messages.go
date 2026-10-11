package sqs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindSends binds SendMessage and SendMessageBatch.
func (s *Service) bindSends(b sqsapi.Binder) {
	b.SendMessage(s.sendMessage, registry.Full{},
		kernel.WithCheck(checkSendMessage),
		queueByURL(func(in *sqsapi.SendMessageInput) string { return in.QueueUrl }))
	b.SendMessageBatch(s.sendMessageBatch, registry.Full{},
		kernel.WithCheck(checkSendMessageBatch),
		queueByURL(func(in *sqsapi.SendMessageBatchInput) string { return in.QueueUrl }))
}

// checkSendMessage runs SendMessage's stateless checks before fault rules
// (M7): the body's characters, then the message and system attributes.
// Their codes or texts are not the generated policy's, so they are checks,
// not validators.
func checkSendMessage(in *sqsapi.SendMessageInput) *awserr.Error {
	if !validText(in.MessageBody) {
		return errInvalidMessageContents()
	}
	if err := checkMessageAttributes(in.MessageAttributes); err != nil {
		return err
	}
	return checkSystemAttributes(in.MessageSystemAttributes)
}

// checkSendMessageBatch checks the entry ids and the total size, which
// fail the whole request (sqs.md 6.2); everything else about an entry
// fails that entry only.
func checkSendMessageBatch(in *sqsapi.SendMessageBatchInput) *awserr.Error {
	total := 0
	for i := range in.Entries {
		e := &in.Entries[i]
		total += len(e.MessageBody) + attributesSize(messageAttributes(e.MessageAttributes))
	}
	if err := checkBatchIDs("SendMessageBatchRequestEntry", entryIDs(in.Entries, func(e *sqsapi.SendMessageBatchRequestEntry) string { return e.Id })); err != nil {
		return err
	}
	if total > maxBatchBytes {
		return errBatchTooLong(total)
	}
	return nil
}

// checkBatchEntry runs the stateless checks of one SendMessageBatch entry,
// which SendMessage's validators and checks make for a single message.
func checkBatchEntry(e *sqsapi.SendMessageBatchRequestEntry) *awserr.Error {
	switch {
	case e.MessageBody == "":
		return errMissingBody()
	case e.DelaySeconds != nil && (*e.DelaySeconds < 0 || *e.DelaySeconds > 900):
		return errDelaySeconds(*e.DelaySeconds)
	case e.MessageGroupId != nil && !validFIFOID(*e.MessageGroupId):
		return errInvalidGroupID(*e.MessageGroupId)
	case e.MessageDeduplicationId != nil && !validFIFOID(*e.MessageDeduplicationId):
		return errInvalidDedupID(*e.MessageDeduplicationId)
	case !validText(e.MessageBody):
		return errInvalidMessageContents()
	}
	if err := checkMessageAttributes(e.MessageAttributes); err != nil {
		return err
	}
	return checkSystemAttributes(e.MessageSystemAttributes)
}

// validFIFOID reports whether id is 1 to 128 ASCII alphanumeric and
// punctuation characters, the rule of MessageGroupId and
// MessageDeduplicationId (sqs.md 6.1), as the overlay's pattern says for
// the single send.
func validFIFOID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		if id[i] < '!' || id[i] > '~' {
			return false
		}
	}
	return true
}

// checkMessageAttributes checks a send's message attributes (sqs.md 6.1):
// at most 10; each name valid; each data type String, Number or Binary
// with an optional custom label, at most 256 characters; each value
// non-empty and of its type. The first offending name in name order is
// reported.
func checkMessageAttributes(in map[string]sqsapi.MessageAttributeValue) *awserr.Error {
	if len(in) > 10 {
		return errTooManyAttributes(len(in))
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if err := checkMessageAttribute(name, in[name]); err != nil {
			return err
		}
	}
	return nil
}

// checkMessageAttribute checks one message attribute's name, data type
// and value.
func checkMessageAttribute(name string, v sqsapi.MessageAttributeValue) *awserr.Error {
	if !validAttributeName(name) {
		return errAttributeName(name)
	}
	base, label, labelled := strings.Cut(v.DataType, ".")
	if utf8.RuneCountInString(v.DataType) > 256 || (labelled && label == "") {
		return errAttributeDataType(name)
	}
	switch base {
	case "Binary":
		if len(v.BinaryValue) == 0 {
			return errEmptyAttributeValue(name, base)
		}
		return nil
	case "String", "Number":
	default:
		return errAttributeDataType(name)
	}
	switch {
	case v.StringValue == nil || *v.StringValue == "":
		return errEmptyAttributeValue(name, base)
	case base == "String" && !validText(*v.StringValue):
		return errAttributeString(name)
	case base == "Number" && !validNumber(*v.StringValue):
		return errAttributeNumber(name, *v.StringValue)
	}
	return nil
}

// validAttributeName reports a message attribute name of at most 256
// letters, digits, underscores, hyphens and periods, which does not start
// or end with a period, has no two periods in a row and does not start
// with AWS. or Amazon. in any case (sqs.md 6.1; AWS accepted non-ASCII
// letters).
func validAttributeName(name string) bool {
	n := utf8.RuneCountInString(name)
	lower := strings.ToLower(name)
	if n < 1 || n > 256 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.") {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// validNumber reports a decimal number SQS accepts as a Number attribute:
// at most 38 significant digits, magnitude from 10^-128 to below 10^126
// (sqs.md 6.1).
func validNumber(v string) bool {
	mantissa, exponent, hasExp := strings.Cut(strings.ToLower(v), "e")
	mantissa = strings.TrimLeft(mantissa, "+-")
	exp := 0
	if hasExp {
		e, err := strconv.Atoi(exponent)
		if err != nil || e < -1000 || e > 1000 {
			return false
		}
		exp = e
	}
	whole, frac, _ := strings.Cut(mantissa, ".")
	digits := whole + frac
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return false
	}
	first := strings.IndexFunc(digits, func(r rune) bool { return r != '0' })
	if first < 0 {
		return true
	}
	last := strings.LastIndexFunc(digits, func(r rune) bool { return r != '0' })
	magnitude := len(whole) - 1 - first + exp
	return last-first < 38 && magnitude >= -128 && magnitude < 126
}

// checkSystemAttributes checks a send's system attributes (sqs.md 6.1):
// AWSTraceHeader is the only one a send may carry, a non-empty String. The
// overlay leaves the name's enum to the handler, so any other name arrives
// here and is refused rather than dropped; the first in name order is
// reported.
func checkSystemAttributes(in map[sqsapi.MessageSystemAttributeNameForSends]sqsapi.MessageSystemAttributeValue) *awserr.Error {
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if name != sqsapi.MessageSystemAttributeNameForSendsAWSTraceHeader {
			return errSystemAttributeName(string(name))
		}
		if v := in[name]; v.DataType != "String" || v.StringValue == nil || *v.StringValue == "" {
			return errTraceHeader()
		}
	}
	return nil
}

// validText reports whether every character is one SQS accepts in a body
// or a String attribute: #x9, #xA, #xD, #x20 to #xD7FF, #xE000 to #xFFFD
// and #x10000 to #x10FFFF.
func validText(text string) bool {
	for i, r := range text {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(text[i:]); size == 1 {
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

// maxBatchBytes is the total payload a batch may carry, whatever the
// queue's MaximumMessageSize (sqs.md 6.2).
const maxBatchBytes = 1 << 20

// outgoing is a checked message a send enqueues.
type outgoing struct {
	body          string
	attrs, system map[string]Attribute
	// sentSystem are the system attributes the request carried, which
	// MD5OfMessageSystemAttributes covers; an AWSTraceHeader taken from
	// the X-Amzn-Trace-Id header is kept but not digested (sqs.md 6.1).
	sentSystem   map[string]Attribute
	group, dedup *string
	delay        *int32
}

func newOutgoing(c *kernel.Call, body string, attrs map[string]sqsapi.MessageAttributeValue,
	system map[sqsapi.MessageSystemAttributeNameForSends]sqsapi.MessageSystemAttributeValue,
	group, dedup *string, delay *int32,
) outgoing {
	sentSystem := sentSystemAttributes(system)
	return outgoing{
		body: body, attrs: messageAttributes(attrs), sentSystem: sentSystem, system: withTraceHeader(c, sentSystem),
		group: group, dedup: dedup, delay: delay,
	}
}

// sent is what a send reports about the message it enqueued, or about
// the original of a suppressed duplicate.
type sent struct {
	id                            ids.MessageID
	bodyMD5, attrs, sys, sequence string
}

// enqueue checks o against the queue and appends it, or acknowledges a
// FIFO duplicate within the deduplication window with the original's id
// and sequence number (sqs.md 8.2). tooLong is the size error of the
// calling operation. It runs inside kernel.Write; every check precedes
// every change.
func (s *Service) enqueue(c *kernel.Call, q queueRef, queue *Queue, o *outgoing, now clock.Instant, tx *kernel.Tx, tooLong func(int32) *awserr.Error) (sent, *awserr.Error) {
	fifo := queue.FIFO
	switch {
	case fifo == nil && o.dedup != nil:
		return sent{}, errNotForQueueType("MessageDeduplicationId", *o.dedup)
	case fifo != nil && o.group == nil:
		return sent{}, errMissingGroupID()
	case fifo != nil && o.dedup == nil && !fifo.ContentBasedDeduplication:
		return sent{}, errDedupRequired()
	case fifo != nil && o.delay != nil && *o.delay > 0:
		return sent{}, errNotForQueueType("DelaySeconds", strconv.Itoa(int(*o.delay)))
	}
	if size := len(o.body) + attributesSize(o.attrs); size > int(queue.Attributes.MaximumMessageSize) {
		return sent{}, tooLong(queue.Attributes.MaximumMessageSize)
	}
	out := sent{bodyMD5: bodyMD5(o.body), attrs: attributesMD5(o.attrs), sys: attributesMD5(o.sentSystem)}
	delay := queue.Attributes.DelaySeconds
	if fifo == nil && o.delay != nil {
		delay = *o.delay
	}
	m := Message{
		Body:       o.body,
		BodyMD5:    out.bodyMD5,
		Attributes: o.attrs,
		System:     o.system,
		SenderID:   senderID(c.Principal()),
		Enqueued:   c.Event(),
		Sent:       now,
		VisibleAt:  now.Add(seconds(delay)),
	}
	if o.group != nil {
		m.GroupID = *o.group
	}
	if fifo == nil {
		m.ID = s.ids.MessageID()
		s.append(tx, q, queue, m, now)
		out.id = m.ID
		return out, nil
	}
	dedupID := contentDedupID(o.body)
	if o.dedup != nil {
		dedupID = *o.dedup
	}
	key := fifo.dedupKey(m.GroupID, dedupID)
	fifo.pruneDedup(now)
	if d, ok := fifo.Dedup.get(key, now); ok {
		out.id, out.sequence = d.MessageID, d.Sequence
		return out, nil
	}
	m.ID = s.ids.MessageID()
	m.FIFO = &FIFOMessage{DedupID: dedupID, Sequence: fifo.nextSequence()}
	fifo.Dedup.put(key, Dedup{MessageID: m.ID, Sequence: m.FIFO.Sequence, Expires: now.Add(dedupWindow)})
	s.append(tx, q, queue, m, now)
	out.id, out.sequence = m.ID, m.FIFO.Sequence
	return out, nil
}

// append adds m to the end of queue and arms its timers.
func (s *Service) append(tx *kernel.Tx, q queueRef, queue *Queue, m Message, now clock.Instant) {
	queue.Messages = append(queue.Messages, m)
	last := &queue.Messages[len(queue.Messages)-1]
	s.armRetention(tx, q, queue, last)
	s.armVisibility(tx, q, last, now)
}

// sequenceBase makes every SequenceNumber 20 digits, as AWS's are, so
// they order as numbers and as strings alike.
const sequenceBase uint64 = 18_000_000_000_000_000_000

// nextSequence issues the queue's next SequenceNumber.
func (f *FIFO) nextSequence() string {
	f.Sequence++
	return strconv.FormatUint(sequenceBase+f.Sequence, 10)
}

// dedupKey is the deduplication cache key: the id alone for a queue
// created with queue scope, the group and the id for messageGroup scope
// (sqs.md 8.2). A newline appears in neither.
func (f *FIFO) dedupKey(group, id string) string {
	if f.CreatedScope == DedupMessageGroup {
		return group + "\n" + id
	}
	return id
}

// pruneDedup forgets the receive attempts and the generations of
// deduplication ids whose windows have ended (DedupCache.prune), and
// reports whether it forgot any.
func (f *FIFO) pruneDedup(now clock.Instant) bool {
	pruned := f.Dedup.prune(now)
	attemptEnded := func(_ string, a Attempt) bool { return !now.Before(a.Expires) }
	if anyEntry(f.Attempts, attemptEnded) {
		maps.DeleteFunc(f.attempts(), attemptEnded)
		pruned = true
	}
	return pruned
}

// contentDedupID is the content-based deduplication id: the hex SHA-256
// of the body alone (sqs.md 8.2).
func contentDedupID(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// sendMessage enqueues a message, visible after its delay (sqs.md 6.1).
func (s *Service) sendMessage(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	o := newOutgoing(c, in.MessageBody, in.MessageAttributes, in.MessageSystemAttributes, in.MessageGroupId, in.MessageDeduplicationId, in.DelaySeconds)
	out, err := writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (sent, *awserr.Error) {
		return s.enqueue(c, q, queue, &o, now, tx, errMessageTooLong)
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.SendMessageOutput{
		MessageId:                    new(string(out.id)),
		MD5OfMessageBody:             new(out.bodyMD5),
		MD5OfMessageAttributes:       optional(out.attrs),
		MD5OfMessageSystemAttributes: optional(out.sys),
		SequenceNumber:               optional(out.sequence),
	}, nil
}

// batchSend is one SendMessageBatch entry: the message, or the error its
// stateless checks found.
type batchSend struct {
	o   outgoing
	err *awserr.Error
}

// entryResult is one batch entry's outcome inside the write.
type entryResult struct {
	sent sent
	err  *awserr.Error
}

// sendMessageBatch enqueues up to ten messages in entry order. A FIFO
// entry without a MessageGroupId or a needed deduplication id fails the
// whole request; any other problem fails its entry only (sqs.md 6.2).
func (s *Service) sendMessageBatch(_ context.Context, c *kernel.Call, in *sqsapi.SendMessageBatchInput) (*sqsapi.SendMessageBatchOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	entries := make([]batchSend, len(in.Entries))
	for i := range in.Entries {
		e := &in.Entries[i]
		entries[i] = batchSend{
			o:   newOutgoing(c, e.MessageBody, e.MessageAttributes, e.MessageSystemAttributes, e.MessageGroupId, e.MessageDeduplicationId, e.DelaySeconds),
			err: checkBatchEntry(e),
		}
	}
	results, err := writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) ([]entryResult, *awserr.Error) {
		if err := checkFIFOBatch(queue.FIFO, entries); err != nil {
			return nil, err
		}
		out := make([]entryResult, len(entries))
		for i := range entries {
			if out[i].err = entries[i].err; out[i].err == nil {
				out[i].sent, out[i].err = s.enqueue(c, q, queue, &entries[i].o, now, tx, errBatchEntryTooLong)
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	out := &sqsapi.SendMessageBatchOutput{}
	for i, r := range results {
		id := in.Entries[i].Id
		if r.err != nil {
			out.Failed = append(out.Failed, batchFailure(id, r.err))
			continue
		}
		out.Successful = append(out.Successful, sqsapi.SendMessageBatchResultEntry{
			Id:                           id,
			MessageId:                    string(r.sent.id),
			MD5OfMessageBody:             r.sent.bodyMD5,
			MD5OfMessageAttributes:       optional(r.sent.attrs),
			MD5OfMessageSystemAttributes: optional(r.sent.sys),
			SequenceNumber:               optional(r.sent.sequence),
		})
	}
	return out, nil
}

// checkFIFOBatch refuses a batch to a FIFO queue, f, with an entry
// without a MessageGroupId or without the deduplication id the queue needs;
// a standard queue's f is nil and passes.
func checkFIFOBatch(f *FIFO, entries []batchSend) *awserr.Error {
	if f == nil {
		return nil
	}
	for i := range entries {
		switch o := &entries[i].o; {
		case o.group == nil:
			return errBatchMissingGroupID()
		case o.dedup == nil && !f.ContentBasedDeduplication:
			return errDedupRequired()
		}
	}
	return nil
}

// messageAttributes converts a send's message attributes: a Binary type
// keeps its bytes, the others their text.
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
// request sent, the only one checkSystemAttributes lets through (sqs.md
// 6.1).
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

// clone copies the message's maps, slices and pointers, so a value handed
// out of a cell never aliases it.
func (m *Message) clone() Message {
	c := *m
	c.Attributes = cloneAttributes(m.Attributes)
	c.System = cloneAttributes(m.System)
	if m.FIFO != nil {
		c.FIFO = new(*m.FIFO)
	}
	if m.Receives != nil {
		r := *m.Receives
		if r.Latest != nil {
			r.Latest = new(*r.Latest)
		}
		c.Receives = &r
	}
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
