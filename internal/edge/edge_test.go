package edge_test

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

var update = flag.Bool("update", false, "rewrite the wire goldens in testdata")

// dynamodb stands for a JSON service that speaks no other protocol, the
// one the F7 rows were recorded against.
func dynamodb() *registry.Service {
	return &registry.Service{
		ID:               "dynamodb",
		Shape:            "com.amazonaws.dynamodb#DynamoDB_20120810",
		SigningName:      "dynamodb",
		IAMPrefix:        "dynamodb",
		ARNNamespace:     "dynamodb",
		Protocols:        []ref.ProtocolID{ref.AWSJSON10},
		JSONTargetPrefix: "DynamoDB_20120810",
	}
}

// front is the edge over a kernel with SQS bound, a synthetic DynamoDB and
// an admin handler that records what reached it.
type front struct {
	handler *edge.Handler
	journal *journal.Journal
	admin   *recorder
}

// recorder is an http.Handler that keeps the request line it saw.
type recorder struct {
	mu        sync.Mutex
	uri, path string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.uri, r.path = req.RequestURI, req.URL.EscapedPath()
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *recorder) seen() (uri, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.uri, r.path
}

func newFront(t *testing.T, codecs map[ref.ProtocolID]edge.Codec, extra ...*registry.Service) *front {
	t.Helper()
	timers := kernel.NewTimers()
	at, err := time.Parse(time.RFC3339, "2026-10-09T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Start: clock.FromWire(at), Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(append([]*registry.Service{sqsapi.Service(), dynamodb()}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(3)
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	idStore := identity.New(src, aws.DefaultAccount())
	sqs.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(sqsapi.Service()), Clock: clk, IDs: src, Identity: idStore}, sqs.Options{})
	if err := k.BindErr(); err != nil {
		t.Fatal(err)
	}
	if codecs == nil {
		codecs = map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: server.JSONCodec(awsjson.V10), ref.AWSJSON11: server.JSONCodec(awsjson.V11)}
	}
	admin := &recorder{}
	h, err := edge.New(edge.Options{
		Kernel: k, Registry: reg, Identity: idStore, Regions: aws.KnownRegions(), Codecs: codecs,
		S3BaseDomains: []string{"localhost"}, Listener: ref.ListenerMain, Admin: admin, Unclaimed: ref.AWSJSON10,
		UnknownOperationError: func(svc ref.ServiceID, f awserr.Family) (awserr.Identity, bool) {
			if svc == "sqs" {
				return sqsapi.UnknownOperationError(f)
			}
			return awserr.Identity{}, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &front{handler: h, journal: j, admin: admin}
}

// request builds a request, signed for service when it is not empty.
func request(t *testing.T, method, target string, header http.Header, body, service string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), method, "http://localhost:4566"+target, strings.NewReader(body))
	maps.Copy(r.Header, header)
	if service != "" {
		region, err := aws.KnownRegions().Parse("us-east-1")
		if err != nil {
			t.Fatal(err)
		}
		auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: service}, time.Now())
	}
	return r
}

// serve answers r and renders the answer as status, headers in name order
// with the Date value checked and masked, and body.
func (f *front) serve(t *testing.T, r *http.Request) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	res := rec.Result()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if dates := res.Header.Values("Date"); len(dates) != 1 {
		t.Errorf("Date headers = %q, want exactly one", dates)
	} else if _, err := http.ParseTime(dates[0]); err != nil {
		t.Errorf("Date %q is not an HTTP date: %v", dates[0], err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", res.StatusCode)
	for _, name := range slices.Sorted(maps.Keys(res.Header)) {
		for _, v := range res.Header[name] {
			if name == "Date" {
				v = "<date>"
			}
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	b.WriteString("\n")
	b.Write(body)
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s:\n%s\nwant:\n%s", name, got, want)
	}
}

func jsonHeader(target, contentType string) http.Header {
	h := http.Header{"Content-Type": {contentType}}
	if target != "" {
		h.Set("X-Amz-Target", target)
	}
	return h
}

// unclaimedReasons returns the reasons of the latest Unclaimed event.
func (f *front) unclaimedReasons(t *testing.T) []string {
	t.Helper()
	page := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed})
	if len(page.Events) == 0 {
		t.Fatal("no Unclaimed event was journaled")
	}
	u, ok := page.Events[len(page.Events)-1].Detail.(journal.Unclaimed)
	if !ok {
		t.Fatalf("event is %T", page.Events[len(page.Events)-1].Detail)
	}
	return u.Reasons
}

