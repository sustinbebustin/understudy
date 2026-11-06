package ses

import (
	"context"
	"fmt"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindV2Templates binds the v2 email template operations over the template
// store v1 shares (switch ses.store.shared-v1-v2, ses.md 2.3). Each uses
// its primary action alone and touches the template ARN, except
// CreateEmailTemplate with Tags, which also declares ses:TagResource;
// ListEmailTemplates names no resource (contract 2.6).
func (v *V2) bindV2Templates(b sesv2api.Binder) {
	b.CreateEmailTemplate(v.createEmailTemplate, registry.Full{},
		kernel.WithActions(func(_ *kernel.Call, in *sesv2api.CreateEmailTemplateInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{{Prefix: "ses", Name: "TagResource"}}
		}),
		refusedConditionKeys(),
		templateNamed(func(in *sesv2api.CreateEmailTemplateInput) string { return in.TemplateName }))
	b.GetEmailTemplate(v.getEmailTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		templateNamed(func(in *sesv2api.GetEmailTemplateInput) string { return in.TemplateName }))
	b.UpdateEmailTemplate(v.updateEmailTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		templateNamed(func(in *sesv2api.UpdateEmailTemplateInput) string { return in.TemplateName }))
	b.DeleteEmailTemplate(v.deleteEmailTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		templateNamed(func(in *sesv2api.DeleteEmailTemplateInput) string { return in.TemplateName }))
	b.ListEmailTemplates(v.listEmailTemplates, registry.Full{}, kernel.WithoutSecondaryActions(), refusedConditionKeys())
}

// errEmailTemplateMissing answers a v2 call on a template the caller's view
// does not hold: NotFoundException, which each v2 template operation but
// Create and List declares (model; the text is inferred, SES-18).
func errEmailTemplateMissing(name string) *awserr.Error {
	return sesv2api.ErrNotFoundException(fmt.Sprintf(TemplateMissingText, name))
}

// checkEmailTemplateSize refuses content of template name whose text and
// HTML parts together are above MaxTemplateBytes, with v1's recorded text
// (ses.md 7; the v2 answer is inferred, SES-29 recorded v1).
func checkEmailTemplateSize(name string, c *sesv2api.EmailTemplateContent) *awserr.Error {
	n := 0
	for _, p := range []*string{c.Text, c.Html} {
		if p != nil {
			n += len(*p)
		}
	}
	if n > MaxTemplateBytes {
		return sesv2api.ErrBadRequestException(fmt.Sprintf(TemplateTooLargeText, name))
	}
	return nil
}

// createEmailTemplate stores a new template with its tags, checked as v1
// CreateTemplate checks one: a text or HTML part (recorded, SES-18), size,
// then a free name, then MaxTemplates (ses.md 7; the AlreadyExists and
// limit texts are inferred).
func (v *V2) createEmailTemplate(_ context.Context, c *kernel.Call, in *sesv2api.CreateEmailTemplateInput) (*sesv2api.CreateEmailTemplateOutput, *awserr.Error) {
	if in.TemplateContent.Text == nil && in.TemplateContent.Html == nil {
		return nil, sesv2api.ErrBadRequestException(TemplateNoPartsText)
	}
	if err := checkEmailTemplateSize(in.TemplateName, &in.TemplateContent); err != nil {
		return nil, err
	}
	s := v.svc
	vw := s.view(APIv2)
	tc := in.TemplateContent
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if st.findTemplate(vw, in.TemplateName) >= 0 {
			return struct{}{}, sesv2api.ErrAlreadyExistsException(fmt.Sprintf(TemplateExistsText, in.TemplateName))
		}
		if st.templates(vw) >= MaxTemplates {
			return struct{}{}, sesv2api.ErrLimitExceededException(fmt.Sprintf(TemplateLimitText, MaxTemplates))
		}
		st.Templates = append(st.Templates, template{
			Name: in.TemplateName, Subject: tc.Subject, Text: tc.Text, HTML: tc.Html,
			Tags: mergeTags(nil, in.Tags), API: APIv2, Created: now,
		})
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.CreateEmailTemplateOutput{}, nil
}

// getEmailTemplate answers a template's content exactly as stored, unset
// parts omitted, and its tags, [] when it has none (ses.md 7).
func (v *V2) getEmailTemplate(_ context.Context, c *kernel.Call, in *sesv2api.GetEmailTemplateInput) (*sesv2api.GetEmailTemplateOutput, *awserr.Error) {
	var out *sesv2api.GetEmailTemplateOutput
	v.read(c.Scope(), func(st *state) {
		i := st.findTemplate(v.svc.view(APIv2), in.TemplateName)
		if i < 0 {
			return
		}
		t := &st.Templates[i]
		out = &sesv2api.GetEmailTemplateOutput{
			TemplateName:    t.Name,
			TemplateContent: sesv2api.EmailTemplateContent{Subject: clonePart(t.Subject), Text: clonePart(t.Text), Html: clonePart(t.HTML)},
			Tags:            tagList(t.Tags),
		}
	})
	if out == nil {
		return nil, errEmailTemplateMissing(in.TemplateName)
	}
	return out, nil
}

// updateEmailTemplate replaces every part of an existing template, an
// omitted part becoming unset, as v1 UpdateTemplate does; its tags and
// creation time stay.
func (v *V2) updateEmailTemplate(_ context.Context, c *kernel.Call, in *sesv2api.UpdateEmailTemplateInput) (*sesv2api.UpdateEmailTemplateOutput, *awserr.Error) {
	if err := checkEmailTemplateSize(in.TemplateName, &in.TemplateContent); err != nil {
		return nil, err
	}
	s := v.svc
	vw := s.view(APIv2)
	tc := in.TemplateContent
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.findTemplate(vw, in.TemplateName)
		if i < 0 {
			return struct{}{}, errEmailTemplateMissing(in.TemplateName)
		}
		cur := &st.Templates[i]
		cur.Subject, cur.Text, cur.HTML = tc.Subject, tc.Text, tc.Html
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.UpdateEmailTemplateOutput{}, nil
}

// deleteEmailTemplate removes a template. Unlike v1 DeleteTemplate, whose
// model declares no error, a missing one is NotFoundException, which v2's
// model declares (the text is inferred, SES-18).
func (v *V2) deleteEmailTemplate(_ context.Context, c *kernel.Call, in *sesv2api.DeleteEmailTemplateInput) (*sesv2api.DeleteEmailTemplateOutput, *awserr.Error) {
	s := v.svc
	vw := s.view(APIv2)
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.findTemplate(vw, in.TemplateName)
		if i < 0 {
			return struct{}{}, errEmailTemplateMissing(in.TemplateName)
		}
		st.Templates = append(st.Templates[:i:i], st.Templates[i+1:]...)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.DeleteEmailTemplateOutput{}, nil
}

// listEmailTemplates lists the templates in v2's view in creation order,
// [] when there are none. PageSize is 1 to ListEmailTemplatesMaxPageSize
// (model; the refusal's text is inferred, SES-29), its default
// ListEmailTemplatesDefaultPageSize. NextToken is understudy's own, as
// ListTemplates' is.
func (v *V2) listEmailTemplates(_ context.Context, c *kernel.Call, in *sesv2api.ListEmailTemplatesInput) (*sesv2api.ListEmailTemplatesOutput, *awserr.Error) {
	limit := ListEmailTemplatesDefaultPageSize
	if in.PageSize != nil {
		if *in.PageSize < 1 || *in.PageSize > ListEmailTemplatesMaxPageSize {
			return nil, sesv2api.ErrBadRequestException(fmt.Sprintf(TemplatePageSizeText, ListEmailTemplatesMaxPageSize))
		}
		limit = int(*in.PageSize)
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesv2api.ErrBadRequestException("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListEmailTemplates")
		}
		start = n
	}
	var all []sesv2api.EmailTemplateMetadata
	vw := v.svc.view(APIv2)
	if cell, ok := v.svc.cells.Lookup(c.Scope()); ok {
		all = store.Read(cell, func(st *state) []sesv2api.EmailTemplateMetadata {
			var out []sesv2api.EmailTemplateMetadata
			for i := range st.Templates {
				t := &st.Templates[i]
				if vw.owns(t.API) {
					out = append(out, sesv2api.EmailTemplateMetadata{TemplateName: new(t.Name), CreatedTimestamp: new(t.Created.Wire())})
				}
			}
			return out
		})
	}
	items, next := page(all, start, limit)
	if items == nil {
		items = []sesv2api.EmailTemplateMetadata{}
	}
	return &sesv2api.ListEmailTemplatesOutput{TemplatesMetadata: items, NextToken: next}, nil
}
