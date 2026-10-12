package sns

import (
	"context"
	"encoding/hex"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

const (
	// tokenValidity is how long a confirmation token confirms (sns.md
	// 3.1): two days of emulator time.
	tokenValidity = 48 * time.Hour
	// tokenBytes is the random length of a token, 288 hex characters
	// as LocalStack reports real tokens to be (sns.md 3.2, UNVERIFIED).
	tokenBytes = 144
	// pendingARN is Subscribe's SubscriptionArn for a subscription that
	// waits for confirmation, unless ReturnSubscriptionArn is set.
	pendingARN = "pending confirmation"
)

// bindSubscriptions binds the subscription lifecycle and listings.
func (s *Service) bindSubscriptions(b snsapi.Binder) {
	b.Subscribe(s.subscribe, registry.Partial{Note: "sqs and lambda endpoints are delivered to, and their failed deliveries go to the RedrivePolicy queue; " +
		"http, https, email and email-json stay subscribed with a readable token but receive nothing; sms, application and firehose answer NotImplemented; " +
		"the quota of 10,000 filter policies per account is not enforced"},
		arnResource(func(in *snsapi.SubscribeInput) string { return in.TopicArn }))
	b.ConfirmSubscription(s.confirmSubscription, registry.Full{},
		arnResource(func(in *snsapi.ConfirmSubscriptionInput) string { return in.TopicArn }))
	b.Unsubscribe(s.unsubscribeOp, registry.Full{},
		arnResource(func(in *snsapi.UnsubscribeInput) string { return in.SubscriptionArn }))
	b.ListSubscriptions(s.listSubscriptions, registry.Full{})
	b.ListSubscriptionsByTopic(s.listSubscriptionsByTopic, registry.Full{},
		arnResource(func(in *snsapi.ListSubscriptionsByTopicInput) string { return in.TopicArn }))
}

// subscribe subscribes an endpoint to a topic, or returns the existing
// subscription of the same protocol and endpoint when the request's
// attributes agree with it (sns.md 3.1).
func (s *Service) subscribe(_ context.Context, c *kernel.Call, in *snsapi.SubscribeInput) (*snsapi.SubscribeOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	endpoint := ""
	if in.Endpoint != nil {
		endpoint = *in.Endpoint
	}
	protocol, err := subscribeProtocol(in.Protocol, endpoint)
	if err != nil {
		return nil, err
	}
	owner, err := checkEndpoint(protocol, endpoint, ref)
	if err != nil {
		return nil, err
	}
	settings, err := s.subscriptionSettings(subscriber{endpoint: endpoint, topicFIFO: ref.fifo(), home: ref.scope}, in.Attributes)
	if err != nil {
		return nil, err
	}
	candidate := Subscription{
		ID:             s.ids.UUID(ids.KindSNSMessageID).String(),
		Protocol:       protocol,
		Endpoint:       endpoint,
		OwnerAccountID: c.Scope().Account.String(),
		Principal:      identity.Render(c.Principal()),
		Status:         Confirmed{By: AtSubscribe{}},
	}
	for _, set := range settings {
		set.apply(&candidate)
	}
	sub, err := s.addSubscription(c, ref, candidate, settings, confirmRouteOf(protocol, owner, ref, c))
	if err != nil {
		return nil, err
	}
	arn := subscriptionRef{topic: ref, id: sub.ID}.arn().String()
	if _, pending := sub.Status.(Pending); pending && !in.ReturnSubscriptionArn {
		arn = pendingARN
	}
	return &snsapi.SubscribeOutput{SubscriptionArn: &arn}, nil
}

// confirmRoute is how a new subscription becomes confirmed.
type confirmRoute uint8

// confirmRoute values.
const (
	// confirmedAtSubscribe: SNS confirms it before Subscribe returns.
	confirmedAtSubscribe confirmRoute = iota + 1
	// confirmedByToken: it waits for ConfirmSubscription with its token,
	// which understudy hands out through the admin API (ADR 0015).
	confirmedByToken
	// confirmedFromQueue: it waits as confirmedByToken does, and its queue
	// gets a SubscriptionConfirmation carrying the token (sns.md 3.1).
	confirmedFromQueue
)

// confirmRouteOf is the confirmRoute of a subscription of protocol to an
// endpoint owner owns: SNS confirms an sqs or lambda endpoint at once when
// the topic owner or the caller owns it, and any other subscription waits
// (sns.md 3.1, Implications 6).
func confirmRouteOf(protocol Protocol, owner string, ref topicRef, c *kernel.Call) confirmRoute {
	switch {
	case protocol.confirmedAtOnce() && (owner == ref.scope.Account.String() || owner == c.Scope().Account.String()):
		return confirmedAtSubscribe
	case protocol == ProtocolSQS:
		return confirmedFromQueue
	default:
		return confirmedByToken
	}
}

// added is the outcome of a Subscribe write that stored or found a
// subscription: refused is the FilterPolicy quota error of a new
// subscription that was stored without its policy, nil otherwise.
type added struct {
	sub     Subscription
	refused *awserr.Error
}

// addSubscription adds candidate to the topic ref names, confirmed by
// route, or returns the topic's subscription of the same protocol and
// endpoint when settings agree with it. A new subscription whose
// FilterPolicy would pass the topic's quota is still stored, without the
// policy, and Subscribe answers the quota error, as AWS still creates it
// (sns.md 3.1).
func (s *Service) addSubscription(c *kernel.Call, ref topicRef, candidate Subscription, settings []subscriptionSetting, route confirmRoute) (Subscription, *awserr.Error) {
	token := ""
	if route != confirmedAtSubscribe {
		token = hex.EncodeToString(s.ids.Draw(ids.KindSNSMessageID, tokenBytes))
	}
	if route == confirmedFromQueue {
		if err := s.ensureKey(c, ref); err != nil {
			return Subscription{}, err
		}
	}
	links := linksOf(c.Endpoint())
	out, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (added, *awserr.Error) {
		t, ok := st.writable(ref.name, now)
		if !ok {
			return added{}, errTopicNotFound()
		}
		if existing, ok := t.subscribed(candidate.Protocol, candidate.Endpoint); ok {
			tx.Unchanged()
			return added{sub: existing}, existing.agrees(settings)
		}
		if route != confirmedAtSubscribe {
			candidate.Status = Pending{Token: token, Expires: now.Add(tokenValidity)}
		}
		refused := t.admitFilterPolicy(&candidate)
		t.Subscriptions = append(t.Subscriptions, candidate)
		if reason := t.largeMessageViolation(); reason != "" {
			return added{}, errLargeMessages(reason)
		}
		if route == confirmedFromQueue {
			r, err := s.confirmation(st, ref, &t, &candidate, token, links, now)
			if err != nil {
				return added{}, err
			}
			tx.Deliver(r)
		}
		st.Topics[ref.name] = t
		return added{sub: candidate, refused: refused}, nil
	})
	if err != nil {
		return Subscription{}, err
	}
	return out.sub, out.refused
}

