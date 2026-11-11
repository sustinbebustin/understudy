package secretsmanager

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// defaultKey is the KMS key of a secret without KmsKeyId: the AWS managed
// key, which KMS creates on first use.
const defaultKey = "alias/aws/secretsmanager"

// valueKind says which member a secret value came in and goes back out in.
type valueKind string

const (
	kindString valueKind = "string"
	kindBinary valueKind = "binary"
)

// plain is a secret value in the clear. It never reaches state, the
// journal or a log.
type plain struct {
	kind  valueKind
	bytes []byte
}

// valueOf reads the SecretString and SecretBinary members: at most one may
// be set, and ok is false when neither is.
func valueOf(s *string, b []byte) (p plain, ok bool, err *awserr.Error) {
	switch {
	case s != nil && b != nil:
		return plain{}, false, errBothValues()
	case s != nil:
		return plain{kind: kindString, bytes: []byte(*s)}, true, nil
	case b != nil:
		return plain{kind: kindBinary, bytes: b}, true, nil
	}
	return plain{}, false, nil
}

func (p plain) equal(q plain) bool { return p.kind == q.kind && bytes.Equal(p.bytes, q.bytes) }

// sealed is a secret value encrypted under a data key: AES-256-GCM with the
// secret ARN, version id and kind as additional authenticated data and the
// all-zero nonce (see seal). DataKey is the data key as KMS encrypted it,
// under the encryption context Secrets Manager uses (iam-sts-kms.md 5.8).
type sealed struct {
	Kind    valueKind `json:"kind"`
	DataKey []byte    `json:"dataKey"`
	Box     []byte    `json:"box"`
}

// encryptionContext is the context Secrets Manager sends KMS for a version
// (iam-sts-kms.md 5.8).
func encryptionContext(arn, versionID string) map[string]string {
	return map[string]string{"SecretARN": arn, "SecretVersionId": versionID}
}

func aad(arn, versionID string, kind valueKind) []byte {
	return []byte(arn + "\x00" + versionID + "\x00" + string(kind))
}

// kmsTarget is a forward-access call to KMS in the secret's scope, charged
// to the caller (ARCHITECTURE.md 16).
func kmsTarget(scope aws.Scope) kernel.Target {
	return kernel.Target{Scope: scope, As: kernel.AsCaller{}, Via: ref.KMSForward}
}

// encryptionKey is the KMS key of a secret or version, as its stored
// KMSKeyID reads (keyOf).
//
//sumtype:decl
type encryptionKey interface{ isEncryptionKey() }

// managedKey is the AWS managed key aws/secretsmanager, sent as no
// KmsKeyId or named by its alias ARN, key id or key ARN; it is stored as
// nil, so DescribeSecret omits it.
type managedKey struct{}

// managedKeyByAlias is the AWS managed key named by its bare alias, which
// is stored as sent and which DescribeSecret echoes.
type managedKeyByAlias struct{}

// customerKey is any other key, stored as the identifier the caller sent.
// Only a customer key needs an access check, since every caller of the
// account can use the AWS managed key.
type customerKey struct{ id string }

func (managedKey) isEncryptionKey()        {}
func (managedKeyByAlias) isEncryptionKey() {}
func (customerKey) isEncryptionKey()       {}

// keyOf reads a stored KMSKeyID: nil or empty is the AWS managed key, the
// bare alias is that key named by its alias, and anything else a customer
// key. storedKey is its inverse.
func keyOf(kmsKeyID *string) encryptionKey {
	switch {
	case kmsKeyID == nil || *kmsKeyID == "":
		return managedKey{}
	case *kmsKeyID == defaultKey:
		return managedKeyByAlias{}
	}
	return customerKey{id: *kmsKeyID}
}

// storedKey is the KMSKeyID a secret or version stores for k.
func storedKey(k encryptionKey) *string {
	switch k := k.(type) {
	case managedKey:
		return nil
	case managedKeyByAlias:
		return new(defaultKey)
	case customerKey:
		return new(k.id)
	}
	return nil
}

// keyRef is the KeyId Secrets Manager asks KMS for under k.
func keyRef(k encryptionKey) string {
	if ck, ok := k.(customerKey); ok {
		return ck.id
	}
	return defaultKey
}

// isCustomerKey reports a key other than the AWS managed key.
func isCustomerKey(k encryptionKey) bool {
	_, ok := k.(customerKey)
	return ok
}

// servicePrincipal is the principal Secrets Manager calls KMS as on its own
// behalf.
const servicePrincipal = "secretsmanager.amazonaws.com"

