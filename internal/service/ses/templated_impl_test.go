package ses_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// templated is a v1 SendTemplatedEmail from sender of template name with
// data, to the given To addresses.
func templated(name, data string, to ...string) *sesapi.SendTemplatedEmailInput {
	return &sesapi.SendTemplatedEmailInput{
		Source:       sender,
		Destination:  sesapi.Destination{ToAddresses: to},
		Template:     name,
		TemplateData: data,
	}
}

// onlySend is the one recorded send, failing unless there is exactly one.
func onlySend(h *harness) ses.Message {
	h.Helper()
	got := h.sends()
	if len(got) != 1 {
		h.Fatalf("%d sends recorded, want 1: %+v", len(got), got)
	}
	return got[0]
}

// wantPart fails unless c is a part rendered as want, with no charset.
func wantPart(h *harness, what string, c *ses.Content, want string) {
	h.Helper()
	if c == nil || c.Data != want || c.Charset != "" {
		h.Errorf("rendered %s = %+v, want %q", what, c, want)
	}
}

// SendTemplatedEmail renders {{name}} and dotted-path tags from
// TemplateData (ses.md 7, 11.6 item 6) and records the stored template as
// used with the data as given beside the rendered parts (ses.md 11.2).
func TestSendTemplatedEmailRendersNamesAndPaths(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		tmpl := sesapi.Template{
			TemplateName: "welcome",
			SubjectPart:  new("Hi {{name}}"),
			TextPart:     new("Dear {{ user.first }}, order {{n}} paid: {{paid}}"),
			HtmlPart:     new("<p>{{name}}</p>"),
		}
		h.createTemplate(tmpl)
		const data = `{"name":"Ana","user":{"first":"A"},"n":3,"paid":true}`
		out := must(h, sesapi.OpSendTemplatedEmail, templated("welcome", data, simulator))
		m := onlySend(h)
		if m.MessageID != out.MessageId || m.Operation != "SendTemplatedEmail" || m.API != ses.APIv1 || m.Source != sender {
			h.Errorf("record = %+v, want MessageId %s from SendTemplatedEmail", m, out.MessageId)
		}
		wantPart(h, "subject", m.Subject, "Hi Ana")
		wantPart(h, "text", m.Text, "Dear A, order 3 paid: true")
		wantPart(h, "html", m.HTML, "<p>Ana</p>")
		want := &ses.TemplateSend{Name: "welcome", Subject: tmpl.SubjectPart, Text: tmpl.TextPart, HTML: tmpl.HtmlPart, Data: data}
		if diff := cmp.Diff(want, m.Template); diff != "" {
			h.Errorf("recorded template (-want +got):\n%s", diff)
		}
		if got := h.quota().SentLast24Hours; got != 0 {
			h.Errorf("SentLast24Hours = %v after a simulator send, want 0", got)
		}
	})
}

// A construct outside {{name}} and dotted paths, a value the data lacks,
// one Handlebars would escape, or anything but a string, boolean or integer
// leaves its part unrendered with a render gap naming the tag; the send is
// still accepted, as SES accepts before rendering (ses.md 4.3, 11.8).
func TestSendTemplatedEmailRecordsRenderGaps(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createTemplate(sesapi.Template{
			TemplateName: "gaps",
			SubjectPart:  new("Hi {{name}}"),
			TextPart:     new("{{#each items}}x{{/each}} {{{raw}}}"),
			HtmlPart:     new("{{missing}} {{amp}} {{ratio}} {{this.name}} {{name"),
		})
		const data = `{"name":"Ana","amp":"a&b","ratio":1.5,"items":[1]}`
		must(h, sesapi.OpSendTemplatedEmail, templated("gaps", data, simulator))
		m := onlySend(h)
		wantPart(h, "subject", m.Subject, "Hi Ana")
		if m.Text != nil || m.HTML != nil {
			h.Errorf("parts with a gap were rendered: text %+v, html %+v", m.Text, m.HTML)
		}
		got := make([]string, 0, len(m.Template.Gaps))
		for _, g := range m.Template.Gaps {
			if g.Reason == "" {
				h.Errorf("gap %+v gives no reason", g)
			}
			got = append(got, g.Part+" "+g.Tag)
		}
		want := []string{
			"text {{#each items}}", "text {{/each}}", "text {{{raw}}}",
			"html {{missing}}", "html {{amp}}", "html {{ratio}}", "html {{this.name}}", "html {{name",
		}
		if diff := cmp.Diff(want, got); diff != "" {
			h.Errorf("gaps (-want +got):\n%s", diff)
		}
	})
}

