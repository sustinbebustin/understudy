package server_test

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/server"
)

// iam.enforce end to end: a user IAM CreateUser and CreateAccessKey minted
// signs requests over the wire. Under enforce a user with no policy is
// refused by every service with the service's denial identity
// (iam-enforcement-surfaces.md 12.3), whose message is compared byte for
// byte where the identity's evidence is aws-recorded or aws-doc (STS, S3),
// and whose code and status otherwise follow the service's declared
// identity, set by its switch (12.8). Under advise the calls are served
// and journaled; under off nothing changes (12.1).

// wire is an answer's status, headers and body.
type wire struct {
	status int
	header http.Header
	body   string
}

// signedCall signs req with s for service and sends it.
func signedCall(t *testing.T, s signer, service string, req *http.Request, body string) wire {
	t.Helper()
	auth.Sign(req, []byte(body), s.scoped(service), time.Now())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return wire{res.StatusCode, res.Header, string(data)}
}

// queryCall sends an awsQuery form.
func queryCall(t *testing.T, s signer, endpoint, service string, form url.Values) wire {
	t.Helper()
	body := form.Encode()
	req := newRequest(t, http.MethodPost, endpoint+"/", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return signedCall(t, s, service, req, body)
}

// jsonCall sends an awsJson request with target and version 1.0 or 1.1.
func jsonCall(t *testing.T, s signer, endpoint, service, version, target, body string) wire {
	t.Helper()
	req := newRequest(t, http.MethodPost, endpoint+"/", body)
	req.Header.Set("Content-Type", "application/x-amz-json-"+version)
	req.Header.Set("X-Amz-Target", target)
	return signedCall(t, s, service, req, body)
}

// restGet sends a REST GET.
func restGet(t *testing.T, s signer, service, target string) wire {
	t.Helper()
	return signedCall(t, s, service, newRequest(t, http.MethodGet, target, ""), "")
}

// xmlError is an awsQuery or restXml error body's elements.
type xmlError struct {
	XMLName xml.Name
	Type    string `xml:"Error>Type"`
	Code    string `xml:"Error>Code"`
	Message string `xml:"Error>Message"`
	// S3's error is the root element itself.
	S3Code    string `xml:"Code"`
	S3Message string `xml:"Message"`
}

func parseXMLError(t *testing.T, w wire) xmlError {
	t.Helper()
	var e xmlError
	if err := xml.Unmarshal([]byte(w.body), &e); err != nil {
		t.Fatalf("error body %q: %v", w.body, err)
	}
	return e
}

// jsonType is an awsJson or restJson error's type: __type, else the
// x-amzn-errortype header, without the namespace.
func jsonType(w wire) string {
	var body struct {
		Type string `json:"__type"`
	}
	t := w.header.Get("X-Amzn-Errortype")
	if json.Unmarshal([]byte(w.body), &body) == nil && body.Type != "" {
		t = body.Type
	}
	t, _, _ = strings.Cut(t, ":")
	return t[strings.LastIndex(t, "#")+1:]
}

func shortName(id awserr.Identity) string {
	s := string(id.Shape)
	return s[strings.LastIndex(s, "#")+1:]
}

// enforced runs a server under mode and returns a client, the endpoint and
// the credentials of the user u, created by root.
func enforced(t *testing.T, mode server.IAMEnforce) (*admin.Client, string, signer) {
	t.Helper()
	c, endpoint := adminServer(t, server.Config{IAMEnforce: mode})
	return c, endpoint, accessKey(t, endpoint, "u")
}

// putUserPolicy gives the user u the inline policy document, as root.
func putUserPolicy(t *testing.T, endpoint, document string) {
	t.Helper()
	got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"PutUserPolicy"}, "Version": {"2010-05-08"}, "UserName": {"u"}, "PolicyName": {"p"}, "PolicyDocument": {document}})
	if got.status != http.StatusOK {
		t.Fatalf("PutUserPolicy = %d %s", got.status, got.body)
	}
}

