package ses_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// harness is a kernel with both SES APIs over one store, and typed
// shortcuts for their operations.
type harness struct {
	*kerneltest.K
	svc *ses.Service
	v2  *ses.V2
}

// run runs fn against both SES APIs with every switch at its default.
func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, ses.Options{}, fn)
}

// runWith runs fn against both SES APIs built with o; extra adjusts the
// kernel.
func runWith(t *testing.T, o ses.Options, fn func(h *harness), extra ...kerneltest.Option) {
	t.Helper()
	opts := append([]kerneltest.Option{kerneltest.Services(sesapi.Service(), sesv2api.Service())}, extra...)
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 1<<20)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		d := k.Deps(sesapi.Service())
		d.Blob = blobs
		svc := ses.New(d, o)
		d2 := k.Deps(sesv2api.Service())
		d2.Blob = blobs
		fn(&harness{K: k, svc: svc, v2: ses.NewV2(d2, svc)})
	}, opts...)
}

// The verified identities of runVerified: an exact address and a domain,
// both configured through ses.verifiedIdentities, so each starts Success
// when created (contract 6).
const (
	sender         = "sender@example.com"
	verifiedDomain = "verified.test"
	recipient      = "recipient@verified.test"
	simulator      = "success@" + ses.SimulatorDomain
)

// runVerified runs fn with sender and verifiedDomain configured as
// verified and created through v1, so a send from sender to an address in
// verifiedDomain passes the identity and sandbox checks.
func runVerified(t *testing.T, o ses.Options, fn func(h *harness), extra ...kerneltest.Option) {
	t.Helper()
	o.VerifiedIdentities.Addresses = append(o.VerifiedIdentities.Addresses, sender)
	o.VerifiedIdentities.Domains = append(o.VerifiedIdentities.Domains, verifiedDomain)
	runWith(t, o, func(h *harness) {
		h.verifyEmail(sender)
		h.verifyDomain(verifiedDomain)
		fn(h)
	}, extra...)
}

// call runs op as a client request in h.Scope.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s %d: %s", op.Descriptor().ID.Name, err.ID.QueryCode, err.ID.Status, err.Message)
	}
	return out
}

func (h *harness) verifyEmail(addr string) {
	h.Helper()
	must(h, sesapi.OpVerifyEmailIdentity, &sesapi.VerifyEmailIdentityInput{EmailAddress: addr})
}

func (h *harness) verifyDomain(domain string) string {
	h.Helper()
	return must(h, sesapi.OpVerifyDomainIdentity, &sesapi.VerifyDomainIdentityInput{Domain: domain}).VerificationToken
}

// attributes is GetIdentityVerificationAttributes of ids.
func (h *harness) attributes(ids ...string) map[string]sesapi.IdentityVerificationAttributes {
	h.Helper()
	return must(h, sesapi.OpGetIdentityVerificationAttributes, &sesapi.GetIdentityVerificationAttributesInput{Identities: ids}).VerificationAttributes
}

// status is the v1 verification status of id, failing when it is absent.
func (h *harness) status(id string) sesapi.VerificationStatus {
	h.Helper()
	a, ok := h.attributes(id)[id]
	if !ok {
		h.Fatalf("GetIdentityVerificationAttributes omits %q", id)
	}
	return a.VerificationStatus
}

