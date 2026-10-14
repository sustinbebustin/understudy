package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmw "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/sustinbebustin/understudy/admin"
)

// Wall-time bounds. Emulator time only moves when a test advances the
// frozen clock; these bound real work such as container cold starts.
const (
	settleTimeout = 3 * time.Minute
	waitTimeout   = 3 * time.Minute
	pollEvery     = 250 * time.Millisecond
)

// suite is the connection to the running understudy and the pipeline the
// tests share. TestMain builds it once.
type suite struct {
	account string
	runID   string
	admin   *admin.Client
	sns     *sns.Client
	sqs     *sqs.Client
	s3      *s3.Client
	ddb     *dynamodb.Client
	lambda  *lambda.Client
	secrets *secretsmanager.Client
	kms     *kms.Client
	logs    *cloudwatchlogs.Client
	events  *eventbridge.Client
	iam     *iam.Client
	sts     *sts.Client
	web     *http.Client

	pipe     *pipeline
	pipeErr  error
	artifact artifacts
}

var env *suite

// dynamoDBOptedOut reports whether the caller left DynamoDB out of the run
// (run.sh's CONFORMANCE_DYNAMODB=0). understudy then runs without the
// DynamoDB Local licence, so a DynamoDB call would answer 501.
func dynamoDBOptedOut() bool { return os.Getenv("CONFORMANCE_DYNAMODB") == "0" }

// requireDynamoDB skips a test that touches DynamoDB, directly or through a
// worker taking its lock, when the caller opted DynamoDB out.
func requireDynamoDB(t *testing.T) {
	t.Helper()
	if dynamoDBOptedOut() {
		t.Skip("DynamoDB opted out (CONFORMANCE_DYNAMODB=0)")
	}
}

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }

// runSuite connects, builds the function artifacts and provisions the
// pipeline, then runs the tests. A provisioning failure does not stop the
// run: every test that needs the pipeline fails with it, so the report
// names each affected test.
func runSuite(m *testing.M) int {
	ctx := context.Background()
	s, err := connect(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e-pipeline: %v\n", err)
		return 2
	}
	env = s
	arts, release, err := buildArtifacts(ctx, s.account, s.runID)
	defer release()
	if err != nil {
		s.pipeErr = fmt.Errorf("building the function artifacts: %w", err)
	} else {
		s.artifact = arts
		s.pipe, s.pipeErr = provision(ctx, s, arts)
	}
	return m.Run()
}

// connect reads the endpoint scripts/sealed set, refuses anything that is
// not loopback, waits for health and builds the clients.
func connect(ctx context.Context) (*suite, error) {
	raw := os.Getenv("AWS_ENDPOINT_URL")
	if raw == "" {
		return nil, errors.New("no AWS_ENDPOINT_URL is set; run the suite through run.sh or scripts/sealed --endpoint http://127.0.0.1:<port> go test ./...")
	}
	ep, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("AWS_ENDPOINT_URL %q is not a URL: %w", raw, err)
	}
	if !isLoopback(ep.Hostname()) {
		return nil, fmt.Errorf("AWS_ENDPOINT_URL %q is not a loopback endpoint; this suite only talks to a local understudy", raw)
	}
	ac, err := admin.New(ep.Scheme+"://"+ep.Host, admin.Options{})
	if err != nil {
		return nil, err
	}
	if err := awaitHealth(ctx, ac); err != nil {
		return nil, err
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading the SDK configuration: %w", err)
	}
	s := &suite{
		runID:   runID(),
		admin:   ac,
		sns:     sns.NewFromConfig(cfg),
		sqs:     sqs.NewFromConfig(cfg),
		s3:      s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }),
		ddb:     dynamodb.NewFromConfig(cfg),
		lambda:  lambda.NewFromConfig(cfg),
		secrets: secretsmanager.NewFromConfig(cfg),
		kms:     kms.NewFromConfig(cfg),
		logs:    cloudwatchlogs.NewFromConfig(cfg),
		events:  eventbridge.NewFromConfig(cfg),
		iam:     iam.NewFromConfig(cfg),
		sts:     sts.NewFromConfig(cfg),
		web:     localhostClient(ep.Host),
	}
	who, err := s.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("sts GetCallerIdentity: %w", err)
	}
	s.account = aws.ToString(who.Account)
	return s, nil
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func awaitHealth(ctx context.Context, ac *admin.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		h, err := ac.Health(ctx)
		if err == nil && h.Ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("understudy did not report ready within 30s (last error: %w)", err)
		case <-time.After(pollEvery):
		}
	}
}

