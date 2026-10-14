package sns

import (
	"maps"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// Topic attribute names (sns.md 2.2).
const (
	attrDisplayName               = "DisplayName"
	attrPolicy                    = "Policy"
	attrDeliveryPolicy            = "DeliveryPolicy"
	attrEffectiveDeliveryPolicy   = "EffectiveDeliveryPolicy"
	attrMaximumMessageSize        = "MaximumMessageSize"
	attrSignatureVersion          = "SignatureVersion"
	attrTracingConfig             = "TracingConfig"
	attrKmsMasterKeyID            = "KmsMasterKeyId"
	attrContentBasedDeduplication = "ContentBasedDeduplication"
	attrFifoThroughputScope       = "FifoThroughputScope"
	attrArchivePolicy             = "ArchivePolicy"
	attrFifoTopic                 = "FifoTopic"
	attrTopicArn                  = "TopicArn"
	attrOwner                     = "Owner"
	attrSubscriptionsConfirmed    = "SubscriptionsConfirmed"
	attrSubscriptionsPending      = "SubscriptionsPending"
	attrSubscriptionsDeleted      = "SubscriptionsDeleted"
)

// Topic limits and defaults.
const (
	fifoSuffix             = ".fifo"
	defaultMaxMessageBytes = 262144
	minMaxMessageBytes     = 1024
	maxMaxMessageBytes     = 1048576
	// maxLargeMessageSubscriptions bounds the subscriptions of a topic
	// whose MaximumMessageSize is above the default (sns.md 2.2).
	maxLargeMessageSubscriptions = 100
)

// feedbackProtocols prefix the delivery-status attributes:
// <Protocol>SuccessFeedbackRoleArn, <Protocol>FailureFeedbackRoleArn and
// <Protocol>SuccessFeedbackSampleRate.
func feedbackProtocols() []string {
	return []string{"HTTP", "Firehose", "Lambda", "Application", "SQS"}
}

// topicSetting is one checked attribute of a CreateTopic or
// SetTopicAttributes call.
type topicSetting struct {
	name string
	// shown is what GetTopicAttributes shows for name once the setting
	// applies; "" for an attribute it unsets.
	shown string
	apply func(t *Topic)
	// refuse, when set, is why the setting cannot apply to an existing
	// topic in its current state (a detail as topicAttribute returns), or
	// "" when it can. A new topic is never refused.
	refuse func(t *Topic) string
}

// refusal is why set cannot apply to the existing topic t, or "".
func (set topicSetting) refusal(t *Topic) string {
	if set.refuse == nil {
		return ""
	}
	return set.refuse(t)
}

// storedTopicAttribute is a setting kept in Attributes as shown; the empty
// string unsets it.
func storedTopicAttribute(name, value string) topicSetting {
	return topicSetting{name: name, shown: value, apply: func(t *Topic) {
		if value == "" {
			delete(t.Attributes, name)
			return
		}
		if t.Attributes == nil {
			t.Attributes = map[string]string{}
		}
		t.Attributes[name] = value
	}}
}

// topicAttribute checks a settable attribute of a topic (fifo says which
// kind). detail is the text after "Invalid parameter: " (after
// "Attributes Reason: " at CreateTopic), recorded where sns.md has it and
// inferred otherwise; it is "" when the setting is good.
func topicAttribute(fifo bool, name, value string) (set topicSetting, detail string) {
	switch name {
	case attrDisplayName, attrKmsMasterKeyID:
		return storedTopicAttribute(name, value), ""
	case attrPolicy, attrDeliveryPolicy:
		return documentAttribute(name, value)
	case attrContentBasedDeduplication, attrFifoThroughputScope, attrArchivePolicy:
		if !fifo {
			return topicSetting{}, name
		}
		return fifoAttribute(name, value)
	case attrMaximumMessageSize:
		n, err := strconv.Atoi(value)
		if err != nil || n < minMaxMessageBytes || n > maxMaxMessageBytes {
			return topicSetting{}, attrMaximumMessageSize
		}
		return topicSetting{name: name, shown: strconv.Itoa(n), apply: func(t *Topic) { t.MaximumMessageSize = n }}, ""
	case attrSignatureVersion:
		v, ok := envelope.ParseVersion(value)
		if !ok {
			return topicSetting{}, attrSignatureVersion
		}
		return topicSetting{name: name, shown: v.String(), apply: func(t *Topic) { t.SignatureVersion = v }}, ""
	case attrTracingConfig:
		if value != "PassThrough" && (value != "Active" || fifo) {
			return topicSetting{}, attrTracingConfig
		}
		return storedTopicAttribute(name, value), ""
	}
	return feedbackAttribute(name, value)
}

// documentAttribute checks Policy or DeliveryPolicy. The empty string
// unsets either: a topic without a Policy shows the default, since AWS
// keeps one on every topic (tf-messaging.md 3.4).
func documentAttribute(name, value string) (set topicSetting, detail string) {
	if value == "" {
		return storedTopicAttribute(name, ""), ""
	}
	if name == attrPolicy {
		doc, err := policydoc.Parse([]byte(value))
		if err != nil {
			return topicSetting{}, "Policy Error: " + err.Message()
		}
		if !inServiceScope(doc) {
			return topicSetting{}, errActionOutOfScope
		}
		return storedTopicAttribute(name, value), ""
	}
	m, ok := parseObject(value)
	if !ok {
		return topicSetting{}, attrDeliveryPolicy + ": failed to parse JSON."
	}
	if detail := checkTopicDeliveryPolicy(m); detail != "" {
		return topicSetting{}, detail
	}
	stored, ok := storedDeliveryPolicy(m)
	if !ok {
		return topicSetting{}, attrDeliveryPolicy + ": failed to parse JSON."
	}
	return storedTopicAttribute(name, stored), ""
}

// fifoAttribute checks an attribute only a FIFO topic has.
func fifoAttribute(name, value string) (set topicSetting, detail string) {
	switch name {
	case attrContentBasedDeduplication:
		on, err := strconv.ParseBool(value)
		if err != nil {
			return topicSetting{}, name
		}
		return topicSetting{name: name, shown: strconv.FormatBool(on), apply: func(t *Topic) {
			t.updateFIFO(func(f *FIFO) { f.ContentBasedDeduplication = on })
		}}, ""
	case attrFifoThroughputScope:
		scope, ok := parseThroughputScope(value)
		if !ok {
			return topicSetting{}, name
		}
		return topicSetting{
			name:  name,
			shown: scope.String(),
			apply: func(t *Topic) { t.updateFIFO(func(f *FIFO) { f.ThroughputScope = scope }) },
			refuse: func(t *Topic) string {
				// A MessageGroup scope cannot be reverted to Topic
				// (fifo-high-throughput.html). The text is inferred: no
				// recording shows AWS's.
				if f, ok := t.Kind.(FIFO); ok && f.ThroughputScope == ThroughputMessageGroup && scope == ThroughputTopic {
					return name + " Reason: a FifoThroughputScope of MessageGroup cannot be reverted to Topic"
				}
				return ""
			},
		}, ""
	}
	return storedTopicAttribute(name, value), ""
}

// updateFIFO runs fn on t's FIFO state. topicAttribute refuses a FIFO
// attribute for a standard topic, so a standard t is never asked.
func (t *Topic) updateFIFO(fn func(f *FIFO)) {
	if f, ok := t.Kind.(FIFO); ok {
		fn(&f)
		t.Kind = f
	}
}

// feedbackAttribute checks a delivery-status attribute; any other name is
// not a settable attribute.
func feedbackAttribute(name, value string) (set topicSetting, detail string) {
	for _, p := range feedbackProtocols() {
		switch name {
		case p + "SuccessFeedbackRoleArn", p + "FailureFeedbackRoleArn":
			return storedTopicAttribute(name, value), ""
		case p + "SuccessFeedbackSampleRate":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > 100 {
				return topicSetting{}, name
			}
			return storedTopicAttribute(name, strconv.Itoa(n)), ""
		}
	}
	return topicSetting{}, "AttributeName"
}

// renderTopicAttributes is GetTopicAttributes of t: the defaults AWS
// returns for every topic (sns.md 2.2), then every attribute set. t must
// hold no expired subscription.
func renderTopicAttributes(ref topicRef, t *Topic) map[string]string {
	confirmed, pending := 0, 0
	for i := range t.Subscriptions {
		switch t.Subscriptions[i].Status.(type) {
		case Pending:
			pending++
		case Confirmed:
			confirmed++
		}
	}
	out := map[string]string{
		attrTopicArn:                ref.arn().String(),
		attrOwner:                   ref.scope.Account.String(),
		attrDisplayName:             "",
		attrPolicy:                  defaultPolicy(ref),
		attrEffectiveDeliveryPolicy: defaultEffectiveDeliveryPolicy,
		attrSubscriptionsConfirmed:  strconv.Itoa(confirmed),
		attrSubscriptionsPending:    strconv.Itoa(pending),
		attrSubscriptionsDeleted:    strconv.Itoa(t.Unsubscribed),
	}
	if t.SignatureVersion != 0 {
		out[attrSignatureVersion] = t.SignatureVersion.String()
	}
	if t.MaximumMessageSize != 0 {
		out[attrMaximumMessageSize] = strconv.Itoa(t.MaximumMessageSize)
	}
	switch k := t.Kind.(type) {
	case Standard:
	case FIFO:
		out[attrFifoTopic] = "true"
		out[attrContentBasedDeduplication] = strconv.FormatBool(k.ContentBasedDeduplication)
		if k.ThroughputScope != 0 {
			out[attrFifoThroughputScope] = k.ThroughputScope.String()
		}
	}
	maps.Copy(out, t.Attributes)
	if dp, ok := t.Attributes[attrDeliveryPolicy]; ok {
		out[attrEffectiveDeliveryPolicy] = effectiveDeliveryPolicy(dp)
	}
	return out
}

// signatureVersion is the topic's SignatureVersion, 1 unless set.
func (t *Topic) signatureVersion() envelope.Version {
	if t.SignatureVersion == 0 {
		return envelope.V1
	}
	return t.SignatureVersion
}

// maxMessageBytes is the topic's MaximumMessageSize, 262144 unless set.
func (t *Topic) maxMessageBytes() int {
	if t.MaximumMessageSize == 0 {
		return defaultMaxMessageBytes
	}
	return t.MaximumMessageSize
}

// largeMessageViolation is why t breaks the rule of a MaximumMessageSize
// above 262144 bytes (sns.md 2.2): at most 100 subscriptions, all sqs,
// lambda or firehose. It is "" when t keeps the rule. The texts are
// inferred: no recording shows AWS's.
func (t *Topic) largeMessageViolation() string {
	if t.maxMessageBytes() <= defaultMaxMessageBytes {
		return ""
	}
	const prefix = "a topic whose MaximumMessageSize is above 262144 bytes "
	if len(t.Subscriptions) > maxLargeMessageSubscriptions {
		return prefix + "can have at most 100 subscriptions"
	}
	for i := range t.Subscriptions {
		if p := t.Subscriptions[i].Protocol; p != ProtocolSQS && p != ProtocolLambda {
			return prefix + "can have only sqs, lambda and firehose subscriptions, not " + p.String()
		}
	}
	return ""
}

// validTopicName checks a CreateTopic name (sns.md 2.1): 1 to 256
// characters of letters, digits, hyphens and underscores, a FIFO name
// ending in .fifo within the 256.
func validTopicName(name string, fifo bool) bool {
	base := name
	if fifo {
		var ok bool
		if base, ok = strings.CutSuffix(name, fifoSuffix); !ok {
			return false
		}
	}
	if base == "" || len(name) > 256 {
		return false
	}
	for _, r := range base {
		if !isAlnum(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
