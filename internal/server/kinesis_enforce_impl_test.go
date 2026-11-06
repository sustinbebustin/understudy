package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/server"
)

// ordersKinesis is the stream these tests name, in the signer's account
// and region (kinesis.md 3.1).
const ordersKinesis = "arn:aws:kinesis:us-east-1:000000000000:stream/orders"

// kinesisCall sends a Kinesis operation signed by s.
func kinesisCall(t *testing.T, s signer, endpoint, op, body string) wire {
	t.Helper()
	return jsonCall(t, s, endpoint, "kinesis", "1.1", "Kinesis_20131202."+op, body)
}

// isKinesisDenial reports whether w is the Kinesis denial identity: the
// model's AccessDeniedException at 400 (08b contract 3.8; KIN-27,
// IAM-ENF-36).
func isKinesisDenial(w wire) bool {
	return w.status == http.StatusBadRequest && jsonType(w) == "AccessDeniedException"
}

// The Kinesis denial identity the registry declares is the one
// isKinesisDenial looks for.
func TestKinesisDenialIdentityIsTheModels(t *testing.T) {
	id := kinesisapi.DenialError()
	if id.Status != http.StatusBadRequest || !strings.HasSuffix(string(id.Shape), "#AccessDeniedException") {
		t.Errorf("Kinesis denial identity = %s %d, want AccessDeniedException 400", id.Shape, id.Status)
	}
}

// granted reads principal's GET /iam/policy as the actions it grants on
// each resource, sorted.
func granted(t *testing.T, c *admin.Client, principal string) map[string][]string {
	t.Helper()
	p, err := c.IAMPolicy(t.Context(), principal)
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
	out := map[string][]string{}
	for _, st := range doc.Statement {
		for _, r := range asStrings(st.Resource) {
			out[r] = append(out[r], asStrings(st.Action)...)
		}
	}
	for r := range out {
		slices.Sort(out[r])
		out[r] = slices.Compact(out[r])
	}
	return out
}

