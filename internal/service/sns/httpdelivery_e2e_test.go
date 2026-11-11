package sns_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// hookServer is an HTTP/1.1 endpoint on an in-memory network behind
// egress's dial seam, listening on 127.0.0.1:8080 and 127.0.0.1:8081.
// answer gives each request's status and extra response headers; the n
// passed is how many requests of the same message type came before it.
type hookServer struct {
	answer func(messageType string, n int) (int, http.Header)

	mu    sync.Mutex
	posts []hookPost
	byTyp map[string]int
}

type hookPost struct {
	addr   netip.AddrPort
	method string
	uri    string
	header http.Header
	body   []byte
}

func (p hookPost) messageType() string { return p.header.Get("X-Amz-Sns-Message-Type") }

func newHookServer(answer func(messageType string, n int) (int, http.Header)) *hookServer {
	return &hookServer{answer: answer, byTyp: map[string]int{}}
}

// answering returns a server that answers every request with status.
func answering(status int) *hookServer {
	return newHookServer(func(string, int) (int, http.Header) { return status, nil })
}

// confirmingThen returns a server that answers confirmations 200 and
// notifications by notify.
func confirmingThen(notify func(n int) (int, http.Header)) *hookServer {
	return newHookServer(func(typ string, n int) (int, http.Header) {
		if typ == "Notification" {
			return notify(n)
		}
		return http.StatusOK, nil
	})
}

var listening = []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:8080"), netip.MustParseAddrPort("127.0.0.1:8081")}

func (s *hookServer) Dial(_ context.Context, addr netip.AddrPort) (net.Conn, error) {
	if !slices.Contains(listening, addr) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Addr: net.TCPAddrFromAddrPort(addr), Err: syscall.ECONNREFUSED}
	}
	client, server := net.Pipe()
	go s.serve(addr, server)
	return client, nil
}

func (s *hookServer) serve(addr netip.AddrPort, conn net.Conn) {
	defer conn.Close()
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return
	}
	p := hookPost{addr: addr, method: req.Method, uri: req.RequestURI, header: req.Header, body: body}
	s.mu.Lock()
	n := s.byTyp[p.messageType()]
	s.byTyp[p.messageType()]++
	s.posts = append(s.posts, p)
	s.mu.Unlock()
	status, extra := s.answer(p.messageType(), n)
	var head bytes.Buffer
	fmt.Fprintf(&head, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	for name, values := range extra {
		for _, v := range values {
			fmt.Fprintf(&head, "%s: %s\r\n", name, v)
		}
	}
	head.WriteString("Content-Length: 0\r\nConnection: close\r\n\r\n")
	if _, err := conn.Write(head.Bytes()); err != nil {
		return
	}
}

func (s *hookServer) all() []hookPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.posts)
}

// ofType returns the posts of one message type, in order.
func (s *hookServer) ofType(typ string) []hookPost {
	var out []hookPost
	for _, p := range s.all() {
		if p.messageType() == typ {
			out = append(out, p)
		}
	}
	return out
}

// onTheWire runs fn against a harness whose outbox reaches s for the
// loopback ports 8080, 8081 and 8090 (nothing listens on 8090).
func onTheWire(t *testing.T, s *hookServer, fn func(h *harness)) {
	t.Helper()
	runWith(t, harnessOptions{dialer: s, allow: []string{"127.0.0.1:8080", "127.0.0.1:8081", "127.0.0.1:8090"}}, fn)
}

// confirmByLink GETs the SubscribeURL of the last SubscriptionConfirmation
// s received, as an endpoint confirms (sns-http-delivery.md 2.3).
func confirmByLink(h *harness, s *hookServer) {
	h.Helper()
	cs := s.ofType("SubscriptionConfirmation")
	if len(cs) == 0 {
		h.Fatal("the endpoint got no SubscriptionConfirmation")
	}
	u, err := url.Parse(parseBody(h, cs[len(cs)-1].body).SubscribeURL)
	if err != nil {
		h.Fatal(err)
	}
	if _, aerr := h.link("ConfirmSubscriptionLink", u.RequestURI()); aerr != nil {
		h.Fatalf("ConfirmSubscriptionLink = %s %q", aerr.ID.QueryCode, aerr.Message)
	}
}

