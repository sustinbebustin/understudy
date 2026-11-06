package ses_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// Clearing the recorded messages, as DELETE /_understudy/v1/ses/messages
// does, leaves the recipient counter alone: the quota counts what was
// accepted, not what the store still holds (ses.md 11.8).
func TestClearMessagesLeavesSentLast24Hours(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesapi.OpSendEmail, email(sender, recipient))
		before := h.quota().SentLast24Hours
		if before != 1 {
			h.Fatalf("SentLast24Hours = %v after one counted send, want 1", before)
		}
		if n, err := h.svc.ClearMessages(ses.MessageQuery{}); err != nil || n == 0 {
			h.Fatalf("ClearMessages = %d, %v", n, err)
		}
		if got := h.sends(); len(got) != 0 {
			h.Errorf("sends after the clear = %d, want none", len(got))
		}
		if got := h.quota().SentLast24Hours; got != before {
			h.Errorf("SentLast24Hours = %v after the clear, want %v", got, before)
		}
	})
}
