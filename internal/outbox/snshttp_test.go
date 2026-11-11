package outbox_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// hookReceiver is an HTTP/1.1 endpoint on an in-memory network behind
// egress's dial seam. It listens on every address in addrs; answer gives
// the status of the n-th request (0-based), and 0 holds the request until
// the client hangs up. hold, when set, blocks each answer until it closes.
type hookReceiver struct {
	addrs  []netip.AddrPort
	answer func(n int) int
	hold   chan struct{}

	mu   sync.Mutex
	got  []hookRequest
	seen int
}

type hookRequest struct {
	addr netip.AddrPort
	head string
	req  *http.Request
	body []byte
}

func answeringHook(status int) *hookReceiver {
	return &hookReceiver{
		addrs:  []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8080"), netip.MustParseAddrPort("127.0.0.1:8081")},
		answer: func(int) int { return status },
	}
}

func (r *hookReceiver) Dial(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
	if !slices.Contains(r.addrs, addr) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go r.serve(addr, server)
	return client, nil
}

func (r *hookReceiver) serve(addr netip.AddrPort, conn net.Conn) {
	defer conn.Close()
	var raw bytes.Buffer
	req, err := http.ReadRequest(bufio.NewReader(io.TeeReader(conn, &raw)))
	if err != nil {
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}
	head, _, _ := strings.Cut(raw.String(), "\r\n\r\n")
	r.mu.Lock()
	n := r.seen
	r.seen++
	r.got = append(r.got, hookRequest{addr: addr, head: head, req: req, body: body})
	r.mu.Unlock()
	if r.hold != nil {
		<-r.hold
	}
	status := r.answer(n)
	if status == 0 {
		if _, err := io.Copy(io.Discard, conn); err != nil {
			return
		}
		return
	}
	if _, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status)); err != nil {
		return
	}
}

func (r *hookReceiver) requests() []hookRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// egressFor is the egress client a producer configures: the emulator clock
// and an allow-list naming the receiver's loopback ports
// (sns-http-delivery.md 7.2).
func egressFor(k *kerneltest.K, r *hookReceiver) *egress.Client {
	k.Helper()
	allow, perr := egress.ParseAllowList(egress.Setting{Service: "sns", Feature: "httpDelivery"}, []string{"127.0.0.1:8080", "127.0.0.1:8081"})
	if perr != nil {
		k.Fatalf("ParseAllowList: %v", perr)
	}
	c, oerr := egress.New(egress.Options{Allow: allow, Clock: k.Clock, Dialer: r})
	if oerr != nil {
		k.Fatalf("egress.New: %v", oerr)
	}
	return c
}

// runHTTP runs fn against a kernel whose outbox sends HTTP deliveries
// through an egress client to r; a nil r binds no client.
func runHTTP(t *testing.T, r *hookReceiver, fn func(k *kerneltest.K, o *outbox.Outbox)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		o := outbox.New()
		oo := outbox.Options{Faults: k.Faults}
		if r != nil {
			oo.Egress = egressFor(k, r)
		}
		if err := o.Bind(k.Kernel, oo); err != nil {
			k.Fatal(err)
		}
		fn(k, o)
	}, kerneltest.Services(sqsapi.Service(), lambdaapi.Service(), snsapi.Service()))
}

// defaultHTTPPolicy is SNS's system default healthyRetryPolicy: three
// retries 20 s apart (sns-http-delivery.md 6.2, 6.3, aws-doc).
func defaultHTTPPolicy() delivery.SNSHTTP {
	return delivery.SNSHTTP{Backoff: 3, MinDelayTarget: 20 * time.Second, MaxDelayTarget: 20 * time.Second, BackoffFunction: delivery.BackoffLinear}
}

var hookHeaders = []delivery.HTTPHeader{
	{Name: "x-amz-sns-message-type", Value: "Notification"},
	{Name: "x-amz-sns-message-id", Value: "22b80b92-fdea-4c2c-8f9d-bdfb0c7bf324"},
	{Name: "Content-Type", Value: "text/plain; charset=UTF-8"},
}

