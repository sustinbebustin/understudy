package outbox_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
)

// Settle counts an HTTP attempt in flight and returns once it ends; the
// retry it arms for a later instant is not in flight (sns-http-delivery.md
// 8.2 item 7). The receiver's hold channel is made inside the bubble, so
// the receiver blocked on it is durably blocked.
func TestSNSHTTPSettleWaitsForTheAttemptInFlight(t *testing.T) {
	r := answeringHook(http.StatusServiceUnavailable)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		r.hold = make(chan struct{})
		enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy()), source(k))
		done := make(chan error, 1)
		go func() {
			_, err := k.Kernel.Settle(context.Background(), settle)
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			k.Fatalf("Settle returned (%v) while the attempt was in flight", err)
		default:
		}
		close(r.hold)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				k.Errorf("Settle = %v, want nothing in flight", err)
			}
		default:
			k.Fatal("Settle did not return once the attempt ended")
		}
		if res := o.Resources(k.Scope); len(res) != 1 {
			k.Errorf("Resources = %v, want the delivery pending its retry", res)
		}
	})
}

// A permanent answer, a 4xx other than 429 under sns.http.status-classes,
// is not retried: the notification goes to its dead-letter queue at once
// (sns-http-delivery.md 6.1, 6.5), and the attempt's exchange is classed
// permanent with the endpoint's status.
func TestSNSHTTPPermanentAnswerGoesToTheDLQAtOnce(t *testing.T) {
	r := answeringHook(http.StatusNotFound)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		dlq := arn("sqs", "orders-dlq")
		req := toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy())
		req.DLQ = &dlq
		enqueue(k, o, req, source(k))
		settled(k)
		advance(k, time.Hour)
		if n := len(r.requests()); n != 1 {
			k.Errorf("the endpoint got %d requests, want 1: a permanent failure is not retried", n)
		}
		if got := q.sent(); len(got) != 1 || got[0].queue != "orders-dlq" {
			k.Errorf("SendMessage calls = %+v, want one dead letter to orders-dlq", got)
		}
		_, ds := httpDeliveries(k)
		if len(ds) != 1 || ds[0].Outcome != (journal.DeadLettered{DLQ: dlq}) {
			k.Fatalf("deliveries = %+v, want one attempt dead-lettered to %s", ds, dlq)
		}
		if x := ds[0].HTTP; x == nil || x.Status != http.StatusNotFound || x.Class != journal.HTTPPermanent {
			k.Errorf("exchange = %+v, want status 404 classed permanent", x)
		}
	})
}

// A raw notification's dead letter is its body with the message attributes
// as SQS message attributes, and nothing else: no error attributes
// (sns.http.dlq-body as-sqs, sns-http-delivery.md 6.5, 8.8).
func TestSNSHTTPDeadLetterCarriesTheRawAttributes(t *testing.T) {
	r := answeringHook(http.StatusBadRequest)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		dlq := arn("sqs", "orders-dlq")
		req := toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy())
		target, ok := req.Target.(delivery.HTTPEndpoint)
		if !ok {
			k.Fatal("target is not an HTTPEndpoint")
		}
		target.Body = []byte("raw message")
		target.DeadLetterAttributes = []delivery.MessageAttribute{{Name: "store", Value: delivery.StringAttribute{Value: "north"}}}
		req.Target, req.DLQ = target, &dlq
		enqueue(k, o, req, source(k))
		settled(k)
		got := q.sent()
		if len(got) != 1 {
			k.Fatalf("SendMessage calls = %+v, want one dead letter", got)
		}
		in := got[0].in
		if in.MessageBody != "raw message" {
			k.Errorf("dead letter body = %q, want the raw message", in.MessageBody)
		}
		attr, ok := in.MessageAttributes["store"]
		if len(in.MessageAttributes) != 1 || !ok || attr.DataType != "String" || attr.StringValue == nil || *attr.StringValue != "north" {
			k.Errorf("dead letter attributes = %+v, want store=north alone", in.MessageAttributes)
		}
	})
}

// A delivery-phase latency rule runs first, and an http-status rule then
// answers the attempt after the wait; both hits are journaled
// (sns-http-delivery.md 8.2 item 2).
func TestSNSHTTPLatencyThenInjectedStatus(t *testing.T) {
	r := answeringHook(http.StatusOK)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		slow, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.NextN{N: 1}})
		if err != nil {
			k.Fatal(err)
		}
		status, err := k.Faults.Add(fault.Rule{
			Match: fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToHTTP}}, Phase: fault.Delivery,
			Action: fault.HTTPStatus{Status: http.StatusTooManyRequests}, Trigger: fault.NextN{N: 1},
		})
		if err != nil {
			k.Fatal(err)
		}
		t0 := k.Clock.Now()
		enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy()), source(k))
		settled(k)
		if n := len(r.requests()); n != 0 {
			k.Errorf("the endpoint got %d requests during the injected attempt, want none", n)
		}
		_, ds := httpDeliveries(k)
		if len(ds) != 1 || ds[0].Outcome != (journal.Retrying{Next: t0.Add(20 * time.Second)}) {
			k.Fatalf("deliveries = %+v, want the injected 429 retrying at +20s", ds)
		}
		if x := ds[0].HTTP; x == nil || *x != (journal.HTTPExchange{Status: http.StatusTooManyRequests, Class: journal.HTTPRetryable}) {
			k.Errorf("exchange = %+v, want the injected 429 classed retryable with no duration", x)
		}
		hits := k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events
		rules := make([]journal.FaultInjected, 0, len(hits))
		for _, ev := range hits {
			rules = append(rules, detail[journal.FaultInjected](k, ev))
		}
		if want := []journal.FaultInjected{{Rule: slow, Hit: 1, Of: 1}, {Rule: status, Hit: 1, Of: 1}}; !slices.Equal(rules, want) {
			k.Errorf("fault events = %+v, want the latency hit, then the status hit", rules)
		}
	})
}

// A drop-delivery rule drops an HTTP attempt before it is made, so its
// Delivery event has no exchange.
func TestSNSHTTPDroppedAttemptHasNoExchange(t *testing.T) {
	r := answeringHook(http.StatusOK)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		if _, err := k.Faults.Add(fault.Rule{Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.NextN{N: 1}}); err != nil {
			k.Fatal(err)
		}
		enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy()), source(k))
		settled(k)
		_, ds := httpDeliveries(k)
		if len(ds) != 1 || ds[0].HTTP != nil {
			k.Fatalf("deliveries = %+v, want one dropped attempt with no exchange", ds)
		}
		if _, ok := ds[0].Outcome.(journal.Dropped); !ok {
			k.Errorf("outcome = %#v, want Dropped", ds[0].Outcome)
		}
		if n := len(r.requests()); n != 0 {
			k.Errorf("the endpoint got %d requests, want none", n)
		}
	})
}
