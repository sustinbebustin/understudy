package destination_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

// The golden fixtures replace LocalStack's placeholders (<uuid:1>,
// <resource:2>, "date") with these fixed values.
const (
	goldenRequestID = ids.LambdaRequestID("0f0e0d0c-0b0a-4908-8706-050403020100")
	goldenQualified = "arn:aws:lambda:us-east-1:111111111111:function:destination-fn:$LATEST"
)

// goldenAt is the fixtures' timestamp: the documented example's format,
// milliseconds and Z (lambda-destinations-layers.md 3.1, 3.2).
var goldenAt = time.Date(2026, 10, 26, 9, 5, 7, 123_000_000, time.UTC)

// successResponse is the recorded success record's responsePayload as the
// Python test function returns it, json.dumps spacing included
// (test_lambda_destinations.snapshot.json:L101-L113).
const successResponse = `{"event": {}, "context": {"invoked_function_arn": "arn:aws:lambda:us-east-1:111111111111:function:destination-fn", ` +
	`"function_version": "$LATEST", "function_name": "destination-fn", "memory_limit_in_mb": "128", ` +
	`"aws_request_id": "0f0e0d0c-0b0a-4908-8706-050403020100", "log_group_name": "/aws/lambda/destination-fn", ` +
	`"log_stream_name": "2026/10/26/[$LATEST]0123456789abcdef0123456789abcdef", "client_context": null}}`

// raiseErrorResponse is the Python runtime's error object for the recorded
// RetriesExhausted record (test_lambda_destinations.snapshot.json:L167-L174).
const raiseErrorResponse = `{"errorMessage": "Test exception (this is intentional)", "errorType": "Exception", ` +
	`"requestId": "0f0e0d0c-0b0a-4908-8706-050403020100", ` +
	`"stackTrace": ["  File \"/var/task/handler.py\", line 55, in handler\n    raise Exception(\"Test exception (this is intentional)\")\n"]}`

// maxEventAgeResponse is the error object of the recorded EventAgeExceeded
// record (test_lambda_destinations.snapshot.json:L430-L442).
const maxEventAgeResponse = `{"errorMessage": "intentional failure", "errorType": "Exception", ` +
	`"requestId": "0f0e0d0c-0b0a-4908-8706-050403020100", ` +
	`"stackTrace": ["  File \"/var/task/handler.py\", line 7, in handler\n    raise Exception(\"intentional failure\")\n"]}`

// golden reads testdata/<name>.json without the final newline the editor
// config adds: the exact bytes Build writes for the test's inputs.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(b, []byte("\n"))
}

// Build writes the records AWS delivered in the recorded tests, keys in
// AWS's order at every level, the envelope compact and both payloads the
// bytes given (lambda-destinations-layers.md 3.1, 3.4, 11.4). Each case's
// inputs are the recording's, its placeholders replaced by the fixed values
// above. The LocalStack snapshot stores parsed bodies, so its cases take
// their payload spacing from LDL-02, which recorded the raw bytes
// (testdata/parity/lambda/LDL-02/notes.md, row "raw `Body` spacing").
func TestBuildMatchesTheRecordedRecords(t *testing.T) {
	cases := []struct {
		name   string
		record destination.Record
	}{
		{
			// Success of an unqualified invoke (test_lambda_destinations.snapshot.json:L87-L114,
			// validated 2025-11-24): count 1, no functionError.
			"success-sqs",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.Success,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(successResponse)}},
				Payload:    []byte(`{}`),
			},
		},
		{
			// A function error with MaximumRetryAttempts 0
			// (test_lambda_destinations.snapshot.json:L150-L175).
			"retries-exhausted",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.RetriesExhausted,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{
					ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(raiseErrorResponse),
				}},
				Payload: []byte(`{"raise_error": 1}`),
			},
		},
		{
			// Reserved concurrency 0: no attempt, count 0, no response half
			// (test_lambda.snapshot.json:L2968-L2978, validated 2025-11-25).
			"zero-reserved-concurrency",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.ZeroReservedConcurrency,
				Invocation: destination.NeverRan{},
				Payload:    []byte(`{}`),
			},
		},
		{
			// The age limit decided after the first failed attempt: count 1
			// with that attempt's response half
			// (test_lambda_destinations.snapshot.json:L407-L486, validated 2025-11-27).
			"event-age-exceeded",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.EventAgeExceeded,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{
					ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(maxEventAgeResponse),
				}},
				Payload: []byte(`{"message": "msg-0001"}`),
			},
		},
		{
			// The raw SQS Body of the LDL-02 ok record: the envelope compact,
			// both payloads the bytes the caller sent and the Python runtime
			// returned, spaces kept (lambda-destinations-layers.md 11.4:
			// testdata/parity/lambda/LDL-02/notes.md, row "raw `Body`
			// spacing"; testdata/parity/lambda/LDL-02/01-invoke-ok.json).
			"spaced-payloads",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.Success,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(`{"ok": true, "mode": "ok"}`)}},
				Payload:    []byte(`{"mode": "ok",  "pad": [1, 2]}`),
			},
		},
		{
			// A handler returning None: "responsePayload":null, the key
			// present (testdata/parity/lambda/LDL-02/notes.md, row
			// "`responsePayload` for `none`").
			"null-response",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.Success,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(`null`)}},
				Payload:    []byte(`{"mode": "none",  "pad": [1, 2]}`),
			},
		},
		{
			// A handler returning a string: the runtime's JSON string,
			// embedded as such (testdata/parity/lambda/LDL-02/notes.md, row
			// "`responsePayload` for `text`").
			"string-response",
			destination.Record{
				At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.Success,
				Invocation: destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", Payload: []byte(`"plain text"`)}},
				Payload:    []byte(`{"mode": "text",  "pad": [1, 2]}`),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := golden(t, c.name)
			got, err := destination.Build(c.record)
			if err != nil {
				t.Fatalf("Build = %v", err)
			}
			if !bytes.Equal(got.JSON, want) {
				t.Errorf("Build =\n%s\nwant testdata/%s.json\n%s", got.JSON, c.name, want)
			}
			if got.ResponseOmitted {
				t.Error("ResponseOmitted = true for a JSON response")
			}
		})
	}
}

