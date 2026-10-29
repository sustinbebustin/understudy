package ses_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
)

// secret marks every piece of message content in the inputs below; a
// journaled input (kernel doc: every input is journaled as its Redact())
// must carry none of it, in plain or base64 form (ses.md 11.2).
const secret = "content-that-must-not-be-journaled"

func assertNoContent(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding the redacted input: %v", err)
	}
	for _, form := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
		if strings.Contains(string(b), form) {
			t.Errorf("the redacted input keeps message content %q: %s", form, b)
		}
	}
	if !strings.Contains(string(b), "sender@example.com") {
		t.Errorf("the redacted input lost its sender, which the journal keeps: %s", b)
	}
}

func content() *string { s := secret; return &s }

func TestRedactV1SendInputsKeepNoContent(t *testing.T) {
	text := sesapi.Content{Data: secret}
	assertNoContent(t, (&sesapi.SendEmailInput{
		Source:      "sender@example.com",
		Destination: sesapi.Destination{ToAddresses: []string{"to@example.com"}},
		Message: sesapi.Message{
			Subject: sesapi.Content{Data: secret},
			Body:    sesapi.Body{Text: &text, Html: &text},
		},
	}).Redact())
	source := "sender@example.com"
	assertNoContent(t, (&sesapi.SendRawEmailInput{
		Source:     &source,
		RawMessage: sesapi.RawMessage{Data: []byte(secret)},
	}).Redact())
	assertNoContent(t, (&sesapi.SendTemplatedEmailInput{
		Source:       "sender@example.com",
		Destination:  sesapi.Destination{ToAddresses: []string{"to@example.com"}},
		Template:     "t",
		TemplateData: secret,
	}).Redact())
}

func TestRedactV2SendInputKeepsNoContent(t *testing.T) {
	from := "sender@example.com"
	text := sesv2api.Content{Data: secret}
	attachments := []sesv2api.Attachment{{FileName: "a.txt", RawContent: []byte(secret)}}
	for name, c := range map[string]sesv2api.EmailContent{
		"simple": {Simple: &sesv2api.Message{
			Subject:     sesv2api.Content{Data: secret},
			Body:        sesv2api.Body{Text: &text, Html: &text},
			Attachments: attachments,
		}},
		"raw": {Raw: &sesv2api.RawMessage{Data: []byte(secret)}},
		"template": {Template: &sesv2api.Template{
			TemplateContent: &sesv2api.EmailTemplateContent{Subject: content(), Text: content(), Html: content()},
			TemplateData:    content(),
			Attachments:     attachments,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			assertNoContent(t, (&sesv2api.SendEmailInput{FromEmailAddress: &from, Content: c}).Redact())
		})
	}
}
