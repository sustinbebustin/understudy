package outbox

import (
	"bufio"
	"bytes"
	"context"
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

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/egress"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// calls collects what a stub target received; stubs may run on other
// goroutines, so it locks.
type calls[T any] struct {
	mu    sync.Mutex
	items []T
}

func (r *calls[T]) add(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, v)
}

func (r *calls[T]) all() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.items)
}

type putEventsCall struct {
	in     eventbridgeapi.PutEventsInput
	origin ref.Origin
}

type putObjectCall struct {
	bucket, key string
	body        []byte
	length      *int64
	origin      ref.Origin
}

func busARN() aws.ARN {
	return aws.ARN{Partition: "aws", Service: "events", Region: "us-east-1", Account: "000000000000", Resource: "event-bus/orders"}
}

// asS3 is an attempt's target call as S3 makes its deliveries: the s3
// service principal for the bucket, under the S3 notification mechanism.
func asS3(k *kerneltest.K) kernel.Target {
	bucket := aws.ARN{Partition: "aws", Service: "s3", Resource: "orders"}
	return kernel.Target{Scope: k.Scope, As: kernel.AsService{Name: "s3.amazonaws.com", Source: bucket}, Via: ref.S3Notification}
}

//go:fix inline

// An EventBus attempt is one events:PutEvents through the kernel with one
// request entry per entry, in order, naming the bus by its ARN, which
// EventBusName accepts (https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html),
// and no Time, so the call's own time stamps the event (same page).
func TestPutEventsSendsTheEntriesInOrder(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		var got calls[putEventsCall]
		k.Stub(eventbridgeapi.OpPutEvents, func(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
			got.add(putEventsCall{in: *in, origin: c.Origin()})
			out := &eventbridgeapi.PutEventsOutput{}
			for i := range in.Entries {
				out.Entries = append(out.Entries, eventbridgeapi.PutEventsResultEntry{EventId: new(fmt.Sprintf("event-%d", i))})
			}
			return out, nil
		})
		target := delivery.EventBus{ARN: busARN(), Entries: []delivery.EventEntry{
			{Source: "aws.s3", DetailType: "Object Created", Detail: `{"bucket":{"name":"orders"}}`, Resources: []string{"arn:aws:s3:::orders"}},
			{Source: "aws.s3", DetailType: "Object Deleted", Detail: `{"bucket":{"name":"orders"}}`},
		}}
		parent := k.Call(eventbridgeapi.OpPutEvents.Descriptor())
		if err := putEvents(k.Context(), k.Kernel, parent, asS3(k), target); err != nil {
			k.Fatalf("putEvents = %s: %s", err.ID.Shape, err.Message)
		}
		sent := got.all()
		if len(sent) != 1 {
			k.Fatalf("%d PutEvents calls, want 1", len(sent))
		}
		if o, ok := sent[0].origin.(ref.Internal); !ok || o.Via != ref.S3Notification {
			k.Errorf("origin = %#v, want the attempt's internal call under %v", sent[0].origin, ref.S3Notification)
		}
		entries := sent[0].in.Entries
		if len(entries) != len(target.Entries) {
			k.Fatalf("%d request entries, want %d", len(entries), len(target.Entries))
		}
		for i, e := range entries {
			want := target.Entries[i]
			if val(e.Source) != want.Source || val(e.DetailType) != want.DetailType || val(e.Detail) != want.Detail || !slices.Equal(e.Resources, want.Resources) {
				k.Errorf("entry %d = %s %s %s %v, want %+v", i, val(e.Source), val(e.DetailType), val(e.Detail), e.Resources, want)
			}
			if val(e.EventBusName) != busARN().String() {
				k.Errorf("entry %d EventBusName = %q, want the bus ARN %s", i, val(e.EventBusName), busARN())
			}
			if e.Time != nil {
				k.Errorf("entry %d Time = %v, want none", i, *e.Time)
			}
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

func val(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// PutEvents reports a failed entry in its answer, not as an error
// (FailedEntryCount and each entry's ErrorCode and ErrorMessage,
// https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html),
// so an answer with failed entries is a failed attempt naming the entry's
// failure, never a delivery reported as made.
func TestPutEventsFailedEntriesFailTheAttempt(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		k.Stub(eventbridgeapi.OpPutEvents, func(context.Context, *kernel.Call, *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
			return &eventbridgeapi.PutEventsOutput{FailedEntryCount: 1, Entries: []eventbridgeapi.PutEventsResultEntry{
				{EventId: new("event-0")},
				{ErrorCode: new("InternalFailure"), ErrorMessage: new("the bus could not take the event")},
			}}, nil
		})
		target := delivery.EventBus{ARN: busARN(), Entries: []delivery.EventEntry{
			{Source: "aws.s3", DetailType: "Object Created", Detail: `{}`},
			{Source: "aws.s3", DetailType: "Object Created", Detail: `{}`},
		}}
		err := putEvents(k.Context(), k.Kernel, k.Call(eventbridgeapi.OpPutEvents.Descriptor()), asS3(k), target)
		if err == nil {
			k.Fatal("putEvents with a failed entry = nil, want a failed attempt")
		}
		for _, want := range []string{"InternalFailure", "the bus could not take the event"} {
			if !strings.Contains(err.Message, want) {
				k.Errorf("message %q does not name the failed entry's %q", err.Message, want)
			}
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// The target's own error is the attempt's, for the outbox to classify.
func TestPutEventsReturnsTheTargetsError(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		want := eventbridgeapi.ErrResourceNotFoundException("Event bus orders does not exist.")
		k.Stub(eventbridgeapi.OpPutEvents, func(context.Context, *kernel.Call, *eventbridgeapi.PutEventsInput) (*eventbridgeapi.PutEventsOutput, *awserr.Error) {
			return nil, want
		})
		target := delivery.EventBus{ARN: busARN(), Entries: []delivery.EventEntry{{Source: "aws.s3", DetailType: "Object Created", Detail: `{}`}}}
		err := putEvents(k.Context(), k.Kernel, k.Call(eventbridgeapi.OpPutEvents.Descriptor()), asS3(k), target)
		if err == nil || err.ID.Shape != want.ID.Shape {
			k.Errorf("putEvents = %v, want %s", err, want.ID.Shape)
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// An S3Object attempt is one s3:PutObject through the kernel of the body,
// with its length.
func TestPutObjectStoresTheBody(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		var got calls[putObjectCall]
		k.Stub(s3api.OpPutObject, func(_ context.Context, c *kernel.Call, in *s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
			x := putObjectCall{bucket: in.Bucket, key: in.Key, length: in.ContentLength, origin: c.Origin()}
			if in.Body != nil {
				rc, err := in.Body.Open()
				if err != nil {
					return nil, awserr.Internalf(err)
				}
				x.body, err = io.ReadAll(rc)
				if cerr := rc.Close(); err == nil {
					err = cerr
				}
				if err != nil {
					return nil, awserr.Internalf(err)
				}
			}
			got.add(x)
			return &s3api.PutObjectOutput{ETag: new(`"etag"`)}, nil
		})
		target := delivery.S3Object{Bucket: "audit", Key: "deliveries/2026/10/26/one.json", Body: []byte(`{"ok":true}`)}
		if err := putObject(k.Context(), k.Kernel, k.Call(s3api.OpPutObject.Descriptor()), asS3(k), target); err != nil {
			k.Fatalf("putObject = %s: %s", err.ID.Shape, err.Message)
		}
		sent := got.all()
		if len(sent) != 1 {
			k.Fatalf("%d PutObject calls, want 1", len(sent))
		}
		x := sent[0]
		if x.bucket != target.Bucket || x.key != target.Key || !bytes.Equal(x.body, target.Body) {
			k.Errorf("PutObject %s/%s %q, want %s/%s %q", x.bucket, x.key, x.body, target.Bucket, target.Key, target.Body)
		}
		if x.length == nil || *x.length != int64(len(target.Body)) {
			k.Errorf("ContentLength = %v, want %d", x.length, len(target.Body))
		}
		if o, ok := x.origin.(ref.Internal); !ok || o.Via != ref.S3Notification {
			k.Errorf("origin = %#v, want the attempt's internal call under %v", x.origin, ref.S3Notification)
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

func TestPutObjectReturnsTheTargetsError(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		want := s3api.ErrNoSuchBucket("The specified bucket does not exist")
		k.Stub(s3api.OpPutObject, func(context.Context, *kernel.Call, *s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
			return nil, want
		})
		target := delivery.S3Object{Bucket: "missing", Key: "k", Body: []byte("b")}
		err := putObject(k.Context(), k.Kernel, k.Call(s3api.OpPutObject.Descriptor()), asS3(k), target)
		if err == nil || err.ID.Shape != want.ID.Shape {
			k.Errorf("putObject = %v, want %s", err, want.ID.Shape)
		}
	}, kerneltest.Services(eventbridgeapi.Service(), s3api.Service()))
}

// endpoint is an in-memory HTTP receiver behind egress's dial seam: it
// answers each request with status, or holds it until the client hangs up
// when status is 0.
type endpoint struct {
	status int

	mu   sync.Mutex
	head []string
	body [][]byte
	reqs []*http.Request
}

func (e *endpoint) Dial(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
	if addr != netip.MustParseAddrPort("127.0.0.1:8080") {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go e.serve(server)
	return client, nil
}

func (e *endpoint) serve(conn net.Conn) {
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
	e.mu.Lock()
	e.head, e.body, e.reqs = append(e.head, head), append(e.body, body), append(e.reqs, req)
	e.mu.Unlock()
	if e.status == 0 {
		if _, err := io.Copy(io.Discard, conn); err != nil {
			return
		}
		return
	}
	if _, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", e.status, http.StatusText(e.status)); err != nil {
		return
	}
}

// egressTo builds an egress client that reaches only 127.0.0.1:8080, the
// endpoint, the way a producer would list a test receiver.
func egressTo(t *testing.T, e *endpoint) *egress.Client {
	t.Helper()
	allow, perr := egress.ParseAllowList(egress.Setting{Service: "sns", Feature: "httpDelivery"}, []string{"127.0.0.1:8080"})
	if perr != nil {
		t.Fatalf("ParseAllowList: %v", perr)
	}
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}})
	if err != nil {
		t.Fatal(err)
	}
	c, oerr := egress.New(egress.Options{Allow: allow, Clock: clk, Dialer: e})
	if oerr != nil {
		t.Fatalf("egress.New: %v", oerr)
	}
	return c
}

func hook(t *testing.T, raw string) delivery.HTTPEndpoint {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return delivery.HTTPEndpoint{
		URL:     *u,
		Headers: []delivery.HTTPHeader{{Name: "x-amz-sns-message-type", Value: "Notification"}, {Name: "Content-Type", Value: "text/plain; charset=UTF-8"}},
		Body:    []byte(`{"Type":"Notification","Message":"hello"}`),
	}
}

// An HTTPEndpoint attempt POSTs the body to the URL with the headers under
// the names given (sns-http-delivery.md 3.1, 3.2).
func TestPostHTTPSendsTheDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &endpoint{status: http.StatusOK}
		target := hook(t, "http://127.0.0.1:8080/hooks/orders?attempt=1")
		postHTTP(t.Context(), egressTo(t, e), target)
		synctest.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.reqs) != 1 {
			t.Fatalf("the endpoint got %d requests, want 1", len(e.reqs))
		}
		if r := e.reqs[0]; r.Method != http.MethodPost || r.RequestURI != "/hooks/orders?attempt=1" {
			t.Errorf("request = %s %s, want POST /hooks/orders?attempt=1", r.Method, r.RequestURI)
		}
		if !bytes.Equal(e.body[0], target.Body) {
			t.Errorf("body = %q, want %q", e.body[0], target.Body)
		}
		for _, h := range target.Headers {
			if line := "\r\n" + h.Name + ": " + h.Value; !strings.Contains(e.head[0], line) {
				t.Errorf("request head\n%s\nlacks %q", e.head[0], strings.TrimPrefix(line, "\r\n"))
			}
		}
	})
}

