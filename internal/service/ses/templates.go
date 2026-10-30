package ses

import (
	"context"
	"fmt"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// template is one v1 email template (ses.md 7). Each part is kept exactly
// as given, nil when the request left it out, so GetTemplate echoes the
// parts given and omits the rest (ses.md 9.4).
type template struct {
	Name    string        `json:"name"`
	Subject *string       `json:"subject,omitempty"`
	Text    *string       `json:"text,omitempty"`
	HTML    *string       `json:"html,omitempty"`
	Created clock.Instant `json:"created"`
}

// templateARN is the ARN of template name in scope,
// arn:aws:ses:<region>:<account>:template/<name> (ses.md 9.4).
func templateARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "ses",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "template/" + name,
	}
}

// findTemplate is the index of the template named name, byte for byte, or
// -1.
func (s *state) findTemplate(name string) int {
	for i := range s.Templates {
		if s.Templates[i].Name == name {
			return i
		}
	}
	return -1
}

// bindTemplates binds the v1 template operations. Each uses its primary
// action alone and touches the template ARN; ListTemplates names no
// resource (contract 2.6).
func (s *Service) bindTemplates(b sesapi.Binder) {
	b.CreateTemplate(s.createTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(),
		templateNamed(func(in *sesapi.CreateTemplateInput) string { return in.Template.TemplateName }))
	b.GetTemplate(s.getTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(),
		templateNamed(func(in *sesapi.GetTemplateInput) string { return in.TemplateName }))
	b.UpdateTemplate(s.updateTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(),
		templateNamed(func(in *sesapi.UpdateTemplateInput) string { return in.Template.TemplateName }))
	b.DeleteTemplate(s.deleteTemplate, registry.Full{},
		kernel.WithoutSecondaryActions(),
		templateNamed(func(in *sesapi.DeleteTemplateInput) string { return in.TemplateName }))
	b.ListTemplates(s.listTemplates, registry.Full{}, kernel.WithoutSecondaryActions())
}

// templateNamed names the template an input addresses as the call's
// resource (contract 2.6).
func templateNamed[In any](nameOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{templateARN(c.Scope(), nameOf(in))}
	})
}

// checkTemplateSize refuses a template whose text and HTML parts together
// are above MaxTemplateBytes (ses.md 7; the bound's reading and the answer
// are inferred, SES-29).
func checkTemplateSize(t *sesapi.Template) *awserr.Error {
	n := 0
	for _, p := range []*string{t.TextPart, t.HtmlPart} {
		if p != nil {
			n += len(*p)
		}
	}
	if n > MaxTemplateBytes {
		return sesapi.ErrInvalidParameterValue(fmt.Sprintf(TemplateTooLargeText, MaxTemplateBytes))
	}
	return nil
}

// errTemplateMissing answers GetTemplate or UpdateTemplate of a template
// the scope does not hold (ses.md 7; the text is inferred, SES-18).
func errTemplateMissing(name string) *awserr.Error {
	return sesapi.ErrTemplateDoesNotExistExceptionMembers(fmt.Sprintf(TemplateMissingText, name),
		sesapi.TemplateDoesNotExistException{TemplateName: new(name)})
}

// createTemplate stores a new template. Its size is checked first, then
// that the name is free (AlreadyExists, member Name), then MaxTemplates
// (ses.md 7; the texts are inferred, SES-18, SES-29).
func (s *Service) createTemplate(_ context.Context, c *kernel.Call, in *sesapi.CreateTemplateInput) (*sesapi.CreateTemplateOutput, *awserr.Error) {
	if err := checkTemplateSize(&in.Template); err != nil {
		return nil, err
	}
	t := in.Template
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if st.findTemplate(t.TemplateName) >= 0 {
			return struct{}{}, sesapi.ErrAlreadyExistsExceptionMembers(fmt.Sprintf(TemplateExistsText, t.TemplateName),
				sesapi.AlreadyExistsException{Name: new(t.TemplateName)})
		}
		if len(st.Templates) >= MaxTemplates {
			return struct{}{}, sesapi.ErrLimitExceededException(fmt.Sprintf(TemplateLimitText, MaxTemplates))
		}
		st.Templates = append(st.Templates, template{Name: t.TemplateName, Subject: t.SubjectPart, Text: t.TextPart, HTML: t.HtmlPart, Created: now})
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.CreateTemplateOutput{}, nil
}

