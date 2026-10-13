package kms

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the ones
// recorded against AWS in LocalStack's KMS snapshots
// (tests/aws/services/kms/test_kms.snapshot.json@8b9a79f) unless a comment
// says otherwise.

func errKeyNotFound(keyARN string) *awserr.Error {
	return kmsapi.ErrNotFoundException("Key '" + keyARN + "' does not exist")
}

func errInvalidKeyID(id string) *awserr.Error {
	return kmsapi.ErrNotFoundException("Invalid keyId '" + id + "'")
}

func errInvalidARNRegion(region string) *awserr.Error {
	return kmsapi.ErrNotFoundException("Invalid arn " + region)
}

// errAliasNotFound follows moto's text; AWS's is unrecorded
// (iam-sts-kms.md 5.1).
func errAliasNotFound(aliasARN string) *awserr.Error {
	return kmsapi.ErrNotFoundException("Alias " + aliasARN + " is not found.")
}

// errAliasExists is the text iam-sts-kms.md 5.4 quotes (UNVERIFIED as a
// capture).
func errAliasExists(aliasARN string) *awserr.Error {
	return kmsapi.ErrAlreadyExistsException("An alias with the name " + aliasARN + " already exists")
}

func errAliasPrefix() *awserr.Error {
	return kmsapi.ErrValidationException(`Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html`)
}

// errReservedAlias carries aws-in-a-box's text under the model's
// InvalidAliasNameException: its recorded NotAuthorizedException is not in
// CreateAlias's error list (UNVERIFIED as real; iam-sts-kms.md 5.4).
func errReservedAlias() *awserr.Error {
	return kmsapi.ErrInvalidAliasNameException("The alias name for a customer managed CMK cannot begin with alias/aws/.")
}

// errAliasTarget is aws-in-a-box's text for an alias as TargetKeyId
// (UNVERIFIED as real; iam-sts-kms.md 5.1).
func errAliasTarget() *awserr.Error {
	return kmsapi.ErrValidationException("Aliases must refer to keys. Not aliases")
}

// errAliasQuota is unrecorded: AWS documents LimitExceededException for
// CreateAlias past the quota of aliases per KMS key, but no capture of its
// text exists, so the message is understudy's own and says so.
func errAliasQuota(keyARN string, quota int) *awserr.Error {
	return kmsapi.ErrLimitExceededException("Key " + keyARN + " already has " + strconv.Itoa(quota) +
		" aliases, the quota of aliases per KMS key; delete one of its aliases or target another key (understudy's text; AWS's is unrecorded).")
}

// errPolicyTooLong is unrecorded: CreateKey and PutKeyPolicy document
// LimitExceededException for a key policy past the length constraint, but
// no capture of its text exists, so the message is understudy's own. The
// policy itself is not echoed.
func errPolicyTooLong(chars, quota int) *awserr.Error {
	return kmsapi.ErrLimitExceededException("The key policy is " + strconv.Itoa(chars) + " characters long, more than the quota of " +
		strconv.Itoa(quota) + "; shorten it, for example by merging statements (understudy's text; AWS's is unrecorded).")
}

// errInvalidMarker is unrecorded: the text is understudy's own.
func errInvalidMarker(marker string) *awserr.Error {
	return kmsapi.ErrInvalidMarkerException("The marker " + marker + " is not one a previous page of this listing returned; start the listing again without a Marker (understudy's text; AWS's is unrecorded).")
}

func errDisabled(keyARN string) *awserr.Error {
	return kmsapi.ErrDisabledException(keyARN + " is disabled.")
}

// errPendingDeletion and errNotPendingDeletion are the texts of AWS's
// key-state table, footnotes 3 and 4 (iam-sts-kms.md 5.6); unrecorded.
func errPendingDeletion(keyARN string) *awserr.Error {
	return kmsapi.ErrKMSInvalidStateException(keyARN + " is pending deletion (or pending replica deletion).")
}

func errNotPendingDeletion(keyARN string) *awserr.Error {
	return kmsapi.ErrKMSInvalidStateException(keyARN + " is not pending deletion (or pending replica deletion).")
}

