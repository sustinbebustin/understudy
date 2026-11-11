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
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
	"github.com/sustinbebustin/understudy/internal/store"
)

const (
	// tokenValidity is how long a confirmation token confirms (sns.md
	// 3.1): two days of emulator time.
	tokenValidity = 48 * time.Hour
	// tokenBytes is the random length of a token, 288 lower-case hex
	// characters as in the ConfirmSubscription example (aws-doc,
	// sns-http-delivery.md 2.3); the docs state no length.
	tokenBytes = 144
	// pendingARN is Subscribe's SubscriptionArn for a subscription that
	// waits for confirmation, unless ReturnSubscriptionArn is set.
	pendingARN = "pending confirmation"
)

// bindSubscriptions binds the subscription lifecycle and listings.
// Subscribe's note is sns-http-delivery.md 8.1's text. A DeliveryPolicy
// passed to Subscribe applies as SetSubscriptionAttributes's does:
// throttlePolicy, sicklyRetryPolicy and guaranteed are stored and echoed,
// never applied (6.2, 6.4, SNSH-18, SNSH-20), and the 3,600-second total retry time is not checked
// (httpRetryPolicy).
func (s *Service) bindSubscriptions(b snsapi.Binder) {
	b.Subscribe(s.subscribe, registry.Partial{Note: "sqs and lambda endpoints are delivered to, and their failed deliveries go to the RedrivePolicy queue; " +
		"the endpoint's policy is not checked at Subscribe (sns.subscribe.policy-check), so under iam.enforce=enforce a policy that refuses SNS refuses each delivery; " +
		"http and https endpoints get a signed SubscriptionConfirmation when their host and port are in sns.httpDelivery.allow, " +
		"and otherwise stay subscribed with a readable token but receive nothing; loopback and private endpoints are accepted, where AWS refuses them; email and email-json stay subscribed with a readable token but receive nothing; " +
		"sms, application and firehose answer NotImplemented; the quota of 10,000 filter policies per account is not enforced"},
		kernel.WithResourceARN(func(in *snsapi.SubscribeInput) string { return in.TopicArn }),
		kernel.WithResources(func(_ *kernel.Call, in *snsapi.SubscribeInput) []aws.ARN {
			return subscriptionRole(in.Attributes[attrSubscriptionRoleArn])
		}))
	b.ConfirmSubscription(s.confirmSubscription, registry.Full{},
		kernel.WithResourceARN(func(in *snsapi.ConfirmSubscriptionInput) string { return in.TopicArn }))
	b.Unsubscribe(s.unsubscribeOp, registry.Full{},
		kernel.WithResourceARN(func(in *snsapi.UnsubscribeInput) string { return in.SubscriptionArn }))
	b.ListSubscriptions(s.listSubscriptions, registry.Full{})
	b.ListSubscriptionsByTopic(s.listSubscriptionsByTopic, registry.Full{},
		kernel.WithResourceARN(func(in *snsapi.ListSubscriptionsByTopicInput) string { return in.TopicArn }))
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
	protocol, err := subscribeProtocol(in.Protocol, endpoint, ref)
	if err != nil {
		return nil, err
	}
	owner, err := checkEndpoint(protocol, endpoint, ref)
	if err != nil {
		return nil, err
	}
	settings, err := s.subscriptionSettings(subscriber{protocol: protocol, endpoint: endpoint, topicFIFO: ref.fifo(), home: ref.scope}, in.Attributes)
	if err != nil {
		return nil, err
	}
	candidate := Subscription{
		ID:             s.ids.UUID(ids.KindSNSMessageID).String(),
		Protocol:       protocol,
		Endpoint:       endpoint,
		OwnerAccountID: c.Scope().Account.String(),
		Principal:      identity.Render(c.Principal()),
		// "AuthenticateOnUnsubscribe is automatically set to true when the
		// queue owner creates the subscription" (cross-account doc, sns.md
		// 3.1). A pending subscription replaces this status.
		Status: Confirmed{By: AtSubscribe{AuthenticateOnUnsubscribe: protocol == ProtocolSQS && owner == c.Scope().Account.String()}},
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
	// confirmedFromEndpoint: it waits as confirmedByToken does, and its
	// http or https endpoint gets a SubscriptionConfirmation POST carrying
	// the token (sns-http-delivery.md 2.2).
	confirmedFromEndpoint
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
	case protocol.http():
		return confirmedFromEndpoint
	default:
		return confirmedByToken
	}
}

