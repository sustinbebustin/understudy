package protocoltest_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/identity"
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
	codecs := server.Codecs()
	var front http.Handler
	if routedByEdge(svc) {
		front, err = edge.New(edge.Options{
			Kernel: k, Registry: reg, Codecs: codecs, S3BaseDomains: s3BaseDomains,
			Listener: ref.ListenerMain, Unclaimed: svc.Protocols[0],
		})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		front = &routeFront{k: k, svc: svc, codec: codecs[svc.Protocols[0]]}
	}
	srv := httptest.NewServer(front)
	t.Cleanup(srv.Close)
	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &bench{echo: e, url: srv.URL, client: &http.Client{Transport: transport}}
}

// routedByEdge reports whether the front door claims svc's requests: every
// RPC protocol is claimed by its codec, but the edge's REST rules know
// only the lambda and s3 route tables (ARCHITECTURE.md 4.1 rule 8), so the
// synthetic RestJson and RestXml services are served by routeFront.
func routedByEdge(svc *registry.Service) bool {
	return svc.Routes == nil || svc.ID == "s3"
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
			err, problem := modeledError(o)
			if problem != nil {
				return fail(awserr.Internalf(problem))
			}
			return fail(err)
		}
		return fail(awserr.Internalf(fmt.Errorf("unknown case outcome %T", outcome)))
	})
}

// routeFront serves a synthetic REST service the edge cannot claim: it
// matches the request against the service's route table, then decodes,
// dispatches and encodes as the edge does (pipeline steps 4, 5 and 13).
type routeFront struct {
	k     *kernel.Kernel
	svc   *registry.Service
	codec edge.Codec
}

func (f *routeFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := string(f.k.IDs().RequestID())
	path, _, _ := strings.Cut(r.RequestURI, "?")
	rt, ok := f.svc.Routes.Match(r.Method, path, r.URL.Query(), r.Header)
	if !ok {
		write(w, f.codec.EncodeError(r, f.svc, id, &awserr.Error{
			ID: awserr.UnknownOperationException(), Message: "no route matches " + r.Method + " " + path, Origin: awserr.Generic{},
		}))
		return
	}
	op := rt.Operation
	call := f.k.NewCall(kernel.ExternalCall{
		RequestID: ids.RequestID(id),
		Operation: op.ID,
		Scope:     aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		Principal: identity.Root{Account: aws.DefaultAccount()},
		Origin:    ref.External{Protocol: f.codec.Protocol(), Listener: ref.ListenerMain},
		Admitted:  f.k.Clock().Now(),
		Endpoint:  kernel.Endpoint{Scheme: "http", Host: r.Host},
		Header:    r.Header,
	})
	switch res := f.k.Dispatch(ctx, call, f.decode(r, op)).(type) {
	case kernel.Output:
		out, ok := res.Out.(smithy.Serializable)
		if !ok {
			write(w, f.codec.EncodeError(r, f.svc, id, awserr.Internalf(fmt.Errorf("output %T is not serializable", res.Out))))
			return
		}
		resp, err := f.codec.Encode(r, op, id, out, res.Status)
		if err != nil {
			write(w, f.codec.EncodeError(r, f.svc, id, err))
			return
		}
		write(w, resp)
	case kernel.Failure:
		write(w, f.codec.EncodeError(r, f.svc, id, res.Err))
	case kernel.StatusOKFailure:
		write(w, f.codec.EncodeError(r, f.svc, id, res.Err))
	}
}

func (f *routeFront) decode(r *http.Request, op *registry.Operation) kernel.Decoded {
	in, ok := f.k.NewInput(op)
	if !ok {
		return kernel.Ok{}
	}
	des, ok := in.(smithy.Deserializable)
	if !ok {
		return kernel.Rejected{Err: awserr.Internalf(fmt.Errorf("input %T is not deserializable", in))}
	}
	if err := f.codec.DecodeHead(r, op, des); err != nil {
		return kernel.Rejected{Err: err}
	}
	if err := f.codec.DecodeBody(r, op, requestBody{r}, des); err != nil {
		return kernel.Rejected{Err: err}
	}
	return kernel.Ok{In: in}
}

// requestBody is the request body as the edge hands it to a codec.
type requestBody struct{ r *http.Request }

func (b requestBody) Open() (io.ReadCloser, error) { return b.r.Body, nil }

func (b requestBody) Len() (int64, bool) { return b.r.ContentLength, b.r.ContentLength >= 0 }

func (requestBody) Trailer(string) (string, bool) { return "", false }

// write sends a codec's response as the edge does.
func write(w http.ResponseWriter, resp edge.Response) {
	maps.Copy(w.Header(), resp.Header)
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if resp.Body != nil {
		_, _ = io.Copy(w, resp.Body) //nolint:errcheck // the bench's client reads to the end; a failed write fails the case
	}
}
