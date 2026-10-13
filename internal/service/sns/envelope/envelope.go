package envelope

import (
	"encoding/base64"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// Notification is one published message as a subscription receives it.
type Notification struct {
	MessageID string
	TopicARN  string
	// Subject is the publish's Subject; "" means none, since Publish
	// rejects an empty one.
	Subject string
	Message string
	// Timestamp is the publish instant, rendered in UTC with milliseconds.
	Timestamp time.Time
	// Attributes are the message attributes in rendering order.
	Attributes []Attribute
	// UnsubscribeURL is the subscription's unsubscribe link.
	UnsubscribeURL string
}

// Attribute is a message attribute as an envelope carries it.
type Attribute struct {
	Name string
	// Type is the DataType, label included: "Number.float".
	Type string
	// Value is the StringValue, or the BinaryValue in base64.
	Value string
}

// Signing is what a standard topic's notification is signed with.
type Signing struct {
	Version Version
	Signer  Signer
	// CertURL is where the certificate of the signer's key is served.
	CertURL string
}

// timestampLayout is the envelope Timestamp: UTC, exactly three fractional
// digits, Z (sns.md 5.1).
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp renders t as an envelope Timestamp.
func Timestamp(t time.Time) string { return t.UTC().Format(timestampLayout) }

// SQS renders the SQS message body of a standard topic's notification
// (sns.md 5.1).
func SQS(n Notification, s Signing) ([]byte, error) {
	signature, err := sign(s.Signer, s.Version, n)
	if err != nil {
		return nil, err
	}
	w := prettyWriter{}
	w.field("Type", "Notification")
	w.field("MessageId", n.MessageID)
	w.field("TopicArn", n.TopicARN)
	if n.Subject != "" {
		w.field("Subject", n.Subject)
	}
	w.field("Message", n.Message)
	w.field("Timestamp", Timestamp(n.Timestamp))
	w.field("SignatureVersion", s.Version.String())
	w.field("Signature", signature)
	w.field("SigningCertURL", s.CertURL)
	w.field("UnsubscribeURL", n.UnsubscribeURL)
	w.attributes(n.Attributes)
	return w.close(), nil
}

// SQSFIFO renders the SQS message body of a FIFO topic's notification:
// SequenceNumber after MessageId and no signature fields (sns.md 5.3).
func SQSFIFO(n Notification, sequenceNumber string) []byte {
	w := prettyWriter{}
	w.field("Type", "Notification")
	w.field("MessageId", n.MessageID)
	w.field("SequenceNumber", sequenceNumber)
	w.field("TopicArn", n.TopicARN)
	if n.Subject != "" {
		w.field("Subject", n.Subject)
	}
	w.field("Message", n.Message)
	w.field("Timestamp", Timestamp(n.Timestamp))
	w.field("UnsubscribeURL", n.UnsubscribeURL)
	w.attributes(n.Attributes)
	return w.close()
}

// Lambda renders the event a function subscribed to a standard topic is
// invoked with (sns.md 5.4). It is signed with the topic's SignatureVersion
// but always labelled "1": AWS signs a version 2 topic's event with
// SHA256withRSA and still reports SignatureVersion "1".
func Lambda(n Notification, subscriptionARN string, s Signing) ([]byte, error) {
	signature, err := sign(s.Signer, s.Version, n)
	if err != nil {
		return nil, err
	}
	b := []byte(`{"Records":[{"EventSource":"aws:sns","EventVersion":"1.0","EventSubscriptionArn":`)
	b = jsonx.AppendString(b, subscriptionARN)
	b = append(b, `,"Sns":{"Type":"Notification","MessageId":`...)
	b = jsonx.AppendString(b, n.MessageID)
	b = append(b, `,"TopicArn":`...)
	b = jsonx.AppendString(b, n.TopicARN)
	b = append(b, `,"Subject":`...)
	if n.Subject == "" {
		b = append(b, "null"...)
	} else {
		b = jsonx.AppendString(b, n.Subject)
	}
	b = append(b, `,"Message":`...)
	b = jsonx.AppendString(b, n.Message)
	b = append(b, `,"Timestamp":`...)
	b = jsonx.AppendString(b, Timestamp(n.Timestamp))
	b = append(b, `,"SignatureVersion":"1","Signature":`...)
	b = jsonx.AppendString(b, signature)
	b = append(b, `,"SigningCertUrl":`...)
	b = jsonx.AppendString(b, s.CertURL)
	b = append(b, `,"UnsubscribeUrl":`...)
	b = jsonx.AppendString(b, n.UnsubscribeURL)
	b = append(b, `,"MessageAttributes":{`...)
	for i, a := range n.Attributes {
		if i > 0 {
			b = append(b, ',')
		}
		b = jsonx.AppendString(b, a.Name)
		b = append(b, ':')
		b = appendAttributeValue(b, a)
	}
	return append(b, "}}}]}"...), nil
}

// Confirmation is the SubscriptionConfirmation message SNS sends an
// endpoint whose subscription waits for ConfirmSubscription (sns.md 5.5).
type Confirmation struct {
	MessageID string
	Token     string
	TopicARN  string
	// Timestamp is the Subscribe instant, rendered as a notification's.
	Timestamp time.Time
	// SubscribeURL is the link that confirms the subscription.
	SubscribeURL string
}

// message is the Message of a SubscriptionConfirmation (sns.md 5.5).
func (c Confirmation) message() string {
	return "You have chosen to subscribe to the topic " + c.TopicARN +
		".\nTo confirm the subscription, visit the SubscribeURL included in this message."
}

// SQSConfirmation renders the SQS message body of a SubscriptionConfirmation:
// the members of sns.md 5.5 in the documented order, in the layout of the
// notification envelope, signed.
func SQSConfirmation(c Confirmation, s Signing) ([]byte, error) {
	sig, err := s.Signer.Sign(s.Version, ConfirmationStringToSign(c))
	if err != nil {
		return nil, err
	}
	w := prettyWriter{}
	w.field("Type", "SubscriptionConfirmation")
	w.field("MessageId", c.MessageID)
	w.field("Token", c.Token)
	w.field("TopicArn", c.TopicARN)
	w.field("Message", c.message())
	w.field("SubscribeURL", c.SubscribeURL)
	w.field("Timestamp", Timestamp(c.Timestamp))
	w.field("SignatureVersion", s.Version.String())
	w.field("Signature", base64.StdEncoding.EncodeToString(sig))
	w.field("SigningCertURL", s.CertURL)
	return w.close(), nil
}

// sign signs n's string to sign and returns the signature in base64.
func sign(s Signer, v Version, n Notification) (string, error) {
	sig, err := s.Sign(v, StringToSign(n))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// canonical builds a string to sign (sns.md 5.6): each name and value on
// its own line, in the order the caller gives, which is byte order.
type canonical []byte

func (b *canonical) line(name, value string) {
	*b = append(*b, name...)
	*b = append(*b, '\n')
	*b = append(*b, value...)
	*b = append(*b, '\n')
}

// StringToSign is the canonical string of a notification (sns.md 5.6):
// names in byte order, Subject only when present.
func StringToSign(n Notification) []byte {
	var b canonical
	b.line("Message", n.Message)
	b.line("MessageId", n.MessageID)
	if n.Subject != "" {
		b.line("Subject", n.Subject)
	}
	b.line("Timestamp", Timestamp(n.Timestamp))
	b.line("TopicArn", n.TopicARN)
	b.line("Type", "Notification")
	return b
}

// ConfirmationStringToSign is the canonical string of a
// SubscriptionConfirmation (sns.md 5.6).
func ConfirmationStringToSign(c Confirmation) []byte {
	var b canonical
	b.line("Message", c.message())
	b.line("MessageId", c.MessageID)
	b.line("SubscribeURL", c.SubscribeURL)
	b.line("Timestamp", Timestamp(c.Timestamp))
	b.line("Token", c.Token)
	b.line("TopicArn", c.TopicARN)
	b.line("Type", "SubscriptionConfirmation")
	return b
}

// appendAttributeValue appends {"Type":...,"Value":...}.
func appendAttributeValue(b []byte, a Attribute) []byte {
	b = append(b, `{"Type":`...)
	b = jsonx.AppendString(b, a.Type)
	b = append(b, `,"Value":`...)
	b = jsonx.AppendString(b, a.Value)
	return append(b, '}')
}

// prettyWriter writes the SQS envelope layout: one "Key" : "value" pair per
// line, indented two spaces.
type prettyWriter struct {
	b []byte
}

func (w *prettyWriter) open() {
	if w.b == nil {
		w.b = append(w.b, "{\n"...)
	} else {
		w.b = append(w.b, ",\n"...)
	}
	w.b = append(w.b, "  "...)
}

func (w *prettyWriter) field(name, value string) {
	w.open()
	w.b = jsonx.AppendString(w.b, name)
	w.b = append(w.b, " : "...)
	w.b = jsonx.AppendString(w.b, value)
}

// attributes writes MessageAttributes, absent when there are none.
func (w *prettyWriter) attributes(as []Attribute) {
	if len(as) == 0 {
		return
	}
	w.open()
	w.b = append(w.b, `"MessageAttributes" : {`...)
	for i, a := range as {
		if i > 0 {
			w.b = append(w.b, ',')
		}
		w.b = append(w.b, "\n    "...)
		w.b = jsonx.AppendString(w.b, a.Name)
		w.b = append(w.b, " : "...)
		w.b = appendAttributeValue(w.b, a)
	}
	w.b = append(w.b, "\n  }"...)
}

func (w *prettyWriter) close() []byte {
	return append(w.b, "\n}"...)
}
