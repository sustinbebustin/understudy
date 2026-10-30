package ses

import (
	"context"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"unicode"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// refusalClass is how a failed send check answers, whichever API it came
// through (contract 1.6).
type refusalClass uint8

const (
	// refusalInvalid is an invalid input: v1 InvalidParameterValue, v2
	// BadRequestException.
	refusalInvalid refusalClass = iota
	// refusalRejected is a message SES refuses to send: MessageRejected.
	refusalRejected
	// refusalNotImplemented is a member understudy does not implement:
	// NotImplemented, 501.
	refusalNotImplemented
	// refusalThrottled is a send past the daily quota: v1 Throttling (the
	// overlay's throttle error, ses.md 11.4; SES-22).
	refusalThrottled
	// refusalTemplateMissing is a templated send naming a template the
	// scope does not hold: v1 TemplateDoesNotExist (model), v2
	// NotFoundException (inferred, SES-18).
	refusalTemplateMissing
)

// refusal is the answer of a failed send check: its class and the whole
// message, and for refusalTemplateMissing the template's name.
type refusal struct {
	class    refusalClass
	text     string
	template string
}

// templateMissing refuses a send of template name, which the scope does
// not hold (the text is inferred, SES-18).
func templateMissing(name string) *refusal {
	return &refusal{class: refusalTemplateMissing, text: fmt.Sprintf(TemplateMissingText, name), template: name}
}

// v1 is r as a v1 error.
func (r *refusal) v1() *awserr.Error {
	switch r.class {
	case refusalRejected:
		return sesapi.ErrMessageRejected(r.text)
	case refusalNotImplemented:
		return sesapi.ErrNotImplemented(r.text)
	case refusalThrottled:
		return sesapi.ErrThrottling(r.text)
	case refusalTemplateMissing:
		return errTemplateMissing(r.template)
	case refusalInvalid:
	}
	return sesapi.ErrInvalidParameterValue(r.text)
}

// v2 is r as a v2 error (contract 1.6): the daily quota is
// TooManyRequestsException (the overlay's v2 throttle identity, inferred).
func (r *refusal) v2() *awserr.Error {
	switch r.class {
	case refusalRejected:
		return sesv2api.ErrMessageRejected(r.text)
	case refusalNotImplemented:
		return sesv2api.ErrNotImplemented(r.text)
	case refusalThrottled:
		return sesv2api.ErrTooManyRequestsException(r.text)
	case refusalTemplateMissing:
		return sesv2api.ErrNotFoundException(r.text)
	case refusalInvalid:
	}
	return sesv2api.ErrBadRequestException(r.text)
}

func invalid(text string) *refusal { return &refusal{class: refusalInvalid, text: text} }

// notImplementedMember refuses member of operation op of service svc
// ("ses" or "sesv2"), which configures feature understudy does not
// implement (invariant 3; spec conflict rows 4, 6).
func notImplementedMember(svc, op, member, feature string) *refusal {
	return &refusal{class: refusalNotImplemented, text: op + " with " + member + " uses " + feature +
		", which understudy does not implement; nothing was sent or recorded. Leave " + member +
		" out. Run 'understudy coverage --service " + svc + "' for what is supported."}
}

// unimplemented is a request member understudy refuses when it is set, and
// the feature it configures.
type unimplemented struct {
	member, feature string
	set             bool
}

// firstUnimplemented refuses the first set member of ms, in order.
func firstUnimplemented(svc, op string, ms []unimplemented) *refusal {
	for _, m := range ms {
		if m.set {
			return notImplementedMember(svc, op, m.member, m.feature)
		}
	}
	return nil
}

// sendEmailCoverage is v1 SendEmail's coverage note (ses.md 11.1).
const sendEmailCoverage = "journal-only: validates and applies identity and sandbox rules, returns a MessageId and records the message " +
	"for GET /_understudy/v1/ses/messages; no mail is sent, and no Send, Delivery, Bounce or Complaint event or notification is produced; " +
	"ConfigurationSetName and the sending-authorization ARNs answer NotImplemented; " +
	"chosen without a recording (docs/research/ses.md 11.4): address, recipient-count, size and daily-quota answers, " +
	"tag boundaries and ses: names, check order, mailbox-simulator and label matching"

// bindSends binds the v1 send operations. A send's resource is its
// sender's identity ARN (contract 2.6).
func (s *Service) bindSends(b sesapi.Binder) {
	b.SendEmail(s.sendEmail, registry.Partial{Note: sendEmailCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		kernel.WithResources(func(c *kernel.Call, in *sesapi.SendEmailInput) []aws.ARN {
			a, bad := parseAddress(in.Source)
			if bad != nil {
				return nil
			}
			return []aws.ARN{identityARN(c.Scope(), a.Address)}
		}))
}

// sendEmail runs v1 SendEmail's checks in SendChecks order, model
// validation having run before the handler, then records the accepted
// message (contract 2.1). Nothing is sent (ses.md 11.8).
func (s *Service) sendEmail(ctx context.Context, c *kernel.Call, in *sesapi.SendEmailInput) (*sesapi.SendEmailOutput, *awserr.Error) {
	if r := checkSendEmail(in); r != nil {
		return nil, r.v1()
	}
	d := in.Destination
	senders := []string{in.Source}
	if in.ReturnPath != nil {
		senders = append(senders, *in.ReturnPath)
	}
	id, err := s.accept(ctx, c, outgoing{
		api:      APIv1,
		senders:  senders,
		envelope: slices.Concat(d.ToAddresses, d.CcAddresses, d.BccAddresses),
		refuse:   (*refusal).v1,
		fill: func(r *record) {
			r.Source = in.Source
			r.From = []string{addrSpec(in.Source)}
			r.ReplyTo = slices.Clone(in.ReplyToAddresses)
			if in.ReturnPath != nil {
				r.ReturnPath = *in.ReturnPath
			}
			r.To, r.Cc, r.Bcc = slices.Clone(d.ToAddresses), slices.Clone(d.CcAddresses), slices.Clone(d.BccAddresses)
			r.Subject = content(&in.Message.Subject)
			r.Text = content(in.Message.Body.Text)
			r.HTML = content(in.Message.Body.Html)
			for _, t := range in.Tags {
				r.Tags = append(r.Tags, Tag{Name: t.Name, Value: t.Value})
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.SendEmailOutput{MessageId: id}, nil
}

// content is a v1 subject or body as recorded, nil when absent.
func content(c *sesapi.Content) *Content {
	if c == nil {
		return nil
	}
	out := &Content{Data: c.Data}
	if c.Charset != nil {
		out.Charset = *c.Charset
	}
	return out
}

// checkSendEmail runs the v1 SendEmail checks that need no state: the
// NotImplemented members, tags, addresses with the message size, and the
// recipient count (SendChecks; contract 2.1).
func checkSendEmail(in *sesapi.SendEmailInput) *refusal {
	if r := firstUnimplemented("ses", "SendEmail", []unimplemented{
		{"ConfigurationSetName", "configuration sets", in.ConfigurationSetName != nil},
		{"SourceArn", "sending authorization", in.SourceArn != nil},
		{"ReturnPathArn", "sending authorization", in.ReturnPathArn != nil},
	}); r != nil {
		return r
	}
	for _, t := range in.Tags {
		if r := checkTag(t.Name, t.Value); r != nil {
			return r
		}
	}
	d := in.Destination
	addrs := []string{in.Source}
	addrs = append(addrs, d.ToAddresses...)
	addrs = append(addrs, d.CcAddresses...)
	addrs = append(addrs, d.BccAddresses...)
	addrs = append(addrs, in.ReplyToAddresses...)
	if in.ReturnPath != nil {
		addrs = append(addrs, *in.ReturnPath)
	}
	for _, a := range addrs {
		if _, r := parseAddress(a); r != nil {
			return r
		}
	}
	if r := checkSize(simpleSize(&in.Message), MaxV1MessageBytes); r != nil {
		return r
	}
	return checkRecipientCount(len(d.ToAddresses) + len(d.CcAddresses) + len(d.BccAddresses))
}

// simpleSize is the bytes of a Simple message's subject and bodies
// (contract 2.1): AWS counts the assembled MIME, which understudy never
// builds (ses.md 11.8), so the bound is approximate (SES-29).
func simpleSize(m *sesapi.Message) int {
	n := len(m.Subject.Data)
	if m.Body.Text != nil {
		n += len(m.Body.Text.Data)
	}
	if m.Body.Html != nil {
		n += len(m.Body.Html.Data)
	}
	return n
}

// checkSize refuses a message of more than limit bytes (ses.md 4.1, 4.4;
// the answer is inferred, SES-29).
func checkSize(size, limit int) *refusal {
	if size > limit {
		return &refusal{class: refusalRejected, text: fmt.Sprintf(MessageTooLongText, limit)}
	}
	return nil
}

// checkRecipientCount requires 1 to MaxRecipients envelope recipients
// (ses.md 4.1; the answers are inferred, SES-05, SES-06).
func checkRecipientCount(n int) *refusal {
	switch {
	case n == 0:
		return invalid(NoRecipientsText)
	case n > MaxRecipients:
		return invalid(fmt.Sprintf(TooManyRecipientsText, MaxRecipients))
	}
	return nil
}

// The recorded tag texts (ses.md 4.5); the doubled space after "'_'," is
// in every recording.
const (
	tagNameMissingText  = "The tag name must be specified."
	tagValueMissingText = "The tag value must be specified."
	tagNameCharsetText  = "Invalid tag name <%s>: only alphanumeric ASCII characters, '_',  '-' are allowed."
	tagValueCharsetText = "Invalid tag value <%s>: only alphanumeric ASCII characters, '_',  '-' , '.', '@' are allowed."
	tagNameLengthText   = "Tag name cannot exceed %d characters."
	tagValueLengthText  = "Tag value cannot exceed %d characters."
)

// checkTag validates one message tag, name before value (ses.md 4.5): each
// must be given, use its recorded character set (a value also admits "."
// and "@"), and have at most TagMaxLength characters. FeedbackIDTagNames
// pass the name's character set. Charset before length is UNVERIFIED
// (SES-16): the recordings never combine the two in one part.
func checkTag(name, value string) *refusal {
	switch {
	case name == "":
		return invalid(tagNameMissingText)
	case !slices.Contains(FeedbackIDTagNames(), name) && !onlyTagChars(name, ""):
		return invalid(fmt.Sprintf(tagNameCharsetText, name))
	case len(name) > TagMaxLength:
		return invalid(fmt.Sprintf(tagNameLengthText, TagMaxLength))
	case value == "":
		return invalid(tagValueMissingText)
	case !onlyTagChars(value, ".@"):
		return invalid(fmt.Sprintf(tagValueCharsetText, value))
	case len(value) > TagMaxLength:
		return invalid(fmt.Sprintf(tagValueLengthText, TagMaxLength))
	}
	return nil
}

// onlyTagChars reports whether s holds only ASCII letters, digits, "_",
// "-" and the characters of extra.
func onlyTagChars(s, extra string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		case strings.ContainsRune(extra, r):
		default:
			return false
		}
	}
	return true
}

// parseAddress validates an address as SES does, as far as is known
// (ses.md 4.6, 11.6 item 5; texts inferred, SES-04), and parses it. The
// texts answer in this order: no "@" in the addr-spec; whitespace or a
// control character in its local part; a domain ending in "."; anything
// else net/mail refuses, or a non-ASCII character, since addresses must be
// 7-bit ASCII with Unicode display names MIME-encoded (model, ses.md 4.1).
func parseAddress(s string) (*mail.Address, *refusal) {
	spec := s
	if i := strings.LastIndexByte(s, '<'); i >= 0 && strings.HasSuffix(s, ">") {
		spec = s[i+1 : len(s)-1]
	}
	at := strings.LastIndexByte(spec, '@')
	switch {
	case at < 0:
		return nil, invalid(AddressMissingDomainText)
	case strings.ContainsFunc(spec[:at], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return nil, invalid(AddressWhitespaceText)
	case strings.HasSuffix(spec, "."):
		return nil, invalid(AddressTrailingDotText)
	case strings.ContainsFunc(s, func(r rune) bool { return r > unicode.MaxASCII }):
		return nil, invalid(AddressIllegalText)
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, invalid(AddressIllegalText)
	}
	return a, nil
}