// attemptsSince returns the HTTP Delivery events journaled at or after
// from, with their offsets from it.
func attemptsSince(h *harness, from clock.Instant) ([]time.Duration, []journal.Delivery) {
	h.Helper()
	var at []time.Duration
	var ds []journal.Delivery
	events := h.k.Journal().Query(journal.Filter{Kind: journal.KindDelivery, Limit: 1000}).Events
	for i := range events {
		d, ok := events[i].Detail.(journal.Delivery)
		if !ok || d.Mechanism != delivery.SNSToHTTP || d.Attempt == 0 || events[i].At.Before(from) {
			continue
		}
		at = append(at, events[i].At.Sub(from))
		ds = append(ds, d)
	}
	return at, ds
}

func mustARN(h *harness, s string) aws.ARN {
	h.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

// retryPolicy is a full healthyRetryPolicy document.
func retryPolicy(minDelay, maxDelay, retries, noDelay, minRetries, maxRetries int) string {
	return fmt.Sprintf(`{"healthyRetryPolicy":{"minDelayTarget":%d,"maxDelayTarget":%d,"numRetries":%d,"numNoDelayRetries":%d,"numMinDelayRetries":%d,"numMaxDelayRetries":%d,"backoffFunction":"linear"}}`,
		minDelay, maxDelay, retries, noDelay, minRetries, maxRetries)
}

// The whole handshake on the wire: Subscribe POSTs the confirmation at
// once, on a frozen clock (sns-http-delivery.md 2.2, 8.6 item 2); a GET of
// its SubscribeURL confirms (2.3); a Publish POSTs a signed Notification;
// a GET of its UnsubscribeURL removes the subscription and POSTs an
// UnsubscribeConfirmation (2.4). Each is one POST to the endpoint's path
// (3.1) with the headers of 3.2, and no AWS host appears in any URL,
// header or body (8.8).
func TestHTTPHandshakeOnTheWire(t *testing.T) {
	s := answering(http.StatusOK)
	onTheWire(t, s, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", "http://127.0.0.1:8080/hooks/orders?team=a", nil)
		h.settle()
		cs := s.ofType("SubscriptionConfirmation")
		if len(cs) != 1 {
			h.Fatalf("the endpoint got %d SubscriptionConfirmations without the clock moving, want 1", len(cs))
		}
		if cs[0].method != http.MethodPost || cs[0].uri != "/hooks/orders?team=a" {
			h.Errorf("confirmation request = %s %s, want POST /hooks/orders?team=a", cs[0].method, cs[0].uri)
		}
		confirmByLink(h, s)
		attrs := h.subscriptionAttributes(arn)
		if attrs["PendingConfirmation"] != "false" || attrs["ConfirmationWasAuthenticated"] != "false" {
			h.Errorf("after the SubscribeURL: PendingConfirmation %q, ConfirmationWasAuthenticated %q; want false and false", attrs["PendingConfirmation"], attrs["ConfirmationWasAuthenticated"])
		}

		out := must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "hello"})
		h.settle()
		ns := s.ofType("Notification")
		if len(ns) != 1 {
			h.Fatalf("the endpoint got %d Notifications, want 1", len(ns))
		}
		n := parseBody(h, ns[0].body)
		if n.MessageID != *out.MessageId || n.Message != "hello" || ns[0].header.Get("X-Amz-Sns-Message-Id") != *out.MessageId ||
			ns[0].header.Get("X-Amz-Sns-Subscription-Arn") != arn || ns[0].header.Get("X-Amz-Sns-Topic-Arn") != topic {
			h.Errorf("Notification %s with headers %v", ns[0].body, ns[0].header)
		}
		for name, want := range map[string]string{"Content-Type": "text/plain; charset=UTF-8", "User-Agent": "Amazon Simple Notification Service Agent", "Accept-Encoding": "gzip,deflate"} {
			if got := ns[0].header.Get(name); got != want {
				h.Errorf("Notification header %s = %q, want %q", name, got, want)
			}
		}
		verifySignature(h, n, envelope.StringToSign(envelope.Notification{MessageID: n.MessageID, TopicARN: n.TopicArn, Message: n.Message, Timestamp: timestampOf(h, n)}), base)

		u, err := url.Parse(n.UnsubscribeURL)
		if err != nil {
			h.Fatal(err)
		}
		if _, aerr := h.link("UnsubscribeLink", u.RequestURI()); aerr != nil {
			h.Fatalf("UnsubscribeLink = %s %q", aerr.ID.QueryCode, aerr.Message)
		}
		h.settle()
		us := s.ofType("UnsubscribeConfirmation")
		if len(us) != 1 || us[0].header.Get("X-Amz-Sns-Subscription-Arn") != arn {
			h.Fatalf("UnsubscribeConfirmations = %+v, want one naming %s", us, arn)
		}
		_, err2 := call(h, snsapi.OpGetSubscriptionAttributes, &snsapi.GetSubscriptionAttributesInput{SubscriptionArn: arn})
		wantError(h, err2, "NotFound", "Subscription does not exist")

		for _, p := range s.all() {
			var head strings.Builder
			for _, name := range slices.Sorted(maps.Keys(p.header)) {
				head.WriteString(name + ": " + strings.Join(p.header[name], ",") + "\n")
			}
			noAWSHost(h, "the "+p.messageType()+" POST", head.String(), string(p.body))
			b := parseBody(h, p.body)
			for _, link := range []string{b.SigningCertURL, b.SubscribeURL, b.UnsubscribeURL} {
				if link == "" {
					continue
				}
				if lu, err := url.Parse(link); err != nil || lu.Host != "localhost:4566" || !strings.HasPrefix(lu.Path, "/_understudy/sns/") {
					h.Errorf("the %s links to %q, want a URL under understudy's /_understudy/sns/", p.messageType(), link)
				}
			}
		}
		_, ds := attemptsSince(h, clock.Instant{})
		for _, d := range ds {
			if d.Outcome != (journal.Delivered{}) || d.HTTP == nil || d.HTTP.Status != http.StatusOK || d.HTTP.Class != journal.HTTPSuccess {
				h.Errorf("delivery = %+v, want delivered with a 200 classed success", d)
			}
		}
	})
}