// subscribed returns a copy of t's subscription of protocol and endpoint,
// which a second Subscribe returns (sns.md 3.1).
func (t *Topic) subscribed(p Protocol, endpoint string) (Subscription, bool) {
	for i := range t.Subscriptions {
		if t.Subscriptions[i].Protocol == p && t.Subscriptions[i].Endpoint == endpoint {
			return t.Subscriptions[i].clone(), true
		}
	}
	return Subscription{}, false
}

// agrees answers a repeated Subscribe's attributes: nil when each equals
// the value sub shows for it, its default when unset, else the recorded
// conflict (sns.md 3.1). An attribute sub never set therefore conflicts
// unless the repeat passes its default: test_subscribe_idempotency
// (LocalStack, recorded against AWS) accepts FilterPolicyScope
// MessageAttributes on a subscription that never set it, and LocalStack
// refuses any other differing value; the absent-attribute case beyond that
// is inferred.
func (sub *Subscription) agrees(settings []subscriptionSetting) *awserr.Error {
	for _, set := range settings {
		if sub.setting(set.name) != set.shown {
			return errReason("Attributes", "Subscription already exists with different attributes")
		}
	}
	return nil
}

// confirmation is the delivery of the SubscriptionConfirmation of sub, a
// pending sqs subscription to the topic ref names, to its queue (sns.md
// 3.1, 5.5): signed with the scope's key at the topic's SignatureVersion,
// its SubscribeURL the confirm link. A FIFO queue gets the subscription id
// as its message group and the confirmation's MessageId as its
// deduplication id, which is inferred: no recording shows a confirmation
// in a FIFO queue.
func (s *Service) confirmation(st *State, ref topicRef, t *Topic, sub *Subscription, token string, l links, now clock.Instant) (delivery.Request, *awserr.Error) {
	signing, err := s.signing(st.Signing, ref, t.signatureVersion(), l)
	if err != nil {
		return delivery.Request{}, err
	}
	queue, perr := aws.ParseARN(sub.Endpoint)
	if perr != nil {
		return delivery.Request{}, awserr.Internalf(perr)
	}
	topicARN := ref.arn().String()
	messageID := string(s.ids.SNSMessageID())
	body, berr := envelope.SQSConfirmation(envelope.Confirmation{
		MessageID:    messageID,
		Token:        token,
		TopicARN:     topicARN,
		Timestamp:    now.Wire(),
		SubscribeURL: l.confirm(topicARN, token),
	}, signing)
	if berr != nil {
		return delivery.Request{}, awserr.Internalf(berr)
	}
	target := delivery.SQSQueue{ARN: queue, Body: string(body)}
	if isFIFOQueue(queue.Resource) {
		target.GroupID, target.DedupID = sub.ID, messageID
	}
	return delivery.Request{Source: ref.arn(), Mechanism: delivery.SNSToSQS, Target: target, Policy: delivery.SNSManaged{}}, nil
}

