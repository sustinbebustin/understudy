package ses

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// sendRawEmailCoverage is v1 SendRawEmail's coverage note (ses.md 11.1).
const sendRawEmailCoverage = "journal-only as SendEmail; the raw MIME is recorded as received, without the Message-ID and Date headers " +
	"SES would substitute; recipients come from Destinations, else the To, Cc and Bcc headers; X-SES-MESSAGE-TAGS is recorded; " +
	"the ARN members and the X-SES-*-ARN headers answer NotImplemented"

// bindSendRaw binds v1 SendRawEmail, whose resource is its sender's
// identity ARN: Source, else the first From header address.
func (s *Service) bindSendRaw(b sesapi.Binder) {
	b.SendRawEmail(s.sendRawEmail, registry.Partial{Note: sendRawEmailCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		kernel.WithResources(func(c *kernel.Call, in *sesapi.SendRawEmailInput) []aws.ARN {
			return rawSenderARN(c.Scope(), in.Source, in.RawMessage.Data)
		}))
}

// rawSenderARN is a raw send's resource: the identity ARN of source when
// given, else of the raw message's first From address; none when the
// address does not parse.
func rawSenderARN(scope aws.Scope, source *string, data []byte) []aws.ARN {
	if source != nil {
		if a, bad := parseAddress(*source); bad == nil {
			return []aws.ARN{identityARN(scope, a.Address)}
		}
		return nil
	}
	m, bad := readRaw(data)
	if bad != nil {
		return nil
	}
	from, err := m.header.AddressList("From")
	if err != nil || len(from) == 0 {
		return nil
	}
	return []aws.ARN{identityARN(scope, from[0].Address)}
}

// sendRawEmail runs v1 SendRawEmail's checks in SendChecks order, model
// validation having run before the handler, keeps the raw bytes in the blob
// store and records the accepted message. Nothing is sent (ses.md 11.8).
func (s *Service) sendRawEmail(ctx context.Context, c *kernel.Call, in *sesapi.SendRawEmailInput) (*sesapi.SendRawEmailOutput, *awserr.Error) {
	send, r := s.checkSendRawEmail(in)
	if r != nil {
		return nil, r.v1()
	}
	ref, err := s.putBlob(ctx, in.RawMessage.Data)
	if err != nil {
		return nil, err
	}
	id, err := s.accept(ctx, c, outgoing{
		api:              APIv1,
		configurationSet: send.configurationSet,
		senders:          send.senders,
		envelope:         send.envelope,
		refuse:           (*refusal).v1,
		fill: func(r *record) {
			send.fill(r)
			r.Raw = &ref
		},
	})
	if err != nil {
		s.release(ctx, ref)
		return nil, err
	}
	return &sesapi.SendRawEmailOutput{MessageId: id}, nil
}

// checkedSend is a send that passed the checks needing no state: who sends
// it, its envelope, the configuration set it names (nil when none), and
// what its record holds besides blob contents.
type checkedSend struct {
	senders          []string
	envelope         []string
	configurationSet *string
	fill             func(r *record)
}

// checkSendRawEmail runs the v1 SendRawEmail checks that need no state
// (SendChecks): the NotImplemented members, then checkRaw.
func (s *Service) checkSendRawEmail(in *sesapi.SendRawEmailInput) (*checkedSend, *refusal) {
	if r := firstUnimplemented("ses", "SendRawEmail", []unimplemented{
		{"SourceArn", "sending authorization", in.SourceArn != nil},
		{"FromArn", "sending authorization", in.FromArn != nil},
		{"ReturnPathArn", "sending authorization", in.ReturnPathArn != nil},
	}); r != nil {
		return nil, r
	}
	tags := make([]Tag, 0, len(in.Tags))
	for _, t := range in.Tags {
		tags = append(tags, Tag{Name: t.Name, Value: t.Value})
	}
	return s.checkRaw(rawRequest{
		svc:              "ses",
		op:               "SendRawEmail",
		source:           in.Source,
		configurationSet: in.ConfigurationSetName,
		destinations:     in.Destinations,
		tags:             tags,
		data:             in.RawMessage.Data,
		limit:            MaxV1MessageBytes,
	})
}

// rawRequest is a raw send's members other than its API's NotImplemented
// ones: v1 SendRawEmail, or v2 SendEmail with Raw content.
type rawRequest struct {
	// svc and op name the operation in a NotImplemented answer.
	svc, op string
	// source is v1 Source or v2 FromEmailAddress, nil when absent.
	source *string
	// configurationSet is the request's ConfigurationSetName, nil when
	// absent; the X-SES-CONFIGURATION-SET header stands in for it.
	configurationSet *string
	// returnPath is v2 FeedbackForwardingEmailAddress, nil when absent. When
	// given it stands in for the Return-Path header, as a given Source takes
	// precedence over that header for feedback (model, ses.md 4.2; inferred
	// for v2).
	returnPath *string
	// replyTo is v2 ReplyToAddresses; when given they are recorded in place
	// of the Reply-To header's (inferred: the model calls them the
	// message's "Reply-to" addresses, ses.md 4.4).
	replyTo []string
	// destinations is v1 Destinations or the v2 Destination's To, Cc and
	// Bcc in order.
	destinations []string
	// tags is the request's tags; X-SES-MESSAGE-TAGS follow them.
	tags []Tag
	data []byte
	// limit is the API's message size limit.
	limit int
}

// checkRaw runs a raw send's checks that need no state, after its API's
// NotImplemented members (SendChecks): the raw message's
// header block and NotImplemented headers, the tags with X-SES-MESSAGE-TAGS
// after the request's, the addresses, then the size and MIME limits, and
// the recipient count of the envelope ses.raw.envelope picks.
func (s *Service) checkRaw(q rawRequest) (*checkedSend, *refusal) {
	m, r := readRaw(q.data)
	if r != nil {
		return nil, r
	}
	if r := m.notImplemented(q.svc, q.op); r != nil {
		return nil, r
	}
	tags := slices.Concat(q.tags, m.tags())
	for _, t := range tags {
		if r := checkTag(t.Name, t.Value); r != nil {
			return nil, r
		}
	}

	addrs, r := m.addresses()
	if r != nil {
		return nil, r
	}
	if q.returnPath != nil {
		if _, r := parseAddress(*q.returnPath); r != nil {
			return nil, r
		}
		addrs.returnPath = *q.returnPath
	}
	senders, from, r := addrs.senders(q.source)
	if r != nil {
		return nil, r
	}
	for _, d := range slices.Concat(q.destinations, q.replyTo) {
		if _, r := parseAddress(d); r != nil {
			return nil, r
		}
	}
	if len(q.replyTo) > 0 {
		addrs.replyTo = slices.Clone(q.replyTo)
	}
	if r := checkRawMessage(m, q.data, q.limit); r != nil {
		return nil, r
	}
	envelope := rawEnvelope(s.opts.RawEnvelope, q.destinations, slices.Concat(addrs.to, addrs.cc, addrs.bcc))
	if r := checkRecipientCount(len(envelope)); r != nil {
		return nil, r
	}
	return &checkedSend{
		senders:          senders,
		envelope:         envelope,
		configurationSet: m.configurationSet(q.configurationSet),
		fill: func(r *record) {
			if q.source != nil {
				r.Source = *q.source
			}
			r.From = from
			r.ReplyTo = addrs.replyTo
			r.ReturnPath = addrs.returnPath
			r.To, r.Cc, r.Bcc = addrs.to, addrs.cc, addrs.bcc
			r.Tags = tags
		},
	}, nil
}

// rawEnvelope is a raw send's envelope per switch ses.raw.envelope
// (ses.md 4.2, SES-20): the given destinations when there are any, else the
// header recipients; or, under union, both, each mailbox once.
func rawEnvelope(sw RawEnvelope, destinations, headers []string) []string {
	switch sw {
	case RawEnvelopeDestinations:
		if len(destinations) > 0 {
			return slices.Clone(destinations)
		}
		return slices.Clone(headers)
	case RawEnvelopeUnion:
	}
	out := slices.Clone(destinations)
	for _, h := range headers {
		if !slices.ContainsFunc(out, func(o string) bool { return sameMailbox(addrSpec(o), addrSpec(h)) }) {
			out = append(out, h)
		}
	}
	return out
}

// rawMessage is a raw message's header block and body.
type rawMessage struct {
	header mail.Header
	body   io.Reader
}

// readRaw parses data's header block with net/mail (ses.md 11.6 item 5).
func readRaw(data []byte) (*rawMessage, *refusal) {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, invalid(RawUnparseableText)
	}
	return &rawMessage{header: m.Header, body: m.Body}, nil
}

