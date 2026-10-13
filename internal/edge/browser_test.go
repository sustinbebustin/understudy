package edge_test

import (
	"context"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A loopback listener answers only Hosts no outside DNS can point at it,
// so a page that re-points its own name at 127.0.0.1 (DNS rebinding)
// reaches neither the admin API nor the AWS APIs.
func TestLocalHostsRefusesAForeignHost(t *testing.T) {
	for _, host := range []string{"attacker.example:4566", "attacker.example", "localhost.attacker.example", "evil-localhost:4566"} {
		f := newFront(t, nil)
		admin := request(t, http.MethodPost, "/_understudy/v1/reset", http.Header{"Content-Type": {"text/plain"}}, `{}`, "")
		admin.Host = host
		if got := f.serve(t, admin); !strings.HasPrefix(got, "403\n") || !strings.Contains(got, "DNS rebinding") {
			t.Errorf("Host %q POST /reset = %s, want 403 naming DNS rebinding", host, got)
		}
		if uri, _ := f.admin.seen(); uri != "" {
			t.Errorf("Host %q: the admin handler saw %q, want the call refused before it", host, uri)
		}
		api := request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.ListQueues", "application/x-amz-json-1.0"), `{}`, "sqs")
		api.Host = host
		if got := f.serve(t, api); !strings.HasPrefix(got, "403\n") {
			t.Errorf("Host %q SQS ListQueues = %s, want 403", host, got)
		}
	}
}

func TestLocalHostsAnswersLocalNames(t *testing.T) {
	for _, host := range []string{"", "localhost", "localhost:4566", "LOCALHOST:4566", "localhost.:4566", "bucket.s3.localhost:4566", "127.0.0.1:4566", "[::1]:4566", "172.17.0.1:4566", "192.168.65.254"} {
		f := newFront(t, nil)
		r := request(t, http.MethodGet, "/_understudy/v1/health", nil, "", "")
		r.Host = host
		if got := f.serve(t, r); !strings.HasPrefix(got, "204\n") {
			t.Errorf("Host %q GET /health = %s, want the admin handler's 204", host, got)
		}
	}
}

func TestLocalHostsAnswersTheS3BaseDomains(t *testing.T) {
	h, err := edge.New(edge.Options{
		Kernel: kernelOf(t), Registry: mustRegistry(t), Admin: &recorder{},
		S3BaseDomains: []string{"localhost.localstack.cloud"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{
		"localhost.localstack.cloud:4566":        http.StatusNoContent,
		"bucket.localhost.localstack.cloud:4566": http.StatusNoContent,
		"localstack.cloud:4566":                  http.StatusForbidden,
	} {
		r := request(t, http.MethodGet, "/_understudy/v1/health", nil, "", "")
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Host %q GET /health = %d, want %d", host, rec.Code, want)
		}
	}
}

// An exposed listener's clients name it as their network does, such as a
// compose service name; a browser naming it so is a page that re-pointed
// its own name at the listener (DNS rebinding), and is refused unless the
// policy is AnyHost.
func TestNetworkHostsAnswerEveryHostOutsideBrowsers(t *testing.T) {
	cases := []struct {
		hosts   edge.HostPolicy
		host    string
		browser bool
		want    int
	}{
		{edge.NetworkHosts{}, "understudy:4566", false, http.StatusNoContent},
		{edge.NetworkHosts{}, "understudy:4566", true, http.StatusForbidden},
		{edge.NetworkHosts{}, "localhost:4566", true, http.StatusNoContent},
		{edge.AnyHost{}, "understudy:4566", false, http.StatusNoContent},
		{edge.AnyHost{}, "understudy:4566", true, http.StatusNoContent},
	}
	for _, c := range cases {
		h, err := edge.New(edge.Options{Kernel: kernelOf(t), Registry: mustRegistry(t), Admin: &recorder{}, Hosts: c.hosts})
		if err != nil {
			t.Fatal(err)
		}
		r := request(t, http.MethodGet, "/_understudy/v1/health", nil, "", "")
		r.Host = c.host
		if c.browser {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%T, Host %s, browser %t: GET /health = %d, want %d", c.hosts, c.host, c.browser, rec.Code, c.want)
		}
	}
}

// A page on another site can send a CreateQueue or a CreateFunction as a
// simple request (a form or text/plain POST, no-cors) that no CORS
// preflight stops; the AWS APIs refuse it by its Origin, or by its
// Sec-Fetch-Site and Referer, and still answer pages on loopback origins,
// where a developer's own app runs, and the SDKs, which send neither.
func TestAWSAPIsRefuseCallsFromPagesOnOtherSites(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   int
	}{
		{"foreign origin", http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
		{"opaque origin", http.Header{"Origin": {"null"}}, http.StatusForbidden},
		{"two origins", http.Header{"Origin": {"http://localhost:3000", "https://evil.example"}}, http.StatusForbidden},
		{"cross-site fetch without a referer", http.Header{"Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"cross-site fetch from a foreign page", http.Header{"Sec-Fetch-Site": {"cross-site"}, "Referer": {"https://evil.example/page"}}, http.StatusForbidden},
		{"cross-site fetch from a localhost page", http.Header{"Sec-Fetch-Site": {"cross-site"}, "Referer": {"http://localhost:3000/app"}}, http.StatusOK},
		{"localhost origin, cross-site fetch", http.Header{"Origin": {"http://localhost:3000"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusOK},
		{"loopback address origin", http.Header{"Origin": {"http://127.0.0.1:5173"}}, http.StatusOK},
		{"same-site fetch from a localhost page", http.Header{"Sec-Fetch-Site": {"same-site"}, "Referer": {"http://localhost:3000/app"}}, http.StatusOK},
		{"same-site fetch without a referer", http.Header{"Sec-Fetch-Site": {"same-site"}}, http.StatusOK},
		{"a navigation the user started", http.Header{"Sec-Fetch-Site": {"none"}}, http.StatusOK},
		// A browser sends no Fetch Metadata to a host it does not deem
		// trustworthy (plain http on a public S3 base domain) and an older
		// one sends none anywhere; the Referer still names the page.
		{"no fetch metadata from a foreign page", http.Header{"Referer": {"https://evil.example/"}}, http.StatusForbidden},
		{"no fetch metadata from an object page", http.Header{"Referer": {"http://localhost:4566/bucket/evil.html"}}, http.StatusForbidden},
		{"no fetch metadata from a localhost page", http.Header{"Referer": {"http://localhost:3000/app"}}, http.StatusOK},
		{"an SDK", nil, http.StatusOK},
		// Pages on understudy's own port are ones it served: an uploaded S3
		// object or a function URL response, whose content is not trusted.
		{"a path-style object page", http.Header{"Origin": {"http://localhost:4566"}}, http.StatusForbidden},
		{"an object page addressed by IP", http.Header{"Origin": {"http://127.0.0.1:4566"}}, http.StatusForbidden},
		{"a virtual-hosted object page", http.Header{"Origin": {"http://bucket.s3.localhost:4566"}}, http.StatusForbidden},
		{"a function URL page", http.Header{"Origin": {"http://abc123.lambda-url.us-east-1.localhost:4566"}}, http.StatusForbidden},
		{"same-origin fetch from an object page", http.Header{"Sec-Fetch-Site": {"same-origin"}, "Referer": {"http://localhost:4566/bucket/evil.html"}}, http.StatusForbidden},
		{"same-origin fetch without a referer", http.Header{"Sec-Fetch-Site": {"same-origin"}}, http.StatusForbidden},
		{"cross-site fetch from a virtual-hosted object page", http.Header{"Sec-Fetch-Site": {"cross-site"}, "Referer": {"http://bucket.s3.localhost:4566/evil.html"}}, http.StatusForbidden},
		{"same-site fetch from a foreign page", http.Header{"Sec-Fetch-Site": {"same-site"}, "Referer": {"https://evil.example/"}}, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, nil)
			header := jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0")
			maps.Copy(header, c.header)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, `{"QueueName":"q"}`, "sqs"))
			if rec.Code != c.want {
				t.Fatalf("CreateQueue = %d %s, want %d", rec.Code, rec.Body, c.want)
			}
			calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
			if reached, want := len(calls) != 0, c.want == http.StatusOK; reached != want {
				t.Errorf("CreateQueue reached the kernel = %t, want %t", reached, want)
			}
		})
	}
}

// A browser sends no Fetch Metadata and no Origin with an <img> GET to
// plain http on a public name, such as the localhost.localstack.cloud base
// domain, so only the Referer tells a site's presigned Query GET from an
// SDK's.
func TestAWSAPIsRefuseQueryGETsFromPagesWithoutFetchMetadata(t *testing.T) {
	for referer, want := range map[string]int{
		"https://evil.example/": http.StatusForbidden,
		"":                      http.StatusOK,
	} {
		f := newFrontWith(t, nil, func(o *edge.Options) {
			o.Codecs = server.Codecs()
			o.S3BaseDomains = []string{"localhost.localstack.cloud"}
		})
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost.localstack.cloud:4566/?Action=CreateQueue&QueueName=q&Version=2012-11-05", nil)
		region, err := aws.KnownRegions().Parse("us-east-1")
		if err != nil {
			t.Fatal(err)
		}
		auth.Presign(r, auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: region, Service: "sqs"}, 5*time.Minute, time.Now())
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Referer %q, presigned CreateQueue GET = %d %s, want %d", referer, rec.Code, rec.Body, want)
		}
		calls := f.journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events
		if reached, want := len(calls) != 0, want == http.StatusOK; reached != want {
			t.Errorf("Referer %q: CreateQueue reached the kernel = %t, want %t", referer, reached, want)
		}
	}
}

// A page on the port the connection reached is one understudy served even
// when the Host names another port, as through a published container
// port, and a page on the Host's port is one too.
func TestAWSAPIsRefusePagesOnTheListenersPort(t *testing.T) {
	for origin, want := range map[string]int{
		"http://localhost:4566": http.StatusForbidden,
		"http://localhost:5000": http.StatusForbidden,
		"http://localhost:3000": http.StatusOK,
	} {
		f := newFront(t, nil)
		header := jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0")
		header.Set("Origin", origin)
		r := request(t, http.MethodPost, "/", header, `{"QueueName":"q"}`, "sqs")
		r.Host = "localhost:5000"
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4566}))
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Host localhost:5000 on local port 4566, CreateQueue from %s = %d %s, want %d", origin, rec.Code, rec.Body, want)
		}
	}
}

