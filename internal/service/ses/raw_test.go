package ses_test

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// mime is a raw message: the header lines, a blank line, then body, with
// CRLF line endings.
func mime(body string, headers ...string) []byte {
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body)
}

func rawEmail(data []byte, destinations ...string) *sesapi.SendRawEmailInput {
	return &sesapi.SendRawEmailInput{Destinations: destinations, RawMessage: sesapi.RawMessage{Data: data}}
}

// Golden test_send_email_raises_message_rejected: Destinations
// ["invalid@example.com"] and a raw From origin@example.com, both
// unverified, fail MessageRejected (third-party-recorded 2025-09-11,
// ses.md 4.2; localstack@8b9a79f tests/aws/services/ses/test_ses.py:L1162-L1170,
// validated 2025-09-11).
func TestSendRawEmailRaisesMessageRejected(t *testing.T) {
	run(t, func(h *harness) {
		data := mime("This is the message body.\r\n", "From: origin@example.com", "To: invalid@example.com", "Subject: test")
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data, "invalid@example.com"))
		if err == nil || err.ID.QueryCode != "MessageRejected" {
			h.Errorf("error = %+v, want MessageRejected (ses.md 4.2)", err)
		}
	})
}

// Switch ses.raw.envelope (inferred, SES-20): Destinations defines the
// envelope recipients when given (aws-doc send-email-raw, ses.md 4.2),
// else the To, Cc and Bcc headers do; union uses both.
func TestRawEnvelopeFollowsItsSwitch(t *testing.T) {
	const stranger = "header@unverified.test"
	data := mime("body\r\n", "From: "+sender, "To: "+stranger, "Subject: s")
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesapi.OpSendRawEmail, rawEmail(data, simulator))
		sent := h.sends()
		if len(sent) != 1 || !slices.Equal(sent[0].Envelope, []string{simulator}) {
			h.Fatalf("switch ses.raw.envelope=%s: recorded %+v, want envelope [%s]", ses.RawEnvelopeDestinations, sent, simulator)
		}
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
	})
	runVerified(t, ses.Options{RawEnvelope: ses.RawEnvelopeUnion}, func(h *harness) {
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data, simulator))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
	})
}

// The raw message is recorded as received, with its sender and recipients
// read from the headers; X-SES-MESSAGE-TAGS (aws-doc event-publishing,
// ses.md 4.2) are recorded after Tags.
func TestSendRawEmailIsRecorded(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		data := mime("body\r\n",
			"From: Sender Name <"+sender+">",
			"To: "+recipient,
			"Cc: "+simulator,
			"Subject: raw",
			"X-SES-MESSAGE-TAGS: h1=v1, h2=v2",
		)
		in := rawEmail(data)
		in.Tags = []sesapi.MessageTag{{Name: "t1", Value: "v"}}
		id := must(h, sesapi.OpSendRawEmail, in).MessageId
		sent := h.sends()
		if len(sent) != 1 {
			h.Fatalf("%d sends recorded, want 1", len(sent))
		}
		m := sent[0]
		if m.MessageID != id || m.API != ses.APIv1 || m.Operation != "SendRawEmail" || !bytes.Equal(m.Raw, data) {
			h.Errorf("record = %+v, want SendRawEmail %s with the raw bytes as sent", m, id)
		}
		if !slices.Equal(m.From, []string{sender}) || !slices.Equal(m.Envelope, []string{recipient, simulator}) {
			h.Errorf("from %q envelope %q, want [%s] and [%s %s]", m.From, m.Envelope, sender, recipient, simulator)
		}
		wantTags := []ses.Tag{{Name: "t1", Value: "v"}, {Name: "h1", Value: "v1"}, {Name: "h2", Value: "v2"}}
		if !slices.Equal(m.Tags, wantTags) {
			h.Errorf("tags = %+v, want %+v", m.Tags, wantTags)
		}
		if m.Subject != nil || m.Text != nil || m.HTML != nil {
			h.Errorf("a raw record carries Simple content: %+v %+v %+v", m.Subject, m.Text, m.HTML)
		}
	})
}

// X-SES-MESSAGE-TAGS are validated as tags (ses.md 4.2, 4.5): a bad name
// answers the recorded text.
func TestRawMessageTagsHeaderIsValidated(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		data := mime("body\r\n", "From: "+sender, "To: "+simulator, "X-SES-MESSAGE-TAGS: bad:name=v")
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Invalid tag name <bad:name>: only alphanumeric ASCII characters, '_',  '-' are allowed.")
	})
}

