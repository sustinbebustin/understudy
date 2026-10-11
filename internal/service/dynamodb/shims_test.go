package dynamodb_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// decode parses a JSON answer the test reads; an empty body, the answer of
// an operation without output, is an empty object.
func decode(h *harness, r response) map[string]any {
	h.Helper()
	out := map[string]any{}
	if r.Body == "" {
		return out
	}
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		h.Fatalf("answer %d %q: %v", r.Status, r.Body, err)
	}
	return out
}

// path reads a nested member of a decoded answer.
func path(v any, keys ...string) any {
	for _, k := range keys {
		m, isMap := v.(map[string]any)
		if !isMap {
			return nil
		}
		v = m[k]
	}
	return v
}

// mustCall sends op and fails the test unless it answers 200.
func (h *harness) mustCall(op, body string) map[string]any {
	h.Helper()
	got := h.call(op, body)
	if got.Status != http.StatusOK {
		h.Fatalf("%s %s = %d %s", op, body, got.Status, got.Body)
	}
	return decode(h, got)
}

// localSaw counts the requests of op Local received.
func (h *harness) localSaw(op string) int {
	n := 0
	for _, f := range h.local.requests() {
		if f.Op == op {
			n++
		}
	}
	return n
}

// equalJSON compares decoded JSON values; numbers decode as float64.
func equalJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// terraformProbe is one aws_dynamodb_table configuration the PROBE of
// tf-storage.md 3.1 created. The PROBE's index name g1 ran against moto,
// which skips the model's minimum length of 3, so the replay names it
// gsi1, as AWS would require.
type terraformProbe struct {
	name, table, create string
	// tags are what ListTagsOfResource answers after the create.
	tags []any
	// want are the members every description of the table carries, given
	// the AWS managed key's ARN; a nil value is a member left out.
	want func(managed string) map[string]any
	// index is the one global secondary index each description lists, in
	// the members given; nil for a table without one.
	index map[string]any
}

// created2000 is the instant a probe table is created: the bubble's clock
// starts frozen at 2000-01-01T00:00:00Z.
const created2000 = 946684800.0

// unchangedUnits is a ProvisionedThroughput description of units reads and
// writes that never changed on the emulator clock.
func unchangedUnits(units float64) map[string]any {
	return map[string]any{
		"LastIncreaseDateTime": 0.0, "LastDecreaseDateTime": 0.0, "NumberOfDecreasesToday": 0.0,
		"ReadCapacityUnits": units, "WriteCapacityUnits": units,
	}
}

var terraformProbes = []terraformProbe{
	{
		name: "on demand", table: "probe-locks",
		create: `{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"BillingMode":"PAY_PER_REQUEST",` +
			`"KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"SSESpecification":{"Enabled":true},"TableClass":"STANDARD_INFREQUENT_ACCESS",` +
			`"TableName":"probe-locks","Tags":[{"Key":"Name","Value":"probe"},{"Key":"env","Value":"dev"}]}`,
		tags: []any{map[string]any{"Key": "Name", "Value": "probe"}, map[string]any{"Key": "env", "Value": "dev"}},
		want: func(managed string) map[string]any {
			return map[string]any{
				"TableClassSummary":  map[string]any{"TableClass": "STANDARD_INFREQUENT_ACCESS"},
				"SSEDescription":     map[string]any{"Status": "ENABLED", "SSEType": "KMS", "KMSMasterKeyArn": managed},
				"BillingModeSummary": map[string]any{"BillingMode": "PAY_PER_REQUEST", "LastUpdateToPayPerRequestDateTime": created2000},
			}
		},
	},
	{
		name: "provisioned with a global secondary index", table: "probe-indexed",
		create: `{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"},{"AttributeName":"Owner","AttributeType":"S"}],"BillingMode":"PROVISIONED",` +
			`"GlobalSecondaryIndexes":[{"IndexName":"gsi1","KeySchema":[{"AttributeName":"Owner","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"},` +
			`"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1}}],"KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],` +
			`"ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":1},"TableName":"probe-indexed"}`,
		tags: []any{},
		want: func(string) map[string]any {
			return map[string]any{"TableClassSummary": nil, "SSEDescription": nil, "ProvisionedThroughput": unchangedUnits(1)}
		},
		index: map[string]any{
			"IndexName": "gsi1", "IndexStatus": "ACTIVE", "IndexArn": "arn:aws:dynamodb:us-east-1:111111111111:table/probe-indexed/index/gsi1",
			"KeySchema":             []any{map[string]any{"AttributeName": "Owner", "KeyType": "HASH"}},
			"Projection":            map[string]any{"ProjectionType": "ALL"},
			"ProvisionedThroughput": unchangedUnits(1),
		},
	},
}