// The four conditions and their wire values (test_lambda_destinations.snapshot.json:L93,
// L156, L424; test_lambda.snapshot.json:L2974). Every one but Success sends
// the record to the on-failure destination and the event to the
// dead-letter queue (lambda-destinations-layers.md 5.3, from
// https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html).
// The EventBridge detail-type is the guide's text (lambda-destinations-layers.md 4.4;
// test_lambda_destinations.snapshot.json:L584 for Success).
func TestConditionValues(t *testing.T) {
	const (
		succeeded = "Lambda Function Invocation Result - Success"
		failed    = "Lambda Function Invocation Result - Failure"
	)
	want := []struct {
		c      destination.Condition
		wire   string
		failed bool
		detail string
	}{
		{destination.Success, "Success", false, succeeded},
		{destination.RetriesExhausted, "RetriesExhausted", true, failed},
		{destination.EventAgeExceeded, "EventAgeExceeded", true, failed},
		{destination.ZeroReservedConcurrency, "ZeroReservedConcurrency", true, failed},
	}
	declared := make([]destination.Condition, len(want))
	for i, w := range want {
		declared[i] = w.c
		if got := w.c.String(); got != w.wire {
			t.Errorf("Condition(%d).String() = %q, want %q", w.c, got, w.wire)
		}
		if got := w.c.Failed(); got != w.failed {
			t.Errorf("%s.Failed() = %t, want %t", w.wire, got, w.failed)
		}
		if got := w.c.DetailType(); got != w.detail {
			t.Errorf("%s.DetailType() = %q, want %q", w.wire, got, w.detail)
		}
	}
	if got := destination.Conditions(); !slices.Equal(got, declared) {
		t.Errorf("Conditions() = %v, want %v in value order", got, declared)
	}
	for _, c := range []destination.Condition{0, destination.ZeroReservedConcurrency + 1} {
		if got, want := c.String(), "Condition("+strconv.Itoa(int(c))+")"; got != want {
			t.Errorf("undeclared Condition(%d).String() = %q, want %q", c, got, want)
		}
		if c.Failed() {
			t.Errorf("undeclared Condition(%d).Failed() = true, want false", c)
		}
	}
}