// toHook is an SNS http delivery of kind to rawURL from the orders topic.
func toHook(k *kerneltest.K, rawURL string, kind delivery.HTTPKind, policy delivery.SNSHTTP) delivery.Request {
	k.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		k.Fatal(err)
	}
	return delivery.Request{
		Source:    topic(),
		Mechanism: delivery.SNSToHTTP,
		Target:    delivery.HTTPEndpoint{URL: *u, Kind: kind, Headers: hookHeaders, Body: []byte(`{"Type" : "Notification", "Message" : "hello"}`)},
		Policy:    policy,
	}
}

// httpDeliveries returns the journal's Delivery details, in order.
func httpDeliveries(k *kerneltest.K) ([]journal.Event, []journal.Delivery) {
	k.Helper()
	events := deliveries(k)
	out := make([]journal.Delivery, len(events))
	for i := range events {
		out[i] = detail[journal.Delivery](k, events[i])
	}
	return events, out
}

func TestSNSHTTPEnqueueChecks(t *testing.T) {
	runHTTP(t, nil, func(k *kerneltest.K, o *outbox.Outbox) {
		base := toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy())
		with := func(change func(r *delivery.Request)) delivery.Request {
			r := base
			change(&r)
			return r
		}
		withPolicy := func(change func(p *delivery.SNSHTTP)) delivery.Request {
			p := defaultHTTPPolicy()
			change(&p)
			return with(func(r *delivery.Request) { r.Policy = p })
		}
		withTarget := func(change func(e *delivery.HTTPEndpoint)) delivery.Request {
			e, ok := base.Target.(delivery.HTTPEndpoint)
			if !ok {
				k.Fatal("base target is not an HTTPEndpoint")
			}
			change(&e)
			return with(func(r *delivery.Request) { r.Target = e })
		}
		dlq := arn("sqs", "orders-dlq")
		cases := []struct {
			name  string
			req   delivery.Request
			field string
		}{
			{"sns-to-http to a queue", with(func(r *delivery.Request) { r.Target = delivery.SQSQueue{ARN: arn("sqs", "q"), Body: "b"} }), "Target"},
			{"an endpoint under sns-to-sqs", with(func(r *delivery.Request) { r.Mechanism = delivery.SNSToSQS }), "Target"},
			{"sns-to-http under the managed policy", with(func(r *delivery.Request) { r.Policy = delivery.SNSManaged{} }), "Policy"},
			{"sns-to-http under no policy", with(func(r *delivery.Request) { r.Policy = delivery.None{} }), "Policy"},
			{"the http policy under sns-to-sqs", delivery.Request{Source: topic(), Mechanism: delivery.SNSToSQS, Target: delivery.SQSQueue{ARN: arn("sqs", "q"), Body: "b"}, Policy: defaultHTTPPolicy()}, "Policy"},
			// The documented bounds (sns-http-delivery.md 6.7, aws-doc):
			// phase counts at least 0, numRetries 0 to 100, minDelayTarget
			// from 1 s to maxDelayTarget, maxDelayTarget at most 3600 s.
			{"a negative phase count", withPolicy(func(p *delivery.SNSHTTP) { p.NoDelay = -1 }), "Policy"},
			{"101 retries", withPolicy(func(p *delivery.SNSHTTP) { p.Backoff = 101 }), "Policy"},
			{"a minDelayTarget under 1 s", withPolicy(func(p *delivery.SNSHTTP) { p.MinDelayTarget = 999 * time.Millisecond }), "Policy"},
			{"minDelayTarget above maxDelayTarget", withPolicy(func(p *delivery.SNSHTTP) { p.MinDelayTarget, p.MaxDelayTarget = 30*time.Second, 20*time.Second }), "Policy"},
			{"a maxDelayTarget above 3600 s", withPolicy(func(p *delivery.SNSHTTP) { p.MaxDelayTarget = 3601 * time.Second }), "Policy"},
			{"no backoff function", withPolicy(func(p *delivery.SNSHTTP) { p.BackoffFunction = 0 }), "Policy"},
			{"no kind", withTarget(func(e *delivery.HTTPEndpoint) { e.Kind = 0 }), "Target.Kind"},
			{"an undeclared kind", withTarget(func(e *delivery.HTTPEndpoint) { e.Kind = delivery.HTTPUnsubscribeConfirmation + 1 }), "Target.Kind"},
			{"an ftp endpoint", withTarget(func(e *delivery.HTTPEndpoint) { e.URL = url.URL{Scheme: "ftp", Host: "127.0.0.1:21", Path: "/"} }), "Target.URL"},
		}
		for _, c := range cases {
			_, err := o.Enqueue(c.req, ref.Cause{}, k.Clock.Now())
			if rerr, ok := errors.AsType[*outbox.RequestError](err); !ok || rerr.Field != c.field {
				k.Errorf("%s: Enqueue = %v, want a *RequestError for %s", c.name, err, c.field)
			}
		}
		// A control message never goes to a dead-letter queue
		// (sns.http.control-dlq, sns-http-delivery.md 8.2 item 4).
		for _, kind := range []delivery.HTTPKind{delivery.HTTPSubscriptionConfirmation, delivery.HTTPUnsubscribeConfirmation} {
			req := toHook(k, "http://127.0.0.1:8080/hook", kind, defaultHTTPPolicy())
			req.DLQ = &dlq
			_, err := o.Enqueue(req, ref.Cause{}, k.Clock.Now())
			const reason = "control messages never go to a dead-letter queue (sns.http.control-dlq)"
			if rerr, ok := errors.AsType[*outbox.RequestError](err); !ok || rerr.Field != "DLQ" || rerr.Reason != reason {
				k.Errorf("a %s with a DLQ: Enqueue = %v, want a *RequestError for DLQ: %s", kind, err, reason)
			}
		}
		if r := o.Resources(k.Scope); len(r) != 0 {
			k.Errorf("Resources = %v, want nothing queued", r)
		}
		// The bounds themselves are accepted, as is a notification with a
		// dead-letter queue.
		edge := withPolicy(func(p *delivery.SNSHTTP) {
			p.NoDelay, p.MinDelay, p.Backoff, p.MaxDelay = 25, 25, 25, 25
			p.MinDelayTarget, p.MaxDelayTarget = time.Second, 3600*time.Second
		})
		edge.DLQ = &dlq
		if _, err := o.Enqueue(edge, ref.Cause{}, k.Clock.Now()); err != nil {
			k.Errorf("Enqueue at the documented bounds = %v, want it accepted", err)
		}
	})
}

