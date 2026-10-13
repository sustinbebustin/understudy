package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

func sqsOp[In, Out any](tb testing.TB, name string) kernel.Op[In, Out] {
	tb.Helper()
	d, ok := sqsapi.Service().Operation(name)
	if !ok {
		tb.Fatalf("sqsapi has no operation %s", name)
	}
	return kernel.NewOp[In, Out](d)
}

func run(t *testing.T, fn func(k *kerneltest.K)) {
	t.Helper()
	kerneltest.Run(t, fn, kerneltest.Services(sqsapi.Service()))
}

func cells(k *kerneltest.K) *store.Partitioned[aws.Scope, int] {
	return store.NewPartitioned(k.Kernel.Gate(), store.Codec{Marshal: json.Marshal, Unmarshal: json.Unmarshal}, func(aws.Scope) int { return 0 })
}

// internalCause returns the cause of an Internal error, failing otherwise.
func internalCause(t *testing.T, err *awserr.Error) error {
	t.Helper()
	if err == nil {
		t.Fatal("got no error, want Internal")
	}
	in, ok := err.Origin.(awserr.Internal)
	if !ok || err.ID.Status != 500 {
		t.Fatalf("error = %s %d %T, want 500 InternalFailure", err.ID.Shape, err.ID.Status, err.Origin)
	}
	return in.Cause
}

func TestNestedWriteIsInternalNamingTheOperation(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		p := cells(k)
		k.Stub(sqsapi.OpSendMessage, func(_ context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			return kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (*sqsapi.SendMessageOutput, *awserr.Error) {
				_, err := kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (int, *awserr.Error) { return 0, nil })
				return nil, err
			})
		})
		_, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
		cause := internalCause(t, err)
		var holding *kernel.HoldingError
		if !errors.As(cause, &holding) || holding.Holder != "sqs.SendMessage" || holding.Attempted != "kernel.Write" {
			t.Fatalf("cause = %v, want a HoldingError for kernel.Write from sqs.SendMessage", cause)
		}
	})
}

// A write that changed state advances the cell's revision; one that says
// Unchanged leaves it, so persistence sees no dirty cell, and its wakes
// still run.
func TestUnchangedWritesKeepTheCellRevision(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		p := cells(k)
		c := k.Call(sqsapi.OpSendMessage.Descriptor())
		write := func(fn func(s *int, tx *kernel.Tx)) {
			t.Helper()
			if _, err := kernel.Write(c, p, k.Scope, func(s *int, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
				fn(s, tx)
				return struct{}{}, nil
			}); err != nil {
				t.Fatal(err.AsError())
			}
		}
		write(func(s *int, _ *kernel.Tx) { *s++ })
		before := p.For(k.Scope).Rev()
		woke := k.Kernel.Waiters().Wait(t.Context(), "k")
		write(func(_ *int, tx *kernel.Tx) {
			tx.Wake("k")
			tx.Unchanged()
		})
		if got := p.For(k.Scope).Rev(); got != before {
			t.Errorf("revision after an unchanged write = %d, want %d", got, before)
		}
		select {
		case <-woke:
		default:
			t.Error("an unchanged write dropped its wake")
		}
		write(func(s *int, _ *kernel.Tx) { *s++ })
		if got := p.For(k.Scope).Rev(); got != before+1 {
			t.Errorf("revision after a change = %d, want %d", got, before+1)
		}
	})
}

func TestInvokeWhileHoldingIsInternalNamingBothOperations(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		p := cells(k)
		called := false
		k.Stub(sqsapi.OpGetQueueUrl, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			called = true
			return &sqsapi.GetQueueUrlOutput{}, nil
		})
		k.Stub(sqsapi.OpSendMessage, func(ctx context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			return kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (*sqsapi.SendMessageOutput, *awserr.Error) {
				_, err := kernel.Invoke(ctx, k.Kernel, c, sqsapi.OpGetQueueUrl,
					kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.SNSDelivery},
					&sqsapi.GetQueueUrlInput{QueueName: "q"})
				return nil, err
			})
		})
		_, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
		msg := internalCause(t, err).Error()
		if !strings.Contains(msg, "kernel.Invoke of sqs.GetQueueUrl from sqs.SendMessage") {
			t.Errorf("cause = %q, want it to name sqs.GetQueueUrl and sqs.SendMessage", msg)
		}
		if called {
			t.Error("the invoked handler ran while the caller held a cell lock")
		}
	})
}