// Spec enforce matrix row 1 for Kinesis: under enforce a principal with no
// policy is refused every bound operation with the model's
// AccessDeniedException; a policy allowing the stream's actions on its ARN
// (ListStreams and DescribeLimits on "*") lets the calls past
// authorization, by name or by ARN, but not on another stream; advise
// serves the call with an access-would-deny advisory, off serves it
// silently.
func TestKinesisCallsUnderEachEnforceMode(t *testing.T) {
	byName := `{"StreamName":"orders"}`
	calls := map[string]string{
		"CreateStream":                  `{"StreamName":"orders","ShardCount":1}`,
		"DescribeStream":                byName,
		"DescribeStreamSummary":         `{"StreamARN":"` + ordersKinesis + `"}`,
		"ListShards":                    byName,
		"IncreaseStreamRetentionPeriod": `{"StreamName":"orders","RetentionPeriodHours":48}`,
		"DecreaseStreamRetentionPeriod": `{"StreamName":"orders","RetentionPeriodHours":24}`,
		"PutRecord":                     `{"StreamName":"orders","PartitionKey":"k","Data":"YQ=="}`,
		"PutRecords":                    `{"StreamName":"orders","Records":[{"PartitionKey":"k","Data":"YQ=="}]}`,
		"GetShardIterator":              `{"StreamName":"orders","ShardId":"shardId-000000000000","ShardIteratorType":"TRIM_HORIZON"}`,
		"AddTagsToStream":               `{"StreamName":"orders","Tags":{"a":"b"}}`,
		"RemoveTagsFromStream":          `{"StreamName":"orders","TagKeys":["a"]}`,
		"ListTagsForStream":             byName,
		"TagResource":                   `{"ResourceARN":"` + ordersKinesis + `","Tags":{"a":"b"}}`,
		"UntagResource":                 `{"ResourceARN":"` + ordersKinesis + `","TagKeys":["a"]}`,
		"ListTagsForResource":           `{"ResourceARN":"` + ordersKinesis + `"}`,
		"EnableEnhancedMonitoring":      `{"StreamName":"orders","ShardLevelMetrics":["IncomingBytes"]}`,
		"DisableEnhancedMonitoring":     `{"StreamName":"orders","ShardLevelMetrics":["IncomingBytes"]}`,
		"ListStreams":                   `{}`,
		"DescribeLimits":                `{}`,
		"DeleteStream":                  byName,
	}
	actions := make([]string, 0, len(calls))
	for op := range calls {
		if op != "ListStreams" && op != "DescribeLimits" {
			actions = append(actions, `"kinesis:`+op+`"`)
		}
	}
	actions = append(actions, `"kinesis:GetRecords"`)

	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	for op, body := range calls {
		if w := kinesisCall(t, u, endpoint, op, body); !isKinesisDenial(w) {
			t.Errorf("enforce, no policy: %s = %d %s, want 400 AccessDeniedException", op, w.status, w.body)
		}
	}
	if w := kinesisCall(t, u, endpoint, "GetRecords", `{"ShardIterator":"AAAAAAAAAAE="}`); !isKinesisDenial(w) {
		t.Errorf("enforce, no policy: GetRecords = %d %s, want 400 AccessDeniedException", w.status, w.body)
	}

	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["kinesis:ListStreams","kinesis:DescribeLimits"],"Resource":"*"},`+
		`{"Effect":"Allow","Action":[`+strings.Join(actions, ",")+`],"Resource":"`+ordersKinesis+`"}]}`)
	// In order, so each call finds the stream it needs and GetRecords
	// reads a real iterator.
	for _, op := range []string{
		"CreateStream", "DescribeStream", "DescribeStreamSummary", "ListShards", "IncreaseStreamRetentionPeriod", "DecreaseStreamRetentionPeriod",
		"PutRecord", "PutRecords", "AddTagsToStream", "RemoveTagsFromStream", "ListTagsForStream", "TagResource", "UntagResource",
		"ListTagsForResource", "EnableEnhancedMonitoring", "DisableEnhancedMonitoring", "ListStreams", "DescribeLimits",
	} {
		if w := kinesisCall(t, u, endpoint, op, calls[op]); w.status != http.StatusOK {
			t.Errorf("enforce, allowed on the stream: %s = %d %s, want 200", op, w.status, w.body)
		}
	}
	it := kinesisCall(t, u, endpoint, "GetShardIterator", calls["GetShardIterator"])
	var iterator struct{ ShardIterator string }
	if err := json.Unmarshal([]byte(it.body), &iterator); it.status != http.StatusOK || err != nil {
		t.Fatalf("enforce, allowed: GetShardIterator = %d %s", it.status, it.body)
	}
	if w := kinesisCall(t, u, endpoint, "GetRecords", `{"ShardIterator":"`+iterator.ShardIterator+`"}`); w.status != http.StatusOK {
		t.Errorf("enforce, allowed on the stream: GetRecords = %d %s, want 200", w.status, w.body)
	}
	// An iterator in one pair of double quotes, which the handler accepts
	// (R-weak, kinesis.md 3.5), is asked on the same stream ARN.
	if w := kinesisCall(t, u, endpoint, "GetRecords", `{"ShardIterator":"\"`+iterator.ShardIterator+`\""}`); w.status != http.StatusOK {
		t.Errorf("enforce, allowed on the stream: GetRecords with a quoted iterator = %d %s, want 200", w.status, w.body)
	}
	for op, body := range map[string]string{
		"DescribeStreamSummary": `{"StreamName":"other"}`,
		"PutRecord":             `{"StreamARN":"arn:aws:kinesis:us-east-1:000000000000:stream/other","PartitionKey":"k","Data":"YQ=="}`,
		"ListTagsForResource":   `{"ResourceARN":"arn:aws:kinesis:us-east-1:000000000000:stream/other"}`,
	} {
		if w := kinesisCall(t, u, endpoint, op, body); !isKinesisDenial(w) {
			t.Errorf("enforce, another stream: %s = %d %s, want 400 AccessDeniedException", op, w.status, w.body)
		}
	}
	if w := kinesisCall(t, u, endpoint, "DeleteStream", calls["DeleteStream"]); w.status != http.StatusOK {
		t.Errorf("enforce, allowed on the stream: DeleteStream = %d %s, want 200", w.status, w.body)
	}

	c, endpoint, u := enforced(t, server.IAMEnforceAdvise)
	if w := kinesisCall(t, u, endpoint, "ListStreams", `{}`); w.status != http.StatusOK {
		t.Errorf("advise ListStreams = %d %s, want it served", w.status, w.body)
	}
	if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 1 || !strings.Contains(got[0], "kinesis:ListStreams") {
		t.Errorf("advise access-would-deny advisories = %q, want one for kinesis:ListStreams", got)
	}

	c, endpoint, u = enforced(t, server.IAMEnforceOff)
	if w := kinesisCall(t, u, endpoint, "ListStreams", `{}`); w.status != http.StatusOK {
		t.Errorf("off ListStreams = %d %s, want it served", w.status, w.body)
	}
	for _, kind := range []string{"access-denied", "access-would-deny"} {
		if got := enforcementAdvisories(t, c, kind); len(got) != 0 {
			t.Errorf("off journaled %s advisories %q, want none", kind, got)
		}
	}
}

// GET /iam/policy for a principal that called each of the 21 bound
// operations grants each its own action on the stream it named, by name,
// by ARN, by ResourceARN or inside a GetRecords iterator or a ListShards
// NextToken, and ListStreams and DescribeLimits on "*" (spec B6).
func TestIAMPolicyListsTheKinesisActions(t *testing.T) {
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	for _, op := range [][2]string{
		{"CreateStream", `{"StreamName":"orders","ShardCount":2}`},
		{"DescribeStream", `{"StreamName":"orders"}`},
		{"DescribeStreamSummary", `{"StreamARN":"` + ordersKinesis + `"}`},
		{"IncreaseStreamRetentionPeriod", `{"StreamName":"orders","RetentionPeriodHours":48}`},
		{"DecreaseStreamRetentionPeriod", `{"StreamARN":"` + ordersKinesis + `","RetentionPeriodHours":24}`},
		{"PutRecord", `{"StreamName":"orders","PartitionKey":"k","Data":"YQ=="}`},
		{"PutRecords", `{"StreamARN":"` + ordersKinesis + `","Records":[{"PartitionKey":"k","Data":"YQ=="}]}`},
		{"AddTagsToStream", `{"StreamName":"orders","Tags":{"a":"b"}}`},
		{"RemoveTagsFromStream", `{"StreamName":"orders","TagKeys":["a"]}`},
		{"ListTagsForStream", `{"StreamName":"orders"}`},
		{"TagResource", `{"ResourceARN":"` + ordersKinesis + `","Tags":{"a":"b"}}`},
		{"UntagResource", `{"ResourceARN":"` + ordersKinesis + `","TagKeys":["a"]}`},
		{"ListTagsForResource", `{"ResourceARN":"` + ordersKinesis + `"}`},
		{"EnableEnhancedMonitoring", `{"StreamName":"orders","ShardLevelMetrics":["IncomingBytes"]}`},
		{"DisableEnhancedMonitoring", `{"StreamName":"orders","ShardLevelMetrics":["IncomingBytes"]}`},
		{"ListStreams", `{}`},
		{"DescribeLimits", `{}`},
	} {
		if w := kinesisCall(t, u, endpoint, op[0], op[1]); w.status != http.StatusOK {
			t.Fatalf("%s = %d %s", op[0], w.status, w.body)
		}
	}
	var page struct{ NextToken string }
	if w := kinesisCall(t, u, endpoint, "ListShards", `{"StreamName":"orders","MaxResults":1}`); json.Unmarshal([]byte(w.body), &page) != nil || page.NextToken == "" {
		t.Fatalf("ListShards = %d %s, want a NextToken", w.status, w.body)
	}
	var iterator struct{ ShardIterator string }
	w := kinesisCall(t, u, endpoint, "GetShardIterator", `{"StreamName":"orders","ShardId":"shardId-000000000000","ShardIteratorType":"TRIM_HORIZON"}`)
	if json.Unmarshal([]byte(w.body), &iterator) != nil || iterator.ShardIterator == "" {
		t.Fatalf("GetShardIterator = %d %s", w.status, w.body)
	}
	// Another user, who names the stream only inside the token and the
	// iterator.
	v := accessKey(t, endpoint, "v")
	for _, op := range [][2]string{
		{"ListShards", `{"NextToken":"` + page.NextToken + `"}`},
		{"GetRecords", `{"ShardIterator":"` + iterator.ShardIterator + `"}`},
	} {
		if w := kinesisCall(t, v, endpoint, op[0], op[1]); w.status != http.StatusOK {
			t.Fatalf("%s = %d %s", op[0], w.status, w.body)
		}
	}
	if w := kinesisCall(t, u, endpoint, "DeleteStream", `{"StreamName":"orders"}`); w.status != http.StatusOK {
		t.Fatalf("DeleteStream = %d %s", w.status, w.body)
	}

	want := map[string][]string{
		"*": {"kinesis:DescribeLimits", "kinesis:ListStreams"},
		ordersKinesis: {
			"kinesis:AddTagsToStream", "kinesis:CreateStream", "kinesis:DecreaseStreamRetentionPeriod", "kinesis:DeleteStream",
			"kinesis:DescribeStream", "kinesis:DescribeStreamSummary", "kinesis:DisableEnhancedMonitoring", "kinesis:EnableEnhancedMonitoring",
			"kinesis:GetShardIterator", "kinesis:IncreaseStreamRetentionPeriod", "kinesis:ListShards", "kinesis:ListTagsForResource",
			"kinesis:ListTagsForStream", "kinesis:PutRecord", "kinesis:PutRecords", "kinesis:RemoveTagsFromStream", "kinesis:TagResource",
			"kinesis:UntagResource",
		},
	}
	got := granted(t, c, userU)
	for r, actions := range want {
		if !slices.Equal(got[r], actions) {
			t.Errorf("u's policy grants %q on %s, want %q", got[r], r, actions)
		}
	}
	if len(got) != len(want) {
		t.Errorf("u's policy grants on %d resources (%v), want %d", len(got), got, len(want))
	}
	if got, want := granted(t, c, "arn:aws:iam::000000000000:user/v")[ordersKinesis], []string{"kinesis:GetRecords", "kinesis:ListShards"}; !slices.Equal(got, want) {
		t.Errorf("v's policy grants %q on the stream, want %q", got, want)
	}
}

// CreateStream with Tags needs kinesis:AddTagsToStream besides its own
// action, and kinesis:TagResource does not stand in for it (kinesis.md
// 4.1); without Tags it needs only kinesis:CreateStream. GET /iam/policy
// grants exactly that, and under enforce a policy without
// kinesis:AddTagsToStream refuses the tagged create alone.
func TestCreateStreamWithTagsNeedsAddTagsToStream(t *testing.T) {
	const tagged = "arn:aws:kinesis:us-east-1:000000000000:stream/tagged"
	const plain = "arn:aws:kinesis:us-east-1:000000000000:stream/plain"
	c, endpoint, u := enforced(t, server.IAMEnforceOff)
	kinesisCall(t, u, endpoint, "CreateStream", `{"StreamName":"tagged","ShardCount":1,"Tags":{"team":"a"}}`)
	kinesisCall(t, u, endpoint, "CreateStream", `{"StreamName":"plain","ShardCount":1}`)
	got := granted(t, c, userU)
	if want := []string{"kinesis:AddTagsToStream", "kinesis:CreateStream"}; !slices.Equal(got[tagged], want) {
		t.Errorf("policy grants %q on the tagged stream, want %q", got[tagged], want)
	}
	if want := []string{"kinesis:CreateStream"}; !slices.Equal(got[plain], want) {
		t.Errorf("policy grants %q on the plain stream, want %q", got[plain], want)
	}

	_, endpoint, u = enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["kinesis:CreateStream","kinesis:TagResource"],"Resource":"*"}]}`)
	if w := kinesisCall(t, u, endpoint, "CreateStream", `{"StreamName":"tagged","ShardCount":1,"Tags":{"team":"a"}}`); !isKinesisDenial(w) {
		t.Errorf("tagged CreateStream without kinesis:AddTagsToStream = %d %s, want 400 AccessDeniedException", w.status, w.body)
	}
	if w := kinesisCall(t, u, endpoint, "CreateStream", `{"StreamName":"plain","ShardCount":1}`); w.status != http.StatusOK {
		t.Errorf("CreateStream without Tags = %d %s, want 200", w.status, w.body)
	}
}
