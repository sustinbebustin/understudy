package server_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A SendTemplatedEmail is readable through GET /ses/messages with its
// rendered parts and its template, data and render gaps under "template"
// (ses.md 11.2).
func TestSESTemplatedSendThroughTheAdminAPI(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{SESVerifiedIdentities: server.SESVerifiedIdentities{AddressDomains: []string{"example.net"}}})
	for _, form := range []url.Values{
		{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {"from@example.net"}},
		{"Action": {"CreateTemplate"}, "Template.TemplateName": {"t"}, "Template.SubjectPart": {"Hi {{name}}"}, "Template.TextPart": {"{{#if a}}x{{/if}}"}},
		{
			"Action": {"SendTemplatedEmail"}, "Source": {"from@example.net"}, "Destination.ToAddresses.member.1": {"success@simulator.amazonses.com"},
			"Template": {"t"}, "TemplateData": {`{"name":"Ana"}`},
		},
	} {
		if status, body := sesCall(t, endpoint, form); status != http.StatusOK {
			t.Fatalf("%s = %d %s", form.Get("Action"), status, body)
		}
	}
	page, err := c.SESMessages(t.Context(), admin.SESMessageQuery{To: "success@simulator.amazonses.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("SESMessages = %+v, want one send", page)
	}
	m := page.Messages[0]
	if m.Subject == nil || m.Subject.Data != "Hi Ana" || m.Text != nil {
		t.Errorf("subject %+v, text %+v; want Hi Ana and no text", m.Subject, m.Text)
	}
	if m.Template == nil {
		t.Fatalf("message %+v has no template", m)
	}
	subject, text := "Hi {{name}}", "{{#if a}}x{{/if}}"
	want := &admin.SESTemplate{Name: "t", Subject: &subject, Text: &text, Data: `{"name":"Ana"}`}
	got := *m.Template
	got.Gaps = nil
	if diff := cmp.Diff(want, &got); diff != "" {
		t.Errorf("template (-want +got):\n%s", diff)
	}
	if len(m.Template.Gaps) != 2 || m.Template.Gaps[0].Part != "text" || m.Template.Gaps[0].Tag != "{{#if a}}" {
		t.Errorf("gaps = %+v, want {{#if a}} and {{/if}} in text", m.Template.Gaps)
	}
}
