package kernel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

func TestDeliverWithoutASinkIsInternal(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		c := k.Call(sqsapi.OpSendMessage.Descriptor())
		_, err := kernel.Write(c, cells(k), k.Scope, func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
			tx.Deliver(delivery.Request{Mechanism: delivery.SNSToSQS})
			return 0, nil
		})
		if !isA[*kernel.NoDeliverSinkError](internalCause(t, err)) {
			t.Errorf("err = %v, want a NoDeliverSinkError", err)
		}
	})
}

// sink records the deliveries it accepts.
type sink struct {
	mu   sync.Mutex
	got  []kernel.Delivery
	fail error
}

func (s *sink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return s.fail
}

// A delivery reaches the sink after the write commits, caused by the call
// and accepted at the commit instant.
func TestDeliveriesRunAfterCommitWithTheCallsCause(t *testing.T) {
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	s := &sink{}
	k := newKernelWith(t, clk, timers, s)
	c := k.NewCall(kernel.ExternalCall{Operation: sqsapi.OpSendMessage.Descriptor().ID, Scope: scope(), Admitted: clk.Now()})
	k.Dispatch(t.Context(), c, kernel.Ok{In: &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}})
	p := cellsOf(k)
	req := delivery.Request{Mechanism: delivery.SNSToSQS, Target: delivery.SQSQueue{Body: "x"}}
	if _, err := kernel.Write(c, p, scope(), func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
		tx.Deliver(req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.got) != 0 {
			t.Error("the delivery reached the sink before the write committed")
		}
		return 0, nil
	}); err != nil {
		t.Fatal(err.AsError())
	}
	if len(s.got) != 1 || s.got[0].Cause.Parent != c.Event() || s.got[0].Accepted != clk.Now() {
		t.Fatalf("deliveries = %+v, want one caused by event %d", s.got, c.Event())
	}
	s.fail = errors.New("outbox full")
	_, werr := kernel.Write(c, p, scope(), func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
		tx.Deliver(req)
		return 0, nil
	})
	if !isA[*kernel.DeliverError](internalCause(t, werr)) {
		t.Errorf("a refused delivery = %v, want a DeliverError", werr)
	}
}

// A handler's records carry the call's event as their parent and its trace:
// Call.Record at once, Tx.Record only when the write commits. A malformed
// record fails the write as Internal after it commits.
func TestRecordsAreCausedByTheCall(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		p := cells(k)
		queue := aws.ARN{Partition: "aws", Service: "sqs", Region: k.Scope.Region.String(), Account: k.Scope.Account.String(), Resource: "q"}
		advice := func(text string) journal.Advisory {
			return journal.Advisory{Kind: journal.AdvisoryPolicyWouldDeny, Resource: queue, Text: text}
		}
		var call ref.Cause
		var werr *awserr.Error
		k.Stub(sqsapi.OpSendMessage, func(ctx context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			call = ref.Cause{Parent: c.Event(), Trace: c.Cause().Trace}
			c.Record(ctx, c.Scope(), advice("recorded"))
			if _, err := kernel.Write(c, p, c.Scope(), func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
				tx.Record(c.Scope(), advice("rolled back"))
				return 0, sqsapi.ErrQueueDoesNotExist("no")
			}); err == nil {
				t.Error("the failing write succeeded")
			}
			if _, err := kernel.Write(c, p, c.Scope(), func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
				tx.Record(c.Scope(), advice("committed"))
				return 0, nil
			}); err != nil {
				return nil, err
			}
			_, werr = kernel.Write(c, p, c.Scope(), func(_ *int, _ clock.Instant, tx *kernel.Tx) (int, *awserr.Error) {
				tx.Record(c.Scope(), journal.Delivery{From: queue, To: queue})
				return 0, nil
			})
			return &sqsapi.SendMessageOutput{}, nil
		})
		if _, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}); err != nil {
			t.Fatal(err.AsError())
		}
		events := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
		texts := make([]string, 0, len(events))
		for _, e := range events {
			if e.Cause != call || e.Scope != k.Scope {
				t.Errorf("record %+v is not caused by the call (%+v) in %v", e, call, k.Scope)
			}
			if a, ok := e.Detail.(journal.Advisory); ok {
				texts = append(texts, a.Text)
			}
		}
		if call.Parent == 0 || !slices.Equal(texts, []string{"recorded", "committed"}) {
			t.Errorf("records = %q of call %+v, want the call's and the committed write's", texts, call)
		}
		if !isA[*kernel.RecordError](internalCause(t, werr)) {
			t.Errorf("a malformed record = %v, want a RecordError", werr)
		}
	})
}