// TemplateData that is not a JSON object renders nothing: one gap, the
// send accepted (ses.md 4.3).
func TestSendTemplatedEmailWithUnreadableData(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "t", SubjectPart: new("plain")})
		for _, data := range []string{"", "not json", "[1]", `{"a":1} extra`} {
			must(h, sesapi.OpSendTemplatedEmail, templated("t", data, simulator))
		}
		for _, m := range h.sends() {
			if m.Subject != nil || len(m.Template.Gaps) != 1 || m.Template.Gaps[0].Part != "" {
				h.Errorf("data %q: subject %+v, gaps %+v; want nothing rendered and one gap", m.Template.Data, m.Subject, m.Template.Gaps)
			}
		}
	})
}

// A template the scope does not hold is TemplateDoesNotExist, member
// TemplateName (model, ses.md 3.3; text inferred, SES-18), and nothing is
// recorded or counted.
func TestSendTemplatedEmailMissingTemplate(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesapi.OpSendTemplatedEmail, templated("none", "{}", recipient))
		wantErr(h, err, sesapi.IdentityTemplateDoesNotExistException(), fmt.Sprintf(ses.TemplateMissingText, "none"))
		if m, ok := err.Members().(*sesapi.TemplateDoesNotExistException); !ok || m.TemplateName == nil || *m.TemplateName != "none" {
			h.Errorf("members = %#v, want TemplateName none", err.Members())
		}
		if len(h.sends()) != 0 || h.quota().SentLast24Hours != 0 {
			h.Errorf("a refused send was recorded or counted")
		}
	})
}

// SendTemplatedEmail runs SendEmail's checks: NotImplemented members (the
// template ARN among them), tags, addresses, the recipient count, then the
// sender and sandbox checks (ses.md 11.1).
func TestSendTemplatedEmailChecks(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "t", SubjectPart: new("s")})
		for member, set := range map[string]func(*sesapi.SendTemplatedEmailInput){
			"SourceArn":     func(in *sesapi.SendTemplatedEmailInput) { in.SourceArn = new("arn") },
			"ReturnPathArn": func(in *sesapi.SendTemplatedEmailInput) { in.ReturnPathArn = new("arn") },
			"TemplateArn":   func(in *sesapi.SendTemplatedEmailInput) { in.TemplateArn = new("arn") },
		} {
			in := templated("t", "{}", simulator)
			set(in)
			_, err := call(h, sesapi.OpSendTemplatedEmail, in)
			wantNotImplemented(h, err, member)
		}
		_, err := call(h, sesapi.OpSendTemplatedEmail, templated("t", "{}", "no-at-sign"))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressMissingDomainText)
		_, err = call(h, sesapi.OpSendTemplatedEmail, templated("t", "{}"))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.NoRecipientsText)
		const stranger = "stranger@unverified.test"
		_, err = call(h, sesapi.OpSendTemplatedEmail, templated("t", "{}", stranger))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
		in := templated("t", "{}", simulator)
		in.Tags = []sesapi.MessageTag{{Name: "a:b", Value: "v"}}
		_, err = call(h, sesapi.OpSendTemplatedEmail, in)
		wantCode(h, err, sesapi.IdentityInvalidParameterValue())
		if len(h.sends()) != 0 {
			h.Errorf("a refused send was recorded: %+v", h.sends())
		}
	})
}

// The journaled call touches the sender's identity ARN and keeps no
// TemplateData (ses.md 11.2; overlay smithy.api#sensitive).
func TestSendTemplatedEmailJournal(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "t", SubjectPart: new("{{x}}")})
		must(h, sesapi.OpSendTemplatedEmail, templated("t", `{"x":"`+journalSecret+`"}`, simulator))
		calls := h.apiCalls("ses", "SendTemplatedEmail")
		if len(calls) != 1 {
			h.Fatalf("%d SendTemplatedEmail calls journaled, want 1", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN(sender))
		wantNoSecret(h, calls[0].Input.JSON())
	})
}

// templateV2 is a v2 SendEmail from sender to simulator with Template
// content t.
func templateV2(t *sesv2api.Template) *sesv2api.SendEmailInput {
	in := simpleEmail(sender, simulator)
	in.Content = sesv2api.EmailContent{Template: t}
	return in
}

