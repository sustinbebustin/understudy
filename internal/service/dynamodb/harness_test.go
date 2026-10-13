package dynamodb_test

import (
	"bytes"
	"context"
	"hash/crc32"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/kms"
)

// forwarded is one request the fake Local received.
type forwarded struct {
	Op     string
	Header http.Header
	Body   string
}

// reply is what the fake Local answers. Local sets x-amz-crc32 on its 200
// answers only (dynamodb-local.md 9.1).
type reply struct {
	Status int
	Body   string
}

// fakeLocal is DynamoDB Local behind an in-process transport: it records
// every forwarded request and answers through answer, {} by default.
type fakeLocal struct {
	mu     sync.Mutex
	seen   []forwarded
	answer func(op, body string) reply
	// startErr is what Start returns; startBlocks makes Start wait until
	// its context ends instead, as a long pull does.
	startErr    error
	startBlocks bool
	// stopped makes Endpoint report no running Local, as after shutdown.
	stopped bool
	// lost are the endpoints Lost was told a round trip failed to reach.
	lost []string
	// fail replaces the answer after the request is recorded: the round
	// trip's own error or response.
	fail func(r *http.Request) (*http.Response, error)
}

func (f *fakeLocal) Start(ctx context.Context) error {
	if f.startBlocks {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.startErr
}

// fakeEndpoint is where every fakeLocal listens.
var fakeEndpoint = url.URL{Scheme: "http", Host: "ddblocal.invalid:8000"}

func (f *fakeLocal) Endpoint() (*url.URL, bool) {
	if f.stopped {
		return nil, false
	}
	u := fakeEndpoint
	return &u, true
}

func (f *fakeLocal) Transport() http.RoundTripper { return f }

// Lost records the endpoint a failed round trip was sent to.
func (f *fakeLocal) Lost(_ context.Context, endpoint *url.URL) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lost = append(f.lost, endpoint.String())
	return nil
}

func (f *fakeLocal) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")
	f.mu.Lock()
	f.seen = append(f.seen, forwarded{Op: op, Header: r.Header.Clone(), Body: string(body)})
	answer := f.answer
	f.mu.Unlock()
	if f.fail != nil {
		return f.fail(r)
	}
	rep := reply{Status: http.StatusOK, Body: "{}"}
	if answer != nil {
		rep = answer(op, string(body))
	}
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/x-amz-json-1.0")
	rec.Header().Set("X-Amzn-Requestid", "local-request-id")
	rec.Header().Set("Server", "Jetty(12.1.11)")
	if rep.Status == http.StatusOK {
		rec.Header().Set("X-Amz-Crc32", crc(rep.Body))
	}
	rec.WriteHeader(rep.Status)
	if _, err := rec.WriteString(rep.Body); err != nil {
		return nil, err
	}
	return rec.Result(), nil
}

func (f *fakeLocal) requests() []forwarded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forwarded(nil), f.seen...)
}

// harness is a kernel with the DynamoDB service over a fake Local, and the
// edge in front of it.
type harness struct {
	*kerneltest.K
	local *fakeLocal
	svc   *dynamodb.Service
	front *edge.Handler
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, &fakeLocal{}, fn)
}

// neighbour is another service the harness serves beside DynamoDB.
type neighbour struct {
	api   func() *registry.Service
	build func(k *kerneltest.K)
}

// withKMS serves KMS, which the shims resolve SSE keys through; without
// it the kernel models no KMS operation.
var withKMS = neighbour{api: kmsapi.Service, build: func(k *kerneltest.K) { kms.New(k.Deps(kmsapi.Service()), kms.Options{}) }}

// runWith runs fn against the service over local; a nil local builds the
// service without one, as a server that never accepted the licence does.
func runWith(t *testing.T, local *fakeLocal, fn func(h *harness), others ...neighbour) {
	t.Helper()
	services := make([]*registry.Service, 0, 1+len(others))
	services = append(services, dynamodbapi.Service())
	for _, o := range others {
		services = append(services, o.api())
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		for _, o := range others {
			o.build(k)
		}
		var opts dynamodb.Options
		if local != nil {
			opts.Local = local
		}
		svc := dynamodb.New(k.Deps(dynamodbapi.Service()), opts)
		front, err := edge.New(edge.Options{
			Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
			Codecs:   map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: jsonCodec{awsjson.V10}, ref.AWSJSON11: jsonCodec{awsjson.V11}},
			Listener: ref.ListenerMain, Unclaimed: ref.AWSJSON10,
			UnknownOperationError: func(ref.ServiceID, awserr.Family) (awserr.Identity, bool) {
				return dynamodbapi.UnknownOperationError(awserr.JSONFamily)
			},
		})
		if err != nil {
			k.Fatal(err)
		}
		if local == nil {
			local = &fakeLocal{}
		}
		fn(&harness{K: k, local: local, svc: svc, front: front})
	}, kerneltest.Services(services...))
}

// jsonCodec adapts the awsJson codec to the edge as server/codecs.go does;
// the server package itself wires every service, so the test adapts the
// one codec DynamoDB speaks.
type jsonCodec struct{ v awsjson.Version }