// TestInvokeLinksTheChildCall: an internal call is the parent's child in the
// journal, admitted at Target.At, with its own principal mode.
func TestInvokeLinksTheChildCall(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		at := k.Clock.Now().Add(-time.Minute)
		var child *kernel.Call
		k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, _ *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			child = c
			return &sqsapi.GetQueueUrlOutput{}, nil
		})
		var parent *kernel.Call
		k.Stub(sqsapi.OpSendMessage, func(ctx context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			parent = c
			_, err := kernel.Invoke(ctx, k.Kernel, c, sqsapi.OpGetQueueUrl,
				kernel.Target{Scope: c.Scope(), As: kernel.AsService{Name: "sns.amazonaws.com"}, Via: ref.SNSDelivery, At: at},
				&sqsapi.GetQueueUrlInput{QueueName: "q"})
			if err != nil {
				return nil, err
			}
			return &sqsapi.SendMessageOutput{}, nil
		})
		if _, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}); err != nil {
			t.Fatal(err.AsError())
		}
		if child.Cause().Parent != parent.Event() || child.Admitted() != at {
			t.Errorf("child cause %+v admitted %s, want parent %d admitted %s", child.Cause(), child.Admitted(), parent.Event(), at)
		}
		tree, ok := k.Journal.Trace(parent.Event())
		if !ok || len(tree.Children) != 1 {
			t.Fatalf("trace of the parent = %+v, want one child", tree)
		}
		call, ok := tree.Children[0].Event.Detail.(journal.APICall)
		if !ok || call.Op.Name != "GetQueueUrl" || call.Mode != "service:sns.amazonaws.com" || call.Principal != "sns.amazonaws.com" {
			t.Errorf("child event = %+v", call)
		}
	})
}

func TestDispatchMapsHandlerContractBreaches(t *testing.T) {
	cases := map[string]kernel.Handler[sqsapi.SendMessageInput, sqsapi.SendMessageOutput]{
		"nil, nil": func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			return nil, nil
		},
		"out, err": func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			return &sqsapi.SendMessageOutput{}, sqsapi.ErrQueueDoesNotExist("gone")
		},
		"panic": func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			panic("boom")
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, func(k *kerneltest.K) {
				t := k.T
				k.Stub(sqsapi.OpSendMessage, h)
				c := k.Call(sqsapi.OpSendMessage.Descriptor())
				_, err := kerneltest.Dispatch[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](t.Context(), k, c, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
				cause := internalCause(t, err)
				if !strings.Contains(cause.Error(), "sqs.SendMessage") {
					t.Errorf("cause = %v, want it to name sqs.SendMessage", cause)
				}
				if strings.Contains(err.Message, "boom") || strings.Contains(err.Message, "sqs.SendMessage") {
					t.Errorf("message %q leaks the cause", err.Message)
				}
				tree, ok := k.Journal.Trace(c.Event())
				call, isCall := tree.Event.Detail.(journal.APICall)
				failed, isFailed := call.Outcome.(journal.Failed)
				if !ok || !isCall || !isFailed || !strings.Contains(failed.Message, cause.Error()) {
					t.Fatalf("journaled outcome = %+v, want Failed naming the cause %q", tree.Event.Detail, cause)
				}
				if name == "panic" && !strings.Contains(failed.Message, "goroutine ") {
					t.Errorf("journaled message %q lacks the panic's stack", failed.Message)
				}
			})
		})
	}
}

