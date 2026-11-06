package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Service surfaces under iam.enforce=enforce: operations that resolve their
// resource by name are decided on the resolved resource (contract 5.3,
// 11.2, 11.4), CopyObject asks about each action on the resource it applies
// to (iam-enforcement-surfaces.md 12.6), and an EventBridge delivery the
// target refuses goes to the target's dead-letter queue (8.4).

// wiredSwitch is the value the running server reports for switch name.
func wiredSwitch(t *testing.T, c *admin.Client, name string) string {
	t.Helper()
	data, err := c.Coverage(t.Context(), "json")
	if err != nil {
		t.Fatal(err)
	}
	report, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range report.Switches {
		if s.Name == name {
			return s.Value
		}
	}
	t.Fatalf("the server reports no switch %s", name)
	return ""
}

// An IAM operation that names its user by name is decided on the user's
// ARN once IAM resolved it, so a policy granting iam:GetUser on the
// caller's own ARN allows reading itself and no other user (the policy's
// Resource matches the request's resource, iam-evaluation.md 5.3); a user
// that does not exist answers NoSuchEntity first (contract section 0,
// iam.enforce.missing-resource).
func TestEnforceDecidesIAMCallsOnTheResolvedUser(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	accessKey(t, endpoint, "v")
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:GetUser","Resource":"arn:aws:iam::000000000000:user/u"}]}`)
	get := func(name string) wire {
		return queryCall(t, u, endpoint, "iam", url.Values{"Action": {"GetUser"}, "Version": {"2010-05-08"}, "UserName": {name}})
	}
	if w := get("u"); w.status != http.StatusOK {
		t.Errorf("GetUser of itself = %d %s, want 200", w.status, w.body)
	}
	if w := get("v"); w.status != http.StatusForbidden || parseXMLError(t, w).Code != "AccessDenied" {
		t.Errorf("GetUser of another user = %d %s, want the IAM denial", w.status, w.body)
	}
	if w := get("ghost"); w.status != http.StatusNotFound || parseXMLError(t, w).Code != "NoSuchEntity" {
		t.Errorf("GetUser of a missing user = %d %s, want NoSuchEntity", w.status, w.body)
	}
}

