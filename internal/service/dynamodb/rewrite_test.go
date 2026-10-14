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

// defaultIndexWarm is the WarmThroughput every global secondary index
// description carries, appended after Local's members of an index Local
// describes without an IndexStatus.
const defaultIndexWarm = `,"WarmThroughput":{"ReadUnitsPerSecond":12000,"WriteUnitsPerSecond":4000,"Status":"ACTIVE"}`

// withWarm is a DescribeTable answer of Local's with defaultWarm added.
func withWarm(local string) string { return strings.TrimSuffix(local, "}}") + defaultWarm }

// withIndexWarm is local with defaultIndexWarm added to the index whose
// IndexArn ends in /index/<index>.
func withIndexWarm(local, index string) string {
	return strings.Replace(local, `/index/`+index+`"}`, `/index/`+index+`"`+defaultIndexWarm+`}`, 1)
}

// TestTableARNsMoveIntoTheCallersScope: Local's table, index and stream
// ARNs name the caller's account and region, everything else stays as
// Local wrote it but for the WarmThroughput the shim adds to the table and
// each index, and x-amz-crc32 and Content-Length match the new body
// (dynamodb-local.md 7.2 item 6, 9.1).
func TestTableARNsMoveIntoTheCallersScope(t *testing.T) {
	run(t, func(h *harness) {
		h.local.answer = func(string, string) reply { return reply{Status: http.StatusOK, Body: describeLocal} }
		got := h.post(target("DescribeTable"), `{"TableName":"Locks"}`, &caller{Account: "222222222222", Region: "eu-west-1"})
		want := withWarm(withIndexWarm(strings.ReplaceAll(describeLocal, "arn:aws:dynamodb:ddblocal:000000000000:", "arn:aws:dynamodb:eu-west-1:222222222222:"), "ByHolder"))
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

// TestItemAnswersEchoTheTableARNsTheRequestNamed: Local names each table by
// its bare name in Responses, UnprocessedKeys, UnprocessedItems,
// ItemCollectionMetrics and ConsumedCapacity's TableName; AWS echoes the
// identifier the caller used (API_ConsumedCapacity, API_BatchGetItem), so a
// table the request named by ARN is keyed by that ARN, one named by name
// keeps its name, and item data is never touched.
func TestItemAnswersEchoTheTableARNsTheRequestNamed(t *testing.T) {
	const arn = "arn:aws:dynamodb:us-east-1:111111111111:table/Zed"
	tests := []struct {
		name, op, request, local, want string
	}{
		{
			"GetItem's ConsumedCapacity", "GetItem",
			`{"TableName":"` + arn + `","Key":{"id":{"S":"a"}},"ReturnConsumedCapacity":"TOTAL"}`,
			`{"Item":{"TableName":{"S":"Zed"}},"ConsumedCapacity":{"TableName":"Zed","CapacityUnits":0.5}}`,
			`{"Item":{"TableName":{"S":"Zed"}},"ConsumedCapacity":{"TableName":"` + arn + `","CapacityUnits":0.5}}`,
		},
		{
			"BatchGetItem's maps and capacities", "BatchGetItem",
			`{"RequestItems":{"` + arn + `":{"Keys":[{"id":{"S":"a"}}]},"Other":{"Keys":[{"id":{"S":"b"}}]}},"ReturnConsumedCapacity":"TOTAL"}`,
			`{"Responses":{"Zed":[{"id":{"S":"a"}}],"Other":[{"id":{"S":"b"}}]},"UnprocessedKeys":{"Zed":{"Keys":[{"id":{"S":"c"}}]}},` +
				`"ConsumedCapacity":[{"TableName":"Zed","CapacityUnits":0.5},{"TableName":"Other","CapacityUnits":0.5}]}`,
			`{"Responses":{"` + arn + `":[{"id":{"S":"a"}}],"Other":[{"id":{"S":"b"}}]},"UnprocessedKeys":{"` + arn + `":{"Keys":[{"id":{"S":"c"}}]}},` +
				`"ConsumedCapacity":[{"TableName":"` + arn + `","CapacityUnits":0.5},{"TableName":"Other","CapacityUnits":0.5}]}`,
		},
		{
			"BatchWriteItem's maps", "BatchWriteItem",
			`{"RequestItems":{"` + arn + `":[{"PutRequest":{"Item":{"id":{"S":"a"}}}}]},"ReturnItemCollectionMetrics":"SIZE"}`,
			`{"UnprocessedItems":{"Zed":[{"PutRequest":{"Item":{"id":{"S":"a"}}}}]},"ItemCollectionMetrics":{"Zed":[]}}`,
			`{"UnprocessedItems":{"` + arn + `":[{"PutRequest":{"Item":{"id":{"S":"a"}}}}]},"ItemCollectionMetrics":{"` + arn + `":[]}}`,
		},
		{
			"TransactWriteItems' maps and capacities", "TransactWriteItems",
			`{"TransactItems":[{"Put":{"TableName":"` + arn + `","Item":{"id":{"S":"a"}}}},{"Delete":{"TableName":"` + arn + `","Key":{"id":{"S":"b"}}}}],` +
				`"ReturnConsumedCapacity":"TOTAL","ReturnItemCollectionMetrics":"SIZE"}`,
			`{"ConsumedCapacity":[{"TableName":"Zed","CapacityUnits":4.0}],"ItemCollectionMetrics":{"Zed":[]}}`,
			`{"ConsumedCapacity":[{"TableName":"` + arn + `","CapacityUnits":4.0}],"ItemCollectionMetrics":{"` + arn + `":[]}}`,
		},
		{
			"a table named by name keeps it", "BatchGetItem",
			`{"RequestItems":{"Zed":{"Keys":[{"id":{"S":"a"}}]}},"ReturnConsumedCapacity":"TOTAL"}`,
			`{"Responses":{"Zed":[]},"ConsumedCapacity":[{"TableName":"Zed","CapacityUnits":0.5}]}`,
			`{"Responses":{"Zed":[]},"ConsumedCapacity":[{"TableName":"Zed","CapacityUnits":0.5}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run(t, func(h *harness) {
				h.local.answer = func(string, string) reply { return reply{Status: http.StatusOK, Body: tt.local} }
				got := h.call(tt.op, tt.request)
				if got.Status != http.StatusOK || got.Body != tt.want {
					h.Fatalf("%s = %d\n%s\nwant\n%s", tt.op, got.Status, got.Body, tt.want)
				}
				if got.Header.Get("X-Amz-Crc32") != crc(tt.want) {
					h.Errorf("x-amz-crc32 = %s, want %s, the checksum of the body", got.Header.Get("X-Amz-Crc32"), crc(tt.want))
				}
			})
		})
	}
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
			"CreateTable of an existing table names it", "CreateTable",
			`{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks","KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}]}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceInUseException","Message":"Cannot create preexisting table"}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ResourceInUseException","message":"Table already exists: Locks"}`,
		},
		{
			// testdata/parity/dynamodb/R04/05-put-ccf-all-old.json
			"conditional check with its item keeps Message", "PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","Message":"The conditional request failed","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","Message":"The conditional request failed","Item":{"LockID":{"S":"a"}}}`,
		},
		{
			// testdata/parity/dynamodb/R04/05-put-ccf.json
			"conditional check without an item says message", "PutItem", `{"TableName":"Locks","Item":{"LockID":{"S":"a"}}}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","Message":"The conditional request failed"}`,
			`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`,
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
// moved into its scope and AWS's default WarmThroughput added to the table
// and its index, nothing else changed, and a checksum that matches the
// body it reads.
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
			want := withWarm(withIndexWarm(strings.NewReplacer(
				`"TableArn":"arn:aws:dynamodb:ddblocal:000000000000:`, `"TableArn":"arn:aws:dynamodb:ap-southeast-2:333333333333:`,
				`"IndexArn":"arn:aws:dynamodb:ddblocal:000000000000:`, `"IndexArn":"arn:aws:dynamodb:ap-southeast-2:333333333333:`,
			).Replace(local), index))
			if got.Body != want {
				rt.Fatalf("DescribeTable =\n%s\nwant\n%s", got.Body, want)
			}
			if got.Header.Get("X-Amz-Crc32") != crc(got.Body) {
				rt.Fatalf("x-amz-crc32 %s does not match the body", got.Header.Get("X-Amz-Crc32"))
			}
		})
	})
}
