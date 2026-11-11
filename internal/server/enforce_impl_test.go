package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/server"
)

// iam.enforce beyond the blind tests in enforce_test.go and
// enforce_surfaces_test.go: writes that name their resource by name are
// decided on what IAM, KMS and Secrets Manager resolved; a creation on the
// ARN it creates; PutEvents on its bus; DynamoDB's denial identity; and the
// switches the denial texts follow, reported by the running server.

// A write that names a user is decided on the user's ARN with its path,
// once IAM resolved it, and a creation on the ARN it creates (iam-evaluation.md
// 5.3: the policy's Resource matches the request's resource).
func TestEnforceDecidesIAMWritesOnTheResolvedARN(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	iamCall := func(form url.Values) wire {
		form.Set("Version", "2010-05-08")
		return queryCall(t, u, endpoint, "iam", form)
	}
	for _, name := range []string{"keep", "drop"} {
		if got := query(t, dummy, endpoint, "iam", url.Values{"Action": {"CreateUser"}, "Version": {"2010-05-08"}, "UserName": {name}, "Path": {"/team/"}}); got.status != http.StatusOK {
			t.Fatalf("CreateUser %s = %d %s", name, got.status, got.body)
		}
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":["iam:TagUser","iam:DeleteUser"],"Resource":"arn:aws:iam::000000000000:user/team/drop"},`+
		`{"Effect":"Allow","Action":"iam:CreateUser","Resource":"arn:aws:iam::000000000000:user/team/new"}]}`)
	if w := iamCall(url.Values{"Action": {"TagUser"}, "UserName": {"drop"}, "Tags.member.1.Key": {"k"}, "Tags.member.1.Value": {"v"}}); w.status != http.StatusOK {
		t.Errorf("TagUser of the granted user = %d %s, want 200", w.status, w.body)
	}
	if w := iamCall(url.Values{"Action": {"TagUser"}, "UserName": {"keep"}, "Tags.member.1.Key": {"k"}, "Tags.member.1.Value": {"v"}}); w.status != http.StatusForbidden {
		t.Errorf("TagUser of another user = %d %s, want the IAM denial", w.status, w.body)
	}
	// A missing user is denied first, as GetRole of a missing role is
	// (inferred from IAM-ENF-14, iam-enforcement-surfaces.md 5.3).
	if w := iamCall(url.Values{"Action": {"DeleteUser"}, "UserName": {"gone"}}); w.status != http.StatusForbidden || !strings.Contains(parseXMLError(t, w).Message, "iam:DeleteUser on resource: user gone ") {
		t.Errorf("DeleteUser of a missing user = %d %s, want the IAM denial naming user gone", w.status, w.body)
	}
	if w := iamCall(url.Values{"Action": {"DeleteUser"}, "UserName": {"drop"}}); w.status != http.StatusOK {
		t.Errorf("DeleteUser of the granted user = %d %s, want 200", w.status, w.body)
	}
	if w := iamCall(url.Values{"Action": {"CreateUser"}, "UserName": {"new"}, "Path": {"/team/"}}); w.status != http.StatusOK {
		t.Errorf("CreateUser on the granted ARN = %d %s, want 200", w.status, w.body)
	}
	if w := iamCall(url.Values{"Action": {"CreateUser"}, "UserName": {"new"}, "Path": {"/other/"}}); w.status != http.StatusForbidden {
		t.Errorf("CreateUser on another path = %d %s, want the IAM denial", w.status, w.body)
	}
}

