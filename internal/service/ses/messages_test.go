package ses_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// An accepted v1 SendEmail is recorded with the fields of contract 3.1,
// the envelope To, Cc and Bcc in order, and each simulator recipient
// mapped to its scenario by local part, labels allowed (ses.md 6.2).
func TestSendEmailIsRecorded(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		bounce := "bounce+label1@" + ses.SimulatorDomain
		in := &sesapi.SendEmailInput{
			Source: "Sender Name <" + sender + ">",
			Destination: sesapi.Destination{
				ToAddresses:  []string{recipient},
				CcAddresses:  []string{bounce},
				BccAddresses: []string{"hidden@" + verifiedDomain},
			},
			ReplyToAddresses: []string{"replies@elsewhere.test"},
			ReturnPath:       new("bounces@" + verifiedDomain),
			Message: sesapi.Message{
				Subject: sesapi.Content{Data: "Hello", Charset: new("UTF-8")},
				Body: sesapi.Body{
					Text: &sesapi.Content{Data: "plain"},
					Html: &sesapi.Content{Data: "<p>html</p>", Charset: new("UTF-8")},
				},
			},
			Tags: []sesapi.MessageTag{{Name: "campaign", Value: "spring"}},
		}
		id := must(h, sesapi.OpSendEmail, in).MessageId
		sent := h.sends()
		if len(sent) != 1 {
			h.Fatalf("%d sends recorded, want 1", len(sent))
		}
		m := sent[0]
		calls := h.apiCalls("ses", "SendEmail")
		if len(calls) != 1 {
			h.Fatalf("%d SendEmail calls journaled, want 1", len(calls))
		}
		want := ses.Message{
			MessageID:  id,
			API:        ses.APIv1,
			Operation:  "SendEmail",
			RequestID:  calls[0].RequestID,
			Scope:      h.Scope,
			Accepted:   h.Clock.Now(),
			Kind:       ses.KindSend,
			Source:     in.Source,
			From:       []string{sender},
			ReplyTo:    in.ReplyToAddresses,
			ReturnPath: *in.ReturnPath,
			To:         in.Destination.ToAddresses,
			Cc:         in.Destination.CcAddresses,
			Bcc:        in.Destination.BccAddresses,
			Envelope:   []string{recipient, bounce, "hidden@" + verifiedDomain},
			Subject:    &ses.Content{Data: "Hello", Charset: "UTF-8"},
			Text:       &ses.Content{Data: "plain"},
			HTML:       &ses.Content{Data: "<p>html</p>", Charset: "UTF-8"},
			Tags:       []ses.Tag{{Name: "campaign", Value: "spring"}},
			Simulator:  map[string]ses.Scenario{bounce: ses.ScenarioBounce},
		}
		opts := cmp.Options{
			cmp.Comparer(func(a, b aws.Scope) bool { return a == b }),
			cmp.Comparer(func(a, b clock.Instant) bool { return a.Compare(b) == 0 }),
		}
		if diff := cmp.Diff(want, m, opts); diff != "" {
			h.Errorf("recorded message (-want +got):\n%s", diff)
		}
	})
}

// The secret marks message content that a journaled input must not keep
// (ses.md 11.2; spec acceptance: subject, bodies, attachment contents, raw
// MIME, template data).
const journalSecret = "journal-secret-content"

// The APICall of a send touches the sender's identity ARN (contract 2.6)
// and its journaled input carries no message content.
func TestSendEmailJournalTouchesSenderAndKeepsNoContent(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := email(sender, simulator)
		in.Message.Subject.Data = journalSecret
		in.Message.Body.Text.Data = journalSecret
		in.Message.Body.Html = &sesapi.Content{Data: journalSecret}
		must(h, sesapi.OpSendEmail, in)
		calls := h.apiCalls("ses", "SendEmail")
		if len(calls) != 1 {
			h.Fatalf("%d SendEmail calls journaled, want 1", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN(sender))
		wantNoSecret(h, calls[0].Input.JSON())
	})
}