// serveOn serves h on an ephemeral loopback port until the test ends and
// returns the port, once the server answers there, so its listener has
// joined h's ServedPorts.
func serveOn(t *testing.T, h *edge.Handler) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := h.Server(nil)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve = %v, want http.ErrServerClosed", err)
		}
	})
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if res := createQueueOn(t, port, ""); res != http.StatusOK {
		t.Fatalf("CreateQueue on %s = %d, want 200", port, res)
	}
	return port
}

// createQueueOn sends a CreateQueue to 127.0.0.1:port, from a page on
// origin when it is not empty, and returns the status.
func createQueueOn(t *testing.T, port, origin string) int {
	t.Helper()
	header := jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0")
	if origin != "" {
		header.Set("Origin", origin)
	}
	r := request(t, http.MethodPost, "/", header, `{"QueueName":"q"}`, "sqs")
	r.URL.Host, r.Host, r.RequestURI = "127.0.0.1:"+port, "127.0.0.1:"+port, ""
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode
}

// With --tls one handler serves the main and the TLS listener, and the
// container listener's handler shares their ServedPorts; a page understudy
// served on any of those ports is refused on every other one, since a
// function URL response or an S3 object there is not trusted.
func TestAWSAPIsRefusePagesOnEveryListenersPort(t *testing.T) {
	var served edge.ServedPorts
	f := newFrontWith(t, nil, func(o *edge.Options) { o.Served = &served })
	main, secure := serveOn(t, f.handler), serveOn(t, f.handler)
	container := serveOn(t, newFrontWith(t, nil, func(o *edge.Options) { o.Served = &served }).handler)
	cases := []struct {
		name, port, origin string
		want               int
	}{
		{"a TLS listener page calling the main listener", main, "https://abc.lambda-url.us-east-1.localhost:" + secure, http.StatusForbidden},
		{"a main listener page calling the TLS listener", secure, "http://localhost:" + main, http.StatusForbidden},
		{"a main listener page calling the container listener", container, "http://localhost:" + main, http.StatusForbidden},
		{"a container listener page calling the main listener", main, "http://127.0.0.1:" + container, http.StatusForbidden},
		{"a developer's app calling the main listener", main, "http://localhost:3000", http.StatusOK},
		{"a developer's app calling the TLS listener", secure, "https://localhost:3000", http.StatusOK},
	}
	for _, c := range cases {
		if got := createQueueOn(t, c.port, c.origin); got != c.want {
			t.Errorf("%s: CreateQueue from %s = %d, want %d", c.name, c.origin, got, c.want)
		}
	}
}

