package dynamodb_test

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// labelOf is a stream label: the instant in UTC to the millisecond,
// yyyy-MM-ddTHH:mm:ss.SSS with no zone (dynamodb-streams.md 4.1, aws-doc
// examples; Terraform's acceptance pattern against AWS).
func labelOf(at clock.Instant) string {
	return at.Wire().UTC().Format("2006-01-02T15:04:05.000")
}

// streamARNOf is the documented stream ARN form in the caller's account
// and region (dynamodb-streams.md 4.1, aws-doc).
func streamARNOf(c *caller, table, label string) string {
	return "arn:aws:dynamodb:" + c.Region + ":" + c.Account + ":table/" + table + "/stream/" + label
}

// shardIDOf is understudy's deterministic shard id in AWS's form
// shardId-<20 digits>-<8 hex> (dynamodb-streams.md 4.2, aws-doc examples):
// the label instant's Unix milliseconds and the CRC-32 of the stream ARN.
func shardIDOf(arn string, label clock.Instant) string {
	return fmt.Sprintf("shardId-%020d-%08x", label.Wire().UnixMilli(), crc32.ChecksumIEEE([]byte(arn)))
}

// decimal21 is dynamodb.streams.sequence-number's default value, decimal-21
// (inferred, DDBS-09): the numbers it mints are read from the counter the
// switch names, never written out here.
var decimal21 = shardlog.DecimalCounter{Digits: 21}

func (h *streamsHarness) createStreamTable(table, view string) map[string]any {
	h.Helper()
	return h.mustCall("CreateTable", `{"TableName":"`+table+`","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],`+
		`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST",`+
		`"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"`+view+`"}}`)
}

// setStream enables the Orders table's stream with view, or disables it
// when view is "".
func (h *streamsHarness) setStream(view string) response {
	h.Helper()
	spec := `{"StreamEnabled":false}`
	if view != "" {
		spec = `{"StreamEnabled":true,"StreamViewType":"` + view + `"}`
	}
	return h.call("UpdateTable", `{"TableName":"Orders","StreamSpecification":`+spec+`}`)
}

type streamDescription struct {
	StreamArn               string
	StreamLabel             string
	StreamStatus            string
	StreamViewType          string
	CreationRequestDateTime json.Number
	TableName               string
	KeySchema               []map[string]string
	Shards                  []struct {
		ShardID             string  `json:"ShardId"`
		ParentShardID       *string `json:"ParentShardId"`
		SequenceNumberRange struct {
			StartingSequenceNumber string
			EndingSequenceNumber   *string
		}
	}
	LastEvaluatedShardID *string `json:"LastEvaluatedShardId"`
}

func (h *streamsHarness) describe(arn string) streamDescription {
	h.Helper()
	var out struct{ StreamDescription streamDescription }
	h.ok200(h.streamsCall("DescribeStream", `{"StreamArn":"`+arn+`"}`, defaultCaller()), &out)
	return out.StreamDescription
}

func (h *streamsHarness) iterator(arn, shard, kind, seq string) string {
	h.Helper()
	body := `{"StreamArn":"` + arn + `","ShardId":"` + shard + `","ShardIteratorType":"` + kind + `"`
	if seq != "" {
		body += `,"SequenceNumber":"` + seq + `"`
	}
	var out struct{ ShardIterator string }
	h.ok200(h.streamsCall("GetShardIterator", body+"}", defaultCaller()), &out)
	return out.ShardIterator
}

// gotRecord is a GetRecords record (dynamodb-streams.md 7.1).
type gotRecord struct {
	AwsRegion string `json:"awsRegion"`
	Dynamodb  struct {
		ApproximateCreationDateTime json.Number
		Keys                        item
		NewImage                    item
		OldImage                    item
		SequenceNumber              string
		SizeBytes                   int
		StreamViewType              string
	} `json:"dynamodb"`
	EventID      string            `json:"eventID"`
	EventName    string            `json:"eventName"`
	EventSource  string            `json:"eventSource"`
	EventVersion string            `json:"eventVersion"`
	UserIdentity map[string]string `json:"userIdentity"`
}

type recordsPage struct {
	Records           []gotRecord
	NextShardIterator *string
}

func (h *streamsHarness) records(it string, limit int) recordsPage {
	h.Helper()
	body := `{"ShardIterator":"` + it + `"`
	if limit > 0 {
		body += fmt.Sprintf(`,"Limit":%d`, limit)
	}
	var out recordsPage
	h.ok200(h.streamsCall("GetRecords", body+"}", defaultCaller()), &out)
	return out
}

// allRecords reads stream's one shard from TRIM_HORIZON to its end.
func (h *streamsHarness) allRecords(arn string) []gotRecord {
	h.Helper()
	d := h.describe(arn)
	if len(d.Shards) != 1 {
		h.Fatalf("%s has %d shards, want 1", arn, len(d.Shards))
	}
	it := h.iterator(arn, d.Shards[0].ShardID, "TRIM_HORIZON", "")
	var out []gotRecord
	for range 100 {
		p := h.records(it, 0)
		out = append(out, p.Records...)
		if p.NextShardIterator == nil || len(p.Records) == 0 {
			return out
		}
		it = *p.NextShardIterator
	}
	h.Fatal("the shard did not end")
	return nil
}

func (h *streamsHarness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// put writes the item id with v "1" to table.
func (h *streamsHarness) put(table, id string) {
	h.Helper()
	h.mustCall("PutItem", `{"TableName":"`+table+`","Item":{"id":{"S":"`+id+`"},"v":{"S":"1"}}}`)
}

// tableOf is the table description of a CreateTable, UpdateTable or
// DescribeTable answer.
func tableOf(desc map[string]any) map[string]any {
	for _, k := range []string{"TableDescription", "Table"} {
		if td, isMap := desc[k].(map[string]any); isMap {
			return td
		}
	}
	return nil
}

// latest returns a table description's LatestStreamArn and
// LatestStreamLabel, "" for one it lacks.
func latest(desc map[string]any) (arn, label string) {
	td := tableOf(desc)
	if s, isString := td["LatestStreamArn"].(string); isString {
		arn = s
	}
	if s, isString := td["LatestStreamLabel"].(string); isString {
		label = s
	}
	return arn, label
}

func epochSeconds(n json.Number) *big.Rat {
	r, ok := new(big.Rat).SetString(n.String())
	if !ok {
		return nil
	}
	return r
}

// CreateTable mints the stream's label at its admission instant on the
// emulator clock and answers understudy's LatestStreamArn and
// LatestStreamLabel in the caller's account and region; DescribeTable
// carries the same on every read. Nothing of Local's region or account
// shows (dynamodb-streams.md 11.1 item 6, 4.1, 9.3, 11.8).
func TestCreateTableMintsTheStreamLabel(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		h.advance(1234 * time.Millisecond)
		at := h.Clock.Now()
		arn, label := latest(h.createStreamTable("Orders", "NEW_AND_OLD_IMAGES"))
		if want := labelOf(at); label != want {
			h.Errorf("LatestStreamLabel = %q, want %q (the admission instant)", label, want)
		}
		if want := streamARNOf(defaultCaller(), "Orders", labelOf(at)); arn != want {
			h.Errorf("LatestStreamArn = %q, want %q", arn, want)
		}
		h.advance(time.Hour)
		for range 2 {
			r := h.call("DescribeTable", `{"TableName":"Orders"}`)
			if a, l := latest(decode(h.harness, r)); a != arn || l != label {
				h.Errorf("DescribeTable = %s, %s; want %s, %s on every read", a, l, arn, label)
			}
			if strings.Contains(r.Body, "ddblocal") || strings.Contains(r.Body, "000000000000") {
				h.Errorf("DescribeTable carries Local's region or account: %s", r.Body)
			}
		}
	})
}