// sendsConfirmation reports a route whose endpoint gets a signed
// SubscriptionConfirmation, which needs the scope's signing key.
func (r confirmRoute) sendsConfirmation() bool {
	return r == confirmedFromQueue || r == confirmedFromEndpoint
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
// (sns.md 3.1). A repeat for a pending http or https subscription gives it
// token as a further token and sends its endpoint a SubscriptionConfirmation
// carrying it (sns-http-delivery.md 2.2).
func (s *Service) addSubscription(c *kernel.Call, ref topicRef, candidate Subscription, settings []subscriptionSetting, route confirmRoute) (Subscription, *awserr.Error) {
	token := ""
	if route != confirmedAtSubscribe {
		token = hex.EncodeToString(s.ids.Draw(ids.KindSNSMessageID, tokenBytes))
	}
	if route.sendsConfirmation() {
		if err := s.ensureKey(c, ref); err != nil {
			return Subscription{}, err
		}
	}
	l := linksOf(c.Endpoint())
	if route == confirmedFromEndpoint {
		l = s.httpLinks(c.Endpoint())
	}
	out, err := kernel.Write(c, s.cells, ref.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (added, *awserr.Error) {
		t, ok := st.writable(ref.name, now)
		if !ok {
			return added{}, errTopicNotFound()
		}
		if existing, ok := t.subscribed(candidate.Protocol, candidate.Endpoint); ok {
			return added{sub: existing}, s.resubscribe(tx, route, st, ref, &t, &existing, settings, token, l, now)
		}
		if route != confirmedAtSubscribe {
			candidate.Status = Pending{Tokens: []string{token}, Expires: now.Add(tokenValidity)}
		}
		refused := t.admitFilterPolicy(&candidate)
		t.Subscriptions = append(t.Subscriptions, candidate)
		if reason := t.largeMessageViolation(); reason != "" {
			return added{}, errLargeMessages(reason)
		}
		if err := s.sendConfirmation(tx, route, st, ref, &t, &candidate, token, l, now); err != nil {
			return added{}, err
		}
		st.Topics[ref.name] = t
		return added{sub: candidate, refused: refused}, nil
	})
	if err != nil {
		return Subscription{}, err
	}
	return out.sub, out.refused
}

// resubscribe answers a repeated Subscribe of existing, the topic's
// subscription of the same protocol and endpoint: the conflict when
// settings disagree with it, else nil. A pending http or https subscription
// keeps its tokens and expiry, gains token, and its endpoint gets a
// SubscriptionConfirmation carrying token, with a new MessageId and the
// current Timestamp; each of its tokens then confirms it
// (sns-http-delivery.md 2.2, SNSH-12). Keeping the expiry is inferred:
// SNSH-12 did not record whether the repeat extends it. A confirmed
// subscription, or one of another protocol, gets nothing and is unchanged.
func (s *Service) resubscribe(tx *kernel.Tx, route confirmRoute, st *State, ref topicRef, t *Topic, existing *Subscription, settings []subscriptionSetting, token string, l links, now clock.Instant) *awserr.Error {
	if err := existing.agrees(settings); err != nil {
		return err
	}
	pending, isPending := existing.Status.(Pending)
	if !isPending || route != confirmedFromEndpoint {
		tx.Unchanged()
		return nil
	}
	pending.Tokens = append(slices.Clone(pending.Tokens), token)
	existing.Status = pending
	t.Subscriptions[t.indexOf(existing.ID)].Status = pending
	if err := s.sendConfirmation(tx, route, st, ref, t, existing, token, l, now); err != nil {
		return err
	}
	st.Topics[ref.name] = *t
	return nil
}

// sendConfirmation hands tx the SubscriptionConfirmation of token that
// route sends sub, a pending subscription of the topic ref names: to its
// queue, or to its http or https endpoint, its links on l. A route that
// sends none hands nothing.
func (s *Service) sendConfirmation(tx *kernel.Tx, route confirmRoute, st *State, ref topicRef, t *Topic, sub *Subscription, token string, l links, now clock.Instant) *awserr.Error {
	var (
		r   delivery.Request
		err *awserr.Error
	)
	switch route {
	case confirmedFromQueue:
		r, err = s.confirmation(st, ref, t, sub, token, l, now)
	case confirmedFromEndpoint:
		r, err = s.httpConfirmation(st, ref, t, sub, token, l, now)
	case confirmedAtSubscribe, confirmedByToken:
		return nil
	}
	if err != nil {
		return err
	}
	tx.Deliver(r)
	return nil
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
	c := s.confirmationMessage(ref, token, l, now)
	body, berr := envelope.SQSConfirmation(c, signing)
	if berr != nil {
		return delivery.Request{}, awserr.Internalf(berr)
	}
	target := delivery.SQSQueue{ARN: queue, Body: string(body)}
	if isFIFOQueue(queue.Resource) {
		target.GroupID, target.DedupID = sub.ID, c.MessageID
	}
	req := ref.deliveryFrom()
	req.Mechanism, req.Target = delivery.SNSToSQS, target
	return req, nil
}

// confirmationMessage is a SubscriptionConfirmation of token from the
// topic ref names at now, with a new MessageId and its SubscribeURL the
// confirm link of token on l (sns.md 5.5, sns-http-delivery.md 4.3).
func (s *Service) confirmationMessage(ref topicRef, token string, l links, now clock.Instant) envelope.Confirmation {
	topicARN := ref.arn().String()
	return envelope.Confirmation{
		MessageID:    string(s.ids.SNSMessageID()),
		Token:        token,
		TopicARN:     topicARN,
		Timestamp:    now.Wire(),
		SubscribeURL: l.confirm(topicARN, token),
	}
}

// subscribeProtocol parses Subscribe's Protocol: sms, application and
// firehose, which understudy does not accept, are NotImplemented (ADR
// 0015) on a standard topic and "Invalid protocol type" on a FIFO one, as
// every protocol but sqs is; anything else unknown is AWS's recorded text
// (sns.md 3.1). An sms endpoint is checked first, so a number AWS refuses
// gets AWS's answer rather than NotImplemented, and an application
// endpoint that is not an ARN is refused before anything else, as AWS
// recorded (testdata/parity/sns/R16/05-application-non-arn.json).
func subscribeProtocol(name, endpoint string, topic topicRef) (Protocol, *awserr.Error) {
	if p, ok := parseProtocol(name); ok {
		return p, nil
	}
	switch name {
	case "sms":
		if !e164.MatchString(endpoint) {
			return 0, errSMSEndpoint(endpoint)
		}
	case "application":
		if _, err := aws.ParseARN(endpoint); err != nil {
			return 0, errInvalidParameter("Application endpoint arn invalid:" + endpoint)
		}
	case "firehose":
	default:
		return 0, errInvalidParameter("Amazon SNS does not support this protocol string: " + name)
	}
	if topic.fifo() {
		return 0, errFIFOProtocol(name)
	}
	return 0, errProtocolNotImplemented(name)
}

// errFIFOProtocol answers a Subscribe of a FIFO topic with a protocol other
// than sqs. AWS recorded it for lambda and http (testdata/parity/sns/R16
// 04-subscribe-fifo-lambda.json, 04-subscribe-fifo-http.json); for the
// other protocols the text is inferred.
func errFIFOProtocol(protocol string) *awserr.Error {
	return errInvalidParameter("Invalid protocol type: " + protocol)
}

// e164 is an E.164 phone number with an optional leading plus, the form
// LocalStack's validated provider checks; it refuses every endpoint AWS
// recorded refusing (sns.md 3.1).
var e164 = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`)

// checkEndpoint checks a Subscribe endpoint against its protocol and the
// topic's kind and returns the account that owns an ARN endpoint, "" for
// the others. An sqs or lambda ARN must name a scope the outbox delivers
// to (aws.ScopeOf), or every later publish to the topic would fail. Only
// the SQS endpoint ARN text and the FIFO-queue-on-a-standard-topic text
// are AWS-recorded (sns.md 3.1), as is the FIFO-topic protocol text
// (errFIFOProtocol); the lambda and email texts are inferred. The http
// text is AWS's for an endpoint without the protocol's scheme
// (sns-http-delivery.md 2.1); giving it for a URL with the scheme that
// does not parse with a host is inferred.
func checkEndpoint(p Protocol, endpoint string, topic topicRef) (string, *awserr.Error) {
	if endpoint == "" {
		return "", errReason("Endpoint", "no value for required parameter")
	}
	if topic.fifo() && p != ProtocolSQS {
		return "", errFIFOProtocol(p.String())
	}
	switch p {
	case ProtocolSQS:
		return queueOwner(endpoint, topic)
	case ProtocolLambda:
		if _, a, ok := lambdaTarget(endpoint); ok {
			if _, err := aws.ScopeOf(a); err == nil {
				return a.Account, nil
			}
		}
		return "", errInvalidParameter("Lambda endpoint ARN")
	case ProtocolHTTP, ProtocolHTTPS:
		if _, ok := httpURL(p, endpoint); !ok {
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
	if _, err := aws.ScopeOf(a); err != nil {
		return "", errInvalidParameter("SQS endpoint ARN")
	}
	if !topic.fifo() && isFIFOQueue(a.Resource) {
		// The doubled "Invalid parameter: " prefix is AWS's (sns.md 3.1).
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
	// here (sns.md 3.2). Only the ARN's scope matters: AWS finds the token
	// in any topic of it, so a topic name that names no topic still
	// confirms (LocalStack nonexistent-token-confirm, AWS-recorded).
	ref, err := resolveTopic(c, "Topic", in.TopicArn)
	if err != nil {
		return nil, errInvalidParameter("Topic")
	}
	arn, err := s.confirm(c, ref.scope, ByCall{
		Token:                     in.Token,
		AuthenticateOnUnsubscribe: in.AuthenticateOnUnsubscribe != nil && strings.EqualFold(*in.AuthenticateOnUnsubscribe, "true"),
	})
	if err != nil {
		return nil, err
	}
	return &snsapi.ConfirmSubscriptionOutput{SubscriptionArn: new(arn.String())}, nil
}

// confirm confirms the subscription of any topic of scope that waits for
// by's token, as by says, and returns its ARN. Confirming again with the
// token it was confirmed with succeeds and changes nothing; any other of
// its tokens is 403 AuthorizationError "Subscription already confirmed"
// (sns-http-delivery.md 2.2, SNSH-12, AWS-recorded). The token of an
// UnsubscribeConfirmation restores the subscription it removed (restore).
// A token with a character that is not hex is "Invalid token"; any other
// token that confirms nothing, expired included, is "Invalid parameter: Token"
// (LocalStack test_sns_confirm_subscription_wrong_token and
// test_subscribe_external_http_endpoint, AWS-recorded). Which text an
// odd-length hex token gets is inferred: none was recorded.
func (s *Service) confirm(c *kernel.Call, scope aws.Scope, by tokenConfirmation) (aws.ARN, *awserr.Error) {
	token := by.token()
	if !hexText(token) {
		return aws.ARN{}, errInvalidToken()
	}
	ref, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, tx *kernel.Tx) (subscriptionRef, *awserr.Error) {
		name, i, ok := st.holder(token, now)
		if !ok {
			return st.restore(scope, token, by, now)
		}
		ref := subscriptionRef{topic: topicRef{scope: scope, name: name}, id: st.Topics[name].Subscriptions[i].ID}
		pending, isPending := st.Topics[name].Subscriptions[i].Status.(Pending)
		if !isPending {
			tx.Unchanged()
			if st.Topics[name].Subscriptions[i].confirmedWith(token) {
				return ref, nil
			}
			return subscriptionRef{}, errAlreadyConfirmed()
		}
		t, _ := st.writable(name, now)
		t.Subscriptions[t.indexOf(ref.id)].Status = Confirmed{By: by, Unused: pending.othersThan(token)}
		st.Topics[name] = t
		return ref, nil
	})
	if err != nil {
		return aws.ARN{}, err
	}
	return ref.arn(), nil
}

// holder finds the subscription that holds token: the pending one with it
// among its tokens, unless expired by now, or the confirmed one that was
// confirmed with it or held it unused. It returns the topic's name and the
// subscription's index in it; topics are searched in name order, though a
// token drawn from 144 random bytes is in at most one of them.
func (s *State) holder(token string, now clock.Instant) (topic string, index int, ok bool) {
	for _, name := range slices.Sorted(maps.Keys(s.Topics)) {
		subs := s.Topics[name].Subscriptions
		for i := range subs {
			switch status := subs[i].Status.(type) {
			case Pending:
				if slices.Contains(status.Tokens, token) && !subs[i].expired(now) {
					return name, i, true
				}
			case Confirmed:
				if subs[i].confirmedWith(token) || slices.Contains(status.Unused, token) {
					return name, i, true
				}
			}
		}
	}
	return "", 0, false
}

// restore puts back, confirmed as by says, the restorable subscription of
// any topic of scope that token restores, and returns its ref
// (RestorableSubscription). A token that restores none, gone by now
// included, is "Invalid parameter: Token", as any token that confirms
// nothing (2.3). So is one whose topic has subscribed the same endpoint
// again since, which would otherwise hold it twice; that answer is
// inferred. A FilterPolicy the topic's quota no longer has room for
// refuses the restore with the quota error and keeps it restorable, which
// is inferred too.
func (s *State) restore(scope aws.Scope, token string, by tokenConfirmation, now clock.Instant) (subscriptionRef, *awserr.Error) {
	for _, name := range slices.Sorted(maps.Keys(s.Topics)) {
		if existing := s.Topics[name]; existing.restorableIndex(token, now) < 0 {
			continue
		}
		t, _ := s.writable(name, now)
		j := t.restorableIndex(token, now)
		sub := t.Restorable[j].Subscription
		if _, again := t.subscribed(sub.Protocol, sub.Endpoint); again {
			return subscriptionRef{}, errInvalidParameter("Token")
		}
		if _, filtered := sub.Attributes[attrFilterPolicy]; filtered {
			if err := t.checkFilterPolicyQuota(sub.ID); err != nil {
				return subscriptionRef{}, err
			}
		}
		sub.Status = Confirmed{By: by}
		t.Restorable = slices.Delete(t.Restorable, j, j+1)
		if len(t.Restorable) == 0 {
			t.Restorable = nil
		}
		t.Subscriptions = append(t.Subscriptions, sub)
		s.Topics[name] = t
		return subscriptionRef{topic: topicRef{scope: scope, name: name}, id: sub.ID}, nil
	}
	return subscriptionRef{}, errInvalidParameter("Token")
}

// restores reports whether token restores a subscription of s, unless
// gone by now.
func (s *State) restores(token string, now clock.Instant) bool {
	for _, t := range s.Topics {
		if t.restorableIndex(token, now) >= 0 {
			return true
		}
	}
	return false
}

// confirmedWith reports a subscription confirmed with token.
func (sub *Subscription) confirmedWith(token string) bool {
	c, ok := sub.Status.(Confirmed)
	if !ok {
		return false
	}
	prior, ok := c.By.(tokenConfirmation)
	return ok && prior.token() == token
}

// hexText reports whether s holds hex digits only.
func hexText(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
	})
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
// topic's SubscriptionsDeleted, or forgets it when DeleteTopic detached
// it. A pending subscription cannot be removed (sns.md 3.2: Terraform
// swallows exactly this text), and one with AuthenticateOnUnsubscribe only
// by an authenticated caller. Under sns.http.unsubscribe-confirmation an
// unauthenticated caller that removes an http or https subscription still
// attached to its topic sends its endpoint an UnsubscribeConfirmation
// (sns-http-delivery.md 2.4), whose token then restores it
// (RestorableSubscription); a detached one, whose topic is gone, sends
// nothing, which is inferred.
func (s *Service) unsubscribe(c *kernel.Call, ref subscriptionRef, by caller) *awserr.Error {
	l := s.httpLinks(c.Endpoint())
	_, err := kernel.Write(c, s.cells, ref.topic.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		t, ok := st.writable(ref.topic.name, now)
		i := t.indexOf(ref.id)
		if !ok || i < 0 {
			j := st.detachedIndex(ref, now)
			if j < 0 {
				tx.Unchanged()
				return struct{}{}, nil
			}
			if err := mayUnsubscribe(&st.Detached[j].Subscription, by); err != nil {
				return struct{}{}, err
			}
			st.Detached = slices.Delete(slices.Clone(st.Detached), j, j+1)
			if len(st.Detached) == 0 {
				st.Detached = nil
			}
			return struct{}{}, nil
		}
		if err := mayUnsubscribe(&t.Subscriptions[i], by); err != nil {
			return struct{}{}, err
		}
		if by == unauthenticated && t.Subscriptions[i].Protocol.http() {
			if err := s.sendUnsubscribeConfirmation(tx, st, ref, &t, &t.Subscriptions[i], l, now); err != nil {
				return struct{}{}, err
			}
		}
		t.Subscriptions = slices.Delete(t.Subscriptions, i, i+1)
		t.Unsubscribed++
		st.Topics[ref.topic.name] = t
		return struct{}{}, nil
	})
	return err
}

// mayUnsubscribe is nil when by may remove sub: a confirmed subscription
// that does not require credentials, or any confirmed one for a caller
// with them.
func mayUnsubscribe(sub *Subscription, by caller) *awserr.Error {
	switch status := sub.Status.(type) {
	case Pending:
		return errUnsubscribePending()
	case Confirmed:
		if status.credentialsRequired() && by != authenticated {
			return errUnsubscribeNeedsCredentials()
		}
	}
	return nil
}

// listSubscriptions lists the caller's subscriptions in its partition and
// region, those it owns on another account's topic included: "Returns a
// list of the requester's subscriptions" (API_ListSubscriptions.html). A
// subscription another account made on the caller's topic is that
// account's. The subscriptions of a deleted topic are listed until they
// are gone (sns/R16 06-list-subscriptions-after-delete, detachedRetention).
// They come ordered by subscription ARN.
func (s *Service) listSubscriptions(_ context.Context, c *kernel.Call, in *snsapi.ListSubscriptionsInput) (*snsapi.ListSubscriptionsOutput, *awserr.Error) {
	own := c.Scope()
	owner := own.Account.String()
	now := s.clock.Now()
	var all []listedSubscription
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if scope.Partition != own.Partition || scope.Region != own.Region {
			continue
		}
		mine := store.View(s.cells, scope, func(st *State) []listedSubscription {
			out := make([]listedSubscription, 0, len(st.Topics))
			for _, name := range slices.Sorted(maps.Keys(st.Topics)) {
				t := st.Topics[name]
				t.Subscriptions = slices.DeleteFunc(slices.Clone(t.Subscriptions), func(sub Subscription) bool { return sub.OwnerAccountID != owner })
				out = append(out, listed(topicRef{scope: scope, name: name}, &t, now)...)
			}
			for i := range st.Detached {
				d := &st.Detached[i]
				if d.Subscription.OwnerAccountID == owner && !d.gone(now) {
					out = append(out, listedOne(topicRef{scope: scope, name: d.Topic}, &d.Subscription))
				}
			}
			return out
		})
		all = append(all, mine...)
	}
	items, next, err := subscriptionsPage(listingSubscriptions, all, in.NextToken)
	if err != nil {
		return nil, err
	}
	return &snsapi.ListSubscriptionsOutput{Subscriptions: items, NextToken: next}, nil
}

// listSubscriptionsByTopic lists a topic's subscriptions ordered by ARN.
func (s *Service) listSubscriptionsByTopic(_ context.Context, c *kernel.Call, in *snsapi.ListSubscriptionsByTopicInput) (*snsapi.ListSubscriptionsByTopicOutput, *awserr.Error) {
	ref, err := resolveTopic(c, "TopicArn", in.TopicArn)
	if err != nil {
		return nil, err
	}
	t, ok := s.readTopic(ref)
	if !ok {
		return nil, errTopicNotFound()
	}
	items, next, err := subscriptionsPage(listingTopicSubscriptions, listed(ref, &t, s.clock.Now()), in.NextToken)
	if err != nil {
		return nil, err
	}
	return &snsapi.ListSubscriptionsByTopicOutput{Subscriptions: items, NextToken: next}, nil
}

// listedSubscription is a subscription as a listing renders it, with the
// ARN the listing pages by. That is the subscription's own ARN even where
// the listing shows PendingConfirmation, which is not unique.
type listedSubscription struct {
	arn string
	api snsapi.Subscription
}

func (l listedSubscription) key() string { return l.arn }

// listed renders t's subscriptions for a listing, without those expired by
// now; a pending one shows PendingConfirmation in place of its ARN
// (sns.md 3.4), and an endpoint's password is masked (maskedEndpoint).
func listed(ref topicRef, t *Topic, now clock.Instant) []listedSubscription {
	out := make([]listedSubscription, 0, len(t.Subscriptions))
	for i := range t.Subscriptions {
		if !t.Subscriptions[i].expired(now) {
			out = append(out, listedOne(ref, &t.Subscriptions[i]))
		}
	}
	return out
}

// listedOne renders sub, a subscription to the topic ref names, for a
// listing.
func listedOne(ref topicRef, sub *Subscription) listedSubscription {
	own := subscriptionRef{topic: ref, id: sub.ID}.arn().String()
	shown := own
	if _, pending := sub.Status.(Pending); pending {
		shown = pendingInListings().Value
	}
	return listedSubscription{arn: own, api: snsapi.Subscription{
		SubscriptionArn: new(shown),
		Owner:           new(sub.OwnerAccountID),
		Protocol:        new(sub.Protocol.String()),
		Endpoint:        new(maskedEndpoint(sub.Endpoint)),
		TopicArn:        new(ref.arn().String()),
	}}
}

// subscriptionsPage is the page of listing l that token names, as the
// Subscription members a response carries; none is an empty list, so the
// member is sent.
func subscriptionsPage(l paging.Listing, all []listedSubscription, token *string) ([]snsapi.Subscription, *string, *awserr.Error) {
	p, err := page(l, all, listedSubscription.key, token)
	if err != nil {
		return nil, nil, err
	}
	items := make([]snsapi.Subscription, len(p.Items))
	for i, s := range p.Items {
		items[i] = s.api
	}
	return items, p.Next, nil
}

// maskedEndpoint is endpoint as a listing shows it: the password of a
// URL's user info replaced by ****, the rest as sent (SNSH-22,
// sns-http-delivery.md 2.1). GetSubscriptionAttributes returns it
// verbatim. The user info ends at the last @ before the path.
func maskedEndpoint(endpoint string) string {
	scheme, rest, ok := strings.Cut(endpoint, "://")
	if !ok {
		return endpoint
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return endpoint
	}
	user, _, hasPassword := strings.Cut(authority[:at], ":")
	if !hasPassword {
		return endpoint
	}
	return scheme + "://" + user + ":****" + rest[at:]
}
