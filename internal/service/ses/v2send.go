package ses

import (
	"context"
	"encoding/base64"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// v2SendEmailCoverage is v2 SendEmail's coverage note (ses.md 11.1).
const v2SendEmailCoverage = "journal-only as the v1 sends, for Simple (with Headers and Attachments), Raw and Template content; " +
	"Template content, stored by TemplateName or inline, renders and records as SendTemplatedEmail does; " +
	"FeedbackForwardingEmailAddress must be verified, as ReturnPath (inferred); ConfigurationSetName, " +
	"ConfigurationOverrides, EndpointId, TenantName, ListManagementOptions, Template.TemplateArn and the identity ARN members " +
	"answer NotImplemented; the SDKs sign a send with EndpointId SigV4A, which understudy refuses, so it answers sesv2's " +
	"unknown operation at the edge and only a SigV4-signed one reaches the NotImplemented answer"

// bindV2Send binds v2 SendEmail, whose resource is its sender's identity
// ARN: FromEmailAddress, else, for Raw content, the first From header
// address (contract 2.6).
func (v *V2) bindV2Send(b sesv2api.Binder) {
	b.SendEmail(v.sendEmail, registry.Partial{Note: v2SendEmailCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		kernel.WithResources(func(c *kernel.Call, in *sesv2api.SendEmailInput) []aws.ARN {
			if in.Content.Raw != nil {
				return rawSenderARN(c.Scope(), in.FromEmailAddress, in.Content.Raw.Data)
			}
			if in.FromEmailAddress == nil {
				return nil
			}
			a, bad := parseAddress(*in.FromEmailAddress)
			if bad != nil {
				return nil
			}
			return []aws.ARN{identityARN(c.Scope(), a.Address)}
		}))
}

// sendEmail runs v2 SendEmail's checks in SendChecks order, model
// validation having run before the handler, keeps attachment contents or
// the raw bytes in the blob store and records the accepted message
// (contract 2.2). Nothing is sent (ses.md 11.8).
func (v *V2) sendEmail(ctx context.Context, c *kernel.Call, in *sesv2api.SendEmailInput) (*sesv2api.SendEmailOutput, *awserr.Error) {
	s := v.svc
	if r := checkV2Send(in); r != nil {
		return nil, r.v2()
	}
	var (
		send *checkedSend
		r    *refusal
	)
	switch {
	case in.Content.Raw != nil:
		send, r = s.checkRaw(rawRequest{
			svc:          "sesv2",
			op:           "SendEmail",
			source:       in.FromEmailAddress,
			returnPath:   in.FeedbackForwardingEmailAddress,
			replyTo:      in.ReplyToAddresses,
			destinations: destinations(in.Destination),
			tags:         v2Tags(in.EmailTags),
			data:         in.Content.Raw.Data,
			limit:        MaxV2MessageBytes,
		})
	case in.Content.Template != nil:
		send, r = checkTemplateContent(in)
	default:
		send, r = checkSimple(in)
	}
	if r != nil {
		return nil, r.v2()
	}

	var stored []blob.Ref
	var raw *blob.Ref
	var attachments []attachmentRecord
	switch {
	case in.Content.Raw != nil:
		ref, err := s.putBlob(ctx, in.Content.Raw.Data)
		if err != nil {
			return nil, err
		}
		stored, raw = append(stored, ref), &ref
	default:
		for _, a := range attachmentsOf(&in.Content) {
			ref, err := s.putBlob(ctx, a.RawContent)
			if err != nil {
				s.release(ctx, stored...)
				return nil, err
			}
			stored = append(stored, ref)
			attachments = append(attachments, attachmentOf(&a, ref))
		}
	}
	o := outgoing{
		api:      APIv2,
		senders:  send.senders,
		envelope: send.envelope,
		refuse:   (*refusal).v2,
	}
	var used *TemplateSend
	t := in.Content.Template
	if t != nil {
		o.prepare = func(st *state) *refusal {
			var r *refusal
			used, r = s.templateContent(st, t)
			return r
		}
	}
	o.fill = func(r *record) {
		send.fill(r)
		r.Raw = raw
		r.Attachments = attachments
		if used != nil {
			r.Subject, r.Text, r.HTML = used.render(t.TemplateData == nil)
			r.Template = used
		}
	}
	id, err := s.accept(ctx, c, o)
	if err != nil {
		s.release(ctx, stored...)
		return nil, err
	}
	return &sesv2api.SendEmailOutput{MessageId: &id}, nil
}

// checkV2Send runs the v2 SendEmail checks every content kind shares,
// before the content's own: the Content shape, the sender of Simple and
// Template content and the Template's source, which the overlay's
// validation identity answers (SES-23, inferred), then the NotImplemented
// members (contract 2.2; spec conflict rows 4, 6, 9).
func checkV2Send(in *sesv2api.SendEmailInput) *refusal {
	t := in.Content.Template
	switch {
	case count(in.Content.Simple != nil, in.Content.Raw != nil, t != nil) != 1:
		return invalid(ContentShapeText)
	case in.Content.Raw == nil && in.FromEmailAddress == nil:
		return invalid(MissingFromText)
	case t != nil && count(t.TemplateName != nil, t.TemplateArn != nil, t.TemplateContent != nil) != 1:
		return invalid(TemplateSourceText)
	}
	return firstUnimplemented("sesv2", "SendEmail", []unimplemented{
		{"Content.Template.TemplateArn", "template ARNs", t != nil && t.TemplateArn != nil},
		{"ConfigurationSetName", "configuration sets", in.ConfigurationSetName != nil},
		{"ConfigurationOverrides", "configuration sets", in.ConfigurationOverrides != nil},
		{"EndpointId", "multi-region endpoints", in.EndpointId != nil},
		{"TenantName", "tenants", in.TenantName != nil},
		{"ListManagementOptions", "list management", in.ListManagementOptions != nil},
		{"FromEmailAddressIdentityArn", "sending authorization", in.FromEmailAddressIdentityArn != nil},
		{"FeedbackForwardingEmailAddressIdentityArn", "sending authorization", in.FeedbackForwardingEmailAddressIdentityArn != nil},
	})
}

// count is how many of set are true.
func count(set ...bool) int {
	n := 0
	for _, b := range set {
		if b {
			n++
		}
	}
	return n
}

// attachmentsOf is the Attachments of Simple or Template content.
func attachmentsOf(c *sesv2api.EmailContent) []sesv2api.Attachment {
	switch {
	case c.Simple != nil:
		return c.Simple.Attachments
	case c.Template != nil:
		return c.Template.Attachments
	}
	return nil
}

// checkSimple runs the checks of v2 Simple content that need no state
// (checkAddressed) and records its subject, bodies and Headers.
func checkSimple(in *sesv2api.SendEmailInput) (*checkedSend, *refusal) {
	m := in.Content.Simple
	send, r := checkAddressed(in, v2SimpleSize(m))
	if r != nil {
		return nil, r
	}
	fill := send.fill
	send.fill = func(r *record) {
		fill(r)
		r.Subject = v2Content(&m.Subject)
		r.Text = v2Content(m.Body.Text)
		r.HTML = v2Content(m.Body.Html)
		r.Headers = v2Headers(m.Headers)
	}
	return send, nil
}

// checkTemplateContent runs the checks of v2 Template content that need no
// state (checkAddressed) and records its Headers; accept's prepare finds a
// stored template (templateContent), and the fill renders it.
func checkTemplateContent(in *sesv2api.SendEmailInput) (*checkedSend, *refusal) {
	t := in.Content.Template
	send, r := checkAddressed(in, v2TemplateSize(t))
	if r != nil {
		return nil, r
	}
	fill := send.fill
	send.fill = func(r *record) {
		fill(r)
		r.Headers = v2Headers(t.Headers)
	}
	return send, nil
}

// templateContent is the template Template content t uses: the stored
// template in v2's view named TemplateName, or the inline TemplateContent,
// with its TemplateData ("" when absent).
func (s *Service) templateContent(st *state, t *sesv2api.Template) (*TemplateSend, *refusal) {
	var data string
	if t.TemplateData != nil {
		data = *t.TemplateData
	}
	if t.TemplateName != nil {
		return st.storedTemplate(s.view(APIv2), *t.TemplateName, data)
	}
	c := t.TemplateContent
	return &TemplateSend{Subject: clonePart(c.Subject), Text: clonePart(c.Text), HTML: clonePart(c.Html), Data: data}, nil
}

// v2Headers is v2 message headers as recorded, nil when there are none.
func v2Headers(in []sesv2api.MessageHeader) []Header {
	if len(in) == 0 {
		return nil
	}
	out := make([]Header, 0, len(in))
	for _, h := range in {
		out = append(out, Header{Name: h.Name, Value: h.Value})
	}
	return out
}

// checkAddressed runs the checks Simple and Template content share, as v1
// SendEmail's (contract 2.2): tags, addresses (FeedbackForwardingEmail-
// Address checked as ReturnPath, spec conflict row 9), the message size
// against MaxV2MessageBytes, and the recipient count. Headers' names and
// values were checked against their model patterns before the handler
// (ses.md 4.4). The fill records the addressing and tags.
func checkAddressed(in *sesv2api.SendEmailInput, size int) (*checkedSend, *refusal) {
	tags := v2Tags(in.EmailTags)
	for _, t := range tags {
		if r := checkTag(t.Name, t.Value); r != nil {
			return nil, r
		}
	}
	from := *in.FromEmailAddress
	var d sesv2api.Destination
	if in.Destination != nil {
		d = *in.Destination
	}
	senders := []string{from}
	if in.FeedbackForwardingEmailAddress != nil {
		senders = append(senders, *in.FeedbackForwardingEmailAddress)
	}
	for _, a := range slices.Concat(senders, d.ToAddresses, d.CcAddresses, d.BccAddresses, in.ReplyToAddresses) {
		if _, r := parseAddress(a); r != nil {
			return nil, r
		}
	}
	if r := checkSize(size, MaxV2MessageBytes); r != nil {
		return nil, r
	}
	envelope := destinations(in.Destination)
	if r := checkRecipientCount(len(envelope)); r != nil {
		return nil, r
	}
	return &checkedSend{
		senders:  senders,
		envelope: envelope,
		fill: func(r *record) {
			r.Source = from
			r.From = []string{addrSpec(from)}
			r.ReplyTo = slices.Clone(in.ReplyToAddresses)
			if in.FeedbackForwardingEmailAddress != nil {
				r.ReturnPath = *in.FeedbackForwardingEmailAddress
			}
			r.To, r.Cc, r.Bcc = slices.Clone(d.ToAddresses), slices.Clone(d.CcAddresses), slices.Clone(d.BccAddresses)
			r.Tags = tags
		},
	}, nil
}

// v2SimpleSize is the bytes of Simple content (contract 2.2): subject,
// bodies, header names and values, and attachments as base64. As for v1
// (simpleSize), the bound is approximate, since understudy never assembles
// the MIME AWS measures (SES-29).
func v2SimpleSize(m *sesv2api.Message) int {
	n := len(m.Subject.Data)
	for _, b := range []*sesv2api.Content{m.Body.Text, m.Body.Html} {
		if b != nil {
			n += len(b.Data)
		}
	}
	return n + headersSize(m.Headers, m.Attachments)
}

// v2TemplateSize is the bytes of Template content as v2SimpleSize counts
// them: inline parts, headers and attachments. A stored template's parts
// are held to MaxTemplateBytes when stored, and rendering is not measured.
func v2TemplateSize(t *sesv2api.Template) int {
	n := 0
	if c := t.TemplateContent; c != nil {
		for _, p := range []*string{c.Subject, c.Text, c.Html} {
			if p != nil {
				n += len(*p)
			}
		}
	}
	return n + headersSize(t.Headers, t.Attachments)
}

// headersSize is the bytes of header names and values and of attachments
// as base64.
func headersSize(hs []sesv2api.MessageHeader, as []sesv2api.Attachment) int {
	n := 0
	for _, h := range hs {
		n += len(h.Name) + len(h.Value)
	}
	for _, a := range as {
		n += base64.StdEncoding.EncodedLen(len(a.RawContent))
	}
	return n
}

// destinations is a v2 Destination's To, Cc and Bcc in order, nil when it
// is absent.
func destinations(d *sesv2api.Destination) []string {
	if d == nil {
		return nil
	}
	return slices.Concat(d.ToAddresses, d.CcAddresses, d.BccAddresses)
}

// v2Tags is a v2 send's EmailTags as recorded.
func v2Tags(in []sesv2api.MessageTag) []Tag {
	out := make([]Tag, 0, len(in))
	for _, t := range in {
		out = append(out, Tag{Name: t.Name, Value: t.Value})
	}
	return out
}

// v2Content is a v2 subject or body as recorded, nil when absent.
func v2Content(c *sesv2api.Content) *Content {
	if c == nil {
		return nil
	}
	out := &Content{Data: c.Data}
	if c.Charset != nil {
		out.Charset = *c.Charset
	}
	return out
}

// attachmentOf is a's record, its content in blob ref.
func attachmentOf(a *sesv2api.Attachment, ref blob.Ref) attachmentRecord {
	out := attachmentRecord{FileName: a.FileName, Content: ref}
	if a.ContentType != nil {
		out.ContentType = *a.ContentType
	}
	if a.ContentDisposition != nil {
		out.ContentDisposition = string(*a.ContentDisposition)
	}
	if a.ContentDescription != nil {
		out.ContentDescription = *a.ContentDescription
	}
	if a.ContentId != nil {
		out.ContentID = *a.ContentId
	}
	if a.ContentTransferEncoding != nil {
		out.ContentTransferEncoding = string(*a.ContentTransferEncoding)
	}
	return out
}