// configurationSet is the configuration set a raw send names: member, its
// ConfigurationSetName, when given, else the X-SES-CONFIGURATION-SET
// header's value, trimmed (ses.md 4.2, 8); nil when neither is. Which
// wins when both are given is unrecorded; the member does (inferred).
func (m *rawMessage) configurationSet(member *string) *string {
	if member != nil {
		return member
	}
	v, ok := m.header[textproto.CanonicalMIMEHeaderKey("X-SES-CONFIGURATION-SET")]
	if !ok || len(v) == 0 {
		return nil
	}
	return new(strings.TrimSpace(v[0]))
}

// notImplemented refuses the first X-SES header standing in for a member
// understudy does not implement that m carries, naming it in upper case
// (ses.md 4.2).
func (m *rawMessage) notImplemented(svc, op string) *refusal {
	has := func(name string) bool {
		_, ok := m.header[textproto.CanonicalMIMEHeaderKey(name)]
		return ok
	}
	return firstUnimplemented(svc, op, []unimplemented{
		{"X-SES-SOURCE-ARN", "sending authorization", has("X-SES-SOURCE-ARN")},
		{"X-SES-FROM-ARN", "sending authorization", has("X-SES-FROM-ARN")},
		{"X-SES-RETURN-PATH-ARN", "sending authorization", has("X-SES-RETURN-PATH-ARN")},
	})
}

