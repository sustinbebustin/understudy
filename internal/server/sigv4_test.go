package server_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // Content-MD5 is MD5 by definition; the test sends one.
	"crypto/sha1" //nolint:gosec // SigV2 is HMAC-SHA1 by definition; the test presigns as boto3 does.
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// lockedBuffer is a log sink the server writes to from many goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// sigv4Server runs a seeded server with cfg's SigV4 mode and static keys,
// frozen at wall time now so presigned URLs signed now are current, logging
// at debug into the returned buffer.
func sigv4Server(t *testing.T, cfg server.Config) (c *admin.Client, endpoint string, logs *lockedBuffer) {
	t.Helper()
	seed := uint64(13)
	logs = &lockedBuffer{}
	cfg.Seed, cfg.ClockStart, cfg.Version = &seed, server.ClockFrozen{At: time.Now().UTC().Truncate(time.Second)}, "test"
	cfg.LogOutput, cfg.LogLevel = logs, -4
	r := launch(t, cfg)
	r.stopOnCleanup(t)
	c, err := admin.New(r.endpoint, admin.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c, r.endpoint, logs
}

// signer is a client's credentials for service.
type signer struct{ key, secret, token string }

func (s signer) scoped(service string) auth.Signer {
	return auth.Signer{AccessKeyID: s.key, SecretAccessKey: s.secret, SessionToken: s.token, Region: aws.DefaultRegion(), Service: service}
}

// reply is an answer's status and body.
type reply struct {
	status int
	body   string
}

func sigv4Do(t *testing.T, req *http.Request) reply {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{res.StatusCode, string(body)}
}

func newRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// s3Call sends a header-signed S3 request.
func s3Call(t *testing.T, s signer, method, target, body string) reply {
	t.Helper()
	req := newRequest(t, method, target, body)
	auth.Sign(req, []byte(body), s.scoped("s3"), time.Now())
	return sigv4Do(t, req)
}

// sqsJSON sends a header-signed awsJson1_0 ListQueues.
func sqsJSON(t *testing.T, s signer, endpoint string) reply {
	t.Helper()
	req := newRequest(t, http.MethodPost, endpoint+"/", "{}")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.ListQueues")
	auth.Sign(req, []byte("{}"), s.scoped("sqs"), time.Now())
	return sigv4Do(t, req)
}

// query sends a header-signed awsQuery form to service.
func query(t *testing.T, s signer, endpoint, service string, form url.Values) reply {
	t.Helper()
	body := form.Encode()
	req := newRequest(t, http.MethodPost, endpoint+"/", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	auth.Sign(req, []byte(body), s.scoped(service), time.Now())
	return sigv4Do(t, req)
}

func wantAnswer(t *testing.T, what string, got reply, status int, code string) {
	t.Helper()
	if got.status != status || !strings.Contains(got.body, code) {
		t.Errorf("%s = %d %s; want %d %s", what, got.status, got.body, status, code)
	}
}

var dummy = signer{key: "test", secret: "test"}

// xmlElement is the text of the first <name> element of body.
func xmlElement(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile("<" + name + ">([^<]+)</" + name + ">").FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the answer has no %s: %s", name, body)
	}
	return m[1]
}

// assumeRole creates the role worker in the default account and returns
// the temporary credentials STS AssumeRole issues for it.
func assumeRole(t *testing.T, endpoint string) signer {
	t.Helper()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}]}`
	if got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"worker"}, "AssumeRolePolicyDocument": {trust}}); got.status != http.StatusOK {
		t.Fatalf("CreateRole = %d %s", got.status, got.body)
	}
	assumed := query(t, dummy, endpoint, "sts", url.Values{
		"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::000000000000:role/worker"}, "RoleSessionName": {"s1"},
	})
	if assumed.status != http.StatusOK {
		t.Fatalf("AssumeRole = %d %s", assumed.status, assumed.body)
	}
	return signer{key: xmlElement(t, assumed.body, "AccessKeyId"), secret: xmlElement(t, assumed.body, "SecretAccessKey"), token: xmlElement(t, assumed.body, "SessionToken")}
}

// accessKey creates the IAM user name in the default account and returns
// the long-term key IAM CreateAccessKey mints for it.
func accessKey(t *testing.T, endpoint, name string) signer {
	t.Helper()
	if got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateUser"}, "Version": {"2010-05-08"}, "UserName": {name}}); got.status != http.StatusOK {
		t.Fatalf("CreateUser = %d %s", got.status, got.body)
	}
	created := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateAccessKey"}, "Version": {"2010-05-08"}, "UserName": {name}})
	if created.status != http.StatusOK {
		t.Fatalf("CreateAccessKey = %d %s", created.status, created.body)
	}
	return signer{key: xmlElement(t, created.body, "AccessKeyId"), secret: xmlElement(t, created.body, "SecretAccessKey")}
}

// TestSigV4VerifyAcceptsTheKeysThatHoldASecret: under sigv4 verify the
// dummy pair test/test, a 12-digit key with secret test, an accounts.keys
// entry and STS AssumeRole credentials with their token all verify (ADR 0012
// pre-tag review amendment), and each lands in its own account.
func TestSigV4VerifyAcceptsTheKeysThatHoldASecret(t *testing.T) {
	ci, err := aws.ParseAccountID("222222222222")
	if err != nil {
		t.Fatal(err)
	}
	_, endpoint, _ := sigv4Server(t, server.Config{
		SigV4:      server.SigV4Verify,
		StaticKeys: []server.StaticKey{{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: ci}},
	})
	caller := func(s signer) string {
		got := query(t, s, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}})
		if got.status != http.StatusOK {
			t.Fatalf("GetCallerIdentity as %s = %d %s", s.key, got.status, got.body)
		}
		return regexp.MustCompile(`<Account>(\d+)</Account>`).FindStringSubmatch(got.body)[1]
	}
	for _, tc := range []struct {
		s       signer
		account string
	}{
		{dummy, "000000000000"},
		{signer{key: "111122223333", secret: "test"}, "111122223333"},
		{signer{key: "CIKEY0000000000001", secret: "ci-secret"}, "222222222222"},
	} {
		if got := caller(tc.s); got != tc.account {
			t.Errorf("%s is in account %s, want %s", tc.s.key, got, tc.account)
		}
	}

	role := assumeRole(t, endpoint)
	if got := sqsJSON(t, role, endpoint); got.status != http.StatusOK {
		t.Errorf("ListQueues with AssumeRole credentials = %d %s", got.status, got.body)
	}
	withoutToken := role
	withoutToken.token = ""
	wantAnswer(t, "ListQueues with AssumeRole credentials without the token", sqsJSON(t, withoutToken, endpoint), 400, "UnrecognizedClientException")
	wrongToken := role
	wrongToken.token = "not-the-token"
	wantAnswer(t, "GetCallerIdentity with a wrong token", query(t, wrongToken, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}), 403, "InvalidClientTokenId")
}

// TestSigV4VerifyRefusesInEachFamilysWords: a wrong secret and an unknown
// key are refused with each protocol family's identity; S3's mismatch
// carries its six signing-debug elements.
func TestSigV4VerifyRefusesInEachFamilysWords(t *testing.T) {
	_, endpoint, _ := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/verified", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket with test/test = %d %s", got.status, got.body)
	}
	wrong := signer{key: "test", secret: "wrong-secret-value"}
	got := s3Call(t, wrong, http.MethodGet, endpoint+"/verified", "")
	wantAnswer(t, "S3 with a wrong secret", got, 403, "<Code>SignatureDoesNotMatch</Code>")
	for _, element := range []string{"AWSAccessKeyId", "StringToSign", "SignatureProvided", "StringToSignBytes", "CanonicalRequest", "CanonicalRequestBytes"} {
		if !strings.Contains(got.body, "<"+element+">") {
			t.Errorf("S3's SignatureDoesNotMatch lacks %s: %s", element, got.body)
		}
	}
	wantAnswer(t, "SQS JSON with a wrong secret", sqsJSON(t, wrong, endpoint), 400, "InvalidSignatureException")
	wantAnswer(t, "SQS Query with a wrong secret", query(t, wrong, endpoint, "sqs", url.Values{"Action": {"ListQueues"}, "Version": {"2012-11-05"}}), 403, "SignatureDoesNotMatch")

	unknown := signer{key: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"} //nolint:gosec // G101: AWS's published example key, not a credential
	wantAnswer(t, "S3 with an unknown key", s3Call(t, unknown, http.MethodGet, endpoint+"/verified", ""), 403, "<Code>InvalidAccessKeyId</Code>")
	wantAnswer(t, "SQS JSON with an unknown key", sqsJSON(t, unknown, endpoint), 400, "UnrecognizedClientException")
	wantAnswer(t, "STS with an unknown key", query(t, unknown, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}), 403, "InvalidClientTokenId")
}

// journalText is every event the journal holds, as the admin API serves
// them.
func journalText(t *testing.T, c *admin.Client) string {
	t.Helper()
	var b strings.Builder
	q := admin.JournalQuery{}
	for {
		page, err := c.JournalEvents(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(page.Events)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		if page.Next == 0 {
			return b.String()
		}
		q.Cursor = page.Next
	}
}

// wantNoCredential fails if the server log or the journal holds one of the
// credentials named, each of which the server minted or was sent.
func wantNoCredential(t *testing.T, c *admin.Client, logs *lockedBuffer, creds map[string]string) {
	t.Helper()
	journaled := journalText(t, c)
	if !strings.Contains(journaled, "GetCallerIdentity") || !strings.Contains(logs.String(), "request_id") {
		t.Fatalf("the journal or the debug log holds no calls, so the check proves nothing:\n%s\n%s", journaled, logs.String())
	}
	for what, cred := range creds {
		if strings.Contains(logs.String(), cred) {
			t.Errorf("the server log holds %s", what)
		}
		if strings.Contains(journaled, cred) {
			t.Errorf("the journal holds %s", what)
		}
	}
}

// expiredSession loads into the server behind c a snapshot whose minted
// keys are one AssumeRole session in the default account that expired a
// minute ago, and returns its credentials. STS stamps expiry in wall time,
// which no admin call moves, so a snapshot is how expired credentials
// reach a running server. The server must already hold a minted key, so
// its snapshot has an identity section to replace.
func expiredSession(t *testing.T, c *admin.Client) signer {
	t.Helper()
	var saved bytes.Buffer
	if _, err := c.SaveSnapshot(t.Context(), &saved, admin.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	archive, err := snapshot.Read(&saved, snapshot.ReadOptions{MaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	keys := identity.New(ids.NewSeeded(99), aws.DefaultAccount())
	role := identity.AssumedRole{Account: aws.DefaultAccount(), Role: identity.RoleRef{Name: "worker"}, Session: "expired", Source: identity.FromAssumeRole{}}
	creds, err := keys.MintTemporary(role, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	life, ok := creds.Lifetime.(identity.Temporary)
	if !ok {
		t.Fatalf("MintTemporary returned lifetime %#v", creds.Lifetime)
	}
	data, err := keys.Export()
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	for _, sections := range [][]store.Section{archive.Sections, archive.Late} {
		for i := range sections {
			if sections[i].Path == "services/identity/keys.json" {
				sections[i].Data, replaced = data, true
			}
		}
	}
	if !replaced {
		t.Fatal("the snapshot has no services/identity/keys.json section to replace")
	}
	var edited bytes.Buffer
	if err := snapshot.Write(&edited, *archive, snapshot.WriteOptions{Blobs: archive.Manifest.Blobs, MaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadSnapshot(t.Context(), &edited); err != nil {
		t.Fatal(err)
	}
	return signer{key: string(creds.AccessKeyID), secret: string(creds.SecretAccessKey), token: string(life.SessionToken)}
}

// TestSigV4VerifyRefusesTokensInEachFamilysWords: temporary credentials
// sent without their session token or with another are InvalidToken in S3
// (400, as S3's error list gives it), and expired ones ExpiredToken in S3
// (400), ExpiredTokenException in the JSON family and ExpiredToken in the
// Query family (403). Neither the debug log nor the journal ever holds a
// session token or a minted secret, of a call that verified or of one
// refused.
func TestSigV4VerifyRefusesTokensInEachFamilysWords(t *testing.T) {
	c, endpoint, logs := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/tokens", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	role := assumeRole(t, endpoint)
	if got := s3Call(t, role, http.MethodGet, endpoint+"/tokens", ""); got.status != http.StatusOK {
		t.Fatalf("ListObjects with AssumeRole credentials = %d %s", got.status, got.body)
	}
	withoutToken, wrongToken := role, role
	withoutToken.token, wrongToken.token = "", "not-the-token"
	wantAnswer(t, "S3 with AssumeRole credentials without the token", s3Call(t, withoutToken, http.MethodGet, endpoint+"/tokens", ""), 400, "<Code>InvalidToken</Code>")
	wantAnswer(t, "S3 with a wrong token", s3Call(t, wrongToken, http.MethodGet, endpoint+"/tokens", ""), 400, "<Code>InvalidToken</Code>")
	wrongSecret := role
	wrongSecret.secret = "not-the-secret"
	wantAnswer(t, "S3 with a wrong secret and the token", s3Call(t, wrongSecret, http.MethodGet, endpoint+"/tokens", ""), 403, "<Code>SignatureDoesNotMatch</Code>")
	wantAnswer(t, "STS with a wrong secret and the token", query(t, wrongSecret, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}), 403, "SignatureDoesNotMatch")
	if got := query(t, role, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}); got.status != http.StatusOK {
		t.Fatalf("GetCallerIdentity with AssumeRole credentials = %d %s", got.status, got.body)
	}
	wantNoCredential(t, c, logs, map[string]string{"the AssumeRole session token": role.token, "the AssumeRole secret": role.secret})

	expired := expiredSession(t, c)
	wantAnswer(t, "S3 with expired credentials", s3Call(t, expired, http.MethodGet, endpoint+"/tokens", ""), 400, "<Code>ExpiredToken</Code>")
	wantAnswer(t, "SQS JSON with expired credentials", sqsJSON(t, expired, endpoint), 403, "ExpiredTokenException")
	wantAnswer(t, "STS Query with expired credentials", query(t, expired, endpoint, "sts", url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}), 403, "<Code>ExpiredToken</Code>")
	wantNoCredential(t, c, logs, map[string]string{"the expired session token": expired.token, "the expired secret": expired.secret})
}

// TestSigV4VerifyCoversRawRoutesAndLinks: a signed request to a raw
// operation (S3 Control's ListTagsForResource on the account host) or to a
// link (a function URL by path) is verified like any other: the dummy key
// reaches the handler, and a wrong secret is refused in the service's
// words, S3 Control's wrapped error or Lambda's JSON, and journaled as a
// failed call.
func TestSigV4VerifyCoversRawRoutesAndLinks(t *testing.T) {
	c, endpoint, _ := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/tagged", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	listTags := func(s signer) reply {
		req := newRequest(t, http.MethodGet, endpoint+"/v20180820/tags/arn:aws:s3:::tagged", "")
		req.Host = "000000000000.s3-control.localhost:" + u.Port()
		req.Header.Set("X-Amz-Account-Id", "000000000000")
		auth.Sign(req, nil, s.scoped("s3"), time.Now())
		return sigv4Do(t, req)
	}
	functionURL := func(s signer) reply {
		req := newRequest(t, http.MethodGet, endpoint+"/_understudy/lambda-url/abcdefghijklmnopqrstuvwxyz012345/", "")
		auth.Sign(req, nil, s.scoped("lambda"), time.Now())
		return sigv4Do(t, req)
	}
	wrong := signer{key: "test", secret: "wrong-secret-value"}
	wantAnswer(t, "S3 Control ListTagsForResource with test/test", listTags(dummy), 200, "<ListTagsForResourceResult")
	// An unknown function URL is 403 {"Message":null}, AWS's answer for a
	// URL that names no function: the request passed verification.
	wantAnswer(t, "a function URL with test/test", functionURL(dummy), 403, `{"Message":null}`)
	wantAnswer(t, "S3 Control ListTagsForResource with a wrong secret", listTags(wrong), 403, "<ErrorResponse><Error><Type>Sender</Type><Code>SignatureDoesNotMatch</Code>")
	wantAnswer(t, "a function URL with a wrong secret", functionURL(wrong), 400, "The request signature we calculated does not match")

	page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "api-call", Outcome: "error"})
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string]string{}
	for _, e := range page.Events {
		var call struct {
			Operation string `json:"operation"`
			Outcome   struct {
				Identity struct {
					Shape string `json:"shape"`
				} `json:"identity"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(e.Detail, &call); err != nil {
			t.Fatal(err)
		}
		refused[call.Operation] = call.Outcome.Identity.Shape
	}
	want := map[string]string{
		"ListTagsForResource": "com.amazonaws.s3#SignatureDoesNotMatch",
		"InvokeFunctionUrl":   "com.amazon.coral.service#InvalidSignatureException",
	}
	if !maps.Equal(refused, want) {
		t.Errorf("journaled failed calls = %v, want %v", refused, want)
	}
}