// TestTerraformTableLifecycleReadsWithoutDiff replays the calls
// aws_dynamodb_table makes (tf-storage.md 3) against a fake Local, for
// each PROBE table: every member the provider reads is present, two reads
// in a row agree, tags, TTL and point-in-time recovery round-trip, an
// update of only the table class is answered without Local's "Nothing to
// update", and the delete reports DELETING then not found in the words
// the provider matches.
func TestTerraformTableLifecycleReadsWithoutDiff(t *testing.T) {
	for _, p := range terraformProbes {
		t.Run(p.name, func(t *testing.T) { runStore(t, func(h *harness, _ *tableStore) { replayTerraform(h, p) }, withKMS) })
	}
}

func replayTerraform(h *harness, p terraformProbe) {
	h.Helper()
	created := h.mustCall("CreateTable", p.create)
	arn := "arn:aws:dynamodb:us-east-1:111111111111:table/" + p.table
	named := `{"TableName":"` + p.table + `"}`
	// The provider's clearSSEDefaultKey compares KMSMasterKeyArn with
	// DescribeKey of alias/aws/dynamodb: the AWS managed key's ARN.
	managed := h.managedKeyARN()
	if !strings.HasPrefix(managed, "arn:aws:kms:us-east-1:111111111111:key/") {
		h.Errorf("alias/aws/dynamodb names %s, want a key ARN in the caller's account", managed)
	}
	want := map[string]any{
		"TableArn":         arn,
		"TableStatus":      "ACTIVE",
		"WarmThroughput":   map[string]any{"ReadUnitsPerSecond": 12000.0, "WriteUnitsPerSecond": 4000.0, "Status": "ACTIVE"},
		"CreationDateTime": created2000,
	}
	maps.Copy(want, p.want(managed))
	check := func(what string, desc any) {
		h.Helper()
		for k, v := range want {
			if got := path(desc, k); !equalJSON(got, v) {
				h.Errorf("%s: %s = %v, want %v", what, k, got, v)
			}
		}
		if id, isString := path(desc, "TableId").(string); !isString || len(id) != 36 {
			h.Errorf("%s: TableId = %v, want a UUID", what, path(desc, "TableId"))
		}
		indexes, listed := path(desc, "GlobalSecondaryIndexes").([]any)
		switch {
		case p.index == nil && listed:
			h.Errorf("%s: GlobalSecondaryIndexes = %v, want none", what, indexes)
		case p.index != nil && (!listed || len(indexes) != 1):
			h.Errorf("%s: GlobalSecondaryIndexes = %v, want one", what, indexes)
		case p.index != nil:
			for k, v := range p.index {
				if got := path(indexes[0], k); !equalJSON(got, v) {
					h.Errorf("%s: GlobalSecondaryIndexes[0].%s = %v, want %v", what, k, got, v)
				}
			}
		}
	}
	check("CreateTable", path(created, "TableDescription"))
	tableID := path(created, "TableDescription", "TableId")

	// waitTableActive and waitTableWarmThroughputActive.
	for range 3 {
		check("DescribeTable", path(h.mustCall("DescribeTable", named), "Table"))
	}
	if ttl := h.mustCall("UpdateTimeToLive", `{"TableName":"`+p.table+`","TimeToLiveSpecification":{"AttributeName":"ExpiresAt","Enabled":true}}`); !equalJSON(
		path(ttl, "TimeToLiveSpecification"), map[string]any{"AttributeName": "ExpiresAt", "Enabled": true}) {
		h.Errorf("UpdateTimeToLive = %v", ttl)
	}
	pitr := h.mustCall("UpdateContinuousBackups", `{"PointInTimeRecoverySpecification":{"PointInTimeRecoveryEnabled":true},"TableName":"`+p.table+`"}`)
	if s := path(pitr, "ContinuousBackupsDescription", "PointInTimeRecoveryDescription", "PointInTimeRecoveryStatus"); s != "ENABLED" {
		h.Errorf("UpdateContinuousBackups status = %v", s)
	}

	reads := []struct{ op, body string }{
		{"DescribeTable", named},
		{"DescribeContinuousBackups", named},
		{"DescribeTimeToLive", named},
		{"ListTagsOfResource", `{"ResourceArn":"` + arn + `"}`},
	}
	read := func() []string {
		bodies := make([]string, 0, len(reads))
		for _, c := range reads {
			got := h.call(c.op, c.body)
			if got.Status != http.StatusOK {
				h.Fatalf("%s = %d %s", c.op, got.Status, got.Body)
			}
			if got.Header.Get("X-Amz-Crc32") != crc(got.Body) {
				h.Errorf("%s: x-amz-crc32 %s does not match the body", c.op, got.Header.Get("X-Amz-Crc32"))
			}
			bodies = append(bodies, got.Body)
		}
		return bodies
	}
	first := read()
	if second := read(); !slices.Equal(first, second) {
		h.Errorf("two reads differ:\n%v\n%v", first, second)
	}
	answer := func(i int) map[string]any { return decode(h, response{Status: http.StatusOK, Body: first[i]}) }
	check("read DescribeTable", path(answer(0), "Table"))
	if got := path(answer(1), "ContinuousBackupsDescription"); !equalJSON(got, map[string]any{
		"ContinuousBackupsStatus": "ENABLED",
		"PointInTimeRecoveryDescription": map[string]any{
			"PointInTimeRecoveryStatus": "ENABLED", "RecoveryPeriodInDays": 35.0,
			"EarliestRestorableDateTime": created2000, "LatestRestorableDateTime": created2000,
		},
	}) {
		h.Errorf("DescribeContinuousBackups = %v", got)
	}
	if got := answer(2); !equalJSON(got, map[string]any{"TimeToLiveDescription": map[string]any{"TimeToLiveStatus": "ENABLED", "AttributeName": "ExpiresAt"}}) {
		h.Errorf("DescribeTimeToLive = %v", got)
	}
	if got := path(answer(3), "Tags"); !equalJSON(got, p.tags) {
		h.Errorf("ListTagsOfResource = %v", got)
	}

	// Update: tags through the interceptor, then the table class alone.
	h.mustCall("UntagResource", `{"ResourceArn":"`+arn+`","TagKeys":["env"]}`)
	h.mustCall("TagResource", `{"ResourceArn":"`+arn+`","Tags":[{"Key":"team","Value":"core"},{"Key":"Name","Value":"locks"}]}`)
	if got := path(h.mustCall("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`), "Tags"); !equalJSON(got,
		[]any{map[string]any{"Key": "Name", "Value": "locks"}, map[string]any{"Key": "team", "Value": "core"}}) {
		h.Errorf("tags after the update = %v", got)
	}
	updated := h.mustCall("UpdateTable", `{"TableName":"`+p.table+`","TableClass":"STANDARD"}`)
	if got := path(updated, "TableDescription", "TableClassSummary"); !equalJSON(got, map[string]any{"TableClass": "STANDARD", "LastUpdateDateTime": created2000}) {
		h.Errorf("UpdateTable TableClassSummary = %v", got)
	}
	if got := path(updated, "TableDescription", "TableId"); got != tableID {
		h.Errorf("UpdateTable TableId = %v, want %v", got, tableID)
	}
	if n := h.localSaw("UpdateTable"); n != 0 {
		h.Errorf("Local received %d UpdateTable requests; an update of only the table class is the shim's", n)
	}

	deleted := h.mustCall("DeleteTable", named)
	if s := path(deleted, "TableDescription", "TableStatus"); s != "DELETING" {
		h.Errorf("DeleteTable TableStatus = %v, want DELETING", s)
	}
	for _, c := range []struct{ op, body, typ, message string }{
		{"DescribeTable", named, "ResourceNotFoundException", "Requested resource not found: Table: " + p.table + " not found"},
		{"DescribeContinuousBackups", named, "TableNotFoundException", "Table not found: " + p.table},
		{"DescribeTimeToLive", named, "ResourceNotFoundException", "Requested resource not found: Table: " + p.table + " not found"},
		{"ListTagsOfResource", `{"ResourceArn":"` + arn + `"}`, "ResourceNotFoundException", "Requested resource not found: ResourcArn: " + arn + " not found"},
	} {
		got := h.call(c.op, c.body)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazonaws.dynamodb.v20120810#"+c.typ || e.Message != c.message {
			h.Errorf("%s after the delete = %d %s, want %s %q", c.op, got.Status, got.Body, c.typ, c.message)
		}
	}
	for _, op := range []string{"UpdateTimeToLive", "DescribeTimeToLive", "TagResource", "UntagResource", "ListTagsOfResource", "UpdateContinuousBackups", "DescribeContinuousBackups"} {
		if n := h.localSaw(op); n != 0 {
			h.Errorf("Local received %d %s requests; the shims answer it", n, op)
		}
	}
}

