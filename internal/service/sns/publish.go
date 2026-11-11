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
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
	"github.com/sustinbebustin/understudy/internal/store"
)

const (
	// dedupWindow is how long a FIFO topic remembers a deduplication id
	// (sns.md 4.3).
	dedupWindow = 5 * time.Minute
	// maxSubjectRunes bounds Subject. The API reference says "less than
	// 100 characters", but AWS accepts 100 and refuses 101 (observed in
	// moto issue 1503, getmoto/moto#1503).
	maxSubjectRunes = 100
	// maxFIFOIDLength bounds MessageGroupId and MessageDeduplicationId.
	maxFIFOIDLength = 128
	// maxBatchEntries is PublishBatch's entry limit (sns.md 4.4).
	maxBatchEntries = 10
	// maxBatchIDLength bounds a batch entry Id.
	maxBatchIDLength = 80
)

// bindPublish binds Publish and PublishBatch.
func (s *Service) bindPublish(b snsapi.Binder) {
	const note = "fan-out to sqs and lambda subscriptions, and to http and https subscriptions whose host and port are in sns.httpDelivery.allow, after their filter policies, " +
		"with raw or enveloped delivery, the DeliveryPolicy retry schedule and the RedrivePolicy dead-letter queue; every backoffFunction steps linearly from minDelayTarget to maxDelayTarget, " +
		"without jitter; under iam.enforce=enforce an sqs or lambda delivery the endpoint's policy refuses is not retried and goes to the RedrivePolicy queue, else is dropped; " +
		"other http and https endpoints, email and email-json receive nothing"
	// S3, EventBridge and CloudWatch deliver to Publish; one to a deleted
	// topic fails as the missing topic, NO_RESOURCE to EventBridge
	// (iam-enforcement-surfaces.md 8.4).
	b.Publish(s.publish, registry.Partial{Note: note},
		kernel.AuthorizedAfterResolveForDeliveries(),
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
		kernel.WithResourceARN(func(in *snsapi.PublishBatchInput) string { return in.TopicArn }))
}