// A 2xx answer delivers (sns-http-delivery.md 6.1, aws-doc). The attempt is
// one POST of the stored body and headers to the URL as given (3.1, 3.2),
// and its journal entry names the topic, no ARN, the endpoint's host:port,
// the mechanism, the attempt and the exchange.
func TestSNSHTTPDeliveredAttemptIsJournaled(t *testing.T) {
	r := answeringHook(http.StatusNoContent)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		cause := source(k)
		t0 := k.Clock.Now()
		req := toHook(k, "http://127.0.0.1:8080/hooks/orders?attempt=1", delivery.HTTPNotification, defaultHTTPPolicy())
		enqueue(k, o, req, cause)
		settled(k)

		got := r.requests()
		if len(got) != 1 {
			k.Fatalf("the endpoint got %d requests, want 1", len(got))
		}
		if got[0].req.Method != http.MethodPost || got[0].req.RequestURI != "/hooks/orders?attempt=1" {
			k.Errorf("request = %s %s, want POST /hooks/orders?attempt=1", got[0].req.Method, got[0].req.RequestURI)
		}
		target, ok := req.Target.(delivery.HTTPEndpoint)
		if !ok {
			k.Fatal("target is not an HTTPEndpoint")
		}
		if !bytes.Equal(got[0].body, target.Body) {
			k.Errorf("body = %q, want %q", got[0].body, target.Body)
		}
		for _, h := range target.Headers {
			if line := "\r\n" + h.Name + ": " + h.Value; !strings.Contains(got[0].head, line) {
				k.Errorf("request head\n%s\nlacks %q", got[0].head, strings.TrimPrefix(line, "\r\n"))
			}
		}
		events, ds := httpDeliveries(k)
		if len(ds) != 1 {
			k.Fatalf("delivery events = %+v, want one", ds)
		}
		d := ds[0]
		if d.From != topic() || d.To != (aws.ARN{}) || d.Endpoint != "127.0.0.1:8080" || d.Mechanism != delivery.SNSToHTTP || d.Attempt != 1 || d.Outcome != (journal.Delivered{}) {
			k.Errorf("delivery = %+v, want attempt 1 of sns-to-http from %s to 127.0.0.1:8080, delivered", d, topic())
		}
		if d.HTTP == nil || d.HTTP.Status != http.StatusNoContent || d.HTTP.Class != journal.HTTPSuccess || d.HTTP.Duration < 0 {
			k.Errorf("exchange = %+v, want status 204 classed success", d.HTTP)
		}
		if events[0].At != t0 || events[0].Cause.Parent != cause.Parent {
			k.Errorf("event at %v caused by %d, want at %v caused by %d", events[0].At, events[0].Cause.Parent, t0, cause.Parent)
		}
	})
}

