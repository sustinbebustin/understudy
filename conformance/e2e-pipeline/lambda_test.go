package e2e_test

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// TestLambdaWorkersAreActive checks the three worker packages: a Go zip on
// provided.al2023, a Python zip and a container image, each Active with
// its last update Successful (lambda-api.md, function states).
func TestLambdaWorkersAreActive(t *testing.T) {
	p := pipe(t)
	want := map[string]struct {
		pkg     lambdatypes.PackageType
		runtime lambdatypes.Runtime
	}{
		goWorker:    {lambdatypes.PackageTypeZip, lambdatypes.RuntimeProvidedal2023},
		pyWorker:    {lambdatypes.PackageTypeZip, lambdatypes.RuntimePython313},
		imageWorker: {lambdatypes.PackageTypeImage, ""},
	}
	for name, w := range want {
		out, err := env.lambda.GetFunction(t.Context(), &lambda.GetFunctionInput{FunctionName: aws.String(name)})
		if err != nil {
			t.Errorf("lambda GetFunction %s: %v", name, err)
			continue
		}
		c := out.Configuration
		if c.State != lambdatypes.StateActive || c.LastUpdateStatus != lambdatypes.LastUpdateStatusSuccessful {
			t.Errorf("%s: State %s, LastUpdateStatus %s; want Active, Successful", name, c.State, c.LastUpdateStatus)
		}
		if c.PackageType != w.pkg || c.Runtime != w.runtime {
			t.Errorf("%s: PackageType %s, Runtime %q; want %s, %q", name, c.PackageType, c.Runtime, w.pkg, w.runtime)
		}
		if aws.ToString(c.FunctionArn) != p.workers[name].functionARN {
			t.Errorf("%s: FunctionArn %s, want the ARN CreateFunction returned, %s", name, aws.ToString(c.FunctionArn), p.workers[name].functionARN)
		}
		if w.pkg == lambdatypes.PackageTypeImage {
			if out.Code == nil || aws.ToString(out.Code.ImageUri) != env.artifact.image || aws.ToString(out.Code.RepositoryType) != "ECR" {
				t.Errorf("%s: Code %+v, want ImageUri %s and RepositoryType ECR", name, out.Code, env.artifact.image)
			}
		}
	}
}

// TestLambdaEventSourceMappingsKeepTheirSettings reads each mapping back:
// batch size, batching window and response types as created, State
// Enabled (lambda-events.md 1.4).
func TestLambdaEventSourceMappingsKeepTheirSettings(t *testing.T) {
	p := pipe(t)
	for _, sp := range specs {
		w := p.workers[sp.name]
		out, err := env.lambda.GetEventSourceMapping(t.Context(), &lambda.GetEventSourceMappingInput{UUID: aws.String(w.esmUUID)})
		if err != nil {
			t.Errorf("lambda GetEventSourceMapping %s: %v", sp.name, err)
			continue
		}
		var types []lambdatypes.FunctionResponseType
		if sp.reportFailures {
			types = []lambdatypes.FunctionResponseType{lambdatypes.FunctionResponseTypeReportBatchItemFailures}
		}
		if aws.ToInt32(out.BatchSize) != sp.batchSize || aws.ToInt32(out.MaximumBatchingWindowInSeconds) != sp.window ||
			!slices.Equal(out.FunctionResponseTypes, types) || aws.ToString(out.State) != "Enabled" ||
			aws.ToString(out.EventSourceArn) != w.queue.arn || aws.ToString(out.FunctionArn) != w.functionARN {
			t.Errorf("%s mapping: BatchSize %d, MaximumBatchingWindowInSeconds %d, FunctionResponseTypes %v, State %s, EventSourceArn %s, FunctionArn %s; want %d, %d, %v, Enabled, %s, %s",
				sp.name, aws.ToInt32(out.BatchSize), aws.ToInt32(out.MaximumBatchingWindowInSeconds), out.FunctionResponseTypes, aws.ToString(out.State),
				aws.ToString(out.EventSourceArn), aws.ToString(out.FunctionArn), sp.batchSize, sp.window, types, w.queue.arn, w.functionARN)
		}
	}
}

