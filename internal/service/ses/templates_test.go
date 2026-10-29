package ses_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

func (h *harness) createTemplate(t sesapi.Template) {
	h.Helper()
	must(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: t})
}

func (h *harness) templateNames(maxItems *int32) []string {
	h.Helper()
	out := must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{MaxItems: maxItems})
	names := make([]string, 0, len(out.TemplatesMetadata))
	for _, m := range out.TemplatesMetadata {
		if m.Name == nil || m.CreatedTimestamp == nil {
			h.Fatalf("TemplatesMetadata entry %+v lacks Name or CreatedTimestamp (ses.md 7)", m)
		}
		names = append(names, *m.Name)
	}
	return names
}

// Goldens test_list_templates and test_delete_template: ListTemplates
// returns TemplatesMetadata of {CreatedTimestamp, Name}, [] when there are
// none, and a template disappears from it after DeleteTemplate
// (third-party-recorded 2023-08-25, ses.md 7; localstack@8b9a79f
// tests/aws/services/ses/test_ses.snapshot.json:L736-L807, validated
// 2023-08-25).
func TestListAndDeleteTemplateGoldens(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{})
		if out.TemplatesMetadata == nil || len(out.TemplatesMetadata) != 0 {
			h.Errorf("ListTemplates with none = %#v, want []", out.TemplatesMetadata)
		}
		h.createTemplate(sesapi.Template{TemplateName: "hello-world", SubjectPart: new("Subject"), TextPart: new("Text"), HtmlPart: new("<p>Html</p>")})
		if got := h.templateNames(nil); len(got) != 1 || got[0] != "hello-world" {
			h.Errorf("ListTemplates = %q, want [hello-world]", got)
		}
		must(h, sesapi.OpDeleteTemplate, &sesapi.DeleteTemplateInput{TemplateName: "hello-world"})
		if got := h.templateNames(nil); len(got) != 0 {
			h.Errorf("ListTemplates after DeleteTemplate = %q, want []", got)
		}
	})
}

// GetTemplate echoes each part exactly and omits unset ones; UpdateTemplate
// replaces them (provider, ses.md 9.4). GetTemplate and UpdateTemplate of a
// missing template raise TemplateDoesNotExist (model, ses.md 7); the text
// is inferred (SES-18) and read from the constant.
func TestTemplateCRUD(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "partial", SubjectPart: new("Hi {{name}}"), TextPart: new(" spaced \n")})
		got := must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "partial"}).Template
		if got == nil || got.TemplateName != "partial" || got.SubjectPart == nil || *got.SubjectPart != "Hi {{name}}" ||
			got.TextPart == nil || *got.TextPart != " spaced \n" || got.HtmlPart != nil {
			h.Errorf("GetTemplate = %+v, want the parts as given and no HtmlPart", got)
		}
		must(h, sesapi.OpUpdateTemplate, &sesapi.UpdateTemplateInput{Template: sesapi.Template{
			TemplateName: "partial", SubjectPart: new("S2"), TextPart: new("T2"), HtmlPart: new("<b>new</b>"),
		}})
		got = must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "partial"}).Template
		if got == nil || got.SubjectPart == nil || *got.SubjectPart != "S2" || got.TextPart == nil || *got.TextPart != "T2" ||
			got.HtmlPart == nil || *got.HtmlPart != "<b>new</b>" {
			h.Errorf("GetTemplate after UpdateTemplate = %+v, want the new parts", got)
		}
		_, err := call(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "missing"})
		wantErr(h, err, sesapi.IdentityTemplateDoesNotExistException(), fmt.Sprintf(ses.TemplateMissingText, "missing"))
		_, err = call(h, sesapi.OpUpdateTemplate, &sesapi.UpdateTemplateInput{Template: sesapi.Template{TemplateName: "missing"}})
		wantErr(h, err, sesapi.IdentityTemplateDoesNotExistException(), fmt.Sprintf(ses.TemplateMissingText, "missing"))
		_, err = call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "partial"}})
		wantErr(h, err, sesapi.IdentityAlreadyExistsException(), fmt.Sprintf(ses.TemplateExistsText, "partial"))
	})
}

// 500 KB per template, text and HTML parts together (aws-doc quotas,
// ses.md 7); the bound is read as MaxTemplateBytes and the answer is
// inferred (SES-29).
func TestTemplateSizeLimit(t *testing.T) {
	run(t, func(h *harness) {
		half := ses.MaxTemplateBytes / 2
		h.createTemplate(sesapi.Template{TemplateName: "at", TextPart: new(strings.Repeat("t", half)), HtmlPart: new(strings.Repeat("h", ses.MaxTemplateBytes-half))})
		_, err := call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{
			TemplateName: "over", TextPart: new(strings.Repeat("t", half)), HtmlPart: new(strings.Repeat("h", ses.MaxTemplateBytes-half+1)),
		}})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.TemplateTooLargeText, ses.MaxTemplateBytes))
	})
}

// 20,000 templates per Region (aws-doc quotas, ses.md 7); the answer past
// it is inferred and read from the constant.
func TestTemplateLimit(t *testing.T) {
	run(t, func(h *harness) {
		for i := range ses.MaxTemplates {
			h.createTemplate(sesapi.Template{TemplateName: fmt.Sprintf("t%d", i)})
		}
		_, err := call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "over"}})
		wantErr(h, err, sesapi.IdentityLimitExceededException(), fmt.Sprintf(ses.TemplateLimitText, ses.MaxTemplates))
	})
}

// ListTemplates pages ListTemplatesDefaultItems by default and clamps a
// MaxItems above ListTemplatesMaxItems (model,
// com.amazonaws.ses#ListTemplatesRequest$MaxItems); below 1 is refused
// with MaxItemsText (inferred, SES-29).
func TestListTemplatesMaxItems(t *testing.T) {
	run(t, func(h *harness) {
		for i := range ses.ListTemplatesMaxItems + 5 {
			h.createTemplate(sesapi.Template{TemplateName: fmt.Sprintf("t%03d", i)})
		}
		if got := len(h.templateNames(nil)); got != ses.ListTemplatesDefaultItems {
			h.Errorf("ListTemplates without MaxItems lists %d, want %d", got, ses.ListTemplatesDefaultItems)
		}
		if got := len(h.templateNames(new(int32(ses.ListTemplatesMaxItems + 5)))); got != ses.ListTemplatesMaxItems {
			h.Errorf("ListTemplates MaxItems %d lists %d, want %d", ses.ListTemplatesMaxItems+5, got, ses.ListTemplatesMaxItems)
		}
		_, err := call(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{MaxItems: new(int32(0))})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.MaxItemsText, ses.ListTemplatesMaxItems))
	})
}
