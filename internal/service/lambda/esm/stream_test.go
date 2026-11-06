package esm_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// StartingPosition on a DynamoDB stream is TRIM_HORIZON or LATEST
// ("required; TRIM_HORIZON or LATEST", lambda-stream-sources.md 3.1, aws-doc
// ddb-params); AT_TIMESTAMP never parses, as the caller answers 3.3 row 3
// for it, and nothing outside the model's enum (3.3 row 2) parses either.
func TestParseStreamStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		wire string
		want esm.StreamStart
	}{
		{"TRIM_HORIZON", esm.TrimHorizon{}},
		{"LATEST", esm.Latest{}},
	} {
		got, ok := esm.ParseStreamStart(tc.wire)
		if !ok || got != tc.want {
			t.Errorf("ParseStreamStart(%q) = %#v, %v; want %#v, true", tc.wire, got, ok, tc.want)
		}
		if name := esm.StreamStartName(tc.want); name != tc.wire {
			t.Errorf("StreamStartName(%#v) = %q, want %q", tc.want, name, tc.wire)
		}
	}
	for _, s := range []string{"AT_TIMESTAMP", "invalid", "", "latest", "Trim_Horizon"} {
		if got, ok := esm.ParseStreamStart(s); ok {
			t.Errorf("ParseStreamStart(%q) = %#v, true; want false", s, got)
		}
	}
}

// A stream mapping created with only StartingPosition reports
// MaximumRecordAgeInSeconds -1, MaximumRetryAttempts -1 and
// DestinationConfig {"OnFailure": {}} (third-party-recorded, LS-D:L246-L271;
// lambda-stream-sources.md 3.5).
func TestDefaultStreamSettings(t *testing.T) {
	t.Parallel()
	want := esm.StreamSettings{RecordAge: esm.AnyAge{}, Retries: esm.Unlimited{}, OnFailure: esm.NoDestination{}}
	if got := esm.DefaultStreamSettings(); got != want {
		t.Errorf("DefaultStreamSettings() = %#v, want %#v", got, want)
	}
}

func streamRecord(i, size int) esm.StreamRecord {
	event := `{"eventID":"` + strconv.Itoa(i) + `","pad":"` + strings.Repeat("x", size) + `"}`
	return esm.StreamRecord{Seq: shardlog.Seq(strconv.Itoa(100 + i)), Event: json.RawMessage(event)}
}

// A StreamRecord encodes as its Event: the record as the function receives
// it (contract 7).
func TestStreamRecordMarshalsAsItsEvent(t *testing.T) {
	t.Parallel()
	r := streamRecord(1, 3)
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, r.Event) {
		t.Errorf("json.Marshal(record) = %s, want %s", got, r.Event)
	}
}

// A stream batch never takes the event past 6 MB unless it holds a single
// record, keeps its records in the order they were added, refuses a record
// without changing, and its Event is exactly {"Records":[...]} over the
// records' events, of the size reported ("The payload size reaches 6 MB",
// aws-doc lambda-stream-sources.md 4.2; counted over the serialized event,
// inferred, ESMS-18, a clause of the CreateEventSourceMapping coverage note).
func TestStreamBatchProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		var b esm.Batch[esm.StreamRecord]
		var held []esm.StreamRecord
		for i, n := range rapid.SliceOfN(rapid.IntRange(0, 2<<20), 1, 20).Draw(t, "sizes") {
			r := streamRecord(i, n)
			before, size := b.Len(), b.Size()
			ok, err := b.Add(r)
			if err != nil {
				t.Fatal(err)
			}
			if !ok && before == 0 {
				t.Fatal("an empty batch refused a record")
			}
			if ok {
				held = append(held, r)
			} else if b.Len() != before || b.Size() != size {
				t.Fatalf("a refused record changed the batch: %d records %d bytes, was %d, %d", b.Len(), b.Size(), before, size)
			}
		}
		event := b.Event()
		if len(event) != b.Size() {
			t.Fatalf("Event is %d bytes, Size says %d", len(event), b.Size())
		}
		if b.Len() > 1 && len(event) > esm.MaxPayload {
			t.Fatalf("%d records make %d bytes, more than %d", b.Len(), len(event), esm.MaxPayload)
		}
		parts := make([]string, len(held))
		for i, r := range held {
			parts[i] = string(r.Event)
		}
		if want := `{"Records":[` + strings.Join(parts, ",") + `]}`; string(event) != want {
			t.Fatalf("Event is not the held records' events in order")
		}
		if !slices.EqualFunc(b.Records(), held, func(a, b esm.StreamRecord) bool { return a.Seq == b.Seq }) || b.Len() != len(held) {
			t.Fatalf("Records %d, want %d in order", b.Len(), len(held))
		}
	})
}