func wantTouches(h *harness, touched []aws.ARN, arn string) {
	h.Helper()
	if slices.ContainsFunc(touched, func(a aws.ARN) bool { return a.String() == arn }) {
		return
	}
	h.Errorf("the call touched %v, want %s", touched, arn)
}

func wantNoSecret(h *harness, input []byte) {
	h.Helper()
	for _, form := range []string{journalSecret, base64.StdEncoding.EncodeToString([]byte(journalSecret))} {
		if strings.Contains(string(input), form) {
			h.Errorf("the journaled input keeps message content %q: %s", form, input)
		}
	}
}

// VerifyEmailIdentity records the verification email (ses.md 5.2: from
// no-reply-aws@amazon.com, subject naming the Region; contract 3.2): kind
// verification, to the identity, its text carrying the link built from
// the request's host. It counts toward no quota.
func TestVerificationEmailIsRecorded(t *testing.T) {
	run(t, func(h *harness) {
		const addr = "new@example.com"
		h.verifyEmail(addr)
		all := h.messages(ses.MessageQuery{})
		if len(all) != 1 {
			h.Fatalf("%d messages after VerifyEmailIdentity, want the verification email", len(all))
		}
		m := all[0]
		if m.Kind != ses.KindVerification || m.API != ses.APIv1 || m.Operation != "VerifyEmailIdentity" ||
			m.Source != ses.VerificationSender || !slices.Equal(m.From, []string{ses.VerificationSender}) ||
			!slices.Equal(m.To, []string{addr}) || !slices.Equal(m.Envelope, []string{addr}) {
			h.Errorf("verification record = %+v", m)
		}
		if m.Subject == nil || m.Subject.Data != fmt.Sprintf(ses.VerificationSubjectText, h.Scope.Region.String()) {
			h.Errorf("verification subject = %+v, want %q", m.Subject, fmt.Sprintf(ses.VerificationSubjectText, h.Scope.Region.String()))
		}
		link := h.Endpoint.Scheme + "://" + h.Endpoint.Host + ses.VerificationLinkPath + "?token="
		if m.Text == nil || !hasLineWithPrefix(m.Text.Data, link) {
			h.Errorf("verification text %+v has no line starting %s", m.Text, link)
		}
		if !messageIDPattern.MatchString(m.MessageID) {
			h.Errorf("verification MessageId %q is not in the send format (contract 3.1)", m.MessageID)
		}
		if got := h.quota().SentLast24Hours; got != 0 {
			h.Errorf("SentLast24Hours = %v after a verification email, want 0", got)
		}
		h.verifyDomain("example.org")
		if got := len(h.messages(ses.MessageQuery{})); got != 1 {
			h.Errorf("%d messages after VerifyDomainIdentity, want no verification email for a domain", got)
		}
	})
}

func hasLineWithPrefix(text, prefix string) bool {
	for line := range strings.Lines(text) {
		if strings.HasPrefix(strings.TrimRight(line, "\r\n"), prefix) {
			return true
		}
	}
	return false
}

