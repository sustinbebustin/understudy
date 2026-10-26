package secretsmanager_test

import (
	"reflect"
	"strings"
	"testing"

	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// A secret's resource policy (iam-enforcement-surfaces.md 7.6) is read by
// the secret's full ARN, as GetResourcePolicy returns it; a secret without
// one reads as no policy. A partial ARN (no random suffix) or a secret that
// does not exist names none.
func TestTheSecretPolicyIsTheSecretsResourcePolicy(t *testing.T) {
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
		full := val(must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "app/db", ClientRequestToken: new(h.token())}).ARN)
		arn := parse(full)
		if doc, ok := provider.ResourcePolicy(arn); !ok || doc != nil {
			h.Errorf("ResourcePolicy of a secret without a policy = %+v, %t; want nil, true", doc, ok)
		}

		const policy = `{"Statement":[{"Action":"secretsmanager:GetSecretValue","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Resource":"*"}],"Version":"2012-10-17"}`
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: full, ResourcePolicy: policy})
		stored := val(must(h, sm.OpGetResourcePolicy, &sm.GetResourcePolicyInput{SecretId: full}).ResourcePolicy)
		if doc, ok := provider.ResourcePolicy(arn); !ok || doc == nil || !reflect.DeepEqual(doc, policydoc.ParseStored(stored)) {
			h.Errorf("ResourcePolicy(%s) = %+v, %t; want the stored policy %s", full, doc, ok, stored)
		}

		dash := strings.LastIndex(full, "-")
		if dash < 0 {
			h.Fatalf("secret ARN %s has no random suffix", full)
		}
		for _, miss := range []string{
			full[:dash], // the partial ARN
			"arn:aws:secretsmanager:us-east-1:000000000000:secret:absent-AbCdEf",
			"arn:aws:secretsmanager:us-east-1:111111111111:secret:" + full[strings.LastIndex(full, ":")+1:],
		} {
			if doc, ok := provider.ResourcePolicy(parse(miss)); ok || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, ok)
			}
		}
	})
}