// managedKeyARN asks KMS, as the default caller, for the ARN of the key
// alias/aws/dynamodb names.
func (h *harness) managedKeyARN() string {
	h.Helper()
	account, err := aws.ParseAccountID(defaultCaller().Account)
	if err != nil {
		h.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse(defaultCaller().Region)
	if err != nil {
		h.Fatal(err)
	}
	op := kmsapi.OpDescribeKey.Descriptor()
	c := h.Kernel.NewCall(kernel.ExternalCall{
		RequestID: h.Kernel.IDs().RequestID(), Operation: op.ID,
		Scope:     aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region},
		Principal: identity.Root{Account: account},
		Origin:    ref.External{Protocol: ref.AWSJSON11, Listener: ref.ListenerMain},
		Admitted:  h.Clock.Now(), Endpoint: h.Endpoint,
	})
	out, aerr := kerneltest.Dispatch[kmsapi.DescribeKeyInput, kmsapi.DescribeKeyOutput](h.Context(), h.K, c, &kmsapi.DescribeKeyInput{KeyId: "alias/aws/dynamodb"})
	if aerr != nil || out.KeyMetadata == nil || out.KeyMetadata.Arn == nil {
		h.Fatalf("DescribeKey alias/aws/dynamodb = %+v, %v", out, aerr)
	}
	return *out.KeyMetadata.Arn
}

