package iam

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are those of
// iam-sts-kms.md 3.4 (the role texts are the ones Terraform matches) unless
// a comment says otherwise; "inferred" marks a text no source records.

func errNoSuchRole(name string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The role with name " + name + " cannot be found.")
}

func errRoleExists(name string) *awserr.Error {
	return iamapi.ErrEntityAlreadyExistsException("Role with name " + name + " already exists.")
}

func errNoSuchUser(name string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The user with name " + name + " cannot be found.")
}

// errUserExists follows the role text (inferred).
func errUserExists(name string) *awserr.Error {
	return iamapi.ErrEntityAlreadyExistsException("User with name " + name + " already exists.")
}

func errNoSuchPolicy(arn string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Policy " + arn + " was not found.")
}

func errPolicyExists(name string) *awserr.Error {
	return iamapi.ErrEntityAlreadyExistsException("A policy called " + name + " already exists. Duplicate names are not allowed.")
}

// errNotAttachable answers an attachment of a well-formed ARN that names no
// policy the account can attach. AttachRolePolicy and AttachUserPolicy
// recorded this text (error-matrix.md 5.10 T3: LocalStack snapshots
// test_role_attach_policy 2025-06-19 and test_user_attach_policy
// 2025-03-06).
func errNotAttachable(arn string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Policy " + arn + " does not exist or is not attachable.")
}

// errNotAttached answers a detach of a policy the entity does not attach
// (moto's text, UNVERIFIED).
func errNotAttached(arn string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Policy " + arn + " was not attached.")
}

// errNoSuchBoundary answers a permissions boundary the account cannot see
// (inferred).
func errNoSuchBoundary(arn string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Scope ARN: " + arn + " does not exist or is not attachable.")
}

// errInvalidPolicyARN answers a policy ARN that is no managed policy's on
// the paths no recording covers: the policy operations and permissions
// boundaries (inferred).
func errInvalidPolicyARN(arn string) *awserr.Error {
	return iamapi.ErrInvalidInputException("ARN " + arn + " is not valid.")
}

// errUnparsableARN answers an attachment whose PolicyArn does not parse as
// an ARN at all, such as a bare policy name. The two spaces are AWS's
// (error-matrix.md 5.10 T3, the snapshots errNotAttachable cites).
func errUnparsableARN() *awserr.Error {
	return iamapi.ErrValidationException("Invalid ARN:  Could not be parsed!")
}

// errNoSuchVersion is inferred.
func errNoSuchVersion(arn, version string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Policy " + arn + " version " + version + " does not exist.")
}

func errTooManyVersions() *awserr.Error {
	return iamapi.ErrLimitExceededException("A managed policy can have up to " + strconv.Itoa(maxPolicyVersions) + " versions. Before you create a new version, you must delete an existing version.")
}

func errDeleteDefaultVersion() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete the default version of a policy.")
}

// errPolicyAttached and errPolicyHasVersions are inferred; the second
// follows the DeletePolicy documentation.
func errPolicyAttached() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete a policy attached to entities.")
}

func errPolicyHasVersions() *awserr.Error {
	return iamapi.ErrDeleteConflictException("This policy has more than one version. Before you delete a policy, you must delete the policy's versions. The default version is deleted with the policy.")
}

func errNoSuchRolePolicy(name string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The role policy with name " + name + " cannot be found.")
}

// errNoSuchUserPolicy follows the role text (inferred).
func errNoSuchUserPolicy(name string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The user policy with name " + name + " cannot be found.")
}

func errMustDetachPolicies() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete entity, must detach all policies first.")
}

func errMustDeletePolicies() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete entity, must delete policies first.")
}

func errMustRemoveFromProfiles() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete entity, must remove roles from instance profile first.")
}

// errMustDeleteAccessKeys is versitygw's text (iam-sts-kms.md implication 15,
// UNVERIFIED).
func errMustDeleteAccessKeys() *awserr.Error {
	return iamapi.ErrDeleteConflictException("Cannot delete entity, must delete access keys first.")
}

// errNoSuchInstanceProfile and errInstanceProfileExists: the second is
// moto's; the first follows it (inferred).
func errNoSuchInstanceProfile(name string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("Instance Profile " + name + " cannot be found.")
}

func errInstanceProfileExists(name string) *awserr.Error {
	return iamapi.ErrEntityAlreadyExistsException("Instance Profile " + name + " already exists.")
}

// errRoleNotInProfile answers a removal of a role the profile does not hold
// (inferred).
func errRoleNotInProfile(role, profile string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The role with name " + role + " is not in instance profile " + profile + ".")
}

// errNoSuchAccessKey is inferred.
func errNoSuchAccessKey(id string) *awserr.Error {
	return iamapi.ErrNoSuchEntityException("The Access Key with id " + id + " cannot be found.")
}

// errQuota is IAM's quota text, "Cannot exceed quota for <Quota>: <n>"
// (inferred for every quota name used here).
func errQuota(quota string, limit int) *awserr.Error {
	return iamapi.ErrLimitExceededException("Cannot exceed quota for " + quota + ": " + strconv.Itoa(limit))
}

// errInlineTooLarge answers inline policies whose combined size passes the
// entity's quota (inferred).
func errInlineTooLarge(kind, name string, limit int) *awserr.Error {
	return iamapi.ErrLimitExceededException("Maximum policy size of " + strconv.Itoa(limit) + " bytes exceeded for " + kind + " " + name)
}

// errDuplicateTagKeys is moto's text (iam-sts-kms.md 3.2, UNVERIFIED) for
// keys that match regardless of case; for case-sensitive keys it drops the
// note on case (inferred).
func errDuplicateTagKeys(m tagKeys) *awserr.Error {
	switch m {
	case foldedKeys:
		return iamapi.ErrInvalidInputException("Duplicate tag keys found. Please note that Tag keys are case insensitive.")
	case exactKeys:
	}
	return iamapi.ErrInvalidInputException("Duplicate tag keys found.")
}

// errReservedTag answers a tag whose key or value begins with the prefix
// AWS reserves (inferred).
func errReservedTag(key string) *awserr.Error {
	return iamapi.ErrInvalidInputException("Tag " + key + " is not valid: tag keys and values cannot begin with " + reservedTagPrefix + ", which AWS reserves for its own use.")
}

// errTooManyTags is inferred.
func errTooManyTags() *awserr.Error {
	return iamapi.ErrLimitExceededException("The number of tags has reached the maximum limit of " + strconv.Itoa(maxTags) + ".")
}

// errInvalidMarker is inferred.
func errInvalidMarker() *awserr.Error {
	return iamapi.ErrInvalidInputException("Invalid Marker: it is not one a previous response returned.")
}

// errRootAccessKeys answers a root caller's CreateAccessKey without
// UserName, which AWS serves and understudy does not.
func errRootAccessKeys() *awserr.Error {
	return iamapi.ErrNotImplemented("understudy does not manage access keys of the account's root user; any access key id it did not mint already signs as the root. Pass UserName to create a key for an IAM user. Run 'understudy coverage --service iam' for what is supported.")
}

// errNonUserCredentials answers an operation that defaults to the calling
// user when the caller is a role session, a service or anonymous.
func errNonUserCredentials() *awserr.Error {
	return iamapi.ErrValidationException("Must specify userName when calling with non-User credentials")
}

func errMalformedPolicy(message string) *awserr.Error {
	return iamapi.ErrMalformedPolicyDocumentException(message)
}

// errInvalidJSON answers a policy document that is not JSON
// (error-matrix.md 5.10 T3).
func errInvalidJSON() *awserr.Error {
	return errMalformedPolicy("This policy contains invalid Json")
}
