package kms

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindCrypto binds the symmetric cryptographic operations: real AES-256-GCM
// with the encryption context as additional authenticated data.
func (s *Service) bindCrypto(b kmsapi.Binder) {
	noRecipient := "Recipient (Nitro Enclaves attestation) answers UnsupportedOperationException"
	b.Encrypt(s.encrypt, registry.Full{})
	b.Decrypt(s.decrypt, registry.Partial{Note: noRecipient + ", as do DryRunModifiers"},
		kernel.WithValidator(func(in *kmsapi.DecryptInput) []awserr.Violation { return requireBlob(in.CiphertextBlob) }))
	b.ReEncrypt(s.reEncrypt, registry.Partial{Note: "DryRunModifiers answer UnsupportedOperationException"},
		kernel.WithValidator(func(in *kmsapi.ReEncryptInput) []awserr.Violation { return requireBlob(in.CiphertextBlob) }))
	b.GenerateDataKey(s.generateDataKey, registry.Partial{Note: noRecipient},
		kernel.WithCheck(func(in *kmsapi.GenerateDataKeyInput) *awserr.Error {
			return checkDataKeySize(in.KeySpec, in.NumberOfBytes)
		}))
	b.GenerateDataKeyWithoutPlaintext(s.generateDataKeyWithoutPlaintext, registry.Full{},
		kernel.WithCheck(func(in *kmsapi.GenerateDataKeyWithoutPlaintextInput) *awserr.Error {
			return checkDataKeySize(in.KeySpec, in.NumberOfBytes)
		}))
	b.GenerateRandom(s.generateRandom, registry.Partial{Note: noRecipient + "; custom key stores do not exist"},
		kernel.WithCheck(func(in *kmsapi.GenerateRandomInput) *awserr.Error {
			if in.NumberOfBytes == nil {
				return kmsapi.ErrValidationException("NumberOfBytes is required.")
			}
			return nil
		}))
}

// requireBlob reports a missing CiphertextBlob, which the model leaves
// optional only for DryRun with IGNORE_CIPHERTEXT (iam-sts-kms.md 5.7).
func requireBlob(blob []byte) []awserr.Violation {
	if blob != nil {
		return nil
	}
	return []awserr.Violation{{Path: awserr.Path{"ciphertextBlob"}, Value: awserr.RenderedNull{}, Constraint: awserr.Required{}}}
}

// checkDataKeySize requires exactly one of KeySpec and NumberOfBytes. The
// text is moto's; AWS's is unrecorded (iam-sts-kms.md 5.7).
func checkDataKeySize(spec *kmsapi.DataKeySpec, n *int32) *awserr.Error {
	if (spec == nil) == (n == nil) {
		return kmsapi.ErrValidationException("Please specify either number of bytes or key spec.")
	}
	return nil
}

// dataKeyBytes is the length of a data key of spec or n bytes.
func dataKeyBytes(spec *kmsapi.DataKeySpec, n *int32) int {
	if n != nil {
		return int(*n)
	}
	if spec != nil && *spec == kmsapi.DataKeySpecAes128 {
		return 16
	}
	return 32
}

// checkAlgorithm accepts the one algorithm of a symmetric key.
func checkAlgorithm(alg *kmsapi.EncryptionAlgorithmSpec) *awserr.Error {
	if alg != nil && *alg != kmsapi.EncryptionAlgorithmSpecSymmetricDefault {
		return errAlgorithm(*alg)
	}
	return nil
}

// sealed is plaintext encrypted under a key.
type sealed struct {
	key  located
	blob []byte
}

// sealUnder encrypts plaintext under the usable key keyID names (any
// identifier form) with context; a dry run stops with DryRunOperationException
// once every check has passed.
func (s *Service) sealUnder(c *kernel.Call, keyID string, encCtx map[string]string, plaintext []byte, dryRun *bool) (sealed, *awserr.Error) {
	r, err := parseKeyRef(c, keyID, keyOrAlias)
	if err != nil {
		return sealed{}, err
	}
	dry := dryRun != nil && *dryRun
	// A dry run creates no AWS managed key: one created now would be
	// enabled and pass every check.
	if dry && s.wouldMint(c, r) {
		return sealed{}, errDryRun()
	}
	k, err := s.keyFor(c, r, usable)
	if err != nil {
		return sealed{}, err
	}
	if dry {
		return sealed{}, errDryRun()
	}
	loc := locate(r.scope(), k)
	blob, serr := seal(k.Material, loc.arn, encCtx, plaintext)
	if serr != nil {
		return sealed{}, awserr.Internalf(serr)
	}
	c.Touch(arnOf(loc.scope, loc.ID))
	return sealed{key: loc, blob: blob}, nil
}

