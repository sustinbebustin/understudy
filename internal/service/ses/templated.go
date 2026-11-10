package ses

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// sendTemplatedEmailCoverage is v1 SendTemplatedEmail's coverage note
// (ses.md 11.1).
const sendTemplatedEmailCoverage = "journal-only as SendEmail; the stored template and TemplateData are recorded, " +
	"and rendered subject and bodies only for {{name}} and dotted-path tags; other Handlebars constructs record a render gap, " +
	"and the send is still accepted, as SES accepts before rendering; so do a value TemplateData lacks, a string holding " +
	"a character Handlebars escapes and any value but a string, boolean or integer, whose rendering on SES is unrecorded; " +
	"TemplateArn answers NotImplemented"

// bindSendTemplated binds v1 SendTemplatedEmail, whose resource is its
// sender's identity ARN.
func (s *Service) bindSendTemplated(b sesapi.Binder) {
	b.SendTemplatedEmail(s.sendTemplatedEmail, registry.Partial{Note: sendTemplatedEmailCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		kernel.WithResources(func(c *kernel.Call, in *sesapi.SendTemplatedEmailInput) []aws.ARN {
			a, bad := parseAddress(in.Source)
			if bad != nil {
				return nil
			}
			return []aws.ARN{identityARN(c.Scope(), a.Address)}
		}))
}

// sendTemplatedEmail runs SendEmail's checks, then finds the stored
// template, renders what it can of it and records the accepted message
// (ses.md 4.3, 11.6 item 6). Rendering never refuses a send: SES accepts
// before it renders (ses.md 4.3). Nothing is sent (ses.md 11.8).
func (s *Service) sendTemplatedEmail(ctx context.Context, c *kernel.Call, in *sesapi.SendTemplatedEmailInput) (*sesapi.SendTemplatedEmailOutput, *awserr.Error) {
	if r := checkSendTemplated(in); r != nil {
		return nil, r.v1()
	}
	d := in.Destination
	senders := []string{in.Source}
	if in.ReturnPath != nil {
		senders = append(senders, *in.ReturnPath)
	}
	var used *TemplateSend
	id, err := s.accept(ctx, c, outgoing{
		api:      APIv1,
		senders:  senders,
		envelope: slices.Concat(d.ToAddresses, d.CcAddresses, d.BccAddresses),
		refuse:   (*refusal).v1,
		prepare: func(st *state) *refusal {
			var r *refusal
			used, r = st.storedTemplate(s.view(APIv1), in.Template, in.TemplateData)
			return r
		},
		fill: func(r *record) {
			r.Source = in.Source
			r.From = []string{addrSpec(in.Source)}
			r.ReplyTo = slices.Clone(in.ReplyToAddresses)
			if in.ReturnPath != nil {
				r.ReturnPath = *in.ReturnPath
			}
			r.To, r.Cc, r.Bcc = slices.Clone(d.ToAddresses), slices.Clone(d.CcAddresses), slices.Clone(d.BccAddresses)
			r.Subject, r.Text, r.HTML = used.render(false)
			r.Template = used
			for _, t := range in.Tags {
				r.Tags = append(r.Tags, Tag{Name: t.Name, Value: t.Value})
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.SendTemplatedEmailOutput{MessageId: id}, nil
}

// checkSendTemplated runs SendEmail's checks that need no state on a
// templated send: the NotImplemented members, tags, addresses and the
// recipient count (SendChecks). TemplateData's length was
// checked by model validation (ses.md 4.3).
func checkSendTemplated(in *sesapi.SendTemplatedEmailInput) *refusal {
	if r := firstUnimplemented("ses", "SendTemplatedEmail", []unimplemented{
		{"ConfigurationSetName", "configuration sets", in.ConfigurationSetName != nil},
		{"SourceArn", "sending authorization", in.SourceArn != nil},
		{"ReturnPathArn", "sending authorization", in.ReturnPathArn != nil},
		{"TemplateArn", "template ARNs", in.TemplateArn != nil},
	}); r != nil {
		return r
	}
	for _, t := range in.Tags {
		if r := checkTag(t.Name, t.Value); r != nil {
			return r
		}
	}
	d := in.Destination
	for _, a := range slices.Concat([]string{in.Source}, d.ToAddresses, d.CcAddresses, d.BccAddresses, in.ReplyToAddresses) {
		if _, r := parseAddress(a); r != nil {
			return r
		}
	}
	if in.ReturnPath != nil {
		if _, r := parseAddress(*in.ReturnPath); r != nil {
			return r
		}
	}
	return checkRecipientCount(len(d.ToAddresses) + len(d.CcAddresses) + len(d.BccAddresses))
}

// storedTemplate is the template in v named name as a send of data uses
// it, or the refusal of a template the scope does not hold. Which check SES
// runs first, this or the sender's, is unrecorded (SES-18).
func (s *state) storedTemplate(v view, name, data string) (*TemplateSend, *refusal) {
	i := s.findTemplate(v, name)
	if i < 0 {
		return nil, templateMissing(name)
	}
	t := &s.Templates[i]
	return &TemplateSend{Name: t.Name, Subject: clonePart(t.Subject), Text: clonePart(t.Text), HTML: clonePart(t.HTML), Data: data}, nil
}

// clonePart is a template part sharing no memory with the state.
func clonePart(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