// The F7 table: a request signed for a service that no protocol rule
// claims is answered by that service, never by S3, and journaled as
// Unclaimed.
func TestRequestsSignedForAServiceAreAnsweredByIt(t *testing.T) {
	cases := []struct {
		name, method, path string
		header             http.Header
		body, signedFor    string
	}{
		// sqs.md 2.3: GET on a queue URL with no Action is 404 with an
		// <UnknownOperationException body.
		{"f7-sqs-get-without-action", http.MethodGet, "/000000000000/q", nil, "", "sqs"},
		// error-matrix.md finding 6: DynamoDB without X-Amz-Target is 400
		// UnknownOperationException.
		{"f7-dynamodb-without-target", http.MethodPost, "/", jsonHeader("", "application/x-amz-json-1.0"), `{}`, "dynamodb"},
		// error-matrix.md H7: DynamoDB with a wrong Content-Type is 404
		// <UnknownOperationException/>.
		{"f7-dynamodb-wrong-content-type", http.MethodPost, "/", jsonHeader("DynamoDB_20120810.ListTables", "whatever"), `{}`, "dynamodb"},
		// error-matrix.md 5.5 T7: the other awsJson version is a wrong
		// Content-Type too.
		{"f7-dynamodb-json-1.1-content-type", http.MethodPost, "/", jsonHeader("DynamoDB_20120810.ListTables", "application/x-amz-json-1.1"), `{}`, "dynamodb"},
		// A target of another service than the one signed is the signed
		// service's to answer.
		{"f7-sqs-signed-dynamodb-target", http.MethodPost, "/", jsonHeader("DynamoDB_20120810.ListTables", "application/x-amz-json-1.0"), `{}`, "sqs"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := newFront(t, nil)
			golden(t, tt.name, f.serve(t, request(t, tt.method, tt.path, tt.header, tt.body, tt.signedFor)))
			if reasons := f.unclaimedReasons(t); len(reasons) == 0 {
				t.Error("the Unclaimed event lists no reasons")
			}
		})
	}
}

func TestUnsignedRequestNoRuleClaimsIsA400ListingTheReasons(t *testing.T) {
	f := newFront(t, nil)
	golden(t, "unclaimed-unsigned", f.serve(t, request(t, http.MethodGet, "/bucket/key", nil, "", "")))
	want := []string{
		"rule 1: the path is not under /_understudy/",
		`rule 2: host "localhost" is not a Lambda function URL host`,
		"rule 3: not an S3 Control request (<account>.<base> host and a /v20180820/ path)",
		"rule 5: no Smithy-Protocol: rpc-v2-cbor header with a /service/<name>/operation/<op> path",
		"rule 6: no X-Amz-Target header",
		"rule 7: neither a form body nor a GET with Action",
		"rule 8: unsigned S3: no s3 route table is registered",
	}
	if got := f.unclaimedReasons(t); !slices.Equal(got, want) {
		t.Errorf("reasons = %q, want %q", got, want)
	}
}

// X-Amz-Target routes with or without a charset on the media type
// (wire-json-query.md 2.2).
func TestJSONTargetClaimsWithAndWithoutCharset(t *testing.T) {
	for name, ct := range map[string]string{
		"json-target-plain":   "application/x-amz-json-1.0",
		"json-target-charset": "application/x-amz-json-1.0; charset=utf-8",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFront(t, nil)
			golden(t, name, f.serve(t, request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.CreateQueue", ct), `{"QueueName":"q"}`, "sqs")))
		})
	}
}

func TestSmithyProtocolWithJSONTargetIsRejected(t *testing.T) {
	f := newFront(t, nil)
	h := jsonHeader("AmazonSQS.CreateQueue", "application/cbor")
	h.Set("Smithy-Protocol", "rpc-v2-cbor")
	golden(t, "smithy-protocol-and-target", f.serve(t, request(t, http.MethodPost, "/service/AmazonSQS/operation/CreateQueue", h, "", "sqs")))
	reasons := f.unclaimedReasons(t)
	if last := reasons[len(reasons)-1]; !strings.HasPrefix(last, "rule 5: the request carries both Smithy-Protocol: rpc-v2-cbor and X-Amz-Target") {
		t.Errorf("reasons = %q, want rule 5's rejection last: no later rule may claim the request", reasons)
	}
}

// The rpcv2Cbor path may carry a prefix before /service/ (Smithy
// rpcv2Cbor "Requests"); rule 5 still recognizes it, here to reject the
// ambiguous X-Amz-Target.
func TestSmithyProtocolPathMayCarryAPrefix(t *testing.T) {
	f := newFront(t, nil)
	h := jsonHeader("AmazonSQS.CreateQueue", "application/cbor")
	h.Set("Smithy-Protocol", "rpc-v2-cbor")
	f.serve(t, request(t, http.MethodPost, "/some/prefix/service/AmazonSQS/operation/CreateQueue", h, "", "sqs"))
	reasons := f.unclaimedReasons(t)
	if last := reasons[len(reasons)-1]; !strings.HasPrefix(last, "rule 5: the request carries both Smithy-Protocol: rpc-v2-cbor and X-Amz-Target") {
		t.Errorf("reasons = %q, want rule 5 to recognize the prefixed path", reasons)
	}
}

