package ses_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// createEmailTemplate creates template name, with a Text part "t" when
// content has neither Text nor Html, which AWS refuses (SES-18, ses.md 7).
func (h *harness) createEmailTemplate(name string, content sesv2api.EmailTemplateContent, tags ...sesv2api.Tag) {
	h.Helper()
	if content.Text == nil && content.Html == nil {
		content.Text = new("t")
	}
	must(h, sesv2api.OpCreateEmailTemplate, &sesv2api.CreateEmailTemplateInput{TemplateName: name, TemplateContent: content, Tags: tags})
}

func (h *harness) templateARN(name string) string {
	h.Helper()
	return strings.Replace(h.identityARN(name), ":identity/", ":template/", 1)
}

// CreateEmailTemplate stores the content and tags GetEmailTemplate answers
// (ses.md 7); v1 GetTemplate reads the same template, the store being
// shared (switch ses.store.shared-v1-v2, ses.md 2.3).
func TestEmailTemplateRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		h.createEmailTemplate("welcome", sesv2api.EmailTemplateContent{Subject: new("Hi {{name}}"), Text: new("Dear {{name}}")},
			sesv2api.Tag{Key: "team", Value: "mail"})
		got := must(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "welcome"})
		c := got.TemplateContent
		if got.TemplateName != "welcome" || c.Subject == nil || *c.Subject != "Hi {{name}}" || c.Text == nil || *c.Text != "Dear {{name}}" || c.Html != nil {
			h.Errorf("GetEmailTemplate = %+v", got)
		}
		if len(got.Tags) != 1 || got.Tags[0] != (sesv2api.Tag{Key: "team", Value: "mail"}) {
			h.Errorf("GetEmailTemplate tags = %+v", got.Tags)
		}
		v1 := must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "welcome"}).Template
		if v1.SubjectPart == nil || *v1.SubjectPart != "Hi {{name}}" || v1.HtmlPart != nil {
			h.Errorf("v1 GetTemplate of a v2 template = %+v", v1)
		}
	})
}

// A template v1 created is read by v2, and stays out of v2's view when the
// APIs are kept apart (switch ses.store.shared-v1-v2, ses.md 2.3).
func TestEmailTemplatesFollowTheStoreSwitch(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "v1", HtmlPart: new("<p>x</p>")})
		got := must(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "v1"})
		if got.TemplateContent.Html == nil || *got.TemplateContent.Html != "<p>x</p>" || got.Tags == nil {
			h.Errorf("v2 GetEmailTemplate of a v1 template = %+v, want its HTML and empty tags", got)
		}
	})
	runWith(t, ses.Options{Store: ses.StoreSeparate}, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "v1"})
		_, err := call(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "v1"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.TemplateMissingText, "v1"))
		h.createEmailTemplate("v1", sesv2api.EmailTemplateContent{Subject: new("s")})
		h.createEmailTemplate("v2only", sesv2api.EmailTemplateContent{})
		var v1 []string
		for _, m := range must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{}).TemplatesMetadata {
			if m.Name != nil {
				v1 = append(v1, *m.Name)
			}
		}
		if !slices.Equal(v1, []string{"v1"}) {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v1 ListTemplates = %q, want only v1's template", ses.StoreSeparate, v1)
		}
	})
}

// A duplicate is AlreadyExistsException; a missing template is
// NotFoundException on Get, Update and Delete, each declaring it (model;
// the texts are inferred, SES-18). Above MaxTemplateBytes is
// BadRequestException (ses.md 7; the answer is inferred, SES-29).
func TestEmailTemplateErrors(t *testing.T) {
	run(t, func(h *harness) {
		h.createEmailTemplate("dup", sesv2api.EmailTemplateContent{Subject: new("s")})
		_, err := call(h, sesv2api.OpCreateEmailTemplate, &sesv2api.CreateEmailTemplateInput{TemplateName: "dup", TemplateContent: sesv2api.EmailTemplateContent{Text: new("t")}})
		wantErr(h, err, sesv2api.IdentityAlreadyExistsException(), fmt.Sprintf(ses.TemplateExistsText, "dup"))

		missing := fmt.Sprintf(ses.TemplateMissingText, "gone")
		_, err = call(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "gone"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), missing)
		_, err = call(h, sesv2api.OpUpdateEmailTemplate, &sesv2api.UpdateEmailTemplateInput{TemplateName: "gone"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), missing)
		_, err = call(h, sesv2api.OpDeleteEmailTemplate, &sesv2api.DeleteEmailTemplateInput{TemplateName: "gone"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), missing)

		big := sesv2api.EmailTemplateContent{Text: new(strings.Repeat("t", ses.MaxTemplateBytes/2)), Html: new(strings.Repeat("h", ses.MaxTemplateBytes/2+1))}
		_, err = call(h, sesv2api.OpCreateEmailTemplate, &sesv2api.CreateEmailTemplateInput{TemplateName: "big", TemplateContent: big})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), fmt.Sprintf(ses.TemplateTooLargeText, "big"))
		_, err = call(h, sesv2api.OpUpdateEmailTemplate, &sesv2api.UpdateEmailTemplateInput{TemplateName: "dup", TemplateContent: big})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), fmt.Sprintf(ses.TemplateTooLargeText, "dup"))
	})
}