// TestUnknownKMSKeysAreRefused: an SSE specification naming a key KMS
// does not find is refused before Local creates or changes anything.
func TestUnknownKMSKeysAreRefused(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		got := h.call("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs",`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"SSESpecification":{"Enabled":true,"KMSMasterKeyId":"alias/missing"}}`)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazon.coral.validate#ValidationException" {
			h.Errorf("CreateTable with an unknown key = %d %s, want ValidationException", got.Status, got.Body)
		}
		if n := h.localSaw("CreateTable"); n != 0 {
			h.Errorf("Local received %d CreateTable requests for a refused key", n)
		}
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`)
		got = h.call("UpdateTable", `{"TableName":"Jobs","SSESpecification":{"Enabled":true,"KMSMasterKeyId":"alias/missing"}}`)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != "com.amazon.coral.validate#ValidationException" {
			h.Errorf("UpdateTable with an unknown key = %d %s, want ValidationException", got.Status, got.Body)
		}
		if n := h.localSaw("UpdateTable"); n != 0 {
			h.Errorf("Local received %d UpdateTable requests for a refused key", n)
		}
		if desc := path(h.mustCall("DescribeTable", `{"TableName":"Jobs"}`), "Table", "SSEDescription"); desc != nil {
			h.Errorf("SSEDescription after a refused update = %v, want none", desc)
		}
	}, withKMS)
}

// TestUpdateTableAnswerAgreesWithDescribeOnAMovingClock: the UpdateTable
// answer and the DescribeTable after it report the same table class and
// throughput instants, the update's admission, though the clock moves
// while Local works; a throughput increase and a decrease are each stamped
// on the emulator clock, never Local's wall clock.
func TestUpdateTableAnswerAgreesWithDescribeOnAMovingClock(t *testing.T) {
	runStore(t, func(h *harness, store *tableStore) {
		h.local.answer = func(op, body string) reply {
			if op == "UpdateTable" {
				if _, err := h.Clock.Advance(time.Second); err != nil {
					h.Error(err)
				}
			}
			return store.serve(op, body)
		}
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs",`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"ProvisionedThroughput":{"ReadCapacityUnits":5,"WriteCapacityUnits":5}}`)
		check := func(what, body string, class, increased, decreased float64) {
			h.Helper()
			updated := path(h.mustCall("UpdateTable", body), "TableDescription")
			described := path(h.mustCall("DescribeTable", `{"TableName":"Jobs"}`), "Table")
			for _, member := range []string{"TableClassSummary", "ProvisionedThroughput"} {
				if !equalJSON(path(updated, member), path(described, member)) {
					h.Errorf("%s: UpdateTable %s = %v, DescribeTable %v", what, member, path(updated, member), path(described, member))
				}
			}
			if got := path(described, "TableClassSummary", "LastUpdateDateTime"); got != class {
				h.Errorf("%s: LastUpdateDateTime = %v, want %v", what, got, class)
			}
			if got := path(described, "ProvisionedThroughput", "LastIncreaseDateTime"); got != increased {
				h.Errorf("%s: LastIncreaseDateTime = %v, want %v", what, got, increased)
			}
			if got := path(described, "ProvisionedThroughput", "LastDecreaseDateTime"); got != decreased {
				h.Errorf("%s: LastDecreaseDateTime = %v, want %v", what, got, decreased)
			}
		}
		check("increase", `{"TableName":"Jobs","TableClass":"STANDARD_INFREQUENT_ACCESS","ProvisionedThroughput":{"ReadCapacityUnits":10,"WriteCapacityUnits":5}}`,
			946684800, 946684800, 0)
		if _, err := h.Clock.Advance(time.Minute); err != nil {
			h.Fatal(err)
		}
		check("decrease", `{"TableName":"Jobs","ProvisionedThroughput":{"ReadCapacityUnits":3,"WriteCapacityUnits":5}}`,
			946684800, 946684800, 946684861)
	})
}

// TestTagLimits: a table holds at most 50 tags, and keys starting with
// aws: are AWS's, on CreateTable as on TagResource; Local never sees a
// refused CreateTable, and a refused TagResource changes nothing.
func TestTagLimits(t *testing.T) {
	tags := func(prefix string, n int) string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf(`{"Key":"%s%d","Value":"v"}`, prefix, i))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	const arn = "arn:aws:dynamodb:us-east-1:111111111111:table/Jobs"
	for _, c := range []struct {
		name          string
		created, more string
		refused       bool
	}{
		{name: "50 at creation", created: tags("k", 50), more: "[]"},
		{name: "51 at creation", created: tags("k", 51), refused: true},
		{name: "reserved at creation", created: `[{"Key":"AWS:owner","Value":"v"}]`, refused: true},
		{name: "51 by tagging", created: tags("k", 49), more: tags("n", 2), refused: true},
		{name: "overwriting at 50", created: tags("k", 50), more: tags("k", 3)},
		{name: "reserved by tagging", created: "[]", more: `[{"Key":"aws:owner","Value":"v"}]`, refused: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			runStore(t, func(h *harness, _ *tableStore) {
				create := h.call("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs",`+
					`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"Tags":`+c.created+`}`)
				if c.more == "" {
					if create.Status != http.StatusBadRequest || h.localSaw("CreateTable") != 0 {
						h.Errorf("CreateTable = %d %s, want ValidationException before Local", create.Status, create.Body)
					}
					return
				}
				if create.Status != http.StatusOK {
					h.Fatalf("CreateTable = %d %s", create.Status, create.Body)
				}
				before := h.mustCall("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`)
				got := h.call("TagResource", `{"ResourceArn":"`+arn+`","Tags":`+c.more+`}`)
				if refused := got.Status == http.StatusBadRequest; refused != c.refused {
					h.Errorf("TagResource = %d %s, want refused %v", got.Status, got.Body, c.refused)
				}
				if after := h.mustCall("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`); c.refused && !equalJSON(after, before) {
					h.Errorf("tags after a refused TagResource = %v, want %v", after, before)
				}
			})
		})
	}
}