// A response that is not JSON leaves responsePayload out and says so:
// how AWS renders one is unrecorded, so understudy embeds nothing rather
// than guess (lambda-destinations-layers.md 11.4, LDL-02). The response
// context stays. An empty response is not JSON.
func TestBuildLeavesOutAResponseThatIsNotJSON(t *testing.T) {
	for _, payload := range []string{"", "RequestId: 0f0e0d0c Error: Runtime exited", "{", `{"a":}`} {
		r := destination.Record{
			At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.RetriesExhausted,
			Invocation: destination.Ran{Attempts: 3, Response: destination.Response{ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(payload)}},
			Payload:    []byte(`{"n":1}`),
		}
		got, err := destination.Build(r)
		if err != nil {
			t.Errorf("Build with response %q = %v, want a record without responsePayload", payload, err)
			continue
		}
		if !got.ResponseOmitted {
			t.Errorf("Build with response %q: ResponseOmitted = false", payload)
		}
		want := `{"version":"1.0","timestamp":"2026-10-26T09:05:07.123Z","requestContext":{"requestId":"0f0e0d0c-0b0a-4908-8706-050403020100",` +
			`"functionArn":"arn:aws:lambda:us-east-1:111111111111:function:destination-fn:$LATEST","condition":"RetriesExhausted","approximateInvokeCount":3},` +
			`"requestPayload":{"n":1},"responseContext":{"statusCode":200,"executedVersion":"$LATEST","functionError":"Unhandled"}}`
		if string(got.JSON) != want {
			t.Errorf("Build with response %q =\n%s\nwant\n%s", payload, got.JSON, want)
		}
	}
}

// The timestamp is the instant in UTC to the millisecond, as the
// documented example writes it (lambda-destinations-layers.md 3.2, 11.4),
// whatever zone the instant carries; finer digits are cut, not rounded.
func TestBuildWritesTheTimestampInUTC(t *testing.T) {
	at := time.Date(2026, 10, 27, 5, 0, 0, 999_999_999, time.FixedZone("UTC+5:30", 5*3600+1800))
	r := destination.Record{
		At: at, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.ZeroReservedConcurrency,
		Invocation: destination.NeverRan{}, Payload: []byte(`{}`),
	}
	got, err := destination.Build(r)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	if ts := stringMember(t, got.JSON, "timestamp"); ts != "2026-10-26T23:30:00.999Z" {
		t.Errorf("timestamp = %q, want 2026-10-26T23:30:00.999Z", ts)
	}
}

