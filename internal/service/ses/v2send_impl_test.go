package ses_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// rawV2 is a v2 SendEmail with Raw content data and no Destination.
func rawV2(data []byte) *sesv2api.SendEmailInput {
	return &sesv2api.SendEmailInput{Content: sesv2api.EmailContent{Raw: &sesv2api.RawMessage{Data: data}}}
}

// Template content answers NotImplemented naming it until the template
// send lands (spec ticket 13; invariant 3).
func TestV2TemplateContentIsNotImplemented(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := simpleEmail(sender, simulator)
		in.Content = sesv2api.EmailContent{Template: &sesv2api.Template{TemplateName: new("welcome")}}
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantNotImplemented(h, err, "Content.Template")
	})
}

// Simple content runs v1 SendEmail's address and recipient-count checks,
// answering through v2's validation identity (contract 1.6).
func TestV2SimpleAddressAndRecipientChecks(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesv2api.OpSendEmail, simpleEmail(sender, "no-at-sign"))
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.AddressMissingDomainText)
		in := simpleEmail(sender)
		in.Destination = nil
		_, err = call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.NoRecipientsText)
		_, err = call(h, sesv2api.OpSendEmail, simpleEmail(sender, "a@x.test", "b@x.test"))
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "a@x.test", "b@x.test"))
	})
}

// A header name outside the model pattern ^[!-9;-@A-~]+$ (ses.md 4.4) is
// refused by model validation before any send check.
func TestV2HeaderPatternIsValidated(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := simpleEmail(sender, simulator)
		in.Content.Simple.Headers = []sesv2api.MessageHeader{{Name: "Bad:Name", Value: "v"}}
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantCode(h, err, sesv2api.IdentityBadRequestException())
		if got := h.sends(); len(got) != 0 {
			h.Errorf("a refused send was recorded: %+v", got)
		}
	})
}

// Raw content takes FromEmailAddress as v1 takes Source, and
// FeedbackForwardingEmailAddress in place of the Return-Path header: both
// are sender-checked and recorded (spec conflict row 9; ses.md 4.2).
func TestV2RawSendersAndReturnPath(t *testing.T) {
	const stranger = "stranger@unverified.test"
	runVerified(t, ses.Options{}, func(h *harness) {
		data := mime("body\r\n", "From: "+stranger, "To: "+simulator, "Return-Path: <"+stranger+">")
		in := rawV2(data)
		in.FromEmailAddress = new(sender)
		in.FeedbackForwardingEmailAddress = new("feedback@unverified.test")
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "feedback@unverified.test"))

		in.FeedbackForwardingEmailAddress = new("feedback@" + verifiedDomain)
		id := must(h, sesv2api.OpSendEmail, in).MessageId
		sent := h.sends()
		if len(sent) != 1 {
			h.Fatalf("%d sends recorded, want 1", len(sent))
		}
		m := sent[0]
		if id == nil || m.MessageID != *id || m.API != ses.APIv2 || m.Source != sender || !slices.Equal(m.From, []string{sender}) ||
			m.ReturnPath != "feedback@"+verifiedDomain || !bytes.Equal(m.Raw, data) {
			h.Errorf("record %+v, want Source and From %s, returnPath feedback@%s and the raw bytes", m, sender, verifiedDomain)
		}
	})
}

// A v2 Raw send's journaled input keeps no raw bytes and touches the From
// header's identity ARN (contract 2.1, 2.6).
func TestV2RawSendIsRedacted(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := rawV2(mime(journalSecret+"\r\n", "From: "+sender, "To: "+simulator, "Subject: "+journalSecret))
		must(h, sesv2api.OpSendEmail, in)
		calls := h.apiCalls("sesv2", "SendEmail")
		if len(calls) != 1 {
			h.Fatalf("%d v2 SendEmail calls journaled, want 1", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN(sender))
		wantNoSecret(h, calls[0].Input.JSON())
	})
}

// Attachment contents live in the blob store: a refused send keeps none,
// a snapshot names each and round-trips them, and clearing the message
// releases them (spec Scope).
func TestV2AttachmentBlobLifecycle(t *testing.T) {
	runVerifiedWithBlobs(t, func(h *harness, blobs *blob.Store) {
		attach := func(in *sesv2api.SendEmailInput) *sesv2api.SendEmailInput {
			in.Content.Simple.Attachments = []sesv2api.Attachment{
				{FileName: "a.txt", RawContent: []byte("first attachment")},
				{FileName: "b.txt", RawContent: []byte("second attachment")},
			}
			return in
		}
		if _, err := call(h, sesv2api.OpSendEmail, attach(simpleEmail("stranger@unverified.test", simulator))); err == nil {
			h.Fatal("an unverified v2 sender was accepted")
		}
		if refs := blobs.Refs(); len(refs) != 0 {
			h.Fatalf("a refused send left blobs %v", refs)
		}

		must(h, sesv2api.OpSendEmail, attach(simpleEmail(sender, simulator)))
		want := make([]string, 0, 2)
		for _, c := range []string{"first attachment", "second attachment"} {
			sum := sha256.Sum256([]byte(c))
			want = append(want, hex.EncodeToString(sum[:]))
		}
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		if len(sections) != 1 || !slices.Equal(sections[0].Blobs, want) {
			h.Fatalf("exported sections %+v, want one naming blobs %v", sections, want)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		sent := h.sends()
		if len(sent) != 1 || len(sent[0].Attachments) != 2 ||
			string(sent[0].Attachments[0].Content) != "first attachment" || string(sent[0].Attachments[1].Content) != "second attachment" {
			h.Fatalf("after the round trip the send is %+v", sent)
		}

		if n, err := h.svc.ClearMessages(ses.MessageQuery{}); err != nil || n == 0 {
			h.Fatalf("ClearMessages = %d, %v", n, err)
		}
		for _, w := range want {
			ref, err := blob.ParseRef(w)
			if err != nil {
				h.Fatal(err)
			}
			if _, err := blobs.Size(ref); err == nil {
				h.Errorf("attachment blob %s outlived its cleared record", w)
			}
		}
	})
}