// runID labels what this run builds in Docker, so run.sh can remove it.
func runID() string {
	if id := os.Getenv("E2E_RUN_ID"); id != "" {
		return id
	}
	return strings.ToLower(rand.Text()[:10])
}

// localhostClient resolves *.localhost names, such as function URL hosts,
// to the emulator's listener, as a resolver that follows RFC 6761 does.
func localhostClient(listener string) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err == nil && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
				addr = listener
			}
			return d.DialContext(ctx, network, addr)
		},
		IdleConnTimeout: 90 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 2 * time.Minute}
}

// call sends req through the *.localhost-aware client and returns the
// response with its body read and closed.
func call(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := env.web.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the response body: %v", req.Method, req.URL, err)
	}
	return resp, body
}

// get is call for a GET of rawURL.
func get(t *testing.T, rawURL string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	return call(t, req)
}

// pipe returns the shared pipeline, failing the test when it could not be
// provisioned.
func pipe(t *testing.T) *pipeline {
	t.Helper()
	if env.pipeErr != nil {
		t.Fatalf("the shared pipeline is not available: %v", env.pipeErr)
	}
	return env.pipe
}

// settle waits until understudy has no tracked work pending: deliveries,
// queued async invokes, running invocations and due timers.
func settle(t *testing.T) {
	t.Helper()
	if _, err := env.admin.Settle(t.Context(), settleTimeout); err != nil {
		t.Fatalf("understudy did not settle within %s: %v", settleTimeout, err)
	}
}

// emuNow reads the emulator clock.
func emuNow(t *testing.T) time.Time {
	t.Helper()
	c, err := env.admin.Clock(t.Context())
	if err != nil {
		t.Fatalf("reading the emulator clock: %v", err)
	}
	return c.Now
}

// advance settles, so work already started happens at the current
// emulator time (a delivery lands, a batching window opens), then moves
// the frozen clock forward by d and settles again.
func advance(t *testing.T, d time.Duration) {
	t.Helper()
	settle(t)
	if _, err := env.admin.ChangeClock(t.Context(), admin.Advance{By: d}); err != nil {
		t.Fatalf("advancing the clock by %s: %v", d, err)
	}
	settle(t)
}

// eventually polls cond, settling between polls, until it reports true or
// waitTimeout of wall time passes; then it fails with cond's last reason.
func eventually(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		settle(t)
		ok, why := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not true after %s: %s", what, waitTimeout, why)
		}
		time.Sleep(pollEvery)
	}
}

var seq atomic.Uint64

// newID is a message id unique within the run.
func newID(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	return fmt.Sprintf("%s-%s-%d", strings.ToLower(name), env.runID, seq.Add(1))
}