// Build refuses a record AWS never writes rather than encode it, naming
// the member at fault.
func TestBuildRefusesRecordsAWSNeverWrites(t *testing.T) {
	valid := func() destination.Record {
		return destination.Record{
			At: goldenAt, RequestID: goldenRequestID, FunctionARN: goldenQualified, Condition: destination.RetriesExhausted,
			Invocation: destination.Ran{Attempts: 1, Response: destination.Response{ExecutedVersion: "$LATEST", FunctionError: "Unhandled", Payload: []byte(`{}`)}},
			Payload:    []byte(`{}`),
		}
	}
	if _, err := destination.Build(valid()); err != nil {
		t.Fatalf("Build of a valid record = %v", err)
	}
	ranWith := func(attempts int, functionError string) destination.Invocation {
		return destination.Ran{Attempts: attempts, Response: destination.Response{ExecutedVersion: "$LATEST", FunctionError: functionError, Payload: []byte(`{}`)}}
	}
	cases := []struct {
		name   string
		mutate func(*destination.Record)
		// fields are the member names one of which the error's Field
		// must contain.
		fields []string
	}{
		{"an undeclared condition", func(r *destination.Record) { r.Condition = 0 }, []string{"Condition"}},
		{"a condition past the declared ones", func(r *destination.Record) { r.Condition = destination.ZeroReservedConcurrency + 1 }, []string{"Condition"}},
		{"a success that never ran", func(r *destination.Record) {
			r.Condition, r.Invocation = destination.Success, destination.NeverRan{}
		}, []string{"Invocation", "Condition"}},
		{"a success with a function error", func(r *destination.Record) {
			r.Condition, r.Invocation = destination.Success, ranWith(1, "Unhandled")
		}, []string{"FunctionError"}},
		{"a failure that ran without a function error", func(r *destination.Record) { r.Invocation = ranWith(1, "") }, []string{"FunctionError"}},
		{"no attempts", func(r *destination.Record) { r.Invocation = ranWith(0, "Unhandled") }, []string{"Attempts"}},
		{"negative attempts", func(r *destination.Record) { r.Invocation = ranWith(-1, "Unhandled") }, []string{"Attempts"}},
		{"a payload that is not JSON", func(r *destination.Record) { r.Payload = []byte(`{"n":`) }, []string{"Payload"}},
		{"an empty payload", func(r *destination.Record) { r.Payload = nil }, []string{"Payload"}},
		{"no invocation", func(r *destination.Record) { r.Invocation = nil }, []string{"Invocation"}},
		{"no request id", func(r *destination.Record) { r.RequestID = "" }, []string{"RequestID"}},
		{"no function ARN", func(r *destination.Record) { r.FunctionARN = "" }, []string{"FunctionARN"}},
	}
	for _, c := range cases {
		r := valid()
		c.mutate(&r)
		_, err := destination.Build(r)
		re, ok := errors.AsType[*destination.RecordError](err)
		if !ok {
			t.Errorf("%s: Build = %v, want a *RecordError", c.name, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "destination: cannot build the invocation record: ") {
			t.Errorf("%s: error %q does not start with the package's prefix", c.name, err)
		}
		if !slices.ContainsFunc(c.fields, func(f string) bool { return strings.Contains(re.Field, f) }) {
			t.Errorf("%s: Field = %q, want it to name one of %v", c.name, re.Field, c.fields)
		}
	}
}

// For any record Build accepts: valid JSON; a compact envelope with the
// keys in AWS's order at every level (lambda-destinations-layers.md 3.1,
// 11.4); version 1.0; the timestamp in UTC to the millisecond;
// approximateInvokeCount the attempts made, 0 when the function never ran
// (3.2); the response half present exactly when it ran (finding 5), with
// statusCode 200 and functionError only for a function error (3.2); and
// both payloads embedded byte for byte as given, whitespace included
// (11.4: testdata/parity/lambda/LDL-02/notes.md, row "raw `Body`
// spacing").
func TestBuildProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genRecord().Draw(t, "record")
		got, err := destination.Build(r)
		if err != nil {
			t.Fatalf("Build = %v", err)
		}
		out := got.JSON
		if !json.Valid(out) {
			t.Fatalf("Build wrote invalid JSON: %s", out)
		}
		if got.ResponseOmitted {
			t.Fatalf("ResponseOmitted = true for JSON response %s", responseOf(r))
		}
		ran, didRun := r.Invocation.(destination.Ran)
		wantKeys := []string{"version", "timestamp", "requestContext", "requestPayload"}
		if didRun {
			wantKeys = append(wantKeys, "responseContext", "responsePayload")
		}
		if k := keys(t, out); !slices.Equal(k, wantKeys) {
			t.Fatalf("keys = %v, want %v", k, wantKeys)
		}
		if v := stringMember(t, out, "version"); v != "1.0" {
			t.Fatalf("version = %q, want 1.0", v)
		}
		if ts, want := stringMember(t, out, "timestamp"), r.At.UTC().Format("2006-01-02T15:04:05.000Z"); ts != want {
			t.Fatalf("timestamp = %q, want %q", ts, want)
		}

		rc := member(t, out, "requestContext")
		if k := keys(t, rc); !slices.Equal(k, []string{"requestId", "functionArn", "condition", "approximateInvokeCount"}) {
			t.Fatalf("requestContext keys = %v", k)
		}
		count := 0
		if didRun {
			count = ran.Attempts
		}
		if id, arn, cond, n := stringMember(t, rc, "requestId"), stringMember(t, rc, "functionArn"), stringMember(t, rc, "condition"), string(member(t, rc, "approximateInvokeCount")); id != string(r.RequestID) ||
			arn != r.FunctionARN || cond != r.Condition.String() || n != strconv.Itoa(count) {
			t.Fatalf("requestContext = %s, want %s, %s, %s, %d", rc, r.RequestID, r.FunctionARN, r.Condition, count)
		}

		if p, want := member(t, out, "requestPayload"), unpadded(r.Payload); !bytes.Equal(p, want) {
			t.Fatalf("requestPayload = %s, want the event's bytes %s", p, want)
		}
		// The envelope is compact: out is the compact contexts and the
		// payloads joined with no whitespace.
		ts := `"` + r.At.UTC().Format("2006-01-02T15:04:05.000Z") + `"`
		want := `{"version":"1.0","timestamp":` + ts + `,"requestContext":` + string(compacted(t, rc)) + `,"requestPayload":` + string(unpadded(r.Payload))
		if !didRun {
			if string(out) != want+"}" {
				t.Fatalf("Build =\n%s\nwant\n%s}", out, want)
			}
			return
		}
		resp := member(t, out, "responseContext")
		want += `,"responseContext":` + string(compacted(t, resp)) + `,"responsePayload":` + string(unpadded(ran.Response.Payload)) + "}"
		if string(out) != want {
			t.Fatalf("Build =\n%s\nwant\n%s", out, want)
		}
		wantResp := []string{"statusCode", "executedVersion"}
		if ran.Response.FunctionError != "" {
			wantResp = append(wantResp, "functionError")
		}
		if k := keys(t, resp); !slices.Equal(k, wantResp) {
			t.Fatalf("responseContext keys = %v, want %v", k, wantResp)
		}
		if s, v := string(member(t, resp, "statusCode")), stringMember(t, resp, "executedVersion"); s != "200" || v != ran.Response.ExecutedVersion {
			t.Fatalf("responseContext = %s, want statusCode 200 and executedVersion %s", resp, ran.Response.ExecutedVersion)
		}
		if ran.Response.FunctionError != "" && stringMember(t, resp, "functionError") != ran.Response.FunctionError {
			t.Fatalf("responseContext = %s, want functionError %s", resp, ran.Response.FunctionError)
		}
	})
}

