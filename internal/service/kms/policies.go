package kms

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// policyName is the one key policy name KMS knows (iam-sts-kms.md 5.3).
const policyName = "default"

// bindPolicies binds the key policy operations. Policies are stored and
// returned as sent, and evaluated under iam.enforce advise and enforce.
func (s *Service) bindPolicies(b kmsapi.Binder) {
	b.GetKeyPolicy(s.getKeyPolicy, registry.Full{})
	b.PutKeyPolicy(s.putKeyPolicy, registry.Partial{Note: "the policy is parsed and stored verbatim, evaluated under iam.enforce=advise, which journals each denial, and enforced under iam.enforce=enforce; grants are not evaluated; the lockout safety check is not run under any iam.enforce mode, so a policy that locks out its caller is stored where AWS answers MalformedPolicyDocumentException, and each put without BypassPolicyLockoutSafetyCheck journals a check-not-run advisory"})
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
func (s *Service) putKeyPolicy(ctx context.Context, c *kernel.Call, in *kmsapi.PutKeyPolicyInput) (*kmsapi.PutKeyPolicyOutput, *awserr.Error) {
	if err := checkPolicyName(in.PolicyName); err != nil {
		return nil, err
	}
	if err := checkPolicy(in.Policy); err != nil {
		return nil, err
	}
	loc, err := updateKey(s, c, in.KeyId, anyState, func(k Key, _ string, _ clock.Instant, _ *kernel.Tx) (Key, *awserr.Error) {
		k.Policy = in.Policy
		return k, nil
	})
	if err != nil {
		return nil, err
	}
	lockoutUnchecked(ctx, c, in.BypassPolicyLockoutSafetyCheck, keyARN(loc.scope, loc.ID))
	return &kmsapi.PutKeyPolicyOutput{}, nil
}

// lockoutUnchecked journals a check-not-run advisory for a key policy
// stored without the lockout safety check AWS runs unless the caller
// bypasses it: the policy must allow the caller a later PutKeyPolicy
// (iam-evaluation.md 4.5). understudy does not run it under any iam.enforce
// mode, whose rejection text is unrecorded (IAM-EVAL-29), so under enforce
// a policy can lock out its caller where AWS refuses it.
func lockoutUnchecked(ctx context.Context, c *kernel.Call, bypass bool, key aws.ARN) {
	if bypass {
		return
	}
	c.Record(ctx, c.Scope(), journal.Advisory{Kind: journal.AdvisoryCheckNotRun, Resource: key, Text: lockoutUncheckedText})
}

// lockoutUncheckedText is lockoutUnchecked's advisory.
const lockoutUncheckedText = "understudy did not run the key policy lockout safety check: AWS refuses a key policy that does not allow the caller " +
	"a later PutKeyPolicy with MalformedPolicyDocumentException unless BypassPolicyLockoutSafetyCheck is true (iam-evaluation.md 4.5); the policy was stored"

func (s *Service) listKeyPolicies(_ context.Context, c *kernel.Call, in *kmsapi.ListKeyPoliciesInput) (*kmsapi.ListKeyPoliciesOutput, *awserr.Error) {
	if _, err := readKey(s, c, in.KeyId, keyOnly, ownAccount); err != nil {
		return nil, err
	}
	return &kmsapi.ListKeyPoliciesOutput{PolicyNames: []string{policyName}}, nil
}
