package dynamodb_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// TestCredentialPartitionsLocalByAccountAndRegion: whatever key the
// client signs with, Local is sent the caller's account and region as the
// credential scope, so its native partitioning isolates (account, region)
// (ADR 0014, dynamodb-local.md 3), and Accept-Encoding: identity so its
// checksum is over the bytes the client reads (9.1).
func TestCredentialPartitionsLocalByAccountAndRegion(t *testing.T) {
	run(t, func(h *harness) {
		callers := []caller{
			{Account: "111111111111", Region: "us-east-1"},
			{Account: "111111111111", Region: "eu-west-1"},
			{Account: "222222222222", Region: "us-east-1"},
			{Account: "222222222222", Region: "eu-west-1"},
		}
		for _, c := range callers {
			got := h.post(target("ListTables"), `{}`, &c)
			if got.Status != http.StatusOK {
				h.Fatalf("ListTables as %+v = %d %s", c, got.Status, got.Body)
			}
		}
		seen := h.local.requests()
		if len(seen) != len(callers) {
			h.Fatalf("Local received %d requests, want %d", len(seen), len(callers))
		}
		for i, c := range callers {
			hdr := seen[i].Header
			want := "AWS4-HMAC-SHA256 Credential=" + c.Account + "/20000101/" + c.Region + "/dynamodb/aws4_request, "
			if got := hdr.Get("Authorization"); !strings.HasPrefix(got, want) {
				h.Errorf("Authorization = %q, want prefix %q", got, want)
			}
			for name, want := range map[string]string{
				"X-Amz-Target": "DynamoDB_20120810.ListTables", "Content-Type": "application/x-amz-json-1.0",
				"Accept-Encoding": "identity", "X-Amz-Date": "20000101T000000Z", "X-Amz-Security-Token": "",
			} {
				if got := hdr.Get(name); got != want {
					h.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		}
	})
}

// TestFromClientForwardsTheClientsBytes: a client request is forwarded as
// its own bytes, so Local decodes exactly what the client sent; an empty
// body is {} as Coral reads it; a gzipped body, which Local does not read,
// is encoded from the decoded input.
func TestFromClientForwardsTheClientsBytes(t *testing.T) {
	run(t, func(h *harness) {
		original := "{ \"TableName\" : \"Locks\",\n  \"Key\": {\"LockID\": {\"S\": \"a\\u00e9\"}}, \"Unknown\": 1 }"
		in := &dynamodbapi.GetItemInput{TableName: "Locks", Key: map[string]dynamodbapi.AttributeValue{"LockID": &dynamodbapi.AttributeValueMemberS{Value: "aé"}}}
		raw := kernel.RawRequest{Method: http.MethodPost, Path: "/", Header: target("GetItem"), Body: []byte(original)}
		if _, err := fromClient(h, dynamodbapi.OpGetItemRaw, in, raw); err != nil {
			h.Fatalf("GetItem: %s", err.Message)
		}
		if _, err := fromClient(h, dynamodbapi.OpListTablesRaw, &dynamodbapi.ListTablesInput{},
			kernel.RawRequest{Method: http.MethodPost, Path: "/", Header: target("ListTables")}); err != nil {
			h.Fatalf("ListTables: %s", err.Message)
		}
		var zipped bytes.Buffer
		zw := gzip.NewWriter(&zipped)
		if _, err := zw.Write([]byte(`{"Limit":5}`)); err != nil {
			h.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			h.Fatal(err)
		}
		gz := target("ListTables")
		gz.Set("Content-Encoding", "gzip")
		if _, err := fromClient(h, dynamodbapi.OpListTablesRaw, &dynamodbapi.ListTablesInput{Limit: new(int32(5))},
			kernel.RawRequest{Method: http.MethodPost, Path: "/", Header: gz, Body: zipped.Bytes()}); err != nil {
			h.Fatalf("ListTables gzipped: %s", err.Message)
		}
		got := h.local.requests()
		want := []string{original, "{}", `{"Limit":5}`}
		if len(got) != len(want) {
			h.Fatalf("Local received %d requests, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].Body != want[i] {
				h.Errorf("request %d body = %q, want %q", i, got[i].Body, want[i])
			}
		}
	})
}

// TestSynthesizedSendsTheModelOrderBody: an internal caller's input, which
// has no client bytes, is encoded through the awsJson codec: members in
// model order, compact, as a client writing model order sends it.
func TestSynthesizedSendsTheModelOrderBody(t *testing.T) {
	run(t, func(h *harness) {
		const (
			putItem = `{"TableName":"Locks","Item":{"ExpiresAt":{"N":"1791434323"},"Holder":{"M":{"blob":{"B":"AAEC/w=="},"none":{"NULL":true},"set":{"SS":["a","b"]},"tags":{"L":[{"BOOL":false},{"N":"-1.5e3"}]}}},"LockID":{"S":"job/1 <&>"}},` +
				`"ConditionExpression":"attribute_not_exists(LockID) OR ExpiresAt < :now","ExpressionAttributeValues":{":now":{"N":"1791434000"}}}`
			createTable = `{"AttributeDefinitions":[{"AttributeName":"LockID","AttributeType":"S"}],"TableName":"Locks","KeySchema":[{"AttributeName":"LockID","KeyType":"HASH"}],"BillingMode":"PAY_PER_REQUEST","Tags":[{"Key":"team","Value":""}]}`
		)
		if _, err := synthesize(h, dynamodbapi.OpCreateTableRaw, createTable); err != nil {
			h.Fatalf("CreateTable: %s", err.Message)
		}
		if _, err := synthesize(h, dynamodbapi.OpPutItemRaw, putItem); err != nil {
			h.Fatalf("PutItem: %s", err.Message)
		}
		want := []string{createTable, putItem}
		got := h.local.requests()
		if len(got) != len(want) {
			h.Fatalf("Local received %d requests, want %d", len(got), len(want))
		}
		for i, f := range got {
			if f.Body != want[i] {
				h.Errorf("%s forwarded\n%s\nwant the model-order encoding\n%s", f.Op, f.Body, want[i])
			}
		}
	})
}

// TestEdgeForwardsTheClientsBytes: through the edge, Local receives the
// client's exact bytes, spacing and a member the model does not know
// included (ADR 0014 amendment: byte-exact passthrough).
func TestEdgeForwardsTheClientsBytes(t *testing.T) {
	run(t, func(h *harness) {
		const body = "{ \"TableName\" : \"Locks\",\n  \"Key\": {\"LockID\": {\"S\": \"a\"}}, \"NewMember\": {\"S\": \"kept\"} }"
		if got := h.call("GetItem", body); got.Status != http.StatusOK {
			h.Fatalf("GetItem = %d %s", got.Status, got.Body)
		}
		seen := h.local.requests()
		if len(seen) != 1 || seen[0].Body != body {
			h.Fatalf("Local received %+v, want the client's bytes %q", seen, body)
		}
	})
}

// TestSynthesizedBodyMeansWhatTheClientSent: for any item a Go SDK writes
// (members in name order, map entries in any order), the body Local
// receives is the same JSON value the client sent.
func TestSynthesizedBodyMeansWhatTheClientSent(t *testing.T) {
	run(t, func(h *harness) {
		rapid.Check(bubbleTB{h.T}, func(rt *rapid.T) {
			item := rapid.MapOfN(rapid.StringMatching(`[a-zA-Z#:_]{1,8}`), attributeValue(3), 1, 4).Draw(rt, "item")
			body := `{"Item":` + sdkJSON(rt, item) + `,"TableName":"Locks"}`
			n := len(h.local.requests())
			if _, err := synthesize(h, dynamodbapi.OpPutItemRaw, body); err != nil {
				rt.Fatalf("PutItem %s: %s", body, err.Message)
			}
			seen := h.local.requests()
			if len(seen) != n+1 {
				rt.Fatalf("Local received %d new requests, want 1", len(seen)-n)
			}
			var want, got any
			if err := json.Unmarshal([]byte(body), &want); err != nil {
				rt.Fatal(err)
			}
			if err := json.Unmarshal([]byte(seen[n].Body), &got); err != nil {
				rt.Fatalf("Local received %q, not JSON: %v", seen[n].Body, err)
			}
			if !reflect.DeepEqual(got, want) {
				rt.Fatalf("Local received\n%s\nfor the client's\n%s", seen[n].Body, body)
			}
		})
	})
}

// attributeValue draws a DynamoDB attribute value as a JSON-ready tree:
// one member of the AttributeValue union, nested up to depth.
func attributeValue(depth int) *rapid.Generator[map[string]any] {
	return rapid.Custom(func(t *rapid.T) map[string]any {
		kinds := []string{"S", "N", "B", "BOOL", "NULL", "SS", "NS"}
		if depth > 0 {
			kinds = append(kinds, "M", "L")
		}
		text := rapid.StringMatching(`[ -~\x{e9}\x{2028}]{0,6}`)
		number := rapid.StringMatching(`-?[1-9][0-9]{0,5}(\.[0-9]{1,3})?(e[+-]?[0-9])?`)
		switch kind := rapid.SampledFrom(kinds).Draw(t, "kind"); kind {
		case "S":
			return map[string]any{kind: text.Draw(t, "s")}
		case "N":
			return map[string]any{kind: number.Draw(t, "n")}
		case "B":
			return map[string]any{kind: base64.StdEncoding.EncodeToString(rapid.SliceOfN(rapid.Byte(), 0, 6).Draw(t, "b"))}
		case "BOOL":
			return map[string]any{kind: rapid.Bool().Draw(t, "bool")}
		case "NULL":
			return map[string]any{kind: true}
		case "SS":
			return map[string]any{kind: rapid.SliceOfNDistinct(text, 1, 3, rapid.ID).Draw(t, "ss")}
		case "NS":
			return map[string]any{kind: rapid.SliceOfNDistinct(number, 1, 3, rapid.ID).Draw(t, "ns")}
		case "M":
			return map[string]any{kind: rapid.MapOfN(rapid.StringMatching(`[a-z]{1,4}`), attributeValue(depth-1), 0, 3).Draw(t, "m")}
		default:
			return map[string]any{kind: rapid.SliceOfN(attributeValue(depth-1), 0, 3).Draw(t, "l")}
		}
	})
}

// sdkJSON writes v compactly with object members in an order t draws, as
// an SDK iterating a Go map does.
func sdkJSON(t *rapid.T, v any) string {
	switch v := v.(type) {
	case map[string]any:
		keys := rapid.Permutation(slices.Sorted(maps.Keys(v))).Draw(t, "member order")
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = sdkJSON(t, k) + ":" + sdkJSON(t, v[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	case map[string]map[string]any:
		m := make(map[string]any, len(v))
		for k, x := range v {
			m[k] = x
		}
		return sdkJSON(t, m)
	case []map[string]any:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = sdkJSON(t, x)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case []string:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = sdkJSON(t, x)
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("sdkJSON of %T: %v", v, err)
		}
		return string(b)
	}
}

// bubbleTB runs rapid inside a synctest bubble: it hides
// testing.T.Deadline, which panics there.
type bubbleTB struct{ testing.TB }