func TestUnknownAndUnboundOperations(t *testing.T) {
	f := newFront(t, nil)
	golden(t, "json-unknown-operation", f.serve(t, request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.Bogus", "application/x-amz-json-1.0"), `{}`, "sqs")))
	golden(t, "json-unbound-operation", f.serve(t, request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.ListQueues", "application/x-amz-json-1.0"), `{}`, "sqs")))
	page := f.journal.Query(journal.Filter{Kind: journal.KindAPICall})
	var ops []string
	for i := range page.Events {
		if call, ok := page.Events[i].Detail.(journal.APICall); ok {
			ops = append(ops, call.Op.String())
		}
	}
	if !slices.Equal(ops, []string{"sqs.Bogus", "sqs.ListQueues"}) {
		t.Errorf("journaled calls = %v, want the unknown and the unbound one", ops)
	}
}

func TestRequestsPastTheBodyCapAre413(t *testing.T) {
	f := newFront(t, nil)
	r := request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0"), `{"QueueName":"q"}`, "sqs")
	r.ContentLength = awsjson.DefaultMaxRequestBytes + 1
	golden(t, "body-over-cap", f.serve(t, r))
}

func TestUnreadableCredentialsAreTheFamilysAuthError(t *testing.T) {
	f := newFront(t, nil)
	h := jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0")
	h.Set("Authorization", "Bearer token")
	golden(t, "auth-unknown-scheme", f.serve(t, request(t, http.MethodPost, "/", h, `{"QueueName":"q"}`, "")))
}

// Coral checks the operation before the credentials (error-matrix.md 4):
// an unknown target with unreadable credentials is UnknownOperation.
func TestUnknownOperationIsCheckedBeforeCredentials(t *testing.T) {
	f := newFront(t, nil)
	h := jsonHeader("AmazonSQS.Bogus", "application/x-amz-json-1.0")
	h.Set("Authorization", "Bearer x")
	golden(t, "auth-unknown-operation", f.serve(t, request(t, http.MethodPost, "/", h, `{}`, "")))
}

// The Date header is the wall time the response is sent, as AWS stamps it,
// not the admission time: a slow answer carries a current Date.
func TestDateIsStampedWhenTheResponseIsSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(20 * time.Second)
			w.WriteHeader(http.StatusNoContent)
		})
		h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Listener: ref.ListenerMain, Admin: slow})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/v1/health", nil, "", ""))
		if got, want := rec.Header().Values("Date"), time.Now().UTC().Format(http.TimeFormat); len(got) != 1 || got[0] != want {
			t.Errorf("Date = %q, want one Date of the send time %q", got, want)
		}
	})
}

func TestContainerListenerServesOnlyHealthAndClock(t *testing.T) {
	f := newFront(t, nil)
	if got := f.serve(t, request(t, http.MethodGet, "/_understudy/v1/health", nil, "", "")); !strings.HasPrefix(got, "204\n") {
		t.Errorf("main listener admin = %s", got)
	}
	h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Listener: ref.ListenerContainer, Admin: f.admin})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/_understudy/v1/health": 204, "/_understudy/v1/clock": 204, "/_understudy/v1/reset": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(t, http.MethodGet, path, nil, "", ""))
		if rec.Code != want {
			t.Errorf("container listener GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func mustRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func kernelOf(t *testing.T) *kernel.Kernel {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	k, err := kernel.New(kernel.Options{Registry: mustRegistry(t), Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// pathCodec records the path the claim sees and declines everything.
type pathCodec struct {
	edge.Codec
	mu        sync.Mutex
	uri, path string
}

func (p *pathCodec) Claim(r *http.Request, reg *registry.Registry) edge.Claim {
	p.mu.Lock()
	p.uri, p.path = r.RequestURI, r.URL.EscapedPath()
	p.mu.Unlock()
	return p.Codec.Claim(r, reg)
}

func (p *pathCodec) seen() (uri, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.uri, p.path
}

// rawGet sends a request line exactly as written, so no client cleans the
// path, and returns the status line.
func rawGet(t *testing.T, addr, requestLine, header string) string {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, requestLine+"\r\nHost: localhost\r\n"+header+"Connection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(status)
}

// A path with %2F and dot segments reaches the codecs and the admin handler
// exactly as sent: no ServeMux cleaning, no redirect (go-toolchain.md 5).
func TestPathsReachHandlersUnchanged(t *testing.T) {
	codec := &pathCodec{Codec: server.JSONCodec(awsjson.V10)}
	f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: codec})
	srv := httptest.NewServer(f.handler)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if status := rawGet(t, addr, "GET /_understudy/v1/../a%2Fb/./c HTTP/1.1", ""); status != "HTTP/1.1 204 No Content" {
		t.Fatalf("admin status = %q", status)
	}
	if uri, path := f.admin.seen(); uri != "/_understudy/v1/../a%2Fb/./c" || path != "/_understudy/v1/../a%2Fb/./c" {
		t.Errorf("admin saw RequestURI %q, path %q", uri, path)
	}

	rawGet(t, addr, "POST /a%2Fb/../c HTTP/1.1", "X-Amz-Target: AmazonSQS.ListQueues\r\nContent-Type: application/x-amz-json-1.0\r\nContent-Length: 0\r\n")
	if uri, path := codec.seen(); uri != "/a%2Fb/../c" || path != "/a%2Fb/../c" {
		t.Errorf("codec saw RequestURI %q, path %q", uri, path)
	}
}
