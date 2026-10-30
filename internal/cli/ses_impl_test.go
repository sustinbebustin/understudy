package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// sesCall sends a signed awsQuery SES v1 call, as the AWS CLI would, and
// fails the test unless it answers 200.
func sesCall(t *testing.T, endpoint string, form url.Values) {
	t.Helper()
	form.Set("Version", "2010-12-01")
	body := form.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "ses"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := io.ReadAll(resp.Body)
	if err := errors.Join(err, resp.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ses %s = %d: %s", form.Get("Action"), resp.StatusCode, answer)
	}
}

// serveSES runs serve with sender@example.com verified by configuration,
// so a send from it to the mailbox simulator is accepted in the sandbox.
func serveSES(t *testing.T) *served {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "understudy.json")
	if err := os.WriteFile(cfg, []byte(`{"ses": {"verifiedIdentities": ["sender@example.com"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := serve(t, "--config", cfg, "--clock", "frozen@2026-01-01T00:00:00Z")
	t.Cleanup(func() { s.stop() })
	// The configured list applies when the identity is created.
	sesCall(t, s.endpoint, url.Values{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {"sender@example.com"}})
	return s
}

func sendSES(t *testing.T, endpoint, subject string) {
	t.Helper()
	sesCall(t, endpoint, url.Values{
		"Action":                           {"SendEmail"},
		"Source":                           {"sender@example.com"},
		"Destination.ToAddresses.member.1": {"success@simulator.amazonses.com"},
		"Message.Subject.Data":             {subject},
		"Message.Body.Text.Data":           {"body"},
	})
}

// A recorded send is listed by 'ses messages' in text, one row naming
// the operation, sender, recipient and subject, and in JSON as the admin
// API's page.
func TestSESMessagesListsARecordedSend(t *testing.T) {
	s := serveSES(t)
	sendSES(t, s.endpoint, "Hello")

	text := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--to", "success@simulator.amazonses.com")
	if text.code != admin.ExitOK {
		t.Fatalf("ses messages = %d, stderr %q", text.code, text.stderr)
	}
	lines := strings.Split(strings.TrimSpace(text.stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ACCEPTED") {
		t.Fatalf("ses messages printed %q, want a header and one row", text.stdout)
	}
	for _, want := range []string{"2026-01-01T00:00:00Z", "send", "ses.SendEmail", "sender@example.com", "success@simulator.amazonses.com", "Hello"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q lacks %q", lines[1], want)
		}
	}

	js := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--from", "sender@example.com", "--output", "json")
	var page admin.SESMessagePage
	if js.code != admin.ExitOK || json.Unmarshal([]byte(js.stdout), &page) != nil {
		t.Fatalf("ses messages --output json = %d, %q", js.code, js.stdout)
	}
	if len(page.Messages) != 1 || page.Messages[0].Subject == nil || page.Messages[0].Subject.Data != "Hello" || page.Messages[0].Text == nil || page.Messages[0].Text.Data != "body" {
		t.Errorf("ses messages --output json = %+v, want the one send with its subject and text", page)
	}

	none := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--from", "other@example.com")
	if none.code != admin.ExitOK || strings.TrimSpace(none.stdout) != "no messages" {
		t.Errorf("ses messages --from other = %d, %q; want no messages", none.code, none.stdout)
	}
}

// --limit reads across server pages and stops at the limit, keeping the
// oldest, and says on stderr that more match; --since is RFC 3339.
func TestSESMessagesLimitAndSince(t *testing.T) {
	s := serveSES(t)
	for _, subject := range []string{"one", "two", "three"} {
		sendSES(t, s.endpoint, subject)
	}
	r := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--from", "sender@example.com", "--limit", "2", "--since", "2026-01-01T00:00:00Z", "--output", "json")
	var page admin.SESMessagePage
	if r.code != admin.ExitOK || json.Unmarshal([]byte(r.stdout), &page) != nil {
		t.Fatalf("ses messages --limit 2 = %d, %q", r.code, r.stdout)
	}
	if len(page.Messages) != 2 || page.Messages[0].Subject.Data != "one" || page.Messages[1].Subject.Data != "two" || page.Next == "" {
		t.Errorf("ses messages --limit 2 = %+v, want one and two with a next cursor", page)
	}
	text := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--from", "sender@example.com", "--limit", "2")
	if text.code != admin.ExitOK || !strings.Contains(text.stderr, "showing the oldest 2 matching messages") {
		t.Errorf("ses messages --limit 2 = %d, stderr %q; want the more-match note", text.code, text.stderr)
	}
	later := runCLI(t.Context(), s.endpoint, nil, "ses", "messages", "--since", "2026-01-02T00:00:00Z")
	if later.code != admin.ExitOK || strings.TrimSpace(later.stdout) != "no messages" {
		t.Errorf("ses messages --since a day later = %d, %q; want no messages", later.code, later.stdout)
	}
}

// Bad values are usage errors naming the flag and its form.
func TestSESMessagesRefusesBadFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"ses", "messages", "--since", "10m"}, `--since "10m" is not an RFC 3339 time`},
		{[]string{"ses", "messages", "--limit", "0"}, "--limit must be positive"},
		{[]string{"ses", "messages", "extra"}, "no arguments"},
	}
	for _, c := range cases {
		r := runCLI(t.Context(), closedEndpoint(t), nil, c.args...)
		if r.code != admin.ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%q = %d, stderr %q; want 2 and %q", c.args, r.code, r.stderr, c.want)
		}
	}
}
