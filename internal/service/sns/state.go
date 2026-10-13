package sns

import (
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the schema version of State in snapshot sections.
const stateVersion = 2

// State is the SNS state of one account and region: its topics by name and
// the key notifications from them are signed with. Records are values,
// replaced on change and never mutated in place, so a value read out of a
// cell never aliases the cell.
type State struct {
	Topics map[string]Topic `json:"topics"`
	// Signing is made from crypto/rand on first use and kept, so the
	// certificate at SigningCertURL verifies every signature the scope
	// ever made (sns.md Implications 7).
	Signing *SigningKey `json:"signing,omitempty"`
	// Detached are the subscriptions of deleted topics, oldest first.
	// GetSubscriptionAttributes and Unsubscribe still find them until they
	// are gone; nothing is delivered to them.
	Detached []DetachedSubscription `json:"detached,omitempty"`
}

func newState(aws.Scope) State { return State{Topics: map[string]Topic{}} }

// IsZero reports a scope with no topics, no detached subscriptions and no
// key, which snapshots skip.
func (s *State) IsZero() bool { return len(s.Topics) == 0 && len(s.Detached) == 0 && s.Signing == nil }

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// DetachedSubscription is a subscription whose topic DeleteTopic removed.
// AWS keeps it readable for a while (detachedRetention).
type DetachedSubscription struct {
	// Topic is the name of the deleted topic, which the subscription's ARN
	// and its TopicArn attribute still name.
	Topic        string       `json:"topic"`
	Subscription Subscription `json:"subscription"`
	// Until is when it is gone.
	Until clock.Instant `json:"until"`
}

// gone reports a detached subscription past its retention, or a pending
// one whose token expired, by now.
func (d *DetachedSubscription) gone(now clock.Instant) bool {
	return !now.Before(d.Until) || d.Subscription.expired(now)
}

// detach moves t's subscriptions to s.Detached for detachedRetention from
// now, and forgets the detached subscriptions that are gone. s.Detached is
// replaced, never changed in place.
func (s *State) detach(t *Topic, now clock.Instant) {
	kept := slices.DeleteFunc(slices.Clone(s.Detached), func(d DetachedSubscription) bool { return d.gone(now) })
	until := now.Add(detachedRetention().Value)
	for i := range t.Subscriptions {
		kept = append(kept, DetachedSubscription{Topic: t.Name, Subscription: t.Subscriptions[i].clone(), Until: until})
	}
	if len(kept) == 0 {
		kept = nil
	}
	s.Detached = kept
}

// detachedIndex returns the index in s.Detached of the subscription ref
// names, unless it is gone by now; -1 when there is none.
func (s *State) detachedIndex(ref subscriptionRef, now clock.Instant) int {
	for i := range s.Detached {
		d := &s.Detached[i]
		if d.Topic == ref.topic.name && d.Subscription.ID == ref.id && !d.gone(now) {
			return i
		}
	}
	return -1
}

// SigningKey is the RSA key and certificate of an account and region, DER.
type SigningKey struct {
	Private     []byte `json:"private"`
	Certificate []byte `json:"certificate"`
}

// Topic is one topic.
type Topic struct {
	Name string    `json:"name"`
	Kind TopicKind `json:"kind"`
	// SignatureVersion is the SignatureVersion set on the topic; zero when
	// none was, which signs with version 1 and is not shown.
	SignatureVersion envelope.Version `json:"signatureVersion,omitzero"`
	// MaximumMessageSize is the MaximumMessageSize set on the topic, in
	// bytes; zero when none was, which allows 262144 and is not shown.
	MaximumMessageSize int `json:"maximumMessageSize,omitzero"`
	// Attributes are the settable attributes nothing in understudy acts
	// on, by name, in the form GetTopicAttributes returns them: Policy,
	// DeliveryPolicy, DisplayName and the rest of sns.md 2.2.
	Attributes map[string]string `json:"attributes,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	// Subscriptions are in creation order, the order fan-out delivers in.
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	// Unsubscribed counts subscriptions removed from the topic, as
	// SubscriptionsDeleted reports it.
	Unsubscribed int `json:"unsubscribed,omitempty"`
}

// TopicKind says whether a topic is standard or FIFO and holds what only a
// FIFO topic has. CreateTopic picks it from the name, which ends in .fifo
// exactly for a FIFO topic, so topicRef.fifo agrees with it.
//
//sumtype:decl
type TopicKind interface{ isTopicKind() }

// Standard is a standard topic.
type Standard struct{}

// FIFO is a FIFO topic and its ordering and deduplication state.
type FIFO struct {
	ContentBasedDeduplication bool `json:"contentBasedDeduplication,omitzero"`
	// ThroughputScope is FifoThroughputScope; zero when none was set,
	// which deduplicates topic-wide and is not shown.
	ThroughputScope ThroughputScope `json:"throughputScope,omitzero"`
	// Sequence is the last SequenceNumber a publish was given.
	Sequence uint64 `json:"sequence,omitzero"`
	// Dedup holds the deduplication keys seen in the last five minutes,
	// oldest first.
	Dedup []DedupEntry `json:"dedup,omitempty"`
}

func (Standard) isTopicKind() {}
func (FIFO) isTopicKind()     {}

// DedupEntry is one deduplication key and the end of its window.
type DedupEntry struct {
	Key   string        `json:"key"`
	Until clock.Instant `json:"until"`
}

// Subscription is one subscription of a topic.
type Subscription struct {
	// ID is the last element of the subscription ARN.
	ID       string   `json:"id"`
	Protocol Protocol `json:"protocol"`
	Endpoint string   `json:"endpoint"`
	// OwnerAccountID is the account that subscribed: Owner.
	OwnerAccountID string `json:"ownerAccountId"`
	// Principal is the ARN of the principal that subscribed:
	// SubscriptionPrincipal.
	Principal string       `json:"principal"`
	Status    Confirmation `json:"status"`
	// RawMessageDelivery is the RawMessageDelivery attribute: an sqs
	// endpoint gets the bare message rather than the envelope.
	RawMessageDelivery bool `json:"rawMessageDelivery,omitzero"`
	// Attributes are the other settable attributes, by name, as
	// GetSubscriptionAttributes returns them: FilterPolicy,
	// FilterPolicyScope, RedrivePolicy, DeliveryPolicy, SubscriptionRoleArn
	// and ReplayPolicy (sns.md 3.3).
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Confirmation says whether a subscription receives notifications.
//
//sumtype:decl
type Confirmation interface{ isConfirmation() }

// Confirmed is a subscription that receives notifications.
type Confirmed struct {
	By ConfirmedBy `json:"by"`
}

// Pending is a subscription waiting for ConfirmSubscription with Token.
type Pending struct {
	Token string `json:"token"`
	// Expires is when the token stops confirming and the subscription is
	// gone: two days after Subscribe.
	Expires clock.Instant `json:"expires"`
}

func (Confirmed) isConfirmation() {}
func (Pending) isConfirmation()   {}

// ConfirmedBy says how a subscription was confirmed, which decides
// ConfirmationWasAuthenticated and who may unsubscribe it.
//
//sumtype:decl
type ConfirmedBy interface{ isConfirmedBy() }

// AtSubscribe is a subscription SNS confirmed before Subscribe returned:
// authenticated, with no token.
type AtSubscribe struct {
	// AuthenticateOnUnsubscribe is set when the queue owner subscribed its
	// own queue, which AWS documents as setting AuthenticateOnUnsubscribe
	// automatically: only a caller with credentials may then unsubscribe.
	AuthenticateOnUnsubscribe bool `json:"authenticateOnUnsubscribe,omitzero"`
}

// ByCall is a subscription a caller with credentials confirmed through
// ConfirmSubscription: authenticated.
type ByCall struct {
	// Token is the token it was confirmed with, so confirming again is
	// idempotent.
	Token string `json:"token"`
	// AuthenticateOnUnsubscribe is ConfirmSubscription's
	// AuthenticateOnUnsubscribe: only a caller with credentials may then
	// unsubscribe.
	AuthenticateOnUnsubscribe bool `json:"authenticateOnUnsubscribe,omitzero"`
}

// ByLink is a subscription an unsigned GET of its SubscribeURL confirmed:
// not authenticated.
type ByLink struct {
	// Token is the token it was confirmed with, so confirming again is
	// idempotent.
	Token string `json:"token"`
}

func (AtSubscribe) isConfirmedBy() {}
func (ByCall) isConfirmedBy()      {}
func (ByLink) isConfirmedBy()      {}

// authenticated is ConfirmationWasAuthenticated: false only for a
// confirmation an unsigned request made.
func (c Confirmed) authenticated() bool {
	switch c.By.(type) {
	case AtSubscribe, ByCall:
		return true
	case ByLink:
		return false
	default:
		return false
	}
}

// credentialsRequired is why only an Unsubscribe call with credentials may
// remove the subscription, never the unsigned UnsubscribeURL: it was
// confirmed with AuthenticateOnUnsubscribe, or is a queue its owner
// subscribed. It is "" when an unsigned request may remove it.
func (c Confirmed) credentialsRequired() string {
	switch by := c.By.(type) {
	case AtSubscribe:
		if by.AuthenticateOnUnsubscribe {
			return "the queue owner subscribed its own queue, which sets AuthenticateOnUnsubscribe=true"
		}
	case ByCall:
		if by.AuthenticateOnUnsubscribe {
			return "the subscription was confirmed with AuthenticateOnUnsubscribe=true"
		}
	case ByLink:
	}
	return ""
}

// tokenConfirmation is a confirmation made with a token: ByCall or ByLink.
type tokenConfirmation interface {
	ConfirmedBy
	token() string
}

func (b ByCall) token() string { return b.Token }
func (b ByLink) token() string { return b.Token }

// expired reports a pending subscription whose token stopped confirming
// at or before now. AWS deletes it (sns.md 3.1), so every read skips it
// and every write of its topic drops it; no timer is needed.
func (sub *Subscription) expired(now clock.Instant) bool {
	p, ok := sub.Status.(Pending)
	return ok && !now.Before(p.Expires)
}

// dropExpired removes the expired pending subscriptions from t, which must
// not alias a cell. They are not counted in SubscriptionsDeleted: no source
// says AWS counts them.
func (t *Topic) dropExpired(now clock.Instant) {
	t.Subscriptions = slices.DeleteFunc(t.Subscriptions, func(sub Subscription) bool { return sub.expired(now) })
}

// writable returns a copy of the topic named name, to change and store
// back, without the subscriptions expired by now; false when there is no
// such topic. A write that then reports Unchanged leaves the expired
// subscriptions in the cell, where every read still skips them.
func (s *State) writable(name string, now clock.Instant) (Topic, bool) {
	existing, ok := s.Topics[name]
	if !ok {
		return Topic{}, false
	}
	t := existing.clone()
	t.dropExpired(now)
	return t, true
}

// ThroughputScope is a FIFO topic's FifoThroughputScope (sns.md 4.3).
type ThroughputScope uint8

// ThroughputScope values.
const (
	// ThroughputTopic deduplicates across the topic, as an unset scope does.
	ThroughputTopic ThroughputScope = iota + 1
	// ThroughputMessageGroup scopes deduplication to the message group.
	ThroughputMessageGroup
)

// String returns the attribute value: "Topic" or "MessageGroup".
func (s ThroughputScope) String() string {
	switch s {
	case ThroughputTopic:
		return "Topic"
	case ThroughputMessageGroup:
		return "MessageGroup"
	default:
		return fmt.Sprintf("ThroughputScope(%d)", uint8(s))
	}
}

// parseThroughputScope returns the ThroughputScope an attribute value names.
func parseThroughputScope(s string) (ThroughputScope, bool) {
	for _, scope := range []ThroughputScope{ThroughputTopic, ThroughputMessageGroup} {
		if scope.String() == s {
			return scope, true
		}
	}
	return 0, false
}

// ThroughputScopeError is a persisted FifoThroughputScope understudy does
// not know.
type ThroughputScopeError struct {
	Text string
}

func (e *ThroughputScopeError) Error() string {
	return fmt.Sprintf("sns state: %q names no FifoThroughputScope; known values are Topic and MessageGroup", e.Text)
}

// MarshalText returns the attribute value.
func (s ThroughputScope) MarshalText() ([]byte, error) {
	if _, ok := parseThroughputScope(s.String()); !ok {
		return nil, &ThroughputScopeError{Text: s.String()}
	}
	return []byte(s.String()), nil
}

// UnmarshalText parses an attribute value.
func (s *ThroughputScope) UnmarshalText(text []byte) error {
	parsed, ok := parseThroughputScope(string(text))
	if !ok {
		return &ThroughputScopeError{Text: string(text)}
	}
	*s = parsed
	return nil
}

// Protocol is a subscription's endpoint type among those understudy
// accepts: Subscribe answers sms, application and firehose with
// NotImplemented (ADR 0015), so they are not values.
type Protocol uint8

// Protocol values.
const (
	ProtocolSQS Protocol = iota + 1
	ProtocolLambda
	ProtocolHTTP
	ProtocolHTTPS
	ProtocolEmail
	ProtocolEmailJSON
)

// protocols lists every Protocol.
func protocols() []Protocol {
	return []Protocol{ProtocolSQS, ProtocolLambda, ProtocolHTTP, ProtocolHTTPS, ProtocolEmail, ProtocolEmailJSON}
}

// String returns the Protocol member Subscribe takes: "sqs", "email-json".
func (p Protocol) String() string {
	switch p {
	case ProtocolSQS:
		return "sqs"
	case ProtocolLambda:
		return "lambda"
	case ProtocolHTTP:
		return "http"
	case ProtocolHTTPS:
		return "https"
	case ProtocolEmail:
		return "email"
	case ProtocolEmailJSON:
		return "email-json"
	default:
		return fmt.Sprintf("Protocol(%d)", uint8(p))
	}
}

// parseProtocol returns the Protocol a Subscribe protocol string names.
func parseProtocol(s string) (Protocol, bool) {
	for _, p := range protocols() {
		if p.String() == s {
			return p, true
		}
	}
	return 0, false
}

// ProtocolError is a persisted protocol name understudy does not know.
type ProtocolError struct {
	Text string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("sns state: %q names no subscription protocol; known names are sqs, lambda, http, https, email and email-json", e.Text)
}

// MarshalText returns the protocol name.
func (p Protocol) MarshalText() ([]byte, error) {
	if _, ok := parseProtocol(p.String()); !ok {
		return nil, &ProtocolError{Text: p.String()}
	}
	return []byte(p.String()), nil
}

// UnmarshalText parses a protocol name.
func (p *Protocol) UnmarshalText(text []byte) error {
	parsed, ok := parseProtocol(string(text))
	if !ok {
		return &ProtocolError{Text: string(text)}
	}
	*p = parsed
	return nil
}

// confirmedAtOnce reports a protocol whose same-account subscription SNS
// confirms at Subscribe (sns.md 3.1); the others wait for
// ConfirmSubscription.
func (p Protocol) confirmedAtOnce() bool { return p == ProtocolSQS || p == ProtocolLambda }

// rawDelivery reports a protocol that takes RawMessageDelivery=true: AWS
// documents raw delivery for sqs, http, https and firehose only, and
// refuses it for email and sms (recorded against AWS by
// ballerinax-aws.sns); refusing it for lambda and email-json is inferred.
func (p Protocol) rawDelivery() bool {
	return p == ProtocolSQS || p == ProtocolHTTP || p == ProtocolHTTPS
}

// subscription returns a copy of the subscription ref names, unless it
// expired by now.
func (s *State) subscription(ref subscriptionRef, now clock.Instant) (Subscription, bool) {
	t, ok := s.Topics[ref.topic.name]
	if !ok {
		return Subscription{}, false
	}
	i := t.indexOf(ref.id)
	if i < 0 || t.Subscriptions[i].expired(now) {
		return Subscription{}, false
	}
	return t.Subscriptions[i].clone(), true
}

// indexOf returns the index of the subscription with id, or -1.
func (t *Topic) indexOf(id string) int {
	for i := range t.Subscriptions {
		if t.Subscriptions[i].ID == id {
			return i
		}
	}
	return -1
}

// clone returns a copy of t that shares nothing with it.
func (t *Topic) clone() Topic {
	out := *t
	out.Attributes = maps.Clone(t.Attributes)
	out.Tags = maps.Clone(t.Tags)
	out.Subscriptions = slices.Clone(t.Subscriptions)
	for i := range out.Subscriptions {
		out.Subscriptions[i] = out.Subscriptions[i].clone()
	}
	if f, ok := t.Kind.(FIFO); ok {
		f.Dedup = slices.Clone(f.Dedup)
		out.Kind = f
	}
	return out
}

// clone returns a copy of s that shares nothing with it.
func (sub *Subscription) clone() Subscription {
	out := *sub
	out.Attributes = maps.Clone(sub.Attributes)
	return out
}

// codec is the State codec: jsonx with the TopicKind, Confirmation and
// ConfirmedBy unions. The tables are static, so an error building one is a bug in it;
// the codec then fails every section with it.
func codec() store.Codec {
	failing := func(err error) store.Codec {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	kinds, err := jsonx.NewUnion("sns.TopicKind",
		jsonx.Variant[TopicKind, Standard]("standard"),
		jsonx.Variant[TopicKind, FIFO]("fifo"),
	)
	if err != nil {
		return failing(err)
	}
	statuses, err := jsonx.NewUnion("sns.Confirmation",
		jsonx.Variant[Confirmation, Confirmed]("confirmed"),
		jsonx.Variant[Confirmation, Pending]("pending"),
	)
	if err != nil {
		return failing(err)
	}
	confirmedBy, err := jsonx.NewUnion("sns.ConfirmedBy",
		jsonx.Variant[ConfirmedBy, AtSubscribe]("atSubscribe"),
		jsonx.Variant[ConfirmedBy, ByCall]("byCall"),
		jsonx.Variant[ConfirmedBy, ByLink]("byLink"),
	)
	if err != nil {
		return failing(err)
	}
	c := jsonx.NewCodec(kinds, statuses, confirmedBy)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
