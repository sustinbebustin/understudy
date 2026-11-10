package server_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// sesCall sends one signed awsQuery SES v1 request as the default
// account's root and returns the status and body.
func sesCall(t *testing.T, endpoint string, form url.Values) (status int, body string) {
	t.Helper()
	form.Set("Version", "2010-12-01")
	encoded := form.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, []byte(encoded), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "ses"}, time.Now())
	return send(t, req)
}

// The verification email VerifyEmailIdentity records is readable through
// GET /ses/messages with admin.Client.SESMessages, its link built from the
// address the client used, and DELETE /ses/messages clears what its filters
// select (ses.md 11.2).
func TestSESMessagesThroughTheAdminAPI(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	for _, addr := range []string{"a@example.com", "b@example.com"} {
		if status, body := sesCall(t, endpoint, url.Values{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {addr}}); status != http.StatusOK {
			t.Fatalf("VerifyEmailIdentity(%s) = %d %s", addr, status, body)
		}
	}

	page, err := c.SESMessages(t.Context(), admin.SESMessageQuery{To: "a@example.com", Region: "us-east-1", Account: "000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Next != "" || page.Evicted != 0 {
		t.Fatalf("SESMessages(To a) = %+v, want one message", page)
	}
	m := page.Messages[0]
	if m.Kind != string(ses.KindVerification) || m.API != "ses" || m.Operation != "VerifyEmailIdentity" ||
		m.Account != "000000000000" || m.Region != "us-east-1" || m.From[0] != ses.VerificationSender || m.RequestID == "" {
		t.Errorf("message = %+v", m)
	}
	if m.Text == nil || !strings.Contains(m.Text.Data, "\n"+endpoint+ses.VerificationLinkPath+"?token=") {
		t.Errorf("text %+v carries no link on %s", m.Text, endpoint)
	}

	first, err := c.SESMessages(t.Context(), admin.SESMessageQuery{Limit: 1})
	if err != nil || len(first.Messages) != 1 || first.Next == "" {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	second, err := c.SESMessages(t.Context(), admin.SESMessageQuery{Limit: 1, Cursor: first.Next})
	if err != nil || len(second.Messages) != 1 || second.Messages[0].To[0] != "b@example.com" {
		t.Fatalf("page 2 = %+v, %v", second, err)
	}

	res, err := c.ClearSESMessages(t.Context(), admin.SESMessageQuery{To: "a@example.com"})
	if err != nil || res.Deleted != 1 {
		t.Fatalf("ClearSESMessages(To a) = %+v, %v; want 1", res, err)
	}
	left, err := c.SESMessages(t.Context(), admin.SESMessageQuery{})
	if err != nil || len(left.Messages) != 1 || left.Messages[0].To[0] != "b@example.com" {
		t.Errorf("after the clear = %+v, %v", left, err)
	}
}

// A refused filter is invalid_request naming its field, and
// DELETE refuses the paging parameters.
func TestSESMessagesRefusesBadFilters(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	for _, tc := range []struct {
		q     admin.SESMessageQuery
		field string
	}{
		{admin.SESMessageQuery{Account: "12"}, admin.ParamSESAccount},
		{admin.SESMessageQuery{Region: "moon-1"}, admin.ParamSESRegion},
		{admin.SESMessageQuery{Limit: ses.MaxMessageLimit + 1}, admin.ParamLimit},
		{admin.SESMessageQuery{Limit: -1}, admin.ParamLimit},
		{admin.SESMessageQuery{Cursor: "not-a-cursor"}, admin.ParamCursor},
	} {
		_, err := c.SESMessages(t.Context(), tc.q)
		var e *admin.Error
		if !errors.As(err, &e) || e.Code != admin.InvalidRequest || e.Field != tc.field {
			t.Errorf("SESMessages(%+v) = %v, want invalid_request on %s", tc.q, err, tc.field)
		}
	}
	for _, raw := range []string{admin.ParamSESSince + "=yesterday", admin.ParamLimit + "=abc"} {
		status, body := get(t, endpoint+admin.Prefix+"/ses/messages?"+raw)
		if status != http.StatusBadRequest || !strings.Contains(body, `"invalid_request"`) {
			t.Errorf("GET ?%s = %d %s, want 400 invalid_request", raw, status, body)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, endpoint+admin.Prefix+"/ses/messages?"+admin.ParamLimit+"=1", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := send(t, req); status != http.StatusBadRequest {
		t.Errorf("DELETE ?limit=1 = %d %s, want 400", status, body)
	}
}