// presignedGet is a SigV4 presigned GET of target valid for expires.
func presignedGet(t *testing.T, s signer, target string, expires time.Duration) string {
	t.Helper()
	req := newRequest(t, http.MethodGet, target, "")
	auth.Presign(req, s.scoped("s3"), expires, time.Now())
	return req.URL.String()
}

// hmacV1 is botocore HmacV1Auth's signature of stringToSign.
func hmacV1(secret, stringToSign string) string {
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// tokenLine is the canonical x-amz-security-token line of a SigV2 string
// to sign, empty for long-term credentials.
func (s signer) tokenLine() string {
	if s.token == "" {
		return ""
	}
	return "x-amz-security-token:" + s.token + "\n"
}

// sigV2Presigned is a SigV2 presigned GET of endpoint/path expiring at
// expires, as boto3 builds one in the legacy regions: a session token is
// signed as an x-amz-* header and sent as a query parameter.
func sigV2Presigned(endpoint, path string, s signer, expires time.Time) string {
	stamp := strconv.FormatInt(expires.Unix(), 10)
	sig := hmacV1(s.secret, "GET\n\n\n"+stamp+"\n"+s.tokenLine()+path)
	target := endpoint + path + "?AWSAccessKeyId=" + url.QueryEscape(s.key) + "&Signature=" + url.QueryEscape(sig) + "&Expires=" + stamp
	if s.token != "" {
		target += "&x-amz-security-token=" + url.QueryEscape(s.token)
	}
	return target
}

// sigV2Header is a GET of target signed in the Authorization header as
// botocore's HmacV1Auth signs it, over resource, the path-style
// /bucket/key, with any session token sent and signed as a header. host,
// when not empty, is the Host header, for a virtual-hosted request.
func sigV2Header(t *testing.T, s signer, target, host, resource string) *http.Request {
	t.Helper()
	req := newRequest(t, http.MethodGet, target, "")
	if host != "" {
		req.Host = host
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	if s.token != "" {
		req.Header.Set("X-Amz-Security-Token", s.token)
	}
	req.Header.Set("Authorization", "AWS "+s.key+":"+hmacV1(s.secret, "GET\n\n\n"+date+"\n"+s.tokenLine()+resource))
	return req
}

// s3GetForms are the ways a client signs a GET of bucket/key as s: SigV4
// in the header or the query, and SigV2 in the header, path-style and
// virtual-hosted, or the query.
func s3GetForms(t *testing.T, s signer, endpoint, bucket, key string) map[string]*http.Request {
	t.Helper()
	path := "/" + bucket + "/" + key
	v4 := newRequest(t, http.MethodGet, endpoint+path, "")
	auth.Sign(v4, nil, s.scoped("s3"), time.Now())
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*http.Request{
		"SigV4 header":                 v4,
		"SigV4 presigned":              newRequest(t, http.MethodGet, presignedGet(t, s, endpoint+path, time.Minute), ""),
		"SigV2 header, path-style":     sigV2Header(t, s, endpoint+path, "", path),
		"SigV2 header, virtual-hosted": sigV2Header(t, s, endpoint+"/"+key, bucket+".s3.localhost:"+u.Port(), path),
		"SigV2 presigned":              newRequest(t, http.MethodGet, sigV2Presigned(endpoint, path, s, time.Now().Add(time.Minute)), ""),
	}
}

// TestSigV4VerifyAcceptsEveryFormForEveryKey: under sigv4 verify each
// credential form S3 accepts (SigV4 header and presigned, SigV2 header
// path-style and virtual-hosted, SigV2 presigned) verifies with each kind
// of key that holds a secret: a minted long-term key, AssumeRole
// credentials with their token, an accounts.keys entry, the dummy key and
// a 12-digit key.
func TestSigV4VerifyAcceptsEveryFormForEveryKey(t *testing.T) {
	ci, err := aws.ParseAccountID("222222222222")
	if err != nil {
		t.Fatal(err)
	}
	_, endpoint, _ := sigv4Server(t, server.Config{
		SigV4:      server.SigV4Verify,
		StaticKeys: []server.StaticKey{{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: ci}},
	})
	for _, k := range []struct {
		name string
		s    signer
	}{
		{"minted", accessKey(t, endpoint, "developer")},
		{"assumed", assumeRole(t, endpoint)},
		{"static", signer{key: "CIKEY0000000000001", secret: "ci-secret"}},
		{"dummy", dummy},
		{"account", signer{key: "111122223333", secret: "test"}},
	} {
		bucket := "forms-" + k.name
		if got := s3Call(t, k.s, http.MethodPut, endpoint+"/"+bucket, ""); got.status != http.StatusOK {
			t.Fatalf("CreateBucket as the %s key = %d %s", k.name, got.status, got.body)
		}
		if got := s3Call(t, k.s, http.MethodPut, endpoint+"/"+bucket+"/key", "body-"+k.name); got.status != http.StatusOK {
			t.Fatalf("PutObject as the %s key = %d %s", k.name, got.status, got.body)
		}
		for form, req := range s3GetForms(t, k.s, endpoint, bucket, "key") {
			if got := sigv4Do(t, req); got.status != http.StatusOK || got.body != "body-"+k.name {
				t.Errorf("%s GET as the %s key = %d %s", form, k.name, got.status, got.body)
			}
		}
	}
}

func fetch(t *testing.T, target string, header http.Header) reply {
	t.Helper()
	req := newRequest(t, http.MethodGet, target, "")
	maps.Copy(req.Header, header)
	return sigv4Do(t, req)
}

// TestSigV4VerifyExpiresPresignedURLsOnTheClock: a SigV4 or SigV2
// presigned URL is served until the emulator clock passes its expiry, then
// refused with S3's AccessDenied "Request has expired" and its elements; a
// tampered URL is SignatureDoesNotMatch, a missing parameter or an
// X-Amz-Expires outside 1..604800 AuthorizationQueryParametersError, and an
// unsigned x-amz-* header AccessDenied (s3.md 12).
func TestSigV4VerifyExpiresPresignedURLsOnTheClock(t *testing.T) {
	c, endpoint, _ := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/presigned", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/presigned/key", "object-body"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	v4 := presignedGet(t, dummy, endpoint+"/presigned/key", time.Minute)
	v2 := sigV2Presigned(endpoint, "/presigned/key", dummy, time.Now().Add(time.Minute))
	for name, target := range map[string]string{"SigV4": v4, "SigV2": v2} {
		if got := fetch(t, target, nil); got.status != http.StatusOK || got.body != "object-body" {
			t.Errorf("%s presigned GET before expiry = %d %s", name, got.status, got.body)
		}
	}

	tampered := strings.Replace(v4, "X-Amz-Signature=", "X-Amz-Signature=0", 1)
	wantAnswer(t, "a tampered presigned URL", fetch(t, tampered, nil), 403, "<Code>SignatureDoesNotMatch</Code>")
	missing := regexp.MustCompile(`&X-Amz-Signature=[0-9a-f]+`).ReplaceAllString(v4, "")
	wantAnswer(t, "a presigned URL without X-Amz-Signature", fetch(t, missing, nil), 400, "<Code>AuthorizationQueryParametersError</Code>")
	for _, expires := range []string{"0", "604801"} {
		outOfRange := regexp.MustCompile(`X-Amz-Expires=\d+`).ReplaceAllString(v4, "X-Amz-Expires="+expires)
		wantAnswer(t, "X-Amz-Expires="+expires, fetch(t, outOfRange, nil), 400, "<Code>AuthorizationQueryParametersError</Code>")
	}
	unsigned := fetch(t, v4, http.Header{"X-Amz-Meta-Extra": {"1"}})
	wantAnswer(t, "an unsigned x-amz-* header", unsigned, 403, "<Code>AccessDenied</Code>")
	if !strings.Contains(unsigned.body, "<HeadersNotSigned>x-amz-meta-extra</HeadersNotSigned>") {
		t.Errorf("the unsigned-header refusal does not name the header: %s", unsigned.body)
	}

	if _, err := c.ChangeClock(t.Context(), admin.Advance{By: 2 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	expired := fetch(t, v4, nil)
	wantAnswer(t, "SigV4 presigned GET after expiry", expired, 403, "<Code>AccessDenied</Code>")
	for _, want := range []string{"<Message>Request has expired</Message>", "<X-Amz-Expires>60</X-Amz-Expires>", "<Expires>", "<ServerTime>"} {
		if !strings.Contains(expired.body, want) {
			t.Errorf("the expired SigV4 URL's answer lacks %s: %s", want, expired.body)
		}
	}
	expiredV2 := fetch(t, v2, nil)
	wantAnswer(t, "SigV2 presigned GET after expiry", expiredV2, 403, "<Code>AccessDenied</Code>")
	if !strings.Contains(expiredV2.body, "Request has expired") {
		t.Errorf("the expired SigV2 URL's answer: %s", expiredV2.body)
	}
}

// A SigV4 URL's lifetime counts from the emulator instant it was signed
// at: one presigned after the clock moved an hour ahead is served, and
// expires once the clock passes its lifetime, whether the clock is frozen
// or running.
func TestSigV4VerifyCountsPresignedLifetimesOnTheEmulatorTimeline(t *testing.T) {
	c, endpoint, _ := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/timeline", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/timeline/key", "object-body"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	for _, mode := range []string{"frozen", "running"} {
		if mode == "running" {
			if _, err := c.ChangeClock(t.Context(), admin.Resume{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.ChangeClock(t.Context(), admin.Advance{By: time.Hour}); err != nil {
			t.Fatal(err)
		}
		target := presignedGet(t, dummy, endpoint+"/timeline/key", 5*time.Minute)
		if got := fetch(t, target, nil); got.status != http.StatusOK || got.body != "object-body" {
			t.Errorf("%s: a URL presigned after a one-hour advance = %d %s, want it served", mode, got.status, got.body)
		}
		if _, err := c.ChangeClock(t.Context(), admin.Advance{By: 4 * time.Minute}); err != nil {
			t.Fatal(err)
		}
		if got := fetch(t, target, nil); got.status != http.StatusOK {
			t.Errorf("%s: the URL a minute before its expiry = %d %s, want it served", mode, got.status, got.body)
		}
		if _, err := c.ChangeClock(t.Context(), admin.Advance{By: 2 * time.Minute}); err != nil {
			t.Fatal(err)
		}
		wantAnswer(t, mode+": the URL past its lifetime", fetch(t, target, nil), 403, "<Message>Request has expired</Message>")
	}
}

// Credentials that cannot be read refuse a raw route (S3 Control tagging)
// or a link (a function URL) in both modes, journaled as a failed call,
// and the handler never runs, so nothing is written.
func TestUnreadableCredentialsRefuseRawRoutesAndLinks(t *testing.T) {
	for mode, sigv4 := range map[string]server.SigV4Mode{"parse": server.SigV4ParseOnly, "verify": server.SigV4Verify} {
		c, endpoint, _ := sigv4Server(t, server.Config{SigV4: sigv4})
		if got := s3Call(t, dummy, http.MethodPut, endpoint+"/probe", ""); got.status != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", got.status, got.body)
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		control := func(method, authz, body string) reply {
			req := newRequest(t, method, endpoint+"/v20180820/tags/arn:aws:s3:::probe", body)
			req.Host = "000000000000.s3-control.localhost:" + u.Port()
			req.Header.Set("X-Amz-Account-Id", "000000000000")
			if authz != "" {
				req.Header.Set("Authorization", authz)
			} else {
				auth.Sign(req, []byte(body), dummy.scoped("s3"), time.Now())
			}
			return sigv4Do(t, req)
		}
		const garbage = "AWS4-HMAC-SHA256 Credential=test/garbage, SignedHeaders=host, Signature=00"
		tags := `<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags><Tag><Key>planted</Key><Value>yes</Value></Tag></Tags></TagResourceRequest>`
		wantAnswer(t, mode+": TagResource with unreadable credentials", control(http.MethodPost, garbage, tags), 400, "<Code>AuthorizationHeaderMalformed</Code>")
		wantAnswer(t, mode+": ListTagsForResource with a Bearer header", control(http.MethodGet, "Bearer x", ""), 400, "<Code>InvalidArgument</Code>")
		functionURL := newRequest(t, http.MethodGet, endpoint+"/_understudy/lambda-url/abcdefghijklmnopqrstuvwxyz012345/", "")
		functionURL.Header.Set("Authorization", garbage)
		wantAnswer(t, mode+": a function URL with unreadable credentials", sigv4Do(t, functionURL), 400, "Authorization header requires existence of either a 'X-Amz-Date' or a 'Date' header")

		if got := control(http.MethodGet, "", ""); got.status != http.StatusOK || strings.Contains(got.body, "planted") {
			t.Errorf("%s: the tags after the refused TagResource = %d %s, want none written", mode, got.status, got.body)
		}
		page, err := c.JournalEvents(t.Context(), admin.JournalQuery{Kind: "api-call", Outcome: "error"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 3 {
			t.Errorf("%s: journaled %d failed calls, want the 3 refusals", mode, len(page.Events))
		}
	}
}

// swapped is a header-signed S3 request whose x-amz-content-sha256 is the
// hash of signedBody but which sends sentBody, with header added unsigned
// after signing.
func swapped(t *testing.T, method, target, signedBody, sentBody string, header http.Header) *http.Request {
	t.Helper()
	req := newRequest(t, method, target, signedBody)
	auth.Sign(req, []byte(signedBody), dummy.scoped("s3"), time.Now())
	maps.Copy(req.Header, header)
	req.Body = io.NopCloser(strings.NewReader(sentBody))
	req.ContentLength = int64(len(sentBody))
	return req
}

// Under sigv4 verify an S3 body that S3 does not stream is bound to the
// signature: a PutBucketTagging or DeleteObjects body other than the one
// signed is XAmzContentSHA256Mismatch and changes nothing.
func TestSigV4VerifyBindsS3BodiesToTheSignature(t *testing.T) {
	_, endpoint, _ := sigv4Server(t, server.Config{SigV4: server.SigV4Verify})
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/bound", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket = %d %s", got.status, got.body)
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/bound/victim", "keep me"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	tagging := func(value string) string {
		return `<Tagging><TagSet><Tag><Key>owner</Key><Value>` + value + `</Value></Tag></TagSet></Tagging>`
	}
	wantAnswer(t, "PutBucketTagging with a swapped body",
		sigv4Do(t, swapped(t, http.MethodPut, endpoint+"/bound?tagging", tagging("a"), tagging("b"), nil)), 400, "<Code>XAmzContentSHA256Mismatch</Code>")
	wantAnswer(t, "GetBucketTagging after the refusal", s3Call(t, dummy, http.MethodGet, endpoint+"/bound?tagging", ""), 404, "<Code>NoSuchTagSet</Code>")

	deletes := func(key string) string {
		return `<Delete><Object><Key>` + key + `</Key></Object></Delete>`
	}
	sent := deletes("victim")
	sum := md5.Sum([]byte(sent)) //nolint:gosec // see the import
	md5Header := http.Header{"Content-Md5": {base64.StdEncoding.EncodeToString(sum[:])}}
	wantAnswer(t, "DeleteObjects with a swapped body",
		sigv4Do(t, swapped(t, http.MethodPost, endpoint+"/bound?delete", deletes("other"), sent, md5Header)), 400, "<Code>XAmzContentSHA256Mismatch</Code>")
	if got := s3Call(t, dummy, http.MethodGet, endpoint+"/bound/victim", ""); got.status != http.StatusOK || got.body != "keep me" {
		t.Errorf("the object the swapped body named = %d %s, want it kept", got.status, got.body)
	}

	// S3 Control's raw TagResource is S3's too, and as bound.
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	controlTags := func(value string) string {
		return `<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags><Tag><Key>owner</Key><Value>` + value + `</Value></Tag></Tags></TagResourceRequest>`
	}
	control := func(req *http.Request) *http.Request {
		req.Host = "000000000000.s3-control.localhost:" + u.Port()
		req.Header.Set("X-Amz-Account-Id", "000000000000")
		return req
	}
	tagResource := newRequest(t, http.MethodPost, endpoint+"/v20180820/tags/arn:aws:s3:::bound", controlTags("a"))
	auth.Sign(control(tagResource), []byte(controlTags("a")), dummy.scoped("s3"), time.Now())
	tagResource.Body, tagResource.ContentLength = io.NopCloser(strings.NewReader(controlTags("b"))), int64(len(controlTags("b")))
	wantAnswer(t, "S3 Control TagResource with a swapped body", sigv4Do(t, tagResource), 400, "<Code>XAmzContentSHA256Mismatch</Code>")
	listTags := control(newRequest(t, http.MethodGet, endpoint+"/v20180820/tags/arn:aws:s3:::bound", ""))
	auth.Sign(listTags, nil, dummy.scoped("s3"), time.Now())
	if got := sigv4Do(t, listTags); got.status != http.StatusOK || strings.Contains(got.body, "owner") {
		t.Errorf("the tags after the refused TagResource = %d %s, want none written", got.status, got.body)
	}
}

// Under sigv4 parse nothing is checked, as before: a URL past its expiry
// and a request with a wrong secret are both served.
func TestSigV4ParseChecksNothing(t *testing.T) {
	c, endpoint, _ := sigv4Server(t, server.Config{})
	wrong := signer{key: "AKIAIOSFODNN7EXAMPLE", secret: "anything"} //nolint:gosec // G101: AWS's published example key, not a credential
	if got := s3Call(t, wrong, http.MethodPut, endpoint+"/parsed", ""); got.status != http.StatusOK {
		t.Fatalf("CreateBucket with an unknown key under parse = %d %s", got.status, got.body)
	}
	v4 := presignedGet(t, dummy, endpoint+"/parsed", time.Minute)
	if _, err := c.ChangeClock(t.Context(), admin.Advance{By: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(t, v4, nil); got.status != http.StatusOK {
		t.Errorf("an expired presigned URL under parse = %d %s", got.status, got.body)
	}
}

// S3 answers credentials it cannot read in its own words in both modes.
func TestS3ParseFailuresAnswerInS3sWords(t *testing.T) {
	for _, mode := range []server.SigV4Mode{server.SigV4ParseOnly, server.SigV4Verify} {
		_, endpoint, _ := sigv4Server(t, server.Config{SigV4: mode})
		req := newRequest(t, http.MethodGet, endpoint+"/any", "")
		auth.Sign(req, nil, dummy.scoped("s3"), time.Now())
		req.Header.Del("X-Amz-Content-Sha256")
		wantAnswer(t, "a header-signed request without x-amz-content-sha256", sigv4Do(t, req), 400, "<Code>InvalidRequest</Code>")
		wantAnswer(t, "a presigned URL missing parameters", fetch(t, endpoint+"/any?X-Amz-Algorithm=AWS4-HMAC-SHA256", nil), 400, "<Code>AuthorizationQueryParametersError</Code>")
		wantAnswer(t, "an unknown Authorization scheme", fetch(t, endpoint+"/any", http.Header{"Authorization": {"Bearer x"}}), 400, "<Code>InvalidArgument</Code>")
	}
}