// Bound validators, checks and resource resolvers are service code too: a
// panic in one answers 500 InternalFailure and closes the journal event
// with the stack, as a handler's does (ARCHITECTURE.md 4).
func TestPanicBeforeTheHandlerIsInternalAndJournaled(t *testing.T) {
	cases := map[string]kernel.BindOption{
		"validator": kernel.WithValidator(func(*sqsapi.SendMessageInput) []awserr.Violation { panic("boom") }),
		"check":     kernel.WithCheck(func(*sqsapi.SendMessageInput) *awserr.Error { panic("boom") }),
		"resources": kernel.WithResources(func(*kernel.Call, *sqsapi.SendMessageInput) []aws.ARN { panic("boom") }),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, func(k *kerneltest.K) {
				t := k.T
				ran := false
				kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpSendMessage,
					func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
						ran = true
						return &sqsapi.SendMessageOutput{}, nil
					}, registry.Full{}, opt)
				if err := k.Kernel.BindErr(); err != nil {
					t.Fatal(err)
				}
				c := k.Call(sqsapi.OpSendMessage.Descriptor())
				_, err := kerneltest.Dispatch[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](t.Context(), k, c, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
				cause := internalCause(t, err)
				if p, ok := errors.AsType[*kernel.PanicError](cause); !ok || p.Value != "boom" || p.Op != "sqs.SendMessage" {
					t.Errorf("cause = %v, want the sqs.SendMessage PanicError of boom", cause)
				}
				if strings.Contains(err.Message, "boom") {
					t.Errorf("message %q leaks the panic", err.Message)
				}
				if ran {
					t.Error("the handler ran after a panic before it")
				}
				tree, ok := k.Journal.Trace(c.Event())
				call, isCall := tree.Event.Detail.(journal.APICall)
				failed, isFailed := call.Outcome.(journal.Failed)
				if !ok || !isCall || !isFailed || failed.Status != 500 || !strings.Contains(failed.Message, "goroutine ") {
					t.Fatalf("journaled outcome = %+v, want Failed 500 with the panic's stack", tree.Event.Detail)
				}
			})
		})
	}
}

func TestUnboundOperationAnswers501AndIsJournaled(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		op := sqsapi.OpSendMessage
		if _, ok := k.Kernel.Bindings().Status(op.Descriptor()).(kernel.Unbound); !ok {
			t.Fatal("SendMessage is bound before anything bound it")
		}
		c := k.Call(op.Descriptor())
		_, err := kerneltest.Dispatch[sqsapi.SendMessageInput, sqsapi.SendMessageOutput](t.Context(), k, c, nil)
		if err == nil || err.ID.Status != 501 || err.ID.QueryCode != "NotImplemented" {
			t.Fatalf("err = %+v, want 501 NotImplemented", err)
		}
		want := "understudy does not implement sqs.SendMessage. Run 'understudy coverage --service sqs' for what is supported; to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=sqs.SendMessage"
		if err.Message != want {
			t.Errorf("message = %q, want %q", err.Message, want)
		}
		tree, ok := k.Journal.Trace(c.Event())
		if !ok {
			t.Fatal("the 501 is not journaled")
		}
		call, ok := tree.Event.Detail.(journal.APICall)
		failed, isFailed := call.Outcome.(journal.Failed)
		if !ok || !isFailed || failed.Status != 501 {
			t.Errorf("outcome = %+v, want Failed 501", tree.Event.Detail)
		}
	})
}

