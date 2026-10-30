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
	"ConfigurationSetName, X-SES-CONFIGURATION-SET, the ARN members and the X-SES-*-ARN headers answer NotImplemented"

// bindSendRaw binds v1 SendRawEmail, whose resource is its sender's
// identity ARN: Source, else the first From header address (contract 2.6).
func (s *Service) bindSendRaw(b sesapi.Binder) {
	b.SendRawEmail(s.sendRawEmail, registry.Partial{Note: sendRawEmailCoverage},
		kernel.WithoutSecondaryActions(),
		kernel.WithResources(func(c *kernel.Call, in *sesapi.SendRawEmailInput) []aws.ARN {
			if in.Source != nil {
				if a, bad := parseAddress(*in.Source); bad == nil {
					return []aws.ARN{identityARN(c.Scope(), a.Address)}
				}
				return nil
			}
			m, bad := readRaw(in.RawMessage.Data)
			if bad != nil {
				return nil
			}
			from, err := m.header.AddressList("From")
			if err != nil || len(from) == 0 {
				return nil
			}
			return []aws.ARN{identityARN(c.Scope(), from[0].Address)}
		}))
}

// sendRawEmail runs v1 SendRawEmail's checks in SendChecks order, model
// validation having run before the handler, keeps the raw bytes in the blob
// store and records the accepted message (contract 2.1). Nothing is sent
// (ses.md 11.8).
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
		api:      APIv1,
		senders:  send.senders,
		envelope: send.envelope,
		refuse:   (*refusal).v1,
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

// rawSend is a raw message that passed the checks needing no state: who
// sends it, its envelope, and what its record holds besides the bytes.
type rawSend struct {
	senders  []string
	envelope []string
	fill     func(r *record)
}

// checkSendRawEmail runs the v1 SendRawEmail checks that need no state
// (SendChecks; contract 2.1): the NotImplemented members, the raw message's
// header block and NotImplemented headers, the tags with X-SES-MESSAGE-TAGS
// after Tags, the addresses, then the size and MIME limits, and the
// recipient count of the envelope ses.raw.envelope picks.
func (s *Service) checkSendRawEmail(in *sesapi.SendRawEmailInput) (*rawSend, *refusal) {
	for _, m := range []struct {
		member, feature string
		set             bool
	}{
		{"ConfigurationSetName", "configuration sets", in.ConfigurationSetName != nil},
		{"SourceArn", "sending authorization", in.SourceArn != nil},
		{"FromArn", "sending authorization", in.FromArn != nil},
		{"ReturnPathArn", "sending authorization", in.ReturnPathArn != nil},
	} {
		if m.set {
			return nil, notImplementedMember("SendRawEmail", m.member, m.feature)
		}
	}
	data := in.RawMessage.Data
	m, r := readRaw(data)
	if r != nil {
		return nil, r
	}
	if r := m.notImplemented("SendRawEmail"); r != nil {
		return nil, r
	}
	tags := make([]Tag, 0, len(in.Tags))
	for _, t := range in.Tags {
		tags = append(tags, Tag{Name: t.Name, Value: t.Value})
	}
	tags = append(tags, m.tags()...)
	for _, t := range tags {
		if r := checkTag(t.Name, t.Value); r != nil {
			return nil, r
		}
	}

	addrs, r := m.addresses()
	if r != nil {
		return nil, r
	}
	senders, from, r := addrs.senders(in.Source)
	if r != nil {
		return nil, r
	}
	for _, d := range in.Destinations {
		if _, r := parseAddress(d); r != nil {
			return nil, r
		}
	}
	if r := checkRawMessage(m, data, MaxV1MessageBytes); r != nil {
		return nil, r
	}
	envelope := rawEnvelope(s.opts.RawEnvelope, in.Destinations, slices.Concat(addrs.to, addrs.cc, addrs.bcc))
	if r := checkRecipientCount(len(envelope)); r != nil {
		return nil, r
	}
	return &rawSend{
		senders:  senders,
		envelope: envelope,
		fill: func(r *record) {
			if in.Source != nil {
				r.Source = *in.Source
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

// notImplemented refuses the first X-SES header standing in for a member
// understudy does not implement that m carries, naming it in upper case
// (ses.md 4.2; spec conflict rows 4 and 6).
func (m *rawMessage) notImplemented(op string) *refusal {
	for _, h := range []struct{ name, feature string }{
		{"X-SES-CONFIGURATION-SET", "configuration sets"},
		{"X-SES-SOURCE-ARN", "sending authorization"},
		{"X-SES-FROM-ARN", "sending authorization"},
		{"X-SES-RETURN-PATH-ARN", "sending authorization"},
	} {
		if _, ok := m.header[textproto.CanonicalMIMEHeaderKey(h.name)]; ok {
			return notImplementedMember(op, h.name, h.feature)
		}
	}
	return nil
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
// Return-Path header. Neither Source nor From answers AddressIllegalText
// (model, SendRawEmailRequest; the answer is inferred, SES-20).
func (a *rawAddresses) senders(source *string) (senders, from []string, r *refusal) {
	switch {
	case source != nil:
		p, r := parseAddress(*source)
		if r != nil {
			return nil, nil, r
		}
		senders, from = []string{*source}, []string{p.Address}
	case len(a.from) == 0:
		return nil, nil, invalid(AddressIllegalText)
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
