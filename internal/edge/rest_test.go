package edge_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// s3 is a one-route REST service standing in for S3: GET /{Bucket}.
func s3() *registry.Service {
	svc := &registry.Service{ID: "s3", SigningName: "s3", IAMPrefix: "s3", ARNNamespace: "s3", Protocols: []ref.ProtocolID{ref.RESTXML}}
	op := &registry.Operation{Service: svc, ID: ref.OperationID{Service: "s3", Name: "ListObjects"}, Kind: registry.Modeled}
	svc.Operations = []*registry.Operation{op}
	svc.Routes = &registry.RouteTable{Routes: []*registry.Route{{
		Method: http.MethodGet, Path: []registry.Segment{registry.Label{Name: "Bucket"}},
		Operation: op,
	}}}
	return svc
}

// xmlCodec renders every error as its status alone.
type xmlCodec struct{ edge.Codec }

func (xmlCodec) Protocol() ref.ProtocolID { return ref.RESTXML }

func (xmlCodec) EncodeError(_ *http.Request, _ *registry.Service, _ string, e *awserr.Error) edge.Response {
	return edge.Response{Status: e.ID.Status}
}

// Unsigned path-style requests fall through to S3's route table, and S3
// answers carry x-amz-request-id and x-amz-id-2 (s3.md finding 7).
func TestUnsignedRequestsReachS3WithItsRequestIDHeaders(t *testing.T) {
	f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, s3())
	got := f.serve(t, request(t, http.MethodGet, "/bucket", nil, "", ""))
	for _, want := range []string{"501\n", "X-Amz-Id-2: ", "X-Amz-Request-Id: "} {
		if !strings.Contains(got, want) {
			t.Errorf("the S3 answer lacks %q:\n%s", want, got)
		}
	}
	// A date path naming no bucket is Lambda's, but with no Lambda routes
	// registered it falls to S3 path-style too.
	if got := f.serve(t, request(t, http.MethodGet, "/2015-03-31/functions", nil, "", "")); !strings.HasPrefix(got, "400\n") {
		t.Errorf("a date path with no matching S3 route = %s", got)
	}
}

// A header-signed S3 request without x-amz-content-sha256 has credentials
// auth refuses, but only S3 refuses them, so S3 answers it (with the auth
// error) rather than the edge leaving it unclaimed.
func TestS3RequestWithoutPayloadHashReachesS3(t *testing.T) {
	f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, s3())
	r := request(t, http.MethodGet, "/bucket", nil, "", "s3")
	r.Header.Del("X-Amz-Content-Sha256")
	got := f.serve(t, r)
	for _, want := range []string{"400\n", "X-Amz-Request-Id: "} {
		if !strings.Contains(got, want) {
			t.Errorf("the answer lacks %q, so S3 did not answer:\n%s", want, got)
		}
	}
	if page := f.journal.Query(journal.Filter{Kind: journal.KindUnclaimed}); len(page.Events) != 0 {
		t.Errorf("the request was journaled as Unclaimed: %+v", page.Events[0].Detail)
	}
}

// A LocalStack path, such as a migrated health check's, is never S3's
// bucket "_localstack", which no bucket name can be: it is 404 in the admin
// API's JSON error envelope naming understudy's own health check, and is
// journaled as Unclaimed rather than as an S3 call. A bare /health is a
// valid bucket path and stays S3's.
func TestLocalStackPathsNameUnderstudysHealthCheck(t *testing.T) {
	for _, c := range []struct{ path, signedFor string }{
		{"/_localstack/health", ""},
		{"/_localstack/health", "s3"},
		{"/_localstack", ""},
		{"/_localstack/init/ready", ""},
	} {
		f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, s3())
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, request(t, http.MethodGet, c.path, nil, "", c.signedFor))
		var env admin.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
			t.Errorf("GET %s signed for %q: body %q is not the admin error envelope: %v", c.path, c.signedFor, rec.Body, err)
			continue
		}
		if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/json" || env.Error.Code != admin.NotFound {
			t.Errorf("GET %s signed for %q = %d %s %+v, want 404 application/json not_found", c.path, c.signedFor, rec.Code, rec.Header().Get("Content-Type"), env.Error)
		}
		for _, want := range []string{"GET " + admin.Prefix + "/health", "understudy health --wait"} {
			if !strings.Contains(env.Error.Hint, want) {
				t.Errorf("GET %s: hint %q does not name %q", c.path, env.Error.Hint, want)
			}
		}
		if calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events; len(calls) != 0 {
			t.Errorf("GET %s was journaled as an API call: %+v", c.path, calls[0].Detail)
		}
		reasons := f.unclaimedReasons(t)
		if !slices.ContainsFunc(reasons, func(r string) bool { return strings.HasPrefix(r, "rule 1: ") && strings.Contains(r, "LocalStack") }) {
			t.Errorf("GET %s: journaled reasons = %q, want rule 1 naming LocalStack", c.path, reasons)
		}
	}
	f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, s3())
	if got := f.serve(t, request(t, http.MethodGet, "/health", nil, "", "")); !strings.HasPrefix(got, "501\n") || !strings.Contains(got, "X-Amz-Request-Id: ") {
		t.Errorf("GET /health, the bucket health, did not reach S3:\n%s", got)
	}
}

// smallCodec caps bodies at 8 bytes.
type smallCodec struct{ edge.Codec }

func (smallCodec) BodyLimit(*registry.Operation) int64 { return 8 }

// A chunked body past the cap stops at the counting reader with 413; it is
// never buffered whole.
func TestChunkedBodiesPastTheCapAre413(t *testing.T) {
	f := newFront(t, map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: smallCodec{Codec: server.JSONCodec(awsjson.V10)}})
	r := request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0"), "", "sqs")
	r.Body = io.NopCloser(strings.NewReader(`{"QueueName":"longer-than-eight-bytes"}`))
	r.ContentLength = -1
	got := f.serve(t, r)
	if !strings.HasPrefix(got, "413\n") || !strings.Contains(got, "exceeds the 8-byte limit of sqs.CreateQueue") {
		t.Errorf("an oversized chunked body = %s", got)
	}
}