// opened is a blob's plaintext and the key that sealed it.
type opened struct {
	key   located
	plain []byte
}

// openBlob decrypts a blob under the key it names, which must be usable;
// keyID, when given, must name that same key (IncorrectKeyException).
func (s *Service) openBlob(c *kernel.Call, blob []byte, keyID *string, encCtx map[string]string) (opened, *awserr.Error) {
	env, ok := parseBlob(blob)
	if !ok {
		return opened{}, errInvalidCiphertext()
	}
	r, err := parseKeyRef(c, env.keyARN, keyOnly)
	if err != nil {
		return opened{}, errInvalidCiphertext()
	}
	if keyID != nil {
		if err := s.checkNamed(c, *keyID, r); err != nil {
			return opened{}, err
		}
	}
	k, err := s.keyFor(c, r, usable)
	if err != nil {
		return opened{}, err
	}
	plain, ok := env.open(k.Material, encCtx)
	if !ok {
		return opened{}, errInvalidCiphertext()
	}
	loc := locate(r.scope(), k)
	c.Touch(arnOf(loc.scope, loc.ID))
	return opened{key: loc, plain: plain}, nil
}

// checkNamed answers IncorrectKeyException unless keyID (any identifier
// form) names the key that sealed a blob, sealedBy. It only looks: an
// alias/aws/ alias with no AWS managed key yet names no key, so it cannot
// name sealedBy and creates none.
func (s *Service) checkNamed(c *kernel.Call, keyID string, sealedBy keyRef) *awserr.Error {
	named, err := parseKeyRef(c, keyID, keyOrAlias)
	if err != nil {
		return err
	}
	k, err := lookup(s, named)
	if a, isAlias := named.(keyByAlias); err != nil && isAlias && managedAlias(a.name) {
		return errIncorrectKey()
	}
	if err != nil {
		return err
	}
	c.Touch(arnOf(named.scope(), k.ID))
	if want, ok := sealedBy.(keyByID); !ok || named.scope() != want.in || k.ID != want.id {
		return errIncorrectKey()
	}
	return nil
}

func (s *Service) encrypt(_ context.Context, c *kernel.Call, in *kmsapi.EncryptInput) (*kmsapi.EncryptOutput, *awserr.Error) {
	if err := checkAlgorithm(in.EncryptionAlgorithm); err != nil {
		return nil, err
	}
	out, err := s.sealUnder(c, in.KeyId, in.EncryptionContext, in.Plaintext, in.DryRun)
	if err != nil {
		return nil, err
	}
	return &kmsapi.EncryptOutput{
		CiphertextBlob:      out.blob,
		KeyId:               new(out.key.arn),
		EncryptionAlgorithm: new(kmsapi.EncryptionAlgorithmSpecSymmetricDefault),
	}, nil
}

func (s *Service) decrypt(_ context.Context, c *kernel.Call, in *kmsapi.DecryptInput) (*kmsapi.DecryptOutput, *awserr.Error) {
	switch {
	case in.Recipient != nil:
		return nil, errUnsupported("Recipient")
	case len(in.DryRunModifiers) > 0:
		return nil, errUnsupported("DryRunModifiers")
	}
	if err := checkAlgorithm(in.EncryptionAlgorithm); err != nil {
		return nil, err
	}
	out, err := s.openBlob(c, in.CiphertextBlob, in.KeyId, in.EncryptionContext)
	if err != nil {
		return nil, err
	}
	if in.DryRun != nil && *in.DryRun {
		return nil, errDryRun()
	}
	return &kmsapi.DecryptOutput{
		KeyId:               new(out.key.arn),
		Plaintext:           out.plain,
		EncryptionAlgorithm: new(kmsapi.EncryptionAlgorithmSpecSymmetricDefault),
		KeyMaterialId:       new(out.key.MaterialID),
	}, nil
}