// An endpoint whose host and port are not in the allow-list is refused:
// the journal says so, nothing is sent and the subscription stays pending
// (sns-http-delivery.md 7.2, 7.3).
func TestHTTPDeliveryToAnUnlistedHostIsRefused(t *testing.T) {
	s := answering(http.StatusOK)
	runWith(t, harnessOptions{dialer: s, allow: []string{"127.0.0.1:8081"}}, func(h *harness) {
		topic := h.topic("events", nil)
		arn := h.subscribe(topic, "http", hookURL, nil)
		h.settle()
		h.advance(time.Hour)
		if n := len(s.all()); n != 0 {
			h.Errorf("the unlisted endpoint got %d requests", n)
		}
		_, ds := attemptsSince(h, clock.Instant{})
		if len(ds) != 1 {
			h.Fatalf("HTTP deliveries = %+v, want the one refused attempt", ds)
		}
		if _, ok := ds[0].Outcome.(journal.Refused); !ok || ds[0].HTTP == nil || ds[0].HTTP.Class != journal.HTTPRefused {
			h.Errorf("delivery = %+v, want Refused, classed refused", ds[0])
		}
		if got := h.subscriptionAttributes(arn)["PendingConfirmation"]; got != "true" {
			h.Errorf("PendingConfirmation = %q, want true", got)
		}
	})
}

// subscribeConfirmed subscribes endpoint with attrs, confirms it through
// its SubscribeURL and returns the instant after which its notifications
// are attempted.
func subscribeConfirmed(h *harness, s *hookServer, topic, endpoint string, attrs map[string]string) clock.Instant {
	h.Helper()
	arn := h.subscribe(topic, "http", endpoint, attrs)
	h.settle()
	confirmByLink(h, s)
	if got := h.subscriptionAttributes(arn)["PendingConfirmation"]; got != "false" {
		h.Fatalf("%s is still pending after its SubscribeURL", arn)
	}
	h.advance(time.Hour)
	return h.clock.Now()
}

