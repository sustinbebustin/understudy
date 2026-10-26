package secretsmanager_test

import (
	"testing"

	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// A recreated secret has another ARN (secrets-logs-events-alarms.md 2.1:
// "The suffix makes a recreated secret with the same name a different
// ARN"), so the old full ARN names no secret and the new one reads the
// new secret's policy, none until one is put; DeleteResourcePolicy leaves
// the secret without one.
func TestTheSecretPolicyFollowsTheSecretsARN(t *testing.T) {
	run(t, func(h *harness) {
		var provider authz.PolicyProvider = h.svc
		parse := func(s string) aws.ARN {
			h.Helper()
			a, err := aws.ParseARN(s)
			if err != nil {
				h.Fatal(err)
			}
			return a
		}
		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
		first := val(h.create("app/db", "v1").ARN)
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: first, ResourcePolicy: policy})
		must(h, sm.OpDeleteSecret, &sm.DeleteSecretInput{SecretId: first, ForceDeleteWithoutRecovery: new(true)})
		second := val(h.create("app/db", "v2").ARN)
		if second == first {
			h.Fatalf("recreated secret kept ARN %s", first)
		}

		if doc, ok := provider.ResourcePolicy(parse(first)); ok || doc != nil {
			h.Errorf("ResourcePolicy(%s) of the deleted secret = %+v, %t; want nil, false", first, doc, ok)
		}
		if doc, ok := provider.ResourcePolicy(parse(second)); !ok || doc != nil {
			h.Errorf("ResourcePolicy(%s) of the recreated secret = %+v, %t; want nil, true", second, doc, ok)
		}
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: second, ResourcePolicy: policy})
		if doc, ok := provider.ResourcePolicy(parse(second)); !ok || doc == nil {
			h.Errorf("ResourcePolicy(%s) after PutResourcePolicy = %+v, %t; want the policy", second, doc, ok)
		}
		must(h, sm.OpDeleteResourcePolicy, &sm.DeleteResourcePolicyInput{SecretId: second})
		if doc, ok := provider.ResourcePolicy(parse(second)); !ok || doc != nil {
			h.Errorf("ResourcePolicy(%s) after DeleteResourcePolicy = %+v, %t; want nil, true", second, doc, ok)
		}
	})
}