// resolveKey is the key a secret of scope takes for the KmsKeyId a request
// sends: managedKey for none and for the AWS managed key
// aws/secretsmanager named by its alias ARN, key id or key ARN,
// managedKeyByAlias for its bare alias, and a customerKey of the
// identifier as sent for any other key. AWS treats
// every name of that key as the managed key: ListSecretVersionIds calls it
// DefaultEncryptionKey, and another account cannot use it
// (API_DescribeSecret.html, API_UpdateSecret.html). DescribeSecret echoes
// the bare alias (secretsmanager/R07/06-describe-s-awsalias.json); whether
// it echoes the other names is unrecorded, so they stay omitted as the
// documentation says. Like seal, it runs outside any cell.
func (s *Service) resolveKey(ctx context.Context, c *kernel.Call, scope aws.Scope, kmsKeyID *string) (encryptionKey, *awserr.Error) {
	ck, ok := keyOf(kmsKeyID).(customerKey)
	if !ok {
		return keyOf(kmsKeyID), nil
	}
	managed, err := s.isDefaultKey(ctx, c, scope, ck.id)
	switch {
	case err != nil:
		return nil, err
	case managed:
		return managedKey{}, nil
	}
	return ck, nil
}

// isDefaultKey reports whether id names scope's AWS managed key
// aws/secretsmanager. An alias or alias ARN does by its name alone, since
// KMS lets no customer alias point at an AWS managed key. A key id or key
// ARN does when KMS lists that alias for the key: a ListAliases that runs
// as Secrets Manager itself and is not charged to the caller, since AWS
// documents no kms:ListAliases permission for Secrets Manager callers. A
// key KMS does not find in scope is not the managed key; seal or
// checkKeyAccess then reports it as AWS does. A server without KMS bound
// cannot tell, and keeps id as sent.
func (s *Service) isDefaultKey(ctx context.Context, c *kernel.Call, scope aws.Scope, id string) (bool, *awserr.Error) {
	if strings.HasPrefix(id, "alias/") {
		return id == defaultKey, nil
	}
	if a, err := aws.ParseARN(id); err == nil && strings.HasPrefix(a.Resource, "alias/") {
		return a.Resource == defaultKey && a.Partition == scope.Partition.String() && a.Region == scope.Region.String() && a.Account == scope.Account.String(), nil
	}
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpListAliases,
		kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: servicePrincipal}, Via: ref.KMSForward},
		&kmsapi.ListAliasesInput{KeyId: &id})
	switch {
	case err == nil:
		return slices.ContainsFunc(out.Aliases, func(a kmsapi.AliasListEntry) bool { return a.AliasName != nil && *a.AliasName == defaultKey }), nil
	case err.ID.Shape == kmsapi.IdentityNotFoundException().Shape, err.ID.Shape == awserr.NotImplemented().Shape:
		return false, nil
	}
	return false, kmsFailure(err, errEncryption)
}

// seal encrypts p as version versionID of the secret arn in scope: KMS
// GenerateDataKey under key, then AES-256-GCM here. It runs before
// the caller takes its cell, since an internal call under a cell lock is a
// bug (ARCHITECTURE.md 8 rule 1).
//
// The nonce is all zeros, which is safe because every data key seals
// exactly one plaintext: each seal asks KMS for a fresh data key, and a
// data key moved under another KMS key (rewrap) keeps its one box. So no
// randomness is drawn here, and crypto/rand stays with ids, kms and sns
// (ADR 0018).
func (s *Service) seal(ctx context.Context, c *kernel.Call, scope aws.Scope, arn, versionID string, key encryptionKey, p plain) (sealed, *awserr.Error) {
	dk, err := s.generateDataKey(ctx, c, scope, arn, versionID, key)
	if err != nil {
		return sealed{}, err
	}
	defer clear(dk.Plaintext)
	aead, gerr := newAEAD(dk.Plaintext)
	if gerr != nil {
		return sealed{}, awserr.Internalf(gerr)
	}
	return sealed{
		Kind:    p.kind,
		DataKey: dk.CiphertextBlob,
		Box:     aead.Seal(nil, make([]byte, aead.NonceSize()), p.bytes, aad(arn, versionID, p.kind)), //nolint:gosec // every data key seals exactly one plaintext, so a fixed nonce never repeats under a key (see above)
	}, nil
}