// TestLambdaSQSEventRecordShape checks the record the Go worker received
// for a raw-delivered message against the documented SQS event record
// (lambda-events.md 3): the body as sent, lowercase hex MD5 of the body,
// the system attributes as strings, and the SNS attribute carried as an
// SQS message attribute.
func TestLambdaSQSEventRecordShape(t *testing.T) {
	p := pipe(t)
	id := newID(t)
	pub := publish(t, message{ID: id}, map[string]string{"kind": "order"})
	rec := waitProcessed(t, goWorker, id).Record

	sum := md5.Sum([]byte(pub.Body)) //nolint:gosec // md5OfBody is defined as the MD5 of the body

	if rec.Body != pub.Body {
		t.Errorf("record body %q, want the published message %q (raw delivery)", rec.Body, pub.Body)
	}
	if rec.MD5OfBody != hex.EncodeToString(sum[:]) {
		t.Errorf("md5OfBody %s, want %s", rec.MD5OfBody, hex.EncodeToString(sum[:]))
	}
	if rec.EventSource != "aws:sqs" || rec.EventSourceARN != p.workers[goWorker].queue.arn || rec.AWSRegion != region {
		t.Errorf("eventSource %q, eventSourceARN %q, awsRegion %q; want aws:sqs, %s, %s", rec.EventSource, rec.EventSourceARN, rec.AWSRegion, p.workers[goWorker].queue.arn, region)
	}
	for _, k := range []string{"ApproximateReceiveCount", "SentTimestamp", "SenderId", "ApproximateFirstReceiveTimestamp"} {
		if rec.Attributes[k] == "" {
			t.Errorf("record attributes lack %s: %v", k, rec.Attributes)
		}
	}
	if rec.Attributes["ApproximateReceiveCount"] != "1" {
		t.Errorf("ApproximateReceiveCount %q on the first delivery, want 1", rec.Attributes["ApproximateReceiveCount"])
	}
	var kind struct {
		StringValue string `json:"stringValue"`
		DataType    string `json:"dataType"`
	}
	if raw, ok := rec.MessageAttributes["kind"]; !ok || json.Unmarshal(raw, &kind) != nil || kind.StringValue != "order" || kind.DataType != "String" {
		t.Errorf("messageAttributes.kind = %s, want {stringValue: order, dataType: String}", rec.MessageAttributes["kind"])
	}
	if rec.MD5OfMessageAttributes == nil || len(*rec.MD5OfMessageAttributes) != 32 {
		t.Errorf("md5OfMessageAttributes %v, want the 32-digit hex checksum of the attributes", rec.MD5OfMessageAttributes)
	}
}

// TestLambdaBatchingWindowHoldsASmallBatch drives the Python worker's
// 20 second batching window on the emulator clock: a batch below BatchSize
// is not dispatched before the window expires, and is dispatched when it
// does (lambda-events.md 2.1).
func TestLambdaBatchingWindowHoldsASmallBatch(t *testing.T) {
	pipe(t)
	ids := []string{newID(t), newID(t), newID(t)}
	for _, id := range ids {
		publish(t, message{ID: id, Priority: 7}, map[string]string{"kind": "audit"})
	}
	settle(t)
	time.Sleep(2 * time.Second)
	settle(t)
	for _, id := range ids {
		assertNotProcessed(t, pyWorker, id)
	}
	advance(t, pyWindow()-time.Second)
	for _, id := range ids {
		assertNotProcessed(t, pyWorker, id)
	}
	advance(t, time.Second)
	for _, id := range ids {
		rec := waitProcessed(t, pyWorker, id)
		if rec.Record.EventSourceARN != pipe(t).workers[pyWorker].queue.arn {
			t.Errorf("%s: eventSourceARN %s, want %s", id, rec.Record.EventSourceARN, pipe(t).workers[pyWorker].queue.arn)
		}
	}
}

// TestLambdaFullBatchDispatchesBeforeTheWindow fills the Python worker's
// BatchSize of 25: Lambda dispatches a full batch without waiting for the
// batching window (lambda-events.md 2.1), so no clock advance is needed.
func TestLambdaFullBatchDispatchesBeforeTheWindow(t *testing.T) {
	pipe(t)
	n := int(spec(pyWorker).batchSize)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = newID(t)
		publish(t, message{ID: ids[i], Priority: 9}, nil)
	}
	for _, id := range ids {
		waitProcessed(t, pyWorker, id)
	}
}

// TestLambdaContainerImageWorkerProcessesRefunds routes a refund above the
// filter's amount to the container-image worker, and one below it nowhere.
func TestLambdaContainerImageWorkerProcessesRefunds(t *testing.T) {
	pipe(t)
	big, small := newID(t), newID(t)
	publish(t, message{ID: big}, map[string]string{"kind": "refund", "amount": "250"})
	publish(t, message{ID: small}, map[string]string{"kind": "refund", "amount": "50"})
	rec := waitProcessed(t, imageWorker, big)
	if rec.Function != imageWorker {
		t.Errorf("record written by %q, want %s", rec.Function, imageWorker)
	}
	settle(t)
	assertNotProcessed(t, imageWorker, small)
}

