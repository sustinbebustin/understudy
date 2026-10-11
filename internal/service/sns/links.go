package sns

import (
	"context"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// linkPrefix is where SNS's AWS-visible links live: the reserved prefix,
// then the service and the raw operation that serves the link.
const linkPrefix = "/_understudy/sns/"

// bindLinks binds the raw operations behind the links a notification
// carries: the signing certificate and the unsubscribe and confirm links.
func (s *Service) bindLinks(b snsapi.Binder) {
	b.SigningCert(s.signingCert, registry.Full{})
	b.UnsubscribeLink(s.unsubscribeLink, registry.Full{})
	b.ConfirmSubscriptionLink(s.confirmLink, registry.Full{})
}

// links builds the URLs a notification carries, on the host the
// publishing client addressed.
type links struct {
	base string
}

func linksOf(e kernel.Endpoint) links { return links{base: e.String() + linkPrefix} }

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
	pem, _ := read(s, scope, func(st *State) []byte {
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
// the subscription named by the SubscriptionArn query member.
func (s *Service) unsubscribeLink(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	arn := r.URL.Query().Get("SubscriptionArn")
	if _, err := aws.ParseARN(arn); err != nil {
		return errInvalidParameter("SubscriptionArn")
	}
	if ref, ok := s.subscriptionOf(arn); ok {
		c.Touch(ref.arn())
		if err := s.unsubscribe(c, ref, unauthenticated); err != nil {
			return err
		}
	}
	writeQueryResponse(w, c, "Unsubscribe", "")
	return nil
}

// confirmLink serves the SubscribeURL of a confirmation message:
// ConfirmSubscription without credentials, from the TopicArn and Token
// query members, so ConfirmationWasAuthenticated reads false.
func (s *Service) confirmLink(_ context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	q := r.URL.Query()
	ref, ok := s.topicOf(q.Get("TopicArn"))
	if !ok {
		return errInvalidParameter("Topic")
	}
	c.Touch(ref.arn())
	arn, err := s.confirm(c, ref, ByLink{Token: q.Get("Token")})
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