// reEncrypt decrypts under the source key and encrypts under the
// destination key, each with its own context.
func (s *Service) reEncrypt(_ context.Context, c *kernel.Call, in *kmsapi.ReEncryptInput) (*kmsapi.ReEncryptOutput, *awserr.Error) {
	if len(in.DryRunModifiers) > 0 {
		return nil, errUnsupported("DryRunModifiers")
	}
	if err := checkAlgorithm(in.SourceEncryptionAlgorithm); err != nil {
		return nil, err
	}
	if err := checkAlgorithm(in.DestinationEncryptionAlgorithm); err != nil {
		return nil, err
	}
	src, err := s.openBlob(c, in.CiphertextBlob, in.SourceKeyId, in.SourceEncryptionContext)
	if err != nil {
		return nil, err
	}
	dst, err := s.sealUnder(c, in.DestinationKeyId, in.DestinationEncryptionContext, src.plain, in.DryRun)
	if err != nil {
		return nil, err
	}
	return &kmsapi.ReEncryptOutput{
		CiphertextBlob:                 dst.blob,
		SourceKeyId:                    new(src.key.arn),
		KeyId:                          new(dst.key.arn),
		SourceEncryptionAlgorithm:      new(kmsapi.EncryptionAlgorithmSpecSymmetricDefault),
		DestinationEncryptionAlgorithm: new(kmsapi.EncryptionAlgorithmSpecSymmetricDefault),
		SourceKeyMaterialId:            new(src.key.MaterialID),
		DestinationKeyMaterialId:       new(dst.key.MaterialID),
	}, nil
}

// dataKey is a fresh data key and its blob under a KMS key.
type dataKey struct {
	sealed
	plain []byte
}

// newDataKey draws a data key from crypto/rand and seals it under keyID.
func (s *Service) newDataKey(c *kernel.Call, keyID string, encCtx map[string]string, spec *kmsapi.DataKeySpec, n *int32, dryRun *bool) (dataKey, *awserr.Error) {
	plain, err := randomBytes(dataKeyBytes(spec, n))
	if err != nil {
		return dataKey{}, err
	}
	out, err := s.sealUnder(c, keyID, encCtx, plain, dryRun)
	if err != nil {
		return dataKey{}, err
	}
	return dataKey{sealed: out, plain: plain}, nil
}

func (s *Service) generateDataKey(_ context.Context, c *kernel.Call, in *kmsapi.GenerateDataKeyInput) (*kmsapi.GenerateDataKeyOutput, *awserr.Error) {
	if in.Recipient != nil {
		return nil, errUnsupported("Recipient")
	}
	dk, err := s.newDataKey(c, in.KeyId, in.EncryptionContext, in.KeySpec, in.NumberOfBytes, in.DryRun)
	if err != nil {
		return nil, err
	}
	return &kmsapi.GenerateDataKeyOutput{
		CiphertextBlob: dk.blob,
		Plaintext:      dk.plain,
		KeyId:          new(dk.key.arn),
		KeyMaterialId:  new(dk.key.MaterialID),
	}, nil
}

func (s *Service) generateDataKeyWithoutPlaintext(_ context.Context, c *kernel.Call, in *kmsapi.GenerateDataKeyWithoutPlaintextInput) (*kmsapi.GenerateDataKeyWithoutPlaintextOutput, *awserr.Error) {
	dk, err := s.newDataKey(c, in.KeyId, in.EncryptionContext, in.KeySpec, in.NumberOfBytes, in.DryRun)
	if err != nil {
		return nil, err
	}
	return &kmsapi.GenerateDataKeyWithoutPlaintextOutput{
		CiphertextBlob: dk.blob,
		KeyId:          new(dk.key.arn),
		KeyMaterialId:  new(dk.key.MaterialID),
	}, nil
}

func (*Service) generateRandom(_ context.Context, _ *kernel.Call, in *kmsapi.GenerateRandomInput) (*kmsapi.GenerateRandomOutput, *awserr.Error) {
	switch {
	case in.Recipient != nil:
		return nil, errUnsupported("Recipient")
	case in.CustomKeyStoreId != nil:
		return nil, errUnsupported("custom key stores")
	}
	b, err := randomBytes(int(*in.NumberOfBytes))
	if err != nil {
		return nil, err
	}
	return &kmsapi.GenerateRandomOutput{Plaintext: b}, nil
}