func TestRejectedDecodeIsJournaledLikeAnyCall(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		c := k.Call(sqsapi.OpSendMessage.Descriptor())
		res := k.Kernel.Dispatch(t.Context(), c, kernel.Rejected{Err: &awserr.Error{ID: awserr.SerializationException(), Origin: awserr.Generic{}}})
		if f, ok := res.(kernel.Failure); !ok || f.Err.ID != awserr.SerializationException() {
			t.Fatalf("Dispatch = %#v, want the rejection", res)
		}
		tree, ok := k.Journal.Trace(c.Event())
		if !ok {
			t.Fatal("the rejected call is not journaled")
		}
		call, ok := tree.Event.Detail.(journal.APICall)
		want := journal.Failed{Identity: journal.ErrorIdentity{Shape: string(awserr.SerializationException().Shape)}, Status: 400}
		if !ok || call.Outcome != want {
			t.Errorf("event = %+v, want outcome %+v", tree.Event.Detail, want)
		}
	})
}

func TestValidationRunsGeneratedThenTypedValidatorsBeforeFaults(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		if _, err := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{sqsapi.OpSendMessage.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.Always{},
		}); err != nil {
			t.Fatal(err)
		}
		ran := false
		kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpSendMessage,
			func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
				ran = true
				return &sqsapi.SendMessageOutput{}, nil
			}, registry.Full{},
			kernel.WithValidator(func(in *sqsapi.SendMessageInput) []awserr.Violation {
				if in.MessageBody == "typed" {
					return []awserr.Violation{{Path: awserr.Path{"delaySeconds"}, Value: awserr.RenderedText{Text: "901"}, Constraint: awserr.RangeMax{Max: "900"}}}
				}
				return nil
			}))
		_, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b", DelaySeconds: new(int32(901))})
		if err == nil || err.Message != "Value 901 for parameter DelaySeconds is invalid. Reason: Must be >= 0 and <= 900, if provided." {
			t.Fatalf("generated validation: err = %+v", err)
		}
		_, err = k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "typed"})
		if err == nil || err.ID.QueryCode != "InvalidParameterValue" {
			t.Fatalf("typed validation: err = %+v", err)
		}
		_, err = k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
		if _, injected := err.Origin.(awserr.Injected); !injected {
			t.Fatalf("valid input: err = %+v, want the injected throttle", err)
		}
		if ran {
			t.Error("the handler ran although every call failed before it")
		}
	})
}

// TestResourceScopedRequestFaultsSeeTheResourcesBeforeTheHandler: a
// Request-phase rule scoped by resource matches on what WithResources names,
// so it throttles a call to a matching queue before its handler runs and
// leaves a call to another queue alone.
func TestResourceScopedRequestFaultsSeeTheResourcesBeforeTheHandler(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		if _, err := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Services: []ref.ServiceID{"sqs"}, Resource: fault.NewARNGlob("arn:aws:sqs:*:*:orders-*")},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.Always{},
		}); err != nil {
			t.Fatal(err)
		}
		var handled []string
		kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpSendMessage,
			func(_ context.Context, _ *kernel.Call, in *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
				handled = append(handled, in.QueueUrl)
				return &sqsapi.SendMessageOutput{}, nil
			}, registry.Full{},
			kernel.WithResources(func(c *kernel.Call, in *sqsapi.SendMessageInput) []aws.ARN {
				return []aws.ARN{{Partition: "aws", Service: "sqs", Region: c.Scope().Region.String(), Account: c.Scope().Account.String(), Resource: in.QueueUrl}}
			}))
		_, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "orders-1", MessageBody: "b"})
		if err == nil {
			t.Fatal("send to orders-1 succeeded, want the injected throttle")
		}
		if _, injected := err.Origin.(awserr.Injected); !injected {
			t.Errorf("send to orders-1 = %+v, want the injected throttle", err)
		}
		if _, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "billing", MessageBody: "b"}); err != nil {
			t.Errorf("send to billing = %v, want it untouched by the rule", err.AsError())
		}
		if fmt.Sprint(handled) != "[billing]" {
			t.Errorf("handled %v, want only billing", handled)
		}
	})
}

