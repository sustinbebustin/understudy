package kms_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// The provider reads key/<key-id> of a KMS ARN only: another service's ARN
// with the same resource, an empty key id and a key ARN of an account and
// region no call reached name no key.
func TestTheKeyPolicyProviderReadsKeyARNsOnly(t *testing.T) {
	run(t, func(h *harness) {
		var provider authz.PolicyProvider = h.svc
		id := h.key()
		if doc, ok := provider.ResourcePolicy(parseKeyARN(h, keyARN(id))); !ok || doc == nil {
			h.Fatalf("ResourcePolicy(%s) = %+v, %t; want the default key policy", keyARN(id), doc, ok)
		}
		for _, miss := range []string{
			"arn:aws:secretsmanager:us-east-1:000000000000:key/" + id,
			arnPrefix + "key/",
			"arn:aws:kms:ap-south-1:222222222222:key/" + id,
		} {
			if doc, ok := provider.ResourcePolicy(parseKeyARN(h, miss)); ok || doc != nil {
				h.Errorf("ResourcePolicy(%s) = %+v, %t; want nil, false", miss, doc, ok)
			}
		}
	})
}

func parseKeyARN(h *harness, s string) aws.ARN {
	h.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}