func TestCatalogueListsModeledAndGenericErrors(t *testing.T) {
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	cat := kernel.NewCatalogue(reg, map[ref.ServiceID]awserr.Identity{"sqs": sqsapi.ThrottleError()})
	errs, ok := cat.Errors(ref.OperationID{Service: "sqs", Name: "SendMessage"})
	if !ok {
		t.Fatal("SendMessage has no catalogue")
	}
	for _, want := range []awserr.Identity{sqsapi.IdentityQueueDoesNotExist(), awserr.UnknownOperationException(), awserr.InvalidAction()} {
		if !slices.Contains(errs, want) {
			t.Errorf("the catalogue lacks %s", want.Shape)
		}
	}
	if _, ok := cat.Errors(ref.OperationID{Service: "sqs", Name: "Bogus"}); ok {
		t.Error("an unknown operation has a catalogue")
	}
	if cat.Throttle("sqs") != sqsapi.IdentityRequestThrottled() || cat.Throttle("nope") != (awserr.Identity{}) {
		t.Errorf("Throttle = %v, %v", cat.Throttle("sqs"), cat.Throttle("nope"))
	}
}

func TestResolveScopeForms(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		c := k.Call(sqsapi.OpSendMessage.Descriptor())
		mismatch := sqsapi.ErrQueueDoesNotExist("elsewhere")
		arn := func(region, account string) aws.ARN {
			return aws.ARN{Partition: "aws", Service: "sqs", Region: region, Account: account, Resource: "q"}
		}
		if s, err := kernel.ResolveScope(c, kernel.SameScope{ARN: arn("us-east-1", "000000000000"), Mismatch: mismatch}); err != nil || s != k.Scope {
			t.Errorf("same-scope ARN = %v, %v", s, err)
		}
		for _, other := range []aws.ARN{arn("eu-west-1", "000000000000"), arn("us-east-1", "111111111111")} {
			if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: other, Mismatch: mismatch}); err != mismatch {
				t.Errorf("ARN %s = %v, want the service's mismatch error", other, err)
			}
		}
		_, noMismatch := kernel.ResolveScope(c, kernel.SameScope{ARN: arn("eu-west-1", "000000000000")})
		if !isA[*kernel.TargetError](internalCause(t, noMismatch)) {
			t.Errorf("an ARN outside the scope with no Mismatch = %v, want Internal", noMismatch)
		}
		other, err := aws.ParseAccountID("111111111111")
		if err != nil {
			t.Fatal(err)
		}
		if s, err := kernel.ResolveScope(c, kernel.CrossAccount{Account: other}); err != nil || s.Account != other || s.Region != k.Scope.Region {
			t.Errorf("cross-account = %v, %v", s, err)
		}
		if s, err := kernel.ResolveScope(c, kernel.Global{}); err != nil || s != (aws.Scope{Partition: aws.PartitionAWS()}) {
			t.Errorf("global = %v, %v", s, err)
		}
	})
}

func TestCallsAreAccessRecordedUnlessInjected(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		k.Stub(sqsapi.OpSendMessage, func(_ context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			c.Touch(aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "q"})
			return &sqsapi.SendMessageOutput{}, nil
		})
		if _, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}); err != nil {
			t.Fatal(err.AsError())
		}
		uses := k.Access.Usage(access.Filter{})
		if len(uses) != 1 || uses[0].Action.String() != "sqs:SendMessage" || uses[0].Calls != 1 {
			t.Fatalf("usage = %+v, want one sqs:SendMessage", uses)
		}
	})
}