// A text/plain POST, which a page on any site may send, is refused even
// when no rule claims it, so its answer reveals nothing either.
func TestAWSAPIsRefuseUnclaimedCallsFromPagesOnOtherSites(t *testing.T) {
	f := newFront(t, nil)
	header := http.Header{"Content-Type": {"text/plain"}, "Origin": {"https://evil.example"}}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/2015-03-31/functions", header, `{"FunctionName":"f"}`, ""))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not a loopback origin") {
		t.Errorf("text/plain CreateFunction from evil.example = %d %s, want 403 naming the origin", rec.Code, rec.Body)
	}
}

// AnyPage is the opt-in for an app served under a name that is not local.
func TestAnyPageAnswersPagesOnEverySite(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.Pages = edge.AnyPage{} })
	header := jsonHeader("AmazonSQS.CreateQueue", "application/x-amz-json-1.0")
	header.Set("Origin", "https://app.example")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, `{"QueueName":"q"}`, "sqs"))
	if rec.Code != http.StatusOK {
		t.Errorf("AnyPage, CreateQueue from app.example = %d %s, want 200", rec.Code, rec.Body)
	}
}

// lambdaLinks registers Lambda's raw function URL operation, unbound, so a
// function URL reaches the kernel and gets 501.
func lambdaLinks() *registry.Service {
	svc := &registry.Service{ID: "lambda", SigningName: "lambda", IAMPrefix: "lambda", ARNNamespace: "lambda", Protocols: []ref.ProtocolID{ref.RESTJSON1}}
	svc.Operations = []*registry.Operation{{Service: svc, ID: ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"}, Kind: registry.Raw}}
	return svc
}

// A function URL is a public endpoint that pages on any site call, as on
// AWS; the browser defences never refuse one, by host or by path.
func TestFunctionURLsAnswerPagesOnEverySite(t *testing.T) {
	for _, target := range []struct{ host, path string }{
		{"abc123.lambda-url.us-east-1.localhost:4566", "/"},
		{"localhost:4566", "/_understudy/lambda-url/abc123/"},
	} {
		f := newFront(t, nil, lambdaLinks())
		r := request(t, http.MethodPost, target.path, http.Header{"Content-Type": {"text/plain"}, "Origin": {"https://evil.example"}}, "hi", "")
		r.Host = target.host
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s%s from evil.example = %d %s, want the unbound operation's 501", target.host, target.path, rec.Code, rec.Body)
		}
	}
}

// A page on another site can send a simple POST (text/plain, no-cors)
// without a CORS preflight; the admin API refuses it by its Origin or
// Sec-Fetch-Site, and answers clients that send neither.
func TestAdminRefusesCrossSiteCalls(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   int
	}{
		{"foreign origin", http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
		{"opaque origin", http.Header{"Origin": {"null"}}, http.StatusForbidden},
		{"localhost-looking origin", http.Header{"Origin": {"http://localhost.evil.example"}}, http.StatusForbidden},
		{"cross-site fetch", http.Header{"Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"loopback origin, cross-site fetch", http.Header{"Origin": {"http://localhost:3000"}, "Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"no origin", nil, http.StatusNoContent},
		{"localhost origin", http.Header{"Origin": {"http://localhost:3000"}}, http.StatusNoContent},
		{"loopback address origin", http.Header{"Origin": {"http://127.0.0.1:4566"}, "Sec-Fetch-Site": {"same-origin"}}, http.StatusNoContent},
		{"ipv6 loopback origin", http.Header{"Origin": {"http://[::1]:4566"}}, http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, nil)
			header := http.Header{"Content-Type": {"text/plain"}}
			maps.Copy(header, c.header)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/_understudy/v1/reset", header, `{}`, ""))
			if rec.Code != c.want {
				t.Errorf("POST /reset = %d %s, want %d", rec.Code, rec.Body, c.want)
			}
			uri, _ := f.admin.seen()
			if reached, want := uri != "", c.want == http.StatusNoContent; reached != want {
				t.Errorf("admin handler reached = %t, want %t", reached, want)
			}
		})
	}
}