// unpadded is p without the JSON whitespace around the whole value: how
// AWS writes it is unrecorded, and Build leaves it out so the envelope
// stays compact (lambda-destinations-layers.md 11.4).
func unpadded(p []byte) []byte { return bytes.Trim(p, " \t\r\n") }

// For any response that is not JSON, the record keeps its response
// context, leaves responsePayload out and reports it
// (lambda-destinations-layers.md 11.4, LDL-02).
func TestBuildOmitsAnyNonJSONResponse(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := genRecord().Filter(func(r destination.Record) bool {
			_, ok := r.Invocation.(destination.Ran)
			return ok
		}).Draw(t, "record")
		ran, ok := r.Invocation.(destination.Ran)
		if !ok {
			t.Fatalf("the filter let through %#v", r.Invocation)
		}
		ran.Response.Payload = []byte(rapid.String().Filter(func(s string) bool { return !json.Valid([]byte(s)) }).Draw(t, "response"))
		r.Invocation = ran
		got, err := destination.Build(r)
		if err != nil {
			t.Fatalf("Build = %v", err)
		}
		if !got.ResponseOmitted {
			t.Fatalf("ResponseOmitted = false for response %q", ran.Response.Payload)
		}
		if k := keys(t, got.JSON); !slices.Equal(k, []string{"version", "timestamp", "requestContext", "requestPayload", "responseContext"}) {
			t.Fatalf("keys = %v, want the response context and no responsePayload", k)
		}
	})
}

// genRecord draws a record AWS writes: a success that ran without a
// function error, a function error that ran out of retries, an event that
// aged out after running or without ever running, and an event reserved
// concurrency 0 kept from running (lambda-destinations-layers.md 3.3).
// Attempts are 1 to 3: MaximumRetryAttempts is 0 to 2 (lambda-api.md 7.7).
func genRecord() *rapid.Generator[destination.Record] {
	return rapid.Custom(func(t *rapid.T) destination.Record {
		cond := rapid.SampledFrom([]destination.Condition{
			destination.Success, destination.RetriesExhausted, destination.EventAgeExceeded, destination.ZeroReservedConcurrency,
		}).Draw(t, "condition")
		ran := func(functionError string) destination.Invocation {
			return destination.Ran{
				Attempts: rapid.IntRange(1, 3).Draw(t, "attempts"),
				Response: destination.Response{
					ExecutedVersion: rapid.SampledFrom([]string{"$LATEST", "1", "42"}).Draw(t, "executedVersion"),
					FunctionError:   functionError,
					Payload:         []byte(genPayload().Draw(t, "response")),
				},
			}
		}
		var inv destination.Invocation
		switch {
		case cond == destination.Success:
			inv = ran("")
		case cond == destination.RetriesExhausted:
			inv = ran("Unhandled")
		case cond == destination.EventAgeExceeded && rapid.Bool().Draw(t, "ranBeforeAgingOut"):
			inv = ran("Unhandled")
		default:
			inv = destination.NeverRan{}
		}
		zone := rapid.SampledFrom([]*time.Location{time.UTC, time.FixedZone("UTC+5:30", 5*3600+1800), time.FixedZone("UTC-8", -8*3600)}).Draw(t, "zone")
		at := time.Unix(rapid.Int64Range(0, 4_102_444_800).Draw(t, "seconds"), rapid.Int64Range(0, 999_999_999).Draw(t, "nanos")).In(zone)
		return destination.Record{
			At:          at,
			RequestID:   ids.LambdaRequestID(rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).Draw(t, "requestID")),
			FunctionARN: rapid.SampledFrom([]string{goldenQualified, "arn:aws:lambda:eu-west-2:222222222222:function:orders:live", "arn:aws:lambda:us-east-1:111111111111:function:orders:7"}).Draw(t, "functionArn"),
			Condition:   cond,
			Invocation:  inv,
			Payload:     []byte(genPayload().Draw(t, "payload")),
		}
	})
}