// tags reads every X-SES-MESSAGE-TAGS header, "name1=value1, name2=value2"
// (ses.md 4.2), in order; checkTag validates them.
func (m *rawMessage) tags() []Tag {
	var out []Tag
	for _, v := range m.header[textproto.CanonicalMIMEHeaderKey("X-SES-MESSAGE-TAGS")] {
		for piece := range strings.SplitSeq(v, ",") {
			name, value, _ := strings.Cut(piece, "=")
			out = append(out, Tag{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
		}
	}
	return out
}

// rawAddresses are a raw message's header addresses as written: the
// display name and addr-spec when a name is given, else the addr-spec.
// returnPath is the Return-Path header's address, "" when absent or null.
type rawAddresses struct {
	from, sender, replyTo, to, cc, bcc []string
	returnPath                         string
}

// addresses reads and validates the address headers of m as a send's
// addresses are validated (ses.md 4.6); a header that does not parse as an
// address list answers AddressIllegalText.
func (m *rawMessage) addresses() (rawAddresses, *refusal) {
	var out rawAddresses
	for _, h := range []struct {
		name string
		dst  *[]string
	}{
		{"From", &out.from},
		{"Sender", &out.sender},
		{"Reply-To", &out.replyTo},
		{"To", &out.to},
		{"Cc", &out.cc},
		{"Bcc", &out.bcc},
	} {
		list, err := m.header.AddressList(h.name)
		switch {
		case errors.Is(err, mail.ErrHeaderNotPresent):
			continue
		case err != nil:
			return out, invalid(AddressIllegalText)
		}
		for _, a := range list {
			if _, r := parseAddress(a.Address); r != nil {
				return out, r
			}
			*h.dst = append(*h.dst, asWritten(a))
		}
	}
	if len(out.sender) > 1 {
		return out, invalid(AddressIllegalText)
	}
	if rp := strings.TrimSpace(m.header.Get("Return-Path")); rp != "" && rp != "<>" {
		a, err := mail.ParseAddress(rp)
		if err != nil {
			return out, invalid(AddressIllegalText)
		}
		if _, r := parseAddress(a.Address); r != nil {
			return out, r
		}
		out.returnPath = rp
	}
	return out, nil
}

// senders is the addresses a raw send must have verified, in the order the
// unverified text lists them, and the record's From addr-specs: Source when
// given, else the From and Sender headers (ses.md 4.2, 6.1), then the
// Return-Path header. Neither Source nor From answers RawMissingFromText
// (model, SendRawEmailRequest; recorded, SES-20).
func (a *rawAddresses) senders(source *string) (senders, from []string, r *refusal) {
	switch {
	case source != nil:
		p, r := parseAddress(*source)
		if r != nil {
			return nil, nil, r
		}
		senders, from = []string{*source}, []string{p.Address}
	case len(a.from) == 0:
		return nil, nil, invalid(RawMissingFromText)
	default:
		senders = slices.Concat(a.from, a.sender)
		for _, f := range a.from {
			from = append(from, addrSpec(f))
		}
	}
	if a.returnPath != "" {
		senders = append(senders, a.returnPath)
	}
	return senders, from, nil
}

// asWritten is a parsed header address in the form the record keeps.
func asWritten(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.String()
}

// checkRawMessage runs the message checks of a raw send after its
// addresses: its size, counted after base64 (ses.md 2.2), against limit,
// then the line length and MIME part limits (ses.md 4.2). The answers are
// inferred (SES-29).
func checkRawMessage(m *rawMessage, data []byte, limit int) *refusal {
	if r := checkSize(base64.StdEncoding.EncodedLen(len(data)), limit); r != nil {
		return r
	}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSuffix(line, []byte("\r"))) > MaxRawLineLength {
			return &refusal{class: refusalRejected, text: fmt.Sprintf(LineTooLongText, MaxRawLineLength)}
		}
	}
	parts := 0
	return countParts(textproto.MIMEHeader(m.header), m.body, &parts)
}

// countParts adds the MIME parts under a part with header h and body
// to *n, nested multiparts included, and refuses once *n passes
// MaxMIMEParts. The message itself is not counted. A part whose
// Content-Type is not a multipart with a boundary has no parts; a multipart
// body that does not parse answers RawUnparseableText.
func countParts(h textproto.MIMEHeader, body io.Reader, n *int) *refusal {
	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		return nil
	}
	mr := multipart.NewReader(body, params["boundary"])
	for {
		p, err := mr.NextRawPart()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return invalid(RawUnparseableText)
		}
		*n++
		if *n > MaxMIMEParts {
			return &refusal{class: refusalRejected, text: fmt.Sprintf(TooManyPartsText, MaxMIMEParts)}
		}
		if r := countParts(p.Header, p, n); r != nil {
			return r
		}
		// Reading the rest of the part is what finds a missing closing
		// boundary: NextRawPart reports one as a wrapped io.EOF.
		if _, err := io.Copy(io.Discard, p); err != nil {
			return invalid(RawUnparseableText)
		}
	}
}
