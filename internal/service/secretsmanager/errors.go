package secretsmanager

import (
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the ones
// recorded against AWS (secrets-logs-events-alarms.md 2) unless a comment
// says otherwise; the unrecorded ones are kept here so a capture changes
// them in one place.

func errSecretNotFound() *awserr.Error {
	return secretsmanagerapi.ErrResourceNotFoundException("Secrets Manager can't find the specified secret.")
}

func errStageValueNotFound(label string) *awserr.Error {
	return secretsmanagerapi.ErrResourceNotFoundException("Secrets Manager can't find the specified secret value for staging label: " + label)
}

func errVersionValueNotFound(id string) *awserr.Error {
	return secretsmanagerapi.ErrResourceNotFoundException("Secrets Manager can't find the specified secret value for VersionId: " + id)
}

func errStageVersionMismatch() *awserr.Error {
	return secretsmanagerapi.ErrInvalidRequestException("You provided a VersionStage that is not associated to the provided VersionId.")
}

func errMarkedForDeletion() *awserr.Error {
	return secretsmanagerapi.ErrInvalidRequestException("You can't perform this operation on the secret because it was marked for deletion.")
}

func errTokenRequired() *awserr.Error {
	return secretsmanagerapi.ErrInvalidRequestException("You must provide a ClientRequestToken value. We recommend a UUID-type value.")
}

func errSecretExists(name string) *awserr.Error {
	return secretsmanagerapi.ErrResourceExistsException("The operation failed because the secret " + name + " already exists.")
}

func errNameScheduled() *awserr.Error {
	return secretsmanagerapi.ErrInvalidRequestException("You can't create this secret because a secret with this name is already scheduled for deletion.")
}

func errInvalidName() *awserr.Error {
	return secretsmanagerapi.ErrValidationException("Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!")
}

// errTokenInUse is moto's text for a token reused with another value; AWS's
// is unrecorded (secrets-logs-events-alarms.md 2.3).
func errTokenInUse(token, arn string) *awserr.Error {
	return secretsmanagerapi.ErrResourceExistsException("You can't use ClientRequestToken " + token + " because that value is already in use for a version of secret " + arn + ".")
}

// errBothValues is unrecorded.
func errBothValues() *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("You can't specify both a binary secret value and a string secret value in the same secret.")
}

// errNoValue is unrecorded.
func errNoValue() *awserr.Error {
	return secretsmanagerapi.ErrInvalidRequestException("You must provide either SecretString or SecretBinary.")
}

// errRecoveryWindow is moto's text; AWS's is unrecorded
// (secrets-logs-events-alarms.md 2.5).
func errRecoveryWindow() *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("The RecoveryWindowInDays value must be between 7 and 30 days (inclusive).")
}

// errForceWithWindow is unrecorded.
func errForceWithWindow() *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("You can't use ForceDeleteWithoutRecovery in conjunction with RecoveryWindowInDays.")
}

// errCurrentNotRemovable is the text the Terraform provider quotes from
// AWS (secrets-logs-events-alarms.md 2.2).
func errCurrentNotRemovable() *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("You can only move staging label AWSCURRENT to a different secret version. It can't be completely removed.")
}

// errLabelAttached is unrecorded: RemoveFromVersionId is absent while
// another version holds the label. AWS documents only that the move fails
// when RemoveFromVersionId does not name the current holder.
func errLabelAttached(label, holder string) *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("The parameter RemoveFromVersionId can't be empty. Staging label " + label + " is currently attached to version " + holder + ", so you must explicitly reference that version in RemoveFromVersionId.")
}

// errLabelOnOther is unrecorded: RemoveFromVersionId names a version other
// than the one holding the label.
func errLabelOnOther(label, holder, from string) *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("Staging label " + label + " is attached to version " + holder + ", not to RemoveFromVersionId " + from + ". Set RemoveFromVersionId to " + holder + " to move the label.")
}

// errLabelNotOn is unrecorded.
func errLabelNotOn(label, id string) *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("The staging label " + label + " is not attached to version " + id + ".")
}

// errNoStageTarget is unrecorded.
func errNoStageTarget() *awserr.Error {
	return secretsmanagerapi.ErrInvalidParameterException("You must specify MoveToVersionId, RemoveFromVersionId or both.")
}

func errTooManyLabels() *awserr.Error {
	return secretsmanagerapi.ErrLimitExceededException("A secret can have at most 20 staging labels across all of its versions.")
}

func errTooManyVersions() *awserr.Error {
	return secretsmanagerapi.ErrLimitExceededException("A secret can have at most 100 versions; versions without staging labels are removed only once they are 24 hours old.")
}

func errTooManySecrets() *awserr.Error {
	return secretsmanagerapi.ErrLimitExceededException("You have reached the quota of 500000 secrets in this account and Region.")
}

func errInvalidNextToken() *awserr.Error {
	return secretsmanagerapi.ErrInvalidNextTokenException("The NextToken value is not one a previous page returned.")
}

// errEncryption and errDecryption are a KMS failure as Secrets Manager
// reports it (EncryptionFailure and DecryptionFailure are the documented
// codes for a disabled or deleted key); the text, which AWS does not
// document, names the KMS error so the cause is visible.
func errEncryption(kms *awserr.Error) *awserr.Error {
	return secretsmanagerapi.ErrEncryptionFailure("Secrets Manager can't encrypt the secret value with the KMS key: " + kms.ID.JSONType + ": " + kms.Message)
}

func errDecryption(kms *awserr.Error) *awserr.Error {
	return secretsmanagerapi.ErrDecryptionFailure("Secrets Manager can't decrypt the secret value with the KMS key: " + kms.ID.JSONType + ": " + kms.Message)
}

// errUnsupported is understudy's own answer for a request member it does
// not implement, so a caller never gets behavior that only looks like AWS's.
func errUnsupported(what string) *awserr.Error {
	return secretsmanagerapi.ErrNotImplemented("understudy does not implement " + what + " in Secrets Manager. Run 'understudy coverage --service secretsmanager' for what is supported.")
}
