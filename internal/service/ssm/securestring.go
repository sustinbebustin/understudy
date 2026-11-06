package ssm

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// defaultKey is the KMS key a SecureString written without KeyId is
// encrypted under, and the KeyId it reads back with (ssm-parameters.md 5.1,
// 5.4). KMS creates the AWS managed key behind it on first use
// (internal/service/kms/doc.go).
const defaultKey = "alias/aws/ssm"

// servicePrincipal is the principal Parameter Store calls KMS as for the
// default key (ssm-parameters.md 5.6).
const servicePrincipal = "ssm.amazonaws.com"

// encryptionContext is the context Parameter Store sends KMS for a
// Standard SecureString (ssm-parameters.md 5.2): the parameter's ARN.
func encryptionContext(arn aws.ARN) map[string]string {
	return map[string]string{"PARAMETER_ARN": arn.String()}
}

// keyRefFor is the KeyId a SecureString version is encrypted under: the
// request's KeyId as given, else, under switch ssm.put.omitted-fields
// (preserved), the one base was encrypted under, else the default key.
// Under switch ssm.kms.key-id-echo (verbatim) it is also the KeyId read
// back (ssm-parameters.md 5.4).
func keyRefFor(requested *string, base value) string {
	if requested != nil {
		return *requested
	}
	if sv, ok := base.(sealedValue); ok {
		return sv.KeyRef
	}
	return defaultKey
}

// kmsTarget is how Parameter Store calls KMS for keyRef in scope: as the
// caller through forward access for a customer managed key, and as
// ssm.amazonaws.com for the default key, so a default-key SecureString
// needs no KMS permission of the caller and records no kms action under
// it. UNVERIFIED (SSM-22 step 08), ssm-parameters.md 5.6.
func (s *Service) kmsTarget(ctx context.Context, c *kernel.Call, scope aws.Scope, keyRef string, p kmsPurpose) (kernel.Target, *awserr.Error) {
	managed, err := s.isDefaultKey(ctx, c, scope, keyRef, p)
	if err != nil {
		return kernel.Target{}, err
	}
	if managed {
		return kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: servicePrincipal}, Via: ref.KMSForward}, nil
	}
	return kernel.Target{Scope: scope, As: kernel.AsCaller{}, Via: ref.KMSForward}, nil
}

// isDefaultKey reports whether keyRef names scope's AWS managed key
// aws/ssm, as Secrets Manager decides for its own managed key
// (internal/service/secretsmanager/values.go): an alias or alias ARN by its
// name, since KMS lets no customer alias point at an AWS managed key; a key
// id or key ARN when KMS lists that alias for it, asked as Parameter Store
// itself. A key KMS does not find is not the managed key; Encrypt or
// Decrypt then reports it.
func (s *Service) isDefaultKey(ctx context.Context, c *kernel.Call, scope aws.Scope, keyRef string, p kmsPurpose) (bool, *awserr.Error) {
	if strings.HasPrefix(keyRef, "alias/") {
		return keyRef == defaultKey, nil
	}
	if a, err := aws.ParseARN(keyRef); err == nil && strings.HasPrefix(a.Resource, "alias/") {
		return a.Resource == defaultKey && a.Partition == scope.Partition.String() &&
			a.Region == scope.Region.String() && a.Account == scope.Account.String(), nil
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpListAliases,
		kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: servicePrincipal}, Via: ref.KMSForward},
		&kmsapi.ListAliasesInput{KeyId: &keyRef})
	switch {
	case err == nil:
		return slices.ContainsFunc(out.Aliases, func(a kmsapi.AliasListEntry) bool { return a.AliasName != nil && *a.AliasName == defaultKey }), nil
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape:
		return false, nil
	}
	return false, kmsFailure("ListAliases", p, err)
}

// seal encrypts plaintext as the value of the parameter canonical of scope
// under keyRef: KMS Encrypt with the parameter's ARN as encryption context
// (ssm-parameters.md 5.2). It runs before the caller takes its cell, since
// an internal call under a cell lock is a bug (ARCHITECTURE.md 8 rule 1).
func (s *Service) seal(ctx context.Context, c *kernel.Call, scope aws.Scope, canonical, keyRef, plaintext string) (sealedValue, *awserr.Error) {
	t, err := s.kmsTarget(ctx, c, scope, keyRef, forWrite)
	if err != nil {
		return sealedValue{}, err
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpEncrypt, t, &kmsapi.EncryptInput{
		KeyId:             keyRef,
		Plaintext:         []byte(plaintext),
		EncryptionContext: encryptionContext(parameterARN(scope, canonical)),
	})
	if err != nil {
		return sealedValue{}, kmsFailure("Encrypt", forWrite, err)
	}
	return sealedValue{Ciphertext: out.CiphertextBlob, KeyRef: keyRef}, nil
}

