package sts

import (
	"strconv"

	"github.com/sustinbebustin/understudy/internal/evidence"
)

// Options are the STS settings wire.go fills from the configuration. The
// zero value is AWS's behavior.
type Options struct {
	// AssumeRole is the iam.assumeRole switch.
	AssumeRole RoleCheck
}

// RoleCheck says what AssumeRole requires of the role it names
// (ARCHITECTURE.md 7).
type RoleCheck uint8

// RoleCheck values. The zero value is RoleMustExist.
const (
	// RoleMustExist answers AccessDenied for a role IAM does not hold, as
	// AWS does.
	RoleMustExist RoleCheck = iota
	// RoleLenient mints a session for any well-formed role ARN, for
	// applications that assume roles their tests never create. The session
	// of a role IAM does not hold has a role id derived from the ARN and the
	// largest MaxSessionDuration AWS allows.
	RoleLenient
)

// String returns the switch value as configured: "strict" or "lenient".
func (r RoleCheck) String() string {
	switch r {
	case RoleMustExist:
		return "strict"
	case RoleLenient:
		return "lenient"
	default:
		return "RoleCheck(" + strconv.Itoa(int(r)) + ")"
	}
}

// label is what backs the behavior r selects. A trust failure for a role
// that does not exist is documented in AWS's CDK pipelines README
// (iam-sts-kms.md 4.2); lenient is a deliberate deviation no source backs.
func (r RoleCheck) label() evidence.Label {
	if r == RoleMustExist {
		return evidence.AWSDoc
	}
	return evidence.Inferred
}