// TestUnrecordedTablesAreAdopted: a table Local holds that the shim never
// saw created (one Local kept under --persist without the archive) answers
// the shim operations as a fresh table does, as DescribeTable says it
// exists, and keeps what they set; a name Local does not hold is still not
// found.
func TestUnrecordedTablesAreAdopted(t *testing.T) {
	run(t, func(h *harness) {
		h.local.answer = func(op, body string) reply {
			if op == "DescribeTable" && strings.Contains(body, `"Old"`) {
				return reply{Status: http.StatusOK, Body: `{"Table":{"TableName":"Old","TableStatus":"ACTIVE","CreationDateTime":1791434323.298}}`}
			}
			return localErr("com.amazonaws.dynamodb.v20120810#ResourceNotFoundException", "Cannot do operations on a non-existent table")
		}
		const arn = "arn:aws:dynamodb:us-east-1:111111111111:table/Old"
		if got := h.mustCall("DescribeTimeToLive", `{"TableName":"Old"}`); !equalJSON(got, map[string]any{"TimeToLiveDescription": map[string]any{"TimeToLiveStatus": "DISABLED"}}) {
			h.Errorf("DescribeTimeToLive = %v", got)
		}
		if got := path(h.mustCall("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`), "Tags"); !equalJSON(got, []any{}) {
			h.Errorf("ListTagsOfResource = %v, want []", got)
		}
		if got := path(h.mustCall("DescribeContinuousBackups", `{"TableName":"Old"}`), "ContinuousBackupsDescription", "ContinuousBackupsStatus"); got != "ENABLED" {
			h.Errorf("ContinuousBackupsStatus = %v", got)
		}
		h.mustCall("TagResource", `{"ResourceArn":"`+arn+`","Tags":[{"Key":"team","Value":"core"}]}`)
		if got := path(h.mustCall("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`), "Tags"); !equalJSON(got, []any{map[string]any{"Key": "team", "Value": "core"}}) {
			h.Errorf("tags after tagging = %v", got)
		}
		desc := path(h.mustCall("DescribeTable", `{"TableName":"Old"}`), "Table")
		if id, isString := path(desc, "TableId").(string); !isString || len(id) != 36 || path(desc, "CreationDateTime") != 1791434323.298 {
			h.Errorf("DescribeTable of the adopted table = %v, want a TableId and Local's CreationDateTime", desc)
		}
		got := h.call("DescribeTimeToLive", `{"TableName":"Gone"}`)
		if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Message != "Requested resource not found: Table: Gone not found" {
			h.Errorf("DescribeTimeToLive of a table nobody holds = %d %s", got.Status, got.Body)
		}
	})
}

// TestDescriptionsOfUnrecordedTablesGetOnlyWarmThroughput: a table Local
// holds that the shim never saw created (one Local kept under --persist
// without the archive) is described as Local has it, with AWS's default
// WarmThroughput, so Terraform's waiter still completes.
func TestDescriptionsOfUnrecordedTablesGetOnlyWarmThroughput(t *testing.T) {
	run(t, func(h *harness) {
		h.local.answer = func(string, string) reply {
			return reply{Status: http.StatusOK, Body: `{"Table":{"TableName":"Old","TableStatus":"ACTIVE","CreationDateTime":1791434323.298}}`}
		}
		got := h.call("DescribeTable", `{"TableName":"Old"}`)
		want := `{"Table":{"TableName":"Old","TableStatus":"ACTIVE","CreationDateTime":1791434323.298` + defaultWarm
		if got.Body != want {
			h.Errorf("DescribeTable = %s, want %s", got.Body, want)
		}
	})
}

// TestShimFieldsFollowUpdates: an UpdateTable that also changes what Local
// owns is forwarded as sent, and the shim records the members it owns
// once Local accepts it; WarmThroughput keeps what was configured.
func TestShimFieldsFollowUpdates(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs",`+
			`"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"WarmThroughput":{"ReadUnitsPerSecond":15000}}`)
		body := `{"TableName":"Jobs","BillingMode":"PAY_PER_REQUEST","SSESpecification":{"Enabled":true,"KMSMasterKeyId":"alias/jobs"}}`
		updated := h.mustCall("UpdateTable", body)
		if seen := h.local.requests(); seen[len(seen)-1].Op != "UpdateTable" || seen[len(seen)-1].Body != body {
			h.Errorf("Local received %+v, want the client's UpdateTable bytes", seen[len(seen)-1])
		}
		if got := path(updated, "TableDescription", "SSEDescription", "KMSMasterKeyArn"); got != "arn:aws:kms:us-east-1:111111111111:alias/jobs" {
			h.Errorf("SSEDescription.KMSMasterKeyArn = %v", got)
		}
		desc := path(h.mustCall("DescribeTable", `{"TableName":"Jobs"}`), "Table")
		if got := path(desc, "WarmThroughput"); !equalJSON(got, map[string]any{"ReadUnitsPerSecond": 15000.0, "WriteUnitsPerSecond": 4000.0, "Status": "ACTIVE"}) {
			h.Errorf("WarmThroughput = %v", got)
		}
		if got := path(desc, "SSEDescription", "Status"); got != "ENABLED" {
			h.Errorf("SSEDescription after DescribeTable = %v", path(desc, "SSEDescription"))
		}
		h.mustCall("UpdateTable", `{"TableName":"Jobs","SSESpecification":{"Enabled":false}}`)
		if got := path(h.mustCall("DescribeTable", `{"TableName":"Jobs"}`), "Table", "SSEDescription"); got != nil {
			h.Errorf("SSEDescription after disabling = %v, want none", got)
		}
	})
}

