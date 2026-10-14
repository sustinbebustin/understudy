package gosdk_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// tlsEndpointEnv names the variable run.sh sets to understudy's TLS
// listener. It is not an AWS_* variable, so scripts/sealed passes it
// through; AWS_CA_BUNDLE, which trusts the listener's certificate, is one
// of the variables sealed passes through by design.
const tlsEndpointEnv = "UNDERSTUDY_TLS_ENDPOINT"

var (
	// awsCfg is the SDK configuration every client starts from: the
	// default chain, which reads AWS_ENDPOINT_URL, the dummy credentials
	// and the region that scripts/sealed sets.
	awsCfg aws.Config
	// endpoint is the plain-HTTP endpoint (AWS_ENDPOINT_URL).
	endpoint *url.URL
	// tlsEndpoint is the HTTPS endpoint, nil when run without one.
	tlsEndpoint *url.URL
)

func TestMain(m *testing.M) {
	code, err := setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "go-sdk conformance: %v\n", err)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// setup loads the SDK configuration and refuses to run against anything
// that is not a loopback endpoint, so the suite can never reach real AWS.
func setup() (int, error) {
	raw := os.Getenv("AWS_ENDPOINT_URL")
	if raw == "" {
		return 2, errors.New("AWS_ENDPOINT_URL is not set; run the suite through conformance/go-sdk/run.sh, or through scripts/sealed --endpoint http://127.0.0.1:<port> go test ./... against a running understudy")
	}
	u, err := loopbackURL(raw)
	if err != nil {
		return 2, fmt.Errorf("AWS_ENDPOINT_URL: %w", err)
	}
	endpoint = u
	if raw := os.Getenv(tlsEndpointEnv); raw != "" {
		if tlsEndpoint, err = loopbackURL(raw); err != nil {
			return 2, fmt.Errorf("%s: %w", tlsEndpointEnv, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return 1, fmt.Errorf("loading the default SDK configuration: %w", err)
	}
	awsCfg = cfg
	return 0, nil
}

// loopbackURL parses raw and accepts it only when its host is a loopback
// address or a localhost name.
func loopbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%q does not parse as a URL: %w", raw, err)
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return u, nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return u, nil
	}
	return nil, fmt.Errorf("%q is not a loopback endpoint; the suite refuses to run against anything that could be real AWS", raw)
}

// localhostURL is u with its host replaced by localhost, the documented
// endpoint form under which the Go SDK addresses S3 virtual-hosted style
// (an IP literal forces path style).
func localhostURL(u *url.URL) string {
	c := *u
	c.Host = net.JoinHostPort("localhost", u.Port())
	return c.String()
}

var (
	accountOnce sync.Once
	accountID   string
	accountErr  error
)

// account is the caller's account id, from STS GetCallerIdentity.
func account(t *testing.T) string {
	t.Helper()
	accountOnce.Do(func() {
		out, err := sts.NewFromConfig(awsCfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{})
		if err != nil {
			accountErr = err
			return
		}
		accountID = aws.ToString(out.Account)
	})
	if accountErr != nil {
		t.Fatalf("resolving the account through sts:GetCallerIdentity: %v", accountErr)
	}
	return accountID
}

// region is the region the clients sign for.
func region() string { return awsCfg.Region }

// name returns prefix plus a random lowercase suffix, unique per call, so
// tests never collide with each other or with an earlier run.
func name(prefix string) string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// ok fails the test when err is not nil.
func ok(t *testing.T, err error, op string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", op, err)
	}
}

// errAs asserts that err matches the SDK's generated typed error T.
func errAs[T error](t *testing.T, err error, op string) {
	t.Helper()
	typedErr[T](t, err, op)
}

// typedErr asserts that err matches the SDK's generated typed error T and
// returns it, for tests that inspect the error's modeled members.
func typedErr[T error](t *testing.T, err error, op string) T {
	t.Helper()
	var target T
	if err == nil {
		t.Fatalf("%s: succeeded, want error %T", op, target)
	}
	if !errors.As(err, &target) {
		t.Fatalf("%s: error is not %T: %v", op, target, err)
	}
	return target
}

