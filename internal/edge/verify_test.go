package edge_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	s3svc "github.com/sustinbebustin/understudy/internal/service/s3"
)

// verifyingFront is the edge over SQS with sigv4 verify on, and the
// identity store it verifies against.
func verifyingFront(t *testing.T) (*front, *identity.Store) {
	t.Helper()
	var store *identity.Store
	f := newFrontWith(t, nil, func(o *edge.Options) {
		o.Verify = true
		store = o.Identity
	})
	return f, store
}

// signedSQS is a ListQueues call signed with key and secret, carrying
// token as X-Amz-Security-Token when it is not empty.
func signedSQS(t *testing.T, key, secret, token string) *http.Request {
	t.Helper()
	const body = "{}"
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(body))
	maps.Copy(r.Header, jsonHeader("AmazonSQS.ListQueues", "application/x-amz-json-1.0"))
	auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: key, SecretAccessKey: secret, SessionToken: token, Region: aws.DefaultRegion(), Service: "sqs"}, time.Now())
	return r
}

// The forms of credentials that hold a secret verify under sigv4 verify:
// a minted long-term key, minted temporary credentials with their token, a
// static key, the dummy pair test/test and a 12-digit key with secret
// test (ADR 0012 pre-tag review amendment).
func TestVerifyAcceptsEveryKeyWithASecret(t *testing.T) {
	f, store := verifyingFront(t)
	acct, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	long, err := store.MintLongTerm(identity.Root{Account: acct})
	if err != nil {
		t.Fatal(err)
	}
	role := identity.AssumedRole{Account: acct, Role: identity.RoleRef{Name: "worker"}, Session: "s1", Source: identity.FromAssumeRole{}}
	temp, err := store.MintTemporary(role, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	life, ok := temp.Lifetime.(identity.Temporary)
	if !ok {
		t.Fatalf("temporary credentials have lifetime %#v", temp.Lifetime)
	}
	if err := store.SetStaticKeys([]identity.StaticKey{{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, secret, token string }{
		{"minted long-term", string(long.AccessKeyID), string(long.SecretAccessKey), ""},
		{"minted temporary", string(temp.AccessKeyID), string(temp.SecretAccessKey), string(life.SessionToken)},
		{"static", "CIKEY0000000000001", "ci-secret", ""},
		{"dummy", "test", "test", ""},
		{"12-digit", "111122223333", "test", ""},
	} {
		if got := f.serve(t, signedSQS(t, tc.key, tc.secret, tc.token)); !strings.HasPrefix(got, "200\n") {
			t.Errorf("%s key: %s", tc.name, got)
		}
	}
}

// Credentials that do not verify are refused in the family's words and
// journaled as a failed call (ARCHITECTURE.md 4 step 4a), but record no
// IAM action for the principal they claim: on AWS such a call never
// reaches authorization.
func TestVerifyRefusals(t *testing.T) {
	f, store := verifyingFront(t)
	acct, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	role := identity.AssumedRole{Account: acct, Role: identity.RoleRef{Name: "worker"}, Session: "s1", Source: identity.FromAssumeRole{}}
	live, err := store.MintTemporary(role, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := store.MintTemporary(role, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tokenOf := func(c identity.Credentials) string {
		life, ok := c.Lifetime.(identity.Temporary)
		if !ok {
			t.Fatalf("credentials have lifetime %#v", c.Lifetime)
		}
		return string(life.SessionToken)
	}
	tampered := signedSQS(t, "test", "test", "")
	tampered.Header.Set("Authorization", tampered.Header.Get("Authorization")[:len(tampered.Header.Get("Authorization"))-4]+"0000")
	for _, tc := range []struct {
		name string
		r    *http.Request
		code string
	}{
		{"wrong secret", signedSQS(t, "test", "not-test", ""), "InvalidSignatureException"},
		{"tampered signature", tampered, "InvalidSignatureException"},
		{"unknown key", signedSQS(t, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", ""), "UnrecognizedClientException"},
		{"missing session token", signedSQS(t, string(live.AccessKeyID), string(live.SecretAccessKey), ""), "UnrecognizedClientException"},
		{"wrong session token", signedSQS(t, string(live.AccessKeyID), string(live.SecretAccessKey), "not-the-token"), "UnrecognizedClientException"},
		{"expired temporary key", signedSQS(t, string(expired.AccessKeyID), string(expired.SecretAccessKey), tokenOf(expired)), "ExpiredTokenException"},
	} {
		got := f.serve(t, tc.r)
		if !strings.Contains(got, "com.amazon.coral.service#"+tc.code) {
			t.Errorf("%s: %s; want %s", tc.name, got, tc.code)
		}
		for _, secret := range []string{"not-test", tokenOf(live), tokenOf(expired), string(live.SecretAccessKey)} {
			if strings.Contains(got, secret) {
				t.Errorf("%s: the answer holds a credential: %s", tc.name, got)
			}
		}
	}
	calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
	if len(calls) != 6 {
		t.Fatalf("journaled %d API calls, want 6", len(calls))
	}
	for _, e := range calls {
		call, ok := e.Detail.(journal.APICall)
		if !ok {
			t.Fatalf("journal event %+v is not an APICall", e.Detail)
		}
		if _, failed := call.Outcome.(journal.Failed); !failed {
			t.Errorf("%s was journaled as %#v, want Failed", call.Op, call.Outcome)
		}
	}
	if uses := f.access.Usage(access.Filter{}); len(uses) != 0 {
		t.Errorf("recorded %+v for calls refused at authentication, want nothing", uses)
	}
}

// swapBody signs r over signed, with its hash sent in
// x-amz-content-sha256, for service, then makes r send sent instead.
func swapBody(t *testing.T, r *http.Request, service, signed, sent string) *http.Request {
	t.Helper()
	sum := sha256.Sum256([]byte(signed))
	r.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	auth.Sign(r, []byte(signed), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: service}, time.Now())
	r.Body = io.NopCloser(strings.NewReader(sent))
	r.ContentLength = int64(len(sent))
	return r
}

// The edge hashes a body and refuses one that differs from the hash it was
// signed with, in the service's words.
func TestVerifyBindsTheBodyToTheSignature(t *testing.T) {
	f, _ := verifyingFront(t)
	if got := f.serve(t, swapBody(t, signedSQS(t, "test", "test", ""), "sqs", "{}", "{ }")); !strings.Contains(got, "com.amazon.coral.service#InvalidSignatureException") {
		t.Errorf("a body that differs from its signed hash: %s; want InvalidSignatureException", got)
	}
}

// s3Put is a one-route REST service standing in for S3: PUT /{Bucket},
// whose input streams a blob when streaming is set, as PutObject's does,
// and is an XML document otherwise, as PutBucketTagging's is.
func s3Put(streaming bool) *registry.Service {
	svc := &registry.Service{ID: "s3", SigningName: "s3", IAMPrefix: "s3", ARNNamespace: "s3", Protocols: []ref.ProtocolID{ref.RESTXML}}
	body := &smithy.Schema{ID: "com.amazonaws.s3#Tagging", Type: smithy.TypeStructure}
	if streaming {
		body = &smithy.Schema{ID: "com.amazonaws.s3#StreamingBlob", Type: smithy.TypeBlob, Streaming: true}
	}
	input := &smithy.Schema{ID: "com.amazonaws.s3#PutInput", Type: smithy.TypeStructure, Members: []*smithy.Member{{Name: "Body", Target: body}}}
	op := &registry.Operation{Service: svc, ID: ref.OperationID{Service: "s3", Name: "Put"}, Kind: registry.Modeled, Input: input}
	svc.Operations = []*registry.Operation{op}
	svc.Routes = &registry.RouteTable{Routes: []*registry.Route{{
		Method: http.MethodPut, Path: []registry.Segment{registry.Label{Name: "Bucket"}},
		Operation: op,
	}}}
	return svc
}

// cappedXML is xmlCodec with a body cap, which the edge needs to read a
// body it hashes.
type cappedXML struct{ xmlCodec }

func (cappedXML) BodyLimit(*registry.Operation) int64 { return 1 << 20 }

// A service that checks the payload itself (ServiceChecksPayload, as S3)
// gets the body of an operation that streams its input unread: the edge
// verifies the signature over the x-amz-content-sha256 the request sent and
// leaves comparing it with the bytes to the service. Every other operation
// of that service is hashed by the edge, so its body is bound to the
// signature: a swapped one is refused with the service's
// XAmzContentSHA256Mismatch and journaled.
func TestVerifyLeavesOnlyStreamedInputsToTheService(t *testing.T) {
	vocabulary, refuse := s3svc.AuthDialect()
	for _, streaming := range []bool{true, false} {
		f := newFrontWith(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: cappedXML{}}, func(o *edge.Options) {
			o.Verify = true
			o.ServiceAuth = map[ref.ServiceID]edge.ServiceAuth{"s3": {Vocabulary: vocabulary, ParseError: refuse, Payload: edge.ServiceChecksPayload}}
		}, s3Put(streaming))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "http://localhost:4566/bucket", nil)
		got := f.serve(t, swapBody(t, r, "s3", "<Tagging>A</Tagging>", "<Tagging>B</Tagging>"))
		if streaming {
			// The operation is unbound, so the kernel answers 501 once
			// the request passed verification.
			if !strings.HasPrefix(got, "501\n") {
				t.Errorf("a streamed input with a swapped body: %s; want it left to the service", got)
			}
			continue
		}
		if !strings.HasPrefix(got, "400\n") {
			t.Errorf("an XML input with a swapped body: %s; want 400", got)
		}
		calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
		if len(calls) != 1 {
			t.Fatalf("journaled %d API calls, want 1", len(calls))
		}
		if call, ok := calls[0].Detail.(journal.APICall); !ok {
			t.Errorf("journal event %+v is not an APICall", calls[0].Detail)
		} else if failed, ok := call.Outcome.(journal.Failed); !ok || failed.Identity.Shape != "com.amazonaws.s3#XAmzContentSHA256Mismatch" {
			t.Errorf("the call ended %#v, want S3's XAmzContentSHA256Mismatch", call.Outcome)
		}
	}
}

// Credentials that cannot be read refuse a raw route or a link in either
// mode, before its handler runs, and the refusal is journaled; an
// Authorization header in no AWS scheme reaches a link unsigned, since a
// function URL's app may read one of its own.
func TestUnreadableCredentialsRefuseRawRoutesAndLinks(t *testing.T) {
	for _, verify := range []bool{false, true} {
		f := newFrontWith(t, nil, func(o *edge.Options) { o.Verify = verify }, lambdaLinks())
		for _, authz := range []string{"AWS4-HMAC-SHA256 Credential=test/garbage, SignedHeaders=host, Signature=00", "AWS4-HMAC-SHA256"} {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:4566/_understudy/lambda-url/abc123/", nil)
			r.Header.Set("Authorization", authz)
			r.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
			if got := f.serve(t, r); !strings.HasPrefix(got, "400\n") || !strings.Contains(got, "\n\n") {
				t.Errorf("verify %t, %q: %s; want a 400 refusal, never the handler's 501", verify, authz, got)
			}
		}
		calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
		if len(calls) != 2 {
			t.Fatalf("verify %t: journaled %d API calls, want 2", verify, len(calls))
		}
		for _, e := range calls {
			if call, ok := e.Detail.(journal.APICall); !ok {
				t.Errorf("journal event %+v is not an APICall", e.Detail)
			} else if _, failed := call.Outcome.(journal.Failed); !failed {
				t.Errorf("%s was journaled as %#v, want Failed", call.Op, call.Outcome)
			}
		}
		bearer := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:4566/_understudy/lambda-url/abc123/", nil)
		bearer.Header.Set("Authorization", "Bearer app-token")
		if got := f.serve(t, bearer); !strings.HasPrefix(got, "501\n") {
			t.Errorf("verify %t: a function URL with a Bearer header = %s; want the unbound handler's 501", verify, got)
		}
	}
}

// A signed request to a raw operation whose service has no codec cannot
// have its body capped, so under sigv4 verify it is refused as an internal
// failure (RawCodecError) in plain text and journaled; it is never served
// unverified.
func TestVerifyRefusesARawRouteWithoutACodec(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.Verify = true }, lambdaLinks())
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:4566/_understudy/lambda-url/abc123/", nil)
	auth.Sign(r, nil, auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: "lambda"}, time.Now())
	got := f.serve(t, r)
	if !strings.HasPrefix(got, "500\n") || !strings.Contains(got, "\n\nThe request processing has failed because of an unknown error") {
		t.Errorf("signed function URL without a Lambda codec: %q; want a plain-text 500 internal failure, not the unbound operation's 501", got)
	}
	calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
	if len(calls) != 1 {
		t.Fatalf("journaled %d API calls, want 1", len(calls))
	}
	if call, ok := calls[0].Detail.(journal.APICall); !ok {
		t.Errorf("journal event %+v is not an APICall", calls[0].Detail)
	} else if _, failed := call.Outcome.(journal.Failed); !failed {
		t.Errorf("%s was journaled as %#v, want Failed", call.Op, call.Outcome)
	}
}

// Under sigv4 parse nothing is verified: a wrong secret and an unknown key
// are served.
func TestParseOnlyServesBadSignatures(t *testing.T) {
	f := newFront(t, nil)
	for name, r := range map[string]*http.Request{
		"wrong secret": signedSQS(t, "test", "not-test", ""),
		"unknown key":  signedSQS(t, "AKIAIOSFODNN7EXAMPLE", "secret", ""),
	} {
		if got := f.serve(t, r); !strings.HasPrefix(got, "200\n") {
			t.Errorf("%s under parse: %s", name, got)
		}
	}
}

// Verification needs the secrets the identity store holds.
func TestVerifyNeedsAnIdentityStore(t *testing.T) {
	var o edge.Options
	newFrontWith(t, nil, func(opts *edge.Options) { o = *opts })
	o.Verify, o.Identity = true, nil
	_, err := edge.New(o)
	if refusal, ok := errors.AsType[*edge.OptionsError](err); !ok || refusal.Field != "Identity" {
		t.Errorf("New with Verify and no Identity = %v, want an OptionsError naming Identity", err)
	}
}