// TestClientLeavingALatencyHoldJournalsTheHitsAlreadySpent: the rules after
// a latency rule are evaluated with it, so a NextN rule behind it has spent
// its hit when the client leaves during the hold. That hit is journaled, or
// it would vanish from the trace while the rule's budget still shrank.
func TestClientLeavingALatencyHoldJournalsTheHitsAlreadySpent(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		send := []ref.OperationID{sqsapi.OpSendMessage.Descriptor().ID}
		latency, err := k.Faults.Add(fault.Rule{Match: fault.Match{Operations: send}, Phase: fault.Request, Action: fault.Latency{Wall: time.Hour}, Trigger: fault.Always{}})
		if err != nil {
			t.Fatal(err)
		}
		throttle, err := k.Faults.Add(fault.Rule{Match: fault.Match{Operations: send}, Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.NextN{N: 1}})
		if err != nil {
			t.Fatal(err)
		}
		kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpSendMessage,
			func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
				return &sqsapi.SendMessageOutput{}, nil
			}, registry.Full{})
		ctx, leave := context.WithCancel(t.Context())
		done := make(chan *awserr.Error, 1)
		go func() {
			_, err := k.Invoke(ctx, sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
			done <- err
		}()
		synctest.Wait() // the call is held by the latency rule
		leave()
		if err := <-done; err == nil || err.ID != awserr.RequestAborted().ID {
			t.Fatalf("Invoke = %+v, want RequestAborted", err)
		}
		var hit []ref.RuleID
		for _, e := range k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events {
			if d, ok := e.Detail.(journal.FaultInjected); ok {
				hit = append(hit, d.Rule)
			}
		}
		if want := []ref.RuleID{latency, throttle}; fmt.Sprint(hit) != fmt.Sprint(want) {
			t.Errorf("journaled fault hits %v, want %v: the throttle's hit was spent during the hold", hit, want)
		}
	})
}

// TestTheCallEventKeepsTheTraceHeader: ADR 0007 journals the X-Amzn-Trace-Id
// a request carried.
func TestTheCallEventKeepsTheTraceHeader(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		const header = "Root=1-5759e988-bd862e3fe1be46a994272793;Sampled=1"
		c := k.Kernel.NewCall(kernel.ExternalCall{
			RequestID: k.Kernel.IDs().RequestID(),
			Operation: sqsapi.OpSendMessage.Descriptor().ID,
			Scope:     k.Scope,
			Principal: identity.Root{Account: k.Scope.Account},
			Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
			Admitted:  k.Clock.Now(),
			Header:    http.Header{"X-Amzn-Trace-Id": {header}},
		})
		k.Kernel.Dispatch(t.Context(), c, kernel.Ok{In: &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}})
		tree, ok := k.Journal.Trace(c.Event())
		if !ok {
			t.Fatal("the call has no journal trace")
		}
		if call, ok := tree.Event.Detail.(journal.APICall); !ok || call.TraceHeader != header {
			t.Errorf("event = %+v, want an APICall with TraceHeader %q", tree.Event.Detail, header)
		}
	})
}

// newKernel builds a kernel that is never run.
func newKernel(t *testing.T) *kernel.Kernel {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	return newKernelWith(t, clk, timers, nil)
}

func TestBindMistakesAreReportedAndStopRun(t *testing.T) {
	k := newKernel(t)
	h := func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
		return &sqsapi.SendMessageOutput{}, nil
	}
	b := k.Binder(sqsapi.Service())
	kernel.Bind(b, sqsapi.OpSendMessage, h, registry.Full{})
	kernel.Bind(b, sqsapi.OpSendMessage, h, registry.Full{})
	kernel.Bind(k.Binder(&registry.Service{ID: "other"}), sqsapi.OpSendMessage, h, registry.Full{})
	k.Timers().Handle(kindLater, func(*kernel.Call, clock.TimerKey, clock.Instant) {})
	k.Timers().Handle(kindLater, func(*kernel.Call, clock.TimerKey, clock.Instant) {})
	err := k.BindErr()
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("BindErr = %v, want the joined mistakes", err)
	}
	var problems []string
	for _, one := range joined.Unwrap() {
		var bind *kernel.BindError
		var kind *kernel.TimerKindError
		switch {
		case errors.As(one, &bind):
			problems = append(problems, fmt.Sprint(bind.Problem))
		case errors.As(one, &kind):
			problems = append(problems, string(kind.Kind))
		}
	}
	want := []string{fmt.Sprint(kernel.BoundTwice), fmt.Sprint(kernel.ForeignOperation), string(kindLater)}
	if fmt.Sprint(problems) != fmt.Sprint(want) {
		t.Errorf("bind problems = %v, want %v (%v)", problems, want, err)
	}
	if err := k.Run(t.Context()); err == nil {
		t.Error("Run started with bind mistakes")
	}
}

