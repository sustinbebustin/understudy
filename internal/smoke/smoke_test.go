//go:build smoke

package smoke_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/awschunked"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// TestSmoke starts the binary with the TLS listener and drives raw HTTP
// requests at every service, over HTTP and, for the S3 write path, over
// TLS. Without the docker tag the emulator runs with Docker disabled, so
// it starts no container.
func TestSmoke(t *testing.T) {
	args := []string{"--tls"}
	if !dockerTests {
		args = append(args, "--docker", "disabled")
	}
	w := &workload{invoke: dockerTests, dynamoDB: dockerTests}
	// Registered before serve's cleanup, this runs once the interrupt has
	// stopped the emulator.
	t.Cleanup(func() { assertDrained(t, w.instance) })
	w.emu = serve(t, childEnv(), args...)
	if w.emu.tls == "" {
		t.Fatal("the ready file names no tlsEndpoint although serve ran with --tls")
	}
	w.run(t)
}

// workload is the smoke workload against one emulator; each step keeps
// what later steps need.
type workload struct {
	emu    *emulator
	invoke bool
	// dynamoDB is whether DynamoDB Local can run, which takes Docker.
	dynamoDB bool

	queueURL string
	topicARN string
	keyARN   string
	roleARN  string
	// instance is the emulator's understudy.dev/instance container label,
	// read from the function's container once it is invoked.
	instance string
}

const (
	queueName = "smoke"
	queueARN  = "arn:aws:sqs:" + region + ":" + account + ":" + queueName
	bucket    = "smoke-bucket"
	function  = "smoke"
)

// run runs every step in order as a subtest.
func (w *workload) run(t *testing.T) {
	t.Helper()
	steps := []struct {
		name string
		fn   func(*testing.T)
	}{
		{"sqs", w.sqs},
		{"sns to sqs", w.sns},
		{"kms", w.kms},
		{"s3", w.s3},
		{"dynamodb", w.dynamodb},
		{"dynamodb streams", w.dynamodbStreams},
		{"kinesis", w.kinesis},
		{"iam", w.iam},
		{"sts", w.sts},
		{"lambda", w.lambda},
		{"api gateway http api", w.apigatewayv2},
		{"api gateway rest api", w.apigateway},
		{"logs", w.logs},
		{"secrets manager", w.secrets},
		{"ssm", w.ssm},
		{"eventbridge", w.events},
		{"cloudwatch", w.cloudwatch},
		{"step functions", w.stepFunctions},
		{"ses", w.ses},
		{"sesv2", w.sesv2},
		{"scheduler", w.scheduler},
		{"iam policy", w.policy},
		{"coverage", w.coverage},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.fn) {
			return
		}
	}
}

func decode(t *testing.T, what, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("%s answered %s, which does not decode: %v", what, body, err)
	}
}

// sqs creates the queue over awsJson1_0 and sends to it.
func (w *workload) sqs(t *testing.T) {
	t.Helper()
	var created struct {
		QueueURL string `json:"QueueUrl"`
	}
	decode(t, "CreateQueue", must(t, "CreateQueue", awsJSON(t, w.emu, "1.0", "sqs", "AmazonSQS.CreateQueue", `{"QueueName":"`+queueName+`"}`), http.StatusOK).body, &created)
	if !strings.HasSuffix(created.QueueURL, "/"+account+"/"+queueName) {
		t.Fatalf("QueueUrl = %q", created.QueueURL)
	}
	w.queueURL = created.QueueURL
	var sent struct {
		MessageID        string `json:"MessageId"`
		MD5OfMessageBody string
	}
	decode(t, "SendMessage", must(t, "SendMessage", awsJSON(t, w.emu, "1.0", "sqs", "AmazonSQS.SendMessage", `{"QueueUrl":"`+w.queueURL+`","MessageBody":"hello"}`), http.StatusOK).body, &sent)
	// MD5 of "hello".
	if sent.MD5OfMessageBody != "5d41402abc4b2a76b9719d911017c592" || sent.MessageID == "" {
		t.Fatalf("SendMessage = %+v", sent)
	}
}