// TestLambdaFunctionURLInvokesOverHTTP calls the image worker's function
// URL and checks the payload format 2.0 event the function received and
// the response mapping (lambda-events.md 7.3, 7.4).
func TestLambdaFunctionURLInvokesOverHTTP(t *testing.T) {
	p := pipe(t)
	u, err := url.Parse(p.functionURL)
	if err != nil {
		t.Fatalf("FunctionUrl %q: %v", p.functionURL, err)
	}
	u.Path = "/orders/42"
	u.RawQuery = "a=1&a=2&b=x%2Fy"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u.String(), strings.NewReader(`{"hello":"url"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom-Header", "Value1")
	req.Header.Add("Cookie", "c1=v1")
	req.Header.Add("Cookie", "c2=v2")
	resp, body := call(t, req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s: HTTP %d, want 201 from the function's statusCode; body %s", u, resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-E2e-Worker"); got != "image-worker" {
		t.Errorf("X-E2e-Worker header %q, want image-worker", got)
	}
	if got := resp.Header.Values("Set-Cookie"); !slices.Contains(got, "seen=1; Max-Age=60") {
		t.Errorf("Set-Cookie %v, want the function's cookie seen=1; Max-Age=60", got)
	}
	var out struct {
		Event struct {
			Version               string            `json:"version"`
			RouteKey              string            `json:"routeKey"`
			RawPath               string            `json:"rawPath"`
			RawQueryString        string            `json:"rawQueryString"`
			Cookies               []string          `json:"cookies"`
			Headers               map[string]string `json:"headers"`
			QueryStringParameters map[string]string `json:"queryStringParameters"`
			Body                  string            `json:"body"`
			IsBase64Encoded       bool              `json:"isBase64Encoded"`
			RequestContext        struct {
				APIID string `json:"apiId"`
				HTTP  struct {
					Method string `json:"method"`
					Path   string `json:"path"`
				} `json:"http"`
				Stage     string `json:"stage"`
				RequestID string `json:"requestId"`
				TimeEpoch int64  `json:"timeEpoch"`
			} `json:"requestContext"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the response body is not the function's JSON: %v\n%s", err, body)
	}
	ev := out.Event
	if ev.Version != "2.0" || ev.RouteKey != "$default" || ev.RequestContext.Stage != "$default" {
		t.Errorf("version %q, routeKey %q, stage %q; want 2.0, $default, $default", ev.Version, ev.RouteKey, ev.RequestContext.Stage)
	}
	if ev.RawPath != "/orders/42" || ev.RawQueryString != "a=1&a=2&b=x%2Fy" {
		t.Errorf("rawPath %q, rawQueryString %q; want /orders/42 and the raw query a=1&a=2&b=x%%2Fy", ev.RawPath, ev.RawQueryString)
	}
	if ev.QueryStringParameters["a"] != "1,2" || ev.QueryStringParameters["b"] != "x/y" {
		t.Errorf("queryStringParameters %v, want a=1,2 (duplicates joined) and b=x/y (decoded)", ev.QueryStringParameters)
	}
	if ev.Headers["x-custom-header"] != "Value1" {
		t.Errorf("headers %v, want x-custom-header lowercased with value Value1", ev.Headers)
	}
	if _, ok := ev.Headers["cookie"]; ok {
		t.Errorf("headers carry cookie %q; cookies belong only in the cookies array", ev.Headers["cookie"])
	}
	if !slices.Equal(ev.Cookies, []string{"c1=v1", "c2=v2"}) {
		t.Errorf("cookies %v, want [c1=v1 c2=v2]", ev.Cookies)
	}
	if ev.Body != `{"hello":"url"}` || ev.IsBase64Encoded {
		t.Errorf("body %q, isBase64Encoded %v; want the JSON text as sent and false", ev.Body, ev.IsBase64Encoded)
	}
	if ev.RequestContext.HTTP.Method != http.MethodPost || ev.RequestContext.HTTP.Path != "/orders/42" {
		t.Errorf("requestContext.http %+v, want POST /orders/42", ev.RequestContext.HTTP)
	}
	// accountId is left out: the documented example is an AWS_IAM request,
	// and what a NONE request carries there is not in the research.
	if ev.RequestContext.RequestID == "" || ev.RequestContext.TimeEpoch == 0 {
		t.Errorf("requestContext requestId %q, timeEpoch %d; want a request id and epoch milliseconds",
			ev.RequestContext.RequestID, ev.RequestContext.TimeEpoch)
	}
	if id, _, _ := strings.Cut(u.Hostname(), "."); ev.RequestContext.APIID != id {
		t.Errorf("requestContext.apiId %q, want the URL id %q", ev.RequestContext.APIID, id)
	}
}

// TestLambdaFunctionURLUnknownIDIsForbidden calls a URL id no function
// owns: 403 AccessDeniedException with {"Message":null}
// (lambda-events.md 7.2).
func TestLambdaFunctionURLUnknownIDIsForbidden(t *testing.T) {
	p := pipe(t)
	u, err := url.Parse(p.functionURL)
	if err != nil {
		t.Fatal(err)
	}
	_, domain, _ := strings.Cut(u.Host, ".")
	u.Host = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa." + domain
	resp, body := get(t, u.String())
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-Amzn-Errortype") != "AccessDeniedException" || strings.TrimSpace(string(body)) != `{"Message":null}` {
		t.Errorf("GET %s: HTTP %d, x-amzn-ErrorType %q, body %s; want 403, AccessDeniedException, {\"Message\":null}",
			u, resp.StatusCode, resp.Header.Get("X-Amzn-Errortype"), body)
	}
}