// message is one checked publish: what every subscription's delivery is
// built from.
type message struct {
	// body is the default message: Message itself, or the "default" entry
	// of a MessageStructure=json Message.
	body string
	// wire is Message as published, which the size limit counts and
	// content-based deduplication hashes even when MessageStructure=json
	// picks another body.
	wire    string
	subject string
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
func (m *message) size() int { return len(m.wire) + attributeBytes(m.attributes) }

// newMessage checks the members a Publish and a batch entry share.
func newMessage(body string, subject, structure *string, attrs map[string]snsapi.MessageAttributeValue, groupID, dedupID *string) (message, *awserr.Error) {
	m := message{body: body, wire: body}
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

// validSubject reports a Subject AWS accepts: non-empty, at most
// maxSubjectRunes characters, no line breaks or control characters. Only
// the empty case is recorded (sns.md 4.1).
func validSubject(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > maxSubjectRunes {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// validFIFOID reports a MessageGroupId or MessageDeduplicationId: up to 128
// ASCII letters, digits, punctuation and symbols (sns.md 4.3).
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
// types are ignored (sns.md 4.1). AWS documents that duplicate keys are not
// allowed and fail the whole Publish (API_Publish.html); answering them
// with the parse-failure text is inferred.
func parseStructure(body string) (def string, perProtocol map[string]string, err *awserr.Error) {
	members, ok := uniqueMembers(body)
	if !ok {
		return "", nil, errInvalidParameter("Message Structure - JSON message body failed to parse")
	}
	perProtocol = map[string]string{}
	for _, e := range members {
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
	// A delivery is authorized only once its topic is found, at fanOut's
	// first write (sns.enforce.missing-resource.delivery).
	if !s.topicExists(ref) {
		return nil, errTopicNotFound()
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

// topicExists reports whether ref's topic exists, read without a write, so
// that a delivery learns it before it is authorized.
func (s *Service) topicExists(ref topicRef) bool {
	exists := store.View(s.cells, ref.scope, func(st *State) bool {
		_, ok := st.Topics[ref.name]
		return ok
	})
	return exists
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

// publishBatch publishes up to ten messages to a topic. An entry whose
// Subject is invalid fails alone, in Failed, and the others are published
// (sns/R16 02-subject-101); every other check fails the whole call, an
// invalid message attribute among them (02-invalid-attribute, sns.md 4.4).
// Subject is the only member recorded to fail its entry alone; the
// whole-call checks of every entry run before it, which is inferred.
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
		m, err := newMessage(e.Message, nil, e.MessageStructure, e.MessageAttributes, e.MessageGroupId, e.MessageDeduplicationId)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	out := &snsapi.PublishBatchOutput{Successful: []snsapi.PublishBatchResultEntry{}, Failed: []snsapi.BatchResultErrorEntry{}}
	// ids[i] is the entry msgs[i] publishes once the failed entries are
	// left out.
	ids := make([]string, 0, len(msgs))
	kept := make([]message, 0, len(msgs))
	for i, e := range in.PublishBatchRequestEntries {
		if e.Subject != nil {
			if !validSubject(*e.Subject) {
				out.Failed = append(out.Failed, failedEntry(e.Id, errInvalidParameter("Subject")))
				continue
			}
			msgs[i].subject = *e.Subject
		}
		ids = append(ids, e.Id)
		kept = append(kept, msgs[i])
	}
	msgs = kept
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
	for i, p := range published {
		out.Successful = append(out.Successful, snsapi.PublishBatchResultEntry{
			Id:             new(ids[i]),
			MessageId:      new(p.messageID),
			SequenceNumber: p.sequence,
		})
	}
	return out, nil
}

// failedEntry is the Failed member of the batch entry id that err refused.
func failedEntry(id string, err *awserr.Error) snsapi.BatchResultErrorEntry {
	return snsapi.BatchResultErrorEntry{Id: id, Code: err.ID.QueryCode, Message: new(err.Message), SenderFault: err.ID.Fault == awserr.Sender}
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
// duplicate inside the deduplication window, and hands every confirmed sqs,
// lambda, http and https subscription its delivery in subscription
// creation order (ARCHITECTURE.md 16).
func (s *Service) fanOut(c *kernel.Call, ref topicRef, msgs []message, checkSize func(t *Topic) *awserr.Error) ([]published, *awserr.Error) {
	if !ref.fifo() {
		if err := s.ensureKey(c, ref); err != nil {
			return nil, err
		}
	}
	l := notifierLinks{service: linksOf(c.Endpoint()), http: s.httpLinks(c.Endpoint())}
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
		n, err := s.newNotifier(st, ref, &t, l, now)
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
		if err := n.deliver(&t.Subscriptions[i], m, p, tx); err != nil {
			return published{}, err
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
// one: the hash of "the body of the message", which is Message as
// published (Smithy Publish MessageDeduplicationId). That this is the whole
// MessageStructure=json document, not its default entry, is inferred: no
// recording covers it.
func (m *message) deduplicationID() string {
	if m.dedupID != "" {
		return m.dedupID
	}
	return contentDedupID(m.wire)
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

// notifierLinks are where a publish's links point: service for sqs and
// lambda deliveries, http for http and https ones (sns-http-delivery.md
// 8.3.3).
type notifierLinks struct {
	service links
	http    links
}

// notifier builds the deliveries of one write's messages.
type notifier struct {
	topic topicRef
	links notifierLinks
	at    time.Time
	// signing signs a standard topic's sqs and lambda notifications, and
	// httpSigning its http and https ones: the same key and memo, each
	// with its certificate URL on its own links. Both are zero on a FIFO
	// topic, whose notifications are not signed.
	signing, httpSigning envelope.Signing
	// deliveryPolicy is the topic's stored DeliveryPolicy, "" for none.
	deliveryPolicy string
	// policies holds the subscriptions' compiled filter policies.
	policies *filterpolicy.Cache
}

// newNotifier is the notifier of a publish to t at now: a standard topic's
// notifications are signed with the scope's key.
func (s *Service) newNotifier(st *State, ref topicRef, t *Topic, l notifierLinks, now clock.Instant) (notifier, *awserr.Error) {
	n := notifier{topic: ref, links: l, at: now.Wire(), deliveryPolicy: t.Attributes[attrDeliveryPolicy], policies: s.policies}
	switch t.Kind.(type) {
	case FIFO:
	case Standard:
		signing, err := s.signing(st.Signing, ref, t.signatureVersion(), l.service)
		if err != nil {
			return notifier{}, err
		}
		httpSigning, err := s.signing(st.Signing, ref, t.signatureVersion(), l.http)
		if err != nil {
			return notifier{}, err
		}
		// A string to sign names no link (sns.md 5.6), so both sign each
		// text once between them.
		httpSigning.Signer = signing.Signer
		n.signing, n.httpSigning = signing, httpSigning
	}
	return n, nil
}

// endpoint is where a subscription's deliveries go.
//
//sumtype:decl
type endpoint interface {
	// route is the delivery mechanism and the ARN it delivers to.
	route() (delivery.Mechanism, aws.ARN)
}

// queueEndpoint is an sqs subscription's queue.
type queueEndpoint struct{ queue aws.ARN }

// functionEndpoint is a lambda subscription's function, invoked at
// qualifier when the endpoint names one.
type functionEndpoint struct {
	function  aws.ARN
	qualifier string
}

func (e queueEndpoint) route() (delivery.Mechanism, aws.ARN) { return delivery.SNSToSQS, e.queue }

func (e functionEndpoint) route() (delivery.Mechanism, aws.ARN) {
	return delivery.SNSToLambda, e.function
}

// endpointOf is the endpoint sub delivers to, false for an endpoint type
// understudy does not deliver to (ADR 0015) or a lambda endpoint that names
// no function.
func endpointOf(sub *Subscription) (endpoint, bool, *awserr.Error) {
	switch sub.Protocol {
	case ProtocolSQS:
		queue, err := aws.ParseARN(sub.Endpoint)
		if err != nil {
			return nil, false, awserr.Internalf(err)
		}
		return queueEndpoint{queue: queue}, true, nil
	case ProtocolLambda:
		qualifier, function, ok := lambdaTarget(sub.Endpoint)
		if !ok {
			return nil, false, nil
		}
		return functionEndpoint{function: function, qualifier: qualifier}, true, nil
	case ProtocolHTTP, ProtocolHTTPS:
		to, bad := httpEndpointOf(sub)
		if bad != nil {
			return nil, false, awserr.Internalf(bad)
		}
		return to, true, nil
	case ProtocolEmail, ProtocolEmailJSON:
	}
	return nil, false, nil
}

// deliver hands m to sub's endpoint through the outbox. A pending
// subscription, or one whose endpoint type understudy does not deliver to,
// receives nothing: under sns.http.pending-messages nothing is held for an
// http or https subscription while it is pending (sns-http-delivery.md
// 2.2). An http or https subscription whose stored endpoint is not a URL it
// can send to (state an older build wrote) gets nothing, journaled as
// Dropped, and the topic's other subscriptions still get the message. A
// filter policy is evaluated before any outbox entry exists, so a message
// it keeps out never reaches the subscription's dead-letter queue (sns.md
// 6.3).
func (n notifier) deliver(sub *Subscription, m *message, p published, tx *kernel.Tx) *awserr.Error {
	if _, confirmed := sub.Status.(Confirmed); !confirmed {
		return nil
	}
	if sub.Protocol.http() {
		if _, bad := httpEndpointOf(sub); bad != nil {
			tx.Record(n.topic.scope, unusableEndpoint(n.topic.arn(), bad))
			return nil
		}
	}
	to, ok, err := endpointOf(sub)
	if err != nil || !ok {
		return err
	}
	if pass, err := n.admits(sub, m, to, tx); err != nil || !pass {
		return err
	}
	r, err := n.request(sub, m, p, to)
	if err != nil {
		return err
	}
	tx.Deliver(r)
	return nil
}

// admits applies sub's filter policy to m, read in the policy's scope: its
// attributes, or the body sub's protocol receives. A message the policy
// does not match is journaled as Filtered with the policy as the rule; one
// the matcher gives up on (its work bound, an understudy limit) is
// journaled as Dropped with the matcher's reason. Either Delivery event
// has Attempt 0: the decision comes before any attempt. An http or https
// endpoint, which has no ARN, is journaled by its host and port.
func (n notifier) admits(sub *Subscription, m *message, to endpoint, tx *kernel.Tx) (bool, *awserr.Error) {
	policy, err := sub.filter(n.policies)
	if err != nil || policy == nil {
		return err == nil, err
	}
	matched, merr := policy.Match(m.attributes, m.bodyFor(sub.Protocol))
	mechanism, target := to.route()
	event := journal.Delivery{From: n.topic.arn(), To: target, Mechanism: mechanism}
	if h, isHTTP := to.(httpEndpoint); isHTTP {
		event.Endpoint = delivery.HTTPEndpoint{URL: h.url}.HostPort()
	}
	switch {
	case merr != nil:
		event.Outcome = journal.Dropped{Reason: "the filter policy of subscription " + sub.ID + " could not be evaluated, so nothing was delivered " +
			"and nothing was sent to its dead-letter queue: " + merr.Error()}
	case !matched:
		event.Outcome = journal.Filtered{Rule: policy.String()}
	default:
		return true, nil
	}
	tx.Record(n.topic.scope, event)
	return false, nil
}

// request is the delivery of m to sub at to: the raw message or the
// envelope for a queue or an http or https endpoint, the event for a
// function, with sub's RedrivePolicy queue as the dead-letter queue
// (sns.md 7.4, sns-http-delivery.md 6.5). A function's dead letter is the
// SQS envelope, built here and handed over as Request.DLQMessage.
func (n notifier) request(sub *Subscription, m *message, p published, to endpoint) (delivery.Request, *awserr.Error) {
	dlq, err := sub.deadLetterQueue()
	if err != nil {
		return delivery.Request{}, err
	}
	subARN := subscriptionRef{topic: n.topic, id: sub.ID}.arn().String()
	note := envelope.Notification{
		MessageID:      p.messageID,
		TopicARN:       n.topic.arn().String(),
		Subject:        m.subject,
		Message:        m.bodyFor(sub.Protocol),
		Timestamp:      n.at,
		Attributes:     envelopeAttributes(m.attributes),
		UnsubscribeURL: n.links.service.unsubscribe(subARN),
	}
	req := n.topic.deliveryFrom()
	req.DLQ = dlq
	req.Mechanism, _ = to.route()
	switch to := to.(type) {
	case queueEndpoint:
		target, err := n.sqsTarget(sub, m, note, p, to.queue)
		if err != nil {
			return delivery.Request{}, err
		}
		req.Target = target
	case functionEndpoint:
		event, err := envelope.Lambda(note, subARN, n.signing)
		if err != nil {
			return delivery.Request{}, awserr.Internalf(err)
		}
		req.Target = delivery.LambdaFunction{ARN: to.function, Qualifier: to.qualifier, Event: event, Invocation: delivery.InvocationEvent}
		if dlq != nil {
			// The dead letter is what an SQS subscription would have
			// received (sns.md 7.4); a function takes no raw delivery.
			dl, err := n.sqsTarget(sub, m, note, p, *dlq)
			if err != nil {
				return delivery.Request{}, err
			}
			req.DLQMessage = &delivery.DeadLetterMessage{Body: dl.Body, Attributes: dl.Attributes, GroupID: dl.GroupID, DedupID: dl.DedupID}
		}
	case httpEndpoint:
		note.UnsubscribeURL = n.links.http.unsubscribe(subARN)
		target, err := n.httpTarget(sub, m, note, subARN, to)
		if err != nil {
			return delivery.Request{}, err
		}
		req.Target, req.Policy = target, httpRetryPolicy(sub.Attributes[attrDeliveryPolicy], n.deliveryPolicy)
	}
	return req, nil
}

// httpTarget is the Notification POST of a delivery to (sns-http-delivery.md
// 3.2, 4.2, 4.5): the raw message, after MessageStructure picked the
// protocol's entry, with x-amz-sns-rawdelivery and its attributes kept for
// the dead letter only (sns.http.dlq-body), else the signed envelope with
// its links on the HTTP links; Content-Type is the effective
// requestPolicy's.
func (n notifier) httpTarget(sub *Subscription, m *message, note envelope.Notification, subARN string, to httpEndpoint) (delivery.HTTPEndpoint, *awserr.Error) {
	target := delivery.HTTPEndpoint{URL: to.url, Kind: delivery.HTTPNotification}
	if sub.RawMessageDelivery {
		target.Body, target.DeadLetterAttributes = []byte(note.Message), m.attributes
	} else {
		body, err := envelope.HTTPNotification(note, n.httpSigning)
		if err != nil {
			return delivery.HTTPEndpoint{}, awserr.Internalf(err)
		}
		target.Body = body
	}
	target.Headers = envelope.HTTPHeaders(envelope.NotificationPost{
		MessageID:       note.MessageID,
		TopicARN:        note.TopicARN,
		SubscriptionARN: subARN,
		Raw:             sub.RawMessageDelivery,
		ContentType:     httpContentType(sub.Attributes[attrDeliveryPolicy], n.deliveryPolicy),
	}, to.url.User)
	return target, nil
}

// sqsTarget is the SQS message of a delivery to queue (sns.md 5.1 to
// 5.3): the raw message with its attributes as SQS message attributes,
// else the envelope; a FIFO topic gives a FIFO queue the group and
// deduplication ids, and a standard topic gives a standard queue the group
// id it was published with (fair queues). Whether the topic is FIFO is
// read from p.sequence, which the topic's Kind set in this write.
func (n notifier) sqsTarget(sub *Subscription, m *message, note envelope.Notification, p published, queue aws.ARN) (delivery.SQSQueue, *awserr.Error) {
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