// v2 Template content renders a stored template by name, or inline
// content, as SendTemplatedEmail does (ses.md 4.4, 11.6 item 6); its
// Headers and Attachments are recorded as Simple content's are.
func TestV2TemplateContent(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "stored", SubjectPart: new("Hi {{a.b}}")})
		must(h, sesv2api.OpSendEmail, templateV2(&sesv2api.Template{TemplateName: new("stored"), TemplateData: new(`{"a":{"b":"Bo"}}`)}))
		inline := &sesv2api.EmailTemplateContent{Subject: new("S {{x}}"), Text: new("T {{x}}")}
		must(h, sesv2api.OpSendEmail, templateV2(&sesv2api.Template{
			TemplateContent: inline,
			TemplateData:    new(`{"x":"1"}`),
			Headers:         []sesv2api.MessageHeader{{Name: "X-Test", Value: "v"}},
			Attachments:     []sesv2api.Attachment{{FileName: "a.txt", RawContent: []byte("attached")}},
		}))
		got := h.sends()
		if len(got) != 2 {
			h.Fatalf("%d sends recorded, want 2", len(got))
		}
		wantPart(h, "stored subject", got[0].Subject, "Hi Bo")
		if got[0].Template.Name != "stored" || got[0].API != ses.APIv2 {
			h.Errorf("stored send = %+v", got[0])
		}
		wantPart(h, "inline subject", got[1].Subject, "S 1")
		wantPart(h, "inline text", got[1].Text, "T 1")
		want := &ses.TemplateSend{Subject: inline.Subject, Text: inline.Text, Data: `{"x":"1"}`}
		if diff := cmp.Diff(want, got[1].Template); diff != "" {
			h.Errorf("inline template (-want +got):\n%s", diff)
		}
		if len(got[1].Headers) != 1 || got[1].Headers[0] != (ses.Header{Name: "X-Test", Value: "v"}) {
			h.Errorf("headers = %+v", got[1].Headers)
		}
		if len(got[1].Attachments) != 1 || string(got[1].Attachments[0].Content) != "attached" {
			h.Errorf("attachments = %+v", got[1].Attachments)
		}
	})
}

// v2 Template content without TemplateData renders tag-free parts and
// leaves a gap for each tag.
func TestV2TemplateContentWithoutData(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesv2api.OpSendEmail, templateV2(&sesv2api.Template{
			TemplateContent: &sesv2api.EmailTemplateContent{Subject: new("plain"), Text: new("{{x}}")},
		}))
		m := onlySend(h)
		wantPart(h, "subject", m.Subject, "plain")
		if m.Text != nil || len(m.Template.Gaps) != 1 || m.Template.Gaps[0].Tag != "{{x}}" {
			h.Errorf("text %+v, gaps %+v; want {{x}} a gap", m.Text, m.Template.Gaps)
		}
	})
}

// v2 Template content names exactly one of a stored template and inline
// content, needs FromEmailAddress as Simple does, answers NotFoundException
// for a stored template the scope does not hold (texts inferred, SES-18,
// SES-23), and refuses TemplateArn as NotImplemented.
func TestV2TemplateContentRefusals(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		content := &sesv2api.EmailTemplateContent{Subject: new("s")}
		for _, tmpl := range []*sesv2api.Template{{}, {TemplateName: new("t"), TemplateContent: content}} {
			_, err := call(h, sesv2api.OpSendEmail, templateV2(tmpl))
			wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.TemplateSourceText)
		}
		in := templateV2(&sesv2api.Template{TemplateContent: content})
		in.FromEmailAddress = nil
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.MissingFromText)
		_, err = call(h, sesv2api.OpSendEmail, templateV2(&sesv2api.Template{TemplateName: new("none")}))
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.TemplateMissingText, "none"))
		_, err = call(h, sesv2api.OpSendEmail, templateV2(&sesv2api.Template{TemplateArn: new("arn:aws:ses:us-east-1:000000000000:template/t")}))
		wantNotImplemented(h, err, "Content.Template.TemplateArn")
		if len(h.sends()) != 0 {
			h.Errorf("a refused send was recorded: %+v", h.sends())
		}
	})
}

// Under ses.store.shared-v1-v2 separate, v2 does not see a v1 template.
func TestSeparateStoresKeepTemplatesApart(t *testing.T) {
	runVerified(t, ses.Options{Store: ses.StoreSeparate}, func(h *harness) {
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: verifiedDomain})
		h.createTemplate(sesapi.Template{TemplateName: "v1only", SubjectPart: new("s")})
		in := templateV2(&sesv2api.Template{TemplateName: new("v1only")})
		in.FromEmailAddress = new("x@" + verifiedDomain)
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.TemplateMissingText, "v1only"))
	})
}