// A table never streamed carries neither member (Terraform's acceptance
// tests against AWS: stream_arn "" for such a table, dynamodb-streams.md
// 9.1).
func TestTableWithoutStreamHasNoLatestStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		h.mustCall("CreateTable", `{"TableName":"Plain","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST"}`)
		if a, l := latest(h.mustCall("DescribeTable", `{"TableName":"Plain"}`)); a != "" || l != "" {
			h.Errorf("LatestStream = %q, %q; want neither", a, l)
		}
	})
}

// A disable keeps LatestStreamArn and LatestStreamLabel and drops
// StreamSpecification (dynamodb-streams.md 5.3, 9.3); a re-enable on a
// frozen clock collides with the old label and takes it plus 1 ms
// (11.1 item 6), and both streams are listed, the newest first
// (6.1, aws-doc example).
func TestDisableKeepsLatestStreamAndReEnableMintsANewOne(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		at := h.Clock.Now()
		first, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		if r := h.setStream(""); r.Status != http.StatusOK {
			h.Fatalf("disable = %d %s", r.Status, r.Body)
		}
		d := h.mustCall("DescribeTable", `{"TableName":"Orders"}`)
		if a, _ := latest(d); a != first {
			h.Errorf("after a disable LatestStreamArn = %q, want %q", a, first)
		}
		if spec := tableOf(d)["StreamSpecification"]; spec != nil {
			h.Errorf("after a disable StreamSpecification = %v, want none", spec)
		}
		r := h.setStream("NEW_IMAGE")
		if r.Status != http.StatusOK {
			h.Fatalf("re-enable = %d %s", r.Status, r.Body)
		}
		second, label := latest(decode(h.harness, r))
		if want := labelOf(at.Add(time.Millisecond)); label != want {
			h.Errorf("re-enabled label = %q, want %q (1 ms after the colliding one)", label, want)
		}
		var out struct {
			Streams []struct{ StreamArn, TableName, StreamLabel string }
		}
		h.ok200(h.streamsCall("ListStreams", `{}`, defaultCaller()), &out)
		got := make([]string, len(out.Streams))
		for i, s := range out.Streams {
			got[i] = s.StreamArn
			if s.TableName != "Orders" || !strings.HasSuffix(s.StreamArn, "/stream/"+s.StreamLabel) {
				h.Errorf("stream %+v does not name its table and label", s)
			}
		}
		if want := []string{second, first}; !slices.Equal(got, want) {
			h.Errorf("ListStreams = %v, want %v, newest first", got, want)
		}
	})
}

// Local's enable and disable refusals carry AWS's texts (aws-recorded,
// dynamodb-streams.md 5.1: testdata/parity/dynamodb/DDBS-06/01-enable-again.json
// to 05-disable-with-view-type.json).
func TestUpdateTableStreamTextsTakeAWSWords(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		h.createStreamTable("Orders", "KEYS_ONLY")
		refused := func(name, spec, want string) {
			h.Helper()
			e := h.errorOf(h.call("UpdateTable", `{"TableName":"Orders","StreamSpecification":`+spec+`}`), http.StatusBadRequest)
			if e.Type != "com.amazon.coral.validate#ValidationException" || e.text() != want {
				h.Errorf("%s: %+v, want ValidationException %q", name, e, want)
			}
		}
		refused("enable twice", `{"StreamEnabled":true,"StreamViewType":"NEW_IMAGE"}`, "Table already has an enabled stream: TableName: Orders")
		h.setStream("")
		refused("disable twice", `{"StreamEnabled":false}`, "Table has no stream to disable: TableName: Orders")
		refused("enable without a view type", `{"StreamEnabled":true}`,
			"One or more parameter values were invalid: If stream is being enabled then UpdateViewType is required")
		refused("disable with a view type", `{"StreamEnabled":false,"StreamViewType":"KEYS_ONLY"}`,
			"One or more parameter values were invalid: If stream is being disabled, then UpdateViewType must not be specified")
	})
}

// StreamSpecification is validated before the forward: Local accepts a
// StreamViewType of BOGUS (observed, dynamodb-streams.md 5.1, 8.3 row 11),
// AWS's model refuses it as an enum violation.
func TestStreamViewTypeIsValidatedBeforeLocal(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		before := h.localSaw("CreateTable")
		r := h.call("CreateTable", `{"TableName":"Orders","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST",`+
			`"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"BOGUS"}}`)
		if e := h.errorOf(r, http.StatusBadRequest); !strings.HasSuffix(e.Type, "ValidationException") {
			h.Errorf("BOGUS view type: %+v, want ValidationException", e)
		}
		if h.localSaw("CreateTable") != before {
			h.Error("a CreateTable with a BOGUS view type reached Local")
		}
	})
}

// ListStreams serves the caller's account and region from understudy's
// log: TableName filters by exact name, a TableName naming no table gives
// {"Streams":[]} (aws-recorded, DDBS-03), Limit pages with
// LastEvaluatedStreamArn set whenever the page is full, the last page too
// (aws-recorded, DDBS-08: 03-list-limit-1.json, 04-list-page-2.json), and
// ExclusiveStartStreamArn continues after it (dynamodb-streams.md 6.1;
// order newest first, aws-recorded DDBS-08, ties by ARN).
func TestListStreams(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		a, _ := latest(h.createStreamTable("Alpha", "KEYS_ONLY"))
		h.advance(time.Second)
		b, _ := latest(h.createStreamTable("Bravo", "KEYS_ONLY"))
		h.advance(time.Second)
		c, _ := latest(h.createStreamTable("Charlie", "KEYS_ONLY"))
		type page struct {
			Streams []struct {
				StreamArn, TableName, StreamLabel string
			}
			LastEvaluatedStreamArn *string
		}
		arns := func(p page) []string {
			out := make([]string, len(p.Streams))
			for i, s := range p.Streams {
				out[i] = s.StreamArn
			}
			return out
		}
		var p page
		h.ok200(h.streamsCall("ListStreams", `{"Limit":2}`, defaultCaller()), &p)
		if want := []string{c, b}; !slices.Equal(arns(p), want) || p.LastEvaluatedStreamArn == nil || *p.LastEvaluatedStreamArn != b {
			h.Errorf("first page = %v, last %v; want %v, last %s", arns(p), p.LastEvaluatedStreamArn, want, b)
		}
		p = page{}
		h.ok200(h.streamsCall("ListStreams", `{"Limit":2,"ExclusiveStartStreamArn":"`+b+`"}`, defaultCaller()), &p)
		if want := []string{a}; !slices.Equal(arns(p), want) || p.LastEvaluatedStreamArn != nil {
			h.Errorf("second page = %v, last %v; want %v and no LastEvaluatedStreamArn", arns(p), p.LastEvaluatedStreamArn, want)
		}
		p = page{}
		h.ok200(h.streamsCall("ListStreams", `{"Limit":1,"ExclusiveStartStreamArn":"`+b+`"}`, defaultCaller()), &p)
		if want := []string{a}; !slices.Equal(arns(p), want) || p.LastEvaluatedStreamArn == nil || *p.LastEvaluatedStreamArn != a {
			h.Errorf("a full last page = %v, last %v; want %v, last %s", arns(p), p.LastEvaluatedStreamArn, want, a)
		}
		p = page{}
		h.ok200(h.streamsCall("ListStreams", `{}`, defaultCaller()), &p)
		if p.LastEvaluatedStreamArn != nil {
			h.Errorf("an unpaged list carries LastEvaluatedStreamArn %s, want none", *p.LastEvaluatedStreamArn)
		}
		p = page{}
		h.ok200(h.streamsCall("ListStreams", `{"TableName":"Bravo"}`, defaultCaller()), &p)
		if want := []string{b}; !slices.Equal(arns(p), want) {
			h.Errorf("TableName Bravo = %v, want %v", arns(p), want)
		}
		r := h.streamsCall("ListStreams", `{"TableName":"Nope"}`, defaultCaller())
		if r.Status != http.StatusOK || r.Body != `{"Streams":[]}` {
			h.Errorf("a TableName naming no table = %d %s, want 200 {\"Streams\":[]}", r.Status, r.Body)
		}
		r = h.streamsCall("ListStreams", `{}`, &caller{Account: "222222222222", Region: "us-east-1"})
		if r.Status != http.StatusOK || r.Body != `{"Streams":[]}` {
			h.Errorf("another account's ListStreams = %d %s, want none of these streams", r.Status, r.Body)
		}
		// dynamodb-streams.md 6.1 and 6.5: Local's text for Limit 101, observed.
		e := h.errorOf(h.streamsCall("ListStreams", `{"Limit":101}`, defaultCaller()), http.StatusBadRequest)
		if e.text() != "One or more parameter values were invalid: Limit cannot be greater than the maximum allowed size: 100" || !strings.HasSuffix(e.Type, "ValidationException") {
			h.Errorf("Limit 101 = %+v", e)
		}
	})
}

