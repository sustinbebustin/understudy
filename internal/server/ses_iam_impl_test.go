package server_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// The sender the SES tests send from, verified through
// ses.verifiedIdentities, and the identity ARN a send touches (ses.md
// 11.2).
const (
	sesSender      = "sender@example.com"
	sesSenderARN   = "arn:aws:ses:us-east-1:000000000000:identity/" + sesSender
	sesV1Version   = "2010-12-01"
	sesV2SendPath  = "/v2/email/outbound-emails"
	sesSimulatorTo = "success@simulator.amazonses.com"
)

func sesConfig(mode server.IAMEnforce) server.Config {
	return server.Config{IAMEnforce: mode, SESVerifiedIdentities: server.SESVerifiedIdentities{Addresses: []string{sesSender}}}
}

// sesSendEmail sends a v1 SendEmail from sesSender to the simulator.
func sesSendEmail(t *testing.T, s signer, endpoint string) wire {
	t.Helper()
	return queryCall(t, s, endpoint, "ses", url.Values{
		"Action": {"SendEmail"}, "Version": {sesV1Version}, "Source": {sesSender},
		"Destination.ToAddresses.member.1": {sesSimulatorTo},
		"Message.Subject.Data":             {"s"}, "Message.Body.Text.Data": {"b"},
	})
}

// sesV2Send sends v2 SendEmail with body over restJson1.
func sesV2Send(t *testing.T, s signer, endpoint, body string) wire {
	t.Helper()
	req := newRequest(t, http.MethodPost, endpoint+sesV2SendPath, body)
	req.Header.Set("Content-Type", "application/json")
	return signedCall(t, s, "ses", req, body)
}

const sesV2SimpleBody = `{"FromEmailAddress":"` + sesSender + `","Destination":{"ToAddresses":["` + sesSimulatorTo + `"]},` +
	`"Content":{"Simple":{"Subject":{"Data":"s"},"Body":{"Text":{"Data":"b"}}}}}`

// GET /iam/policy after a send workload lists each send's action on the
// sender's identity ARN, the resource every send declares:
// ses:SendEmail for v1 and v2 SendEmail and
// ses:SendRawEmail for v1 SendRawEmail, beside ses:VerifyEmailIdentity on
// the identity it verified.
func TestSESSendWorkloadIAMPolicy(t *testing.T) {
	c, endpoint := adminServer(t, sesConfig(server.IAMEnforceOff))
	if w := queryCall(t, dummy, endpoint, "ses", url.Values{"Action": {"VerifyEmailIdentity"}, "Version": {sesV1Version}, "EmailAddress": {sesSender}}); w.status != http.StatusOK {
		t.Fatalf("VerifyEmailIdentity = %d %s", w.status, w.body)
	}
	if w := sesSendEmail(t, dummy, endpoint); w.status != http.StatusOK {
		t.Fatalf("SendEmail = %d %s", w.status, w.body)
	}
	raw := base64.StdEncoding.EncodeToString([]byte("From: " + sesSender + "\r\nTo: " + sesSimulatorTo + "\r\nSubject: s\r\n\r\nb\r\n"))
	if w := queryCall(t, dummy, endpoint, "ses", url.Values{"Action": {"SendRawEmail"}, "Version": {sesV1Version}, "RawMessage.Data": {raw}}); w.status != http.StatusOK {
		t.Fatalf("SendRawEmail = %d %s", w.status, w.body)
	}
	if w := sesV2Send(t, dummy, endpoint, sesV2SimpleBody); w.status != http.StatusOK {
		t.Fatalf("v2 SendEmail = %d %s", w.status, w.body)
	}

	policy, err := c.IAMPolicy(t.Context(), "arn:aws:iam::000000000000:root")
	if err != nil {
		t.Fatal(err)
	}
	got := sesPolicyPairs(t, policy.Document)
	for _, want := range []string{
		"ses:VerifyEmailIdentity " + sesSenderARN,
		"ses:SendEmail " + sesSenderARN,
		"ses:SendRawEmail " + sesSenderARN,
	} {
		if !slices.Contains(got, want) {
			t.Errorf("policy lacks %q; policy = %v", want, got)
		}
	}
	for _, p := range got {
		if strings.HasPrefix(p, "ses:Send") && !strings.HasSuffix(p, " "+sesSenderARN) {
			t.Errorf("policy grants %q, want every send action on the sender's identity ARN only", p)
		}
	}
}