// open decrypts a SecureString value of the parameter canonical of scope.
// Like seal, it runs outside any cell.
func (s *Service) open(ctx context.Context, c *kernel.Call, scope aws.Scope, canonical string, v sealedValue) (string, *awserr.Error) {
	t, err := s.kmsTarget(ctx, c, scope, v.KeyRef, forRead)
	if err != nil {
		return "", err
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpDecrypt, t, &kmsapi.DecryptInput{
		CiphertextBlob:    v.Ciphertext,
		EncryptionContext: encryptionContext(parameterARN(scope, canonical)),
	})
	if err != nil {
		return "", kmsFailure("Decrypt", forRead, err)
	}
	return string(out.Plaintext), nil
}

// valueText is a version's Value as a read returns it: the text of a String
// or StringList whatever decrypt says (ssm-parameters.md 5.3); for a
// SecureString the plaintext with decrypt, else the base64 of the KMS
// ciphertext blob, which KMS Decrypt opens with the parameter's ARN as
// encryption context (ssm-parameters.md 5.2, 5.3).
func (s *Service) valueText(ctx context.Context, c *kernel.Call, scope aws.Scope, canonical string, v value, decrypt bool) (string, *awserr.Error) {
	switch v := v.(type) {
	case plainValue:
		return v.Text, nil
	case sealedValue:
		if decrypt {
			return s.open(ctx, c, scope, canonical, v)
		}
		return base64.StdEncoding.EncodeToString(v.Ciphertext), nil
	}
	return "", awserr.Internalf(&valueKindError{Name: canonical})
}

// valueKindError is a stored value of neither kind, a bug in this package.
type valueKindError struct{ Name string }

func (e *valueKindError) Error() string {
	return "ssm: parameter " + e.Name + " holds a value that is neither plain nor sealed"
}

// keyIDOf is the KeyId metadata reports for v: the KeyRef of a SecureString,
// none for a String or StringList (ssm-parameters.md 5.4 [M]).
func keyIDOf(v value) *string {
	if sv, ok := v.(sealedValue); ok {
		return new(sv.KeyRef)
	}
	return nil
}

// kmsPurpose is what a KMS call was for, which decides what a failure
// tells the caller stayed unchanged.
type kmsPurpose int

const (
	forWrite kmsPurpose = iota + 1 // sealing a value PutParameter would store
	forRead                        // opening a value a read would return
)

// kmsFailure is Parameter Store's answer for a failed KMS call op made for
// p: a KMS access denial is SSM's AccessDeniedException carrying KMS's
// message (SSM-22 step 07); any other KMS client error is InvalidKeyId
// carrying KMS's message (ssm-parameters.md 5.5, 12.3; SSM-22 steps 04-06
// for a missing alias and a disabled key); a KMS server error is
// InternalServerError naming the call and what was left as it was; an
// understudy bug passes through. AWS appends to KMS's message the KMS
// client's "(Service: AWSKMS; Status Code: 400; Error Code: <code>; Request
// ID: <KMS request id>; Proxy: null)", which understudy does not, having no
// KMS request id of its own to name.
func kmsFailure(op string, p kmsPurpose, err *awserr.Error) *awserr.Error {
	if _, internal := err.Origin.(awserr.Internal); internal {
		return err
	}
	if err.ID.Shape == kmsapi.DenialError().Shape {
		return ssmapi.ErrAccessDeniedException(err.Message)
	}
	if err.ID.Status >= 500 {
		outcome := "Nothing was written; try the request again."
		if p == forRead {
			outcome = "The value was not returned and the parameter is unchanged; try the request again."
		}
		return ssmapi.ErrInternalServerError(fmt.Sprintf(
			"The KMS %s call for this SecureString parameter failed with %s (%d): %s %s",
			op, err.ID.Shape, err.ID.Status, err.Message, outcome))
	}
	return ssmapi.ErrInvalidKeyId(err.Message)
}