// ListStreams' Limit and TableName constraint failures use the generated
// Coral text, which quotes the value where AWS's omits it (aws-recorded,
// dynamodb-streams.md 6.5: DDBS-02), so ListStreams is Partial with the
// same clause as the other Streams operations, not Full.
func TestListStreamsNotesTheConstraintTexts(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		const clause = "a constraint violation's text quotes the value (\"Value '0' at 'limit'\"), where AWS's omits it (\"Value at 'limit'\")"
		i := slices.IndexFunc(dynamodbstreamsapi.Service().Operations, func(op *registry.Operation) bool { return op.ID.Name == "ListStreams" })
		if i < 0 {
			h.Fatal("dynamodbstreamsapi has no ListStreams operation")
		}
		status := h.Kernel.Bindings().Status(dynamodbstreamsapi.Service().Operations[i])
		bound, ok := status.(kernel.Bound)
		if !ok {
			h.Fatalf("ListStreams is %#v, want bound", status)
		}
		if p, ok := bound.Coverage.(registry.Partial); !ok || !strings.Contains(p.Note, clause) {
			h.Errorf("ListStreams coverage = %#v, want Partial with the note %q", bound.Coverage, clause)
		}
	})
}

// DescribeStream answers one open shard numbered by the sequence-number
// switch, the table's key schema and the label instant as
// CreationRequestDateTime; ExclusiveStartShardId naming the shard or a
// CHILD_SHARDS filter give no shards, one naming no shard gives every
// shard (DynamoDB Local's observed answers, dynamodb-streams.md 5.6; the
// recorded open-shard answer DDB-SNAP:L708-L735 is one shard with only
// StartingSequenceNumber and ENABLED).
func TestDescribeStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		at := h.Clock.Now()
		arn, label := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		d := h.describe(arn)
		if d.StreamArn != arn || d.StreamLabel != label || d.StreamStatus != "ENABLED" || d.StreamViewType != "NEW_IMAGE" || d.TableName != "Orders" {
			h.Errorf("StreamDescription = %+v", d)
		}
		if got := epochSeconds(d.CreationRequestDateTime); got == nil || got.Cmp(new(big.Rat).SetFrac64(at.Wire().UnixMilli(), 1000)) != 0 {
			h.Errorf("CreationRequestDateTime = %s, want the label instant %s", d.CreationRequestDateTime, at)
		}
		if want := []map[string]string{{"AttributeName": "id", "KeyType": "HASH"}}; !slices.EqualFunc(d.KeySchema, want, func(a, b map[string]string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
			h.Errorf("KeySchema = %v, want %v", d.KeySchema, want)
		}
		if len(d.Shards) != 1 {
			h.Fatalf("Shards = %+v, want one", d.Shards)
		}
		s := d.Shards[0]
		if want := shardIDOf(arn, at); s.ShardID != want {
			h.Errorf("ShardId = %q, want %q", s.ShardID, want)
		}
		if want := string(decimal21.Start(shardlog.ShardInfo{})); s.SequenceNumberRange.StartingSequenceNumber != want || s.SequenceNumberRange.EndingSequenceNumber != nil || s.ParentShardID != nil {
			h.Errorf("shard %+v, want open from %s with no parent (dynamodb.streams.sequence-number decimal-21)", s, want)
		}
		if d.LastEvaluatedShardID != nil {
			h.Errorf("LastEvaluatedShardId = %v, want none", *d.LastEvaluatedShardID)
		}
		shards := func(body string) int {
			var out struct{ StreamDescription streamDescription }
			h.ok200(h.streamsCall("DescribeStream", body, defaultCaller()), &out)
			return len(out.StreamDescription.Shards)
		}
		if n := shards(`{"StreamArn":"` + arn + `","ExclusiveStartShardId":"` + s.ShardID + `"}`); n != 0 {
			h.Errorf("ExclusiveStartShardId of the shard gave %d shards, want 0", n)
		}
		if n := shards(`{"StreamArn":"` + arn + `","ExclusiveStartShardId":"shardId-00000000000000000000-00000000"}`); n != 1 {
			h.Errorf("ExclusiveStartShardId naming no shard gave %d shards, want every shard", n)
		}
		if n := shards(`{"StreamArn":"` + arn + `","ShardFilter":{"Type":"CHILD_SHARDS","ShardId":"` + s.ShardID + `"}}`); n != 0 {
			h.Errorf("CHILD_SHARDS gave %d shards, want 0", n)
		}
		// dynamodb-streams.md 6.5: Local's text for Limit 101, observed.
		if e := h.errorOf(h.streamsCall("DescribeStream", `{"StreamArn":"`+arn+`","Limit":101}`, defaultCaller()), http.StatusBadRequest); e.text() != "One or more parameter values were invalid: Limit cannot be greater than the maximum: 100" {
			h.Errorf("Limit 101 = %+v", e)
		}
		// The recorded not-found text (third-party-recorded, STREAMS-SNAP:L90-L104).
		missing := streamARNOf(defaultCaller(), "Nope", "2000-01-01T00:00:00.000")
		e := h.errorOf(h.streamsCall("DescribeStream", `{"StreamArn":"`+missing+`"}`, defaultCaller()), http.StatusBadRequest)
		if e.Type != "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException" || e.text() != "Requested resource not found: Stream: "+missing+" not found" {
			h.Errorf("missing stream = %+v", e)
		}
	})
}

