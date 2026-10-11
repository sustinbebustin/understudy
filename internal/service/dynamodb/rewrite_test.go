package dynamodb_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// describeLocal is Local's DescribeTable answer, ARNs in its fixed account
// and region (dynamodb-local.md 3, 8).
const describeLocal = `{"Table":{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks",` +
	`"KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"TableStatus":"ACTIVE","CreationDateTime":1791434323.298,` +
	`"ProvisionedThroughput":{"LastIncreaseDateTime":0.000,"LastDecreaseDateTime":0.000,"NumberOfDecreasesToday":0,"ReadCapacityUnits":0,"WriteCapacityUnits":0},` +
	`"TableSizeBytes":0,"ItemCount":0,"TableArn":"arn:aws:dynamodb:ddblocal:000000000000:table/Locks",` +
	`"GlobalSecondaryIndexes":[{"IndexName":"ByHolder","IndexArn":"arn:aws:dynamodb:ddblocal:000000000000:table/Locks/index/ByHolder"}],` +
	`"StreamSpecification":{"StreamEnabled":true,"StreamViewType":"NEW_AND_OLD_IMAGES"},"LatestStreamLabel":"2026-10-08T04:40:16.197",` +
	`"LatestStreamArn":"arn:aws:dynamodb:ddblocal:000000000000:table/Locks/stream/2026-10-08T04:40:16.197"}}`

// defaultWarm is the WarmThroughput every table description carries,
// appended after Local's members (dynamodb-local.md 7.2 item 1).
const defaultWarm = `,"WarmThroughput":{"ReadUnitsPerSecond":12000,"WriteUnitsPerSecond":4000,"Status":"ACTIVE"}}}`

// withWarm is a DescribeTable answer of Local's with defaultWarm added.
func withWarm(local string) string { return strings.TrimSuffix(local, "}}") + defaultWarm }

// TestTableARNsMoveIntoTheCallersScope: Local's table, index and stream
// ARNs name the caller's account and region, everything else stays as
// Local wrote it but for the WarmThroughput the shim adds, and x-amz-crc32
// and Content-Length match the new body (dynamodb-local.md 7.2 item 6,
// 9.1).
func TestTableARNsMoveIntoTheCallersScope(t *testing.T) {
	run(t, func(h *harness) {
		h.local.answer = func(string, string) reply { return reply{Status: http.StatusOK, Body: describeLocal} }
		got := h.post(target("DescribeTable"), `{"TableName":"Locks"}`, &caller{Account: "222222222222", Region: "eu-west-1"})
		want := withWarm(strings.ReplaceAll(describeLocal, "arn:aws:dynamodb:ddblocal:000000000000:", "arn:aws:dynamodb:eu-west-1:222222222222:"))
		if got.Status != http.StatusOK || got.Body != want {
			h.Fatalf("DescribeTable = %d\n%s\nwant\n%s", got.Status, got.Body, want)
		}
		if got.Header.Get("X-Amz-Crc32") != crc(want) {
			h.Errorf("x-amz-crc32 = %s, want %s, the checksum of the rewritten body", got.Header.Get("X-Amz-Crc32"), crc(want))
		}
		if got.Header.Get("Content-Length") != strconv.Itoa(len(want)) {
			h.Errorf("Content-Length = %s, want %d", got.Header.Get("Content-Length"), len(want))
		}
	})
}

// TestItemAnswersPassThroughUntouched: an item answer is Local's bytes
// with Local's checksum, even when an item holds a string that looks like
// one of Local's ARNs; only understudy's request id replaces Local's.
func TestItemAnswersPassThroughUntouched(t *testing.T) {
	run(t, func(h *harness) {
		const item = `{"Item":{"TableArn":{"S":"arn:aws:dynamodb:ddblocal:000000000000:table/Locks"},"n":{"N":"1.50"}}}`
		h.local.answer = func(string, string) reply { return reply{Status: http.StatusOK, Body: item} }
		got := h.call("GetItem", `{"TableName":"Locks","Key":{"LockID":{"S":"a"}}}`)
		if got.Status != http.StatusOK || got.Body != item {
			h.Fatalf("GetItem = %d %s, want Local's bytes %s", got.Status, got.Body, item)
		}
		if got.Header.Get("X-Amz-Crc32") != crc(item) {
			h.Errorf("x-amz-crc32 = %s, want Local's %s", got.Header.Get("X-Amz-Crc32"), crc(item))
		}
		if id := got.Header.Get("X-Amzn-Requestid"); id == "" || id == "local-request-id" {
			h.Errorf("x-amzn-RequestId = %q, want understudy's request id", id)
		}
		if got.Header.Get("Server") != "" {
			h.Errorf("Local's Server header %q reached the client", got.Header.Get("Server"))
		}
	})
}

