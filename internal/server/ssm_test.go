package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/server"
)

// rootJSONCall sends one signed awsJson1_1 request to service with
// X-Amz-Target target as the default account's root and returns the status
// and body.
func rootJSONCall(t *testing.T, endpoint, service, target, body string) (status int, out string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	auth.Sign(req, []byte(body), auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: service}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func mustSSM(t *testing.T, endpoint, op, body string) string {
	t.Helper()
	status, out := rootJSONCall(t, endpoint, "ssm", "AmazonSSM."+op, body)
	if status != http.StatusOK {
		t.Fatalf("ssm %s = %d %s", op, status, out)
	}
	return out
}

// The tracer bullet: SSM is wired, so a String put and get
// over awsJson1_1 (ssm-parameters.md 2.1) answer as recorded, the put
// {Tier: Standard, Version: 1} (localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json:L19-L26, validated
// 2025-01-08) and the get the parameter with its value.
func TestSSMIsWired(t *testing.T) {
	_, endpoint := start(t)
	var put struct {
		Tier    string
		Version int64
	}
	if err := json.Unmarshal([]byte(mustSSM(t, endpoint, "PutParameter", `{"Name":"/tracer/a","Type":"String","Value":"v"}`)), &put); err != nil {
		t.Fatal(err)
	}
	if put.Tier != "Standard" || put.Version != 1 {
		t.Errorf("PutParameter = %+v, want Standard 1", put)
	}
	var got struct {
		Parameter struct{ Name, Value, ARN string }
	}
	if err := json.Unmarshal([]byte(mustSSM(t, endpoint, "GetParameter", `{"Name":"/tracer/a"}`)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Parameter.Name != "/tracer/a" || got.Parameter.Value != "v" || got.Parameter.ARN != "arn:aws:ssm:us-east-1:000000000000:parameter/tracer/a" {
		t.Errorf("GetParameter = %+v", got.Parameter)
	}
}

// R1 over the wire: the server sets the edge's legacy-Query rule for SSM, so
// the Query-protocol GetParameter AWS serves (localstack@8b9a79f
// tests/aws/services/apigateway/test_apigateway_ssm.snapshot.json:L33-L71,
// validated 2024-07-12; request test_apigateway_ssm.py:L110-L158) answers
// 501 NotImplemented, never the parameter.
func TestSSMLegacyQueryAnswers501(t *testing.T) {
	_, endpoint := start(t)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"param-test-123","Type":"String","Value":"123"}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/?Action=GetParameter&Name=param-test-123", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	auth.Sign(req, nil, auth.Signer{AccessKeyID: "test", SecretAccessKey: "test", Region: aws.DefaultRegion(), Service: "ssm"}, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotImplemented || !strings.Contains(string(body), "NotImplemented") || strings.Contains(string(body), `"123"`) {
		t.Errorf("legacy Query GetParameter = %d %s, want 501 NotImplemented without the parameter", resp.StatusCode, body)
	}
}

// policyPairs flattens an IAM policy document into "action resource".
func policyPairs(t *testing.T, doc json.RawMessage) []string {
	t.Helper()
	var d struct {
		Statement []struct {
			Action, Resource json.RawMessage
		}
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatalf("policy %s: %v", doc, err)
	}
	list := func(raw json.RawMessage) []string {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			return []string{one}
		}
		var many []string
		if err := json.Unmarshal(raw, &many); err != nil {
			t.Fatalf("policy element %s: %v", raw, err)
		}
		return many
	}
	var out []string
	for _, s := range d.Statement {
		for _, a := range list(s.Action) {
			for _, r := range list(s.Resource) {
				out = append(out, a+" "+r)
			}
		}
	}
	return out
}

// GET /iam/policy after an aws-cli-like run lists each ssm: action on the
// parameter ARN it touched, kms:Encrypt and kms:Decrypt for a SecureString
// under a customer managed key, no kms: action for a default-key one, and
// no ssm:AddTagsToResource for an untagged put (ssm-parameters.md 5.6).
func TestSSMIAMPolicy(t *testing.T) {
	const root = "arn:aws:iam::000000000000:root"
	const param = "arn:aws:ssm:us-east-1:000000000000:parameter/"

	c, endpoint := adminServer(t, server.Config{})
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/cli/plain","Type":"String","Value":"v"}`)
	mustSSM(t, endpoint, "GetParameter", `{"Name":"/cli/plain"}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/cli/default","Type":"SecureString","Value":"s"}`)
	mustSSM(t, endpoint, "GetParameter", `{"Name":"/cli/default","WithDecryption":true}`)
	mustSSM(t, endpoint, "DeleteParameter", `{"Name":"/cli/plain"}`)
	policy, err := c.IAMPolicy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := policyPairs(t, policy.Document)
	for _, want := range []string{
		"ssm:PutParameter " + param + "cli/plain",
		"ssm:GetParameter " + param + "cli/plain",
		"ssm:DeleteParameter " + param + "cli/plain",
		"ssm:PutParameter " + param + "cli/default",
		"ssm:GetParameter " + param + "cli/default",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("policy lacks %q; policy = %v", want, got)
		}
	}
	for _, p := range got {
		if strings.HasPrefix(p, "kms:") || strings.HasPrefix(p, "ssm:AddTagsToResource ") {
			t.Errorf("policy lists %q for an untagged, default-key run", p)
		}
	}

	c2, endpoint2 := adminServer(t, server.Config{})
	status, out := rootJSONCall(t, endpoint2, "kms", "TrentService.CreateKey", `{}`)
	if status != http.StatusOK {
		t.Fatalf("CreateKey = %d %s", status, out)
	}
	var key struct {
		KeyMetadata struct {
			KeyID string `json:"KeyId"`
			Arn   string
		}
	}
	if err := json.Unmarshal([]byte(out), &key); err != nil {
		t.Fatal(err)
	}
	mustSSM(t, endpoint2, "PutParameter", `{"Name":"/cli/cmk","Type":"SecureString","Value":"s","KeyId":"`+key.KeyMetadata.KeyID+`"}`)
	mustSSM(t, endpoint2, "GetParameter", `{"Name":"/cli/cmk","WithDecryption":true}`)
	policy2, err := c2.IAMPolicy(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	got2 := policyPairs(t, policy2.Document)
	// The key id is minted, so the policy names every key of the account and
	// region (ARCHITECTURE.md 17).
	keys := strings.TrimSuffix(key.KeyMetadata.Arn, key.KeyMetadata.KeyID) + "*"
	for _, want := range []string{"kms:Encrypt " + keys, "kms:Decrypt " + keys} {
		if !slices.Contains(got2, want) {
			t.Errorf("policy lacks %q for a customer-managed-key SecureString; policy = %v", want, got2)
		}
	}
}