// getTemplate answers a template's parts exactly as stored, unset ones
// omitted (ses.md 9.4).
func (s *Service) getTemplate(_ context.Context, c *kernel.Call, in *sesapi.GetTemplateInput) (*sesapi.GetTemplateOutput, *awserr.Error) {
	cell, ok := s.cells.Lookup(c.Scope())
	if !ok {
		return nil, errTemplateMissing(in.TemplateName)
	}
	t := store.Read(cell, func(st *state) *sesapi.Template {
		i := st.findTemplate(in.TemplateName)
		if i < 0 {
			return nil
		}
		t := &st.Templates[i]
		return &sesapi.Template{TemplateName: t.Name, SubjectPart: t.Subject, TextPart: t.Text, HtmlPart: t.HTML}
	})
	if t == nil {
		return nil, errTemplateMissing(in.TemplateName)
	}
	return &sesapi.GetTemplateOutput{Template: t}, nil
}

// updateTemplate replaces every part of an existing template, an omitted
// part becoming unset; its creation time stays (ses.md 7, 9.4).
func (s *Service) updateTemplate(_ context.Context, c *kernel.Call, in *sesapi.UpdateTemplateInput) (*sesapi.UpdateTemplateOutput, *awserr.Error) {
	if err := checkTemplateSize(&in.Template); err != nil {
		return nil, err
	}
	t := in.Template
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.findTemplate(t.TemplateName)
		if i < 0 {
			return struct{}{}, errTemplateMissing(t.TemplateName)
		}
		cur := &st.Templates[i]
		cur.Subject, cur.Text, cur.HTML = t.SubjectPart, t.TextPart, t.HtmlPart
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.UpdateTemplateOutput{}, nil
}

// deleteTemplate removes a template; one the scope does not hold succeeds
// unchanged, as the model declares no error for it (ses.md 7).
func (s *Service) deleteTemplate(_ context.Context, c *kernel.Call, in *sesapi.DeleteTemplateInput) (*sesapi.DeleteTemplateOutput, *awserr.Error) {
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.findTemplate(in.TemplateName)
		if i < 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		st.Templates = append(st.Templates[:i:i], st.Templates[i+1:]...)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.DeleteTemplateOutput{}, nil
}

// listTemplates lists the templates of the caller's scope in creation
// order, [] when there are none (ses.md 7). MaxItems defaults to
// ListTemplatesDefaultItems and a larger one than ListTemplatesMaxItems is
// clamped (model); below 1 is refused (the answer is inferred, SES-29).
// NextToken is understudy's own, as ListIdentities' is.
func (s *Service) listTemplates(_ context.Context, c *kernel.Call, in *sesapi.ListTemplatesInput) (*sesapi.ListTemplatesOutput, *awserr.Error) {
	limit := ListTemplatesDefaultItems
	if in.MaxItems != nil {
		if *in.MaxItems < 1 {
			return nil, sesapi.ErrInvalidParameterValue(fmt.Sprintf(MaxItemsText, ListTemplatesMaxItems))
		}
		limit = min(int(*in.MaxItems), ListTemplatesMaxItems)
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesapi.ErrInvalidParameterValue("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListTemplates")
		}
		start = n
	}
	var all []sesapi.TemplateMetadata
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		all = store.Read(cell, func(st *state) []sesapi.TemplateMetadata {
			out := make([]sesapi.TemplateMetadata, len(st.Templates))
			for i := range st.Templates {
				t := &st.Templates[i]
				out[i] = sesapi.TemplateMetadata{Name: new(t.Name), CreatedTimestamp: new(t.Created.Wire())}
			}
			return out
		})
	}
	items, next := page(all, start, limit)
	if items == nil {
		items = []sesapi.TemplateMetadata{}
	}
	return &sesapi.ListTemplatesOutput{TemplatesMetadata: items, NextToken: next}, nil
}
