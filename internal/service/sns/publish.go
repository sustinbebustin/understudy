package sns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

const (
	// dedupWindow is how long a FIFO topic remembers a deduplication id
	// (sns.md 4.3).
	dedupWindow = 5 * time.Minute
	// maxSubjectRunes bounds Subject: under 100 characters (sns.md 4.1).
	maxSubjectRunes = 99
	// maxFIFOIDLength bounds MessageGroupId and MessageDeduplicationId.
	maxFIFOIDLength = 128
	// maxBatchEntries is PublishBatch's entry limit (sns.md 4.4).
	maxBatchEntries = 10
	// maxBatchIDLength bounds a batch entry Id.
	maxBatchIDLength = 80
)

// bindPublish binds Publish and PublishBatch.
func (s *Service) bindPublish(b snsapi.Binder) {
	const note = "fan-out to sqs and lambda subscriptions; filter policies are not evaluated yet and http, https, email and email-json subscriptions receive nothing; RedrivePolicy is stored; failed deliveries are not sent to the DLQ yet"
	b.Publish(s.publish, registry.Partial{Note: note},
		kernel.WithResources(func(_ *kernel.Call, in *snsapi.PublishInput) []aws.ARN {
			for _, target := range []*string{in.TopicArn, in.TargetArn} {
				if target == nil {
					continue
				}
				if a, err := aws.ParseARN(*target); err == nil {
					return []aws.ARN{a}
				}
			}
			return nil
		}))
	b.PublishBatch(s.publishBatch, registry.Partial{Note: note},
		arnResource(func(in *snsapi.PublishBatchInput) string { return in.TopicArn }))
}

// message is one checked publish: what every subscription's delivery is
// built from.
type message struct {
	body string
	// wireBytes is the length of Message as published, which the size
	// limit counts even when MessageStructure=json picks a shorter body.
	wireBytes int
	subject   string
	// perProtocol is the MessageStructure=json message by protocol name,
	// nil for a plain message.
	perProtocol map[string]string
	attributes  []delivery.MessageAttribute
	groupID     string
	dedupID     string
}

// bodyFor is the message a subscription of protocol receives: its own
// entry of a json structure, else the default (sns.md 4.1).
func (m *message) bodyFor(p Protocol) string {
	if v, ok := m.perProtocol[p.String()]; ok {
		return v
	}
	return m.body
}

// size is the message's size against MaximumMessageSize: the Message
// parameter as sent and every attribute's name, type and value (sns.md
// 4.1).
func (m *message) size() int { return m.wireBytes + attributeBytes(m.attributes) }

// newMessage checks the members a Publish and a batch entry share.
func newMessage(body string, subject, structure *string, attrs map[string]snsapi.MessageAttributeValue, groupID, dedupID *string) (message, *awserr.Error) {
	m := message{body: body, wireBytes: len(body)}
	if body == "" {
		return message{}, errInvalidParameter("Empty message")
	}
	if subject != nil {
		if !validSubject(*subject) {
			return message{}, errInvalidParameter("Subject")
		}
		m.subject = *subject
	}
	if structure != nil {
		if *structure != "json" {
			return message{}, errInvalidParameter("MessageStructure")
		}
		var err *awserr.Error
		if m.body, m.perProtocol, err = parseStructure(body); err != nil {
			return message{}, err
		}
	}
	var err *awserr.Error
	if m.attributes, err = messageAttributes(attrs); err != nil {
		return message{}, err
	}
	for _, id := range []struct {
		member string
		value  *string
		into   *string
	}{{"MessageGroupId", groupID, &m.groupID}, {"MessageDeduplicationId", dedupID, &m.dedupID}} {
		if id.value == nil {
			continue
		}
		if !validFIFOID(*id.value) {
			return message{}, errInvalidParameter(id.member)
		}
		*id.into = *id.value
	}
	return m, nil
}