// A write to a stream-enabled table is in the stream when the write
// answers: PutItem, UpdateItem and DeleteItem give INSERT, MODIFY and
// REMOVE in write order with the images of the view type as Local sent
// them, Local's SizeBytes, and understudy's own stamping: the write's
// admission instant floored to the second, the caller's region, a 32-digit
// lower-case hex eventID (dynamodb-streams.md 4.5, aws-doc), eventVersion
// 1.1 (third-party-recorded, DDB-SNAP:L761), and sequence numbers from the
// sequence-number switch (dynamodb-streams.md 7.1-7.3, 11.1 items 2, 4.3).
func TestWritesFeedTheStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "NEW_AND_OLD_IMAGES"))
		h.advance(1500 * time.Millisecond)
		t1 := h.Clock.Now()
		h.put("Orders", "a")
		h.advance(2700 * time.Millisecond)
		t2 := h.Clock.Now()
		h.mustCall("UpdateItem", `{"TableName":"Orders","Key":{"id":{"S":"a"}},"UpdateExpression":"SET v = :v","ExpressionAttributeValues":{":v":{"S":"2"}}}`)
		h.advance(time.Second)
		t3 := h.Clock.Now()
		h.mustCall("DeleteItem", `{"TableName":"Orders","Key":{"id":{"S":"a"}}}`)
		local := h.ls.records("Orders")
		got := h.allRecords(arn)
		if len(got) != 3 || len(local) != 3 {
			h.Fatalf("%d records (Local %d), want 3", len(got), len(local))
		}
		hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
		seq := shardlog.Seq("")
		ids := map[string]bool{}
		for i, r := range got {
			want := []string{"INSERT", "MODIFY", "REMOVE"}[i]
			at := []clock.Instant{t1, t2, t3}[i]
			seq = decimal21.Next(shardlog.ShardInfo{}, seq, at)
			if r.EventName != want || r.EventSource != "aws:dynamodb" || r.EventVersion != "1.1" || r.AwsRegion != "us-east-1" {
				h.Errorf("record %d = %+v, want %s from aws:dynamodb 1.1 in us-east-1", i, r, want)
			}
			if r.Dynamodb.SequenceNumber != string(seq) {
				h.Errorf("record %d SequenceNumber = %s, want %s (dynamodb.streams.sequence-number decimal-21)", i, r.Dynamodb.SequenceNumber, seq)
			}
			if !hex32.MatchString(r.EventID) || ids[r.EventID] {
				h.Errorf("record %d eventID = %q, want 32 new lower-case hex digits", i, r.EventID)
			}
			ids[r.EventID] = true
			if r.Dynamodb.SizeBytes != local[i].SizeBytes || r.Dynamodb.StreamViewType != "NEW_AND_OLD_IMAGES" {
				h.Errorf("record %d SizeBytes %d view %s, want Local's %d and NEW_AND_OLD_IMAGES", i, r.Dynamodb.SizeBytes, r.Dynamodb.StreamViewType, local[i].SizeBytes)
			}
			if !sameItem(r.Dynamodb.Keys, local[i].Keys) || !sameItem(r.Dynamodb.NewImage, local[i].NewImage) || !sameItem(r.Dynamodb.OldImage, local[i].OldImage) {
				h.Errorf("record %d images = %+v, want Local's %+v", i, r.Dynamodb, local[i])
			}
			if want := big.NewRat(at.Wire().Unix(), 1); epochSeconds(r.Dynamodb.ApproximateCreationDateTime).Cmp(want) != 0 {
				h.Errorf("record %d ApproximateCreationDateTime = %s, want %s (the write's instant floored to the second)", i, r.Dynamodb.ApproximateCreationDateTime, want.FloatString(0))
			}
			if r.UserIdentity != nil {
				h.Errorf("record %d userIdentity = %v, want none on a client's write (DDB-SNAP:L745-L762)", i, r.UserIdentity)
			}
		}
	})
}

// The feed drains Local's stream from the saved position: TRIM_HORIZON
// before the first record, then AFTER_SEQUENCE_NUMBER with Local's last
// number, GetRecords with Limit 1000; a write that changes
// nothing appends nothing (dynamodb-streams.md 7.6, aws-doc and recorded).
func TestFeedDrainsFromTheSavedPosition(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.put("Orders", "a")
		h.put("Orders", "a")
		h.put("Orders", "b")
		var types []string
		for _, f := range h.local.requests() {
			if f.Op != streamsTarget+"GetShardIterator" {
				continue
			}
			var in struct{ ShardIteratorType, SequenceNumber string }
			if err := json.Unmarshal([]byte(f.Body), &in); err != nil {
				h.Fatal(err)
			}
			types = append(types, in.ShardIteratorType+" "+in.SequenceNumber)
		}
		local := h.ls.records("Orders")
		if len(types) < 3 || types[0] != "TRIM_HORIZON " || types[len(types)-1] != "AFTER_SEQUENCE_NUMBER "+local[0].Seq {
			h.Errorf("Local iterators = %q, want TRIM_HORIZON first and AFTER_SEQUENCE_NUMBER %s for the last write", types, local[0].Seq)
		}
		for _, f := range h.local.requests() {
			if f.Op == streamsTarget+"GetRecords" && !strings.Contains(f.Body, `"Limit":1000`) {
				h.Errorf("GetRecords to Local %s, want Limit 1000", f.Body)
			}
		}
		if got := h.allRecords(arn); len(got) != 2 {
			h.Errorf("%d records, want 2: the unchanged PutItem emits none", len(got))
		}
	})
}

// BatchWriteItem, TransactWriteItems and a PartiQL statement feed every
// stream-enabled table they wrote; PartiQL drains each stream-enabled table
// of the scope without the statement being parsed (dynamodb-streams.md
// 11.1 item 2; recorded batch and transact sequences DDB-SNAP:L556-L654,
// L983-L1150).
func TestMultiTableWritesFeedEachStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		a, _ := latest(h.createStreamTable("Alpha", "KEYS_ONLY"))
		b, _ := latest(h.createStreamTable("Bravo", "KEYS_ONLY"))
		h.mustCall("BatchWriteItem", `{"RequestItems":{"Alpha":[{"PutRequest":{"Item":{"id":{"S":"1"}}}}],"Bravo":[{"PutRequest":{"Item":{"id":{"S":"2"}}}}]}}`)
		h.mustCall("TransactWriteItems", `{"TransactItems":[{"Put":{"TableName":"Alpha","Item":{"id":{"S":"3"}}}},{"Put":{"TableName":"Bravo","Item":{"id":{"S":"4"}}}}]}`)
		h.ls.statement = func(string) (string, item) { return "Bravo", item{"id": json.RawMessage(`{"S":"5"}`)} }
		h.mustCall("ExecuteStatement", `{"Statement":"INSERT INTO \"Bravo\" VALUE {'id': '5'}"}`)
		keys := func(rs []gotRecord) []string {
			out := make([]string, len(rs))
			for i, r := range rs {
				out[i] = string(r.Dynamodb.Keys["id"])
			}
			return out
		}
		if got, want := keys(h.allRecords(a)), []string{`{"S":"1"}`, `{"S":"3"}`}; !slices.Equal(got, want) {
			h.Errorf("Alpha's stream = %v, want %v", got, want)
		}
		if got, want := keys(h.allRecords(b)), []string{`{"S":"2"}`, `{"S":"4"}`, `{"S":"5"}`}; !slices.Equal(got, want) {
			h.Errorf("Bravo's stream = %v, want %v", got, want)
		}
	})
}