// UpdateEmailTemplate replaces the content, an omitted part becoming
// unset, and keeps the tags; DeleteEmailTemplate removes it.
func TestUpdateAndDeleteEmailTemplate(t *testing.T) {
	run(t, func(h *harness) {
		h.createEmailTemplate("a", sesv2api.EmailTemplateContent{Subject: new("s"), Text: new("t")}, sesv2api.Tag{Key: "k", Value: "v"})
		must(h, sesv2api.OpUpdateEmailTemplate, &sesv2api.UpdateEmailTemplateInput{TemplateName: "a", TemplateContent: sesv2api.EmailTemplateContent{Html: new("h")}})
		got := must(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "a"})
		if c := got.TemplateContent; c.Subject != nil || c.Text != nil || c.Html == nil || *c.Html != "h" || len(got.Tags) != 1 {
			h.Errorf("after the update = %+v", got)
		}
		must(h, sesv2api.OpDeleteEmailTemplate, &sesv2api.DeleteEmailTemplateInput{TemplateName: "a"})
		_, err := call(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "a"})
		wantCode(h, err, sesv2api.IdentityNotFoundException())
	})
}

// ListEmailTemplates lists v1 and v2 templates in creation order with
// their creation time, paging by PageSize "at least 1, and ... no more
// than 100" (model, com.amazonaws.sesv2#ListEmailTemplatesRequest$PageSize;
// the refusal's text is inferred, SES-29).
func TestListEmailTemplates(t *testing.T) {
	run(t, func(h *harness) {
		empty := must(h, sesv2api.OpListEmailTemplates, &sesv2api.ListEmailTemplatesInput{})
		if empty.TemplatesMetadata == nil || len(empty.TemplatesMetadata) != 0 || empty.NextToken != nil {
			h.Errorf("ListEmailTemplates of none = %+v, want []", empty)
		}
		want := make([]string, 0, 12)
		for i := range 12 {
			name := fmt.Sprintf("t%02d", 11-i)
			if i%2 == 0 {
				h.createTemplate(sesapi.Template{TemplateName: name})
			} else {
				h.createEmailTemplate(name, sesv2api.EmailTemplateContent{})
			}
			want = append(want, name)
		}
		var got []string
		in := &sesv2api.ListEmailTemplatesInput{PageSize: new(int32(5))}
		for pages := 0; ; pages++ {
			if pages > 3 {
				h.Fatalf("ListEmailTemplates did not end after %d pages: %q", pages, got)
			}
			out := must(h, sesv2api.OpListEmailTemplates, in)
			for _, m := range out.TemplatesMetadata {
				if m.CreatedTimestamp == nil || m.TemplateName == nil {
					h.Fatalf("metadata %+v lacks a name or a creation time", m)
				}
				got = append(got, *m.TemplateName)
			}
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if !slices.Equal(got, want) {
			h.Errorf("paged ListEmailTemplates = %q, want %q", got, want)
		}
		if out := must(h, sesv2api.OpListEmailTemplates, &sesv2api.ListEmailTemplatesInput{}); len(out.TemplatesMetadata) != ses.ListEmailTemplatesDefaultPageSize {
			h.Errorf("default page = %d templates, want %d", len(out.TemplatesMetadata), ses.ListEmailTemplatesDefaultPageSize)
		}
		for _, n := range []int32{0, ses.ListEmailTemplatesMaxPageSize + 1} {
			_, err := call(h, sesv2api.OpListEmailTemplates, &sesv2api.ListEmailTemplatesInput{PageSize: new(n)})
			wantErr(h, err, sesv2api.IdentityBadRequestException(), fmt.Sprintf(ses.TemplatePageSizeText, ses.ListEmailTemplatesMaxPageSize))
		}
	})
}

// The tag operations act on a template ARN as on an identity's (ses.md
// 3.2), and journal it; a
// template the scope does not hold is NotFoundException naming it.
func TestTagsOnTemplateARNs(t *testing.T) {
	run(t, func(h *harness) {
		h.createEmailTemplate("t", sesv2api.EmailTemplateContent{}, sesv2api.Tag{Key: "a", Value: "1"})
		arn := h.templateARN("t")
		must(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: arn, Tags: []sesv2api.Tag{{Key: "a", Value: "2"}, {Key: "b", Value: "3"}}})
		must(h, sesv2api.OpUntagResource, &sesv2api.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"b"}})
		tags := must(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: arn}).Tags
		if !slices.Equal(tags, []sesv2api.Tag{{Key: "a", Value: "2"}}) {
			h.Errorf("template tags = %+v, want a=2", tags)
		}
		calls := h.apiCalls("sesv2", "TagResource")
		if len(calls) != 1 {
			h.Fatalf("%d TagResource calls journaled", len(calls))
		}
		wantTouches(h, calls[0].Resources, arn)

		_, err := call(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: h.templateARN("gone")})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.TemplateMissingText, "gone"))
	})
}

// CreateEmailTemplate and the other template operations journal the
// template ARN.
func TestEmailTemplateOperationsTouchTheTemplate(t *testing.T) {
	run(t, func(h *harness) {
		h.createEmailTemplate("t", sesv2api.EmailTemplateContent{})
		calls := h.apiCalls("sesv2", "CreateEmailTemplate")
		if len(calls) != 1 {
			h.Fatalf("%d CreateEmailTemplate calls journaled", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.templateARN("t"))
	})
}