// The answer's class follows the retries page: 2xx delivers, and SNS
// "considers all 5XX errors and 429 (too many requests sent) errors as
// retryable", as is a timeout (sns-http-delivery.md 6.1, aws-doc). The
// classes of 3xx and of other 4xx are UNVERIFIED there and are not pinned
// here.
func TestPostHTTPClassifiesTheAnswer(t *testing.T) {
	cases := []struct {
		status int
		retry  bool
	}{
		{http.StatusOK, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		// 0 holds the request: the attempt times out.
		{0, true},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := &endpoint{status: c.status}
				got := postHTTP(t.Context(), egressTo(t, e), hook(t, "http://127.0.0.1:8080/hook"))
				synctest.Wait()
				switch r := got.(type) {
				case httpAccepted:
					if c.retry || r.status != c.status {
						t.Errorf("result = %#v, want a retry", r)
					}
				case httpRetry:
					if !c.retry || r.reason == "" {
						t.Errorf("result = %#v, want %d accepted, or a retry with a reason", r, c.status)
					}
				case httpPermanent, httpRefused:
					t.Errorf("result = %#v, want %d accepted or retried", r, c.status)
				}
			})
		})
	}
}

// A delivery understudy chooses not to make, because egress refuses the
// host or no client is configured, is refused: never retried or
// dead-lettered, never an AWS-shaped failure (sns-http-delivery.md 7.3).
func TestPostHTTPRefusals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := &endpoint{status: http.StatusOK}
		if got, ok := postHTTP(t.Context(), nil, hook(t, "http://127.0.0.1:8080/hook")).(httpRefused); !ok || got.reason == "" {
			t.Errorf("postHTTP with no client = %#v, want refused with a reason", got)
		}
		got, ok := postHTTP(t.Context(), egressTo(t, e), hook(t, "http://10.9.9.9:8080/hook")).(httpRefused)
		if !ok || !strings.Contains(got.reason, "10.9.9.9") {
			t.Errorf("postHTTP to an unlisted host = %#v, want refused naming 10.9.9.9", got)
		}
		synctest.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.reqs) != 0 {
			t.Errorf("the endpoint got %d requests, want none", len(e.reqs))
		}
	})
}