// validSubject reports a Subject AWS accepts: non-empty, under 100
// characters, no line breaks or control characters. Only the empty case
// is recorded (sns.md 4.1).
func validSubject(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxSubjectRunes {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// validFIFOID reports a MessageGroupId or MessageDeduplicationId: up to 128
// letters, digits and ASCII punctuation (sns.md 4.3).
func validFIFOID(s string) bool {
	if s == "" || len(s) > maxFIFOIDLength {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || (!isAlnum(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r)) {
			return false
		}
	}
	return true
}

// parseStructure parses a MessageStructure=json message: an object with a
// string "default" and other string members by protocol; members of other
// types are ignored (sns.md 4.1).
func parseStructure(body string) (def string, perProtocol map[string]string, err *awserr.Error) {
	m, ok := parseObject(body)
	if !ok {
		return "", nil, errInvalidParameter("Message Structure - JSON message body failed to parse")
	}
	perProtocol = map[string]string{}
	for _, e := range m {
		if v, ok := e.Value.(document.String); ok {
			perProtocol[e.Key] = string(v)
		}
	}
	if def, ok = perProtocol["default"]; !ok {
		return "", nil, errInvalidParameter("Message Structure - No default entry in JSON message body")
	}
	delete(perProtocol, "default")
	return def, perProtocol, nil
}

// publish publishes one message to a topic.
func (s *Service) publish(_ context.Context, c *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
	ref, err := publishTarget(c, in)
	if err != nil {
		return nil, err
	}
	m, err := newMessage(in.Message, in.Subject, in.MessageStructure, in.MessageAttributes, in.MessageGroupId, in.MessageDeduplicationId)
	if err != nil {
		return nil, err
	}
	out, err := s.fanOut(c, ref, []message{m}, func(t *Topic) *awserr.Error {
		if m.size() > t.maxMessageBytes() {
			return errInvalidParameter("Message too long")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.PublishOutput{MessageId: new(out[0].messageID), SequenceNumber: out[0].sequence}, nil
}

// publishTarget is the topic a Publish names: exactly one of TopicArn and
// TargetArn; a phone number or a mobile push endpoint is NotImplemented
// (ADR 0015). The texts for a missing or doubled target are inferred.
func publishTarget(c *kernel.Call, in *snsapi.PublishInput) (topicRef, *awserr.Error) {
	switch {
	case in.PhoneNumber != nil:
		return topicRef{}, errTargetNotImplemented("phone numbers")
	case in.TopicArn != nil && in.TargetArn != nil:
		return topicRef{}, errReason("TopicArn or TargetArn", "Only one of TopicArn or TargetArn may be specified")
	case in.TopicArn != nil:
		return resolveTopic(c, "TopicArn", *in.TopicArn)
	case in.TargetArn != nil:
		if strings.Contains(*in.TargetArn, ":endpoint/") {
			return topicRef{}, errTargetNotImplemented("mobile push endpoints")
		}
		return resolveTopic(c, "TargetArn", *in.TargetArn)
	}
	return topicRef{}, errReason("TopicArn or TargetArn", "no value for required parameter")
}

// publishBatch publishes up to ten messages to a topic. Every check fails
// the whole call: no recorded case puts an entry in Failed (sns.md 4.4).
func (s *Service) publishBatch(_ context.Context, c *kernel.Call, in *snsapi.PublishBatchInput) (*snsapi.PublishBatchOutput, *awserr.Error) {
	if err := checkBatchEntries(in.PublishBatchRequestEntries); err != nil {
		return nil, err
	}
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	msgs := make([]message, 0, len(in.PublishBatchRequestEntries))
	for _, e := range in.PublishBatchRequestEntries {
		m, err := newMessage(e.Message, e.Subject, e.MessageStructure, e.MessageAttributes, e.MessageGroupId, e.MessageDeduplicationId)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	published, err := s.fanOut(c, ref, msgs, func(t *Topic) *awserr.Error {
		total := 0
		for i := range msgs {
			total += msgs[i].size()
		}
		if total > t.maxMessageBytes() {
			return snsapi.ErrBatchRequestTooLongException("The length of all the messages put together is more than the limit.")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := &snsapi.PublishBatchOutput{Successful: make([]snsapi.PublishBatchResultEntry, 0, len(published)), Failed: []snsapi.BatchResultErrorEntry{}}
	for i, p := range published {
		out.Successful = append(out.Successful, snsapi.PublishBatchResultEntry{
			Id:             new(in.PublishBatchRequestEntries[i].Id),
			MessageId:      new(p.messageID),
			SequenceNumber: p.sequence,
		})
	}
	return out, nil
}

// checkBatchEntries checks a batch's shape (sns.md 4.4): one to ten
// entries with distinct ids of up to 80 letters, digits, hyphens and
// underscores.
func checkBatchEntries(entries []snsapi.PublishBatchRequestEntry) *awserr.Error {
	switch {
	case len(entries) == 0:
		return snsapi.ErrEmptyBatchRequestException("The batch request doesn't contain any entries.")
	case len(entries) > maxBatchEntries:
		return snsapi.ErrTooManyEntriesInBatchRequestException("The batch request contains more entries than permissible.")
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if len(e.Id) > maxBatchIDLength {
			return snsapi.ErrInvalidBatchEntryIdException("The Id of a batch entry in the batch request is too long: " + e.Id)
		}
		if e.Id == "" || strings.ContainsFunc(e.Id, func(r rune) bool { return !isAlnum(r) && r != '-' && r != '_' }) {
			return snsapi.ErrInvalidBatchEntryIdException("The Id of a batch entry in the batch request contains an impermissible character: " + e.Id)
		}
		if seen[e.Id] {
			return snsapi.ErrBatchEntryIdsNotDistinctException("Two or more batch entries in the request have the same Id.")
		}
		seen[e.Id] = true
	}
	return nil
}

// published is what one message's publish returns.
type published struct {
	messageID string
	// sequence is the FIFO SequenceNumber, nil on a standard topic.
	sequence *string
}

// fanOut publishes msgs to a topic in one write: it checks them against
// the topic (size first, with checkSize, then the FIFO rules), gives each
// a MessageId and, on a FIFO topic, a SequenceNumber, drops a FIFO
// duplicate inside the deduplication window, and hands every confirmed sqs
// and lambda subscription its delivery in subscription creation order
// (ARCHITECTURE.md 16).
func (s *Service) fanOut(c *kernel.Call, ref topicRef, msgs []message, checkSize func(t *Topic) *awserr.Error) ([]published, *awserr.Error) {
	if !ref.fifo() {
		if err := s.ensureKey(c, ref); err != nil {
			return nil, err
		}
	}
	links := linksOf(c.Endpoint())
	return kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) ([]published, *awserr.Error) {
		t, ok := st.writable(ref.name, now)
		if !ok {
			return nil, errTopicNotFound()
		}
		if err := checkSize(&t); err != nil {
			return nil, err
		}
		for i := range msgs {
			if err := checkFIFO(&t, &msgs[i]); err != nil {
				return nil, err
			}
		}
		n, err := s.newNotifier(st, ref, &t, links, now)
		if err != nil {
			return nil, err
		}
		t.pruneDedup(now)
		out := make([]published, 0, len(msgs))
		for i := range msgs {
			p, err := s.publishOne(&t, n, &msgs[i], now, tx)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		st.Topics[ref.name] = t
		return out, nil
	})
}

// publishOne gives m its MessageId and, on a FIFO topic, its
// SequenceNumber, and delivers it to t's subscriptions unless it is a
// duplicate inside the deduplication window.
func (s *Service) publishOne(t *Topic, n notifier, m *message, now clock.Instant, tx *kernel.Tx) (published, *awserr.Error) {
	p := published{messageID: string(s.ids.SNSMessageID())}
	if f, ok := t.Kind.(FIFO); ok {
		f.Sequence++
		p.sequence = new(fmt.Sprintf("%020d", f.Sequence))
		key := m.dedupKey(f.ThroughputScope == ThroughputMessageGroup)
		duplicate := slices.ContainsFunc(f.Dedup, func(e DedupEntry) bool { return e.Key == key })
		if !duplicate {
			f.Dedup = append(f.Dedup, DedupEntry{Key: key, Until: now.Add(dedupWindow)})
		}
		t.Kind = f
		if duplicate {
			return p, nil
		}
	}
	for i := range t.Subscriptions {
		r, ok, err := n.request(&t.Subscriptions[i], m, p)
		if err != nil {
			return published{}, err
		}
		if ok {
			tx.Deliver(r)
		}
	}
	return p, nil
}

// checkFIFO applies the FIFO publish rules (sns.md 4.3, AWS-recorded): a
// FIFO topic needs a MessageGroupId and a MessageDeduplicationId unless
// it deduplicates by content; a standard topic takes no
// MessageDeduplicationId.
func checkFIFO(t *Topic, m *message) *awserr.Error {
	switch k := t.Kind.(type) {
	case Standard:
		if m.dedupID != "" {
			return errReason("MessageDeduplicationId", "The request includes MessageDeduplicationId parameter that is not valid for this topic type")
		}
	case FIFO:
		if m.groupID == "" {
			return errInvalidParameter("The MessageGroupId parameter is required for FIFO topics")
		}
		if m.dedupID == "" && !k.ContentBasedDeduplication {
			return errInvalidParameter("The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
		}
	}
	return nil
}

// contentDedupID is the deduplication id of a body under content-based
// deduplication: the lowercase hex SHA-256 of the body (sns.md 5.3).
func contentDedupID(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// deduplicationID is the message's MessageDeduplicationId, or the content
// one.
func (m *message) deduplicationID() string {
	if m.dedupID != "" {
		return m.dedupID
	}
	return contentDedupID(m.body)
}

// dedupKey is what the deduplication window remembers: the id, within its
// message group when FifoThroughputScope is MessageGroup.
func (m *message) dedupKey(perGroup bool) string {
	if perGroup {
		return m.groupID + "/" + m.deduplicationID()
	}
	return m.deduplicationID()
}

// pruneDedup forgets the deduplication keys of a FIFO topic whose window
// has ended; t must not alias a cell.
func (t *Topic) pruneDedup(now clock.Instant) {
	t.updateFIFO(func(f *FIFO) {
		f.Dedup = slices.DeleteFunc(f.Dedup, func(e DedupEntry) bool { return !now.Before(e.Until) })
	})
}

// notifier builds the deliveries of one write's messages.
type notifier struct {
	topic topicRef
	links links
	at    time.Time
	// signing signs a standard topic's notifications; zero on a FIFO
	// topic, whose notifications are not signed.
	signing envelope.Signing
}

// newNotifier is the notifier of a publish to t at now: a standard topic's
// notifications are signed with the scope's key.
func (s *Service) newNotifier(st *State, ref topicRef, t *Topic, l links, now clock.Instant) (notifier, *awserr.Error) {
	n := notifier{topic: ref, links: l, at: now.Wire()}
	switch t.Kind.(type) {
	case FIFO:
	case Standard:
		signing, err := s.signing(st.Signing, ref, t.signatureVersion(), l)
		if err != nil {
			return notifier{}, err
		}
		n.signing = signing
	}
	return n, nil
}

// request is the delivery of m to sub, false when sub receives nothing: it
// is pending, or its endpoint type is not delivered to in phase 1.
func (n notifier) request(sub *Subscription, m *message, p published) (delivery.Request, bool, *awserr.Error) {
	if _, confirmed := sub.Status.(Confirmed); !confirmed {
		return delivery.Request{}, false, nil
	}
	subARN := subscriptionRef{topic: n.topic, id: sub.ID}.arn().String()
	note := envelope.Notification{
		MessageID:      p.messageID,
		TopicARN:       n.topic.arn().String(),
		Subject:        m.subject,
		Message:        m.bodyFor(sub.Protocol),
		Timestamp:      n.at,
		Attributes:     envelopeAttributes(m.attributes),
		UnsubscribeURL: n.links.unsubscribe(subARN),
	}
	req := delivery.Request{Source: n.topic.arn(), Policy: delivery.SNSManaged{}}
	switch sub.Protocol {
	case ProtocolSQS:
		target, err := n.sqsTarget(sub, m, note, p)
		if err != nil {
			return delivery.Request{}, false, err
		}
		req.Mechanism, req.Target = delivery.SNSToSQS, target
	case ProtocolLambda:
		qualifier, function, ok := lambdaTarget(sub.Endpoint)
		if !ok {
			return delivery.Request{}, false, nil
		}
		event, err := envelope.Lambda(note, subARN, n.signing.Signer, n.signing.CertURL)
		if err != nil {
			return delivery.Request{}, false, awserr.Internalf(err)
		}
		req.Mechanism = delivery.SNSToLambda
		req.Target = delivery.LambdaFunction{ARN: function, Qualifier: qualifier, Event: event, Invocation: delivery.InvocationEvent}
	case ProtocolHTTP, ProtocolHTTPS, ProtocolEmail, ProtocolEmailJSON:
		// Outbound HTTP and email delivery are phase 2 (ADR 0015).
		return delivery.Request{}, false, nil
	}
	return req, true, nil
}

// sqsTarget is the SQS message of a delivery (sns.md 5.1 to 5.3): the raw
// message with its attributes as SQS message attributes, else the
// envelope; a FIFO topic gives a FIFO queue the group and deduplication
// ids, and a standard topic gives a standard queue the group id it was
// published with (fair queues). Whether the topic is FIFO is read from
// p.sequence, which the topic's Kind set in this write.
func (n notifier) sqsTarget(sub *Subscription, m *message, note envelope.Notification, p published) (delivery.SQSQueue, *awserr.Error) {
	queue, err := aws.ParseARN(sub.Endpoint)
	if err != nil {
		return delivery.SQSQueue{}, awserr.Internalf(err)
	}
	fifoTopic := p.sequence != nil
	target := delivery.SQSQueue{ARN: queue}
	switch {
	case sub.RawMessageDelivery:
		target.Body, target.Attributes = note.Message, m.attributes
	case fifoTopic:
		target.Body = string(envelope.SQSFIFO(note, *p.sequence))
	default:
		body, err := envelope.SQS(note, n.signing)
		if err != nil {
			return delivery.SQSQueue{}, awserr.Internalf(err)
		}
		target.Body = string(body)
	}
	switch fifoQueue := isFIFOQueue(queue.Resource); {
	case fifoTopic && fifoQueue:
		target.GroupID, target.DedupID = m.groupID, m.deduplicationID()
	case !fifoTopic && !fifoQueue:
		target.GroupID = m.groupID
	}
	return target, nil
}