func arn(t *testing.T, s string) aws.ARN {
	t.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func instant(t *testing.T, s string) clock.Instant {
	t.Helper()
	w, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return clock.FromWire(w)
}

func failedBatch(t *testing.T, cond esm.FailCondition) esm.FailedBatch {
	t.Helper()
	return esm.FailedBatch{
		RequestID:   "c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5",
		FunctionARN: "arn:aws:lambda:us-east-2:123456789012:function:myfunction",
		Condition:   cond,
		InvokeCount: 4,
		Timestamp:   instant(t, "2019-11-14T00:38:06.021Z"),
		Stream:      arn(t, "arn:aws:dynamodb:us-east-2:123456789012:table/mytable/stream/2019-11-14T00:04:06.388"),
		Shard:       "shardId-00000001573689847184-864758bb",
		First:       esm.StreamRecord{Seq: "800000000003126276362", Arrival: instant(t, "2019-11-14T00:13:19.512Z")},
		Last:        esm.StreamRecord{Seq: "800000000003126276363", Arrival: instant(t, "2019-11-14T00:13:20.999Z")},
		Size:        2,
	}
}

// The on-failure record for a DynamoDB stream, golden: compact JSON in the
// documented key order (requestContext, responseContext, version, timestamp,
// DDBStreamBatchInfo; lambda-stream-sources.md 12.6 item 1 and 5.6, aws-doc
// kinesis-on-failure, recorded LS-D:L419-L449), "version" "1.0", a
// millisecond Z timestamp and second-precision Z arrival times for DynamoDB
// (aws-doc ddb-errors "2019-11-14T00:13:19Z"; recorded LS-D:L436-L444),
// floored as ApproximateCreationDateTime is ("rounded down to the closest
// second", dynamodb-streams.md 7.3, aws-doc).
func TestFailureRecordRetryAttemptsExhausted(t *testing.T) {
	t.Parallel()
	b := failedBatch(t, esm.RetryAttemptsExhausted{StatusCode: 200, ExecutedVersion: "$LATEST", FunctionError: "Unhandled"})
	want := `{"requestContext":{"requestId":"c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5","functionArn":"arn:aws:lambda:us-east-2:123456789012:function:myfunction","condition":"RetryAttemptsExhausted","approximateInvokeCount":4},` +
		`"responseContext":{"statusCode":200,"executedVersion":"$LATEST","functionError":"Unhandled"},` +
		`"version":"1.0","timestamp":"2019-11-14T00:38:06.021Z",` +
		`"DDBStreamBatchInfo":{"shardId":"shardId-00000001573689847184-864758bb","startSequenceNumber":"800000000003126276362","endSequenceNumber":"800000000003126276363",` +
		`"approximateArrivalOfFirstRecord":"2019-11-14T00:13:19Z","approximateArrivalOfLastRecord":"2019-11-14T00:13:20Z","batchSize":2,` +
		`"streamArn":"arn:aws:dynamodb:us-east-2:123456789012:table/mytable/stream/2019-11-14T00:04:06.388"}}`
	if got := string(esm.FailureRecord(esm.DynamoDBStream{}, b)); got != want {
		t.Errorf("FailureRecord =\n%s\nwant\n%s", got, want)
	}
}

// After a reported partial failure functionError is null (recorded
// LS-D:L2265; lambda-stream-sources.md 5.6).
func TestFailureRecordReportedFailureHasNullFunctionError(t *testing.T) {
	t.Parallel()
	b := failedBatch(t, esm.RetryAttemptsExhausted{StatusCode: 200, ExecutedVersion: "$LATEST"})
	got := string(esm.FailureRecord(esm.DynamoDBStream{}, b))
	if want := `"responseContext":{"statusCode":200,"executedVersion":"$LATEST","functionError":null}`; !strings.Contains(got, want) {
		t.Errorf("FailureRecord = %s, want it to hold %s", got, want)
	}
}

// A RecordAgeExceeded record has no responseContext at all (recorded
// LS-K:L3212-L3219; lambda-stream-sources.md 5.6).
func TestFailureRecordRecordAgeExceeded(t *testing.T) {
	t.Parallel()
	b := failedBatch(t, esm.RecordAgeExceeded{})
	b.InvokeCount, b.Size, b.Last = 1, 1, b.First
	want := `{"requestContext":{"requestId":"c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5","functionArn":"arn:aws:lambda:us-east-2:123456789012:function:myfunction","condition":"RecordAgeExceeded","approximateInvokeCount":1},` +
		`"version":"1.0","timestamp":"2019-11-14T00:38:06.021Z",` +
		`"DDBStreamBatchInfo":{"shardId":"shardId-00000001573689847184-864758bb","startSequenceNumber":"800000000003126276362","endSequenceNumber":"800000000003126276362",` +
		`"approximateArrivalOfFirstRecord":"2019-11-14T00:13:19Z","approximateArrivalOfLastRecord":"2019-11-14T00:13:19Z","batchSize":1,` +
		`"streamArn":"arn:aws:dynamodb:us-east-2:123456789012:table/mytable/stream/2019-11-14T00:04:06.388"}}`
	if got := string(esm.FailureRecord(esm.DynamoDBStream{}, b)); got != want {
		t.Errorf("FailureRecord =\n%s\nwant\n%s", got, want)
	}
}

// An SQS mapping has no on-failure record (contract 7: nil for SQS;
// lambda-events.md 1.3: a destination on an SQS mapping is refused).
func TestFailureRecordNilForSQS(t *testing.T) {
	t.Parallel()
	if got := esm.FailureRecord(esm.SQS{}, failedBatch(t, esm.RecordAgeExceeded{})); got != nil {
		t.Errorf("FailureRecord(SQS) = %s, want nil", got)
	}
}

// The stream poller's three switches, each inferred with its Ref, Record,
// Ops and NoScenario, read from the values StreamFidelity holds; flipping
// each changes only its own Value (spec Fidelity switches; contract 9;
// lambda-stream-sources.md 12.7).
func TestStreamSwitches(t *testing.T) {
	t.Parallel()
	type row struct {
		name, ref, record string
		ops               []string
		def, flipped      string
	}
	rows := []row{
		{
			"lambda.esm-stream.latest-anchor", "docs/research/lambda-stream-sources.md#32-startingposition-and-startingpositiontimestamp", "ESMS-17",
			[]string{"lambda.CreateEventSourceMapping"},
			"create-instant", "first-read",
		},
		{
			"lambda.esm-stream.retry-delay", "docs/research/lambda-stream-sources.md#51-what-blocks-a-shard-and-what-counts-as-a-retry", "ESMS-06",
			[]string{"lambda.CreateEventSourceMapping", "lambda.UpdateEventSourceMapping"},
			"poll-interval", "immediate",
		},
		{
			"lambda.esm-stream.partial-resend-budget", "docs/research/lambda-stream-sources.md#55-reportbatchitemfailures", "ESMS-08",
			[]string{"lambda.CreateEventSourceMapping", "lambda.UpdateEventSourceMapping"},
			"restart", "shared",
		},
	}
	byName := func(es []fidelity.Entry) map[string]fidelity.Entry {
		m := map[string]fidelity.Entry{}
		for _, e := range es {
			m[e.Name] = e
		}
		return m
	}
	defaults := byName(esm.StreamSwitches(esm.StreamFidelity{}))
	if len(defaults) != len(rows) {
		t.Errorf("StreamSwitches lists %d switches, want %d", len(defaults), len(rows))
	}
	for _, r := range rows {
		e, ok := defaults[r.name]
		if !ok {
			t.Errorf("%s: not listed", r.name)
			continue
		}
		if e.Value != r.def || e.Label != evidence.Inferred || e.Ref != r.ref || e.Record != r.record || !slices.Equal(e.Ops, r.ops) {
			t.Errorf("%s = %+v, want value %q, inferred, ref %q, record %q, ops %v", r.name, e, r.def, r.ref, r.record, r.ops)
		}
		ns, ok := e.Settles.(fidelity.NoScenario)
		if want := "unrecorded: " + r.record + " is queued"; !ok || ns.Reason != want {
			t.Errorf("%s Settles = %#v, want NoScenario %q", r.name, e.Settles, want)
		}
	}
	flipped := byName(esm.StreamSwitches(esm.StreamFidelity{LatestAnchor: esm.AnchorFirstRead, RetryDelay: esm.RetryImmediately, ResendBudget: esm.ResendShared}))
	for _, r := range rows {
		if got := flipped[r.name].Value; got != r.flipped {
			t.Errorf("%s flipped = %q, want %q", r.name, got, r.flipped)
		}
	}
	for _, s := range []struct{ got, want string }{
		{esm.AnchorCreateInstant.String(), "create-instant"},
		{esm.AnchorFirstRead.String(), "first-read"},
		{esm.LatestAnchor(9).String(), "LatestAnchor(9)"},
		{esm.RetryAfterPollInterval.String(), "poll-interval"},
		{esm.RetryImmediately.String(), "immediate"},
		{esm.RetryDelay(9).String(), "RetryDelay(9)"},
		{esm.ResendRestart.String(), "restart"},
		{esm.ResendShared.String(), "shared"},
		{esm.ResendBudget(9).String(), "ResendBudget(9)"},
	} {
		if s.got != s.want {
			t.Errorf("String() = %q, want %q", s.got, s.want)
		}
	}
}

// The poll-interval value for DynamoDB is the documented base rate of four
// polls a second (lambda-stream-sources.md 4.1, aws-doc with-ddb).
func TestDynamoDBRetryDelay(t *testing.T) {
	t.Parallel()
	if esm.DynamoDBRetryDelay != time.Second/4 {
		t.Errorf("DynamoDBRetryDelay = %v, want a quarter second", esm.DynamoDBRetryDelay)
	}
}
