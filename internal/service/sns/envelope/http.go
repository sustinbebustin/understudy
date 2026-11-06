package envelope

import (
	"encoding/base64"
	"net/url"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// HTTPNotification renders the body of a Notification POST
// (sns-http-delivery.md 4.1, 4.2): byte-identical to SQS(n, s). The doc
// samples of the HTTP Notification have the SQS envelope's members, order
// and layout; that the bytes match, the attribute block included, is
// UNVERIFIED until SNSH-01 records an HTTP body (4.1).
func HTTPNotification(n Notification, s Signing) ([]byte, error) {
	return SQS(n, s)
}

// HTTPConfirmation renders the body of a SubscriptionConfirmation POST
// (sns-http-delivery.md 4.3): byte-identical to SQSConfirmation(c, s), with
// the same layout caveat as HTTPNotification.
func HTTPConfirmation(c Confirmation, s Signing) ([]byte, error) {
	return SQSConfirmation(c, s)
}

// UnsubscribeConfirmation is the message an unauthenticated unsubscribe
// sends the endpoint (sns-http-delivery.md 2.4, 4.4).
type UnsubscribeConfirmation struct {
	MessageID string
	Token     string
	TopicARN  string
	// SubscriptionARN is the subscription removed, which Message names.
	SubscriptionARN string
	// Timestamp is the unsubscribe instant, rendered as a notification's.
	Timestamp time.Time
	// SubscribeURL is the confirm link of Token.
	SubscribeURL string
}

// control returns u's members (sns-http-delivery.md 4.4): its Message
// names the subscription removed and points at SubscribeURL.
func (u UnsubscribeConfirmation) control() control {
	return control{
		typ:       "UnsubscribeConfirmation",
		messageID: u.MessageID,
		token:     u.Token,
		topicARN:  u.TopicARN,
		message: "You have chosen to deactivate subscription " + u.SubscriptionARN +
			".\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.",
		subscribeURL: u.SubscribeURL,
		timestamp:    u.Timestamp,
	}
}

// HTTPUnsubscribeConfirmation renders an UnsubscribeConfirmation POST's
// body in the layout of sns-http-delivery.md 4.1, members in the order of
// 4.3 (Type, MessageId, Token, TopicArn, Message, SubscribeURL, Timestamp,
// SignatureVersion, Signature, SigningCertURL), signed over
// UnsubscribeConfirmationStringToSign.
func HTTPUnsubscribeConfirmation(u UnsubscribeConfirmation, s Signing) ([]byte, error) {
	return u.control().render(s)
}

// UnsubscribeConfirmationStringToSign is the canonical string of an
// UnsubscribeConfirmation: the confirmation field list of
// sns-http-delivery.md 5.1, Token included, as AWS signs it. sns-validator
// 0.3.5 checks it without Token and so rejects it, as it rejects AWS's own
// (5.3); the list is never bent to suit a validator (8.3.2, 8.8).
func UnsubscribeConfirmationStringToSign(u UnsubscribeConfirmation) []byte {
	return u.control().stringToSign()
}

// Header values of every POST (sns-http-delivery.md 3.2).
const (
	DefaultContentType = "text/plain; charset=UTF-8"
	UserAgent          = "Amazon Simple Notification Service Agent"
	// acceptEncoding is in every third-party-recorded request and in no doc
	// sample (3.2).
	acceptEncoding = "gzip,deflate"
)

// HTTPPost is one POST whose headers HTTPHeaders writes.
//
//sumtype:decl
type HTTPPost interface{ isHTTPPost() }

// NotificationPost is a Notification POST.
type NotificationPost struct {
	MessageID, TopicARN, SubscriptionARN string
	// Raw sends x-amz-sns-rawdelivery: true.
	Raw bool
	// ContentType is the effective requestPolicy.headerContentType; ""
	// sends DefaultContentType.
	ContentType string
}

// SubscriptionConfirmationPost is a SubscriptionConfirmation POST.
type SubscriptionConfirmationPost struct {
	MessageID, TopicARN string
	// ContentType is as NotificationPost's.
	ContentType string
}

// UnsubscribeConfirmationPost is an UnsubscribeConfirmation POST.
type UnsubscribeConfirmationPost struct {
	MessageID, TopicARN, SubscriptionARN string
	// ContentType is as NotificationPost's.
	ContentType string
}

func (NotificationPost) isHTTPPost()             {}
func (SubscriptionConfirmationPost) isHTTPPost() {}
func (UnsubscribeConfirmationPost) isHTTPPost()  {}

// HTTPHeaders returns the headers of a POST in this order, with the names
// as the documentation prints them: the x-amz-sns-* names in lower case,
// the standard ones capitalized (sns-http-delivery.md 3.2, 8.3.1):
//
//   - x-amz-sns-message-type: Notification, SubscriptionConfirmation or
//     UnsubscribeConfirmation;
//   - x-amz-sns-message-id and x-amz-sns-topic-arn;
//   - x-amz-sns-subscription-arn, except on a SubscriptionConfirmation;
//   - x-amz-sns-rawdelivery: true, on a raw notification only (4.5);
//   - Content-Type: the post's ContentType, sent verbatim, else
//     DefaultContentType, for the confirmations as for a notification
//     (3.3, sns.http.control-content-type, SNSH-03);
//   - User-Agent and Accept-Encoding;
//   - Authorization: Basic credentials from user when it is non-nil, on
//     the first request (2.1, 3.1, sns.http.basic-auth); a user without a
//     password sends "username:".
//
// The framing headers (Host, Content-Length, Connection) are the HTTP
// client's to write. A nil p has no headers.
func HTTPHeaders(p HTTPPost, user *url.Userinfo) []delivery.HTTPHeader {
	var (
		kind                                              delivery.HTTPKind
		messageID, topicARN, subscriptionARN, contentType string
		raw                                               bool
	)
	switch p := p.(type) {
	case NotificationPost:
		kind, messageID, topicARN, subscriptionARN, raw, contentType = delivery.HTTPNotification, p.MessageID, p.TopicARN, p.SubscriptionARN, p.Raw, p.ContentType
	case SubscriptionConfirmationPost:
		kind, messageID, topicARN, contentType = delivery.HTTPSubscriptionConfirmation, p.MessageID, p.TopicARN, p.ContentType
	case UnsubscribeConfirmationPost:
		kind, messageID, topicARN, subscriptionARN, contentType = delivery.HTTPUnsubscribeConfirmation, p.MessageID, p.TopicARN, p.SubscriptionARN, p.ContentType
	case nil:
		return nil
	}
	if contentType == "" {
		contentType = DefaultContentType
	}
	hs := make([]delivery.HTTPHeader, 0, 9)
	add := func(name, value string) { hs = append(hs, delivery.HTTPHeader{Name: name, Value: value}) }
	add("x-amz-sns-message-type", kind.String())
	add("x-amz-sns-message-id", messageID)
	add("x-amz-sns-topic-arn", topicARN)
	if kind != delivery.HTTPSubscriptionConfirmation {
		add("x-amz-sns-subscription-arn", subscriptionARN)
	}
	if raw {
		add("x-amz-sns-rawdelivery", "true")
	}
	add("Content-Type", contentType)
	add("User-Agent", UserAgent)
	add("Accept-Encoding", acceptEncoding)
	if user != nil {
		password, _ := user.Password()
		add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password)))
	}
	return hs
}