// subscribeProtocol parses Subscribe's Protocol: an endpoint type
// understudy does not deliver to is NotImplemented (ADR 0015), anything
// else unknown is AWS's recorded text (sns.md 3.1). An sms endpoint is
// checked first, so a number AWS refuses gets AWS's answer rather than
// NotImplemented.
func subscribeProtocol(name, endpoint string) (Protocol, *awserr.Error) {
	if p, ok := parseProtocol(name); ok {
		return p, nil
	}
	switch name {
	case "sms":
		if !e164.MatchString(endpoint) {
			return 0, errSMSEndpoint(endpoint)
		}
		return 0, errProtocolNotImplemented(name)
	case "application", "firehose":
		return 0, errProtocolNotImplemented(name)
	}
	return 0, errInvalidParameter("Amazon SNS does not support this protocol string: " + name)
}

// e164 is an E.164 phone number with an optional leading plus, the form
// LocalStack's validated provider checks; it refuses every endpoint AWS
// recorded refusing (sns.md 3.1).
var e164 = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`)

// checkEndpoint checks a Subscribe endpoint against its protocol and the
// topic's kind and returns the account that owns an ARN endpoint, "" for
// the others. An sqs or lambda ARN must name a scope the outbox delivers
// to (servedScope), or every later publish to the topic would fail. Only
// the SQS endpoint ARN text and the FIFO-queue-on-a-standard-topic text
// are AWS-recorded (sns.md 3.1); the FIFO-topic endpoint text, whose
// wording sns.md 3.1 leaves UNVERIFIED, and the lambda, http and email
// texts are inferred.
func checkEndpoint(p Protocol, endpoint string, topic topicRef) (string, *awserr.Error) {
	if endpoint == "" {
		return "", errReason("Endpoint", "no value for required parameter")
	}
	if topic.fifo() && p != ProtocolSQS {
		return "", errReason("Endpoint", "FIFO topics support only Amazon SQS endpoints")
	}
	switch p {
	case ProtocolSQS:
		return queueOwner(endpoint, topic)
	case ProtocolLambda:
		if _, a, ok := lambdaTarget(endpoint); ok {
			if _, served := servedScope(a); served {
				return a.Account, nil
			}
		}
		return "", errInvalidParameter("Lambda endpoint ARN")
	case ProtocolHTTP, ProtocolHTTPS:
		if !strings.HasPrefix(endpoint, p.String()+"://") {
			return "", errInvalidParameter("Endpoint must match the specified protocol")
		}
	case ProtocolEmail, ProtocolEmailJSON:
		if !strings.Contains(endpoint, "@") {
			return "", errInvalidParameter("Email address")
		}
	}
	return "", nil
}

// queueOwner checks an sqs endpoint, a queue ARN in a served scope that is
// FIFO only on a FIFO topic, and returns the account that owns it.
func queueOwner(endpoint string, topic topicRef) (string, *awserr.Error) {
	a, err := aws.ParseARN(endpoint)
	if err != nil || a.Service != "sqs" || strings.Contains(a.Resource, ":") {
		return "", errInvalidParameter("SQS endpoint ARN")
	}
	if _, ok := servedScope(a); !ok {
		return "", errInvalidParameter("SQS endpoint ARN")
	}
	if !topic.fifo() && isFIFOQueue(a.Resource) {
		return "", errInvalidParameter("Invalid parameter: Endpoint Reason: FIFO SQS Queues can not be subscribed to standard SNS topics")
	}
	return a.Account, nil
}

// lambdaTarget splits a function ARN endpoint into the unqualified
// function ARN and its version or alias qualifier.
func lambdaTarget(endpoint string) (qualifier string, function aws.ARN, ok bool) {
	a, err := aws.ParseARN(endpoint)
	if err != nil || a.Service != "lambda" {
		return "", aws.ARN{}, false
	}
	parts := strings.Split(a.Resource, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "function" || parts[1] == "" {
		return "", aws.ARN{}, false
	}
	if len(parts) == 3 {
		qualifier = parts[2]
	}
	a.Resource = "function:" + parts[1]
	return qualifier, a, true
}

// confirmSubscription confirms a pending subscription by its token.
func (s *Service) confirmSubscription(_ context.Context, c *kernel.Call, in *snsapi.ConfirmSubscriptionInput) (*snsapi.ConfirmSubscriptionOutput, *awserr.Error) {
	// Every malformed or foreign topic ARN is "Invalid parameter: Topic"
	// here (sns.md 3.2).
	ref, err := resolveTopic(c, "Topic", in.TopicArn)
	if err != nil {
		return nil, errInvalidParameter("Topic")
	}
	arn, err := s.confirm(c, ref, ByCall{
		Token:                     in.Token,
		AuthenticateOnUnsubscribe: in.AuthenticateOnUnsubscribe != nil && strings.EqualFold(*in.AuthenticateOnUnsubscribe, "true"),
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.ConfirmSubscriptionOutput{SubscriptionArn: new(arn.String())}, nil
}

// confirm confirms the subscription of topic waiting for by's token as by
// says, and returns its ARN. Confirming again with the token it was
// confirmed with succeeds and changes nothing. A token that is not 288 hex
// characters is "Invalid parameter: Token"; one that confirms nothing, or
// has expired, is "Invalid token" (sns.md 3.2).
func (s *Service) confirm(c *kernel.Call, topic topicRef, by tokenConfirmation) (aws.ARN, *awserr.Error) {
	token := by.token()
	if raw, err := hex.DecodeString(token); err != nil || len(raw) != tokenBytes {
		return aws.ARN{}, errInvalidParameter("Token")
	}
	id, err := kernel.Write(c, s.cells, topic.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (string, *awserr.Error) {
		t, ok := st.writable(topic.name, now)
		if !ok {
			return "", errTopicNotFound()
		}
		for i := range t.Subscriptions {
			sub := &t.Subscriptions[i]
			switch status := sub.Status.(type) {
			case Pending:
				if status.Token == token {
					sub.Status = Confirmed{By: by}
					st.Topics[topic.name] = t
					return sub.ID, nil
				}
			case Confirmed:
				if prior, ok := status.By.(tokenConfirmation); ok && prior.token() == token {
					// The copy is not stored: Unchanged requires the
					// cell to stay exactly as it was.
					tx.Unchanged()
					return sub.ID, nil
				}
			}
		}
		return "", errInvalidToken()
	})
	if err != nil {
		return aws.ARN{}, err
	}
	return subscriptionRef{topic: topic, id: id}.arn(), nil
}

// unsubscribeOp deletes a subscription. Deleting one that does not exist
// succeeds (sns.md 3.2).
func (s *Service) unsubscribeOp(_ context.Context, c *kernel.Call, in *snsapi.UnsubscribeInput) (*snsapi.UnsubscribeOutput, *awserr.Error) {
	if in.SubscriptionArn == "" {
		return nil, errReason("SubscriptionArn", "no value for required parameter")
	}
	ref, err := resolveSubscription(c, in.SubscriptionArn)
	if err != nil {
		return nil, err
	}
	if err := s.unsubscribe(c, ref, authenticated); err != nil {
		return nil, err
	}
	return &snsapi.UnsubscribeOutput{}, nil
}

// caller says whether an unsubscribe carried credentials: an Unsubscribe
// call does, a GET of the UnsubscribeURL does not.
type caller uint8

// caller values.
const (
	authenticated caller = iota + 1
	unauthenticated
)

// unsubscribe removes the subscription ref names, counting it in its
// topic's SubscriptionsDeleted. A pending subscription cannot be removed
// (sns.md 3.2: Terraform swallows exactly this text), and one confirmed
// with AuthenticateOnUnsubscribe only by an authenticated caller.
func (s *Service) unsubscribe(c *kernel.Call, ref subscriptionRef, by caller) *awserr.Error {
	_, err := kernel.Write(c, s.cells, ref.topic.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		t, ok := st.writable(ref.topic.name, now)
		i := t.indexOf(ref.id)
		if !ok || i < 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		switch status := t.Subscriptions[i].Status.(type) {
		case Pending:
			return struct{}{}, errInvalidParameter("Cannot unsubscribe a subscription that is pending confirmation")
		case Confirmed:
			if call, ok := status.By.(ByCall); ok && call.AuthenticateOnUnsubscribe && by != authenticated {
				return struct{}{}, errUnsubscribeNeedsCredentials()
			}
		}
		t.Subscriptions = slices.Delete(t.Subscriptions, i, i+1)
		t.Unsubscribed++
		st.Topics[ref.topic.name] = t
		return struct{}{}, nil
	})
	return err
}

// listSubscriptions lists every subscription in the caller's region: by
// topic name, then in creation order.
func (s *Service) listSubscriptions(_ context.Context, c *kernel.Call, in *snsapi.ListSubscriptionsInput) (*snsapi.ListSubscriptionsOutput, *awserr.Error) {
	scope := c.Scope()
	now := s.clock.Now()
	all, _ := read(s, scope, func(st *State) []snsapi.Subscription {
		var out []snsapi.Subscription
		for _, name := range slices.Sorted(maps.Keys(st.Topics)) {
			t := st.Topics[name]
			out = append(out, listed(topicRef{scope: scope, name: name}, &t, now)...)
		}
		return out
	})
	items, next, err := page(all, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &snsapi.ListSubscriptionsOutput{Subscriptions: nonNil(items), NextToken: next}, nil
}

// listSubscriptionsByTopic lists a topic's subscriptions in creation order.
func (s *Service) listSubscriptionsByTopic(_ context.Context, c *kernel.Call, in *snsapi.ListSubscriptionsByTopicInput) (*snsapi.ListSubscriptionsByTopicOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return nil, errTopicNotFound()
	}
	items, next, err := page(listed(ref, &t, s.clock.Now()), in.NextToken)
	if err != nil {
		return nil, err
	}
	return &snsapi.ListSubscriptionsByTopicOutput{Subscriptions: nonNil(items), NextToken: next}, nil
}

// listed renders t's subscriptions for a listing, without those expired by
// now; a pending one shows PendingConfirmation in place of its ARN
// (sns.md 3.4).
func listed(ref topicRef, t *Topic, now clock.Instant) []snsapi.Subscription {
	out := make([]snsapi.Subscription, 0, len(t.Subscriptions))
	topicARN := ref.arn().String()
	for _, sub := range t.Subscriptions {
		if sub.expired(now) {
			continue
		}
		arn := subscriptionRef{topic: ref, id: sub.ID}.arn().String()
		if _, pending := sub.Status.(Pending); pending {
			arn = pendingInListings().Value
		}
		out = append(out, snsapi.Subscription{
			SubscriptionArn: new(arn),
			Owner:           new(sub.OwnerAccountID),
			Protocol:        new(sub.Protocol.String()),
			Endpoint:        new(sub.Endpoint),
			TopicArn:        new(topicARN),
		})
	}
	return out
}

// nonNil returns an empty list for none, so the member is sent.
func nonNil(items []snsapi.Subscription) []snsapi.Subscription {
	if items == nil {
		return []snsapi.Subscription{}
	}
	return items
}
