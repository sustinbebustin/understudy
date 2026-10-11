package sts

import (
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

// errSessionCredentials answers GetSessionToken signed with a role
// session's credentials (inferred).
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

// errChainedSession answers a role session that assumes a role for more
// than an hour (inferred, after the AssumeRole documentation of role
// chaining).
func errChainedSession() *awserr.Error {
	return stsapi.ErrValidationException("The requested DurationSeconds exceeds the 1 hour session limit for roles assumed by role chaining.")
}
