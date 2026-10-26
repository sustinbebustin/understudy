package kms_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The evaluator reads a key policy through StoredPolicyReader: every key
// has one ("Every KMS key must have exactly one key policy",
// iam-evaluation.md 4.1), parsed, owned by the key's account; an alias ARN
// or a key that does not exist names none, as ResourcePolicy answers.
func TestStoredPolicyReadsTheKeyPolicy(t *testing.T) {
	run(t, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		key := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{Policy: new(rootPolicy)}).KeyMetadata
		arn, err := aws.ParseARN(*key.Arn)
		if err != nil {
			h.Fatal(err)
		}
		stored := must(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: key.KeyId}).Policy
		if stored == nil {
			h.Fatal("GetKeyPolicy returned no policy")
		}
		want := authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.Parsed{Document: *policydoc.ParseStored(*stored)}}
		if got, ok := reader.StoredPolicy(arn); !ok || !reflect.DeepEqual(got, want) {
			h.Errorf("StoredPolicy(%s) = %+v, %t; want %+v", arn, got, ok, want)
		}
		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/guarded", TargetKeyId: key.KeyId})
		for _, miss := range []string{arnPrefix + "alias/guarded", arnPrefix + "key/00000000-0000-4000-8000-000000000000"} {
			a, err := aws.ParseARN(miss)
			if err != nil {
				h.Fatal(err)
			}
			if got, ok := reader.StoredPolicy(a); ok {
				h.Errorf("StoredPolicy(%s) = %+v, want none", miss, got)
			}
		}
	})
}
