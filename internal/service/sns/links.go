package sns

import (
	"context"
	"encoding/xml"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// linkPrefix is where SNS's AWS-visible links live: the reserved prefix,
// then the service and the raw operation that serves the link.
const linkPrefix = "/_understudy/sns/"

// bindLinks binds the raw operations behind the links a notification
// carries: the signing certificate and the unsubscribe and confirm links.
func (s *Service) bindLinks(b snsapi.Binder) {
	b.SigningCert(s.signingCert, registry.Full{})
	b.UnsubscribeLink(s.unsubscribeLink, registry.Partial{Note: "the UnsubscribeConfirmation reaches the endpoint only when its host and port are in sns.httpDelivery.allow; " +
		"its Token and SubscribeURL restore the subscription for two days of emulator time, an inferred lifetime"})
	b.ConfirmSubscriptionLink(s.confirmLink, registry.Full{})
}

// links builds the URLs a notification carries, on the host the
// publishing client addressed.
type links struct {
	base string
}

func linksOf(e kernel.Endpoint) links { return links{base: e.String() + linkPrefix} }

// httpLinks builds the links of an http or https delivery: on the TLS
// listener as https://localhost:<port> while one serves, so the
// validators' https and .pem checks can pass (sns-http-delivery.md 5.6,
// 8.3.3), else on the host the calling client addressed, as every other
// delivery's. Never on an AWS host (8.8): an exposed listener answers any
// Host from a client that is not a browser, and a configured S3 base
// domain can be an AWS one, so a client that addressed understudy by an
// AWS name gets the links on localhost at the port it used. A receiver on
// another host then cannot follow them; it confirms with the Token
// instead.
func (s *Service) httpLinks(e kernel.Endpoint) links {
	if s.tlsPort != nil {
		if port, ok := s.tlsPort(); ok {
			return links{base: "https://localhost:" + strconv.FormatUint(uint64(port), 10) + linkPrefix}
		}
	}
	if s.awsName != nil {
		host, port, err := net.SplitHostPort(e.Host)
		if err != nil {
			// No port: the Host is the name alone.
			host, port = e.Host, ""
		}
		if s.awsName(host) {
			e.Host = "localhost"
			if port != "" {
				e.Host += ":" + port
			}
		}
	}
	return linksOf(e)
}

// unsubscribe is a subscription's UnsubscribeURL, shaped as AWS's
// (sns.md 5.1) under the reserved prefix.
func (l links) unsubscribe(subscriptionARN string) string {
	return l.base + "UnsubscribeLink?Action=Unsubscribe&SubscriptionArn=" + subscriptionARN
}

// confirm is the SubscribeURL of a SubscriptionConfirmation, shaped as
// AWS's (sns.md 5.5) under the reserved prefix.
func (l links) confirm(topicARN, token string) string {
	return l.base + "ConfirmSubscriptionLink?Action=ConfirmSubscription&TopicArn=" + topicARN + "&Token=" + token
}

// cert is the SigningCertURL of the certificate name of scope.
func (l links) cert(scope aws.Scope, name string) string {
	return l.base + "SigningCert/" + scope.Partition.String() + "/" + scope.Region.String() + "/" + scope.Account.String() + "/" + name
}

// signingCert serves a scope's certificate at SigningCertURL:
// /_understudy/sns/SigningCert/<partition>/<region>/<account>/<name>.pem.
func (s *Service) signingCert(_ context.Context, _ *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	rest, _ := strings.CutPrefix(r.URL.Path, linkPrefix+"SigningCert/")
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return errCertificateNotFound()
	}
	scope, ok := s.scopeOf(parts[0], parts[2], parts[1])
	if !ok {
		return errCertificateNotFound()
	}
	pem := store.View(s.cells, scope, func(st *State) []byte {
		if st.Signing == nil {
			return nil
		}
		key := st.Signing.key()
		if key.CertificateName() != parts[3] {
			return nil
		}
		return key.CertificatePEM()
	})
	if pem == nil {
		return errCertificateNotFound()
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pem) //nolint:errcheck // the client went away; nothing is left to tell it
	return nil
}