// enforcementAdvisories returns the texts of the journal's advisories of
// kind (a wire name such as access-denied).
func enforcementAdvisories(t *testing.T, c *admin.Client, kind string) []string {
	t.Helper()
	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "advisory", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range page.Events {
		var d struct{ Advisory, Text string }
		if json.Unmarshal(page.Events[i].Detail, &d) == nil && d.Advisory == kind {
			out = append(out, d.Text)
		}
	}
	return out
}

const userU = "arn:aws:iam::000000000000:user/u"

// The recorded AssumeRole denial: a trust policy that does not name the
// caller answers AccessDenied, Sender, 403, in the STS namespace, with no
// reason clause (iam-enforcement-surfaces.md 4.3, aws-recorded,
// testdata/parity/sts/R15/06-assume-denied.json). The XML is compared
// element by element; the request id is not compared.
func TestEnforceAnswersTheRecordedSTSDenial(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:user/someone-else"},"Action":"sts:AssumeRole"}]}`
	accessKey(t, endpoint, "someone-else")
	if got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"deny-me"}, "AssumeRolePolicyDocument": {trust}}); got.status != http.StatusOK {
		t.Fatalf("CreateRole = %d %s", got.status, got.body)
	}
	w := queryCall(t, u, endpoint, "sts", url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::000000000000:role/deny-me"}, "RoleSessionName": {"rec"}})
	e := parseXMLError(t, w)
	const message = "User: arn:aws:iam::000000000000:user/u is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::000000000000:role/deny-me"
	if w.status != http.StatusForbidden || e.XMLName.Space != "https://sts.amazonaws.com/doc/2011-06-15/" || e.XMLName.Local != "ErrorResponse" ||
		e.Type != "Sender" || e.Code != "AccessDenied" || e.Message != message || !strings.HasPrefix(w.header.Get("Content-Type"), "text/xml") {
		t.Errorf("AssumeRole denial = %d %s %s; want 403 text/xml ErrorResponse in the STS namespace, Sender, AccessDenied, %q", w.status, w.header.Get("Content-Type"), w.body, message)
	}
}

// S3 answers AccessDenied 403 with its variant of the grammar for a
// principal of the bucket owner's account (iam-enforcement-surfaces.md 4.2,
// aws-doc): an implicit identity denial names no resource.
func TestEnforceAnswersTheDocumentedS3Denial(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/docs", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	w := restGet(t, u, "s3", endpoint+"/docs?list-type=2")
	e := parseXMLError(t, w)
	const message = "User: arn:aws:iam::000000000000:user/u is not authorized to perform: s3:ListBucket because no identity-based policy allows the s3:ListBucket action"
	if w.status != http.StatusForbidden || e.S3Code != "AccessDenied" || e.S3Message != message {
		t.Errorf("ListObjectsV2 denial = %d %s; want 403 AccessDenied %q", w.status, w.body, message)
	}
}