// apiErr asserts that err is an API error with the given code. Used for
// codes the service models do not declare, which the SDK surfaces as
// smithy.GenericAPIError.
func apiErr(t *testing.T, err error, op, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: succeeded, want error code %s", op, code)
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("%s: want API error %s, got a non-API error: %v", op, code, err)
	}
	if ae.ErrorCode() != code {
		t.Fatalf("%s: error code = %q, want %q (message %q; full error: %v)", op, ae.ErrorCode(), code, ae.ErrorMessage(), err)
	}
}

// wantMessage asserts the API error's message.
func wantMessage(t *testing.T, err error, op, want string) {
	t.Helper()
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("%s: want an API error with message %q, got %v", op, want, err)
	}
	if ae.ErrorMessage() != want {
		t.Errorf("%s: error message = %q, want %q", op, ae.ErrorMessage(), want)
	}
}

// wantMessageContains asserts that the API error's message contains want.
func wantMessageContains(t *testing.T, err error, op, want string) {
	t.Helper()
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("%s: want an API error whose message contains %q, got %v", op, want, err)
	}
	if !strings.Contains(ae.ErrorMessage(), want) {
		t.Errorf("%s: error message = %q, want it to contain %q", op, ae.ErrorMessage(), want)
	}
}

// wantStatus asserts the HTTP status of the response behind err.
func wantStatus(t *testing.T, err error, op string, want int) {
	t.Helper()
	var re *awshttp.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("%s: want an HTTP response error with status %d, got %v", op, want, err)
	}
	if got := re.HTTPStatusCode(); got != want {
		t.Errorf("%s: HTTP status = %d, want %d", op, got, want)
	}
}

// readAll reads body to the end and closes it, failing the test on
// either error. For the SDK's streaming outputs a read error can be a
// failed response checksum, so it is never ignored.
func readAll(t *testing.T, body io.ReadCloser, what string) []byte {
	t.Helper()
	b, err := io.ReadAll(body)
	ok(t, err, "reading "+what)
	ok(t, body.Close(), "closing "+what)
	return b
}

// eventually polls check every interval until it reports done or timeout
// passes, then fails with the last reason check gave.
func eventually(t *testing.T, timeout, interval time.Duration, what string, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		done, reason := check()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not done after %s: %s", what, timeout, reason)
		}
		time.Sleep(interval)
	}
}

// recordingClient wraps the SDK's default HTTP client and records every
// request it sends, so a test can assert what went on the wire (Host,
// checksum headers) without changing what the SDK sends.
type recordingClient struct {
	next aws.HTTPClient
	mu   sync.Mutex
	reqs []recorded
}

// recorded is what recordingClient keeps of one request.
type recorded struct {
	Method string
	Host   string
	Path   string
	Query  url.Values
	Header http.Header
}

func newRecordingClient() *recordingClient {
	return &recordingClient{next: awsCfg.HTTPClient}
}

func (c *recordingClient) Do(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	// A client request leaves Host empty unless something overrides the
	// URL's host, which is then what goes on the wire.
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	c.reqs = append(c.reqs, recorded{Method: r.Method, Host: host, Path: r.URL.EscapedPath(), Query: r.URL.Query(), Header: r.Header.Clone()})
	c.mu.Unlock()
	return c.next.Do(r)
}

// requests returns every request recorded so far.
func (c *recordingClient) requests() []recorded {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]recorded(nil), c.reqs...)
}

// last returns the most recent request whose method matches.
func (c *recordingClient) last(t *testing.T, method string) recorded {
	t.Helper()
	reqs := c.requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Method == method {
			return reqs[i]
		}
	}
	t.Fatalf("no %s request was recorded", method)
	return recorded{}
}
