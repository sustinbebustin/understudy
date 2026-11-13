package ses_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// The recorded tag texts, all InvalidParameterValue, Type Sender, 400, from
// v1 SendEmail with an unverified Source, so tag validation runs before the
// sender check (third-party-recorded 2024-07-30, ses.md 4.5;
// localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L592-L735,
// validated 2024-07-30). The doubled space after '_', is in every
// recording.
func TestInvalidTagsSendEmailGoldens(t *testing.T) {
	const (
		nameText  = "Invalid tag name <%s>: only alphanumeric ASCII characters, '_',  '-' are allowed."
		valueText = "Invalid tag value <%s>: only alphanumeric ASCII characters, '_',  '-' , '.', '@' are allowed."
	)
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name, tagName, tagValue, want string
	}{
		{"invalid name", "test_invalid_name:123", "test", fmt.Sprintf(nameText, "test_invalid_name:123")},
		{"invalid value", "test", "test_invalid_value:123", fmt.Sprintf(valueText, "test_invalid_value:123")},
		{"both invalid", "test_invalid_name:123", "test_invalid_value:123", fmt.Sprintf(nameText, "test_invalid_name:123")},
		{"long name", long(2100), "test", "Tag name cannot exceed 255 characters."},
		{"long value", "test", long(2200), "Tag value cannot exceed 255 characters."},
		{"invalid name, long value", "test_invalid_name@123", long(2200), fmt.Sprintf(nameText, "test_invalid_name@123")},
		{"empty name and value", "", "", "The tag name must be specified."},
		{"empty name", "", "test", "The tag name must be specified."},
		{"empty value", "test", "", "The tag value must be specified."},
	}
	run(t, func(h *harness) {
		for _, c := range cases {
			in := email("user-unverified@example.com", "success@example.com")
			in.Tags = []sesapi.MessageTag{{Name: c.tagName, Value: c.tagValue}}
			_, err := call(h, sesapi.OpSendEmail, in)
			if err == nil || err.ID.QueryCode != "InvalidParameterValue" || err.ID.Status != 400 || err.Message != c.want {
				h.Errorf("%s: error = %+v, want InvalidParameterValue 400 %q (ses.md 4.5)", c.name, err, c.want)
			}
		}
	})
}

// The two feedback-id tag names pass tag validation and the call fails on
// the unverified sender (third-party-recorded 2024-07-30, ses.md 4.5;
// localstack@8b9a79f tests/aws/services/ses/test_ses.py:L925-L953, validated
// 2024-07-30). Which other "ses:" names pass is UNVERIFIED (SES-16): the
// names come from FeedbackIDTagNames, and another ses: name fails tag
// validation.
func TestSpecialTagsSendEmail(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range ses.FeedbackIDTagNames() {
			in := email("user-unverified@example.com", "success@example.com")
			in.Tags = []sesapi.MessageTag{{Name: name, Value: "value"}}
			_, err := call(h, sesapi.OpSendEmail, in)
			if err == nil || err.ID.QueryCode != "MessageRejected" {
				h.Errorf("tag %s: error = %+v, want MessageRejected (ses.md 4.5)", name, err)
			}
		}
		in := email("user-unverified@example.com", "success@example.com")
		in.Tags = []sesapi.MessageTag{{Name: "ses:caller-identity", Value: "value"}}
		_, err := call(h, sesapi.OpSendEmail, in)
		wantCode(h, err, sesapi.IdentityInvalidParameterValue())
	})
}

// The tag length boundary is UNVERIFIED (SES-16) and read from
// TagMaxLength: a name or value of TagMaxLength characters passes, one more
// fails with the recorded text (ses.md 4.5).
func TestTagLengthBoundary(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		at, over := strings.Repeat("n", ses.TagMaxLength), strings.Repeat("n", ses.TagMaxLength+1)
		in := email(sender, simulator)
		in.Tags = []sesapi.MessageTag{{Name: at, Value: at}}
		must(h, sesapi.OpSendEmail, in)
		in.Tags = []sesapi.MessageTag{{Name: over, Value: "v"}}
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Tag name cannot exceed 255 characters.")
		in.Tags = []sesapi.MessageTag{{Name: "n", Value: over}}
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Tag value cannot exceed 255 characters.")
	})
}

