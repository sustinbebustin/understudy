package protocoltest_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonprotocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/nonquerycompatiblerpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/querycompatiblejsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/querycompatiblerpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjsonvalidation"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxmlwithnamespace"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/rpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/s3"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// corpusService is one generated synthetic service: its cases and the
// constructor of its typed binder, which the bench binds through
// reflection, one echo handler per operation.
type corpusService struct {
	suite  func() gen.Suite
	binder func(*kernel.Binder) any
}

func corpusServices() []corpusService {
	return []corpusService{
		{awsquery.Suite, func(b *kernel.Binder) any { return awsquery.NewBinder(b) }},
		{jsonprotocol.Suite, func(b *kernel.Binder) any { return jsonprotocol.NewBinder(b) }},
		{jsonrpc10.Suite, func(b *kernel.Binder) any { return jsonrpc10.NewBinder(b) }},
		{nonquerycompatiblerpcv2protocol.Suite, func(b *kernel.Binder) any { return nonquerycompatiblerpcv2protocol.NewBinder(b) }},
		{querycompatiblejsonrpc10.Suite, func(b *kernel.Binder) any { return querycompatiblejsonrpc10.NewBinder(b) }},
		{querycompatiblerpcv2protocol.Suite, func(b *kernel.Binder) any { return querycompatiblerpcv2protocol.NewBinder(b) }},
		{restjson.Suite, func(b *kernel.Binder) any { return restjson.NewBinder(b) }},
		{restjsonvalidation.Suite, func(b *kernel.Binder) any { return restjsonvalidation.NewBinder(b) }},
		{restxml.Suite, func(b *kernel.Binder) any { return restxml.NewBinder(b) }},
		{restxmlwithnamespace.Suite, func(b *kernel.Binder) any { return restxmlwithnamespace.NewBinder(b) }},
		{rpcv2protocol.Suite, func(b *kernel.Binder) any { return rpcv2protocol.NewBinder(b) }},
		{s3.Suite, func(b *kernel.Binder) any { return s3.NewBinder(b) }},
	}
}

// s3BaseDomains are the endpoints the S3 slice's cases address buckets
// under, virtual-hosted (smithy-protocol-tests.md 7.1).
var s3BaseDomains = []string{
	"localhost",
	"s3.us-west-2.amazonaws.com",
	"s3.dualstack.us-west-2.amazonaws.com",
	"s3-accelerate.amazonaws.com",
	"s3-accelerate.dualstack.amazonaws.com",
}

// bench is one synthetic service served the way the emulator serves a real
// one: a kernel with every operation bound to the echo handler, behind the
// front door with every codec the server registers, on a loopback HTTP
// server so net/http frames the responses as it does for clients.
type bench struct {
	echo   *echo
	url    string
	client *http.Client
	// scope is the signing name of the credentials each case is sent
	// with, empty for unsigned (credentialScope).
	scope string
}