// unsubscribeLink serves UnsubscribeURL: Unsubscribe without credentials,
// the subscription named by the SubscriptionArn query member. The
// UnsubscribeConfirmation it sends an http or https endpoint is signed, so
// when it will send one, a scope with no key (an older snapshot's) gets one
// first, as for a publish.
func (s *Service) unsubscribeLink(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	arn := r.URL.Query().Get("SubscriptionArn")
	if _, err := aws.ParseARN(arn); err != nil {
		return errInvalidParameter("SubscriptionArn")
	}
	if ref, ok := s.subscriptionOf(arn); ok {
		c.Touch(ref.arn())
		if s.confirmsUnsubscribe(ref) {
			if err := s.ensureKey(c, ref.topic); err != nil {
				return err
			}
		}
		if err := s.unsubscribe(c, ref, unauthenticated); err != nil {
			return err
		}
	}
	writeQueryResponse(w, c, "Unsubscribe", "")
	return nil
}

// confirmsUnsubscribe reports whether an unsigned unsubscribe of ref, as
// the state stands, sends an UnsubscribeConfirmation: ref is a confirmed
// http or https subscription with a usable endpoint, attached to a
// standard topic, that an unauthenticated caller may remove. Only then
// does unsubscribeLink need the scope's signing key.
func (s *Service) confirmsUnsubscribe(ref subscriptionRef) bool {
	if ref.topic.fifo() {
		return false
	}
	sends := store.View(s.cells, ref.topic.scope, func(st *State) bool {
		t, ok := st.Topics[ref.topic.name]
		i := t.indexOf(ref.id)
		if !ok || i < 0 {
			return false
		}
		sub := &t.Subscriptions[i]
		if _, bad := httpEndpointOf(sub); !sub.Protocol.http() || bad != nil {
			return false
		}
		_, confirmed := sub.Status.(Confirmed)
		return confirmed && mayUnsubscribe(sub, unauthenticated) == nil
	})
	return sends
}

// confirmLink serves the SubscribeURL of a confirmation message:
// ConfirmSubscription without credentials, from the TopicArn and Token
// query members, so ConfirmationWasAuthenticated reads false. The
// SubscribeURL of an UnsubscribeConfirmation restores the subscription it
// removed (RestorableSubscription). As with the
// call, only the ARN's scope matters, not its topic name; a token held in
// another scope, such as the same topic in another region, is "Invalid
// parameter: Topic" (LocalStack test_subscribe_external_http_endpoint,
// AWS-recorded).
func (s *Service) confirmLink(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	q := r.URL.Query()
	ref, ok := s.topicOf(q.Get("TopicArn"))
	token := q.Get("Token")
	if home, held := s.tokenScope(token); !ok || (held && home != ref.scope) {
		return errInvalidParameter("Topic")
	}
	c.Touch(ref.arn())
	arn, err := s.confirm(c, ref.scope, ByLink{Token: token})
	if err != nil {
		return err
	}
	var result strings.Builder
	result.WriteString("<SubscriptionArn>")
	_ = xml.EscapeText(&result, []byte(arn.String())) //nolint:errcheck // a strings.Builder does not fail
	result.WriteString("</SubscriptionArn>")
	writeQueryResponse(w, c, "ConfirmSubscription", result.String())
	return nil
}

// tokenScope returns the scope with SNS state where token confirms or
// restores a subscription; false when it does neither anywhere.
func (s *Service) tokenScope(token string) (aws.Scope, bool) {
	now := s.clock.Now()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		if held := store.View(s.cells, scope, func(st *State) bool {
			_, _, ok := st.holder(token, now)
			return ok || st.restores(token, now)
		}); held {
			return scope, true
		}
	}
	return aws.Scope{}, false
}

// writeQueryResponse writes the awsQuery success document of action with
// result inside its <action>Result element, empty for none.
func writeQueryResponse(w http.ResponseWriter, c *kernel.Call, action, result string) {
	var b strings.Builder
	b.WriteString(`<` + action + `Response xmlns="` + c.Op().Service.XMLNamespace + `">`)
	if result != "" {
		b.WriteString(`<` + action + `Result>` + result + `</` + action + `Result>`)
	}
	b.WriteString(`<ResponseMetadata><RequestId>` + string(c.ID()) + `</RequestId></ResponseMetadata></` + action + `Response>`)
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String())) //nolint:errcheck // the client went away; nothing is left to tell it
}
