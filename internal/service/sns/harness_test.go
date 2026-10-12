package sns_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sns"
)

// harness is a running kernel with the SNS service, the outbox behind a
// sink that records every accepted delivery, and stub SQS SendMessage and
// Lambda Invoke handlers that record what they receive. kerneltest builds
// its kernel without a DeliverSink, so publish tests build their own here.
type harness struct {
	*testing.T

	k     *kernel.Kernel
	clock *clock.Clock
	svc   *sns.Service
	sink  *sink
	// sent and invoked are what the stub targets received.
	sent    *recorder[sqsapi.SendMessageInput]
	invoked *recorder[lambdaapi.InvokeInput]
	// scope and endpoint are where and how client calls arrive.
	scope    aws.Scope
	endpoint kernel.Endpoint
}

// sink records the deliveries a publish produced, in the order the kernel
// handed them over, then gives them to the outbox.
type sink struct {
	box *outbox.Outbox
	recorder[kernel.Delivery]
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.add(d)
	return s.box.Deliver(d)
}

// recorder collects values from concurrent delivery attempts.
type recorder[T any] struct {
	mu    sync.Mutex
	items []T
}

func (r *recorder[T]) add(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, v)
}

func (r *recorder[T]) all() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]T(nil), r.items...)
}

// run runs fn in a synctest bubble against a frozen clock.
func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		h := build(t)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- h.k.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := h.k.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		fn(h)
	})
}

func build(t *testing.T) *harness {
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
	reg, err := registry.New(snsapi.Service(), sqsapi.Service(), lambdaapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(1)
	idStore := identity.New(src, aws.DefaultAccount())
	box := outbox.New()
	sk := &sink{box: box}
	k, err := kernel.New(kernel.Options{
		Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j,
		Identity: idStore, Deliver: sk, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := box.Bind(k, outbox.Options{}); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		T: t, k: k, clock: clk, sink: sk,
		sent:     &recorder[sqsapi.SendMessageInput]{},
		invoked:  &recorder[lambdaapi.InvokeInput]{},
		scope:    aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	}
	h.svc = sns.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(snsapi.Service()), Clock: clk, IDs: src, Identity: idStore, Log: k.Log()}, sns.Options{})
	kernel.Bind(k.Binder(sqsapi.Service()), sqsapi.OpSendMessage, func(_ context.Context, _ *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		h.sent.add(*in)
		return &sqsapi.SendMessageOutput{MessageId: new("stub")}, nil
	}, registry.Full{})
	kernel.Bind(k.Binder(lambdaapi.Service()), lambdaapi.OpInvoke, func(_ context.Context, _ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		h.invoked.add(*in)
		return &lambdaapi.InvokeOutput{StatusCode: http.StatusAccepted}, nil
	}, registry.Full{})
	return h
}

// call runs op with in as a client request in h.scope, over awsQuery as
// the SDKs send SNS.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	c := h.k.NewCall(kernel.ExternalCall{
		RequestID: h.k.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     h.scope,
		Principal: identity.Root{Account: h.scope.Account},
		Origin:    ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerMain},
		Admitted:  h.clock.Now(),
		Endpoint:  h.endpoint,
	})
	switch r := h.k.Dispatch(h.Context(), c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		out, ok := r.Out.(*Out)
		if !ok {
			h.Fatalf("%s returned %T", op.Descriptor().ID, r.Out)
		}
		return out, nil
	case kernel.Failure:
		return nil, r.Err
	case kernel.StatusOKFailure:
		return nil, r.Err
	}
	h.Fatalf("%s returned no result", op.Descriptor().ID)
	return nil, nil
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s %q", op.Descriptor().ID, err.ID.QueryCode, err.Message)
	}
	return out
}

// link runs a raw link operation on a GET of target, as the edge
// dispatches it, and returns the recorded response.
func (h *harness) link(name, target string) (*httptest.ResponseRecorder, *awserr.Error) {
	h.Helper()
	op, ok := snsapi.Service().Operation(name)
	if !ok {
		h.Fatalf("snsapi has no %s", name)
	}
	c := h.k.NewCall(kernel.ExternalCall{
		RequestID: h.k.IDs().RequestID(),
		Operation: op.ID,
		Scope:     h.scope,
		Principal: identity.Anonymous{},
		Origin:    ref.External{Protocol: ref.Raw, Listener: ref.ListenerMain},
		Admitted:  h.clock.Now(),
		Endpoint:  h.endpoint,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(h.Context(), http.MethodGet, target, http.NoBody)
	switch r := h.k.Dispatch(h.Context(), c, kernel.Ok{In: kernel.RawExchange{Request: req, Writer: rec}}).(type) {
	case kernel.Failure:
		return rec, r.Err
	case kernel.StatusOKFailure:
		return rec, r.Err
	case kernel.Output:
	}
	return rec, nil
}

// settle waits until every accepted delivery has been attempted.
func (h *harness) settle() {
	h.Helper()
	if pending, err := h.k.Settle(h.Context(), time.Minute); err != nil {
		h.Fatalf("settle: %v (pending %v)", err, pending)
	}
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// topic creates a topic and returns its ARN.
func (h *harness) topic(name string, attrs map[string]string) string {
	h.Helper()
	return *must(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: name, Attributes: attrs}).TopicArn
}

// subscribe subscribes endpoint and returns the subscription ARN.
func (h *harness) subscribe(topic, protocol, endpoint string, attrs map[string]string) string {
	h.Helper()
	return *must(h, snsapi.OpSubscribe, &snsapi.SubscribeInput{
		TopicArn: topic, Protocol: protocol, Endpoint: &endpoint, Attributes: attrs, ReturnSubscriptionArn: true,
	}).SubscriptionArn
}

func (h *harness) subscriptionAttributes(arn string) map[string]string {
	h.Helper()
	return must(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn}).Attributes
}

func (h *harness) topicAttributes(arn string) map[string]string {
	h.Helper()
	return must(h, snsapi.OpGetTopicAttributes, &snsapi.GetTopicAttributesInput{TopicArn: arn}).Attributes
}

// wantError fails unless err is code with message.
func wantError(tb testing.TB, err *awserr.Error, code, message string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Fatalf("no error, want %s %q", code, message)
	case err.ID.QueryCode != code || err.Message != message:
		tb.Fatalf("error = %s %q\nwant  %s %q", err.ID.QueryCode, err.Message, code, message)
	}
}

const (
	queueARN     = "arn:aws:sqs:us-east-1:000000000000:orders"
	fifoQueueARN = "arn:aws:sqs:us-east-1:000000000000:orders.fifo"
	functionARN  = "arn:aws:lambda:us-east-1:000000000000:function:worker"
)