// A drain that fails leaves the write's answer as it was, appends nothing,
// is journaled, and keeps the write's instant: the next write drains both
// records, each stamped with its own write's instant
// (dynamodb-streams.md 11.1 item 3).
func TestFailedDrainRetriesAtTheNextWrite(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.ls.setFail(true)
		t1 := h.Clock.Now()
		h.put("Orders", "a")
		if failed := h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}); !slices.ContainsFunc(failed.Events, func(e journal.Event) bool {
			sc, isSC := e.Detail.(journal.StateChange)
			return isSC && sc.Resource.String() == arn && sc.To == "DrainFailed"
		}) {
			h.Error("a failed drain was not journaled as a StateChange to DrainFailed")
		}
		h.ls.setFail(false)
		h.advance(5 * time.Second)
		t2 := h.Clock.Now()
		h.put("Orders", "b")
		got := h.allRecords(arn)
		if len(got) != 2 {
			h.Fatalf("%d records, want both writes'", len(got))
		}
		for i, at := range []clock.Instant{t1, t2} {
			if want := big.NewRat(at.Wire().Unix(), 1); epochSeconds(got[i].Dynamodb.ApproximateCreationDateTime).Cmp(want) != 0 {
				h.Errorf("record %d at %s, want its write's %s", i, got[i].Dynamodb.ApproximateCreationDateTime, want.FloatString(0))
			}
		}
	})
}

// A failed drain is retried on an emulator timer without another write
// (dynamodb-streams.md 11.1 item 3: "retried at the next write or on a
// short emulator-clock timer"; understudy's fires after 1 s).
func TestFailedDrainRetriesOnTheEmulatorClock(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.ls.setFail(true)
		h.put("Orders", "a")
		h.ls.setFail(false)
		h.advance(time.Second)
		h.settle()
		if got := h.allRecords(arn); len(got) != 1 {
			h.Errorf("%d records a second after the failed drain, want 1", len(got))
		}
	})
}

// A StreamArn shorter than the model's 37 characters is "Invalid StreamArn",
// not the Coral length text (aws-recorded, dynamodb-streams.md 6.5:
// testdata/parity/dynamodb/DDBS-02/01-describe-short-arn.json).
func TestDescribeStreamShortARN(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		e := h.errorOf(h.streamsCall("DescribeStream", `{"StreamArn":"short"}`, defaultCaller()), http.StatusBadRequest)
		if e.Type != "com.amazon.coral.validate#ValidationException" || e.text() != "Invalid StreamArn" {
			h.Errorf("a short StreamArn: %+v, want ValidationException Invalid StreamArn", e)
		}
	})
}

// GetShardIterator's checks, in order, with AWS's texts
// (aws-recorded, dynamodb-streams.md 6.5: testdata/parity/dynamodb/DDBS-02/05-at-without-sequence.json,
// 06-trim-with-sequence.json, 09-non-numeric-sequence.json;
// testdata/parity/dynamodb/DDBS-01/02-iterator-missing-stream.json,
// 03-iterator-missing-shard.json; an AT_SEQUENCE_NUMBER request for a shard
// AWS had removed, testdata/parity/dynamodb/DDBS-15/01-at-old-sequence.json).
func TestGetShardIteratorRefusals(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		cases := []struct {
			name, body, typ, text string
		}{
			{
				"TRIM_HORIZON with a number", `{"StreamArn":"` + arn + `","ShardId":"` + shard + `","ShardIteratorType":"TRIM_HORIZON","SequenceNumber":"100000000000000000001"}`,
				"ValidationException", "One or more parameter values were invalid: SequenceNumber should not be passed for this iterator type",
			},
			{
				"AT_SEQUENCE_NUMBER without one", `{"StreamArn":"` + arn + `","ShardId":"` + shard + `","ShardIteratorType":"AT_SEQUENCE_NUMBER"}`,
				"ValidationException", "One or more parameter values were invalid: SequenceNumber cannot be null",
			},
			{
				"non-digit number", `{"StreamArn":"` + arn + `","ShardId":"` + shard + `","ShardIteratorType":"AT_SEQUENCE_NUMBER","SequenceNumber":"abcdefghijabcdefghijk"}`,
				"ValidationException", "Invalid SequenceNumber for the shard",
			},
			{
				"unknown stream", `{"StreamArn":"` + streamARNOf(defaultCaller(), "Nope", "2000-01-01T00:00:00.000") + `","ShardId":"` + shard + `","ShardIteratorType":"TRIM_HORIZON"}`,
				"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Requested resource not found: Stream does not exist",
			},
			{
				"unknown shard", `{"StreamArn":"` + arn + `","ShardId":"shardId-00000000000000000000-00000000","ShardIteratorType":"TRIM_HORIZON"}`,
				"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Requested resource not found: Shard does not exist",
			},
			{
				// understudy never removes a shard; this pins only the text
				// AWS answered for one it had removed, through the unknown
				// shard path above. Retention is TestRecordsAreTrimmedAt24Hours's.
				"DDBS-15's removed shard id at a sequence number", `{"StreamArn":"` + arn + `","ShardId":"shardId-00000001793723721237-7672acf5","ShardIteratorType":"AT_SEQUENCE_NUMBER","SequenceNumber":"12100000714580876880150"}`,
				"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Requested resource not found: Shard does not exist",
			},
		}
		for _, c := range cases {
			e := h.errorOf(h.streamsCall("GetShardIterator", c.body, defaultCaller()), http.StatusBadRequest)
			if !strings.HasSuffix(e.Type, c.typ) || e.text() != c.text {
				h.Errorf("%s: %+v, want %s %q", c.name, e, c.typ, c.text)
			}
		}
		// The pinned model's enum has no AT_TIMESTAMP (dynamodb-streams.md 3.5).
		e := h.errorOf(h.streamsCall("GetShardIterator", `{"StreamArn":"`+arn+`","ShardId":"`+shard+`","ShardIteratorType":"AT_TIMESTAMP"}`, defaultCaller()), http.StatusBadRequest)
		if !strings.HasSuffix(e.Type, "ValidationException") {
			h.Errorf("AT_TIMESTAMP: %+v, want ValidationException", e)
		}
	})
}