// open decrypts version v of the secret arn in scope: KMS Decrypt of its
// data key, then AES-256-GCM here. Like seal, it runs outside any cell.
func (s *Service) open(ctx context.Context, c *kernel.Call, scope aws.Scope, arn string, v Version) (plain, *awserr.Error) {
	dk, err := s.decryptDataKey(ctx, c, scope, arn, v.ID, v.key(), v.Value.DataKey)
	if err != nil {
		return plain{}, err
	}
	defer clear(dk.Plaintext)
	aead, gerr := newAEAD(dk.Plaintext)
	if gerr != nil {
		return plain{}, awserr.Internalf(gerr)
	}
	b, oerr := aead.Open(nil, make([]byte, aead.NonceSize()), v.Value.Box, aad(arn, v.ID, v.Value.Kind))
	if oerr != nil {
		return plain{}, awserr.Internalf(&SealError{ARN: arn, VersionID: v.ID})
	}
	return plain{kind: v.Value.Kind, bytes: b}, nil
}

// defaultKeyFromOtherAccount refuses a caller of another account than
// scope's the use of scope's AWS managed key: that key serves only its own
// account, whatever the secret's resource policy grants. Every KMS call of
// the service checks it first, so a secret under the managed key stays
// describable and taggable across accounts but its value cannot be read or
// written.
func defaultKeyFromOtherAccount(c *kernel.Call, scope aws.Scope, key encryptionKey) *awserr.Error {
	if !isCustomerKey(key) && scope.Account != c.Scope().Account {
		return errDefaultKeyOtherAccount()
	}
	return nil
}

// generateDataKey asks KMS for an AES-256 data key under key with the
// encryption context of version versionID of the secret arn; a KMS refusal
// is EncryptionFailure.
func (s *Service) generateDataKey(ctx context.Context, c *kernel.Call, scope aws.Scope, arn, versionID string, key encryptionKey) (*kmsapi.GenerateDataKeyOutput, *awserr.Error) {
	if err := defaultKeyFromOtherAccount(c, scope, key); err != nil {
		return nil, err
	}
	dk, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpGenerateDataKey, kmsTarget(scope), &kmsapi.GenerateDataKeyInput{
		KeyId:             keyRef(key),
		EncryptionContext: encryptionContext(arn, versionID),
		KeySpec:           new(kmsapi.DataKeySpecAes256),
	})
	if err != nil {
		return nil, kmsFailure(err, errEncryption)
	}
	return dk, nil
}

// decryptDataKey asks KMS to decrypt a data key sealed under key for
// version versionID of the secret arn; a KMS refusal is DecryptionFailure.
// KMS reads the key from the blob; key is for the account check.
func (s *Service) decryptDataKey(ctx context.Context, c *kernel.Call, scope aws.Scope, arn, versionID string, key encryptionKey, blob []byte) (*kmsapi.DecryptOutput, *awserr.Error) {
	if err := defaultKeyFromOtherAccount(c, scope, key); err != nil {
		return nil, err
	}
	dk, err := s.kmsDecrypt(ctx, c, scope, arn, versionID, blob)
	if err != nil {
		return nil, kmsFailure(err, errDecryption)
	}
	return dk, nil
}

// kmsDecrypt is the KMS Decrypt of a data key for version versionID of the
// secret arn, with KMS's error as KMS answered it.
func (s *Service) kmsDecrypt(ctx context.Context, c *kernel.Call, scope aws.Scope, arn, versionID string, blob []byte) (*kmsapi.DecryptOutput, *awserr.Error) {
	return kernel.Invoke(ctx, s.k, c, kmsapi.OpDecrypt, kmsTarget(scope), &kmsapi.DecryptInput{
		CiphertextBlob:    blob,
		EncryptionContext: encryptionContext(arn, versionID),
	})
}

// rewrapped is the data key of one version moved under another KMS key:
// from is the blob it replaces, to the blob under the new key.
type rewrapped struct {
	from, to []byte
}

// rewrap moves the data keys of sec's versions labeled AWSCURRENT,
// AWSPREVIOUS and AWSPENDING under key, as UpdateSecret does when the
// secret's key changes (secrets-logs-events-alarms.md 2.4): KMS Decrypt
// under the old key, then Encrypt under the new one, both with the
// version's encryption context. The boxes stay as they are. Like seal, it
// runs outside any cell; the result is by version id.
//
// A version whose data key KMS refuses to move, because it cannot decrypt
// it under the old key or encrypt it under the new one, is left out and
// stays under the key that sealed it: AWS documents that without
// kms:Decrypt on the previous key, or kms:Encrypt on the new one, "the
// existing versions are not re-encrypted" while the key still changes
// (security-encryption.html, API_UpdateSecret.html). A disabled or deleted
// old key therefore never traps a secret on it. Another account's use of
// the AWS managed key is still refused, and an understudy bug still fails
// the call.
func (s *Service) rewrap(ctx context.Context, c *kernel.Call, scope aws.Scope, sec Secret, key encryptionKey) (map[string]rewrapped, *awserr.Error) {
	arn := sec.arn(scope).String()
	out := map[string]rewrapped{}
	tried := map[string]bool{}
	for _, label := range []string{stageCurrent, stagePrevious, stagePending} {
		id, held := sec.Stages[label]
		if !held || tried[id] {
			continue
		}
		tried[id] = true
		v := sec.Versions[id]
		if err := firstError(defaultKeyFromOtherAccount(c, scope, v.key()), defaultKeyFromOtherAccount(c, scope, key)); err != nil {
			return nil, err
		}
		blob, err := s.moveDataKey(ctx, c, scope, arn, v, key)
		switch {
		case err == nil:
			out[id] = rewrapped{from: v.Value.DataKey, to: blob}
		case isInternal(err):
			return nil, err
		}
	}
	return out, nil
}