// schedulePolicy has a retry in every phase: one with no delay, one at
// minDelayTarget, two backoff retries (the first waits minDelayTarget, the
// last maxDelayTarget) and one at maxDelayTarget (sns-http-delivery.md 6.3,
// aws-doc), so 1 + 5 attempts.
func schedulePolicy() delivery.SNSHTTP {
	return delivery.SNSHTTP{
		NoDelay: 1, MinDelay: 1, Backoff: 2, MaxDelay: 1,
		MinDelayTarget: 5 * time.Second, MaxDelayTarget: time.Minute, BackoffFunction: delivery.BackoffLinear,
	}
}

// failingSchedule runs one delivery under schedulePolicy against an
// endpoint that answers 503, advancing the clock in the given steps, and
// returns each attempt's instant and, for a Retrying attempt, the instant
// it names, both offset from the start, and the outcomes.
func failingSchedule(t *testing.T, steps []time.Duration) (at, next []time.Duration, outcomes []journal.DeliveryOutcome) {
	t.Helper()
	r := answeringHook(http.StatusServiceUnavailable)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		t0 := k.Clock.Now()
		enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, schedulePolicy()), source(k))
		settled(k)
		for _, step := range steps {
			advance(k, step)
		}
		events, ds := httpDeliveries(k)
		for i := range events {
			at = append(at, events[i].At.Sub(t0))
			outcomes = append(outcomes, ds[i].Outcome)
			if r, ok := ds[i].Outcome.(journal.Retrying); ok {
				next = append(next, r.Next.Sub(t0))
			}
			if ds[i].Attempt != i+1 {
				k.Errorf("event %d is attempt %d, want %d", i, ds[i].Attempt, i+1)
			}
		}
		if n := len(r.requests()); n != len(events) {
			k.Errorf("the endpoint got %d requests for %d journaled attempts", n, len(events))
		}
	})
	return at, next, outcomes
}