func (j jsonCodec) Protocol() ref.ProtocolID { return j.codec().Protocol() }

func (j jsonCodec) Claim(r *http.Request, reg *registry.Registry) edge.Claim {
	switch c := j.codec().Claim(r, reg).(type) {
	case awsjson.Claimed:
		return edge.Claimed{Op: c.Op}
	case awsjson.UnknownOperation:
		return edge.UnknownOperation{Service: c.Service, Name: c.Operation}
	case awsjson.Declined:
		return edge.Declined{Reason: c.Reason}
	}
	return edge.Declined{Reason: "the awsJson codec gave no answer"}
}

func (jsonCodec) BodyLimit(op *registry.Operation) int64 { return awsjson.MaxRequestBytes(op) }

func (jsonCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (j jsonCodec) DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	rc, err := body.Open()
	if err != nil {
		return awserr.Internalf(err)
	}
	defer rc.Close()
	capped := r.WithContext(r.Context())
	capped.Body = rc
	return j.codec().Decode(capped, op, in)
}

func (j jsonCodec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := j.codec().Encode(op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: bytes.NewReader(resp.Body)}, nil
}

func (j jsonCodec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := j.codec().EncodeError(r, svc, requestID, e)
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: bytes.NewReader(resp.Body)}
}

func (j jsonCodec) codec() awsjson.Codec { return awsjson.Codec{Version: j.v} }

// caller is who signs a request: an access key that is an account id, so
// the call runs in that account, and the credential's region.
type caller struct {
	Account, Region string
}

func defaultCaller() *caller { return &caller{Account: "111111111111", Region: "us-east-1"} }

// response is an answer as the client receives it.
type response struct {
	Status int
	Header http.Header
	Body   string
}

// post sends a client request through the edge: header is added to the
// request, and from signs it unless it is nil.
func (h *harness) post(header http.Header, body string, from *caller) response {
	h.Helper()
	r := httptest.NewRequestWithContext(h.Context(), http.MethodPost, "http://localhost:4566/", strings.NewReader(body))
	maps.Copy(r.Header, header)
	if from != nil {
		region, err := aws.KnownRegions().Parse(from.Region)
		if err != nil {
			h.Fatal(err)
		}
		auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: from.Account, SecretAccessKey: "secret", Region: region, Service: "dynamodb"}, time.Now())
	}
	rec := httptest.NewRecorder()
	h.front.ServeHTTP(rec, r)
	res := rec.Result()
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		h.Fatal(err)
	}
	return response{Status: res.StatusCode, Header: res.Header, Body: string(out)}
}

// call sends op with body as a signed SDK request from the default caller.
func (h *harness) call(op, body string) response {
	h.Helper()
	return h.post(target(op), body, defaultCaller())
}

func target(op string) http.Header {
	return http.Header{"Content-Type": {"application/x-amz-json-1.0"}, "X-Amz-Target": {"DynamoDB_20120810." + op}}
}

// dispatch runs c, a call of op, with in and w through the kernel, without
// the edge, and returns the raw answer.
func dispatch[In any](ctx context.Context, h *harness, c *kernel.Call, in *In, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
	h.Helper()
	switch r := h.Kernel.Dispatch(ctx, c, kernel.Ok{In: in, Wire: w}).(type) {
	case kernel.Output:
		out, ok := r.Out.(*kernel.RawOutput)
		if !ok {
			h.Fatalf("output %T, want *kernel.RawOutput", r.Out)
		}
		return out.Response, nil
	case kernel.Failure:
		return kernel.RawResponse{}, r.Err
	case kernel.StatusOKFailure:
		return kernel.RawResponse{}, r.Err
	}
	h.Fatal("Dispatch returned no result")
	return kernel.RawResponse{}, nil
}

// fromClient runs op with in and the client's raw request, as the edge
// hands a raw binding the original bytes.
func fromClient[In any](h *harness, op kernel.Op[In, kernel.RawOutput], in *In, raw kernel.RawRequest) (kernel.RawResponse, *awserr.Error) {
	h.Helper()
	return dispatch(h.Context(), h, h.Call(op.Descriptor()), in, kernel.FromClient{Request: raw})
}

// synthesize decodes body into op's input and runs it as an internal
// caller's input (Synthesized), which the service encodes itself.
func synthesize[In any, P interface {
	*In
	smithy.Deserializable
}](h *harness, op kernel.Op[In, kernel.RawOutput], body string) (kernel.RawResponse, *awserr.Error) {
	h.Helper()
	in := P(new(In))
	r := httptest.NewRequestWithContext(h.Context(), http.MethodPost, "/", strings.NewReader(body))
	if err := (awsjson.Codec{Version: awsjson.V10}).Decode(r, op.Descriptor(), in); err != nil {
		h.Fatalf("decoding %s: %s", body, err.Message)
	}
	return dispatch(h.Context(), h, h.Call(op.Descriptor()), (*In)(in), kernel.Synthesized{})
}

// crc is x-amz-crc32 of body.
func crc(body string) string {
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(body))), 10)
}
