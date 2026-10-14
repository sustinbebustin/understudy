package kms

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// policyName is the one key policy name KMS knows (iam-sts-kms.md 5.3).
const policyName = "default"

// bindPolicies binds the key policy operations. Policies are stored and
// returned as sent; understudy does not enforce them.
func (s *Service) bindPolicies(b kmsapi.Binder) {
	b.GetKeyPolicy(s.getKeyPolicy, registry.Full{})
	b.PutKeyPolicy(s.putKeyPolicy, registry.StoredOnly{Note: "the policy is parsed and stored verbatim, not enforced; the lockout safety check is not run"})
	b.ListKeyPolicies(s.listKeyPolicies, registry.Full{})
}

// checkPolicyName answers a policy name other than default; an absent one
// means default.
func checkPolicyName(name *string) *awserr.Error {
	if name != nil && *name != policyName {
		return errPolicyName()
	}
	return nil
}

func (s *Service) getKeyPolicy(_ context.Context, c *kernel.Call, in *kmsapi.GetKeyPolicyInput) (*kmsapi.GetKeyPolicyOutput, *awserr.Error) {
	if err := checkPolicyName(in.PolicyName); err != nil {
		return nil, err
	}
	k, err := readKey(s, c, in.KeyId, keyOnly, ownAccount)
	if err != nil {
		return nil, err
	}
	return &kmsapi.GetKeyPolicyOutput{Policy: new(k.Policy), PolicyName: new(policyName)}, nil
}

// putKeyPolicy stores the policy verbatim: Terraform waits until GetKeyPolicy
// returns what it sent, and an added Id hangs it (tf-remaining.md 6).
func (s *Service) putKeyPolicy(_ context.Context, c *kernel.Call, in *kmsapi.PutKeyPolicyInput) (*kmsapi.PutKeyPolicyOutput, *awserr.Error) {
	if err := checkPolicyName(in.PolicyName); err != nil {
		return nil, err
	}
	if err := checkPolicy(in.Policy); err != nil {
		return nil, err
	}
	_, err := updateKey(s, c, in.KeyId, anyState, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k.Policy = in.Policy
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	return &kmsapi.PutKeyPolicyOutput{}, nil
}

func (s *Service) listKeyPolicies(_ context.Context, c *kernel.Call, in *kmsapi.ListKeyPoliciesInput) (*kmsapi.ListKeyPoliciesOutput, *awserr.Error) {
	if _, err := readKey(s, c, in.KeyId, keyOnly, ownAccount); err != nil {
		return nil, err
	}
	return &kmsapi.ListKeyPoliciesOutput{PolicyNames: []string{policyName}}, nil
}
