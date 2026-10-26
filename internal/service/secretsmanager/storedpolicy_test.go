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

// The evaluator reads a secret's resource policy through
// StoredPolicyReader, by the secret's full ARN: NoPolicy until
// PutResourcePolicy, then the parsed policy, owned by the secret's account;
// a partial ARN or a secret that does not exist names none, as
// ResourcePolicy answers (iam-enforcement-surfaces.md 7.6).
func TestStoredPolicyReadsTheSecretPolicy(t *testing.T) {
	run(t, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		full := val(must(h, sm.OpCreateSecret, &sm.CreateSecretInput{Name: "app/db", ClientRequestToken: new(h.token())}).ARN)
		arn, err := aws.ParseARN(full)
		if err != nil {
			h.Fatal(err)
		}
		if got, ok := reader.StoredPolicy(arn); !ok || got != (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.NoPolicy{}}) {
			h.Errorf("StoredPolicy without a policy = %+v, %t; want NoPolicy owned by the secret's account", got, ok)
		}
		const policy = `{"Statement":[{"Action":"secretsmanager:GetSecretValue","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Resource":"*"}],"Version":"2012-10-17"}`
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: full, ResourcePolicy: policy})
		stored := val(must(h, sm.OpGetResourcePolicy, &sm.GetResourcePolicyInput{SecretId: full}).ResourcePolicy)
		want := authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.Parsed{Document: *policydoc.ParseStored(stored)}}
		if got, ok := reader.StoredPolicy(arn); !ok || !reflect.DeepEqual(got, want) {
			h.Errorf("StoredPolicy(%s) = %+v, %t; want %+v", full, got, ok, want)
		}
		partial, err := aws.ParseARN(full[:strings.LastIndex(full, "-")])
		if err != nil {
			h.Fatal(err)
		}
		if got, ok := reader.StoredPolicy(partial); ok {
			h.Errorf("StoredPolicy of the partial ARN = %+v, want none", got)
		}
	})
}