func TestTrackedWorkSurvivesAPanic(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		k.Kernel.Go(kernel.DeliveryAttempt, func(context.Context) { panic("boom") })
		if pending, err := k.Kernel.Settle(t.Context(), time.Second); err != nil || len(pending) != 0 {
			t.Errorf("Settle after a panicking goroutine = %v, %v; want settled", pending, err)
		}
	})
}

// isA reports whether err's chain holds an E.
func isA[E error](err error) bool {
	var target E
	return errors.As(err, &target)
}

func scope() aws.Scope {
	return aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
}

// newKernelWith builds a kernel over clk with deliveries going to d; it is
// never run.
func newKernelWith(t *testing.T, clk *clock.Clock, timers *kernel.Timers, d kernel.DeliverSink) *kernel.Kernel {
	t.Helper()
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	k, err := kernel.New(kernel.Options{Registry: reg, Clock: clk, Timers: timers, IDs: ids.NewSeeded(1), Journal: j, Deliver: d})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func cellsOf(k *kernel.Kernel) *store.Partitioned[aws.Scope, int] {
	return store.NewPartitioned(k.Gate(), store.Codec{Marshal: json.Marshal, Unmarshal: json.Unmarshal}, func(aws.Scope) int { return 0 })
}

// An internal caller reaches a passthrough operation (the TTL sweeper and
// the DynamoDB proxy, ADR 0014): the handler sees a Synthesized wire and
// Invoke hands back the response it answered.
func TestInvokeReadsAPassthroughResponse(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		raw := sqsOp[sqsapi.GetQueueUrlInput, kernel.RawOutput](t, "GetQueueUrl")
		want := kernel.RawResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: []byte(`{"QueueUrl":"u"}`)}
		var wire kernel.Wire
		kernel.BindRaw(k.Kernel.Binder(sqsapi.Service()), raw, func(_ context.Context, _ *kernel.Call, _ *sqsapi.GetQueueUrlInput, w kernel.Wire) (kernel.RawResponse, *awserr.Error) {
			wire = w
			return want, nil
		}, registry.Full{})
		var got *kernel.RawOutput
		k.Stub(sqsapi.OpSendMessage, func(ctx context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
			out, err := kernel.Invoke(ctx, k.Kernel, c, raw,
				kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.SNSDelivery},
				&sqsapi.GetQueueUrlInput{QueueName: "q"})
			if err != nil {
				return nil, err
			}
			got = out
			return &sqsapi.SendMessageOutput{}, nil
		})
		if _, err := k.Invoke(t.Context(), sqsapi.OpSendMessage, &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}); err != nil {
			t.Fatal(err.AsError())
		}
		if _, ok := wire.(kernel.Synthesized); !ok {
			t.Errorf("the handler's wire = %T, want kernel.Synthesized", wire)
		}
		if !k.Kernel.Passthrough(raw.Descriptor()) || k.Kernel.Passthrough(sqsapi.OpSendMessage.Descriptor()) {
			t.Error("Passthrough does not tell the BindRaw operation from the Bind one")
		}
		if got == nil || got.Response.Status != want.Status || !bytes.Equal(got.Response.Body, want.Body) ||
			got.Response.Header.Get("Content-Type") != want.Header.Get("Content-Type") {
			t.Errorf("Invoke output = %+v, want the handler's response %+v", got, want)
		}
	})
}

func TestBindRawHTTPRefusesModeledOperations(t *testing.T) {
	k := newKernel(t)
	kernel.BindRawHTTP(k.Binder(sqsapi.Service()), sqsapi.OpSendMessage.Descriptor(), nil, registry.Full{})
	if !isA[*kernel.BindError](k.BindErr()) {
		t.Errorf("BindErr = %v, want the raw-only refusal", k.BindErr())
	}
}