// Every other service answers its declared denial identity: SNS's
// AuthorizationError 403 (the model's, aws-doc); the others' code and
// status as their switches set them (iam-enforcement-surfaces.md 12.3,
// 12.8: sqs.denial.json-identity, sqs.denial.query-text,
// iam.denial.identity, kms.denial.status, secretsmanager.denial.status,
// logs.denial.status, events.denial.status, cloudwatch.denial.identity,
// lambda.denial.body). SQS over Query names a URL on the endpoint the
// client used (4.3, aws-doc text form).
func TestEnforceAnswersEachServicesDenialIdentity(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	probes := []struct {
		service string
		want    awserr.Identity
		send    func() wire
		code    func(wire) string
	}{
		{"sns", snsapi.DenialError(), func() wire {
			return queryCall(t, u, endpoint, "sns", url.Values{"Action": {"ListTopics"}, "Version": {"2010-03-31"}})
		}, func(w wire) string { return parseXMLError(t, w).Code }},
		{"sqs json", sqsapi.DenialError(), func() wire {
			return jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.ListQueues", "{}")
		}, jsonType},
		{"sqs query", sqsapi.DenialError(), func() wire {
			return queryCall(t, u, endpoint, "sqs", url.Values{"Action": {"ListQueues"}, "Version": {"2012-11-05"}})
		}, func(w wire) string { return parseXMLError(t, w).Code }},
		{"iam", iamapi.DenialError(), func() wire {
			return queryCall(t, u, endpoint, "iam", url.Values{"Action": {"ListUsers"}, "Version": {"2010-05-08"}})
		}, func(w wire) string { return parseXMLError(t, w).Code }},
		{"kms", kmsapi.DenialError(), func() wire {
			return jsonCall(t, u, endpoint, "kms", "1.1", "TrentService.ListKeys", "{}")
		}, jsonType},
		{"secretsmanager", secretsmanagerapi.DenialError(), func() wire {
			return jsonCall(t, u, endpoint, "secretsmanager", "1.1", "secretsmanager.ListSecrets", "{}")
		}, jsonType},
		{"logs", logsapi.DenialError(), func() wire {
			return jsonCall(t, u, endpoint, "logs", "1.1", "Logs_20140328.DescribeLogGroups", "{}")
		}, jsonType},
		{"events", eventbridgeapi.DenialError(), func() wire {
			return jsonCall(t, u, endpoint, "events", "1.1", "AWSEvents.ListRules", "{}")
		}, jsonType},
		{"cloudwatch", cloudwatchapi.DenialError(), func() wire {
			return queryCall(t, u, endpoint, "monitoring", url.Values{"Action": {"ListMetrics"}, "Version": {"2010-08-01"}})
		}, func(w wire) string { return parseXMLError(t, w).Code }},
		{"lambda", lambdaapi.DenialError(), func() wire {
			return restGet(t, u, "lambda", endpoint+"/2015-03-31/functions/")
		}, jsonType},
	}
	for _, p := range probes {
		w := p.send()
		code := p.code(w)
		// A JSON answer carries the identity's __type, a Query or XML one
		// its code.
		want := p.want.QueryCode
		if isJSON := strings.HasPrefix(w.header.Get("Content-Type"), "application/x-amz-json") || strings.HasPrefix(w.header.Get("Content-Type"), "application/json"); isJSON {
			want = shortName(p.want)
			if j := p.want.JSONType; j != "" {
				want = j[strings.LastIndex(j, "#")+1:]
			}
		}
		if w.status != p.want.Status || code != want {
			t.Errorf("%s denial = %d %q (%s); want %d %q", p.service, w.status, code, w.body, p.want.Status, want)
		}
		if p.service == "sqs query" {
			if text := parseXMLError(t, w).Message; !strings.HasPrefix(text, "Access to the resource "+endpoint) || !strings.HasSuffix(text, " is denied.") {
				t.Errorf("SQS Query denial text = %q, want %q", text, "Access to the resource "+endpoint+"/ is denied.")
			}
		}
	}
	if got := snsapi.DenialError(); got.QueryCode != "AuthorizationError" || got.Status != http.StatusForbidden {
		t.Errorf("SNS denial identity = %s %d, want AuthorizationError 403 (com.amazonaws.sns#AuthorizationErrorException)", got.QueryCode, got.Status)
	}
	if got := s3api.DenialError(); got.Status != http.StatusForbidden {
		t.Errorf("S3 denial status = %d, want 403", got.Status)
	}
}

