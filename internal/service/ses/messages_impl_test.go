package ses_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// linkLine is the verification link line of m's text body.
func linkLine(h *harness, m ses.Message) string {
	h.Helper()
	prefix := h.Endpoint.String() + ses.VerificationLinkPath + "?token="
	if m.Text != nil {
		for line := range strings.Lines(m.Text.Data) {
			if l := strings.TrimRight(line, "\r\n"); strings.HasPrefix(l, prefix) {
				return l
			}
		}
	}
	h.Fatalf("message %s has no link line in %+v", m.MessageID, m.Text)
	return ""
}

// A verification email's MessageId follows switch ses.message-id.format
// (inferred, SES-07): timestamped carries "01", the Region code and the
// emulator time in Unix ms (ses.md 4.7); random does not.
func TestVerificationMessageIDFollowsItsSwitch(t *testing.T) {
	timed := func(h *harness) string {
		return "01" + ses.MessageIDRegionCode(h.Scope.Region) + fmt.Sprintf("%012x", h.Clock.Now().Wire().UnixMilli())
	}
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		m := h.messages(ses.MessageQuery{})[0]
		if got := messageIDPattern.FindStringSubmatch(m.MessageID); len(m.MessageID) != 60 || got == nil || got[1] != timed(h) {
			h.Errorf("MessageId %q, want 60 characters with prefix %s", m.MessageID, timed(h))
		}
	})
	runWith(t, ses.Options{MessageID: ses.MessageIDRandom}, func(h *harness) {
		h.verifyEmail(sender)
		m := h.messages(ses.MessageQuery{})[0]
		if got := messageIDPattern.FindStringSubmatch(m.MessageID); len(m.MessageID) != 60 || got == nil || got[1] == timed(h) {
			h.Errorf("MessageId %q under random, want 60 characters without the time prefix %s", m.MessageID, timed(h))
		}
	})
}

// A repeated VerifyEmailIdentity of a Pending identity sends another
// verification email with the same link (contract 2.3, inferred SES-09). A
// DeleteIdentity leaves the recorded messages.
func TestRepeatedVerifySendsTheSameLink(t *testing.T) {
	run(t, func(h *harness) {
		const addr = "pending@example.com"
		h.verifyEmail(addr)
		h.verifyEmail(addr)
		all := h.messages(ses.MessageQuery{})
		if len(all) != 2 {
			h.Fatalf("%d messages after two verifies, want 2", len(all))
		}
		if a, b := linkLine(h, all[0]), linkLine(h, all[1]); a != b {
			h.Errorf("the repeat changed the link: %s, then %s", a, b)
		}
		if all[0].MessageID == all[1].MessageID || all[0].RequestID == all[1].RequestID {
			h.Errorf("the two emails share a MessageId or request id: %+v", all)
		}
		must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: addr})
		if got := len(h.messages(ses.MessageQuery{})); got != 2 {
			h.Errorf("%d messages after DeleteIdentity, want the 2 kept", got)
		}
	})
}