// The four iterator types read the shard as documented: TRIM_HORIZON from
// the oldest record, AT_ and AFTER_SEQUENCE_NUMBER at or right after a
// number, LATEST just after the newest, so a record written after LATEST
// was taken is on its next page (dynamodb-streams.md 6.3, aws-doc; 8.2:
// "a LATEST iterator taken before a PutItem returned the INSERT on the next
// GetRecords"). The iterator has the documented prefix (4.4).
func TestIteratorTypes(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		for _, id := range []string{"a", "b", "c"} {
			h.put("Orders", id)
		}
		all := h.allRecords(arn)
		ids := func(rs []gotRecord) string {
			var b strings.Builder
			for _, r := range rs {
				b.WriteString(string(r.Dynamodb.Keys["id"]))
			}
			return b.String()
		}
		trim := h.iterator(arn, shard, "TRIM_HORIZON", "")
		if !strings.HasPrefix(trim, arn+"|1|") || len(trim) > 2048 {
			h.Errorf("iterator %q, want %s|1|<token> of at most 2048 characters", trim, arn)
		}
		if got := ids(h.records(h.iterator(arn, shard, "AT_SEQUENCE_NUMBER", all[1].Dynamodb.SequenceNumber), 0).Records); got != ids(all[1:]) {
			h.Errorf("AT the second = %s, want %s", got, ids(all[1:]))
		}
		if got := ids(h.records(h.iterator(arn, shard, "AFTER_SEQUENCE_NUMBER", all[1].Dynamodb.SequenceNumber), 0).Records); got != ids(all[2:]) {
			h.Errorf("AFTER the second = %s, want %s", got, ids(all[2:]))
		}
		start := string(decimal21.Start(shardlog.ShardInfo{}))
		if got := ids(h.records(h.iterator(arn, shard, "AT_SEQUENCE_NUMBER", start), 0).Records); got != ids(all) {
			h.Errorf("AT StartingSequenceNumber = %s, want every record (DDB-TEST:L882-L889)", got)
		}
		lt := h.iterator(arn, shard, "LATEST", "")
		p := h.records(lt, 0)
		if len(p.Records) != 0 || p.NextShardIterator == nil {
			h.Fatalf("LATEST = %d records, next %v; want none and a next iterator", len(p.Records), p.NextShardIterator)
		}
		h.put("Orders", "d")
		if got := ids(h.records(*p.NextShardIterator, 0).Records); got != `{"S":"d"}` {
			h.Errorf("after LATEST = %s, want the new record", got)
		}
		paged := h.records(trim, 2)
		if len(paged.Records) != 2 || paged.NextShardIterator == nil {
			h.Errorf("Limit 2 = %d records, want 2 and a next iterator", len(paged.Records))
		}
	})
}

// GetRecords refuses an iterator that does not decode, or whose prefix
// names another stream, with "Invalid ShardIterator", and a Limit above
// 1000 with ValidationException, both AWS's (aws-recorded,
// dynamodb-streams.md 6.4, 6.5: DDBS-01/04-records-garbage.json,
// DDBS-02/11-records-limit-1001.json; 11.1 item 4).
func TestGetRecordsRefusals(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		other, _ := latest(h.createStreamTable("Other", "KEYS_ONLY"))
		it := h.iterator(arn, h.describe(arn).Shards[0].ShardID, "TRIM_HORIZON", "")
		_, token, _ := strings.Cut(it, "|1|")
		for name, bad := range map[string]string{
			"garbage":               "garbage",
			"another stream prefix": other + "|1|" + token,
			"Local's form":          arn + "|001|" + token,
		} {
			e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+bad+`"}`, defaultCaller()), http.StatusBadRequest)
			if e.Type != "com.amazon.coral.validate#ValidationException" || e.text() != "Invalid ShardIterator" {
				h.Errorf("%s: %+v, want com.amazon.coral.validate#ValidationException Invalid ShardIterator", name, e)
			}
		}
		e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+it+`","Limit":1001}`, defaultCaller()), http.StatusBadRequest)
		if !strings.HasSuffix(e.Type, "ValidationException") || e.text() != "One or more parameter values were invalid: Limit should be less than or equal to 1000" {
			h.Errorf("Limit 1001 = %+v", e)
		}
	})
}

// expiredText is AWS's expired-iterator message (aws-recorded,
// dynamodb-streams.md 4.4: testdata/parity/dynamodb/DDBS-04/04-unused-at-16min.json).
const expiredText = "Shard iterator has expired"

// An iterator expires exactly 15 minutes after its issue on the emulator
// clock ("A shard iterator expires 15 minutes after it is returned to the
// requester", aws-doc, dynamodb-streams.md 4.4), and a NextShardIterator
// gets its own 15 minutes (aws-recorded, DDBS-04: 05-next-from-14min.json).
// AWS's __type namespace for this error is com.amazonaws.dynamodb.streams.v20120810
// (DDBS-04), which understudy does not send yet: only the name is asserted.
func TestIteratorsExpireAfterFifteenMinutes(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		it := h.iterator(arn, h.describe(arn).Shards[0].ShardID, "TRIM_HORIZON", "")
		h.advance(15*time.Minute - time.Millisecond)
		next := h.records(it, 0).NextShardIterator
		if next == nil {
			h.Fatal("no NextShardIterator on an open shard")
		}
		h.advance(time.Millisecond)
		e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+it+`"}`, defaultCaller()), http.StatusBadRequest)
		if !strings.HasSuffix(e.Type, "#ExpiredIteratorException") || e.text() != expiredText {
			h.Errorf("at 15 minutes: %+v, want ExpiredIteratorException %q", e, expiredText)
		}
		// next was issued 1 ms ago.
		h.advance(15*time.Minute - 2*time.Millisecond)
		if r := h.streamsCall("GetRecords", `{"ShardIterator":"`+*next+`"}`, defaultCaller()); r.Status != http.StatusOK {
			h.Errorf("the NextShardIterator 1 ms before its own 15 minutes: %d %s", r.Status, r.Body)
		}
		h.advance(time.Millisecond)
		if e := h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+*next+`"}`, defaultCaller()), http.StatusBadRequest); e.text() != expiredText {
			h.Errorf("the NextShardIterator at its own 15 minutes: %+v", e)
		}
	})
}

// dynamodb.streams.iterator-reuse (inferred, DDBS-04): allowed by default,
// an iterator reads the same records again (as Local does,
// dynamodb-streams.md 6.4); flipped to single-use, a second use is
// ExpiredIteratorException.
func TestIteratorReuseSwitch(t *testing.T) {
	for _, reuse := range []dynamodb.IteratorReuse{dynamodb.IteratorReuseAllowed, dynamodb.IteratorSingleUse} {
		t.Run(reuse.String(), func(t *testing.T) {
			runStreams(t, dynamodb.Options{IteratorReuse: reuse}, func(h *streamsHarness) {
				arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
				h.put("Orders", "a")
				it := h.iterator(arn, h.describe(arn).Shards[0].ShardID, "TRIM_HORIZON", "")
				if n := len(h.records(it, 0).Records); n != 1 {
					h.Fatalf("first read: %d records", n)
				}
				r := h.streamsCall("GetRecords", `{"ShardIterator":"`+it+`"}`, defaultCaller())
				switch reuse {
				case dynamodb.IteratorReuseAllowed:
					var p recordsPage
					h.ok200(r, &p)
					if len(p.Records) != 1 {
						h.Errorf("reused iterator read %d records, want the same 1", len(p.Records))
					}
				case dynamodb.IteratorSingleUse:
					if e := h.errorOf(r, http.StatusBadRequest); !strings.HasSuffix(e.Type, "#ExpiredIteratorException") || e.text() != expiredText {
						h.Errorf("reused iterator under single-use: %+v", e)
					}
				}
			})
		})
	}
}