// The retry schedule runs on the emulator clock: a retryable answer (503,
// 6.1 aws-doc) arms the next attempt at the attempt's end plus the phase
// delay, which on a frozen clock is the attempt's instant plus the delay
// (6.3; 8.2 item 3). clock advance replays it deterministically, whether the
// clock moves in one step or many (8.2 item 7); once the retries are spent
// with no dead-letter queue the message is discarded (6.5, aws-doc).
func TestSNSHTTPRetryScheduleReplaysUnderClockAdvance(t *testing.T) {
	s := time.Second
	want := []time.Duration{0, 0, 5 * s, 10 * s, 70 * s, 130 * s}
	oneStep, next, outcomes := failingSchedule(t, []time.Duration{time.Hour})
	if !slices.Equal(oneStep, want) {
		t.Fatalf("attempts at %v, want %v", oneStep, want)
	}
	// Each Retrying names the next attempt's instant; the last attempt,
	// with no dead-letter queue, is Dropped.
	if !slices.Equal(next, want[1:]) {
		t.Errorf("Retrying.Next at %v, want %v", next, want[1:])
	}
	if _, ok := outcomes[len(outcomes)-1].(journal.Dropped); !ok {
		t.Errorf("last attempt outcome = %#v, want Dropped: no dead-letter queue", outcomes[len(outcomes)-1])
	}
	again, _, _ := failingSchedule(t, []time.Duration{time.Hour})
	if !slices.Equal(again, oneStep) {
		t.Errorf("a second run attempted at %v, the first at %v", again, oneStep)
	}
	stepped, _, _ := failingSchedule(t, []time.Duration{3 * s, 3 * s, 4 * s, 59 * s, time.Minute, time.Hour})
	if !slices.Equal(stepped, want) {
		t.Errorf("advancing in steps attempted at %v, want %v", stepped, want)
	}
}

// 429, 5xx and a timed-out exchange are retryable (sns-http-delivery.md
// 6.1, aws-doc): the attempt is journaled Retrying with the exchange, and
// the next attempt comes the phase delay later (6.3).
func TestSNSHTTPRetryableAnswers(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, 0} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			r := &hookReceiver{addrs: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8080")}, answer: func(n int) int {
				if n == 0 {
					return status
				}
				return http.StatusOK
			}}
			runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
				t0 := k.Clock.Now()
				enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy()), source(k))
				settled(k)
				advance(k, time.Minute)
				events, ds := httpDeliveries(k)
				if len(ds) != 2 {
					k.Fatalf("delivery events = %+v, want the failed attempt and the retry", ds)
				}
				if want := (journal.Retrying{Next: t0.Add(20 * time.Second)}); ds[0].Outcome != want {
					k.Errorf("attempt 1 outcome = %#v, want %#v", ds[0].Outcome, want)
				}
				if x := ds[0].HTTP; x == nil || x.Status != status || x.Class != journal.HTTPRetryable {
					k.Errorf("attempt 1 exchange = %+v, want status %d classed retryable", x, status)
				}
				if status == 0 && (ds[0].HTTP == nil || ds[0].HTTP.Duration <= 0) {
					k.Errorf("a timed-out exchange took %+v, want the wall time it waited", ds[0].HTTP)
				}
				if events[1].At != t0.Add(20*time.Second) || ds[1].Attempt != 2 || ds[1].Outcome != (journal.Delivered{}) {
					k.Errorf("attempt 2 = %+v at %v, want attempt 2 delivered at +20s", ds[1], events[1].At.Sub(t0))
				}
			})
		})
	}
}

// When the retries are spent, a notification goes to its dead-letter queue
// (sns-http-delivery.md 6.5, aws-doc): one SQS message, sent by SNS
// delivery, and the attempt is journaled DeadLettered.
func TestSNSHTTPExhaustedNotificationGoesToTheDLQ(t *testing.T) {
	r := answeringHook(http.StatusServiceUnavailable)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
		dlq := arn("sqs", "orders-dlq")
		policy := delivery.SNSHTTP{NoDelay: 1, MinDelayTarget: time.Second, MaxDelayTarget: time.Second, BackoffFunction: delivery.BackoffLinear}
		req := toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, policy)
		req.DLQ = &dlq
		enqueue(k, o, req, source(k))
		settled(k)
		advance(k, time.Hour)
		if n := len(r.requests()); n != 2 {
			k.Errorf("the endpoint got %d requests, want 1 + numRetries 1", n)
		}
		got := q.sent()
		if len(got) != 1 || got[0].queue != "orders-dlq" {
			k.Fatalf("SendMessage calls = %+v, want one dead letter to orders-dlq", got)
		}
		if got[0].origin != (ref.Internal{Via: ref.SNSDelivery}) {
			k.Errorf("dead letter sent from %v, want SNS delivery", got[0].origin)
		}
		_, ds := httpDeliveries(k)
		if len(ds) != 2 || ds[1].Outcome != (journal.DeadLettered{DLQ: dlq}) {
			k.Errorf("deliveries = %+v, want the last attempt dead-lettered to %s", ds, dlq)
		}
	})
}

