package sns

import (
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// httpEndpoint is an http or https subscription's URL, which the outbox
// POSTs to through egress (sns-http-delivery.md 8.2).
type httpEndpoint struct{ url url.URL }

// route is SNSToHTTP and no ARN: the endpoint lands in no service.
func (httpEndpoint) route() (delivery.Mechanism, aws.ARN) { return delivery.SNSToHTTP, aws.ARN{} }

// httpURL parses the endpoint of an http or https subscription: an
// absolute URL with p's scheme and a host; false for anything else.
func httpURL(p Protocol, endpoint string) (*url.URL, bool) {
	if !strings.HasPrefix(endpoint, p.String()+"://") {
		return nil, false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	return u, true
}

// httpEndpointOf is the endpoint of sub, an http or https subscription.
// Subscribe refuses an endpoint httpURL does not parse, so only state
// loaded from a snapshot taken before it did can hold one.
func httpEndpointOf(sub *Subscription) (httpEndpoint, *awserr.Error) {
	u, ok := httpURL(sub.Protocol, sub.Endpoint)
	if !ok {
		return httpEndpoint{}, awserr.Internalf(&EndpointError{Subscription: sub.ID, Protocol: sub.Protocol, Endpoint: sub.Endpoint})
	}
	return httpEndpoint{url: *u}, nil
}

// EndpointError is a stored http or https subscription whose endpoint is
// not an absolute URL of its protocol with a host.
type EndpointError struct {
	Subscription string
	Protocol     Protocol
	Endpoint     string
}

func (e *EndpointError) Error() string {
	return "sns state: subscription " + e.Subscription + " has the " + e.Protocol.String() + " endpoint " + strconv.Quote(e.Endpoint) +
		", which is not an absolute " + e.Protocol.String() + " URL with a host, so nothing can be delivered to it; unsubscribe it and subscribe a valid URL"
}

// httpPolicyGap is what the coverage notes of Subscribe, Publish and
// PublishBatch say of httpRetryPolicy while it is not implemented.
const httpPolicyGap = "every http and https delivery retries 3 times 20 seconds apart, unthrottled: " +
	"the healthyRetryPolicy and throttlePolicy of a DeliveryPolicy are stored, never applied"

// httpRetryPolicy resolves the retry policy of an http or https
// subscription from its stored DeliveryPolicy and its topic's, "" for none
// (sns-http-delivery.md 6.2, 6.3). Not implemented yet: every http and
// https delivery takes the system default policy, three backoff retries
// 20 s apart without throttling, whatever either DeliveryPolicy says, as
// httpPolicyGap and the SetSubscriptionAttributes and SetTopicAttributes
// coverage notes say.
func httpRetryPolicy(_, _ string) delivery.SNSHTTP {
	return delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
}

// httpContentType is the effective requestPolicy.headerContentType of an
// http or https subscription: its own, else its topic's
// http.defaultRequestPolicy's, else "" (sns-http-delivery.md 3.3).
func httpContentType(subscriptionPolicy, topicPolicy string) string {
	if m, ok := parseObject(subscriptionPolicy); ok {
		if v := stringMember(mapMember(m, "requestPolicy"), "headerContentType"); v != "" {
			return v
		}
	}
	m, _ := parseObject(topicPolicy)
	return stringMember(mapMember(mapMember(m, "http"), "defaultRequestPolicy"), "headerContentType")
}

// httpPolicyOf is the retry policy of a delivery to sub, an http or https
// subscription of t.
func httpPolicyOf(sub *Subscription, t *Topic) delivery.SNSHTTP {
	return httpRetryPolicy(sub.Attributes[attrDeliveryPolicy], t.Attributes[attrDeliveryPolicy])
}

// httpConfirmation is the delivery of the SubscriptionConfirmation of sub,
// a pending http or https subscription to the topic ref names, to its
// endpoint, due at now (sns-http-delivery.md 2.2, 4.3, 8.6): signed with
// the scope's key at the topic's SignatureVersion, its SubscribeURL the
// confirm link of token on l. Under sns.http.control-dlq it never goes to
// the RedrivePolicy queue, and under sns.http.control-content-type it is
// text/plain whatever requestPolicy says.
func (s *Service) httpConfirmation(st *State, ref topicRef, t *Topic, sub *Subscription, token string, l links, now clock.Instant) (delivery.Request, *awserr.Error) {
	to, err := httpEndpointOf(sub)
	if err != nil {
		return delivery.Request{}, err
	}
	signing, err := s.signing(st.Signing, ref, t.signatureVersion(), l)
	if err != nil {
		return delivery.Request{}, err
	}
	c := s.confirmationMessage(ref, token, l, now)
	body, berr := envelope.HTTPConfirmation(c, signing)
	if berr != nil {
		return delivery.Request{}, awserr.Internalf(berr)
	}
	return delivery.Request{
		Source: ref.arn(), Mechanism: delivery.SNSToHTTP, Policy: httpPolicyOf(sub, t),
		Target: delivery.HTTPEndpoint{
			URL: to.url, Kind: delivery.HTTPSubscriptionConfirmation, Body: body,
			Headers: envelope.HTTPHeaders(envelope.SubscriptionConfirmationPost{MessageID: c.MessageID, TopicARN: c.TopicARN}, to.url.User),
		},
	}, nil
}

// unsubscribeConfirmation is the delivery of the UnsubscribeConfirmation
// that an unsigned GET of the UnsubscribeURL of sub, a confirmed http or
// https subscription of the topic ref names, sends its endpoint, due at
// now (sns-http-delivery.md 2.4, 4.4): a new MessageId and a new token,
// signed over the confirmation field list with Token (5.1). The token is
// kept nowhere, so its SubscribeURL confirms nothing: whether AWS's
// restores the subscription is UNVERIFIED (2.4, SNSH-16), and the
// UnsubscribeLink coverage note says understudy's does not.
func (s *Service) unsubscribeConfirmation(st *State, ref subscriptionRef, t *Topic, sub *Subscription, l links, now clock.Instant) (delivery.Request, *awserr.Error) {
	to, err := httpEndpointOf(sub)
	if err != nil {
		return delivery.Request{}, err
	}
	signing, err := s.signing(st.Signing, ref.topic, t.signatureVersion(), l)
	if err != nil {
		return delivery.Request{}, err
	}
	token := hex.EncodeToString(s.ids.Draw(ids.KindSNSMessageID, tokenBytes))
	topicARN := ref.topic.arn().String()
	u := envelope.UnsubscribeConfirmation{
		MessageID:       string(s.ids.SNSMessageID()),
		Token:           token,
		TopicARN:        topicARN,
		SubscriptionARN: ref.arn().String(),
		Timestamp:       now.Wire(),
		SubscribeURL:    l.confirm(topicARN, token),
	}
	body, berr := envelope.HTTPUnsubscribeConfirmation(u, signing)
	if berr != nil {
		return delivery.Request{}, awserr.Internalf(berr)
	}
	return delivery.Request{
		Source: ref.topic.arn(), Mechanism: delivery.SNSToHTTP, Policy: httpPolicyOf(sub, t),
		Target: delivery.HTTPEndpoint{
			URL: to.url, Kind: delivery.HTTPUnsubscribeConfirmation, Body: body,
			Headers: envelope.HTTPHeaders(envelope.UnsubscribeConfirmationPost{MessageID: u.MessageID, TopicARN: topicARN, SubscriptionARN: u.SubscriptionARN}, to.url.User),
		},
	}, nil
}

// httpSwitches are the behaviors of http and https delivery chosen without
// AWS-recorded evidence (sns-http-delivery.md 8.4).
func httpSwitches() []fidelity.Switch[string] {
	all := []string{"sns.Subscribe", "sns.Publish", "sns.PublishBatch", "sns.UnsubscribeLink"}
	control := []string{"sns.Subscribe", "sns.UnsubscribeLink"}
	return []fidelity.Switch[string]{
		httpSwitch("sns.http.status-classes", "retries-doc", "61-response-classes", "SNSH-05, SNSH-24", all),
		httpSwitch("sns.http.redirects", "not-followed", "31-method-url-and-connection", "SNSH-06", all),
		httpSwitch("sns.http.retry-body", "identical", "46-field-values", "SNSH-07", all),
		httpSwitch("sns.http.dlq-body", "as-sqs", "65-dead-letter-queue", "SNSH-08", publishOps()),
		httpSwitch("sns.http.backoff-curves", "linear-for-all", "63-retry-schedule", "SNSH-09", all),
		httpSwitch("sns.http.retry-jitter", "none", "63-retry-schedule", "SNSH-10", all),
		httpSwitch("sns.http.pending-messages", "dropped", "22-the-confirmation-post-and-the-pending-period", "SNSH-11", publishOps()),
		httpSwitch("sns.http.resubscribe-pending", "resend-same-token", "22-the-confirmation-post-and-the-pending-period", "SNSH-12", []string{"sns.Subscribe"}),
		httpSwitch("sns.http.unsubscribe-confirmation", "unauthenticated-only", "24-unsubscribing-and-the-unsubscribeconfirmation", "SNSH-15",
			[]string{"sns.Unsubscribe", "sns.UnsubscribeLink"}),
		httpSwitch("sns.http.control-dlq", "never", "65-dead-letter-queue", "SNSH-17", control),
		httpSwitch("sns.http.control-content-type", "text-plain", "33-content-type", "SNSH-03", control),
		httpSwitch("sns.http.basic-auth", "preemptive", "31-method-url-and-connection", "SNSH-22", all),
	}
}

// httpSwitch is one switch of sns-http-delivery.md 8.4: inferred, its
// grounds the section anchor names, settled by the recording queue items
// in record (9.1), which no scenario replays yet.
func httpSwitch(name, value, anchor, record string, ops []string) fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   evidence.Inferred,
		Ref:     "docs/research/sns-http-delivery.md#" + anchor,
		Record:  record,
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: "unrecorded: the sns-http-delivery.md recording queue holds " + record},
	}
}