// A KMS operation that takes a key id only is decided on that key at
// step 8, so a missing key is denied first (iam.enforce.missing-resource);
// the key policy, which the default delegates to IAM, must allow too
// (iam-evaluation.md 4.1).
func TestEnforceDecidesKMSKeyOperationsOnTheirKey(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	kms := func(s signer, target, body string) wire {
		return jsonCall(t, s, endpoint, "kms", "1.1", "TrentService."+target, body)
	}
	var created struct {
		KeyMetadata struct {
			Arn   string
			KeyID string `json:"KeyId"`
		}
	}
	if w := kms(dummy, "CreateKey", "{}"); w.status != http.StatusOK || json.Unmarshal([]byte(w.body), &created) != nil {
		t.Fatalf("CreateKey = %d %s", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["kms:GetKeyPolicy","kms:DisableKey"],"Resource":"`+created.KeyMetadata.Arn+`"}]}`)
	if w := kms(u, "GetKeyPolicy", `{"KeyId":"`+created.KeyMetadata.KeyID+`"}`); w.status != http.StatusOK {
		t.Errorf("GetKeyPolicy of the granted key = %d %s, want 200", w.status, w.body)
	}
	if w := kms(u, "DisableKey", `{"KeyId":"`+created.KeyMetadata.KeyID+`"}`); w.status != http.StatusOK {
		t.Errorf("DisableKey of the granted key = %d %s, want 200", w.status, w.body)
	}
	missing := "11111111-2222-3333-4444-555555555555"
	if w := kms(u, "GetKeyPolicy", `{"KeyId":"`+missing+`"}`); w.status != kmsapi.DenialError().Status || jsonType(w) != "AccessDeniedException" {
		t.Errorf("GetKeyPolicy of a missing key = %d %s, want the KMS denial first", w.status, w.body)
	}
	if w := kms(u, "EnableKey", `{"KeyId":"`+created.KeyMetadata.KeyID+`"}`); w.status != kmsapi.DenialError().Status {
		t.Errorf("ungranted EnableKey = %d %s, want the KMS denial", w.status, w.body)
	}
}

// A Secrets Manager call by name is decided on the secret's full ARN once
// resolved, so a policy granting secret:<name>-* covers it. A secret that
// does not exist is decided on the ARN its SecretId implies, without a
// suffix (inferred; iam-enforcement-surfaces.md 5.3, IAM-ENF-14): denied
// first when the policy does not cover it, ResourceNotFoundException when
// it does. GetSecretValue's own denial reaches the caller unwrapped, not as
// a DecryptionFailure. (Root creates the secrets under
// aws/secretsmanager, whose policy admits the account's principals only
// through Secrets Manager: kms:ViaService.)
func TestEnforceDecidesSecretsByNameOnTheirARN(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	sm := func(s signer, target, body string) wire {
		return jsonCall(t, s, endpoint, "secretsmanager", "1.1", "secretsmanager."+target, body)
	}
	for _, name := range []string{"db", "other"} {
		if w := sm(dummy, "CreateSecret", `{"Name":"`+name+`","SecretString":"v","ClientRequestToken":"token-`+name+`-00000000000000000000000000000000"}`); w.status != http.StatusOK {
			t.Fatalf("CreateSecret %s = %d %s", name, w.status, w.body)
		}
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["secretsmanager:DescribeSecret","secretsmanager:GetSecretValue"],"Resource":"arn:aws:secretsmanager:us-east-1:000000000000:secret:db-*"}]}`)
	if w := sm(u, "DescribeSecret", `{"SecretId":"db"}`); w.status != http.StatusOK {
		t.Errorf("DescribeSecret by name of the granted secret = %d %s, want 200", w.status, w.body)
	}
	if w := sm(u, "DescribeSecret", `{"SecretId":"other"}`); w.status != secretsmanagerapi.DenialError().Status || jsonType(w) != "AccessDeniedException" {
		t.Errorf("DescribeSecret of another secret = %d %s, want the Secrets Manager denial", w.status, w.body)
	}
	if w := sm(u, "GetSecretValue", `{"SecretId":"other"}`); w.status != secretsmanagerapi.DenialError().Status || jsonType(w) != "AccessDeniedException" ||
		!strings.Contains(w.body, "secretsmanager:GetSecretValue") {
		t.Errorf("GetSecretValue of another secret = %d %s, want the Secrets Manager denial, unwrapped", w.status, w.body)
	}
	if w := sm(u, "GetSecretValue", `{"SecretId":"ghost"}`); jsonType(w) != "AccessDeniedException" {
		t.Errorf("GetSecretValue of a missing, ungranted secret = %d %s, want the Secrets Manager denial first", w.status, w.body)
	}
	if w := sm(u, "GetSecretValue", `{"SecretId":"db-ghost"}`); jsonType(w) != "ResourceNotFoundException" {
		t.Errorf("GetSecretValue of a missing secret the policy covers = %d %s, want ResourceNotFoundException", w.status, w.body)
	}
}

// PutEvents is decided on each entry's bus (iam-evaluation.md 2.1): a
// grant on the default bus's ARN admits the call; without it the call is
// denied with EventBridge's identity.
func TestEnforceDecidesPutEventsOnItsBus(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	put := func() wire {
		return jsonCall(t, u, endpoint, "events", "1.1", "AWSEvents.PutEvents", `{"Entries":[{"Source":"app","DetailType":"tick","Detail":"{}"}]}`)
	}
	if w := put(); w.status != eventbridgeapi.DenialError().Status || jsonType(w) != "AccessDeniedException" {
		t.Errorf("PutEvents without a grant = %d %s, want the EventBridge denial", w.status, w.body)
	}
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"events:PutEvents","Resource":"arn:aws:events:us-east-1:000000000000:event-bus/default"}]}`)
	if w := put(); w.status != http.StatusOK {
		t.Errorf("PutEvents with a grant on the bus = %d %s, want 200", w.status, w.body)
	}
}

// DynamoDB answers its declared denial identity before forwarding to
// DynamoDB Local (iam-enforcement-surfaces.md 12.1, 12.3).
func TestEnforceAnswersDynamoDBsDenialIdentity(t *testing.T) {
	_, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	w := jsonCall(t, u, endpoint, "dynamodb", "1.0", "DynamoDB_20120810.ListTables", "{}")
	want := dynamodbapi.DenialError()
	if w.status != want.Status || !strings.Contains(string(want.Shape), jsonType(w)) {
		t.Errorf("ListTables denial = %d %s; want %d %s", w.status, w.body, want.Status, want.Shape)
	}
}

// The running server reports the switches the denial texts and the
// evaluation follow (iam-enforcement-surfaces.md 12.8).
func TestTheServerReportsTheEnforcementSwitches(t *testing.T) {
	c, _, _ := enforced(t, server.IAMEnforceEnforce)
	for _, name := range []string{
		"iam.denial.grammar", "iam.denial.identity", "iam.enforce.order", "iam.enforce.missing-resource", "iam.condition.bool-variables",
		"sts.denial.reason", "sqs.denial.json-identity", "sqs.denial.query-text", "sns.denial.text", "s3.denial.text",
		"s3.root.bucket-policy-lockout", "lambda.denial.body", "dynamodb.denial.type", "kms.denial.status", "kms.key-policy.role-boundary",
		"secretsmanager.denial.status", "logs.denial.status", "events.denial.status", "cloudwatch.denial.identity",
	} {
		if v := wiredSwitch(t, c, name); v == "" {
			t.Errorf("switch %s has no value", name)
		}
	}
}
