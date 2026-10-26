package sns

import "github.com/sustinbebustin/understudy/internal/delivery"

// httpRetryPolicy resolves the retry policy of an http or https
// subscription from its stored DeliveryPolicy and its topic's, "" for none
// (sns-http-delivery.md 6.2, 6.3). Not implemented yet: it returns the
// zero policy.
//
//nolint:unused // not implemented yet; the HTTP fan-out calls it once it is
func httpRetryPolicy(_, _ string) delivery.SNSHTTP {
	return delivery.SNSHTTP{}
}

// httpContentType is the effective requestPolicy.headerContentType of an
// http or https subscription: its own, else its topic's
// http.defaultRequestPolicy's, else "" (sns-http-delivery.md 3.3). Not
// implemented yet: it returns "".
//
//nolint:unused // not implemented yet; the HTTP fan-out calls it once it is
func httpContentType(_, _ string) string {
	return ""
}