// Switch ses.error.unverified.text (SES-01, SES-03): the MessageRejected
// text (aws-doc template, upper-case Region, recorded SES-01) lists the
// failing identities as written, display names kept (SES-01 step 02).
// By default they are in Java HashSet iteration order, which reproduces
// both recorded lists (ses.md 6.1): v1 Source, ReturnPath, To and Cc as
// "rp, r1, nobody, r2" (SES-03 step 02), v2 From and To as "r1, nobody"
// (step 03). Flipped, senders or recipients come first in request order.
func TestUnverifiedTextFollowsItsSwitch(t *testing.T) {
	const (
		nobody = "us1103-nobody@example.com"
		rp     = "us1103-rp@example.com"
		r1     = "us1103-r1@example.com"
		r2     = "us1103-r2@example.com"
	)
	both := func() *sesapi.SendEmailInput {
		in := email(nobody, r1)
		in.Destination.CcAddresses = []string{r2}
		in.ReturnPath = new(rp)
		return in
	}
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesapi.OpSendEmail, both())
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, rp, r1, nobody, r2))
		_, err = call(h, sesv2api.OpSendEmail, simpleEmail(nobody, r1))
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, r1, nobody))
		_, err = call(h, sesapi.OpSendEmail, email("Nobody <"+nobody+">", recipient))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "Nobody <"+nobody+">"))
	})
	runVerified(t, ses.Options{UnverifiedText: ses.UnverifiedSendersFirst}, func(h *harness) {
		_, err := call(h, sesapi.OpSendEmail, both())
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, nobody, rp, r1, r2))
	})
	runVerified(t, ses.Options{UnverifiedText: ses.UnverifiedRecipientsFirst}, func(h *harness) {
		_, err := call(h, sesapi.OpSendEmail, both())
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, r1, r2, nobody, rp))
	})
}

// ReturnPath must itself be verified (model, SendEmailRequest.ReturnPath;
// ses.md 4.1); under the default of ses.error.unverified.text it is listed
// after Source.
func TestReturnPathMustBeVerified(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := email(sender, recipient)
		in.ReturnPath = new("bounces@unverified.test")
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "bounces@unverified.test"))
		in.ReturnPath = new("bounces@" + verifiedDomain)
		must(h, sesapi.OpSendEmail, in)
	})
}

// The identity rules (aws-doc creating-identities, ses.md 5.1): a verified
// address may send with a +label; addresses are case-sensitive; a verified
// domain covers every address in it and its subdomains, case-insensitively.
func TestSenderIdentityRules(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for _, from := range []string{
			sender,
			"sender+myLabel@example.com",
			"anyone@" + verifiedDomain,
			"anyone@VERIFIED.test",
			"anyone@mail." + verifiedDomain,
		} {
			if _, err := call(h, sesapi.OpSendEmail, email(from, simulator)); err != nil {
				h.Errorf("Source %s: %s %q, want accepted (ses.md 5.1)", from, err.ID.QueryCode, err.Message)
			}
		}
		for _, from := range []string{"Sender@example.com", "sender@EXAMPLE.com", "other@example.com", "anyone@notverified.test"} {
			_, err := call(h, sesapi.OpSendEmail, email(from, simulator))
			wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, from))
		}
	})
}

// In the sandbox only verified recipients or mailbox simulator addresses
// may receive (aws-doc request-production-access, ses.md 6.1; simulator
// addresses and their labels, ses.md 6.2). Recipient labels are
// SandboxRecipientLabels (UNVERIFIED, SES-19). Production access lifts the
// recipient check but not the sender's (ses.md 6.1).
func TestSandboxRecipients(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for _, scenario := range ses.Scenarios() {
			for _, to := range []string{string(scenario) + "@" + ses.SimulatorDomain, string(scenario) + "+label1@" + ses.SimulatorDomain} {
				if _, err := call(h, sesapi.OpSendEmail, email(sender, to)); err != nil {
					h.Errorf("simulator recipient %s: %s %q, want accepted (ses.md 6.2)", to, err.ID.QueryCode, err.Message)
				}
			}
		}
		must(h, sesapi.OpSendEmail, email(sender, recipient, sender))
		_, err := call(h, sesapi.OpSendEmail, email(sender, "stranger@unverified.test"))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "stranger@unverified.test"))
		_, err = call(h, sesapi.OpSendEmail, email(sender, "sender+label@example.com"))
		if ses.SandboxRecipientLabels != (err == nil) {
			h.Errorf("recipient sender+label@example.com with SandboxRecipientLabels=%v: error %+v", ses.SandboxRecipientLabels, err)
		}
	})
	runVerified(t, ses.Options{ProductionAccess: ses.ProductionAccess{All: true}}, func(h *harness) {
		must(h, sesapi.OpSendEmail, email(sender, "stranger@unverified.test"))
		_, err := call(h, sesapi.OpSendEmail, email("stranger@unverified.test", recipient))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "stranger@unverified.test"))
	})
}