// errDestinationPendingDeletion is the shorter text the key-state table
// gives an UpdateAlias destination, footnote 10 (iam-sts-kms.md 5.6);
// unrecorded.
func errDestinationPendingDeletion(keyARN string) *awserr.Error {
	return kmsapi.ErrKMSInvalidStateException(keyARN + " is pending deletion.")
}

// errInvalidCiphertext has no message: AWS sends none.
func errInvalidCiphertext() *awserr.Error {
	return kmsapi.ErrInvalidCiphertextException("")
}

func errIncorrectKey() *awserr.Error {
	return kmsapi.ErrIncorrectKeyException("The key ID in the request does not identify a CMK that can perform this operation.")
}

func errDryRun() *awserr.Error {
	return kmsapi.ErrDryRunOperationException("The request would have succeeded, but the DryRun option is set.")
}

// errAlgorithm follows the recorded HMAC text ("Algorithm HMAC_SHA_256 is
// incompatible with key spec HMAC_224."); the symmetric case is unrecorded.
func errAlgorithm(alg kmsapi.EncryptionAlgorithmSpec) *awserr.Error {
	return kmsapi.ErrInvalidKeyUsageException("Algorithm " + string(alg) + " is incompatible with key spec SYMMETRIC_DEFAULT.")
}

func errDuplicateTagKeys() *awserr.Error { return kmsapi.ErrTagException("Duplicate tag keys") }

func errTooManyTags() *awserr.Error { return kmsapi.ErrTagException("Too many tags") }

func errReservedTag() *awserr.Error {
	return kmsapi.ErrTagException("Tags beginning with aws: are reserved")
}

// errKeyUsageRequired is recorded in test_hmac_create_key_invalid_operations
// (create-hmac-key-without-key-usage, test_kms.snapshot.json@8b9a79f line
// 902).
func errKeyUsageRequired() *awserr.Error {
	return kmsapi.ErrValidationException("You must specify a KeyUsage value for all KMS keys except for symmetric encryption keys.")
}

// errPolicyPrincipal is unrecorded: CreateKey and PutKeyPolicy document
// that every key policy statement needs a principal, but no capture of
// AWS's refusal exists, so the text is understudy's own. index counts from
// 0, as the Statement array does.
func errPolicyPrincipal(index int, sid string) *awserr.Error {
	which := "Statement " + strconv.Itoa(index)
	if sid != "" {
		which += ` (Sid "` + sid + `")`
	}
	return kmsapi.ErrMalformedPolicyDocumentException(which + " of the key policy has no Principal or NotPrincipal element; each key policy statement must name one or more principals (understudy's text; AWS's is unrecorded).")
}

func errPolicyName() *awserr.Error {
	return kmsapi.ErrNotFoundException("No such policy exists")
}

// errAWSManagedKey and errAWSManagedAlias are unrecorded. AWS refuses these
// changes through the fixed key policy of an AWS managed key, which phase 1
// does not evaluate, so understudy answers with its own explicit refusal
// rather than an imitation of a policy denial. UpdateAlias and DeleteAlias
// model no error for a managed alias, so errAWSManagedAlias answers only
// one that exists; any other name is NotFoundException.
func errAWSManagedKey(keyARN string) *awserr.Error {
	return kmsapi.ErrUnsupportedOperationException(keyARN + " is an AWS managed key. Its tags, rotation, key state, key policy, description, aliases and deletion are managed by AWS and cannot be changed; use a customer managed key (CreateKey) instead.")
}

func errAWSManagedAlias(aliasARN string) *awserr.Error {
	return kmsapi.ErrUnsupportedOperationException(aliasARN + " is the alias of an AWS managed key and cannot be updated or deleted; aliases beginning with alias/aws/ are managed by AWS.")
}

// errUnsupported is understudy's own answer for what it does not implement,
// so a caller never gets behavior that only looks like AWS's.
func errUnsupported(what string) *awserr.Error {
	return kmsapi.ErrUnsupportedOperationException("understudy does not implement " + what + "; it implements symmetric encryption keys with AWS_KMS origin in one Region. Run 'understudy coverage --service kms' for what is supported.")
}
