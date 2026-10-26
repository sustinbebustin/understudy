package envelope

import (
	"errors"
	"net/url"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// errHTTPNotImplemented is what the HTTP builders answer until they are
// implemented.
var errHTTPNotImplemented = errors.New("envelope: the HTTP builders are not implemented yet")

// HTTPNotification renders the body of a Notification POST
// (sns-http-delivery.md 4.1, 4.2): byte-identical to SQS(n, s). Not
// implemented yet: it returns an error.
func HTTPNotification(_ Notification, _ Signing) ([]byte, error) {
	return nil, errHTTPNotImplemented
}

// HTTPConfirmation renders the body of a SubscriptionConfirmation POST
// (sns-http-delivery.md 4.3): byte-identical to SQSConfirmation(c, s). Not
// implemented yet: it returns an error.
func HTTPConfirmation(_ Confirmation, _ Signing) ([]byte, error) {
	return nil, errHTTPNotImplemented
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

// HTTPUnsubscribeConfirmation renders an UnsubscribeConfirmation POST's
// body in the layout of sns-http-delivery.md 4.1, members in the order of
// 4.3, signed over UnsubscribeConfirmationStringToSign. Not implemented
// yet: it returns an error.
func HTTPUnsubscribeConfirmation(_ UnsubscribeConfirmation, _ Signing) ([]byte, error) {
	return nil, errHTTPNotImplemented
}

// UnsubscribeConfirmationStringToSign is the canonical string of an
// UnsubscribeConfirmation: the confirmation field list of
// sns-http-delivery.md 5.1, Token included (8.3.2, 8.8). Not implemented
// yet: it returns nil.
func UnsubscribeConfirmationStringToSign(_ UnsubscribeConfirmation) []byte {
	return nil
}

// Header values of every POST (sns-http-delivery.md 3.2).
const (
	DefaultContentType = "text/plain; charset=UTF-8"
	UserAgent          = "Amazon Simple Notification Service Agent"
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
}

// UnsubscribeConfirmationPost is an UnsubscribeConfirmation POST.
type UnsubscribeConfirmationPost struct {
	MessageID, TopicARN, SubscriptionARN string
}

func (NotificationPost) isHTTPPost()             {}
func (SubscriptionConfirmationPost) isHTTPPost() {}
func (UnsubscribeConfirmationPost) isHTTPPost()  {}

// HTTPHeaders returns the headers of a POST, with the names as the
// documentation prints them (sns-http-delivery.md 3.2, 3.3, 8.3.1), and
// Basic credentials from user when it is non-nil (sns.http.basic-auth). Not
// implemented yet: it returns none.
func HTTPHeaders(_ HTTPPost, _ *url.Userinfo) []delivery.HTTPHeader {
	return nil
}