// genPayload draws a payload as a caller or a runtime may write it: JSON
// text from genJSON, sometimes with whitespace around the whole value.
func genPayload() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		pad := func(label string) string {
			return rapid.SampledFrom([]string{"", "", "", " ", "\n", "\r\n\t"}).Draw(t, label)
		}
		return pad("leading") + genJSON(3).Draw(t, "json") + pad("trailing")
	})
}

// genJSON draws JSON text nested up to depth levels, with insignificant
// whitespace between tokens, numbers in forms a re-encoding would change
// (1.50, 1e3, -0) and strings holding characters an HTML-escaping encoder
// would rewrite.
func genJSON(depth int) *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		ws := func(label string) string {
			return rapid.SampledFrom([]string{"", "", " ", "\n", "\t", "  \r\n"}).Draw(t, label)
		}
		kinds := []string{"null", "bool", "number", "string"}
		if depth > 0 {
			kinds = append(kinds, "array", "object")
		}
		switch rapid.SampledFrom(kinds).Draw(t, "kind") {
		case "null":
			return "null"
		case "bool":
			return strconv.FormatBool(rapid.Bool().Draw(t, "bool"))
		case "number":
			return rapid.StringMatching(`-?(0|[1-9][0-9]{0,5})(\.[0-9]{1,3})?([eE][+-]?[0-9]{1,2})?`).Draw(t, "number")
		case "string":
			return jsonString(t, rapid.OneOf(rapid.String(), rapid.SampledFrom([]string{"<b>&amp;</b>", "a b", "tab\there"})).Draw(t, "text"))
		case "array":
			n := rapid.IntRange(0, 3).Draw(t, "elements")
			parts := make([]string, n)
			for i := range parts {
				parts[i] = ws("before") + genJSON(depth-1).Draw(t, "element") + ws("after")
			}
			return "[" + ws("open") + strings.Join(parts, ",") + "]"
		default:
			names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Za-z_][A-Za-z0-9_]{0,6}`), 0, 3, rapid.ID).Draw(t, "names")
			parts := make([]string, len(names))
			for i, name := range names {
				parts[i] = ws("before") + jsonString(t, name) + ws("colon") + ":" + ws("value") + genJSON(depth-1).Draw(t, "value") + ws("after")
			}
			return "{" + ws("open") + strings.Join(parts, ",") + "}"
		}
	})
}

// fataler is what the JSON helpers need of a *testing.T or *rapid.T.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// jsonString is s as a JSON string literal, with <, > and & unescaped.
func jsonString(t fataler, s string) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatalf("encoding %q: %v", s, err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// keys returns the member names of the JSON object raw, in order.
func keys(t fataler, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not a JSON object", raw)
	}
	var out []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("reading a key of %s: %v", raw, err)
		}
		name, ok := tok.(string)
		if !ok {
			t.Fatalf("key %v of %s is not a string", tok, raw)
		}
		out = append(out, name)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("reading %s of %s: %v", name, raw, err)
		}
	}
	return out
}

// member returns the raw value of the member name of the JSON object raw,
// byte for byte as raw holds it.
func member(t fataler, raw []byte, name string) []byte {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s is not a JSON object: %v", raw, err)
	}
	v, ok := obj[name]
	if !ok {
		t.Fatalf("%s has no member %s", raw, name)
	}
	return v
}

// stringMember returns the member name of raw, which must be a string.
func stringMember(t fataler, raw []byte, name string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(member(t, raw, name), &s); err != nil {
		t.Fatalf("member %s of %s is not a string: %v", name, raw, err)
	}
	return s
}

// compacted is raw with insignificant whitespace removed and nothing else
// changed.
func compacted(t fataler, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compacting %s: %v", raw, err)
	}
	return b.Bytes()
}

// responseOf is the response payload of r's invocation, if it ran.
func responseOf(r destination.Record) []byte {
	if ran, ok := r.Invocation.(destination.Ran); ok {
		return ran.Response.Payload
	}
	return nil
}