// 429, 5xx and a failed connection are retried under the delivery policy,
// and once the retries are spent the message goes to the RedrivePolicy
// queue; every other status, 3xx and 4xx included, delivers once with no
// retry and no dead letter (sns-http-delivery.md 6.1, 6.5; SNSH-05).
func TestHTTPStatusClassesFollowTheRecording(t *testing.T) {
	const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
	cases := []struct {
		name     string
		endpoint string
		status   int
		retried  bool
	}{
		{"301", hookURL, http.StatusMovedPermanently, false},
		{"302", hookURL, http.StatusFound, false},
		{"307", hookURL, http.StatusTemporaryRedirect, false},
		{"400", hookURL, http.StatusBadRequest, false},
		{"403", hookURL, http.StatusForbidden, false},
		{"404", hookURL, http.StatusNotFound, false},
		{"410", hookURL, http.StatusGone, false},
		{"429", hookURL, http.StatusTooManyRequests, true},
		{"500", hookURL, http.StatusInternalServerError, true},
		{"503", hookURL, http.StatusServiceUnavailable, true},
		{"a refused connection", "http://127.0.0.1:8090/hook", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := confirmingThen(func(int) (int, http.Header) { return c.status, nil })
			onTheWire(t, s, func(h *harness) {
				topic := h.topic("events", nil)
				arn := h.subscribe(topic, "http", c.endpoint, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`})
				h.settle()
				must(h, snsapi.OpConfirmSubscription, &snsapi.ConfirmSubscriptionInput{TopicArn: topic, Token: h.token(arn)})
				must(h, snsapi.OpSetSubscriptionAttributes, &snsapi.SetSubscriptionAttributesInput{SubscriptionArn: arn, AttributeName: "DeliveryPolicy", AttributeValue: new(retryPolicy(1, 1, 1, 0, 0, 0))})
				h.advance(time.Hour)
				t1 := h.clock.Now()
				must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
				h.settle()
				h.advance(time.Minute)
				h.settle()
				_, ds := attemptsSince(h, t1)
				want := 1
				if c.retried {
					want = 2
				}
				if len(ds) != want {
					h.Fatalf("%d attempts, want %d", len(ds), want)
				}
				last := ds[len(ds)-1]
				var wantOutcome journal.DeliveryOutcome = journal.Delivered{}
				wantClass := journal.HTTPSuccess
				wantSent := 0
				if c.retried {
					wantOutcome, wantClass, wantSent = journal.DeadLettered{DLQ: mustARN(h, dlq)}, journal.HTTPRetryable, 1
				}
				if last.Outcome != wantOutcome {
					h.Errorf("last attempt outcome = %#v, want %#v", last.Outcome, wantOutcome)
				}
				if last.HTTP == nil || last.HTTP.Class != wantClass || last.HTTP.Status != c.status {
					h.Errorf("last exchange = %+v, want status %d classed %v", last.HTTP, c.status, wantClass)
				}
				sent := h.sent.all()
				if len(sent) != wantSent || (wantSent == 1 && !strings.HasSuffix(sent[0].QueueUrl, "/orders-dlq")) {
					h.Errorf("SendMessage calls = %+v, want %d dead letters", sent, wantSent)
				}
			})
		})
	}
}

// Under sns.http.redirects a 3xx is the attempt's answer: its Location is
// never requested (sns-http-delivery.md 3.1, 6.1, 7.4).
func TestHTTPRedirectsAreNotFollowed(t *testing.T) {
	s := confirmingThen(func(int) (int, http.Header) {
		return http.StatusFound, http.Header{"Location": {"http://127.0.0.1:8081/elsewhere"}}
	})
	onTheWire(t, s, func(h *harness) {
		if v := switchValue(h, "sns.http.redirects"); v != "not-followed" {
			noExpectation(h, "sns.http.redirects", v)
		}
		topic := h.topic("events", nil)
		t1 := subscribeConfirmed(h, s, topic, hookURL, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		h.advance(time.Hour)
		for _, p := range s.all() {
			if p.addr.Port() == 8081 {
				h.Errorf("the redirect's Location got a %s request", p.messageType())
			}
		}
		_, ds := attemptsSince(h, t1)
		if len(ds) == 0 || ds[0].HTTP == nil || ds[0].HTTP.Status != http.StatusFound {
			h.Errorf("deliveries = %+v, want the first attempt's answer, 302", ds)
		}
	})
}

// Under sns.http.retry-body a retry resends the first attempt's bytes and
// headers (sns-http-delivery.md 4.6; the MessageId a retry keeps is
// aws-doc).
func TestHTTPRetriesResendTheSameBytes(t *testing.T) {
	s := confirmingThen(func(n int) (int, http.Header) {
		if n == 0 {
			return http.StatusServiceUnavailable, nil
		}
		return http.StatusOK, nil
	})
	onTheWire(t, s, func(h *harness) {
		topic := h.topic("events", nil)
		subscribeConfirmed(h, s, topic, hookURL, nil)
		must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
		h.settle()
		h.advance(time.Hour)
		h.settle()
		ns := s.ofType("Notification")
		if len(ns) != 2 {
			h.Fatalf("the endpoint got %d Notifications, want the failed one and its retry", len(ns))
		}
		if ns[0].header.Get("X-Amz-Sns-Message-Id") != ns[1].header.Get("X-Amz-Sns-Message-Id") {
			h.Errorf("the retry's MessageId %s differs from the first %s", ns[1].header.Get("X-Amz-Sns-Message-Id"), ns[0].header.Get("X-Amz-Sns-Message-Id"))
		}
		switch v := switchValue(h, "sns.http.retry-body"); v {
		case "identical":
			if !bytes.Equal(ns[0].body, ns[1].body) || !maps.EqualFunc(ns[0].header, ns[1].header, slices.Equal) {
				h.Errorf("the retry sent\n%v\n%s\nwant the first attempt's\n%v\n%s", ns[1].header, ns[1].body, ns[0].header, ns[0].body)
			}
		default:
			noExpectation(h, "sns.http.retry-body", v)
		}
	})
}

// The delays between retries follow sns.http.backoff-curves and
// sns.http.retry-jitter (sns-http-delivery.md 6.3).
func TestHTTPBackoffFollowsTheSwitches(t *testing.T) {
	s := time.Second
	run(t, func(h *harness) {
		curves, jitter := switchValue(h, "sns.http.backoff-curves"), switchValue(h, "sns.http.retry-jitter")
		if curves != "linear-for-all" {
			noExpectation(h, "sns.http.backoff-curves", curves)
		}
		if jitter != "none" {
			noExpectation(h, "sns.http.retry-jitter", jitter)
		}
		// linear-for-all: the backoff retries step evenly from
		// minDelayTarget to maxDelayTarget, whatever the function, and a
		// single backoff retry waits minDelayTarget.
		cases := []struct {
			policy delivery.SNSHTTP
			want   []time.Duration
		}{
			{systemHTTPPolicy(), []time.Duration{20 * s, 20 * s, 20 * s}},
			{delivery.SNSHTTP{Backoff: 4, MinDelayTarget: s, MaxDelayTarget: 10 * s, BackoffFunction: delivery.BackoffExponential}, []time.Duration{s, 4 * s, 7 * s, 10 * s}},
			{delivery.SNSHTTP{NoDelay: 1, MinDelay: 1, Backoff: 1, MaxDelay: 1, MinDelayTarget: 5 * s, MaxDelayTarget: 60 * s, BackoffFunction: delivery.BackoffGeometric}, []time.Duration{0, 5 * s, 5 * s, 60 * s}},
			{delivery.SNSHTTP{Backoff: 3, MinDelayTarget: s, MaxDelayTarget: 2 * s, BackoffFunction: delivery.BackoffArithmetic}, []time.Duration{s, 1500 * time.Millisecond, 2 * s}},
		}
		for _, c := range cases {
			var got []time.Duration
			for retry := 1; retry <= c.policy.Retries(); retry++ {
				d, ok := c.policy.Delay(retry)
				if !ok {
					h.Errorf("%+v: Delay(%d) refused", c.policy, retry)
				}
				got = append(got, d)
			}
			if !slices.Equal(got, c.want) {
				h.Errorf("%+v: delays %v, want %v", c.policy, got, c.want)
			}
		}
	})
	// On the wire, four backoff retries from 1 s to 10 s come 1, 4, 7 and
	// 10 s after each failed attempt, the same on every run: no jitter.
	schedule := func() []time.Duration {
		var at []time.Duration
		srv := confirmingThen(func(int) (int, http.Header) { return http.StatusServiceUnavailable, nil })
		onTheWire(t, srv, func(h *harness) {
			topic := h.topic("events", nil)
			t1 := subscribeConfirmed(h, srv, topic, hookURL, map[string]string{"DeliveryPolicy": retryPolicy(1, 10, 4, 0, 0, 0)})
			must(h, snsapi.OpPublish, &snsapi.PublishInput{TopicArn: &topic, Message: "m"})
			h.settle()
			h.advance(time.Hour)
			h.settle()
			at, _ = attemptsSince(h, t1)
		})
		return at
	}
	want := []time.Duration{0, s, 5 * s, 12 * s, 22 * s}
	first := schedule()
	if !slices.Equal(first, want) {
		t.Errorf("attempts at %v, want %v", first, want)
	}
	if again := schedule(); !slices.Equal(again, first) {
		t.Errorf("a second run attempted at %v, the first at %v", again, first)
	}
}

// Under sns.http.dlq-body a dead letter is what SQS would get: the
// envelope, or the raw message with its attributes as SQS message
// attributes (sns-http-delivery.md 6.5, sns.md 7.4).
func TestHTTPDeadLetterBodies(t *testing.T) {
	const dlq = "arn:aws:sqs:us-east-1:000000000000:orders-dlq"
	s := confirmingThen(func(int) (int, http.Header) { return http.StatusServiceUnavailable, nil })
	onTheWire(t, s, func(h *harness) {
		if v := switchValue(h, "sns.http.dlq-body"); v != "as-sqs" {
			noExpectation(h, "sns.http.dlq-body", v)
		}
		topic := h.topic("events", nil)
		attrs := func(raw string) map[string]string {
			return map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlq + `"}`, "DeliveryPolicy": retryPolicy(1, 1, 0, 0, 0, 0), "RawMessageDelivery": raw}
		}
		subscribeConfirmed(h, s, topic, hookURL, attrs("false"))
		subscribeConfirmed(h, s, topic, "http://127.0.0.1:8081/raw", attrs("true"))
		must(h, snsapi.OpPublish, &snsapi.PublishInput{
			TopicArn: &topic, Message: "m-dlq",
			MessageAttributes: map[string]snsapi.MessageAttributeValue{"store": attr("String", "north")},
		})
		h.settle()
		h.advance(time.Hour)
		ns := s.ofType("Notification")
		sent := h.sent.all()
		if len(ns) != 2 || len(sent) != 2 {
			h.Fatalf("%d Notifications and %d dead letters, want 2 of each", len(ns), len(sent))
		}
		for _, n := range ns {
			i := slices.IndexFunc(sent, func(in sqsapi.SendMessageInput) bool { return in.MessageBody == string(n.body) })
			if i < 0 {
				h.Errorf("no dead letter carries the body the endpoint got: %s", n.body)
				continue
			}
			got := sent[i].MessageAttributes
			if n.header.Get("X-Amz-Sns-Rawdelivery") == "true" {
				if a, ok := got["store"]; len(got) != 1 || !ok || a.DataType != "String" || a.StringValue == nil || *a.StringValue != "north" {
					h.Errorf("the raw dead letter's attributes = %v, want store north as a String", got)
				}
			} else if len(got) != 0 {
				h.Errorf("the enveloped dead letter carries SQS attributes %v", got)
			}
		}
	})
}

// Under sns.http.control-dlq a SubscriptionConfirmation the endpoint
// refuses is discarded, never sent to the RedrivePolicy queue
// (sns-http-delivery.md 6.5).
func TestFailedConfirmationIsNotDeadLettered(t *testing.T) {
	s := answering(http.StatusNotFound)
	onTheWire(t, s, func(h *harness) {
		if v := switchValue(h, "sns.http.control-dlq"); v != "never" {
			noExpectation(h, "sns.http.control-dlq", v)
		}
		topic := h.topic("events", nil)
		h.subscribe(topic, "http", hookURL, map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:us-east-1:000000000000:orders-dlq"}`})
		h.settle()
		h.advance(time.Hour)
		if n := len(s.ofType("SubscriptionConfirmation")); n == 0 {
			h.Fatal("the endpoint got no SubscriptionConfirmation")
		}
		if sent := h.sent.all(); len(sent) != 0 {
			h.Errorf("SendMessage calls = %+v, want no dead letter", sent)
		}
	})
}

// Under sns.http.basic-auth the endpoint's credentials go on the first
// request (sns-http-delivery.md 3.1).
func TestBasicAuthorizationOnTheFirstRequest(t *testing.T) {
	s := answering(http.StatusOK)
	onTheWire(t, s, func(h *harness) {
		if v := switchValue(h, "sns.http.basic-auth"); v != "preemptive" {
			noExpectation(h, "sns.http.basic-auth", v)
		}
		topic := h.topic("events", nil)
		h.subscribe(topic, "http", "http://alice:s3cret@127.0.0.1:8080/hook", nil)
		h.settle()
		posts := s.all()
		if len(posts) != 1 {
			h.Fatalf("the endpoint got %d requests, want the confirmation alone", len(posts))
		}
		user, pass, ok := (&http.Request{Header: posts[0].header}).BasicAuth()
		if !ok || user != "alice" || pass != "s3cret" {
			h.Errorf("the first request's Authorization = %q, want Basic alice:s3cret", posts[0].header.Get("Authorization"))
		}
	})
}