// A delivery understudy chooses not to make, to a host egress refuses or
// with no egress client, is journaled Refused: never retried, never
// dead-lettered, never shaped as an AWS failure (sns-http-delivery.md 7.3,
// 8.8).
func TestSNSHTTPRefusalIsTerminal(t *testing.T) {
	for name, withClient := range map[string]bool{"an unlisted host": true, "no egress client": false} {
		t.Run(name, func(t *testing.T) {
			var r *hookReceiver
			if withClient {
				r = answeringHook(http.StatusOK)
				r.addrs = append(r.addrs, netip.MustParseAddrPort("127.0.0.2:8080"))
			}
			runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
				q := stubQueues(k, func(int, *sqsapi.SendMessageInput) *awserr.Error { return nil })
				dlq := arn("sqs", "orders-dlq")
				req := toHook(k, "http://127.0.0.2:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy())
				req.DLQ = &dlq
				enqueue(k, o, req, source(k))
				settled(k)
				advance(k, time.Hour)
				_, ds := httpDeliveries(k)
				if len(ds) != 1 {
					k.Fatalf("delivery events = %+v, want one", ds)
				}
				if _, ok := ds[0].Outcome.(journal.Refused); !ok || ds[0].Attempt != 1 || ds[0].Endpoint != "127.0.0.2:8080" {
					k.Errorf("delivery = %+v, want attempt 1 to 127.0.0.2:8080 refused", ds[0])
				}
				if x := ds[0].HTTP; x == nil || x.Class != journal.HTTPRefused || x.Status != 0 {
					k.Errorf("exchange = %+v, want classed refused with no status", x)
				}
				if got := q.sent(); len(got) != 0 {
					k.Errorf("SendMessage calls = %+v, want no dead letter", got)
				}
				if r != nil {
					if got := r.requests(); len(got) != 0 {
						k.Errorf("the unlisted host got %d requests", len(got))
					}
				}
				if res := o.Resources(k.Scope); len(res) != 0 {
					k.Errorf("Resources = %v, want the refused delivery gone", res)
				}
			})
		})
	}
}

// Delivery-phase fault rules answer an attempt without the endpoint
// (sns-http-delivery.md 8.2 item 2): http-status as an answer of that
// status, http-timeout as a timed-out exchange; both journal the hit, and
// the exchange has no duration. The retry then reaches the endpoint.
func TestSNSHTTPFaultRulesAnswerWithoutTheEndpoint(t *testing.T) {
	cases := []struct {
		name   string
		action fault.Action
		status int
	}{
		{"http-status", fault.HTTPStatus{Status: http.StatusServiceUnavailable}, http.StatusServiceUnavailable},
		{"http-timeout", fault.HTTPTimeout{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := answeringHook(http.StatusOK)
			runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
				rule, rerr := k.Faults.Add(fault.Rule{
					Match:   fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToHTTP}},
					Phase:   fault.Delivery,
					Action:  c.action,
					Trigger: fault.NextN{N: 1},
				})
				if rerr != nil {
					k.Fatal(rerr)
				}
				t0 := k.Clock.Now()
				enqueue(k, o, toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy()), source(k))
				settled(k)
				if n := len(r.requests()); n != 0 {
					k.Errorf("the endpoint got %d requests during the injected attempt, want none", n)
				}
				advance(k, time.Minute)
				_, ds := httpDeliveries(k)
				if len(ds) != 2 {
					k.Fatalf("delivery events = %+v, want the injected attempt and the retry", ds)
				}
				if want := (journal.Retrying{Next: t0.Add(20 * time.Second)}); ds[0].Outcome != want {
					k.Errorf("injected attempt outcome = %#v, want %#v", ds[0].Outcome, want)
				}
				if x := ds[0].HTTP; x == nil || *x != (journal.HTTPExchange{Status: c.status, Class: journal.HTTPRetryable}) {
					k.Errorf("injected exchange = %+v, want status %d classed retryable with no duration", x, c.status)
				}
				if ds[1].Outcome != (journal.Delivered{}) || len(r.requests()) != 1 {
					k.Errorf("retry = %+v with %d requests, want it delivered through the endpoint", ds[1], len(r.requests()))
				}
				hits := k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events
				if len(hits) != 1 || hits[0].Detail != (journal.FaultInjected{Rule: rule, Hit: 1, Of: 1}) {
					k.Errorf("fault events = %+v, want one hit of %s", hits, rule)
				}
			})
		})
	}
}