// sns publishes over awsQuery to a topic the queue subscribes to, then
// waits for the delivery and checks the journal links it to the publish.
func (w *workload) sns(t *testing.T) {
	t.Helper()
	created := must(t, "CreateTopic", awsQuery(t, w.emu, "sns", "CreateTopic", "2010-03-31", url.Values{"Name": {"smoke"}}), http.StatusOK)
	w.topicARN = element(t, created.body, "TopicArn")
	must(t, "Subscribe", awsQuery(t, w.emu, "sns", "Subscribe", "2010-03-31", url.Values{
		"TopicArn": {w.topicARN}, "Protocol": {"sqs"}, "Endpoint": {queueARN},
	}), http.StatusOK)
	published := must(t, "Publish", awsQuery(t, w.emu, "sns", "Publish", "2010-03-31", url.Values{
		"TopicArn": {w.topicARN}, "Message": {"hello from sns"},
	}), http.StatusOK)
	requestID := element(t, published.body, "RequestId")
	settle := must(t, "settle", send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/v1/settle?timeout=30s"}), http.StatusOK)
	if !strings.Contains(settle.body, `"settled":true`) {
		t.Fatalf("settle = %s", settle.body)
	}

	var trace admin.Trace
	decode(t, "the Publish trace", must(t, "the Publish trace", send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/v1/journal/traces?request_id=" + requestID}), http.StatusOK).body, &trace)
	delivered, sent := false, false
	walk(&trace, func(event *admin.JournalEvent) {
		switch event.Kind {
		case "delivery":
			var d struct {
				From, To, Mechanism string
				Outcome             struct{ Kind string }
			}
			delivered = delivered || json.Unmarshal(event.Detail, &d) == nil &&
				d.From == w.topicARN && d.To == queueARN && d.Mechanism == "sns-to-sqs" && d.Outcome.Kind == "delivered"
		case "api-call":
			var call struct {
				Service, Operation string
				Origin             struct{ Kind string }
				Outcome            struct{ Kind string }
			}
			sent = sent || json.Unmarshal(event.Detail, &call) == nil &&
				call.Service == "sqs" && call.Operation == "SendMessage" && call.Origin.Kind == "internal" && call.Outcome.Kind == "ok"
		}
	})
	if !delivered || !sent {
		t.Fatalf("the Publish trace wants a delivered sns-to-sqs delivery to %s (found: %t) and the internal sqs.SendMessage it made (found: %t): %+v",
			queueARN, delivered, sent, trace)
	}
}

// walk calls visit with every event of the tree, root first.
func walk(tree *admin.Trace, visit func(*admin.JournalEvent)) {
	visit(&tree.Event)
	for i := range tree.Children {
		walk(&tree.Children[i], visit)
	}
}

// kms creates a key over awsJson1_1 and encrypts with it.
func (w *workload) kms(t *testing.T) {
	t.Helper()
	var created struct {
		KeyMetadata struct {
			Arn   string
			KeyID string `json:"KeyId"`
		}
	}
	decode(t, "CreateKey", must(t, "CreateKey", awsJSON(t, w.emu, "1.1", "kms", "TrentService.CreateKey", `{"Description":"smoke"}`), http.StatusOK).body, &created)
	w.keyARN = created.KeyMetadata.Arn
	plaintext := base64.StdEncoding.EncodeToString([]byte("hello"))
	var encrypted struct {
		CiphertextBlob string
		KeyID          string `json:"KeyId"`
	}
	decode(t, "Encrypt", must(t, "Encrypt", awsJSON(t, w.emu, "1.1", "kms", "TrentService.Encrypt", `{"KeyId":"`+created.KeyMetadata.KeyID+`","Plaintext":"`+plaintext+`"}`), http.StatusOK).body, &encrypted)
	if encrypted.CiphertextBlob == "" || encrypted.CiphertextBlob == plaintext || encrypted.KeyID != w.keyARN {
		t.Fatalf("Encrypt = %+v", encrypted)
	}
}

// crc32Header is the x-amz-checksum-crc32 value of data.
func crc32Header(data []byte) string {
	return base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(data)))
}

// s3 creates a bucket over restXml, puts objects over HTTP with a checksum
// header, over TLS with an aws-chunked trailer body and with SSE-KMS, and
// reads them back.
func (w *workload) s3(t *testing.T) {
	t.Helper()
	must(t, "CreateBucket", send(t, w.emu, request{method: http.MethodPut, path: "/" + bucket, service: "s3"}), http.StatusOK)

	plain := []byte("plain object body")
	put := must(t, "PutObject", send(t, w.emu, request{
		method: http.MethodPut, path: "/" + bucket + "/plain.txt", service: "s3",
		header: map[string]string{checksumHeader: crc32Header(plain), "Content-Type": "text/plain"},
		body:   plain,
	}), http.StatusOK)
	if put.header.Get("ETag") == "" || put.header.Get(checksumHeader) != crc32Header(plain) {
		t.Fatalf("PutObject headers = %v", put.header)
	}
	w.getChecksummed(t, "plain.txt", plain)

	if w.emu.tls != "" {
		w.putChunkedOverTLS(t)
	}

	secret := []byte("encrypted at rest")
	sse := must(t, "PutObject with SSE-KMS", send(t, w.emu, request{
		method: http.MethodPut, path: "/" + bucket + "/secret.txt", service: "s3",
		header: map[string]string{sseHeader: "aws:kms", "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id": w.keyARN},
		body:   secret,
	}), http.StatusOK)
	if sse.header.Get(sseHeader) != "aws:kms" {
		t.Fatalf("PutObject with SSE-KMS headers = %v", sse.header)
	}
	w.getObject(t, "secret.txt", secret)

	versioning := must(t, "GetBucketVersioning", send(t, w.emu, request{method: http.MethodGet, path: "/" + bucket + "?versioning", service: "s3"}), http.StatusOK)
	if !strings.Contains(versioning.body, "VersioningConfiguration") {
		t.Fatalf("GetBucketVersioning = %s", versioning.body)
	}
}

// S3 headers the smoke test sends and checks.
const (
	checksumHeader = "X-Amz-Checksum-Crc32"
	sseHeader      = "X-Amz-Server-Side-Encryption"
)

// putChunkedOverTLS puts an object over the TLS listener with a
// STREAMING-UNSIGNED-PAYLOAD-TRAILER body whose trailer carries its
// checksum, the body aws-sdk-go-v2 sends over HTTPS, and reads it back.
func (w *workload) putChunkedOverTLS(t *testing.T) {
	t.Helper()
	chunked := bytes.Repeat([]byte("chunked over tls "), 1024)
	var framed bytes.Buffer
	cw := awschunked.NewWriter(&framed, awschunked.UnsignedTrailer, awschunked.WriterOptions{TrailerEnding: awschunked.CRLF})
	for _, part := range [][]byte{chunked[:8192], chunked[8192:]} {
		if _, err := cw.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := cw.Finish(awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: crc32Header(chunked)}); err != nil {
		t.Fatal(err)
	}
	put := must(t, "PutObject over TLS", send(t, w.emu, request{
		method: http.MethodPut, base: w.emu.tls, path: "/" + bucket + "/chunked.txt", service: "s3",
		header: map[string]string{
			"Content-Encoding":             "aws-chunked",
			"X-Amz-Content-Sha256":         "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
			"X-Amz-Decoded-Content-Length": strconv.Itoa(len(chunked)),
			"X-Amz-Trailer":                "x-amz-checksum-crc32",
		},
		body: framed.Bytes(),
	}), http.StatusOK)
	if put.header.Get(checksumHeader) != crc32Header(chunked) {
		t.Fatalf("PutObject over TLS headers = %v, want the trailer's checksum", put.header)
	}
	w.getChecksummed(t, "chunked.txt", chunked)
}

// getObject reads key over HTTP with checksum mode enabled and checks its
// body, and any CRC32 checksum it carries.
func (w *workload) getObject(t *testing.T, key string, want []byte) response {
	t.Helper()
	got := must(t, "GetObject "+key, send(t, w.emu, request{
		method: http.MethodGet, path: "/" + bucket + "/" + key, service: "s3",
		header: map[string]string{"X-Amz-Checksum-Mode": "ENABLED"},
	}), http.StatusOK)
	if got.body != string(want) {
		t.Fatalf("GetObject %s = %d bytes, want the %d put", key, len(got.body), len(want))
	}
	if sum := got.header.Get(checksumHeader); sum != "" && sum != crc32Header(want) {
		t.Fatalf("GetObject %s checksum = %s, want %s", key, sum, crc32Header(want))
	}
	return got
}

// getChecksummed reads key, put with a CRC32 checksum, which GetObject
// with checksum mode enabled must return.
func (w *workload) getChecksummed(t *testing.T, key string, want []byte) {
	t.Helper()
	if sum := w.getObject(t, key, want).header.Get(checksumHeader); sum != crc32Header(want) {
		t.Fatalf("GetObject %s checksum = %q, want the %s it was put with", key, sum, crc32Header(want))
	}
}

// dynamodb lists tables: the result when DynamoDB Local can run, else the
// 501 saying it needs Docker.
func (w *workload) dynamodb(t *testing.T) {
	t.Helper()
	resp := awsJSON(t, w.emu, "1.0", "dynamodb", "DynamoDB_20120810.ListTables", `{}`)
	if w.dynamoDB {
		must(t, "ListTables", resp, http.StatusOK)
		if !strings.Contains(resp.body, "TableNames") {
			t.Fatalf("ListTables = %s", resp.body)
		}
		return
	}
	must(t, "ListTables", resp, http.StatusNotImplemented)
	if !strings.Contains(resp.body, "which needs Docker") {
		t.Fatalf("ListTables with Docker disabled = %s, want the 501 saying DynamoDB Local needs Docker", resp.body)
	}
}

// dynamodbStreams lists streams under the dynamodb signing name (ADR 0019):
// none, since no table has a stream, when DynamoDB Local can run, else the
// same 501 as DynamoDB, which serves the stream log.
func (w *workload) dynamodbStreams(t *testing.T) {
	t.Helper()
	resp := awsJSON(t, w.emu, "1.0", "dynamodb", "DynamoDBStreams_20120810.ListStreams", `{}`)
	if w.dynamoDB {
		must(t, "ListStreams", resp, http.StatusOK)
		if resp.body != `{"Streams":[]}` {
			t.Fatalf(`ListStreams = %s, want {"Streams":[]}`, resp.body)
		}
		return
	}
	must(t, "ListStreams", resp, http.StatusNotImplemented)
	if !strings.Contains(resp.body, "which needs Docker") {
		t.Fatalf("ListStreams with Docker disabled = %s, want the 501 saying DynamoDB Local needs Docker", resp.body)
	}
}

// kinesis creates a one-shard stream over awsJson1_1, puts a record and
// reads it back from TRIM_HORIZON (kinesis.md 5.1, 5.4, 5.5).
func (w *workload) kinesis(t *testing.T) {
	t.Helper()
	must(t, "CreateStream", awsJSON(t, w.emu, "1.1", "kinesis", "Kinesis_20131202.CreateStream", `{"StreamName":"smoke","ShardCount":1}`), http.StatusOK)
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	var put struct {
		ShardID        string `json:"ShardId"`
		SequenceNumber string
	}
	decode(t, "PutRecord", must(t, "PutRecord", awsJSON(t, w.emu, "1.1", "kinesis", "Kinesis_20131202.PutRecord",
		`{"StreamName":"smoke","PartitionKey":"k","Data":"`+data+`"}`), http.StatusOK).body, &put)
	if put.ShardID != "shardId-000000000000" || put.SequenceNumber == "" {
		t.Fatalf("PutRecord = %+v", put)
	}
	var it struct{ ShardIterator string }
	decode(t, "GetShardIterator", must(t, "GetShardIterator", awsJSON(t, w.emu, "1.1", "kinesis", "Kinesis_20131202.GetShardIterator",
		`{"StreamName":"smoke","ShardId":"shardId-000000000000","ShardIteratorType":"TRIM_HORIZON"}`), http.StatusOK).body, &it)
	var got struct {
		Records []struct {
			Data, PartitionKey, SequenceNumber string
		}
		NextShardIterator string
	}
	decode(t, "GetRecords", must(t, "GetRecords", awsJSON(t, w.emu, "1.1", "kinesis", "Kinesis_20131202.GetRecords",
		`{"ShardIterator":"`+it.ShardIterator+`"}`), http.StatusOK).body, &got)
	if len(got.Records) != 1 || got.Records[0].Data != data || got.Records[0].PartitionKey != "k" ||
		got.Records[0].SequenceNumber != put.SequenceNumber || got.NextShardIterator == "" {
		t.Fatalf("GetRecords = %+v, want the one record put", got)
	}
}

// iam creates the function's role over awsQuery.
func (w *workload) iam(t *testing.T) {
	t.Helper()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	created := must(t, "CreateRole", awsQuery(t, w.emu, "iam", "CreateRole", "2010-05-08", url.Values{
		"RoleName": {"smoke"}, "AssumeRolePolicyDocument": {trust},
	}), http.StatusOK)
	w.roleARN = element(t, created.body, "Arn")
	if w.roleARN != "arn:aws:iam::"+account+":role/smoke" {
		t.Fatalf("CreateRole Arn = %q", w.roleARN)
	}
}

// sts reads the caller over awsQuery.
func (w *workload) sts(t *testing.T) {
	t.Helper()
	who := must(t, "GetCallerIdentity", awsQuery(t, w.emu, "sts", "GetCallerIdentity", "2011-06-15", nil), http.StatusOK)
	if element(t, who.body, "Account") != account || element(t, who.body, "Arn") != principal {
		t.Fatalf("GetCallerIdentity = %s", who.body)
	}
}

// bootstrap is a provided.al2023 runtime that answers each event with
// {"echo": event, "container": hostname}; Docker names a container's host
// after its id.
const bootstrap = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
HOST=$(cat /etc/hostname)
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  curl -sS -X POST "$API/invocation/$ID/response" -d "{\"echo\":$EVENT,\"container\":\"$HOST\"}" >/dev/null
done
`

// functionZip is the function's deployment package.
func functionZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	h.SetMode(0o755)
	f, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(bootstrap)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// lambda creates a zip function over restJson1 and reads it back; with
// Docker it invokes it.
func (w *workload) lambda(t *testing.T) {
	t.Helper()
	create, err := json.Marshal(map[string]any{
		"FunctionName": function,
		"Runtime":      "provided.al2023",
		"Role":         w.roleARN,
		"Handler":      "bootstrap",
		"Code":         map[string]any{"ZipFile": functionZip(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	must(t, "CreateFunction", send(t, w.emu, request{
		path: "/2015-03-31/functions", service: "lambda",
		header: map[string]string{"Content-Type": "application/json"}, body: create,
	}), http.StatusCreated)
	var got struct {
		Configuration struct{ FunctionName, State string }
	}
	decode(t, "GetFunction", must(t, "GetFunction", send(t, w.emu, request{method: http.MethodGet, path: "/2015-03-31/functions/" + function, service: "lambda"}), http.StatusOK).body, &got)
	if got.Configuration.FunctionName != function || got.Configuration.State != "Active" {
		t.Fatalf("GetFunction = %+v", got)
	}
	if !w.invoke {
		return
	}
	invoked := must(t, "Invoke", send(t, w.emu, request{
		path: "/2015-03-31/functions/" + function + "/invocations", service: "lambda",
		body: []byte(`{"hello":"smoke"}`),
	}), http.StatusOK)
	if invoked.header.Get("X-Amz-Function-Error") != "" || !strings.Contains(invoked.body, `"echo":{"hello":"smoke"}`) {
		t.Fatalf("Invoke = %v %s, want the event echoed", invoked.header, invoked.body)
	}
	var ran struct {
		Container string `json:"container"`
	}
	decode(t, "Invoke", invoked.body, &ran)
	w.instance = containerInstance(t, ran.Container)
}

// apigatewayv2 builds an HTTP API over restJson1, signed for apigateway: a
// Lambda proxy integration of payload format 2.0 to the function, a
// GET /hello route and a $default stage that deploys automatically; it
// lets API Gateway invoke the function, then sends GET /hello through the
// path fallback. With Docker the function answers the 2.0 event it got;
// without, the answer is 501 naming Docker.
func (w *workload) apigatewayv2(t *testing.T) {
	t.Helper()
	call := func(what, path, body string, v any) {
		t.Helper()
		got := must(t, what, send(t, w.emu, request{
			path: path, service: "apigateway", header: map[string]string{"Content-Type": "application/json"}, body: []byte(body),
		}), http.StatusCreated)
		decode(t, what, got.body, v)
	}
	var api struct {
		APIID string `json:"apiId"`
	}
	call("CreateApi", "/v2/apis", `{"name":"smoke","protocolType":"HTTP"}`, &api)
	var integration struct {
		IntegrationID string `json:"integrationId"`
	}
	call("CreateIntegration", "/v2/apis/"+api.APIID+"/integrations",
		`{"integrationType":"AWS_PROXY","integrationUri":"arn:aws:lambda:`+region+`:`+account+`:function:`+function+`","payloadFormatVersion":"2.0"}`, &integration)
	var created struct{ RouteKey string }
	call("CreateRoute", "/v2/apis/"+api.APIID+"/routes", `{"routeKey":"GET /hello","target":"integrations/`+integration.IntegrationID+`"}`, &created)
	var stage struct{ DeploymentID string }
	call("CreateStage", "/v2/apis/"+api.APIID+"/stages", `{"stageName":"$default","autoDeploy":true}`, &stage)
	if api.APIID == "" || created.RouteKey != "GET /hello" || stage.DeploymentID == "" {
		t.Fatalf("CreateApi id %q, CreateRoute key %q, CreateStage deployment %q; want an id, GET /hello and the automatic deployment", api.APIID, created.RouteKey, stage.DeploymentID)
	}
	permission := `{"StatementId":"apigateway","Action":"lambda:InvokeFunction","Principal":"apigateway.amazonaws.com",` +
		`"SourceArn":"arn:aws:execute-api:` + region + `:` + account + `:` + api.APIID + `/*/*"}`
	must(t, "AddPermission", send(t, w.emu, request{
		path: "/2015-03-31/functions/" + function + "/policy", service: "lambda",
		header: map[string]string{"Content-Type": "application/json"}, body: []byte(permission),
	}), http.StatusCreated)
	invoked := send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/execute-api/" + api.APIID + "/hello"})
	if !w.invoke {
		if invoked.status != http.StatusNotImplemented || !strings.Contains(invoked.body, "Docker") {
			t.Fatalf("GET /hello without Docker = %d %s, want 501 naming Docker", invoked.status, invoked.body)
		}
		return
	}
	if invoked.status != http.StatusOK || !strings.Contains(invoked.body, `"routeKey":"GET /hello"`) || invoked.header.Get("Apigw-Requestid") == "" {
		t.Fatalf("GET /hello = %d %v %s, want the function's echo of the GET /hello event with an apigw-requestid", invoked.status, invoked.header, invoked.body)
	}
}

// apigateway builds a REST API over restJson1, signed for apigateway: GET
// on the root resource by a Lambda proxy integration to the function and a
// deployment to stage s; it lets API Gateway invoke the function, then
// sends GET /s/ through the path fallback. With Docker the function runs
// and answers its echo, which is no proxy response, so API Gateway answers
// the recorded 502 (apigateway-rest.md 5.2), which it gives only after
// invoking; without Docker the answer is 501 naming Docker.
func (w *workload) apigateway(t *testing.T) {
	t.Helper()
	call := func(what, method, path, body string, v any) {
		t.Helper()
		got := must(t, what, send(t, w.emu, request{
			method: method, path: path, service: "apigateway", header: map[string]string{"Content-Type": "application/json"}, body: []byte(body),
		}), http.StatusCreated)
		decode(t, what, got.body, v)
	}
	var api struct {
		ID             string `json:"id"`
		RootResourceID string `json:"rootResourceId"`
	}
	call("CreateRestApi", http.MethodPost, "/restapis", `{"name":"smoke"}`, &api)
	if api.ID == "" || api.RootResourceID == "" {
		t.Fatalf("CreateRestApi answered id %q, root resource %q; want both", api.ID, api.RootResourceID)
	}
	method := "/restapis/" + api.ID + "/resources/" + api.RootResourceID + "/methods/GET"
	var ignored struct{}
	call("PutMethod", http.MethodPut, method, `{"authorizationType":"NONE"}`, &ignored)
	call("PutIntegration", http.MethodPut, method+"/integration", `{"type":"AWS_PROXY","httpMethod":"POST",`+
		`"uri":"arn:aws:apigateway:`+region+`:lambda:path/2015-03-31/functions/arn:aws:lambda:`+region+`:`+account+`:function:`+function+`/invocations"}`, &ignored)
	var deployment struct{ ID string }
	call("CreateDeployment", http.MethodPost, "/restapis/"+api.ID+"/deployments", `{"stageName":"s"}`, &deployment)
	permission := `{"StatementId":"apigateway-rest","Action":"lambda:InvokeFunction","Principal":"apigateway.amazonaws.com",` +
		`"SourceArn":"arn:aws:execute-api:` + region + `:` + account + `:` + api.ID + `/*/*"}`
	must(t, "AddPermission", send(t, w.emu, request{
		path: "/2015-03-31/functions/" + function + "/policy", service: "lambda",
		header: map[string]string{"Content-Type": "application/json"}, body: []byte(permission),
	}), http.StatusCreated)
	invoked := send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/execute-api/" + api.ID + "/s/"})
	if !w.invoke {
		if invoked.status != http.StatusNotImplemented || !strings.Contains(invoked.body, "Docker") {
			t.Fatalf("GET /s/ without Docker = %d %s, want 501 naming Docker", invoked.status, invoked.body)
		}
		return
	}
	if invoked.status != http.StatusBadGateway || invoked.body != `{"message": "Internal server error"}` || invoked.header.Get("X-Amz-Apigw-Id") == "" {
		t.Fatalf("GET /s/ = %d %v %s, want the 502 a non-proxy output gets, with an x-amz-apigw-id", invoked.status, invoked.header, invoked.body)
	}
}

// logs writes a log event over awsJson1_1.
func (w *workload) logs(t *testing.T) {
	t.Helper()
	must(t, "CreateLogGroup", awsJSON(t, w.emu, "1.1", "logs", "Logs_20140328.CreateLogGroup", `{"logGroupName":"/smoke"}`), http.StatusOK)
	must(t, "CreateLogStream", awsJSON(t, w.emu, "1.1", "logs", "Logs_20140328.CreateLogStream", `{"logGroupName":"/smoke","logStreamName":"run"}`), http.StatusOK)
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	put := must(t, "PutLogEvents", awsJSON(t, w.emu, "1.1", "logs", "Logs_20140328.PutLogEvents",
		`{"logGroupName":"/smoke","logStreamName":"run","logEvents":[{"timestamp":`+now+`,"message":"hello"}]}`), http.StatusOK)
	if strings.Contains(put.body, "rejectedLogEventsInfo") {
		t.Fatalf("PutLogEvents = %s", put.body)
	}
}

// secrets stores a secret and reads it back over awsJson1_1.
func (w *workload) secrets(t *testing.T) {
	t.Helper()
	must(t, "CreateSecret", awsJSON(t, w.emu, "1.1", "secretsmanager", "secretsmanager.CreateSecret", `{"Name":"smoke","SecretString":"s3cret","ClientRequestToken":"5b0c8f4e-7c1d-4e2a-9f3b-1d2e3f4a5b6c"}`), http.StatusOK)
	var got struct{ Name, SecretString string }
	decode(t, "GetSecretValue", must(t, "GetSecretValue", awsJSON(t, w.emu, "1.1", "secretsmanager", "secretsmanager.GetSecretValue", `{"SecretId":"smoke"}`), http.StatusOK).body, &got)
	if got.Name != "smoke" || got.SecretString != "s3cret" {
		t.Fatalf("GetSecretValue = %+v", got)
	}
}

// ssm stores a String parameter and reads it back over awsJson1_1.
func (w *workload) ssm(t *testing.T) {
	t.Helper()
	must(t, "PutParameter", awsJSON(t, w.emu, "1.1", "ssm", "AmazonSSM.PutParameter", `{"Name":"/smoke/a","Type":"String","Value":"v"}`), http.StatusOK)
	var got struct{ Parameter struct{ Name, Value string } }
	decode(t, "GetParameter", must(t, "GetParameter", awsJSON(t, w.emu, "1.1", "ssm", "AmazonSSM.GetParameter", `{"Name":"/smoke/a"}`), http.StatusOK).body, &got)
	if got.Parameter.Name != "/smoke/a" || got.Parameter.Value != "v" {
		t.Fatalf("GetParameter = %+v", got.Parameter)
	}
}

// events puts a scheduled rule and an event over awsJson1_1.
func (w *workload) events(t *testing.T) {
	t.Helper()
	var rule struct{ RuleArn string }
	decode(t, "PutRule", must(t, "PutRule", awsJSON(t, w.emu, "1.1", "events", "AWSEvents.PutRule", `{"Name":"smoke","ScheduleExpression":"rate(5 minutes)"}`), http.StatusOK).body, &rule)
	if rule.RuleArn != "arn:aws:events:"+region+":"+account+":rule/smoke" {
		t.Fatalf("PutRule RuleArn = %q", rule.RuleArn)
	}
	var put struct{ FailedEntryCount int }
	decode(t, "PutEvents", must(t, "PutEvents", awsJSON(t, w.emu, "1.1", "events", "AWSEvents.PutEvents", `{"Entries":[{"Source":"smoke","DetailType":"smoke","Detail":"{}"}]}`), http.StatusOK).body, &put)
	if put.FailedEntryCount != 0 {
		t.Fatalf("PutEvents failed %d entries", put.FailedEntryCount)
	}
}

// cloudwatch puts one alarm over awsJson1_0 and one over rpcv2Cbor, then
// lists both.
func (w *workload) cloudwatch(t *testing.T) {
	t.Helper()
	const target = "GraniteServiceVersion20100801"
	must(t, "PutMetricAlarm over awsJson1_0", send(t, w.emu, request{
		path: "/", service: "monitoring",
		header: map[string]string{"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": target + ".PutMetricAlarm", "x-amzn-query-mode": "true"},
		body:   []byte(`{"AlarmName":"json","MetricName":"Errors","Namespace":"Smoke","Statistic":"Sum","Period":60,"EvaluationPeriods":1,"Threshold":1,"ComparisonOperator":"GreaterThanOrEqualToThreshold"}`),
	}), http.StatusOK)

	cw := cbor.NewWriter()
	cw.BeginMap()
	for _, kv := range [][2]string{{"AlarmName", "cbor"}, {"MetricName", "Errors"}, {"Namespace", "Smoke"}, {"Statistic", "Sum"}, {"ComparisonOperator", "GreaterThanOrEqualToThreshold"}} {
		cw.Key(kv[0])
		cw.Text(kv[1])
	}
	cw.Key("Period")
	cw.Int(60)
	cw.Key("EvaluationPeriods")
	cw.Int(1)
	cw.Key("Threshold")
	cw.Float64(1)
	cw.EndMap()
	body, err := cw.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	must(t, "PutMetricAlarm over rpcv2Cbor", send(t, w.emu, request{
		path: "/service/" + target + "/operation/PutMetricAlarm", service: "monitoring",
		header: map[string]string{"Smithy-Protocol": "rpc-v2-cbor", "Content-Type": "application/cbor", "Accept": "application/cbor", "x-amzn-query-mode": "true"},
		body:   body,
	}), http.StatusOK)

	var alarms struct{ MetricAlarms []struct{ AlarmName string } }
	decode(t, "DescribeAlarms", must(t, "DescribeAlarms", send(t, w.emu, request{
		path: "/", service: "monitoring",
		header: map[string]string{"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": target + ".DescribeAlarms", "x-amzn-query-mode": "true"},
		body:   []byte(`{}`),
	}), http.StatusOK).body, &alarms)
	if len(alarms.MetricAlarms) != 2 || alarms.MetricAlarms[0].AlarmName != "cbor" || alarms.MetricAlarms[1].AlarmName != "json" {
		t.Fatalf("DescribeAlarms = %+v, want the alarms put over both protocols", alarms)
	}
}

// stepFunctions creates a Pass state machine over awsJson1_0 and lists it.
func (w *workload) stepFunctions(t *testing.T) {
	t.Helper()
	must(t, "CreateStateMachine", awsJSON(t, w.emu, "1.0", "states", "AWSStepFunctions.CreateStateMachine",
		`{"name":"smoke","roleArn":"arn:aws:iam::`+account+`:role/smoke","definition":"{\"StartAt\":\"P\",\"States\":{\"P\":{\"Type\":\"Pass\",\"End\":true}}}"}`), http.StatusOK)
	var list struct {
		StateMachines []struct{ Name, StateMachineArn string }
	}
	decode(t, "ListStateMachines", must(t, "ListStateMachines", awsJSON(t, w.emu, "1.0", "states", "AWSStepFunctions.ListStateMachines", `{}`), http.StatusOK).body, &list)
	if len(list.StateMachines) != 1 || list.StateMachines[0].StateMachineArn != "arn:aws:states:"+region+":"+account+":stateMachine:smoke" {
		t.Fatalf("ListStateMachines = %+v, want the smoke machine", list)
	}
}

// ses verifies an email identity over awsQuery and reads it back Pending,
// keyed by the address as sent (ses.md 5.2, 9.1).
func (w *workload) ses(t *testing.T) {
	t.Helper()
	const addr = "smoke@example.com"
	must(t, "VerifyEmailIdentity", awsQuery(t, w.emu, "ses", "VerifyEmailIdentity", "2010-12-01", url.Values{"EmailAddress": {addr}}), http.StatusOK)
	attrs := must(t, "GetIdentityVerificationAttributes", awsQuery(t, w.emu, "ses", "GetIdentityVerificationAttributes", "2010-12-01", url.Values{
		"Identities.member.1": {addr},
	}), http.StatusOK)
	if element(t, attrs.body, "key") != addr || element(t, attrs.body, "VerificationStatus") != "Pending" {
		t.Fatalf("GetIdentityVerificationAttributes = %s, want %s Pending", attrs.body, addr)
	}
}

// sesv2 creates a domain identity over restJson1, a REST request signed
// ses that rule 8 routes to sesv2 (ADR 0019), and reads it back PENDING
// with three DKIM tokens (ses.md 5.4).
func (w *workload) sesv2(t *testing.T) {
	t.Helper()
	const domain = "smoke.example"
	must(t, "CreateEmailIdentity", send(t, w.emu, request{
		path: "/v2/email/identities", service: "ses",
		header: map[string]string{"Content-Type": "application/json"}, body: []byte(`{"EmailIdentity":"` + domain + `"}`),
	}), http.StatusOK)
	got := must(t, "GetEmailIdentity", send(t, w.emu, request{
		method: http.MethodGet, path: "/v2/email/identities/" + domain, service: "ses",
	}), http.StatusOK)
	var identity struct {
		VerificationStatus string
		DkimAttributes     struct{ Tokens []string }
	}
	decode(t, "GetEmailIdentity", got.body, &identity)
	if identity.VerificationStatus != "PENDING" || len(identity.DkimAttributes.Tokens) != 3 {
		t.Fatalf("GetEmailIdentity = %s, want PENDING with three DKIM tokens", got.body)
	}
}

// scheduler creates a rate schedule with an SQS target over restJson1,
// reads it back with the defaults AWS fills (scheduler.md 4.2), deletes it
// and reads it gone. Its queue is its own, so the SNS step's queue holds
// no invocation.
func (w *workload) scheduler(t *testing.T) {
	t.Helper()
	const queue = "smoke-schedule"
	must(t, "CreateQueue", awsJSON(t, w.emu, "1.0", "sqs", "AmazonSQS.CreateQueue", `{"QueueName":"`+queue+`"}`), http.StatusOK)
	rest := func(method, body string) response {
		return send(t, w.emu, request{
			method: method, path: "/schedules/smoke", service: "scheduler",
			header: map[string]string{"Content-Type": "application/json"}, body: []byte(body),
		})
	}
	var created struct{ ScheduleArn string }
	body := `{"ScheduleExpression":"rate(1 hour)","FlexibleTimeWindow":{"Mode":"OFF"},` +
		`"Target":{"Arn":"arn:aws:sqs:` + region + `:` + account + `:` + queue + `","RoleArn":"` + w.roleARN + `","Input":"hello <aws.scheduler.attempt-number>"}}`
	decode(t, "CreateSchedule", must(t, "CreateSchedule", rest(http.MethodPost, body), http.StatusOK).body, &created)
	if created.ScheduleArn != "arn:aws:scheduler:"+region+":"+account+":schedule/default/smoke" {
		t.Fatalf("CreateSchedule ScheduleArn = %q", created.ScheduleArn)
	}
	var got struct {
		GroupName, ActionAfterCompletion string
		Target                           struct {
			RetryPolicy struct{ MaximumEventAgeInSeconds, MaximumRetryAttempts int }
		}
	}
	decode(t, "GetSchedule", must(t, "GetSchedule", rest(http.MethodGet, ""), http.StatusOK).body, &got)
	if got.GroupName != "default" || got.ActionAfterCompletion != "NONE" ||
		got.Target.RetryPolicy.MaximumEventAgeInSeconds != 86400 || got.Target.RetryPolicy.MaximumRetryAttempts != 185 {
		t.Fatalf("GetSchedule = %+v, want group default, NONE and retry 86400/185", got)
	}
	must(t, "DeleteSchedule", rest(http.MethodDelete, ""), http.StatusOK)
	must(t, "GetSchedule after delete", rest(http.MethodGet, ""), http.StatusNotFound)
}

// policy asks the CLI for the smoke principal's least-privilege policy,
// which carries the KMS action S3 called on its behalf for the SSE-KMS
// put, grants s3:PutObject on the bucket's objects and iam:PassRole on the
// role CreateFunction passed, sqs:SendMessage for the SNS-to-SQS delivery,
// and nothing on Object Lambda access points; with Docker, the execution
// role's policy carries the log actions.
func (w *workload) policy(t *testing.T) {
	t.Helper()
	out := cli(t, w.emu, "iam", "policy", "--principal", principal)
	for _, action := range []string{"kms:GenerateDataKey", "s3:PutObject", "sqs:SendMessage"} {
		if !strings.Contains(out, action) {
			t.Errorf("understudy iam policy for %s lacks %s:\n%s", principal, action, out)
		}
	}
	if strings.Contains(out, "s3-object-lambda:") {
		t.Errorf("understudy iam policy for %s grants an s3-object-lambda action no call used:\n%s", principal, out)
	}
	document, _, _ := strings.Cut(out, "\n")
	doc, err := policydoc.Parse([]byte(document))
	if err != nil {
		t.Fatalf("understudy iam policy printed %q, which does not parse as a policy: %v", document, err)
	}
	for _, q := range []struct {
		action, resource string
		allowed          bool
	}{
		{"s3:PutObject", "arn:aws:s3:::" + bucket + "/plain.txt", true},
		{"iam:PassRole", w.roleARN, true},
		{"iam:PassRole", "arn:aws:iam::" + account + ":role/other", false},
	} {
		_, allowed := policydoc.Allows(doc, policydoc.Query{Action: q.action, Resource: q.resource}).(policydoc.Allowed)
		if allowed != q.allowed {
			t.Errorf("understudy iam policy for %s allows %s on %s: %t, want %t:\n%s", principal, q.action, q.resource, allowed, q.allowed, out)
		}
	}
	if !w.invoke {
		return
	}
	// The invoked function's logs were written as its execution role.
	role := cli(t, w.emu, "iam", "policy", "--principal", w.roleARN)
	for _, action := range []string{"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"} {
		if !strings.Contains(role, action) {
			t.Errorf("understudy iam policy for the execution role %s lacks %s:\n%s", w.roleARN, action, role)
		}
	}
}

// coverage reads the coverage report's three numbers: protocol compliance,
// operations per service and evidence, for every service health lists.
func (w *workload) coverage(t *testing.T) {
	t.Helper()
	var health struct {
		Services map[string]json.RawMessage
	}
	decode(t, "health", must(t, "health", send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/v1/health"}), http.StatusOK).body, &health)
	var report struct {
		Protocols []struct {
			Protocol string
			Passed   int
		}
		Services []struct {
			Operations []json.RawMessage
		}
		Switches []json.RawMessage
	}
	decode(t, "coverage", must(t, "coverage", send(t, w.emu, request{method: http.MethodGet, path: "/_understudy/v1/coverage?format=json"}), http.StatusOK).body, &report)
	passed := 0
	for _, p := range report.Protocols {
		passed += p.Passed
	}
	operations := 0
	for _, s := range report.Services {
		operations += len(s.Operations)
	}
	if passed == 0 || len(health.Services) == 0 || len(report.Services) != len(health.Services) || operations == 0 || len(report.Switches) == 0 {
		t.Fatalf("coverage: %d protocol cases passed, %d services (health lists %d) with %d operations, %d switches; want all three numbers for every service",
			passed, len(report.Services), len(health.Services), operations, len(report.Switches))
	}
}
