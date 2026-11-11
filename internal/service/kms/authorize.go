package kms

import (
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// resolvedFirst is the coverage note of the operations that accept an
// alias: the key behind it is known only once KMS resolved it, so the call
// is authorized then, and an alias or key that does not exist answers
// NotFoundException before authorization, as IAM-ENF-14 recorded for
// DescribeKey on a missing key id; an alias follows it by inference
// (iam-enforcement-surfaces.md 5.3).
const resolvedFirst = "under iam.enforce, an alias or key that does not exist answers NotFoundException before authorization (kms.enforce.missing-resource.by-name)"

// mintedOnAnyResource is the coverage note of the operations whose first
// use of an alias/aws/<service> alias creates the AWS managed key it names
// (keyFor): the call is authorized and access-recorded before the key
// exists, so it names no key and is asked about "*", and the policy
// rendered from the record grants the action on "*", an over-grant. Asking
// about the key instead needs the key created before authorization, and
// whether AWS creates it for a caller it then denies is unrecorded: AWS
// documents only that the key is created on first use (iam-sts-kms.md 5.4).
const mintedOnAnyResource = "the first use of an alias/aws/<service> alias, which creates that AWS managed key, is authorized and recorded on * because the key does not exist yet, so the policy grants the action on * (an over-grant); later uses are recorded on the key; whether AWS creates the key before it authorizes the call is unknown (iam-sts-kms.md 5.4)"

// declareAuthorization states what each KMS operation is authorized on
// (iam-evaluation.md 2.1, 4): an operation that takes a key id or key ARN
// only is asked about that key at step 8; one that also takes an alias is
// asked about the key the alias names once KMS resolved it
// (kernel.AuthorizedAfterResolve), Decrypt and ReEncrypt also about the key
// the ciphertext names; an alias operation about the alias and its keys. It
// runs after the bind sites, whose bindings it extends.
func (s *Service) declareAuthorization(b *kernel.Binder) {
	kernel.Declare(b, kmsapi.OpDescribeKey, keyResolved(s, func(in *kmsapi.DescribeKeyInput) *string { return &in.KeyId })...)
	kernel.Declare(b, kmsapi.OpEncrypt, keyResolved(s, func(in *kmsapi.EncryptInput) *string { return &in.KeyId })...)
	kernel.Declare(b, kmsapi.OpDecrypt, keyResolved(s, func(in *kmsapi.DecryptInput) *string { return in.KeyId })...)
	kernel.Declare(b, kmsapi.OpReEncrypt, keyResolved(s, func(in *kmsapi.ReEncryptInput) *string { return &in.DestinationKeyId })...)
	// ReEncrypt needs kms:ReEncryptFrom on the key that sealed the
	// ciphertext and kms:ReEncryptTo on the destination key, each on its
	// own key, never crossed (iam-enforcement-surfaces.md 10.2, 12.6; the
	// ReEncrypt API reference, Required permissions).
	kernel.Declare(b, kmsapi.OpReEncrypt,
		kernel.WithActions(func(*kernel.Call, *kmsapi.ReEncryptInput) []registry.IAMAction {
			return []registry.IAMAction{{Prefix: "kms", Name: "ReEncryptTo"}}
		}),
		kernel.WithResourcesFor([]registry.IAMAction{{Prefix: "kms", Name: "ReEncryptFrom"}}, func(c *kernel.Call, in *kmsapi.ReEncryptInput) []aws.ARN {
			return s.sealedBy(c, in.CiphertextBlob)
		}),
		kernel.WithResourcesFor([]registry.IAMAction{{Prefix: "kms", Name: "ReEncryptTo"}}, func(c *kernel.Call, in *kmsapi.ReEncryptInput) []aws.ARN {
			return s.keysOf(c, in.DestinationKeyId)
		}))
	kernel.Declare(b, kmsapi.OpGenerateDataKey, keyResolved(s, func(in *kmsapi.GenerateDataKeyInput) *string { return &in.KeyId })...)
	kernel.Declare(b, kmsapi.OpGenerateDataKeyWithoutPlaintext, keyResolved(s, func(in *kmsapi.GenerateDataKeyWithoutPlaintextInput) *string { return &in.KeyId })...)

	kernel.Declare(b, kmsapi.OpUpdateKeyDescription, keyNamed(func(in *kmsapi.UpdateKeyDescriptionInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpEnableKey, keyNamed(func(in *kmsapi.EnableKeyInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpDisableKey, keyNamed(func(in *kmsapi.DisableKeyInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpScheduleKeyDeletion, keyNamed(func(in *kmsapi.ScheduleKeyDeletionInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpCancelKeyDeletion, keyNamed(func(in *kmsapi.CancelKeyDeletionInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpEnableKeyRotation, keyNamed(func(in *kmsapi.EnableKeyRotationInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpDisableKeyRotation, keyNamed(func(in *kmsapi.DisableKeyRotationInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpGetKeyRotationStatus, keyNamed(func(in *kmsapi.GetKeyRotationStatusInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpGetKeyPolicy, keyNamed(func(in *kmsapi.GetKeyPolicyInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpPutKeyPolicy, keyNamed(func(in *kmsapi.PutKeyPolicyInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpListKeyPolicies, keyNamed(func(in *kmsapi.ListKeyPoliciesInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpTagResource, keyNamed(func(in *kmsapi.TagResourceInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpUntagResource, keyNamed(func(in *kmsapi.UntagResourceInput) string { return in.KeyId }))
	kernel.Declare(b, kmsapi.OpListResourceTags, keyNamed(func(in *kmsapi.ListResourceTagsInput) string { return in.KeyId }))

	// An alias operation needs its action on the alias and on each key it
	// names: the target, and for UpdateAlias and DeleteAlias the key the
	// alias names now (the KMS API reference, Cross-account use and
	// Required permissions of each).
	kernel.Declare(b, kmsapi.OpCreateAlias, kernel.WithResources(func(c *kernel.Call, in *kmsapi.CreateAliasInput) []aws.ARN {
		return append([]aws.ARN{aliasARN(c.Scope(), in.AliasName)}, s.keysOf(c, in.TargetKeyId)...)
	}))
	kernel.Declare(b, kmsapi.OpUpdateAlias, kernel.WithResources(func(c *kernel.Call, in *kmsapi.UpdateAliasInput) []aws.ARN {
		out := []aws.ARN{aliasARN(c.Scope(), in.AliasName)}
		out = append(out, s.keysOf(c, in.AliasName)...)
		return append(out, s.keysOf(c, in.TargetKeyId)...)
	}))
	kernel.Declare(b, kmsapi.OpDeleteAlias, kernel.WithResources(func(c *kernel.Call, in *kmsapi.DeleteAliasInput) []aws.ARN {
		return append([]aws.ARN{aliasARN(c.Scope(), in.AliasName)}, s.keysOf(c, in.AliasName)...)
	}))
}

// keyNamed asks at step 8 about the key a key id or key ARN names, known
// from the reference itself; a reference that is neither names nothing,
// and the handler answers it.
func keyNamed[In any](id func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		r, err := parseKeyID(c, id(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{keyARN(r.in, r.id)}
	})
}

// keyResolved authorizes an operation on the key a key id, key ARN, alias
// name or alias ARN names once KMS resolved it; the key an alias names is
// read before the handler, and a reference to nothing names no key, so the
// handler answers it before anything is asked.
func keyResolved[In any](s *Service, id func(*In) *string) []kernel.BindOption {
	return []kernel.BindOption{
		kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
			if ref := id(in); ref != nil {
				return s.keysOf(c, *ref)
			}
			return nil
		}),
		kernel.AuthorizedAfterResolve(),
	}
}

// sealedBy is the key a ciphertext blob names as its sealer; none for
// bytes that are not a blob understudy wrote, which the handler refuses.
func (s *Service) sealedBy(c *kernel.Call, blob []byte) []aws.ARN {
	env, ok := parseBlob(blob)
	if !ok {
		return nil
	}
	return s.keysOf(c, env.keyARN)
}

// keysOf is the key a key id, key ARN, alias name or alias ARN names: the
// key's ARN, read from the state for an alias; none for an alias that names
// no key or a reference KMS refuses.
func (s *Service) keysOf(c *kernel.Call, ref string) []aws.ARN {
	r, err := parseKeyRef(c, ref, keyOrAlias)
	if err != nil {
		return nil
	}
	switch r := r.(type) {
	case keyByID:
		return []aws.ARN{keyARN(r.in, r.id)}
	case keyByAlias:
		k, err := lookup(s, r)
		if err != nil {
			return nil
		}
		return []aws.ARN{keyARN(r.in, k.ID)}
	}
	return nil
}