// TestLocalErrorsTakeAWSWordsAndSpelling: Local writes Message on every
// error; AWS writes the shape's own member (message on all but four
// DynamoDB shapes; error-matrix.md 5.5), and its ResourceNotFoundException
// text names the table on the table operations only (dynalite
// db/index.js L486-L520), which Terraform matches on DeleteTable
// (dynamodb-local.md 7.1).
func TestLocalErrorsTakeAWSWordsAndSpelling(t *testing.T) {
	const notFound = `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","Message":"Cannot do operations on a non-existent table"}`
	tests := []struct {
		name, op, request, local, want string
	}{
		{
			"DeleteTable names the table", "DeleteTable", `{"TableName":"Locks"}`, notFound,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Table: Locks not found"}`,
		},
		{
			"DescribeTable names the table", "DescribeTable", `{"TableName":"Locks"}`, notFound,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Table: Locks not found"}`,
		},
		{
			"GetItem names none", "GetItem", `{"TableName":"Locks","Key":{"LockID":{"S":"a"}}}`, notFound,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found"}`,
		},
		{
			"conditional check keeps its text and item", "PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","Message":"The conditional request failed","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed","Item":{"LockID":{"S":"a"}}}`,
		},
		{
			"validation", "PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazon.coral.validate#ValidationException","Message":"One or more parameter values were invalid: Missing the key LockID in the item"}`,
			`{"__type":"com.amazon.coral.validate#ValidationException","message":"One or more parameter values were invalid: Missing the key LockID in the item"}`,
		},
		{
			"a Message shape keeps Message", "TransactWriteItems",
			`{"TransactItems":[{"Put":{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}}]}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed","Message":"The conditional request failed."}],"Message":"Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed]"}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed","Message":"The conditional request failed."}],"Message":"Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed]"}`,
		},
		{
			"a __type the operation does not send stays", "ListTables", `{}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#MissingAuthenticationToken","Message":"Request must contain either a valid (registered) AWS access key ID or X.509 certificate."}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#MissingAuthenticationToken","Message":"Request must contain either a valid (registered) AWS access key ID or X.509 certificate."}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.local.answer = func(string, string) reply { return reply{Status: http.StatusBadRequest, Body: tt.local} }
				got := h.call(tt.op, tt.request)
				if got.Status != http.StatusBadRequest || got.Body != tt.want {
					h.Fatalf("%s = %d\n%s\nwant 400\n%s", tt.op, got.Status, got.Body, tt.want)
				}
				if got.Header.Get("X-Amz-Crc32") != crc(tt.want) {
					h.Errorf("x-amz-crc32 = %s, want %s", got.Header.Get("X-Amz-Crc32"), crc(tt.want))
				}
			})
		})
	}
}

// TestRewrittenTableDescriptionsKeepEverythingButLocalsARNs: for any
// table description Local could write of a table the shim does not
// record, the client reads the same JSON value with every ARN Local minted
// moved into its scope and AWS's default WarmThroughput added, nothing
// else changed, and a checksum that matches the body it reads.
func TestRewrittenTableDescriptionsKeepEverythingButLocalsARNs(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			table := rapid.StringMatching(`[A-Za-z0-9_.-]{3,12}`).Draw(rt, "table")
			index := rapid.StringMatching(`[A-Za-z0-9_.-]{3,12}`).Draw(rt, "index")
			members := []string{
				`"TableName":"` + table + `"`,
				`"TableArn":"arn:aws:dynamodb:ddblocal:000000000000:table/` + table + `"`,
				`"ItemCount":` + rapid.StringMatching(`[1-9][0-9]{0,8}`).Draw(rt, "count"),
				`"CreationDateTime":` + rapid.StringMatching(`17[0-9]{8}\.[0-9]{3}`).Draw(rt, "created"),
				`"GlobalSecondaryIndexes":[{"IndexName":"` + index + `","IndexArn":"arn:aws:dynamodb:ddblocal:000000000000:table/` + table + `/index/` + index + `"}]`,
				`"Description":"arn:aws:dynamodb:ddblocal:000000000000:table/` + table + `"`,
			}
			members = rapid.Permutation(members).Draw(rt, "order")
			local := `{"Table":{` + strings.Join(members, ",") + `}}`
			h.local.answer = func(string, string) reply { return reply{Status: http.StatusOK, Body: local} }
			got := h.post(target("DescribeTable"), `{"TableName":"`+table+`"}`, &caller{Account: "333333333333", Region: "ap-southeast-2"})
			want := withWarm(strings.NewReplacer(
				`"TableArn":"arn:aws:dynamodb:ddblocal:000000000000:`, `"TableArn":"arn:aws:dynamodb:ap-southeast-2:333333333333:`,
				`"IndexArn":"arn:aws:dynamodb:ddblocal:000000000000:`, `"IndexArn":"arn:aws:dynamodb:ap-southeast-2:333333333333:`,
			).Replace(local))
			if got.Body != want {
				rt.Fatalf("DescribeTable =\n%s\nwant\n%s", got.Body, want)
			}
			if got.Header.Get("X-Amz-Crc32") != crc(got.Body) {
				rt.Fatalf("x-amz-crc32 %s does not match the body", got.Header.Get("X-Amz-Crc32"))
			}
		})
	})
}