// At least one and at most 50 recipients across To, Cc and Bcc (model
// documentation of SendEmail, ses.md 4.1); the answers are inferred
// (SES-05, SES-06) and read from the constants.
func TestRecipientCount(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		to := make([]string, ses.MaxRecipients+1)
		for i := range to {
			to[i] = fmt.Sprintf("success+%d@%s", i, ses.SimulatorDomain)
		}
		in := email(sender, to[:20]...)
		in.Destination.CcAddresses = to[20:40]
		in.Destination.BccAddresses = to[40:ses.MaxRecipients]
		must(h, sesapi.OpSendEmail, in)
		in.Destination.BccAddresses = to[40:]
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.TooManyRecipientsText, ses.MaxRecipients))
		_, err = call(h, sesapi.OpSendEmail, email(sender))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.NoRecipientsText)
	})
}

// An invalid recipient rejects the whole message, even with valid ones
// (model documentation of SendEmail, ses.md 4.1). The texts are inferred
// (SES-04) and read from the constants, in their precedence.
func TestInvalidAddressRejectsTheMessage(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for addr, text := range map[string]string{
			"no-at-sign":             ses.AddressMissingDomainText,
			"has space@example.com":  ses.AddressWhitespaceText,
			"trailing@" + "example.": ses.AddressTrailingDotText,
		} {
			_, err := call(h, sesapi.OpSendEmail, email(sender, simulator, addr))
			wantErr(h, err, sesapi.IdentityInvalidParameterValue(), text)
		}
		if got := len(h.sends()); got != 0 {
			h.Errorf("%d messages recorded from rejected sends", got)
		}
	})
}

// The message size limit is 10 MB (model documentation of SendEmail, ses.md
// 4.1); the exact bound and answer are inferred (SES-29) and read from the
// constants.
func TestSendEmailMessageTooLong(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := email(sender, simulator)
		in.Message.Subject.Data = strings.Repeat("s", ses.MaxV1MessageBytes+1)
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityMessageRejected(), fmt.Sprintf(ses.MessageTooLongText, ses.MaxV1MessageBytes))
	})
}

// ConfigurationSetName and the sending-authorization ARNs are not
// implemented and say so, naming the member (invariant 3).
func TestSendEmailNotImplementedMembers(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for member, set := range map[string]func(*sesapi.SendEmailInput){
			"ConfigurationSetName": func(in *sesapi.SendEmailInput) { in.ConfigurationSetName = new("set") },
			"SourceArn":            func(in *sesapi.SendEmailInput) { in.SourceArn = new(h.identityARN(sender)) },
			"ReturnPathArn":        func(in *sesapi.SendEmailInput) { in.ReturnPathArn = new(h.identityARN(sender)) },
		} {
			in := email(sender, simulator)
			set(in)
			_, err := call(h, sesapi.OpSendEmail, in)
			wantNotImplemented(h, err, member)
		}
	})
}

// sendFailure is one way a send fails, attributed to the check that
// catches it.
type sendFailure struct {
	check ses.SendCheck
	apply func(in *sesapi.SendEmailInput)
	want  func(t *rapid.T, h *harness, err *awserr.Error)
}

