package sts

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors; "inferred" marks a
// text no source records.

// errUnsigned answers an unsigned request with the text of STS's common
// errors page (inferred as the message).
func errUnsigned() *awserr.Error {
	return stsapi.ErrMissingAuthenticationToken("The request must contain either a valid (registered) AWS access key ID or X.509 certificate.")
}

// errSessionCredentials answers GetSessionToken signed with temporary
// credentials: a role session's, or an earlier GetSessionToken's under
// sigv4 verify (inferred).
func errSessionCredentials() *awserr.Error {
	return stsapi.ErrAccessDenied("Cannot call GetSessionToken with session credentials")
}

// errNotAuthorized is the trust failure of AssumeRole (iam-sts-kms.md 4.2).
func errNotAuthorized(caller, role string) *awserr.Error {
	return stsapi.ErrAccessDenied("User: " + caller + " is not authorized to perform: sts:AssumeRole on resource: " + role)
}

// errInvalidRoleARN answers a RoleArn that names no role (error-matrix.md
// 5.11).
func errInvalidRoleARN(arn string) *awserr.Error {
	return stsapi.ErrValidationException(arn + " is invalid")
}

// errOverMaxSession is the real text of a duration above the role's
// MaxSessionDuration (iam-sts-kms.md 4.2).
func errOverMaxSession() *awserr.Error {
	return stsapi.ErrValidationException("The requested DurationSeconds exceeds the MaxSessionDuration set for this role.")
}

// errTooManyPolicyARNs answers a request naming more managed session
// policies than AWS allows, in the grammar of a model violation; the model
// carries no such limit, and the text and the omitted value are inferred.
func errTooManyPolicyARNs() *awserr.Error {
	return stsapi.ErrValidationException(awserr.CoralMessage([]awserr.Violation{{
		Path:       awserr.Path{"policyArns"},
		Value:      awserr.RenderedOmitted{},
		Constraint: awserr.LengthMax{Max: maxPolicyARNs},
	}}))
}

// errReservedSourceIdentity answers a SourceIdentity with the reserved
// prefix (inferred).
func errReservedSourceIdentity(value string) *awserr.Error {
	return stsapi.ErrValidationException("SourceIdentity " + value + " is not valid: a source identity cannot begin with " + reservedPrefix + ", which AWS reserves for its own use.")
}

// errSessionTag answers a request that breaks a session tag rule. AWS
// answers every such rule it is recorded breaking with InvalidParameterValue,
// 400, Sender (localstack test_sts.snapshot.json@8b9a79f, L104-L208,
// recorded 2025-04-10); the STS model declares no such shape, so the STS
// overlay adds it.
func errSessionTag(message string) *awserr.Error {
	return stsapi.ErrInvalidParameterValue(message)
}

// errReservedTag answers a session tag whose key or value has the reserved
// prefix (text inferred, after IAM's tag rule; code as the recorded session
// tag errors).
func errReservedTag(key string) *awserr.Error {
	return errSessionTag("Session tag " + key + " is not valid: session tag keys and values cannot begin with " + reservedPrefix + ", which AWS reserves for its own use.")
}

// errDuplicateTagKeys answers session tags two of whose keys differ only in
// case (recorded, as errSessionTag cites).
func errDuplicateTagKeys() *awserr.Error {
	return errSessionTag("Duplicate tag keys found. Please note that Tag keys are case insensitive.")
}

// errTransitiveTagKeyMissing answers a transitive tag key that names none
// of the session tags (recorded, as errSessionTag cites).
func errTransitiveTagKeyMissing() *awserr.Error {
	return errSessionTag("The specified transitive tag key must be included in the requested tags.")
}

// errPackedPolicyTooLarge answers session policies whose plaintext is over
// the documented limit (inferred).
func errPackedPolicyTooLarge(n int) *awserr.Error {
	return stsapi.ErrPackedPolicyTooLargeException("The session policies are " + strconv.Itoa(n) + " characters of plaintext; the inline policy and the managed policy ARNs together cannot exceed " + strconv.Itoa(maxSessionPolicyText) + ".")
}

// errChainedSession answers a role session that assumes a role for more
// than an hour (inferred, after the AssumeRole documentation of role
// chaining).
func errChainedSession() *awserr.Error {
	return stsapi.ErrValidationException("The requested DurationSeconds exceeds the 1 hour session limit for roles assumed by role chaining.")
}