// The sending-authorization members and headers are not implemented and
// say so, naming them (invariant 3; the headers, ses.md 4.2).
func TestSendRawEmailNotImplemented(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		arn := h.identityARN(sender)
		for member, set := range map[string]func(*sesapi.SendRawEmailInput){
			"SourceArn":     func(in *sesapi.SendRawEmailInput) { in.SourceArn = new(arn) },
			"FromArn":       func(in *sesapi.SendRawEmailInput) { in.FromArn = new(arn) },
			"ReturnPathArn": func(in *sesapi.SendRawEmailInput) { in.ReturnPathArn = new(arn) },
		} {
			in := rawEmail(mime("body\r\n", "From: "+sender, "To: "+simulator))
			set(in)
			_, err := call(h, sesapi.OpSendRawEmail, in)
			wantNotImplemented(h, err, member)
		}
		for _, header := range []string{"X-SES-SOURCE-ARN", "X-SES-FROM-ARN", "X-SES-RETURN-PATH-ARN"} {
			data := mime("body\r\n", "From: "+sender, "To: "+simulator, header+": "+arn)
			_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
			wantNotImplemented(h, err, header)
		}
	})
}

// Lines of at most 1,000 characters (model, SendRawEmailRequest.RawMessage;
// ses.md 4.2) and at most 500 MIME parts (aws-doc send-email-raw); the
// answers are inferred (SES-29) and read from the constants.
func TestRawLineAndPartLimits(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		headers := []string{"From: " + sender, "To: " + simulator, "Subject: s"}
		must(h, sesapi.OpSendRawEmail, rawEmail(mime(strings.Repeat("a", ses.MaxRawLineLength)+"\r\n", headers...)))
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(mime(strings.Repeat("a", ses.MaxRawLineLength+1)+"\r\n", headers...)))
		wantErr(h, err, sesapi.IdentityMessageRejected(), fmt.Sprintf(ses.LineTooLongText, ses.MaxRawLineLength))

		multipart := func(parts int) []byte {
			var b strings.Builder
			for i := range parts {
				fmt.Fprintf(&b, "--b\r\nContent-Type: text/plain\r\n\r\npart %d\r\n", i)
			}
			b.WriteString("--b--\r\n")
			return mime(b.String(), append(headers, "MIME-Version: 1.0", `Content-Type: multipart/mixed; boundary="b"`)...)
		}
		must(h, sesapi.OpSendRawEmail, rawEmail(multipart(ses.MaxMIMEParts-1)))
		_, err = call(h, sesapi.OpSendRawEmail, rawEmail(multipart(ses.MaxMIMEParts+1)))
		wantErr(h, err, sesapi.IdentityMessageRejected(), fmt.Sprintf(ses.TooManyPartsText, ses.MaxMIMEParts))
	})
}

// The size limit is 10 MB on v1 (aws-doc quotas, ses.md 2.2), counted
// after base64; bound and answer inferred (SES-29) and read from the
// constants.
func TestSendRawEmailMessageTooLong(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		line := strings.Repeat("a", 76) + "\r\n"
		body := strings.Repeat(line, ses.MaxV1MessageBytes*3/4/len(line)+1)
		data := mime(body, "From: "+sender, "To: "+simulator, "Subject: s")
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(data))
		wantErr(h, err, sesapi.IdentityMessageRejected(), fmt.Sprintf(ses.MessageTooLongText, ses.MaxV1MessageBytes))
	})
}

// Without Source a From header is required (model, SendRawEmailRequest;
// ses.md 4.2); the answer is recorded (SES-20) and read from the constant.
func TestSendRawEmailWithoutSender(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(mime("body\r\n", "To: "+simulator, "Subject: s")))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.RawMissingFromText)
	})
}

// The journaled SendRawEmail input keeps no raw bytes, and the call
// touches the sender's identity ARN.
func TestSendRawEmailJournalKeepsNoContent(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		data := mime(journalSecret+"\r\n", "From: "+sender, "To: "+simulator, "Subject: "+journalSecret)
		must(h, sesapi.OpSendRawEmail, rawEmail(data))
		calls := h.apiCalls("ses", "SendRawEmail")
		if len(calls) != 1 {
			h.Fatalf("%d SendRawEmail calls journaled, want 1", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN(sender))
		wantNoSecret(h, calls[0].Input.JSON())
	})
}