func (h *harness) quota() *sesapi.GetSendQuotaOutput {
	h.Helper()
	return must(h, sesapi.OpGetSendQuota, &sesapi.GetSendQuotaInput{})
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// messages is every recorded message of the store, oldest first.
func (h *harness) messages(q ses.MessageQuery) []ses.Message {
	h.Helper()
	q.Limit = ses.MaxMessageLimit
	page, err := h.svc.Messages(q)
	if err != nil {
		h.Fatalf("Messages(%+v) = %v", q, err)
	}
	return page.Messages
}

// sends is the recorded messages of kind send.
func (h *harness) sends() []ses.Message {
	h.Helper()
	var out []ses.Message
	all := h.messages(ses.MessageQuery{})
	for i := range all {
		if all[i].Kind == ses.KindSend {
			out = append(out, all[i])
		}
	}
	return out
}

// apiCalls are the journaled calls of operation op of service svc.
func (h *harness) apiCalls(svc, op string) []journal.APICall {
	var out []journal.APICall
	events := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
	for i := range events {
		if c, ok := events[i].Detail.(journal.APICall); ok && string(c.Op.Service) == svc && c.Op.Name == op {
			out = append(out, c)
		}
	}
	return out
}

// stateChanges are the journaled state changes of arn as "From->To".
func (h *harness) stateChanges(arn string) []string {
	var out []string
	events := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events
	for i := range events {
		if sc, ok := events[i].Detail.(journal.StateChange); ok && sc.Resource.String() == arn {
			out = append(out, string(sc.From)+"->"+string(sc.To))
		}
	}
	return out
}

// westRegion is us-west-2.
func westRegion(tb testing.TB) aws.Region {
	tb.Helper()
	return parseRegion(tb, "us-west-2")
}

func parseRegion(tb testing.TB, name string) aws.Region {
	tb.Helper()
	r, err := aws.KnownRegions().Parse(name)
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// identityARN is the ARN of identity name in h.Scope (ses.md 5.1).
func (h *harness) identityARN(name string) string {
	return "arn:aws:ses:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":identity/" + name
}

// unverifiedText is the MessageRejected text of a failed sender or
// sandbox check, listing ids in the order given: the aws-doc template
// (ses.md 6.1) with the Region upper case (capture, ses.md 6.1). The order
// of ids is switch ses.error.unverified.text; callers order them per
// Options.UnverifiedText.
func unverifiedText(region aws.Region, ids ...string) string {
	return ses.UnverifiedTextPrefix + strings.ToUpper(region.String()) + ": " + strings.Join(ids, ", ")
}

// failer is what the assertion helpers report through: a *testing.T or a
// *rapid.T.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

// wantErr fails unless err has id's code and status, and message.
func wantErr(tb failer, err *awserr.Error, id awserr.Identity, message string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Fatalf("no error, want %s %d %q", id.QueryCode, id.Status, message)
	case err.ID.QueryCode != id.QueryCode || err.ID.Status != id.Status || err.Message != message:
		tb.Fatalf("error = %s %d %q\nwant  %s %d %q", err.ID.QueryCode, err.ID.Status, err.Message, id.QueryCode, id.Status, message)
	}
}

// wantCode fails unless err has id's code and status.
func wantCode(tb failer, err *awserr.Error, id awserr.Identity) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Fatalf("no error, want %s %d", id.QueryCode, id.Status)
	case err.ID.QueryCode != id.QueryCode || err.ID.Status != id.Status:
		tb.Fatalf("error = %s %d %q, want %s %d", err.ID.QueryCode, err.ID.Status, err.Message, id.QueryCode, id.Status)
	}
}

// wantNotImplemented fails unless err is NotImplemented 501 naming name
// (invariant 3; contract 1.6).
func wantNotImplemented(tb failer, err *awserr.Error, name string) {
	tb.Helper()
	wantCode(tb, err, awserr.NotImplemented())
	if !strings.Contains(err.Message, name) {
		tb.Fatalf("NotImplemented %q does not name %s", err.Message, name)
	}
}

// email is a v1 SendEmail from source to the given To addresses.
func email(source string, to ...string) *sesapi.SendEmailInput {
	return &sesapi.SendEmailInput{
		Source:      source,
		Destination: sesapi.Destination{ToAddresses: to},
		Message: sesapi.Message{
			Subject: sesapi.Content{Data: "subject"},
			Body:    sesapi.Body{Text: &sesapi.Content{Data: "text"}},
		},
	}
}