// A timer armed straight on the scheduler, as Stateful.Rearm does, is
// reported when no handler is registered for its kind.
func TestUnhandledListsArmedTimersWithoutAHandler(t *testing.T) {
	timers := kernel.NewTimers()
	handled := timers.Handle("fake.handled", func(*kernel.Call, clock.TimerKey, clock.Instant) {})
	at := clock.Instant{}.Add(time.Second)
	timers.Scheduler().Arm(handled.Key("a"), at)
	if got := timers.Unhandled(); len(got) != 0 {
		t.Fatalf("Unhandled with only handled timers armed = %v, want none", got)
	}
	orphans := []clock.TimerKey{{Kind: "fake.unhandled", ID: "b"}, {Kind: "other.unhandled", ID: "c"}}
	for i, key := range orphans {
		timers.Scheduler().Arm(key, at.Add(time.Duration(i+1)*time.Second))
	}
	if got := timers.Unhandled(); !slices.Equal(got, orphans) {
		t.Errorf("Unhandled = %v, want %v in firing order", got, orphans)
	}
}

func TestATimerTableServesOneKernel(t *testing.T) {
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	opts := kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j}
	if _, err := kernel.New(opts); err != nil {
		t.Fatal(err)
	}
	var attach *kernel.AttachError
	if _, err := kernel.New(opts); !errors.As(err, &attach) {
		t.Errorf("second New with the same Timers = %v, want an AttachError", err)
	}
	opts.Journal = nil
	var missing *kernel.OptionsError
	if _, err := kernel.New(opts); !errors.As(err, &missing) || missing.Field != "Journal" {
		t.Errorf("New without a journal = %v, want an OptionsError for Journal", err)
	}
}

// TestTwoKernelsBindTheSameOperationIndependently runs two kernels with
// different SendMessage handlers at once; -race checks that bindings never
// touch the shared descriptors.
func TestTwoKernelsBindTheSameOperationIndependently(t *testing.T) {
	t.Parallel()
	for i := range 2 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			run(t, func(k *kerneltest.K) {
				t := k.T
				id := fmt.Sprintf("kernel-%d", i)
				k.Stub(sqsapi.OpSendMessage, func(context.Context, *kernel.Call, *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
					return &sqsapi.SendMessageOutput{MessageId: &id}, nil
				})
				var wg sync.WaitGroup
				for range 50 {
					wg.Go(func() {
						out, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"})
						if err != nil || *out.MessageId != id {
							t.Errorf("Invoke = %v, %v; want MessageId %s", out, err, id)
						}
					})
				}
				wg.Wait()
			})
		})
	}
}

const (
	kindBlock clock.TimerKind = "test.block"
	kindLater clock.TimerKind = "test.later"
)

// timer is one timer a test arms: its kind, id and delay from now.
type timer struct {
	kind  kernel.HandledKind
	id    string
	after time.Duration
}

// armTimers arms each timer through a Write, as handlers do.
func armTimers(t *testing.T, k *kerneltest.K, timers ...timer) {
	t.Helper()
	p := cells(k)
	c := k.Call(sqsapi.OpSendMessage.Descriptor())
	_, err := kernel.Write(c, p, k.Scope, func(_ *int, now clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
		for _, tm := range timers {
			tx.Arm(tm.kind, tm.id, now.Add(tm.after))
		}
		return 0, nil
	})
	if err != nil {
		t.Fatal(err.AsError())
	}
}