// Under enforce a user's identity policy grants what it names and nothing
// more, the account root keeps full access in its own account
// (iam-evaluation.md 2.6), and each denial journals an access-denied
// advisory.
func TestEnforceGrantsWhatPoliciesAllow(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}`)
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.ListQueues", "{}"); w.status != http.StatusOK {
		t.Errorf("granted ListQueues = %d %s, want 200", w.status, w.body)
	}
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.CreateQueue", `{"QueueName":"q"}`); w.status == http.StatusOK {
		t.Errorf("ungranted CreateQueue = %d %s, want the denial", w.status, w.body)
	}
	if w := jsonCall(t, dummy, endpoint, "sqs", "1.0", "AmazonSQS.CreateQueue", `{"QueueName":"q"}`); w.status != http.StatusOK {
		t.Errorf("root CreateQueue = %d %s, want 200", w.status, w.body)
	}
	if got := enforcementAdvisories(t, c, "access-denied"); len(got) != 1 || !strings.Contains(got[0], "sqs:CreateQueue") {
		t.Errorf("access-denied advisories = %q, want one for sqs:CreateQueue", got)
	}
}

// Under advise the same refusal is served and journaled as
// access-would-deny; under off it is served with no such advisory, as
// before enforcement existed (iam-enforcement-surfaces.md 12.1).
func TestAdviseAndOffServeTheCall(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceAdvise)
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.ListQueues", "{}"); w.status != http.StatusOK {
		t.Errorf("advise ListQueues = %d %s, want 200", w.status, w.body)
	}
	got := enforcementAdvisories(t, c, "access-would-deny")
	if len(got) != 1 || !strings.HasSuffix(got[0], "; iam.enforce is advise, so understudy served the call") || !strings.Contains(got[0], userU) {
		t.Errorf("access-would-deny advisories = %q, want one for the user", got)
	}

	c, endpoint, u = enforced(t, server.IAMEnforceOff)
	if w := jsonCall(t, u, endpoint, "sqs", "1.0", "AmazonSQS.ListQueues", "{}"); w.status != http.StatusOK {
		t.Errorf("off ListQueues = %d %s, want 200", w.status, w.body)
	}
	for _, kind := range []string{"access-denied", "access-would-deny"} {
		if got := enforcementAdvisories(t, c, kind); len(got) != 0 {
			t.Errorf("off journaled %s advisories %q, want none", kind, got)
		}
	}
}

// sts:GetCallerIdentity needs no permission and works under an explicit
// deny (iam-enforcement-surfaces.md 5.1, aws-doc), and the account root
// may not assume a role under enforce (iam-evaluation.md 3.5; 12.2).
func TestEnforceNeverAsksAboutGetCallerIdentityAndRefusesRootAssumeRole(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`)
	if w := queryCall(t, u, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}); w.status != http.StatusOK || !strings.Contains(w.body, userU) {
		t.Errorf("GetCallerIdentity under a deny-all = %d %s, want 200 naming the user", w.status, w.body)
	}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`
	if got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"r"}, "AssumeRolePolicyDocument": {trust}}); got.status != http.StatusOK {
		t.Fatalf("CreateRole = %d %s", got.status, got.body)
	}
	w := queryCall(t, dummy, endpoint, "sts", url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::000000000000:role/r"}, "RoleSessionName": {"s1"}})
	if w.status != http.StatusForbidden || parseXMLError(t, w).Code != "AccessDenied" ||
		!strings.HasPrefix(parseXMLError(t, w).Message, "User: arn:aws:iam::000000000000:root is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::000000000000:role/r") {
		t.Errorf("root AssumeRole under enforce = %d %s, want the STS denial naming the root", w.status, w.body)
	}
}

// S3 GetObject on a missing key answers 404 NoSuchKey to a caller with
// s3:ListBucket and 403 AccessDenied to one without
// (iam-enforcement-surfaces.md 5.1, GetObject API reference, aws-doc).
func TestEnforceAnswersAMissingKeyByListBucket(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/docs", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::docs/*"}]}`)
	if w := restGet(t, u, "s3", endpoint+"/docs/missing.txt"); w.status != http.StatusForbidden || parseXMLError(t, w).S3Code != "AccessDenied" {
		t.Errorf("GetObject of a missing key without s3:ListBucket = %d %s, want 403 AccessDenied", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::docs/*"},{"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::docs"}]}`)
	if w := restGet(t, u, "s3", endpoint+"/docs/missing.txt"); w.status != http.StatusNotFound || parseXMLError(t, w).S3Code != "NoSuchKey" {
		t.Errorf("GetObject of a missing key with s3:ListBucket = %d %s, want 404 NoSuchKey", w.status, w.body)
	}
}

// The bucket owner's root may delete a bucket policy that denies everyone
// (iam-evaluation.md 2.6, S3-PBP; iam-enforcement-surfaces.md 6.3), so a
// configuration that locks a bucket stays destroyable (iam-evaluation.md
// 12.5 item 2).
func TestEnforceLetsTheOwnersRootDeleteALockingBucketPolicy(t *testing.T) {
	_, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/locked", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	deny := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:aws:s3:::locked","arn:aws:s3:::locked/*"]}]}`
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/locked?policy", deny); got.status/100 != 2 {
		t.Fatalf("PutBucketPolicy = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/locked/k", "v"); got.status != http.StatusForbidden {
		t.Errorf("root PutObject under a deny-all bucket policy = %d %s, want 403", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodDelete, endpoint+"/locked?policy", ""); got.status/100 != 2 {
		t.Errorf("root DeleteBucketPolicy under a deny-all bucket policy = %d %s, want it allowed", got.status, got.body)
	}
}

// An SNS delivery the queue's policy refuses is a client-side error: no
// retry, and with no dead-letter queue the message is discarded
// (iam-enforcement-surfaces.md 8.2, 12.4, aws-doc); the denial is
// journaled. Under off it is delivered anyway (ARCHITECTURE.md 16).
func TestEnforceDropsADeliveryTheTargetRefuses(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	const queueARN = "arn:aws:sqs:us-east-1:000000000000:guarded"
	queueURL := endpoint + "/000000000000/guarded"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"guarded"}`)
	topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"refused"}}), "TopicArn")
	mustSNS(t, endpoint, url.Values{"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {queueARN}})
	mustSNS(t, endpoint, url.Values{"Action": {"Publish"}, "TopicArn": {topic}, "Message": {"hello"}})
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	received := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`","MaxNumberOfMessages":10}`)
	var out struct{ Messages []json.RawMessage }
	if err := json.Unmarshal([]byte(received), &out); err != nil || len(out.Messages) != 0 {
		t.Errorf("ReceiveMessage = %s, want nothing: the queue policy does not allow sns.amazonaws.com", received)
	}
	denied := enforcementAdvisories(t, c, "access-denied")
	if !slices.ContainsFunc(denied, func(text string) bool {
		return strings.Contains(text, "sns.amazonaws.com") && strings.Contains(text, "sqs:SendMessage")
	}) {
		t.Errorf("access-denied advisories = %q, want one for sns.amazonaws.com calling sqs:SendMessage", denied)
	}
}

// POST /iam/evaluate decides pairs for a principal without a call, under
// every mode: the stored policies or replacements, the decision in the IAM
// simulator's spellings (com.amazonaws.iam#PolicyEvaluationDecisionType,
// iam-evaluation.md 12.4), the denial's type and message. It refuses a
// principal that is no IAM ARN, an empty request list and an action without
// a prefix as invalid requests naming the field, and a principal that does
// not exist as not found.
func TestIAMEvaluate(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceOff)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"},{"Effect":"Deny","Action":"sqs:DeleteQueue","Resource":"*"}]}`)
	got, err := c.IAMEvaluate(t.Context(), admin.EvaluateRequest{Principal: userU, Requests: []admin.EvaluateItem{
		{Action: "sqs:ListQueues", Resource: "*"},
		{Action: "sqs:CreateQueue", Resource: "*"},
		{Action: "sqs:DeleteQueue", Resource: "arn:aws:sqs:us-east-1:000000000000:q"},
	}})
	if err != nil {
		t.Fatalf("IAMEvaluate = %v", err)
	}
	want := []admin.EvaluationResult{
		{Action: "sqs:ListQueues", Resource: "*", Decision: "allowed"},
		{
			Action: "sqs:CreateQueue", Resource: "*", Decision: "implicitDeny", PolicyType: "identity-based policy",
			Message: "User: arn:aws:iam::000000000000:user/u is not authorized to perform: sqs:CreateQueue because no identity-based policy allows the sqs:CreateQueue action",
		},
		{Action: "sqs:DeleteQueue", Resource: "arn:aws:sqs:us-east-1:000000000000:q", Decision: "explicitDeny", PolicyType: "identity-based policy"},
	}
	if len(got.Results) != 3 {
		t.Fatalf("results = %+v, want three", got.Results)
	}
	for i, w := range want {
		g := got.Results[i]
		if g.Action != w.Action || g.Resource != w.Resource || g.Decision != w.Decision || g.PolicyType != w.PolicyType || (w.Message != "" && g.Message != w.Message) {
			t.Errorf("result %d = %+v, want %+v", i, g, w)
		}
	}

	none := []json.RawMessage{}
	replaced, err := c.IAMEvaluate(t.Context(), admin.EvaluateRequest{Principal: userU, IdentityPolicies: &none, Requests: []admin.EvaluateItem{{Action: "sqs:ListQueues", Resource: "*"}}})
	if err != nil || len(replaced.Results) != 1 || replaced.Results[0].Decision != "implicitDeny" {
		t.Errorf("IAMEvaluate with no identity policies = %+v, %v; want an implicit deny", replaced, err)
	}

	refusals := []struct {
		req   admin.EvaluateRequest
		code  admin.ErrorCode
		field string
	}{
		{admin.EvaluateRequest{Principal: "sns.amazonaws.com", Requests: []admin.EvaluateItem{{Action: "sqs:ListQueues", Resource: "*"}}}, admin.InvalidRequest, "principal"},
		{admin.EvaluateRequest{Principal: userU}, admin.InvalidRequest, "requests"},
		{admin.EvaluateRequest{Principal: userU, Requests: []admin.EvaluateItem{{Action: "ListQueues", Resource: "*"}}}, admin.InvalidRequest, "requests[0].action"},
		{admin.EvaluateRequest{Principal: userU, Requests: []admin.EvaluateItem{{Action: "sqs:ListQueues", Resource: "queue"}}}, admin.InvalidRequest, "requests[0].resource"},
	}
	for _, r := range refusals {
		_, err := c.IAMEvaluate(t.Context(), r.req)
		var e *admin.Error
		if !errors.As(err, &e) || e.Code != r.code || e.Field != r.field {
			t.Errorf("IAMEvaluate(%+v) = %v, want %s at %s", r.req, err, r.code, r.field)
		}
	}
	_, err = c.IAMEvaluate(t.Context(), admin.EvaluateRequest{Principal: "arn:aws:iam::000000000000:user/ghost", Requests: []admin.EvaluateItem{{Action: "sqs:ListQueues", Resource: "*"}}})
	var e *admin.Error
	if !errors.As(err, &e) || e.Code.HTTPStatus() != http.StatusNotFound || e.Message != "arn:aws:iam::000000000000:user/ghost does not exist" {
		t.Errorf("IAMEvaluate of a missing user = %v, want 404 naming the ARN", err)
	}
}

// GET /iam/policy renders a delivery's statement with the aws:SourceAccount
// AWS sends: S3 sends the bucket owner's account
// (iam-enforcement-surfaces.md 8.1, aws-doc), so the queue statement carries
// it (12.6); and the identity policy leaves out sts:GetCallerIdentity,
// which needs no permission (5.1).
func TestIAMPolicyRendersTheKeysAWSSends(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	const queueARN = "arn:aws:sqs:us-east-1:000000000000:uploads"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"uploads"}`)
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"` + queueARN + `"}]}`
	attrs, err := json.Marshal(map[string]any{"QueueUrl": endpoint + "/000000000000/uploads", "Attributes": map[string]string{"Policy": policy}})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(attrs))
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	notification := `<NotificationConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><QueueConfiguration>` +
		`<Queue>` + queueARN + `</Queue><Event>s3:ObjectCreated:*</Event></QueueConfiguration></NotificationConfiguration>`
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads?notification", notification); got.status != http.StatusOK {
		t.Fatalf("PutBucketNotificationConfiguration = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/uploads/a.txt", "abc"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	if got := query(t, dummy, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}); got.status != http.StatusOK {
		t.Fatalf("GetCallerIdentity = %d %s", got.status, got.body)
	}
	if settled, err := c.Settle(t.Context(), 10*time.Second); err != nil || !settled.Settled {
		t.Fatalf("settle = %+v, %v", settled, err)
	}
	got, err := c.IAMPolicy(t.Context(), "arn:aws:iam::000000000000:root")
	if err != nil {
		t.Fatal(err)
	}
	want := admin.ResourcePolicy{Target: queueARN, Principal: "s3.amazonaws.com", Action: "sqs:SendMessage", Source: "arn:aws:s3:::uploads", SourceAccount: "000000000000"}
	if !slices.Contains(got.ResourcePolicies, want) {
		t.Errorf("resourcePolicies = %+v, want %+v", got.ResourcePolicies, want)
	}
	if strings.Contains(string(got.Document), "sts:GetCallerIdentity") {
		t.Errorf("policy document %s grants sts:GetCallerIdentity, which needs no permission", got.Document)
	}
}