// The check order is SendChecks (UNVERIFIED beyond tags before the sender,
// ses.md 6.4; ses.send.check-order, SES-17): whatever set of failures a
// send carries, the first of SendChecks that fails answers.
func TestSendChecksRunInOrder(t *testing.T) {
	const badSender, badRecipient = "stranger@unverified.test", "someone@unverified.test"
	failures := []sendFailure{
		{
			ses.CheckNotImplemented, func(in *sesapi.SendEmailInput) { in.ConfigurationSetName = new("set") },
			func(t *rapid.T, _ *harness, err *awserr.Error) { wantNotImplemented(t, err, "ConfigurationSetName") },
		},
		{
			ses.CheckTags, func(in *sesapi.SendEmailInput) { in.Tags = []sesapi.MessageTag{{Name: "bad:name", Value: "v"}} },
			func(t *rapid.T, _ *harness, err *awserr.Error) {
				wantErr(t, err, sesapi.IdentityInvalidParameterValue(), "Invalid tag name <bad:name>: only alphanumeric ASCII characters, '_',  '-' are allowed.")
			},
		},
		{ses.CheckAddresses, func(in *sesapi.SendEmailInput) {
			in.Destination.ToAddresses = append(in.Destination.ToAddresses, "no-at-sign")
		}, func(t *rapid.T, _ *harness, err *awserr.Error) {
			wantErr(t, err, sesapi.IdentityInvalidParameterValue(), ses.AddressMissingDomainText)
		}},
		{ses.CheckRecipientCount, func(in *sesapi.SendEmailInput) {
			for i := range ses.MaxRecipients {
				in.Destination.CcAddresses = append(in.Destination.CcAddresses, fmt.Sprintf("success+%d@%s", i, ses.SimulatorDomain))
			}
		}, func(t *rapid.T, _ *harness, err *awserr.Error) {
			wantErr(t, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.TooManyRecipientsText, ses.MaxRecipients))
		}},
		{
			ses.CheckSender, func(in *sesapi.SendEmailInput) { in.Source = badSender },
			func(t *rapid.T, h *harness, err *awserr.Error) {
				wantCode(t, err, sesapi.IdentityMessageRejected())
				if !strings.HasPrefix(err.Message, unverifiedText(h.Scope.Region)) || !strings.Contains(err.Message, badSender) {
					t.Fatalf("sender check text %q does not name %s", err.Message, badSender)
				}
			},
		},
		{ses.CheckSandboxRecipients, func(in *sesapi.SendEmailInput) {
			in.Destination.BccAddresses = append(in.Destination.BccAddresses, badRecipient)
		}, func(t *rapid.T, h *harness, err *awserr.Error) {
			wantCode(t, err, sesapi.IdentityMessageRejected())
			if !strings.HasPrefix(err.Message, unverifiedText(h.Scope.Region)) || !strings.Contains(err.Message, badRecipient) {
				t.Fatalf("sandbox check text %q does not name %s", err.Message, badRecipient)
			}
		}},
	}
	order := ses.SendChecks()
	rank := func(c ses.SendCheck) int {
		i := slices.Index(order, c)
		if i < 0 {
			t.Fatalf("SendChecks %v omits check %d", order, c)
		}
		return i
	}
	runVerified(t, ses.Options{}, func(h *harness) {
		// h.K, not h.T: rapid asks a *testing.T for its deadline, which a
		// synctest bubble refuses; any other TB gets rapid's default.
		rapid.Check(h.K, func(rt *rapid.T) {
			picked := rapid.SliceOfNDistinct(rapid.IntRange(0, len(failures)-1), 1, len(failures), rapid.ID[int]).Draw(rt, "failures")
			in := email(sender, recipient)
			first := failures[picked[0]]
			for _, i := range picked {
				failures[i].apply(in)
				if rank(failures[i].check) < rank(first.check) {
					first = failures[i]
				}
			}
			_, err := call(h, sesapi.OpSendEmail, in)
			first.want(rt, h, err)
		})
		if got := len(h.sends()); got != 0 {
			h.Errorf("%d messages recorded from failing sends", got)
		}
	})
}

var messageIDPattern = regexp.MustCompile(`^([0-9a-f]{16})-([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})-000000$`)

// Switch ses.message-id.format (inferred, SES-07): by default a MessageId
// is "01", the Region code, 12 hex digits of the emulator time in Unix ms,
// a version-4 UUID and "-000000", 60 characters (capture and model
// examples, ses.md 4.7); random replaces the prefix.
func TestMessageIDFollowsItsSwitch(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		id := must(h, sesapi.OpSendEmail, email(sender, simulator)).MessageId
		m := messageIDPattern.FindStringSubmatch(id)
		if len(id) != 60 || m == nil {
			h.Fatalf("MessageId %q is not 16 hex, a v4 UUID and -000000 in 60 characters (ses.md 4.7)", id)
		}
		want := "01" + ses.MessageIDRegionCode(h.Scope.Region) + fmt.Sprintf("%012x", h.Clock.Now().Wire().UnixMilli())
		if m[1] != want {
			h.Errorf("switch ses.message-id.format=%s: prefix %s, want %s", ses.MessageIDTimestamped, m[1], want)
		}
	})
	runVerified(t, ses.Options{MessageID: ses.MessageIDRandom}, func(h *harness) {
		id := must(h, sesapi.OpSendEmail, email(sender, simulator)).MessageId
		m := messageIDPattern.FindStringSubmatch(id)
		if len(id) != 60 || m == nil {
			h.Fatalf("MessageId %q is not 16 hex, a v4 UUID and -000000 in 60 characters", id)
		}
		timed := "01" + ses.MessageIDRegionCode(h.Scope.Region) + fmt.Sprintf("%012x", h.Clock.Now().Wire().UnixMilli())
		if m[1] == timed {
			h.Errorf("switch ses.message-id.format=%s: prefix %s still carries the time", ses.MessageIDRandom, m[1])
		}
	})
}

