package sns

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the
// recorded ones of sns.md unless a comment says otherwise.

// errInvalidParameter is InvalidParameter with SNS's "Invalid parameter: "
// prefix before detail.
func errInvalidParameter(detail string) *awserr.Error {
	return snsapi.ErrInvalidParameterException("Invalid parameter: " + detail)
}

// errReason is InvalidParameter naming member, with a reason.
func errReason(member, reason string) *awserr.Error {
	return errInvalidParameter(member + " Reason: " + reason)
}

func errARNElements(member string, n int) *awserr.Error {
	return errReason(member, "An ARN must have at least 6 elements, not "+strconv.Itoa(n))
}

func errTopicNotFound() *awserr.Error {
	return snsapi.ErrNotFoundException("Topic does not exist")
}

func errSubscriptionNotFound() *awserr.Error {
	return snsapi.ErrNotFoundException("Subscription does not exist")
}

// errCertificateNotFound answers a SigningCertURL that names no
// certificate understudy made; the text is understudy's own.
func errCertificateNotFound() *awserr.Error {
	return snsapi.ErrNotFoundException("Certificate does not exist; SigningCertURL paths come from notifications this emulator signed")
}

// errInvalidToken is ConfirmSubscription's answer for a token that is not
// hex text; a hex token that confirms nothing is "Invalid parameter:
// Token" (AWS-recorded, Service.confirm).
func errInvalidToken() *awserr.Error {
	return snsapi.ErrInvalidParameterException("Invalid token")
}

// errLargeMessages answers a SetTopicAttributes or Subscribe that would
// break the rule of a MaximumMessageSize above 262144 bytes; reason is
// Topic.largeMessageViolation's inferred text.
func errLargeMessages(reason string) *awserr.Error {
	return errReason(attrMaximumMessageSize, reason)
}

// errUnsubscribeNeedsCredentials answers an unsigned unsubscribe, such as
// a GET of the UnsubscribeURL, of a subscription with
// AuthenticateOnUnsubscribe=true (sns.md 3.2); why is
// Confirmed.credentialsRequired. The text is inferred: no recording shows
// AWS's.
func errUnsubscribeNeedsCredentials(why string) *awserr.Error {
	return errInvalidParameter("SubscriptionArn Reason: " + why + ", so only a signed Unsubscribe call can remove it; the subscription is unchanged")
}

// errResourceNotFound answers a tag operation on a topic that does not
// exist; the text is inferred.
func errResourceNotFound() *awserr.Error {
	return snsapi.ErrResourceNotFoundException("Resource does not exist")
}

// errParameterValue is ParameterValueInvalid, which carries message
// attribute errors (sns.md 4.2).
func errParameterValue(msg string) *awserr.Error {
	return snsapi.ErrInvalidParameterValueException(msg)
}

// errTagLimit is TagLimitExceeded for a topic past 50 tags; the text is
// inferred.
func errTagLimit() *awserr.Error {
	return snsapi.ErrTagLimitExceededException("Could not complete request: tag quota of per resource exceeded")
}

// errDataProtectionNotImplemented answers a CreateTopic that carries a
// DataProtectionPolicy, which understudy neither stores nor applies.
func errDataProtectionNotImplemented() *awserr.Error {
	return snsapi.ErrNotImplemented("understudy does not implement SNS message data protection; create the topic without DataProtectionPolicy. Run 'understudy coverage --service sns' for what is supported.")
}

// errSMSEndpoint answers an sms endpoint that is not an E.164 number; the
// text has no "Invalid parameter: " prefix (AWS-recorded, sns.md 3.1).
func errSMSEndpoint(endpoint string) *awserr.Error {
	return snsapi.ErrInvalidParameterException("Invalid SMS endpoint: " + endpoint)
}

// errProtocolNotImplemented is understudy's answer for an endpoint type it
// does not accept (ADR 0015): 501 in SNS's error format.
func errProtocolNotImplemented(protocol string) *awserr.Error {
	return snsapi.ErrNotImplemented("understudy does not implement SNS delivery to " + protocol +
		" endpoints; subscribe an sqs, lambda, http, https, email or email-json endpoint instead. Run 'understudy coverage --service sns' for what is supported.")
}

// errTargetNotImplemented answers a publish to a phone number or a mobile
// push endpoint (ADR 0015).
func errTargetNotImplemented(target string) *awserr.Error {
	return snsapi.ErrNotImplemented("understudy does not implement publishing to " + target +
		"; publish to a topic ARN instead. Run 'understudy coverage --service sns' for what is supported.")
}