// dynamodb.streams.sequence-number flipped to start-equals-first (DynamoDB
// Local's form, dynamodb-streams.md 8.3 row 7): the first record's number
// is the shard's StartingSequenceNumber.
func TestSequenceNumberSwitch(t *testing.T) {
	runStreams(t, dynamodb.Options{StreamSequence: dynamodb.SequenceStartEqualsFirst}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.put("Orders", "a")
		start := h.describe(arn).Shards[0].SequenceNumberRange.StartingSequenceNumber
		got := h.allRecords(arn)
		if len(got) != 1 || got[0].Dynamodb.SequenceNumber != start {
			h.Errorf("first record %v, want the start %s", got, start)
		}
	})
}

// The Streams service lists its two switches, each with Ref, Record, Ops
// and NoScenario, and reports the value it was built with.
// iterator-reuse is aws-recorded (DDBS-04: allowed);
// DDBS-09 recorded a sequence-number form neither value has, so that
// switch stays inferred (dynamodb-streams.md 4.3, 4.4).
func TestStreamsSwitches(t *testing.T) {
	for _, tc := range []struct {
		opts      dynamodb.Options
		seq, used string
	}{
		{dynamodb.Options{}, "decimal-21", "allowed"},
		{dynamodb.Options{StreamSequence: dynamodb.SequenceStartEqualsFirst, IteratorReuse: dynamodb.IteratorSingleUse}, "start-equals-first", "single-use"},
	} {
		runStreams(t, tc.opts, func(h *streamsHarness) {
			got := map[string]fidelity.Entry{}
			for _, e := range h.streams.Switches() {
				got[e.Name] = e
			}
			for _, w := range []struct {
				name, value          string
				label                evidence.Label
				ref, record, because string
				ops                  []string
			}{
				{
					"dynamodb.streams.sequence-number", tc.seq, evidence.Inferred, "docs/research/dynamodb-streams.md#43-sequence-numbers", "DDBS-09",
					"recorded in dynamodb/DDBS-09: AWS numbers records with 23 digits above a 21-digit StartingSequenceNumber, which neither value does",
					[]string{"dynamodbstreams.DescribeStream", "dynamodbstreams.GetRecords"},
				},
				{
					"dynamodb.streams.iterator-reuse", tc.used, evidence.AWSRecorded, "docs/research/dynamodb-streams.md#44-shard-iterators", "DDBS-04",
					"dynamodb/DDBS-04/02-reuse.json is recorded; the replayer runs without DynamoDB",
					[]string{"dynamodbstreams.GetRecords"},
				},
			} {
				e, ok := got[w.name]
				if !ok {
					h.Errorf("%s not listed", w.name)
					continue
				}
				ns, isNS := e.Settles.(fidelity.NoScenario)
				if e.Value != w.value || e.Label != w.label || e.Ref != w.ref || e.Record != w.record || !slices.Equal(e.Ops, w.ops) ||
					!isNS || ns.Reason != w.because {
					h.Errorf("%s = %+v, want %+v", w.name, e, w)
				}
			}
			if len(got) != 2 {
				h.Errorf("Switches lists %d, want 2", len(got))
			}
		})
	}
	if dynamodb.StreamSequence(7).String() != "StreamSequence(7)" || dynamodb.SequenceDecimal21.String() != "decimal-21" {
		t.Errorf("StreamSequence.String: %q, %q", dynamodb.StreamSequence(7), dynamodb.SequenceDecimal21)
	}
}

// A disable closes the shard: EndingSequenceNumber is the last record's
// number, the stream reads DISABLED, and the page that exhausts the closed
// shard has no NextShardIterator (dynamodb-streams.md 5.3, aws-doc; the
// member's absence is AWS's form too, aws-recorded, DDBS-07).
func TestDisableClosesTheShard(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.put("Orders", "a")
		h.put("Orders", "b")
		h.setStream("")
		d := h.describe(arn)
		all := h.allRecords(arn)
		if d.StreamStatus != "DISABLED" || len(d.Shards) != 1 || d.Shards[0].SequenceNumberRange.EndingSequenceNumber == nil ||
			*d.Shards[0].SequenceNumberRange.EndingSequenceNumber != all[len(all)-1].Dynamodb.SequenceNumber {
			h.Errorf("disabled stream %+v, want DISABLED with the shard ended at the last record", d)
		}
		p := h.records(h.iterator(arn, d.Shards[0].ShardID, "TRIM_HORIZON", ""), 0)
		if len(p.Records) != 2 || p.NextShardIterator != nil {
			h.Errorf("closed shard page: %d records, next %v; want 2 and no NextShardIterator", len(p.Records), p.NextShardIterator)
		}
	})
}

// A deleted table's stream is DISABLED, closed, listed and readable until
// exactly 24 hours after the delete, then gone everywhere ("the stream is
// automatically deleted after 24 hours", aws-doc, dynamodb-streams.md 5.4;
// listed and readable after the delete is AWS's behavior too, aws-recorded,
// DDBS-17).
func TestDeletedTablesStreamIsDeletedAfter24Hours(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		h.put("Orders", "a")
		h.mustCall("DeleteTable", `{"TableName":"Orders"}`)
		h.advance(24*time.Hour - time.Millisecond)
		h.settle()
		if d := h.describe(arn); d.StreamStatus != "DISABLED" || d.Shards[0].SequenceNumberRange.EndingSequenceNumber == nil {
			h.Errorf("1 ms before 24 h: %+v, want DISABLED and closed", d)
		}
		if got := h.allRecords(arn); len(got) != 1 {
			h.Errorf("1 ms before 24 h: %d records, want 1", len(got))
		}
		h.advance(time.Millisecond)
		h.settle()
		r := h.streamsCall("ListStreams", `{}`, defaultCaller())
		if r.Status != http.StatusOK || strings.Contains(r.Body, arn) {
			h.Errorf("ListStreams at 24 h = %d %s, want the stream gone", r.Status, r.Body)
		}
		e := h.errorOf(h.streamsCall("DescribeStream", `{"StreamArn":"`+arn+`"}`, defaultCaller()), http.StatusBadRequest)
		if e.text() != "Requested resource not found: Stream: "+arn+" not found" {
			h.Errorf("DescribeStream at 24 h = %+v", e)
		}
	})
}

// trimmedText is the trimmed-data message: the model's member
// documentation without its stray leading quote (dynamodb-streams.md 3.3;
// inferred: DDBS-15 never saw it, GetRecords note).
const trimmedText = "The data you are trying to access has been trimmed."

// Records are trimmed exactly 24 hours after they arrive on the emulator
// clock (dynamodb-streams.md 5.5, aws-doc lifetime, 11.1 item 5): at 24 h
// - 1 ms the record is read, at 24 h TRIM_HORIZON starts after it, an
// AT_SEQUENCE_NUMBER iterator for it is refused, and an iterator taken
// before the trim whose next record is gone is refused at GetRecords, both
// with TrimmedDataAccessException.
func TestRecordsAreTrimmedAt24Hours(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		shard := h.describe(arn).Shards[0].ShardID
		h.put("Orders", "a")
		seq := h.allRecords(arn)[0].Dynamodb.SequenceNumber
		h.advance(24*time.Hour - time.Millisecond)
		h.settle()
		pending := h.iterator(arn, shard, "AT_SEQUENCE_NUMBER", seq)
		if got := h.allRecords(arn); len(got) != 1 {
			h.Fatalf("1 ms before 24 h: %d records, want 1", len(got))
		}
		h.advance(time.Millisecond)
		h.settle()
		if got := h.allRecords(arn); len(got) != 0 {
			h.Errorf("at 24 h: %d records from TRIM_HORIZON, want none", len(got))
		}
		e := h.errorOf(h.streamsCall("GetShardIterator", `{"StreamArn":"`+arn+`","ShardId":"`+shard+`","ShardIteratorType":"AT_SEQUENCE_NUMBER","SequenceNumber":"`+seq+`"}`, defaultCaller()), http.StatusBadRequest)
		if e.Type != "com.amazonaws.dynamodb.v20120810#TrimmedDataAccessException" || e.text() != trimmedText {
			h.Errorf("GetShardIterator at the trimmed record: %+v", e)
		}
		e = h.errorOf(h.streamsCall("GetRecords", `{"ShardIterator":"`+pending+`"}`, defaultCaller()), http.StatusBadRequest)
		if e.Type != "com.amazonaws.dynamodb.v20120810#TrimmedDataAccessException" || e.text() != trimmedText {
			h.Errorf("GetRecords from a trimmed position: %+v", e)
		}
	})
}