// TestTagOperationsCheckTheARN: a ResourceArn that is no table's is a
// ValidationException; a table of another account or region, or a case
// twin, is not found.
func TestTagOperationsCheckTheARN(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`)
		for arn, typ := range map[string]string{
			"arn:aws:sqs:us-east-1:111111111111:Jobs":                    "com.amazon.coral.validate#ValidationException",
			"arn:aws:dynamodb:us-east-1:111111111111:table/Jobs/index/x": "com.amazon.coral.validate#ValidationException",
			"arn:aws:dynamodb:eu-west-1:111111111111:table/Jobs":         "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException",
			"arn:aws:dynamodb:us-east-1:222222222222:table/Jobs":         "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException",
			"arn:aws:dynamodb:us-east-1:111111111111:table/jobs":         "com.amazonaws.dynamodb.v20120810#ResourceNotFoundException",
		} {
			got := h.call("ListTagsOfResource", `{"ResourceArn":"`+arn+`"}`)
			if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Type != typ {
				h.Errorf("ListTagsOfResource %s = %d %s, want %s", arn, got.Status, got.Body, typ)
			}
		}
		if got := path(h.mustCall("ListTagsOfResource", `{"ResourceArn":"arn:aws:dynamodb:us-east-1:111111111111:table/Jobs"}`), "Tags"); !equalJSON(got, []any{}) {
			h.Errorf("ListTagsOfResource of an untagged table = %v, want []", got)
		}
	})
}

// TestTimeToLiveChangesFollowLocalsRules: enabling twice, enabling another
// attribute and disabling twice are refused with the texts Local gives
// (dynamodb-local.md 5.2), and a disabled table describes no attribute.
func TestTimeToLiveChangesFollowLocalsRules(t *testing.T) {
	runStore(t, func(h *harness, _ *tableStore) {
		h.mustCall("CreateTable", `{"AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"TableName":"Jobs","KeySchema":[{"AttributeName":"id","KeyType":"HASH"}]}`)
		spec := func(attr string, on bool) string {
			enabled := "false"
			if on {
				enabled = "true"
			}
			return `{"TableName":"Jobs","TimeToLiveSpecification":{"AttributeName":"` + attr + `","Enabled":` + enabled + `}}`
		}
		disabled := map[string]any{"TimeToLiveDescription": map[string]any{"TimeToLiveStatus": "DISABLED"}}
		if got := h.mustCall("DescribeTimeToLive", `{"TableName":"Jobs"}`); !equalJSON(got, disabled) {
			h.Errorf("DescribeTimeToLive of a new table = %v", got)
		}
		h.mustCall("UpdateTimeToLive", spec("ExpiresAt", true))
		for body, message := range map[string]string{
			spec("ExpiresAt", true): "TimeToLive is already enabled",
			spec("Other", true):     "TimeToLive is active on a different AttributeName",
		} {
			got := h.call("UpdateTimeToLive", body)
			if e := parseError(h, got.Body); got.Status != http.StatusBadRequest || e.Message != message {
				h.Errorf("UpdateTimeToLive %s = %d %s, want %q", body, got.Status, got.Body, message)
			}
		}
		h.mustCall("UpdateTimeToLive", spec("ExpiresAt", false))
		if got := h.call("UpdateTimeToLive", spec("ExpiresAt", false)); parseError(h, got.Body).Message != "TimeToLive is already disabled" {
			h.Errorf("disabling twice = %d %s", got.Status, got.Body)
		}
		if got := h.mustCall("DescribeTimeToLive", `{"TableName":"Jobs"}`); !equalJSON(got, disabled) {
			h.Errorf("DescribeTimeToLive after disabling = %v", got)
		}
	})
}
