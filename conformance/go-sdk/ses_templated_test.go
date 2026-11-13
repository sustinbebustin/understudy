package gosdk_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Research: docs/research/ses.md sections 4.3, 4.4, 7 and 11.6 item 6.

// verifyByLink opens the link of the verification email recorded for addr,
// as its owner would (ses.md 11.3 item 1).
func verifyByLink(t *testing.T, addr string) {
	t.Helper()
	verification := sesMessages(t, url.Values{"to": {addr}})
	if len(verification) != 1 {
		t.Fatalf("messages to %s = %+v, want the verification email", addr, verification)
	}
	link := verificationLink.FindString(verification[0].Text.Data)
	if link == "" {
		t.Fatalf("verification email has no link: %q", verification[0].Text.Data)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, link, http.NoBody)
	ok(t, err, "GET verification link")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "GET verification link")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", link, resp.StatusCode)
	}
}

// SendTemplatedEmail of a missing template is TemplateDoesNotExist (model,
// ses.md 3.3); of a stored one it is accepted with a MessageId (4.3, SES-18
// 07), and understudy records the {{name}} substitution (7, 11.6 item 6).
func TestSESSendTemplatedEmail(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	addr := name(t, "ses-tmpl-sender") + "@example.com"
	_, err := client.VerifyEmailIdentity(ctx, &ses.VerifyEmailIdentityInput{EmailAddress: aws.String(addr)})
	ok(t, err, "VerifyEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteIdentity(context.Background(), &ses.DeleteIdentityInput{Identity: aws.String(addr)})
	})
	verifyByLink(t, addr)

	tmpl := name(t, "ses-tmpl")
	in := &ses.SendTemplatedEmailInput{
		Source:       aws.String(addr),
		Destination:  &sestypes.Destination{ToAddresses: []string{"success@simulator.amazonses.com"}},
		Template:     aws.String(tmpl),
		TemplateData: aws.String(`{"name":"x"}`),
	}
	_, err = client.SendTemplatedEmail(ctx, in)
	if _, missing := errors.AsType[*sestypes.TemplateDoesNotExistException](err); !missing {
		t.Errorf("SendTemplatedEmail of a missing template = %v, want TemplateDoesNotExist", err)
	}

	_, err = client.CreateTemplate(ctx, &ses.CreateTemplateInput{Template: &sestypes.Template{
		TemplateName: aws.String(tmpl), SubjectPart: aws.String("Hi {{name}}"), TextPart: aws.String("Dear {{name}}"),
	}})
	ok(t, err, "CreateTemplate")
	t.Cleanup(func() {
		_, _ = client.DeleteTemplate(context.Background(), &ses.DeleteTemplateInput{TemplateName: aws.String(tmpl)})
	})
	out, err := client.SendTemplatedEmail(ctx, in)
	ok(t, err, "SendTemplatedEmail")
	got := sesMessages(t, url.Values{"messageId": {aws.ToString(out.MessageId)}})
	if len(got) != 1 || got[0].Subject == nil || got[0].Subject.Data != "Hi x" || got[0].Text.Data != "Dear x" ||
		got[0].Template == nil || got[0].Template.Name != tmpl {
		t.Errorf("recorded %+v, want Hi x and Dear x from %s", got, tmpl)
	}
}

// v2 inline TemplateContent supports "only simple substitutions" (model,
// ses.md 4.4); understudy renders {{name}} (11.6 item 6).
func TestSESv2SendEmailInlineTemplate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sesv2.NewFromConfig(awsCfg)
	addr := name(t, "sesv2-tmpl-sender") + "@example.com"
	_, err := client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String(addr)})
	ok(t, err, "CreateEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteEmailIdentity(context.Background(), &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(addr)})
	})
	verifyByLink(t, addr)

	out, err := client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(addr),
		Destination:      &types.Destination{ToAddresses: []string{"success@simulator.amazonses.com"}},
		Content: &types.EmailContent{Template: &types.Template{
			TemplateContent: &types.EmailTemplateContent{Subject: aws.String("Hi {{name}}"), Text: aws.String("Dear {{name}}")},
			TemplateData:    aws.String(`{"name":"x"}`),
		}},
	})
	ok(t, err, "SendEmail")
	got := sesMessages(t, url.Values{"messageId": {aws.ToString(out.MessageId)}})
	if len(got) != 1 || got[0].API != "sesv2" || got[0].Subject == nil || got[0].Subject.Data != "Hi x" || got[0].Text.Data != "Dear x" ||
		got[0].Template == nil || got[0].Template.Data != `{"name":"x"}` {
		t.Errorf("recorded %+v, want Hi x and Dear x from the inline template", got)
	}
}