// Settle counts an HTTP attempt in flight and returns once it ends; a
// retry armed for a later instant is not in flight (sns-http-delivery.md
// 8.2 item 7).
func TestSNSHTTPSettleCountsTheAttemptInFlight(t *testing.T) {
	r := answeringHook(http.StatusServiceUnavailable)
	runHTTP(t, r, func(k *kerneltest.K, o *outbox.Outbox) {
		// Made inside the bubble, so the receiver's wait on it is durably
		// blocking and synctest.Wait returns (testing/synctest).
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

// A pending HTTP delivery survives a snapshot: after a load into a new
// kernel, Rearm fires its retry at the instant the first kernel armed,
// numbered on from there, with the stored bytes and headers.
func TestSNSHTTPSnapshotKeepsThePendingRetry(t *testing.T) {
	var sections []store.Section
	var t0 clock.Instant
	var first hookRequest
	failing := answeringHook(http.StatusServiceUnavailable)
	runHTTP(t, failing, func(k *kerneltest.K, o *outbox.Outbox) {
		t0 = k.Clock.Now()
		req := toHook(k, "http://127.0.0.1:8080/hook", delivery.HTTPNotification, defaultHTTPPolicy())
		req.DLQ = new(arn("sqs", "orders-dlq"))
		enqueue(k, o, req, source(k))
		settled(k)
		got := failing.requests()
		if len(got) != 1 {
			k.Fatalf("the endpoint got %d requests, want 1", len(got))
		}
		first = got[0]
		var err error
		if sections, err = o.Export(); err != nil {
			k.Fatal(err)
		}
	})
	ok := answeringHook(http.StatusOK)
	runHTTP(t, ok, func(k *kerneltest.K, o *outbox.Outbox) {
		if err := o.Import(sections); err != nil {
			k.Fatal(err)
		}
		o.Rearm(k.Clock.Now())
		settled(k)
		if n := len(ok.requests()); n != 0 {
			k.Fatalf("the endpoint got %d requests before the retry was due", n)
		}
		advance(k, time.Minute)
		got := ok.requests()
		if len(got) != 1 {
			k.Fatalf("the endpoint got %d requests, want the retry", len(got))
		}
		if !bytes.Equal(got[0].body, first.body) || got[0].head != first.head {
			k.Errorf("the retry sent\n%s\n%q\nwant the first attempt's\n%s\n%q", got[0].head, got[0].body, first.head, first.body)
		}
		events, ds := httpDeliveries(k)
		if len(ds) != 1 || ds[0].Attempt != 2 || events[0].At != t0.Add(20*time.Second) || ds[0].Outcome != (journal.Delivered{}) {
			k.Errorf("deliveries = %+v at %v, want attempt 2 delivered 20s after the first", ds, events)
		}
	})
}