// moveDataKey decrypts the data key of version v of the secret arn and
// encrypts it under key, with the version's encryption context. Its
// error is KMS's own, unwrapped, so rewrap can tell a refusal from a bug.
func (s *Service) moveDataKey(ctx context.Context, c *kernel.Call, scope aws.Scope, arn string, v Version, key encryptionKey) ([]byte, *awserr.Error) {
	dk, err := s.kmsDecrypt(ctx, c, scope, arn, v.ID, v.Value.DataKey)
	if err != nil {
		return nil, err
	}
	defer clear(dk.Plaintext)
	out, err := kernel.Invoke(ctx, s.k, c, kmsapi.OpEncrypt, kmsTarget(scope), &kmsapi.EncryptInput{
		KeyId:             keyRef(key),
		Plaintext:         dk.Plaintext,
		EncryptionContext: encryptionContext(arn, v.ID),
	})
	if err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

// applyRewrap stores the data keys rewrap moved, on cur's own maps, with
// the version's KMS key now cur's. A version whose data key changed since
// rewrap read it, or that is gone, keeps what it has.
func applyRewrap(cur *Secret, moved map[string]rewrapped) {
	for id, m := range moved {
		v, ok := cur.Versions[id]
		if !ok || !bytes.Equal(v.Value.DataKey, m.from) {
			continue
		}
		v.Value.DataKey, v.KMSKeyID = m.to, cur.KMSKeyID
		cur.Versions[id] = v
	}
}

// validationVersion is the SecretVersionId of the encryption context
// Secrets Manager sends when it checks access to a key (iam-sts-kms.md 5.8).
const validationVersion = "RequestToValidateKeyAccess"

// checkKeyAccess is the check Secrets Manager runs when a secret's key is
// set or changed with no value to seal under it: GenerateDataKey, then
// Decrypt of the result, with the results discarded (iam-sts-kms.md 5.8).
// Like seal, it runs outside any cell.
func (s *Service) checkKeyAccess(ctx context.Context, c *kernel.Call, scope aws.Scope, arn string, key encryptionKey) *awserr.Error {
	dk, err := s.generateDataKey(ctx, c, scope, arn, validationVersion, key)
	if err != nil {
		return err
	}
	clear(dk.Plaintext)
	opened, err := s.decryptDataKey(ctx, c, scope, arn, validationVersion, key, dk.CiphertextBlob)
	if err != nil {
		return err
	}
	clear(opened.Plaintext)
	return nil
}

// SealError is a stored version whose box does not open under the data key
// KMS returned for it: state written by another build or corrupted.
type SealError struct {
	ARN, VersionID string
}

func (e *SealError) Error() string {
	return "secretsmanager: version " + e.VersionID + " of " + e.ARN + " does not open under its own data key; the stored value is corrupt and cannot be recovered, so put a new value"
}

// kmsFailure is how a failed KMS call reaches the Secrets Manager caller:
// an understudy bug stays Internal; anything KMS answered becomes the
// documented EncryptionFailure or DecryptionFailure. The call's own
// authorization ran at step 8, before any KMS call.
func kmsFailure(err *awserr.Error, wrap func(*awserr.Error) *awserr.Error) *awserr.Error {
	if isInternal(err) {
		return err
	}
	return wrap(err)
}

// isInternal reports an understudy bug rather than an answer KMS gave.
func isInternal(err *awserr.Error) bool {
	_, internal := err.Origin.(awserr.Internal)
	return internal
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, &DataKeySizeError{Got: len(key)}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// DataKeySizeError is a data key KMS returned at a size other than the 32
// bytes of the AES_256 key Secrets Manager asks for.
type DataKeySizeError struct {
	Got int
}

func (e *DataKeySizeError) Error() string {
	return "secretsmanager: KMS returned a " + strconv.Itoa(e.Got) + "-byte data key for an AES_256 request; want 32 bytes"
}
