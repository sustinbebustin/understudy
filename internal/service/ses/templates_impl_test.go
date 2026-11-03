package ses_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// ListTemplates pages in creation order: following NextToken with a small
// MaxItems lists every template once, and the last page carries no token
// (model, com.amazonaws.ses#ListTemplatesResponse$NextToken).
func TestListTemplatesPagesInCreationOrder(t *testing.T) {
	run(t, func(h *harness) {
		want := make([]string, 0, 12)
		for i := range 12 {
			name := fmt.Sprintf("p%02d", 11-i)
			h.createTemplate(sesapi.Template{TemplateName: name})
			want = append(want, name)
		}
		var got []string
		in := &sesapi.ListTemplatesInput{MaxItems: new(int32(5))}
		for pages := 0; ; pages++ {
			if pages > 3 {
				h.Fatalf("ListTemplates did not end after %d pages: %q", pages, got)
			}
			out := must(h, sesapi.OpListTemplates, in)
			for _, m := range out.TemplatesMetadata {
				got = append(got, *m.Name)
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if !slices.Equal(got, want) {
			h.Errorf("paged ListTemplates = %q, want %q", got, want)
		}
	})
}

// AlreadyExists names the template in its modeled Name member (ses.md 2.2,
// 7; inferred); GetTemplate's TemplateDoesNotExist carries no member, as
// recorded (SES-18).
func TestTemplateErrorsCarryTheirMembers(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "dup"})
		_, err := call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "dup", TextPart: new("t")}})
		if m, ok := err.Members().(*sesapi.AlreadyExistsException); !ok || m.Name == nil || *m.Name != "dup" {
			h.Errorf("AlreadyExists members = %#v, want Name dup", err.Members())
		}
		// GetTemplate's recorded answer has no TemplateName (SES-18).
		_, err = call(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "gone"})
		if m, ok := err.WireMembers(); ok {
			h.Errorf("TemplateDoesNotExist members = %#v, want none", m)
		}
	})
}

// UpdateTemplate keeps the size limit and leaves the template as it was
// when refused; an accepted update keeps its creation time and place.
// DeleteTemplate of a missing template succeeds, the model declaring no
// error for it (ses.md 7).
func TestUpdateAndDeleteTemplate(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "a", TextPart: new("t")})
		h.createTemplate(sesapi.Template{TemplateName: "b"})
		before := must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{}).TemplatesMetadata

		_, err := call(h, sesapi.OpUpdateTemplate, &sesapi.UpdateTemplateInput{Template: sesapi.Template{
			TemplateName: "a", HtmlPart: new(strings.Repeat("h", ses.MaxTemplateBytes+1)),
		}})
		wantErr(h, err, sesapi.IdentityInvalidTemplateException(), fmt.Sprintf(ses.TemplateTooLargeText, "a"))
		if got := must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "a"}).Template; got.TextPart == nil || *got.TextPart != "t" || got.HtmlPart != nil {
			h.Errorf("after a refused update = %+v, want the old parts", got)
		}

		must(h, sesapi.OpUpdateTemplate, &sesapi.UpdateTemplateInput{Template: sesapi.Template{TemplateName: "a", SubjectPart: new("s")}})
		got := must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "a"}).Template
		if got.SubjectPart == nil || *got.SubjectPart != "s" || got.TextPart != nil {
			h.Errorf("after an update = %+v, want only SubjectPart s", got)
		}
		after := must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{}).TemplatesMetadata
		if len(after) != 2 || *after[0].Name != "a" || !after[0].CreatedTimestamp.Equal(*before[0].CreatedTimestamp) {
			h.Errorf("ListTemplates after the update = %+v, want a first with its creation time %v", after, before[0].CreatedTimestamp)
		}

		must(h, sesapi.OpDeleteTemplate, &sesapi.DeleteTemplateInput{TemplateName: "missing"})
	})
}

// Template operations touch the template ARN (contract 2.6), so the
// journal names the template a call acted on.
func TestTemplateOperationsTouchTheTemplateARN(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "welcome"})
		must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "welcome"})
		must(h, sesapi.OpUpdateTemplate, &sesapi.UpdateTemplateInput{Template: sesapi.Template{TemplateName: "welcome"}})
		must(h, sesapi.OpDeleteTemplate, &sesapi.DeleteTemplateInput{TemplateName: "welcome"})
		arn := "arn:aws:ses:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":template/welcome"
		for _, op := range []string{"CreateTemplate", "GetTemplate", "UpdateTemplate", "DeleteTemplate"} {
			calls := h.apiCalls("ses", op)
			if len(calls) != 1 {
				h.Errorf("ses.%s journaled %d times, want 1", op, len(calls))
				continue
			}
			wantTouches(h, calls[0].Resources, arn)
		}
	})
}