// sesPolicyPairs flattens an IAM policy document into "action resource".
func sesPolicyPairs(t *testing.T, doc json.RawMessage) []string {
	t.Helper()
	var d struct {
		Statement []struct{ Action, Resource json.RawMessage }
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatalf("policy %s: %v", doc, err)
	}
	list := func(raw json.RawMessage) []string {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			return []string{one}
		}
		var many []string
		if err := json.Unmarshal(raw, &many); err != nil {
			t.Fatalf("policy element %s: %v", raw, err)
		}
		return many
	}
	var out []string
	for _, s := range d.Statement {
		for _, a := range list(s.Action) {
			for _, r := range list(s.Resource) {
				out = append(out, a+" "+r)
			}
		}
	}
	return out
}

// Under enforce a user with no policy is refused v1 and v2 SendEmail with
// switch ses.denial.identity's identities (iam-enforcement-surfaces.md
// 12.3, the SES rows; inferred, SES-28): v1 AccessDenied, Type Sender, 403;
// v2 AccessDeniedException in x-amzn-ErrorType, 403, its text under the
// key "message" (ses.md 3.2). Both name ses:SendEmail.
func TestSESDenialIdentities(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	if v := wiredSwitch(t, c, "ses.denial.identity"); v != ses.DenialIdentityValue {
		t.Errorf("ses.denial.identity = %q, want %q", v, ses.DenialIdentityValue)
	}

	w := sesSendEmail(t, u, endpoint)
	e := parseXMLError(t, w)
	v1 := sesapi.DenialError()
	if w.status != v1.Status || w.status != http.StatusForbidden || e.Code != "AccessDenied" || e.Type != "Sender" ||
		!strings.Contains(e.Message, "ses:SendEmail") {
		t.Errorf("v1 SendEmail denial = %d %s, want 403 Sender AccessDenied naming ses:SendEmail", w.status, w.body)
	}

	w = sesV2Send(t, u, endpoint, sesV2SimpleBody)
	var body map[string]string
	if err := json.Unmarshal([]byte(w.body), &body); err != nil {
		t.Fatalf("v2 SendEmail denial body %q: %v", w.body, err)
	}
	if w.status != sesv2api.DenialError().Status || w.status != http.StatusForbidden || jsonType(w) != "AccessDeniedException" ||
		!strings.Contains(body["Message"], "ses:SendEmail") {
		t.Errorf("v2 SendEmail denial = %d %v %s, want 403 AccessDeniedException with Message naming ses:SendEmail", w.status, w.header, w.body)
	}
}

// Under enforce a policy that allows the send but denies it on
// ses:FromAddress, which understudy does not supply, would let the send
// through if the key were taken as absent: the call ends 501
// NotImplemented naming the key instead, in both APIs. Under advise it is
// served with the advisory.
func TestSESConditionKeysAreRefusedUnderEnforce(t *testing.T) {
	const doc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:*","Resource":"*"},` +
		`{"Sid":"NotBlocked","Effect":"Deny","Action":"ses:SendEmail","Resource":"*","Condition":{"StringLike":{"ses:FromAddress":"*@example.com"}}}]}`
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, doc)
	v1 := sesSendEmail(t, u, endpoint)
	v2 := sesV2Send(t, u, endpoint, sesV2SimpleBody)
	for name, w := range map[string]struct {
		wire
		code string
	}{
		"v1": {v1, parseXMLError(t, v1).Code},
		"v2": {v2, jsonType(v2)},
	} {
		if w.status != http.StatusNotImplemented || w.code != "NotImplemented" || !strings.Contains(w.body, "ses:FromAddress") {
			t.Errorf("%s SendEmail = %d %s, want 501 NotImplemented naming ses:FromAddress", name, w.status, w.body)
		}
	}

	c, adviseEndpoint, au := enforced(t, server.IAMEnforceAdvise)
	putUserPolicy(t, adviseEndpoint, doc)
	if w := sesSendEmail(t, au, adviseEndpoint); w.status == http.StatusNotImplemented || w.status == http.StatusForbidden {
		t.Errorf("v1 SendEmail under advise = %d %s, want it past authorization", w.status, w.body)
	}
	if got := enforcementAdvisories(t, c, "condition-key-unsupplied"); !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "ses:FromAddress") }) {
		t.Errorf("condition-key-unsupplied advisories = %q, want one naming ses:FromAddress", got)
	}
}