// A KMS call that names its key by alias is decided on the key the alias
// resolves to (contract 11.4): a grant on the key's ARN covers the alias,
// and another key stays denied.
func TestEnforceDecidesKMSCallsOnTheResolvedKey(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	keyARN := func(body string) string {
		var out struct{ KeyMetadata struct{ Arn string } }
		if err := json.Unmarshal([]byte(body), &out); err != nil || out.KeyMetadata.Arn == "" {
			t.Fatalf("CreateKey = %s", body)
		}
		return out.KeyMetadata.Arn
	}
	first := jsonCall(t, dummy, endpoint, "kms", "1.1", "TrentService.CreateKey", "{}")
	second := jsonCall(t, dummy, endpoint, "kms", "1.1", "TrentService.CreateKey", "{}")
	k1, k2 := keyARN(first.body), keyARN(second.body)
	if w := jsonCall(t, dummy, endpoint, "kms", "1.1", "TrentService.CreateAlias", `{"AliasName":"alias/app","TargetKeyId":"`+k1+`"}`); w.status != http.StatusOK {
		t.Fatalf("CreateAlias = %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:DescribeKey","Resource":"`+k1+`"}]}`)
	if w := jsonCall(t, u, endpoint, "kms", "1.1", "TrentService.DescribeKey", `{"KeyId":"alias/app"}`); w.status != http.StatusOK {
		t.Errorf("DescribeKey by alias = %d %s, want 200", w.status, w.body)
	}
	if w := jsonCall(t, u, endpoint, "kms", "1.1", "TrentService.DescribeKey", `{"KeyId":"`+k2+`"}`); w.status != kmsapi.DenialError().Status || jsonType(w) != "AccessDeniedException" {
		t.Errorf("DescribeKey of another key = %d %s, want the KMS denial", w.status, w.body)
	}
}

// CopyObject needs s3:GetObject on its source and s3:PutObject on its
// destination (iam-enforcement-surfaces.md 8.7 documents the split for the
// KMS actions; 12.6 pairs the S3 actions likewise), so a policy granting
// exactly those copies, and one without the source read does not.
func TestEnforceScopesCopyObjectToItsSourceAndDestination(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	for _, path := range []string{"/src", "/dst"} {
		if got := s3Call(t, dummy, http.MethodPut, endpoint+path, ""); got.status != http.StatusOK {
			t.Fatalf("CreateBucket %s = %d %s", path, got.status, got.body)
		}
	}
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/src/a.txt", "abc"); got.status != http.StatusOK {
		t.Fatalf("PutObject = %d %s", got.status, got.body)
	}
	copyObject := func() wire {
		req := newRequest(t, http.MethodPut, endpoint+"/dst/b.txt", "")
		req.Header.Set("X-Amz-Copy-Source", "/src/a.txt")
		return signedCall(t, u, "s3", req, "")
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::src/*"},`+
		`{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::dst/*"}]}`)
	if w := copyObject(); w.status != http.StatusOK {
		t.Errorf("CopyObject with GetObject on the source and PutObject on the destination = %d %s, want 200", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::dst/*"}]}`)
	if w := copyObject(); w.status != http.StatusForbidden || parseXMLError(t, w).S3Code != "AccessDenied" {
		t.Errorf("CopyObject without GetObject on the source = %d %s, want 403 AccessDenied", w.status, w.body)
	}
}

// An EventBridge target whose queue policy does not admit events.amazonaws.com
// is not retried; the event goes straight to the target's dead-letter queue
// with the failure in its message attributes (iam-enforcement-surfaces.md
// 8.4, aws-doc). Which ERROR_CODE a refusal carries is UNVERIFIED
// (IAM-ENF-22), so it is read from switch events.delivery.denied-code.
func TestEnforceDeadLettersAnEventBridgeDeliveryTheTargetRefuses(t *testing.T) {
	c, endpoint, _ := enforced(t, server.IAMEnforceEnforce)
	const ruleARN = "arn:aws:events:us-east-1:000000000000:rule/nightly"
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"target"}`)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"events-dlq"}`)
	dlqPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage",` +
		`"Resource":"arn:aws:sqs:us-east-1:000000000000:events-dlq","Condition":{"ArnEquals":{"aws:SourceArn":"` + ruleARN + `"}}}]}`
	attrs, err := json.Marshal(map[string]any{"QueueUrl": endpoint + "/000000000000/events-dlq", "Attributes": map[string]string{"Policy": dlqPolicy}})
	if err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "SetQueueAttributes", string(attrs))
	events := func(target, body string) wire {
		return jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents."+target, body)
	}
	if w := events("PutRule", `{"Name":"nightly","EventPattern":"{\"source\":[\"app\"]}"}`); w.status != http.StatusOK {
		t.Fatalf("PutRule = %d %s", w.status, w.body)
	}
	if w := events("PutTargets", `{"Rule":"nightly","Targets":[{"Id":"q","Arn":"arn:aws:sqs:us-east-1:000000000000:target","DeadLetterConfig":{"Arn":"arn:aws:sqs:us-east-1:000000000000:events-dlq"}}]}`); w.status != http.StatusOK {
		t.Fatalf("PutTargets = %d %s", w.status, w.body)
	}
	if w := events("PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`); w.status != http.StatusOK {
		t.Fatalf("PutEvents = %d %s", w.status, w.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	target := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/target","MaxNumberOfMessages":10}`)
	if strings.Contains(target, `"Messages"`) && !strings.Contains(target, `"Messages":[]`) {
		t.Errorf("the refusing target received %s, want nothing", target)
	}
	letters := mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+endpoint+`/000000000000/events-dlq","MaxNumberOfMessages":10,"MessageAttributeNames":["All"]}`)
	var out struct {
		Messages []struct {
			MessageAttributes map[string]struct{ StringValue string }
		}
	}
	if err := json.Unmarshal([]byte(letters), &out); err != nil || len(out.Messages) != 1 {
		t.Fatalf("dead-letter queue = %s, want one dead letter", letters)
	}
	want := wiredSwitch(t, c, "events.delivery.denied-code")
	got := out.Messages[0].MessageAttributes
	if got["ERROR_CODE"].StringValue != want || got["RULE_ARN"].StringValue != ruleARN || got["RETRY_ATTEMPTS"].StringValue != "0" {
		t.Errorf("dead letter attributes = %+v, want ERROR_CODE %q (events.delivery.denied-code), the rule's ARN and no retry", got, want)
	}
}