// message is the application payload the pipeline carries.
type message struct {
	ID       string `json:"id"`
	Fail     bool   `json:"fail,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Note     string `json:"note,omitempty"`
}

// published is what Publish returned.
type published struct {
	MessageID string
	RequestID string
	Body      string
}

// publish sends m to the topic with string attributes and Number
// attributes for numeric values. Every worker its queues feed takes a
// DynamoDB lock for each record, so publish needs DynamoDB.
func publish(t *testing.T, m message, attrs map[string]string) published {
	t.Helper()
	requireDynamoDB(t)
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out, err := env.sns.Publish(t.Context(), &sns.PublishInput{
		TopicArn:          aws.String(pipe(t).topicARN),
		Message:           aws.String(string(body)),
		Subject:           aws.String("e2e " + m.ID),
		MessageAttributes: snsAttributes(attrs),
	})
	if err != nil {
		t.Fatalf("sns Publish %s: %v", m.ID, err)
	}
	rid, _ := awsmw.GetRequestIDMetadata(out.ResultMetadata)
	return published{MessageID: aws.ToString(out.MessageId), RequestID: rid, Body: string(body)}
}

// numericAttributes are published with DataType Number.
var numericAttributes = map[string]bool{"amount": true}

func snsAttributes(attrs map[string]string) map[string]snstypes.MessageAttributeValue {
	out := map[string]snstypes.MessageAttributeValue{}
	for k, v := range attrs {
		dt := "String"
		if numericAttributes[k] {
			dt = "Number"
		}
		out[k] = snstypes.MessageAttributeValue{DataType: aws.String(dt), StringValue: aws.String(v)}
	}
	return out
}

// processedKey is where worker fn writes the record of message id.
func processedKey(fn, id string) string { return "processed/" + fn + "/" + id + ".json" }

// processedRecord is the object a worker writes for each record it handled.
type processedRecord struct {
	Function        string            `json:"function"`
	SecretVersionID string            `json:"secretVersionId"`
	Record          sqsRecord         `json:"record"`
	Payload         message           `json:"payload"`
	Lock            map[string]string `json:"lock"`
}

// sqsRecord is one record of the SQS event Lambda hands a function, with
// the member names AWS documents (lambda-events.md 3).
type sqsRecord struct {
	MessageID              string                     `json:"messageId"`
	ReceiptHandle          string                     `json:"receiptHandle"`
	Body                   string                     `json:"body"`
	Attributes             map[string]string          `json:"attributes"`
	MessageAttributes      map[string]json.RawMessage `json:"messageAttributes"`
	MD5OfBody              string                     `json:"md5OfBody"`
	MD5OfMessageAttributes *string                    `json:"md5OfMessageAttributes"`
	EventSource            string                     `json:"eventSource"`
	EventSourceARN         string                     `json:"eventSourceARN"`
	AWSRegion              string                     `json:"awsRegion"`
}

// getObject reads s3://bucket/key; ok is false for NoSuchKey.
func getObject(t *testing.T, key string) (body []byte, ok bool) {
	t.Helper()
	out, err := env.s3.GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(bucketName), Key: aws.String(key)})
	if _, missing := errors.AsType[*s3types.NoSuchKey](err); missing {
		return nil, false
	}
	if err != nil {
		t.Fatalf("s3 GetObject %s: %v", key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("reading s3 object %s: %v", key, err)
	}
	return b, true
}

// waitProcessed waits until fn has written its record of message id and
// returns it.
func waitProcessed(t *testing.T, fn, id string) processedRecord {
	t.Helper()
	var rec processedRecord
	eventually(t, fmt.Sprintf("%s processed message %s", fn, id), func() (bool, string) {
		b, ok := getObject(t, processedKey(fn, id))
		if !ok {
			return false, "s3://" + bucketName + "/" + processedKey(fn, id) + " does not exist"
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatalf("the record %s is not JSON: %v\n%s", processedKey(fn, id), err, b)
		}
		return true, ""
	})
	return rec
}

// assertNotProcessed fails when fn wrote a record of message id.
func assertNotProcessed(t *testing.T, fn, id string) {
	t.Helper()
	if b, ok := getObject(t, processedKey(fn, id)); ok {
		t.Errorf("%s processed message %s, which it must not receive:\n%s", fn, id, b)
	}
}

// awsError is the code, message and HTTP status of an SDK error.
func awsError(err error) (code, msg string, status int) {
	if ae, ok := errors.AsType[smithy.APIError](err); ok {
		code, msg = ae.ErrorCode(), ae.ErrorMessage()
	}
	if re, ok := errors.AsType[*awshttp.ResponseError](err); ok {
		status = re.HTTPStatusCode()
	}
	return code, msg, status
}

// wantError fails unless err is an AWS error with code and status.
func wantError(t *testing.T, op string, err error, code string, status int) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: succeeded, want %s (HTTP %d)", op, code, status)
		return
	}
	gotCode, msg, gotStatus := awsError(err)
	if gotCode != code || gotStatus != status {
		t.Errorf("%s: got %s (HTTP %d) %q, want %s (HTTP %d); error: %v", op, gotCode, gotStatus, msg, code, status, err)
	}
}

// docker runs the docker CLI, with its stderr in the error.
func docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
