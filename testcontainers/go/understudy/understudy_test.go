package understudy_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/testcontainers/go/understudy"
)

// These tests run a real understudy image through scripts/sealed and pass
// the container's endpoint to every client (testcontainers.md 8.7). The
// module's design is testcontainers.md sections 8 and 9.

// image is the image under test: UNDERSTUDY_IMAGE, else the local build
// `just image` tags (testcontainers.md 8.7).
func image() string {
	if img := os.Getenv("UNDERSTUDY_IMAGE"); img != "" {
		return img
	}
	return "understudy:dev"
}

// run starts understudy with opts and removes it when the test ends. The
// cleanup is registered before the error check, the tc-go convention for a
// container returned with a start error (testcontainers.md 4.1).
func run(t *testing.T, opts ...testcontainers.ContainerCustomizer) *understudy.Container {
	t.Helper()
	c, err := understudy.Run(t.Context(), image(), opts...)
	cleanup(t, c)
	if err != nil {
		t.Fatalf("Run(%s): %v", image(), err)
	}
	return c
}

// cleanup terminates c, when Run returned one, as the test ends.
// *understudy.Container's Endpoint(ctx) shadows tc-go's Endpoint(ctx,
// proto), so it is not a testcontainers.Container itself and its embedded
// container is what tc-go's CleanupContainer takes.
func cleanup(t *testing.T, c *understudy.Container) {
	t.Helper()
	if c != nil {
		testcontainers.CleanupContainer(t, c.Container)
	}
}

func inspect(t *testing.T, c *understudy.Container) *container.InspectResponse {
	t.Helper()
	info, err := c.Inspect(t.Context())
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return info
}

// containerEnv is the container's environment; Docker keeps the last value
// of a repeated name.
func containerEnv(info *container.InspectResponse) map[string]string {
	env := map[string]string{}
	for _, kv := range info.Config.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}

func endpoint(t *testing.T, c *understudy.Container) string {
	t.Helper()
	e, err := c.Endpoint(t.Context())
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	return e
}

