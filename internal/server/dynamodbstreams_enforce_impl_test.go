package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/server"
)

// streamsTarget is the X-Amz-Target prefix of DynamoDB Streams, which
// signs as dynamodb (dynamodb-streams.md 3.2).
const streamsTarget = "DynamoDBStreams_20120810."

// The streams these tests name; no stream need exist: authorization runs
// before the handler (step 8), which answers 501 while DynamoDB is off.
const (
	ordersStream = "arn:aws:dynamodb:us-east-1:000000000000:table/Orders/stream/2026-10-27T00:00:00.000"
	otherStream  = "arn:aws:dynamodb:us-east-1:000000000000:table/Other/stream/2026-10-27T00:00:00.000"
	// ordersShard has the documented shard id form, long enough for the
	// model's 28-character minimum (dynamodb-streams.md 4.2).
	ordersShard = "shardId-00000001761523200000-1a2b3c4d"
)

// streamsCall sends a Streams operation signed by s for dynamodb.
func streamsCall(t *testing.T, s signer, endpoint, op, body string) wire {
	t.Helper()
	return jsonCall(t, s, endpoint, "dynamodb", "1.0", streamsTarget+op, body)
}

// isStreamsDenial reports whether w is the Streams denial identity:
// AccessDeniedException at 400, DynamoDB's (iam-enforcement-surfaces.md
// 12.3; inferred, IAM-ENF-35).
func isStreamsDenial(w wire) bool {
	return w.status == http.StatusBadRequest && strings.HasSuffix(jsonType(w), "AccessDeniedException")
}

// The Streams denial identity the registry declares is the one
// isStreamsDenial looks for.
func TestStreamsDenialIdentityIsDynamoDBs(t *testing.T) {
	id := dynamodbstreamsapi.DenialError()
	if id.Status != http.StatusBadRequest || !strings.HasSuffix(string(id.Shape), "#AccessDeniedException") {
		t.Errorf("Streams denial identity = %s %d, want AccessDeniedException 400", id.Shape, id.Status)
	}
}

// Spec enforce matrix row 1: a principal's Streams call is denied under
// enforce with DynamoDB's identity unless its identity policy allows the
// call's action on the stream (ListStreams on "*"), served with an
// access-would-deny advisory under advise, and served unchanged under off.
func TestStreamsCallsUnderEachEnforceMode(t *testing.T) {
	calls := map[string]string{
		"ListStreams":      `{}`,
		"DescribeStream":   `{"StreamArn":"` + ordersStream + `"}`,
		"GetShardIterator": `{"StreamArn":"` + ordersStream + `","ShardId":"` + ordersShard + `","ShardIteratorType":"TRIM_HORIZON"}`,
		"GetRecords":       `{"ShardIterator":"` + ordersStream + `|1|AAAA"}`,
	}

	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	for op, body := range calls {
		if w := streamsCall(t, u, endpoint, op, body); !isStreamsDenial(w) {
			t.Errorf("enforce, no policy: %s = %d %s, want 400 AccessDeniedException", op, w.status, w.body)
		}
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":"dynamodb:ListStreams","Resource":"*"},`+
		`{"Effect":"Allow","Action":["dynamodb:DescribeStream","dynamodb:GetShardIterator","dynamodb:GetRecords"],"Resource":"`+ordersStream+`"}]}`)
	for op, body := range calls {
		if w := streamsCall(t, u, endpoint, op, body); isStreamsDenial(w) {
			t.Errorf("enforce, allowed on the stream: %s = %d %s, want it past authorization", op, w.status, w.body)
		}
	}
	for op, body := range map[string]string{
		"DescribeStream": `{"StreamArn":"` + otherStream + `"}`,
		"GetRecords":     `{"ShardIterator":"` + otherStream + `|1|AAAA"}`,
	} {
		if w := streamsCall(t, u, endpoint, op, body); !isStreamsDenial(w) {
			t.Errorf("enforce, another stream: %s = %d %s, want 400 AccessDeniedException", op, w.status, w.body)
		}
	}

	c, endpoint, u := enforced(t, server.IAMEnforceAdvise)
	if w := streamsCall(t, u, endpoint, "DescribeStream", calls["DescribeStream"]); isStreamsDenial(w) {
		t.Errorf("advise DescribeStream = %d %s, want it served", w.status, w.body)
	}
	if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 1 || !strings.Contains(got[0], "dynamodb:DescribeStream") {
		t.Errorf("advise access-would-deny advisories = %q, want one for dynamodb:DescribeStream", got)
	}

	c, endpoint, u = enforced(t, server.IAMEnforceOff)
	if w := streamsCall(t, u, endpoint, "DescribeStream", calls["DescribeStream"]); isStreamsDenial(w) {
		t.Errorf("off DescribeStream = %d %s, want it served", w.status, w.body)
	}
	for _, kind := range []string{"access-denied", "access-would-deny"} {
		if got := enforcementAdvisories(t, c, kind); len(got) != 0 {
			t.Errorf("off journaled %s advisories %q, want none", kind, got)
		}
	}
}

// GET /iam/policy for a principal that called the four Streams operations
// grants each its action on the stream it read, and ListStreams on "*"
// (spec A11), whatever the calls answered.
func TestIAMPolicyListsTheStreamsActions(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	streamsCall(t, u, endpoint, "ListStreams", `{}`)
	streamsCall(t, u, endpoint, "DescribeStream", `{"StreamArn":"`+ordersStream+`"}`)
	streamsCall(t, u, endpoint, "GetShardIterator", `{"StreamArn":"`+ordersStream+`","ShardId":"`+ordersShard+`","ShardIteratorType":"TRIM_HORIZON"}`)
	streamsCall(t, u, endpoint, "GetRecords", `{"ShardIterator":"`+ordersStream+`|1|AAAA"}`)
	p, err := c.IAMPolicy(t.Context(), userU)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Statement []struct {
			Action   any
			Resource any
		}
	}
	if err := json.Unmarshal(p.Document, &doc); err != nil {
		t.Fatalf("policy %s: %v", p.Document, err)
	}
	granted := map[string][]string{}
	for _, st := range doc.Statement {
		for _, r := range asStrings(st.Resource) {
			granted[r] = append(granted[r], asStrings(st.Action)...)
		}
	}
	for r := range granted {
		slices.Sort(granted[r])
	}
	// The stream label is minted, so the policy names the table's streams
	// (ARCHITECTURE.md 17).
	want := map[string][]string{
		"*": {"dynamodb:ListStreams"},
		"arn:aws:dynamodb:us-east-1:000000000000:table/Orders/stream/*": {"dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator"},
	}
	for r, actions := range want {
		if !slices.Equal(granted[r], actions) {
			t.Errorf("policy grants %q on %s, want %q (document %s)", granted[r], r, actions, p.Document)
		}
	}
}

// asStrings reads a policy element that is a string or a list of them.
func asStrings(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