// A timer's synthetic call is its TimerFired event, which roots the trace
// the call's children join.
func TestATimerCallIsItsTimerFiredEvent(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		var got *kernel.Call
		kind := k.Kernel.Timers().Handle(kindLater, func(c *kernel.Call, _ clock.TimerKey, _ clock.Instant) { got = c })
		armTimers(t, k, timer{kind: kind, id: "a", after: time.Minute})
		if _, err := k.Clock.Advance(time.Minute); err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Fatal("the timer did not fire")
		}
		tree, ok := k.Journal.Trace(got.Event())
		if _, fired := tree.Event.Detail.(journal.TimerFired); !ok || !fired {
			t.Fatalf("the call's event = %+v, want the TimerFired event", tree.Event)
		}
		if got.Cause() != (ref.Cause{Trace: got.Event()}) {
			t.Errorf("cause = %+v, want the trace rooted at the TimerFired event %d", got.Cause(), got.Event())
		}
	})
}

func TestBlockingTimerWorkDoesNotStallLaterTimers(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		fired := make(chan clock.Instant, 1)
		block := k.Kernel.Timers().Handle(kindBlock, func(*kernel.Call, clock.TimerKey, clock.Instant) {
			k.Kernel.Go(kernel.InvocationRunning, func(ctx context.Context) { <-ctx.Done() })
		})
		later := k.Kernel.Timers().Handle(kindLater, func(_ *kernel.Call, _ clock.TimerKey, due clock.Instant) { fired <- due })
		start := k.Clock.Now()
		armTimers(t, k, timer{kind: block, id: "a", after: time.Minute}, timer{kind: later, id: "b", after: 2 * time.Minute})
		if _, err := k.Clock.Advance(3 * time.Minute); err != nil {
			t.Fatal(err)
		}
		select {
		case due := <-fired:
			if due != start.Add(2*time.Minute) {
				t.Errorf("later timer fired at %s, want %s", due, start.Add(2*time.Minute))
			}
		default:
			t.Fatal("the later timer did not fire while the earlier timer's work blocked")
		}
		pending, err := k.Kernel.Settle(t.Context(), 10*time.Second)
		var settle *kernel.SettleError
		if !errors.As(err, &settle) || len(pending) != 1 || pending[kernel.InvocationRunning] != 1 {
			t.Errorf("Settle = %v, %v; want one invocation-running", pending, err)
		}
	})
}

// TestSettleGroupsPendingWorkByKind: a parked delivery attempt and a due
// timer whose handler is still running are both pending, by kind; a timer
// not yet due is not.
func TestSettleGroupsPendingWorkByKind(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		entered, hold, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		block := k.Kernel.Timers().Handle(kindBlock, func(*kernel.Call, clock.TimerKey, clock.Instant) {
			close(entered)
			<-hold
		})
		later := k.Kernel.Timers().Handle(kindLater, func(*kernel.Call, clock.TimerKey, clock.Instant) {})
		k.Kernel.Go(kernel.DeliveryAttempt, func(context.Context) { <-release })
		armTimers(t, k, timer{kind: block, id: "due", after: time.Minute}, timer{kind: later, id: "not-due", after: time.Hour})
		advanced := make(chan error, 1)
		go func() {
			_, err := k.Clock.Advance(time.Minute)
			advanced <- err
		}()
		<-entered
		// The clock driver waits on the step lock the held handler keeps, so
		// the bubble's time cannot pass: ask with a context already ended.
		ended, end := context.WithCancel(t.Context())
		end()
		pending, err := k.Kernel.Settle(ended, time.Second)
		if !isA[*kernel.SettleError](err) || pending.String() != "delivery-attempt=1, timer-due=1" {
			t.Errorf("Settle = %v, %v; want a SettleError with delivery-attempt=1, timer-due=1", pending, err)
		}
		close(hold)
		close(release)
		if err := <-advanced; err != nil {
			t.Fatal(err)
		}
		if pending, err := k.Kernel.Settle(t.Context(), time.Second); err != nil || len(pending) != 0 {
			t.Errorf("Settle after the work ended = %v, %v; want settled", pending, err)
		}
	})
}