// Messages filters by Region and Since, matches To without regard to the
// domain's case, pages oldest first across Regions, and ClearMessages
// deletes only what its query selects (contract 3.3).
func TestMessagesAcrossScopes(t *testing.T) {
	run(t, func(h *harness) {
		east := h.Scope
		h.verifyEmail("a@example.com")
		h.advance(1e9)
		since := h.Clock.Now()
		h.Scope.Region = westRegion(h)
		h.verifyEmail("b@example.com")
		h.advance(1e9)
		h.Scope = east
		h.verifyEmail("c@example.com")

		to := func(ms []ses.Message) []string {
			out := make([]string, 0, len(ms))
			for i := range ms {
				out = append(out, ms[i].To[0])
			}
			return out
		}
		if got := to(h.messages(ses.MessageQuery{})); !slices.Equal(got, []string{"a@example.com", "b@example.com", "c@example.com"}) {
			h.Errorf("all = %q, want recording order across Regions", got)
		}
		if got := to(h.messages(ses.MessageQuery{Region: westRegion(h)})); !slices.Equal(got, []string{"b@example.com"}) {
			h.Errorf("us-west-2 = %q", got)
		}
		if got := to(h.messages(ses.MessageQuery{Since: since})); !slices.Equal(got, []string{"b@example.com", "c@example.com"}) {
			h.Errorf("since = %q", got)
		}
		if got := to(h.messages(ses.MessageQuery{To: "A@EXAMPLE.COM"})); len(got) != 0 {
			h.Errorf("To with another local-part case = %q, want none", got)
		}
		if got := to(h.messages(ses.MessageQuery{To: "a@EXAMPLE.com"})); !slices.Equal(got, []string{"a@example.com"}) {
			h.Errorf("To with another domain case = %q", got)
		}
		if got := to(h.messages(ses.MessageQuery{From: "AWS <" + ses.VerificationSender + ">"})); len(got) != 3 {
			h.Errorf("From the verification sender = %q, want all 3", got)
		}

		first, err := h.svc.Messages(ses.MessageQuery{Limit: 2})
		if err != nil || len(first.Messages) != 2 || first.Next == "" {
			h.Fatalf("page 1 = %+v, %v", first, err)
		}
		second, err := h.svc.Messages(ses.MessageQuery{Limit: 2, Cursor: first.Next})
		if err != nil || to(second.Messages)[0] != "c@example.com" || second.Next != "" {
			h.Errorf("page 2 = %+v, %v; want c and no next", second, err)
		}

		n, err := h.svc.ClearMessages(ses.MessageQuery{Region: westRegion(h)})
		if err != nil || n != 1 {
			h.Errorf("ClearMessages(us-west-2) = %d, %v; want 1", n, err)
		}
		if got := to(h.messages(ses.MessageQuery{})); !slices.Equal(got, []string{"a@example.com", "c@example.com"}) {
			h.Errorf("after the clear = %q", got)
		}
	})
}

// The store keeps MaxStoredMessages per scope and counts the evicted
// (ses.md 11.2); Evicted sums only the scopes a query covers.
func TestEvictedCountIsPerScope(t *testing.T) {
	run(t, func(h *harness) {
		for range ses.MaxStoredMessages + 2 {
			h.verifyEmail(sender)
		}
		page, err := h.svc.Messages(ses.MessageQuery{Limit: 1})
		if err != nil || page.Evicted != 2 {
			h.Errorf("Evicted = %d, %v; want 2", page.Evicted, err)
		}
		if got := len(h.messages(ses.MessageQuery{})); got != ses.MaxMessageLimit {
			h.Errorf("a full page holds %d", got)
		}
		west, err := h.svc.Messages(ses.MessageQuery{Region: westRegion(h)})
		if err != nil || west.Evicted != 0 {
			h.Errorf("us-west-2 Evicted = %d, %v; want 0", west.Evicted, err)
		}
	})
}

// Export, Reset, Import gives back the messages, and one recorded after the
// load reads after them (contract 1.3); a reset through the v2 view clears
// the store both APIs share (spec "Scope").
func TestMessagesSnapshotAndV2Reset(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("a@example.com")
		before := messageIDs(h.messages(ses.MessageQuery{}))
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.messages(ses.MessageQuery{}); len(got) != 0 {
			h.Fatalf("after Reset %d messages remain", len(got))
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.verifyEmail("b@example.com")
		got := messageIDs(h.messages(ses.MessageQuery{}))
		if len(got) != 2 || got[0] != before[0] {
			h.Errorf("after the load = %q, want %s first", got, before[0])
		}
		h.v2.Reset(kernel.ResetScope{})
		if got := h.messages(ses.MessageQuery{}); len(got) != 0 {
			h.Errorf("after the sesv2 reset %d messages remain", len(got))
		}
	})
}
