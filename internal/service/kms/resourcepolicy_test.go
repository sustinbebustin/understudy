package kms_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// A KMS key always has a key policy: "Key policy is mandatory"
// (iam-enforcement-surfaces.md 7.5), the default one when CreateKey sends
// none (iam-sts-kms.md 5.3). The provider reads it by the key's ARN as
// GetKeyPolicy returns it, and reads nothing for an alias ARN, a key that
// does not exist or any other KMS ARN.
func TestTheKeyPolicyIsTheKeysResourcePolicy(t *testing.T) {
	run(t, func(h *harness) {
		var provider authz.PolicyProvider = h.svc
		keyPolicy := func(id string) string {
			h.Helper()
			p := must(h, kmsapi.OpGetKeyPolicy, &kmsapi.GetKeyPolicyInput{KeyId: id}).Policy
			if p == nil {
				h.Fatalf("GetKeyPolicy(%s) returned no policy", id)
			}
			return *p
		}
		read := func(arn aws.ARN, want string) {
			h.Helper()
			doc, ok := provider.ResourcePolicy(arn)
			if !ok || doc == nil || !reflect.DeepEqual(doc, policydoc.ParseStored(want)) {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want the key policy %s", arn, doc, ok, want)
			}
		}
		parse := func(s string) aws.ARN {
			h.Helper()
			a, err := aws.ParseARN(s)
			if err != nil {
				h.Fatal(err)
			}
			return a
		}

		defaulted := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		read(parse(*defaulted.Arn), keyPolicy(defaulted.KeyId))

		sent := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{Policy: new(rootPolicy)}).KeyMetadata
		read(parse(*sent.Arn), rootPolicy)
		const replaced = `{"Version":"2012-10-17","Statement":[{"Sid":"Root","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"kms:*","Resource":"*"},{"Sid":"Use","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Action":"kms:Decrypt","Resource":"*"}]}`
		must(h, kmsapi.OpPutKeyPolicy, &kmsapi.PutKeyPolicyInput{KeyId: sent.KeyId, Policy: replaced})
		read(parse(*sent.Arn), keyPolicy(sent.KeyId))

		must(h, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/app", TargetKeyId: sent.KeyId})
		for _, miss := range []string{
			arnPrefix + "alias/app",
			arnPrefix + "key/00000000-0000-4000-8000-000000000000",
			"arn:aws:kms:eu-west-1:000000000000:key/" + sent.KeyId,
			"arn:aws:kms:us-east-1:111111111111:key/" + sent.KeyId,
		} {
			if doc, ok := provider.ResourcePolicy(parse(miss)); ok || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, ok)
			}
		}
	})
}