// TestARefusedEffectKeepsStateAndTimersInStep: a write whose Deliver the
// kernel refuses (no sink) answers Internal, but the state it changed and
// the timer it armed both stand, so the scheduler agrees with the state.
// The zero HandledKind arms nothing and is Internal too.
func TestARefusedEffectKeepsStateAndTimersInStep(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		later := k.Kernel.Timers().Handle(kindLater, func(*kernel.Call, clock.TimerKey, clock.Instant) {})
		p := cells(k)
		c := k.Call(sqsapi.OpSendMessage.Descriptor())
		_, err := kernel.Write(c, p, k.Scope, func(n *int, now clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
			*n++
			tx.Arm(later, "kept", now.Add(time.Minute))
			tx.Deliver(delivery.Request{Mechanism: delivery.SNSToSQS})
			return 0, nil
		})
		if !isA[*kernel.NoDeliverSinkError](internalCause(t, err)) {
			t.Fatalf("err = %v, want a NoDeliverSinkError", err)
		}
		state := store.Read(p.For(k.Scope), func(n *int) int { return *n })
		armed := k.Kernel.Timers().Scheduler().Snapshot()
		if state != 1 || len(armed) != 1 || armed[0].Key != later.Key("kept") {
			t.Errorf("after the refused delivery: state %d, timers %+v; want the write's state change and its timer", state, armed)
		}

		_, err = kernel.Write(c, p, k.Scope, func(_ *int, now clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
			tx.Arm(kernel.HandledKind{}, "nobody", now)
			return 0, nil
		})
		if unregistered, ok := errors.AsType[*kernel.UnregisteredKindError](internalCause(t, err)); !ok || unregistered.ID != "nobody" {
			t.Errorf("err = %v, want an UnregisteredKindError for timer nobody", err)
		}
		if n := len(k.Kernel.Timers().Scheduler().Snapshot()); n != 1 {
			t.Errorf("%d timers armed after arming the zero kind, want only the earlier one", n)
		}
	})
}

// zeroStamp is an output whose required timestamp member is left at the
// zero time.
type zeroStamp struct{ At time.Time }

func (z *zeroStamp) Serialize(s smithy.Serializer) {
	schema := &smithy.Schema{ID: "test#ZeroStamp", Type: smithy.TypeStructure}
	s.WriteStruct(nil, schema)
	s.WriteTimestamp(&smithy.Member{Name: "CreatedAt", Required: true}, z.At)
	s.CloseStruct()
}

func TestStrictRefusesZeroRequiredTimestamps(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		op := sqsOp[sqsapi.GetQueueUrlInput, zeroStamp](t, "GetQueueUrl")
		var at time.Time
		k.Stub(op, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*zeroStamp, *awserr.Error) {
			return &zeroStamp{At: at}, nil
		})
		_, err := k.Invoke(t.Context(), op, &sqsapi.GetQueueUrlInput{QueueName: "q"})
		var zero *kernel.ZeroTimestampError
		if !errors.As(internalCause(t, err), &zero) || fmt.Sprint(zero.Members) != "[CreatedAt]" {
			t.Errorf("err = %v, want a ZeroTimestampError naming CreatedAt", err)
		}
		at = k.Clock.Now().Wire()
		if _, err := k.Invoke(t.Context(), op, &sqsapi.GetQueueUrlInput{QueueName: "q"}); err != nil {
			t.Errorf("a set timestamp: %v", err.AsError())
		}
	})
}