// awsConfig signs as the test pair understudytest.Env uses, in us-east-1,
// against the container's endpoint (testcontainers.md 3.8, 8.7).
func awsConfig(t *testing.T, c *understudy.Container) aws.Config {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithBaseEndpoint(endpoint(t, c)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func dockerClient(t *testing.T) *testcontainers.DockerClient {
	t.Helper()
	cli, err := testcontainers.NewDockerClientWithOpts(t.Context())
	if err != nil {
		t.Fatalf("Docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// managedSessionContainers lists understudy's own containers that carry
// this tc-go session's label: understudy labels what it creates
// understudy.dev/managed=true (testcontainers.md 3.6), and tc-go's session
// label is org.testcontainers.sessionId (testcontainers.md 4.5).
func managedSessionContainers(t *testing.T, cli *testcontainers.DockerClient) []container.Summary {
	t.Helper()
	res, err := cli.ContainerList(t.Context(), client.ContainerListOptions{
		All: true,
		Filters: make(client.Filters).
			Add("label", "understudy.dev/managed=true").
			Add("label", "org.testcontainers.sessionId="+testcontainers.SessionID()),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	return res.Items
}

// imageContainers lists this session's containers of the image under test.
func imageContainers(t *testing.T, cli *testcontainers.DockerClient) []container.Summary {
	t.Helper()
	res, err := cli.ContainerList(t.Context(), client.ContainerListOptions{
		All: true,
		Filters: make(client.Filters).
			Add("ancestor", image()).
			Add("label", "org.testcontainers.sessionId="+testcontainers.SessionID()),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	return res.Items
}

// socketGroup is the group owning the local Docker socket the tc-go client
// uses; the module adds it to the container's groups (testcontainers.md
// 8.5).
func socketGroup(t *testing.T) uint32 {
	t.Helper()
	host := dockerClient(t).DaemonHost()
	path, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		t.Fatalf("the Docker daemon is at %s, not a local unix socket; this test needs one to read the socket's group", host)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s has no Stat_t", path)
	}
	return st.Gid
}

func TestDefaultImageIsTheReleaseImageAtThisVersion(t *testing.T) {
	// The release image is ghcr.io/sustinbebustin/understudy, tagged with
	// the version (testcontainers.md 3.2); the module moves in lockstep and
	// is 0.0.0 until the first release (testcontainers.md 8.8).
	if understudy.DefaultImage != "ghcr.io/sustinbebustin/understudy:0.0.0" {
		t.Errorf("DefaultImage = %q", understudy.DefaultImage)
	}
}

// Each module test does a real client round trip (testcontainers.md 8.7).
func TestSQSRoundTrip(t *testing.T) {
	c := run(t)
	ctx := t.Context()
	q := sqs.NewFromConfig(awsConfig(t, c))
	created, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("orders")})
	if err != nil {
		t.Fatalf("CreateQueue: %v", err)
	}
	if _, err := q.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: created.QueueUrl, MessageBody: aws.String("hello from testcontainers")}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	got, err := q.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: created.QueueUrl, WaitTimeSeconds: 1})
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if len(got.Messages) != 1 || aws.ToString(got.Messages[0].Body) != "hello from testcontainers" {
		t.Errorf("ReceiveMessage = %+v, want the one message sent", got.Messages)
	}
}

// Env is understudytest.Env's five variables, so a test can switch between
// in-process and container unchanged (testcontainers.md 3.8, 8.2, 9.7).
func TestEnvMirrorsUnderstudytest(t *testing.T) {
	c := run(t)
	e := endpoint(t, c)
	// The endpoint names 127.0.0.1, never localhost, so SDKs address S3 path
	// style (testcontainers.md 8.2).
	if !regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+$`).MatchString(e) {
		t.Errorf("Endpoint = %q, want http://127.0.0.1:<port>", e)
	}
	mapped, err := c.MappedPort(t.Context(), "4566/tcp")
	if err != nil {
		t.Fatal(err)
	}
	if e != "http://127.0.0.1:"+mapped.Port() {
		t.Errorf("Endpoint = %q, want the mapped port of 4566/tcp, %s", e, mapped.Port())
	}
	env, err := c.Env(t.Context())
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	want := map[string]string{
		"AWS_ENDPOINT_URL": e, "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_REGION": "us-east-1", "AWS_DEFAULT_REGION": "us-east-1",
	}
	if !maps.Equal(env, want) {
		t.Errorf("Env = %v, want %v", env, want)
	}
}

// Port 4566 is published on 127.0.0.1 with an ephemeral port and nothing
// else, since the image's admin API has no token (testcontainers.md 8.4,
// finding 2), and the module creates no network (testcontainers.md 10.2).
func TestPortIsPublishedOnLoopbackOnly(t *testing.T) {
	c := run(t)
	info := inspect(t, c)
	port := network.MustParsePort("4566/tcp")
	want := []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}
	if got := info.HostConfig.PortBindings[port]; !slices.Equal(got, want) || len(info.HostConfig.PortBindings) != 1 {
		t.Errorf("PortBindings = %v, want only 4566/tcp on %v", info.HostConfig.PortBindings, want)
	}
	for p, bindings := range info.NetworkSettings.Ports {
		for _, b := range bindings {
			if b.HostIP != netip.MustParseAddr("127.0.0.1") {
				t.Errorf("Docker published %s on %s, want only 127.0.0.1", p, b.HostIP)
			}
		}
	}
	if nets := slices.Sorted(maps.Keys(info.NetworkSettings.Networks)); !slices.Equal(nets, []string{"bridge"}) {
		t.Errorf("networks = %v, want only Docker's default bridge", nets)
	}
	// The wait reported ready only once health answered 200 with ready true
	// (testcontainers.md 8.3, 9.7), so health is ready now.
	resp := get(t, endpoint(t, c)+"/_understudy/v1/health", "")
	if resp.status != http.StatusOK || !resp.ready {
		t.Errorf("health after Run = %d ready %v, want 200 ready", resp.status, resp.ready)
	}
}

type health struct {
	status int
	ready  bool
}

// get sends GET url with the bearer token when one is given and decodes
// the health body's ready member (testcontainers.md 3.1, 3.3).
func get(t *testing.T, url, token string) health {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	data, err := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("GET %s: reading the body: %v", url, err)
	}
	// A 401 carries the error envelope, which has no ready member.
	var body struct {
		Ready bool `json:"ready"`
	}
	_ = json.Unmarshal(data, &body)
	return health{status: resp.StatusCode, ready: body.Ready}
}

// The child-label setting carries the session's Ryuk filter labels, all
// five tc-go sets (testcontainers.md 8.6, 4.5), and a value the user sets
// through tc-go's env option wins.
func TestChildLabelsAreTheSessionsRyukLabels(t *testing.T) {
	env := containerEnv(inspect(t, run(t)))
	var got map[string]string
	if err := json.Unmarshal([]byte(env["UNDERSTUDY_DOCKER_LABELS"]), &got); err != nil {
		t.Fatalf("UNDERSTUDY_DOCKER_LABELS = %q, want a JSON object: %v", env["UNDERSTUDY_DOCKER_LABELS"], err)
	}
	if want := testcontainers.GenericLabels(); !maps.Equal(got, want) {
		t.Errorf("UNDERSTUDY_DOCKER_LABELS = %v, want tc-go's session labels %v", got, want)
	}
	if got["org.testcontainers.sessionId"] != testcontainers.SessionID() {
		t.Errorf("session label = %q, want this session's %q", got["org.testcontainers.sessionId"], testcontainers.SessionID())
	}

	const mine = `{"com.example/team":"payments"}`
	env = containerEnv(inspect(t, run(t, testcontainers.WithEnv(map[string]string{"UNDERSTUDY_DOCKER_LABELS": mine}))))
	if env["UNDERSTUDY_DOCKER_LABELS"] != mine {
		t.Errorf("UNDERSTUDY_DOCKER_LABELS with the user's value = %q, want %q", env["UNDERSTUDY_DOCKER_LABELS"], mine)
	}
}

// Each option sets the understudy variable testcontainers.md 8.2 names.
func TestOptionsSetTheirVariables(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	cases := []struct {
		name string
		opt  understudy.Option
		key  string
		want string
	}{
		{"frozen at an instant", understudy.WithClock(understudy.Frozen{At: at}), "UNDERSTUDY_CLOCK", "frozen@" + at.Format(time.RFC3339Nano)},
		{"frozen now", understudy.WithClock(understudy.Frozen{}), "UNDERSTUDY_CLOCK", "frozen"},
		{"running", understudy.WithClock(understudy.Running{}), "UNDERSTUDY_CLOCK", "running"},
		{"scaled", understudy.WithClockScale(60), "UNDERSTUDY_CLOCK_SCALE", "60"},
		{"scaled by a fraction", understudy.WithClockScale(2.5), "UNDERSTUDY_CLOCK_SCALE", "2.5"},
		{"seed", understudy.WithSeed(18446744073709551615), "UNDERSTUDY_SEED", "18446744073709551615"},
		{"sigv4", understudy.WithSigV4Verify(), "UNDERSTUDY_SIGV4", "verify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := containerEnv(inspect(t, run(t, tc.opt)))
			if env[tc.key] != tc.want {
				t.Errorf("%s = %q, want %q", tc.key, env[tc.key], tc.want)
			}
		})
	}
}

// The helpers call the admin endpoints of testcontainers.md 3.7: advance
// moves the clock, settle answers once idle, and reset drops service state
// but keeps the clock.
func TestHelpers(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := run(t, understudy.WithClock(understudy.Frozen{At: at}))
	ctx := t.Context()
	a, err := c.Admin(ctx)
	if err != nil {
		t.Fatalf("Admin: %v", err)
	}
	clock, err := a.Clock(ctx)
	if err != nil || clock.Mode != admin.Frozen || !clock.Now.Equal(at) {
		t.Fatalf("clock = %+v, %v; want frozen at %s", clock, err, at)
	}
	q := sqs.NewFromConfig(awsConfig(t, c))
	if _, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("before-reset")}); err != nil {
		t.Fatal(err)
	}
	if err := c.AdvanceClock(ctx, 90*time.Second); err != nil {
		t.Fatalf("AdvanceClock: %v", err)
	}
	if clock, err := a.Clock(ctx); err != nil || !clock.Now.Equal(at.Add(90*time.Second)) {
		t.Errorf("clock after AdvanceClock(90s) = %+v, %v; want %s", clock, err, at.Add(90*time.Second))
	}
	if err := c.Settle(ctx, 5*time.Second); err != nil {
		t.Errorf("Settle on an idle server: %v", err)
	}
	if err := c.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	listed, err := q.ListQueues(ctx, &sqs.ListQueuesInput{})
	if err != nil || len(listed.QueueUrls) != 0 {
		t.Errorf("queues after Reset = %v, %v; want none", listed, err)
	}
	if clock, err := a.Clock(ctx); err != nil || !clock.Now.Equal(at.Add(90*time.Second)) || clock.Mode != admin.Frozen {
		t.Errorf("clock after Reset = %+v, %v; want it kept, frozen at %s", clock, err, at.Add(90*time.Second))
	}
}

// With the token option the container holds a random 32-byte token in hex
// and accepts the open AWS API; health without the token then answers 401,
// and the wait, Admin and the helpers send it (testcontainers.md 8.2, 3.3).
// Publishing beyond loopback is accepted with the token, whichever order
// the options come in (testcontainers.md 8.4, 9.5).
func TestAdminTokenGuardsTheAdminAPI(t *testing.T) {
	everywhere := netip.MustParseAddr("0.0.0.0")
	orders := map[string][]testcontainers.ContainerCustomizer{
		"publish then token": {understudy.WithPublishedOn(everywhere), understudy.WithAdminToken()},
		"token then publish": {understudy.WithAdminToken(), understudy.WithPublishedOn(everywhere)},
	}
	tokens := map[string]bool{}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			c := run(t, opts...)
			ctx := t.Context()
			info := inspect(t, c)
			env := containerEnv(info)
			token := env["UNDERSTUDY_ADMIN_TOKEN"]
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
				t.Errorf("UNDERSTUDY_ADMIN_TOKEN = %q, want 64 lowercase hex characters", token)
			}
			tokens[token] = true
			if env["UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API"] != "true" {
				t.Errorf("UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API = %q, want true", env["UNDERSTUDY_ALLOW_UNAUTHENTICATED_AWS_API"])
			}
			want := []network.PortBinding{{HostIP: everywhere, HostPort: "0"}}
			if got := info.HostConfig.PortBindings[network.MustParsePort("4566/tcp")]; !slices.Equal(got, want) {
				t.Errorf("PortBindings[4566/tcp] = %v, want %v", got, want)
			}
			url := endpoint(t, c) + "/_understudy/v1/health"
			if got := get(t, url, ""); got.status != http.StatusUnauthorized {
				t.Errorf("health without the token = %d, want 401", got.status)
			}
			if got := get(t, url, token); got.status != http.StatusOK || !got.ready {
				t.Errorf("health with the token = %d ready %v, want 200 ready", got.status, got.ready)
			}
			a, err := c.Admin(ctx)
			if err != nil {
				t.Fatalf("Admin: %v", err)
			}
			if h, err := a.Health(ctx); err != nil || !h.Ready {
				t.Errorf("Admin().Health = %+v, %v; want ready", h, err)
			}
			if err := c.Settle(ctx, 5*time.Second); err != nil {
				t.Errorf("Settle: %v", err)
			}
			if err := c.AdvanceClock(ctx, time.Second); err != nil {
				t.Errorf("AdvanceClock: %v", err)
			}
			if err := c.Reset(ctx); err != nil {
				t.Errorf("Reset: %v", err)
			}
		})
	}
	if len(tokens) != len(orders) {
		t.Errorf("tokens = %v, want a fresh random token per container", tokens)
	}
}

// A snapshot is copied in and loaded at start (testcontainers.md 8.2).
func TestSnapshotIsLoadedAtStart(t *testing.T) {
	ctx := t.Context()
	src := run(t)
	q := sqs.NewFromConfig(awsConfig(t, src))
	if _, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("from-snapshot")}); err != nil {
		t.Fatal(err)
	}
	a, err := src.Admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveSnapshot(ctx, f, admin.SaveOptions{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dst := run(t, understudy.WithSnapshot(path))
	if env := containerEnv(inspect(t, dst)); env["UNDERSTUDY_LOAD_SNAPSHOT"] != "/snapshot/archive" {
		t.Errorf("UNDERSTUDY_LOAD_SNAPSHOT = %q, want /snapshot/archive", env["UNDERSTUDY_LOAD_SNAPSHOT"])
	}
	listed, err := sqs.NewFromConfig(awsConfig(t, dst)).ListQueues(ctx, &sqs.ListQueuesInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.QueueUrls) != 1 || !strings.HasSuffix(listed.QueueUrls[0], "/from-snapshot") {
		t.Errorf("queues after loading the snapshot = %v, want the saved queue", listed.QueueUrls)
	}
}

// understudy refuses a frozen clock with a scale itself; the module does not
// re-validate it, and the exit-aware wait surfaces the exit well before the
// 60 s startup timeout (testcontainers.md 9.5, 8.3, 4.2). The container
// comes back with the error so the cleanup removes it (tc-go convention,
// testcontainers.md 4.1).
func TestRunFailsFastWhenUnderstudyExits(t *testing.T) {
	start := time.Now()
	c, err := understudy.Run(t.Context(), image(), understudy.WithClock(understudy.Frozen{}), understudy.WithClockScale(60))
	cleanup(t, c)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Run with a frozen and scaled clock succeeded, want understudy's refusal")
	}
	if strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("Run = %v, want the container's exit", err)
	}
	if elapsed >= 30*time.Second {
		t.Errorf("Run failed after %s, want the exit noticed before the startup timeout", elapsed)
	}
	if c == nil {
		t.Error("Run returned no container with the start error, so nothing removes it")
	}
}

// --- Docker socket ---

const handler = `def echo(event, context):
    return event
`

func functionZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("handler.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, handler); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The trust policy Lambda's execution role needs
// (https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).
const lambdaTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

// invokeEcho creates a python3.13 function echoing its event and invokes it
// once. The runtime image is in the local cache, and understudy pulls only
// a missing image, so nothing is pulled from public.ecr.aws.
func invokeEcho(t *testing.T, c *understudy.Container) {
	t.Helper()
	ctx := t.Context()
	cfg := awsConfig(t, c)
	role, err := iam.NewFromConfig(cfg).CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String("echo-role"), AssumeRolePolicyDocument: aws.String(lambdaTrustPolicy),
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	fn := lambda.NewFromConfig(cfg)
	if _, err := fn.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String("echo"), Role: role.Role.Arn, Runtime: lambdatypes.RuntimePython313,
		Handler: aws.String("handler.echo"), Code: &lambdatypes.FunctionCode{ZipFile: functionZip(t)},
	}); err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}
	if err := lambda.NewFunctionActiveV2Waiter(fn).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String("echo")}, 2*time.Minute); err != nil {
		t.Fatalf("waiting for the function to be Active: %v", err)
	}
	out, err := fn.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String("echo"), Payload: []byte(`{"ok":1}`)})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	// A synchronous invoke answers 200, and a handler error would set
	// FunctionError (https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html).
	var echoed map[string]int
	if out.StatusCode != 200 || out.FunctionError != nil || json.Unmarshal(out.Payload, &echoed) != nil || echoed["ok"] != 1 {
		t.Fatalf("Invoke = status %d, error %v, payload %s; want 200 and the event echoed", out.StatusCode, aws.ToString(out.FunctionError), out.Payload)
	}
}

// With the socket option understudy runs Lambda: the socket is a bind
// mount, never Binds, of the daemon-side path tc-go reports, and the
// socket's group is added (testcontainers.md 8.5, 3.4). The post-start
// check found Docker enabled.
func TestLambdaRunsWithTheDockerSocket(t *testing.T) {
	gid := socketGroup(t)
	c := run(t, understudy.WithDockerSocket())
	ctx := t.Context()
	info := inspect(t, c)
	socket := testcontainers.MustExtractDockerSocket(ctx)
	if !slices.ContainsFunc(info.HostConfig.Mounts, func(m mount.Mount) bool {
		return m.Type == mount.TypeBind && m.Source == socket && m.Target == "/var/run/docker.sock" && !m.ReadOnly
	}) {
		t.Errorf("Mounts = %+v, want a read-write bind of %s at /var/run/docker.sock", info.HostConfig.Mounts, socket)
	}
	for _, b := range info.HostConfig.Binds {
		if strings.Contains(b, "docker.sock") {
			t.Errorf("Binds = %v, want the socket mounted through Mounts only", info.HostConfig.Binds)
		}
	}
	if !slices.Contains(info.HostConfig.GroupAdd, strconv.FormatUint(uint64(gid), 10)) {
		t.Errorf("GroupAdd = %v, want the socket's group %d", info.HostConfig.GroupAdd, gid)
	}
	a, err := c.Admin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := a.Health(ctx); err != nil || h.Docker.Status != admin.Enabled {
		t.Fatalf("health = %+v, %v; want Docker enabled", h, err)
	}
	invokeEcho(t, c)
}

// A socket understudy cannot use fails the start with understudy's own
// status and reason, never silently (testcontainers.md 8.5, 9.5, 9.7). A
// group that does not own the socket leaves the image's non-root user
// without access.
func TestADockerSocketUnderstudyCannotUseFailsTheStart(t *testing.T) {
	wrong := socketGroup(t) + 1
	c, err := understudy.Run(t.Context(), image(), understudy.WithDockerSocketGroup(wrong))
	cleanup(t, c)
	de, ok := errors.AsType[*understudy.DockerUnavailableError](err)
	if !ok {
		t.Fatalf("Run with the socket and group %d = %v, want a *DockerUnavailableError", wrong, err)
	}
	if de.Status == admin.Enabled {
		t.Errorf("DockerUnavailableError.Status = %q, want what understudy reported instead of enabled", de.Status)
	}
	want := fmt.Sprintf("understudy: the Docker socket is mounted but understudy reports Docker %s: %s; Lambda and DynamoDB will not run. Check the socket group (WithDockerSocketGroup) or run without WithDockerSocket().", de.Status, de.Reason)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if c == nil {
		t.Fatal("Run returned no container with the start error, so nothing removes it")
	}
	info := inspect(t, c)
	if !slices.Contains(info.HostConfig.GroupAdd, strconv.FormatUint(uint64(wrong), 10)) || slices.Contains(info.HostConfig.GroupAdd, strconv.FormatUint(uint64(wrong-1), 10)) {
		t.Errorf("GroupAdd = %v, want the given group %d instead of the socket's", info.HostConfig.GroupAdd, wrong)
	}
}

// After Terminate, no container understudy started for this session is
// left: the children carried the session's labels, so understudy's drain or
// Ryuk removes them (testcontainers.md 8.6, 8.7, 9.3; finding 3).
func TestTerminateLeavesNoChildContainer(t *testing.T) {
	cli := dockerClient(t)
	c, err := understudy.Run(t.Context(), image(), understudy.WithDockerSocket())
	cleanup(t, c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		// Remove what a failed run left, only this session's managed
		// containers.
		for _, kid := range managedSessionContainers(t, cli) {
			_, _ = cli.ContainerRemove(context.Background(), kid.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})
	invokeEcho(t, c)
	kids := managedSessionContainers(t, cli)
	if len(kids) == 0 {
		t.Fatal("no understudy child carries this session's label after an invoke: the child labels did not reach understudy's containers")
	}
	for _, kid := range kids {
		for k, v := range testcontainers.GenericLabels() {
			if kid.Labels[k] != v {
				t.Errorf("child %s label %s = %q, want %q: Ryuk reaps only what carries every session label", kid.ID, k, kid.Labels[k], v)
			}
		}
	}
	if err := c.Terminate(t.Context()); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if left := managedSessionContainers(t, cli); len(left) != 0 {
		ids := make([]string, 0, len(left))
		for _, kid := range left {
			ids = append(ids, kid.ID+" ("+string(kid.State)+")")
		}
		t.Errorf("after Terminate, understudy children remain: %v", ids)
	}
}