func newBench(t *testing.T, cs corpusService, svc *registry.Service) *bench {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(svc)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	e := &echo{}
	if err := e.bindAll(cs.binder(k.Binder(svc))); err != nil {
		t.Fatalf("%s: %v", svc.ID, err)
	}
	if err := k.BindErr(); err != nil {
		t.Fatalf("%s: %v", svc.ID, err)
	}
	for _, op := range svc.Operations {
		if _, ok := k.Bindings().Status(op).(kernel.Bound); !ok {
			t.Fatalf("%s: the echo binder left %s unbound", svc.ID, op.ID)
		}
	}
	// The endpoint-trait cases send Hosts such as foo.example.com; the
	// corpus tests the protocols, not the listener's Host policy.
	front, err := edge.New(edge.Options{
		Kernel: k, Registry: reg, Codecs: server.Codecs(), S3BaseDomains: s3BaseDomains,
		Listener: ref.ListenerMain, Unclaimed: svc.Protocols[0], Hosts: edge.AnyHost{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &bench{echo: e, url: srv.URL, client: &http.Client{Transport: transport}, scope: credentialScope(t, svc)}
}

// credentialScope is the signing name the bench scopes each case's
// credentials to. The edge claims an RPC request by its codec and an
// unsigned request as S3's, but a request to any other REST service only
// by the signing name of its credentials, which selects the route tables
// rule 8 tries (ARCHITECTURE.md 4.1, ADR 0019). Empty means the cases go
// unsigned.
func credentialScope(t *testing.T, svc *registry.Service) string {
	t.Helper()
	if svc.Routes == nil || svc.ID == "s3" {
		return ""
	}
	if svc.SigningName == "" {
		t.Fatalf("%s: a REST service without a signing name cannot be routed by the edge", svc.ID)
	}
	return svc.SigningName
}

// Credentials the bench signs with: the edge parses them but never verifies
// them, since the bench sets no edge.Options.Verify.
const (
	credentialDate = "20260101"
	credentialTime = credentialDate + "T000000Z"
	credentialFake = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/" + credentialDate + "/us-east-1/%s/aws4_request, " +
		"SignedHeaders=host;x-amz-date, Signature=0000000000000000000000000000000000000000000000000000000000000000"
)

// sign gives r a well-formed SigV4 Authorization header scoped to the
// bench's signing name, and the X-Amz-Date the parser requires beside it,
// unless r carries credentials of its own.
func (b *bench) sign(r *http.Request) {
	if b.scope == "" || r.Header.Get("Authorization") != "" {
		return
	}
	r.Header.Set("Authorization", fmt.Sprintf(credentialFake, b.scope))
	r.Header.Set("X-Amz-Date", credentialTime)
}

// reply is what the bench's server answered.
type reply struct {
	code   int
	header http.Header
	body   []byte
}

// serve sends w and reads the whole reply.
func (b *bench) serve(ctx context.Context, w wireRequest) (reply, error) {
	r, err := w.build(ctx, b.url)
	if err != nil {
		return reply{}, err
	}
	b.sign(r)
	resp, err := b.client.Do(r)
	if err != nil {
		return reply{}, fmt.Errorf("sending the case's request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, fmt.Errorf("reading the response: %w", err)
	}
	return reply{code: resp.StatusCode, header: resp.Header, body: body}, nil
}

func (r reply) String() string { return fmt.Sprintf("%d %q", r.code, r.body) }

// echo is the handler every operation of a bench is bound to: it records
// the decoded input and answers with the scripted outcome, or the zero
// output when none is scripted.
type echo struct {
	mu      sync.Mutex
	inputs  []any
	outcome gen.Outcome
}

// script resets the recorded inputs and sets the next answer.
func (e *echo) script(o gen.Outcome) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inputs, e.outcome = nil, o
}

// entered returns the inputs the handler received since the last script.
func (e *echo) entered() []any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inputs
}

// bindAll calls every method of a generated Binder with an echo handler of
// the method's handler type. The binders are typed per operation, so
// reflection is the one way to bind them all without a hand-written line
// per operation.
func (e *echo) bindAll(binder any) error {
	bv := reflect.ValueOf(binder)
	var cov registry.BindCoverage = registry.Full{}
	for i := range bv.NumMethod() {
		m := bv.Method(i)
		mt := m.Type()
		if mt.NumIn() < 2 || mt.In(0).Kind() != reflect.Func {
			return fmt.Errorf("binder method %s is not func(Handler, BindCoverage, ...BindOption)", bv.Type().Method(i).Name)
		}
		m.Call([]reflect.Value{e.handler(mt.In(0)), reflect.ValueOf(&cov).Elem()})
	}
	return nil
}

func (e *echo) answer(in any) (gen.Outcome, bool) {
	drainStreams(in)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inputs = append(e.inputs, in)
	return e.outcome, e.outcome != nil
}

// drainStreams replaces each streaming member of an input with the bytes
// it held, since net/http closes the request body once the handler
// returns and the case compares the input after that. A stream that fails
// to read is replaced by a stream that fails the same way.
func drainStreams(in any) {
	v := reflect.ValueOf(in)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return
	}
	for _, f := range v.Elem().Fields() {
		if f.Type() != bodyType || f.IsNil() || !f.CanSet() {
			continue
		}
		data, err := readBody(f)
		if err != nil {
			f.Set(reflect.ValueOf(smithy.Body(failedBody{err: err})))
			continue
		}
		f.Set(reflect.ValueOf(smithy.Body(gen.TextBody(data))))
	}
}

// failedBody is a drained stream whose read failed.
type failedBody struct{ err error }

func (b failedBody) Open() (io.ReadCloser, error) { return nil, b.err }
func (failedBody) Len() (int64, bool)             { return 0, false }
func (failedBody) Trailer(string) (string, bool)  { return "", false }

// handler builds a kernel.Handler[In, Out] of type ht, which is
// func(context.Context, *kernel.Call, *In) (*Out, *awserr.Error).
func (e *echo) handler(ht reflect.Type) reflect.Value {
	outPtr, errType := ht.Out(0), ht.Out(1)
	return reflect.MakeFunc(ht, func(args []reflect.Value) []reflect.Value {
		fail := func(err *awserr.Error) []reflect.Value {
			return []reflect.Value{reflect.Zero(outPtr), reflect.ValueOf(err)}
		}
		outcome, scripted := e.answer(args[2].Interface())
		if !scripted {
			return []reflect.Value{reflect.New(outPtr.Elem()), reflect.Zero(errType)}
		}
		switch o := outcome.(type) {
		case gen.Output:
			v := reflect.ValueOf(o.Value)
			if v.Type() != outPtr {
				return fail(awserr.Internalf(fmt.Errorf("the case output is a %s, the operation's a %s", v.Type(), outPtr)))
			}
			return []reflect.Value{v, reflect.Zero(errType)}
		case gen.Error:
			return fail(modeledError(o))
		}
		return fail(awserr.Internalf(fmt.Errorf("unknown case outcome %T", outcome)))
	})
}