// Messages filters by recipient, sender, MessageId and account and Region,
// returns oldest first and pages by cursor; ClearMessages deletes what the
// query selects and reports how many (contract 3.3).
func TestMessagesQueryAndClear(t *testing.T) {
	runVerified(t, ses.Options{ProductionAccess: true}, func(h *harness) {
		ids := make([]string, 0, 5)
		for i := range 5 {
			to := fmt.Sprintf("r%d@elsewhere.test", i)
			ids = append(ids, must(h, sesapi.OpSendEmail, email(sender, to)).MessageId)
			h.advance(1e9)
		}
		other := must(h, sesapi.OpSendEmail, email("other@"+verifiedDomain, "r0@elsewhere.test")).MessageId

		got := messageIDs(h.messages(ses.MessageQuery{To: "r0@elsewhere.test"}))
		if !slices.Equal(got, []string{ids[0], other}) {
			h.Errorf("To r0 = %q, want %q oldest first", got, []string{ids[0], other})
		}
		if got := messageIDs(h.messages(ses.MessageQuery{From: "other@" + verifiedDomain})); !slices.Equal(got, []string{other}) {
			h.Errorf("From other = %q", got)
		}
		if got := messageIDs(h.messages(ses.MessageQuery{MessageID: ids[2]})); !slices.Equal(got, []string{ids[2]}) {
			h.Errorf("MessageID = %q", got)
		}
		if got := h.messages(ses.MessageQuery{Region: westRegion(h)}); len(got) != 0 {
			h.Errorf("us-west-2 holds %d messages, want 0", len(got))
		}

		var paged []string
		q := ses.MessageQuery{Limit: 2, From: sender}
		for range 10 {
			page, err := h.svc.Messages(q)
			if err != nil {
				h.Fatal(err)
			}
			if len(page.Messages) > 2 {
				h.Fatalf("a page of Limit 2 holds %d", len(page.Messages))
			}
			paged = append(paged, messageIDs(page.Messages)...)
			if page.Next == "" {
				break
			}
			q.Cursor = page.Next
		}
		// The two configured identities were created verified, so no
		// verification email is in the store.
		if !slices.Equal(paged, ids) {
			h.Errorf("paged From sender = %q, want %q", paged, ids)
		}

		n, err := h.svc.ClearMessages(ses.MessageQuery{To: "r0@elsewhere.test", Limit: 1, Cursor: "ignored"})
		if err != nil || n != 2 {
			h.Errorf("ClearMessages(To r0) = %d, %v; want 2", n, err)
		}
		if got := messageIDs(h.messages(ses.MessageQuery{})); !slices.Equal(got, ids[1:]) {
			h.Errorf("after the clear = %q, want %q", got, ids[1:])
		}
	})
}

func messageIDs(ms []ses.Message) []string {
	out := make([]string, 0, len(ms))
	for i := range ms {
		out = append(out, ms[i].MessageID)
	}
	return out
}

// A limit outside 1..MaxMessageLimit and a cursor the store did not issue
// are QueryErrors naming the field (contract 3.3).
func TestMessagesRefusesBadLimitAndCursor(t *testing.T) {
	run(t, func(h *harness) {
		for _, c := range []struct {
			q     ses.MessageQuery
			field string
		}{
			{ses.MessageQuery{Limit: ses.MaxMessageLimit + 1}, "limit"},
			{ses.MessageQuery{Limit: -1}, "limit"},
			{ses.MessageQuery{Cursor: "not-a-cursor"}, "cursor"},
		} {
			_, err := h.svc.Messages(c.q)
			var qe *ses.QueryError
			if !errors.As(err, &qe) || qe.Field != c.field || qe.Reason == "" {
				h.Errorf("Messages(%+v) = %v, want a QueryError on %s", c.q, err, c.field)
			}
		}
	})
}

// The store keeps MaxStoredMessages per account and Region; the next
// evicts the oldest and counts it (ses.md 11.2; contract 3.3).
func TestMessageStoreEvictsTheOldest(t *testing.T) {
	runVerified(t, ses.Options{ProductionAccess: true}, func(h *harness) {
		first := must(h, sesapi.OpSendEmail, email(sender, simulator)).MessageId
		for range ses.MaxStoredMessages {
			must(h, sesapi.OpSendEmail, email(sender, simulator))
		}
		page, err := h.svc.Messages(ses.MessageQuery{Limit: 1})
		if err != nil {
			h.Fatal(err)
		}
		if page.Evicted != 1 || len(page.Messages) != 1 || page.Messages[0].MessageID == first {
			h.Errorf("after MaxStoredMessages+1 sends: evicted %d, oldest %+v; want 1 evicted and the first gone", page.Evicted, page.Messages)
		}
		if got, err := h.svc.Messages(ses.MessageQuery{MessageID: first}); err != nil || len(got.Messages) != 0 {
			h.Errorf("the evicted message is still readable: %+v, %v", got.Messages, err)
		}
	})
}