// Every Streams answer, 200 and 400, carries x-amz-crc32 over its body
// (aws-recorded: testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json
// on a 200, 07-streams-signed-streams.dynamodb.json on a 400;
// dynamodb-streams.md 3.1).
func TestStreamsAnswersCarryCRC32(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		for _, r := range []response{
			h.streamsCall("ListStreams", `{}`, defaultCaller()),
			h.streamsCall("DescribeStream", `{"StreamArn":"`+streamARNOf(defaultCaller(), "Nope", "2000-01-01T00:00:00.000")+`"}`, defaultCaller()),
		} {
			if got := r.Header.Get("X-Amz-Crc32"); got != crc(r.Body) {
				h.Errorf("%d %s: x-amz-crc32 %q, want %q", r.Status, r.Body, got, crc(r.Body))
			}
		}
	})
}

// A TTL sweep's delete is a REMOVE carrying userIdentity in the GetRecords
// casing {"PrincipalId","Type"} (aws-recorded, dynamodb-streams.md 7.5:
// testdata/parity/dynamodb/DDBS-13/records-3.json), and an ordinary DeleteItem carries none
// (11.8: no userIdentity on a delete that did not come from the sweeper).
func TestTTLDeletesCarryUserIdentity(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Locks", "KEYS_ONLY"))
		h.mustCall("UpdateTimeToLive", `{"TableName":"Locks","TimeToLiveSpecification":{"AttributeName":"exp","Enabled":true}}`)
		// The clock reads 2000-01-01T00:00:00Z: 946684860 is a minute ahead.
		h.mustCall("PutItem", `{"TableName":"Locks","Item":{"id":{"S":"swept"},"exp":{"N":"946684860"}}}`)
		h.put("Locks", "deleted")
		h.mustCall("DeleteItem", `{"TableName":"Locks","Key":{"id":{"S":"deleted"}}}`)
		h.settle()
		h.advance(time.Minute)
		h.settle()
		var removes []gotRecord
		for _, r := range h.allRecords(arn) {
			if r.EventName == "REMOVE" {
				removes = append(removes, r)
			}
		}
		if len(removes) != 2 {
			h.Fatalf("%d REMOVE records, want the DeleteItem's and the sweep's", len(removes))
		}
		if removes[0].UserIdentity != nil {
			h.Errorf("the client's DeleteItem carries userIdentity %v", removes[0].UserIdentity)
		}
		if want := map[string]string{"PrincipalId": "dynamodb.amazonaws.com", "Type": "Service"}; fmt.Sprint(removes[1].UserIdentity) != fmt.Sprint(want) {
			h.Errorf("the sweep's REMOVE userIdentity = %v, want %v", removes[1].UserIdentity, want)
		}
	})
}

// A stream ARN on the tag operations answers 501 NotImplemented with the
// note's text instead of "Invalid TableArn" (dynamodb-streams.md 11.6,
// 11.8; AWS tags existing streams, aws-doc Tagging.Streams).
func TestStreamARNsAreNotTaggable(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, _ := latest(h.createStreamTable("Orders", "KEYS_ONLY"))
		for op, body := range map[string]string{
			"TagResource":        `{"ResourceArn":"` + arn + `","Tags":[{"Key":"k","Value":"v"}]}`,
			"UntagResource":      `{"ResourceArn":"` + arn + `","TagKeys":["k"]}`,
			"ListTagsOfResource": `{"ResourceArn":"` + arn + `"}`,
		} {
			e := h.errorOf(h.call(op, body), http.StatusNotImplemented)
			if e.Type != "NotImplemented" || e.text() != "stream ARNs are not taggable in understudy; AWS tags existing streams" {
				h.Errorf("%s on a stream ARN: %+v", op, e)
			}
		}
	})
}

// A save, reset and load keep the stream: its label, its records with
// their numbers and event ids, the LatestStream members, and an iterator
// issued before the save; the load's own writes to Local emit nothing,
// and a write after the load continues the numbering (dynamodb-streams.md
// 11.1 item 7).
func TestSnapshotKeepsTheStream(t *testing.T) {
	runStreams(t, dynamodb.Options{}, func(h *streamsHarness) {
		arn, label := latest(h.createStreamTable("Orders", "NEW_IMAGE"))
		h.put("Orders", "a")
		h.put("Orders", "b")
		before := h.allRecords(arn)
		pending := h.iterator(arn, h.describe(arn).Shards[0].ShardID, "TRIM_HORIZON", "")
		items, err := h.svc.ExportLate(h.Context())
		if err != nil {
			h.Fatal(err)
		}
		cells, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.advance(time.Minute)
		if err := h.svc.ResetLate(h.Context(), kernel.ResetScope{}); err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.ImportLate(h.Context(), items); err != nil {
			h.Fatal(err)
		}
		if err := h.svc.Import(cells); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settle()
		if a, l := latest(h.mustCall("DescribeTable", `{"TableName":"Orders"}`)); a != arn || l != label {
			h.Errorf("after the load LatestStream = %s, %s; want %s, %s", a, l, arn, label)
		}
		after := h.allRecords(arn)
		same := func(a, b gotRecord) bool {
			return a.EventID == b.EventID && a.Dynamodb.SequenceNumber == b.Dynamodb.SequenceNumber && a.EventName == b.EventName &&
				a.Dynamodb.ApproximateCreationDateTime == b.Dynamodb.ApproximateCreationDateTime
		}
		if !slices.EqualFunc(after, before, same) {
			h.Errorf("records after the load = %+v, want the saved %+v and none for the load's writes", after, before)
		}
		if p := h.records(pending, 0); len(p.Records) != len(before) {
			h.Errorf("an iterator issued before the save read %d records after the load, want %d", len(p.Records), len(before))
		}
		h.put("Orders", "c")
		got := h.allRecords(arn)
		if len(got) != len(before)+1 {
			h.Fatalf("%d records after a write following the load, want %d", len(got), len(before)+1)
		}
		last := shardlog.Seq(before[len(before)-1].Dynamodb.SequenceNumber)
		if want := decimal21.Next(shardlog.ShardInfo{}, last, h.Clock.Now()); got[len(got)-1].Dynamodb.SequenceNumber != string(want) {
			h.Errorf("the next record is numbered %s, want %s after the restored %s", got[len(got)-1].Dynamodb.SequenceNumber, want, last)
		}
	})
}