// The Region codes captured in real MessageIds (capture, ses.md 4.7).
func TestMessageIDRegionCodes(t *testing.T) {
	for name, want := range map[string]string{"us-east-1": "00", "us-west-2": "01", "eu-west-1": "02"} {
		if got := ses.MessageIDRegionCode(parseRegion(t, name)); got != want {
			t.Errorf("MessageIDRegionCode(%s) = %q, want %q (ses.md 4.7)", name, got, want)
		}
	}
}

// Under one seed and a frozen clock two runs issue the same MessageIds
// (ADR 0018).
func TestSeededRunsRepeatMessageIDs(t *testing.T) {
	var runs [2][]string
	for i := range runs {
		runVerified(t, ses.Options{}, func(h *harness) {
			for range 3 {
				runs[i] = append(runs[i], must(h, sesapi.OpSendEmail, email(sender, simulator)).MessageId)
			}
		}, kerneltest.Seed(7))
	}
	if !slices.Equal(runs[0], runs[1]) || len(runs[0]) != 3 {
		t.Errorf("seeded runs issued %q then %q", runs[0], runs[1])
	}
}

// A fresh sandbox account: Max24HourSend 200, MaxSendRate 1 (golden
// get-quota-0, third-party-recorded 2024-11-27, ses.md 6.3;
// localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L905-L918,
// validated 2024-11-27). Production values are UNVERIFIED (SES-26) and read
// from the constants.
func TestGetSendQuota(t *testing.T) {
	run(t, func(h *harness) {
		q := h.quota()
		if q.Max24HourSend != 200.0 || q.MaxSendRate != 1.0 || q.SentLast24Hours != 0 {
			h.Errorf("GetSendQuota = %+v, want 200, 1 and 0 sent (get-quota-0)", q)
		}
	})
	runWith(t, ses.Options{ProductionAccess: ses.ProductionAccess{All: true}}, func(h *harness) {
		q := h.quota()
		if q.Max24HourSend != ses.ProductionMax24HourSend || q.MaxSendRate != ses.ProductionMaxSendRate {
			h.Errorf("production GetSendQuota = %+v, want ProductionMax24HourSend and ProductionMaxSendRate", q)
		}
	})
}

// Quotas count recipients over the last 24 hours (aws-doc quotas, ses.md
// 6.3); simulator recipients are not counted (aws-doc, ses.md 6.2). A send
// past Max24HourSend fails Throttling with DailyQuotaText (Code inferred
// from the overlay, text documented, ses.md 6.3; SES-22). Clearing the
// message store leaves the counter; a reset zeroes it.
func TestDailyQuotaCountsRecipients(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for batch := range int(ses.SandboxMax24HourSend) / ses.MaxRecipients {
			to := make([]string, ses.MaxRecipients)
			for i := range to {
				to[i] = fmt.Sprintf("u%d-%d@%s", batch, i, verifiedDomain)
			}
			must(h, sesapi.OpSendEmail, email(sender, to...))
		}
		must(h, sesapi.OpSendEmail, email(sender, simulator, "bounce@"+ses.SimulatorDomain))
		if got := h.quota().SentLast24Hours; got != ses.SandboxMax24HourSend {
			h.Errorf("SentLast24Hours = %v, want %v counted recipients", got, ses.SandboxMax24HourSend)
		}
		_, err := call(h, sesapi.OpSendEmail, email(sender, recipient))
		wantErr(h, err, sesapi.IdentityThrottling(), ses.DailyQuotaText)
		must(h, sesapi.OpSendEmail, email(sender, simulator))

		if _, err := h.svc.ClearMessages(ses.MessageQuery{}); err != nil {
			h.Fatal(err)
		}
		if got := h.quota().SentLast24Hours; got != ses.SandboxMax24HourSend {
			h.Errorf("SentLast24Hours = %v after ClearMessages, want it unchanged", got)
		}
		h.advance(24*time.Hour + time.Minute) // the 24-hour window (ses.md 6.3)
		if got := h.quota().SentLast24Hours; got != 0 {
			h.Errorf("SentLast24Hours = %v a day later, want 0", got)
		}
		must(h, sesapi.OpSendEmail, email(sender, recipient))
	})
}
