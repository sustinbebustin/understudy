package server_test

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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

// A v2 SendEmail with Raw content is authorized as ses:SendRawEmail on the
// identity of the raw From header, not as ses:SendEmail (SES-28 step 04):
// under advise the journal's would-deny advisory names it, and the policy
// the access record renders grants it on that identity alone.
func TestSESV2RawSendIsAuthorizedAsSendRawEmail(t *testing.T) {
	c, endpoint := adminServer(t, sesConfig(server.IAMEnforceAdvise))
	u := accessKey(t, endpoint, "u")
	if w := queryCall(t, dummy, endpoint, "ses", url.Values{"Action": {"VerifyEmailIdentity"}, "Version": {sesV1Version}, "EmailAddress": {sesSender}}); w.status != http.StatusOK {
		t.Fatalf("VerifyEmailIdentity = %d %s", w.status, w.body)
	}
	raw := base64.StdEncoding.EncodeToString([]byte("From: " + sesSender + "\r\nTo: " + sesSimulatorTo + "\r\nSubject: s\r\n\r\nb\r\n"))
	if w := sesV2Send(t, u, endpoint, `{"Content":{"Raw":{"Data":"`+raw+`"}}}`); w.status != http.StatusOK {
		t.Fatalf("v2 Raw SendEmail under advise = %d %s", w.status, w.body)
	}
	advisories := enforcementAdvisories(t, c, "access-would-deny")
	if len(advisories) != 1 || !strings.Contains(advisories[0], "'ses:SendRawEmail' on resource '"+sesSenderARN+"'") {
		t.Errorf("access-would-deny advisories = %q, want one naming ses:SendRawEmail on %s", advisories, sesSenderARN)
	}
	got := granted(t, c, userU)
	if want := []string{"ses:SendRawEmail"}; !slices.Equal(got[sesSenderARN], want) || len(got) != 1 {
		t.Errorf("generated policy = %v, want %v on %s alone", got, want, sesSenderARN)
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
// 12.3, the SES rows; SES-28): v1 AccessDenied, Type Sender, 403; v2
// AccessDeniedException in x-amzn-ErrorType, 403, its text under the key
// "Message". Both answer SES's send text naming ses:SendEmail on the
// sender's identity.
func TestSESDenialIdentities(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	if v := wiredSwitch(t, c, "ses.denial.identity"); v != ses.DenialIdentityValue {
		t.Errorf("ses.denial.identity = %q, want %q", v, ses.DenialIdentityValue)
	}
	const text = "User '" + userU + "' is not authorized to perform 'ses:SendEmail' on resource '" + sesSenderARN + "'"

	w := sesSendEmail(t, u, endpoint)
	e := parseXMLError(t, w)
	v1 := sesapi.DenialError()
	if w.status != v1.Status || w.status != http.StatusForbidden || e.Code != "AccessDenied" || e.Type != "Sender" || e.Message != text {
		t.Errorf("v1 SendEmail denial = %d %s, want 403 Sender AccessDenied with %q", w.status, w.body, text)
	}

	w = sesV2Send(t, u, endpoint, sesV2SimpleBody)
	var body map[string]string
	if err := json.Unmarshal([]byte(w.body), &body); err != nil {
		t.Fatalf("v2 SendEmail denial body %q: %v", w.body, err)
	}
	if w.status != sesv2api.DenialError().Status || w.status != http.StatusForbidden || jsonType(w) != "AccessDeniedException" || body["Message"] != text {
		t.Errorf("v2 SendEmail denial = %d %v %s, want 403 AccessDeniedException with Message %q", w.status, w.header, w.body, text)
	}
}

// sesDenyUnder is a policy that allows every SES action and denies the
// sends under condition, a Condition element's JSON.
func sesDenyUnder(condition string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:*","Resource":"*"},` +
		`{"Sid":"Scoped","Effect":"Deny","Action":"ses:Send*","Resource":"*","Condition":` + condition + `}]}`
}

// sesKeyed is a server under enforce whose user u holds the policy doc,
// with sesSender's identity created verified (ses.verifiedIdentities).
func sesKeyed(t *testing.T, doc string) (string, signer) {
	t.Helper()
	_, endpoint := adminServer(t, sesConfig(server.IAMEnforceEnforce))
	for _, v := range []url.Values{
		{"Action": {"VerifyEmailIdentity"}, "EmailAddress": {sesSender}},
		{"Action": {"CreateTemplate"}, "Template.TemplateName": {"t"}, "Template.SubjectPart": {"s"}, "Template.TextPart": {"b"}},
	} {
		v.Set("Version", sesV1Version)
		if w := queryCall(t, dummy, endpoint, "ses", v); w.status != http.StatusOK {
			t.Fatalf("%s = %d %s", v.Get("Action"), w.status, w.body)
		}
	}
	u := accessKey(t, endpoint, "u")
	putUserPolicy(t, endpoint, doc)
	return endpoint, u
}

// The mailbox simulator's other addresses, which the sandbox accepts as
// recipients (ses.md 6.1).
const (
	sesBounceTo    = "bounce@simulator.amazonses.com"
	sesComplaintTo = "complaint@simulator.amazonses.com"
)

// sesShape is what a send under a condition-key test varies: its sender
// (sesSender when empty) and its addresses (To the simulator when to is
// nil).
type sesShape struct {
	source               string
	to, cc, bcc, replyTo []string
}

func (sh sesShape) from() string {
	if sh.source == "" {
		return sesSender
	}
	return sh.source
}

func (sh sesShape) toAddresses() []string {
	if sh.to == nil {
		return []string{sesSimulatorTo}
	}
	return sh.to
}

// sesForm is one of the sends that supply ses:FromAddress and
// ses:Recipients: v1 SendEmail and SendTemplatedEmail, v2 SendEmail with
// Simple and Template content (iam-enforcement-surfaces.md 12.5). action
// is the action its denial names.
type sesForm struct {
	name, action string
	v2           bool
	send         func(t *testing.T, s signer, endpoint string, sh sesShape) wire
}

// sesForms are the four sesForms; the templated ones send the template
// "t" sesKeyed stores.
var sesForms = []sesForm{
	{name: "v1 SendEmail", action: "ses:SendEmail", send: func(t *testing.T, s signer, endpoint string, sh sesShape) wire {
		t.Helper()
		return queryCall(t, s, endpoint, "ses", sesV1Values(sh, url.Values{"Action": {"SendEmail"}, "Message.Subject.Data": {"s"}, "Message.Body.Text.Data": {"b"}}))
	}},
	{name: "v1 SendTemplatedEmail", action: "ses:SendTemplatedEmail", send: func(t *testing.T, s signer, endpoint string, sh sesShape) wire {
		t.Helper()
		return queryCall(t, s, endpoint, "ses", sesV1Values(sh, url.Values{"Action": {"SendTemplatedEmail"}, "Template": {"t"}, "TemplateData": {"{}"}}))
	}},
	{name: "v2 Simple", action: "ses:SendEmail", v2: true, send: func(t *testing.T, s signer, endpoint string, sh sesShape) wire {
		t.Helper()
		return sesV2Send(t, s, endpoint, sesV2Body(t, sh, map[string]any{"Simple": map[string]any{
			"Subject": map[string]string{"Data": "s"}, "Body": map[string]any{"Text": map[string]string{"Data": "b"}},
		}}))
	}},
	{name: "v2 Template", action: "ses:SendEmail", v2: true, send: func(t *testing.T, s signer, endpoint string, sh sesShape) wire {
		t.Helper()
		return sesV2Send(t, s, endpoint, sesV2Body(t, sh, map[string]any{"Template": map[string]string{"TemplateName": "t", "TemplateData": "{}"}}))
	}},
}

// sesV1Values are a v1 send's members: sh's addresses, then members.
func sesV1Values(sh sesShape, members url.Values) url.Values {
	v := url.Values{"Version": {sesV1Version}, "Source": {sh.from()}}
	for prefix, list := range map[string][]string{
		"Destination.ToAddresses": sh.toAddresses(), "Destination.CcAddresses": sh.cc,
		"Destination.BccAddresses": sh.bcc, "ReplyToAddresses": sh.replyTo,
	} {
		for i, a := range list {
			v.Set(prefix+".member."+strconv.Itoa(i+1), a)
		}
	}
	maps.Copy(v, members)
	return v
}

// sesV2Body is a v2 SendEmail body with sh's addresses and content.
func sesV2Body(t *testing.T, sh sesShape, content map[string]any) string {
	t.Helper()
	destination := map[string][]string{"ToAddresses": sh.toAddresses()}
	if sh.cc != nil {
		destination["CcAddresses"] = sh.cc
	}
	if sh.bcc != nil {
		destination["BccAddresses"] = sh.bcc
	}
	body := map[string]any{"FromEmailAddress": sh.from(), "Destination": destination, "Content": content}
	if sh.replyTo != nil {
		body["ReplyToAddresses"] = sh.replyTo
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// sesDenied reports whether w is f's SES denial (SES-28): 403, v1
// AccessDenied of Type Sender or v2 AccessDeniedException under "Message",
// in SES's grammar naming f's action. It fails the test on any answer
// that is neither that nor a success.
func sesDenied(t *testing.T, f sesForm, w wire) bool {
	t.Helper()
	head := "User '" + userU + "' is not authorized to perform '" + f.action + "' on resource 'arn:aws:ses:us-east-1:000000000000:identity/"
	if w.status == http.StatusOK {
		return false
	}
	if f.v2 {
		var body map[string]string
		if json.Unmarshal([]byte(w.body), &body) == nil && w.status == http.StatusForbidden && jsonType(w) == "AccessDeniedException" &&
			strings.HasPrefix(body["Message"], head) {
			return true
		}
	} else if e := parseXMLError(t, w); w.status == http.StatusForbidden && e.Code == "AccessDenied" && e.Type == "Sender" && strings.HasPrefix(e.Message, head) {
		return true
	}
	t.Errorf("%s = %d %s, want 200 or the SES denial starting %q", f.name, w.status, w.body, head)
	return false
}

// Under enforce a Deny of the sends conditioned on a key understudy
// supplies denies, with the SES denial answer, each of v1 SendEmail and
// SendTemplatedEmail and v2 SendEmail with Simple and Template content
// whose key matches, and lets through each whose key does not
// (iam-enforcement-surfaces.md 12.5). ses:FromAddress is the sender as
// written, +label kept (aws-doc), and without a display name (switch
// ses.condition.address-form); ses:Recipients are To, Cc and Bcc (aws-doc),
// never ReplyTo, each without its display name (the same switch), and are
// multivalued, so a plain operator never matches (iam-evaluation.md
// 6.12); ses:ApiVersion is "2" on v2 (aws-doc) and "1" on v1 (switch
// ses.condition.api-version-v1).
func TestSESConditionKeysDecideSends(t *testing.T) {
	for _, c := range []struct {
		name, condition string
		shape           sesShape
		v1, v2          bool
	}{
		{"FromAddress matches", `{"StringLike":{"ses:FromAddress":"sender@*"}}`, sesShape{}, true, true},
		{"FromAddress does not match", `{"StringLike":{"ses:FromAddress":"other@*"}}`, sesShape{}, false, false},
		{"FromAddress keeps the +label", `{"StringEquals":{"ses:FromAddress":"sender+news@example.com"}}`, sesShape{source: "sender+news@example.com"}, true, true},
		{"FromAddress is not the label-stripped identity", `{"StringEquals":{"ses:FromAddress":"sender@example.com"}}`, sesShape{source: "sender+news@example.com"}, false, false},
		{"FromAddress drops the display name", `{"StringEquals":{"ses:FromAddress":"sender@example.com"}}`, sesShape{source: "Sender Name <sender@example.com>"}, true, true},
		{"FromAddress is not the display name", `{"StringLike":{"ses:FromAddress":"Sender*"}}`, sesShape{source: "Sender Name <sender@example.com>"}, false, false},
		{"Recipients hold To", `{"ForAnyValue:StringEquals":{"ses:Recipients":"` + sesSimulatorTo + `"}}`, sesShape{}, true, true},
		{"Recipients hold Cc", `{"ForAnyValue:StringLike":{"ses:Recipients":"bounce@*"}}`, sesShape{cc: []string{sesBounceTo}}, true, true},
		{"Recipients hold Bcc", `{"ForAnyValue:StringEquals":{"ses:Recipients":"` + sesComplaintTo + `"}}`, sesShape{bcc: []string{sesComplaintTo}}, true, true},
		{"Recipients do not match", `{"ForAnyValue:StringLike":{"ses:Recipients":"bounce@*"}}`, sesShape{}, false, false},
		{"Recipients exclude ReplyTo", `{"ForAnyValue:StringLike":{"ses:Recipients":"bounce@*"}}`, sesShape{replyTo: []string{sesBounceTo}}, false, false},
		{"Recipients drop the display name", `{"ForAnyValue:StringEquals":{"ses:Recipients":"` + sesBounceTo + `"}}`, sesShape{to: []string{"Bounce <" + sesBounceTo + ">"}}, true, true},
		{"Recipients hold one outside the pattern", `{"ForAnyValue:StringNotLike":{"ses:Recipients":"success@*"}}`, sesShape{cc: []string{sesBounceTo}}, true, true},
		{"Recipients are all inside the pattern", `{"ForAnyValue:StringNotLike":{"ses:Recipients":"success@*"}}`, sesShape{}, false, false},
		{"Recipients need a set operator", `{"StringLike":{"ses:Recipients":"success@*"}}`, sesShape{}, false, false},
		{"ApiVersion 1 is v1's", `{"StringEquals":{"ses:ApiVersion":"1"}}`, sesShape{}, true, false},
		{"ApiVersion 2 is v2's", `{"StringEquals":{"ses:ApiVersion":"2"}}`, sesShape{}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			endpoint, u := sesKeyed(t, sesDenyUnder(c.condition))
			for _, f := range sesForms {
				want := c.v1
				if f.v2 {
					want = c.v2
				}
				if got := sesDenied(t, f, f.send(t, u, endpoint, c.shape)); got != want {
					t.Errorf("%s under %s denied = %t, want %t", f.name, c.condition, got, want)
				}
			}
		})
	}
}

// ses:ApiVersion is carried by every SES operation, not only the sends: a
// Deny of the list operations on "1" denies v1 ListIdentities and lets v2
// ListEmailIdentities through, and on "2" the reverse
// (iam-enforcement-surfaces.md 12.5).
func TestSESApiVersionDecidesEveryOperation(t *testing.T) {
	for version, wantV1 := range map[string]bool{"1": true, "2": false} {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:*","Resource":"*"},` +
			`{"Effect":"Deny","Action":"ses:List*","Resource":"*","Condition":{"StringEquals":{"ses:ApiVersion":"` + version + `"}}}]}`
		endpoint, u := sesKeyed(t, doc)
		v1 := queryCall(t, u, endpoint, "ses", url.Values{"Action": {"ListIdentities"}, "Version": {sesV1Version}})
		v2 := signedCall(t, u, "ses", newRequest(t, http.MethodGet, endpoint+"/v2/email/identities", ""), "")
		for name, c := range map[string]struct {
			w    wire
			want bool
		}{"v1 ListIdentities": {v1, wantV1}, "v2 ListEmailIdentities": {v2, !wantV1}} {
			if denied := c.w.status == http.StatusForbidden; denied != c.want || (!denied && c.w.status != http.StatusOK) {
				t.Errorf("%s under ses:ApiVersion %q = %d %s, want denied %t", name, version, c.w.status, c.w.body, c.want)
			}
		}
	}
}

// Under enforce a Deny on an SES key understudy does not supply for the
// call would let the call through if the key were taken as absent: the
// call ends 501 NotImplemented naming the key instead
// (iam-enforcement-surfaces.md 12.5). ses:FromDisplayName and
// ses:FeedbackAddress are refused by every binding; ses:FromAddress and
// ses:Recipients by the raw sends, v1 SendRawEmail and v2 Raw content,
// and by the non-send operations, where they are not settled. Under advise
// the call is served with the advisory.
func TestSESConditionKeysAreRefusedUnderEnforce(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte("From: " + sesSender + "\r\nTo: " + sesSimulatorTo + "\r\nSubject: s\r\n\r\nb\r\n"))
	calls := map[string]func(s signer, endpoint string) wire{
		"v1 SendRawEmail": func(s signer, endpoint string) wire {
			return queryCall(t, s, endpoint, "ses", url.Values{"Action": {"SendRawEmail"}, "Version": {sesV1Version}, "RawMessage.Data": {raw}})
		},
		"v2 Raw": func(s signer, endpoint string) wire {
			return sesV2Send(t, s, endpoint, `{"Content":{"Raw":{"Data":"`+raw+`"}}}`)
		},
		"v1 ListIdentities": func(s signer, endpoint string) wire {
			return queryCall(t, s, endpoint, "ses", url.Values{"Action": {"ListIdentities"}, "Version": {sesV1Version}})
		},
		"v2 ListEmailIdentities": func(s signer, endpoint string) wire {
			return signedCall(t, s, "ses", newRequest(t, http.MethodGet, endpoint+"/v2/email/identities", ""), "")
		},
	}
	everywhere := slices.Sorted(maps.Keys(calls))
	for _, f := range sesForms {
		calls[f.name] = func(s signer, endpoint string) wire { return f.send(t, s, endpoint, sesShape{}) }
	}
	for key, refusedBy := range map[string][]string{
		"ses:FromDisplayName": slices.Sorted(maps.Keys(calls)),
		"ses:FeedbackAddress": slices.Sorted(maps.Keys(calls)),
		"ses:FromAddress":     everywhere,
		"ses:Recipients":      everywhere,
	} {
		endpoint, u := sesKeyed(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:*","Resource":"*"},`+
			`{"Sid":"NotBlocked","Effect":"Deny","Action":"ses:*","Resource":"*","Condition":{"ForAnyValue:StringLike":{"`+key+`":"*@example.com"}}}]}`)
		for _, name := range refusedBy {
			w := calls[name](u, endpoint)
			code := jsonType(w)
			if strings.HasPrefix(name, "v1") {
				code = parseXMLError(t, w).Code
			}
			if w.status != http.StatusNotImplemented || code != "NotImplemented" || !strings.Contains(w.body, key) {
				t.Errorf("%s under a condition on %s = %d %s, want 501 NotImplemented naming %s", name, key, w.status, w.body, key)
			}
		}
	}

	const doc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:*","Resource":"*"},` +
		`{"Sid":"NotBlocked","Effect":"Deny","Action":"ses:SendEmail","Resource":"*","Condition":{"StringLike":{"ses:FromDisplayName":"*"}}}]}`
	c, adviseEndpoint, au := enforced(t, server.IAMEnforceAdvise)
	putUserPolicy(t, adviseEndpoint, doc)
	if w := sesSendEmail(t, au, adviseEndpoint); w.status == http.StatusNotImplemented || w.status == http.StatusForbidden {
		t.Errorf("v1 SendEmail under advise = %d %s, want it past authorization", w.status, w.body)
	}
	if got := enforcementAdvisories(t, c, "condition-key-unsupplied"); !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "ses:FromDisplayName") }) {
		t.Errorf("condition-key-unsupplied advisories = %q, want one naming ses:FromDisplayName", got)
	}
}
