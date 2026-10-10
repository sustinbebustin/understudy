package aws

import "regexp"

// ValidIAMPath reports whether path is one IAM's CreateUser and CreateRole
// accept: the IAM model's pathType, "/" or a run of printable ASCII between
// slashes, at most 512 characters. pathType's own pattern,
// ^(/)|(/[!-~]+/)$, anchors each alternative on one side only; this is its
// evident reading.
func ValidIAMPath(path string) bool {
	return len(path) <= maxIAMPathLen && iamPath.MatchString(path)
}

// ValidIAMName reports whether name is a user or role name IAM accepts: the
// IAM model's userNameType and roleNameType, 1 to 64 of letters, digits and
// _+=,.@-.
func ValidIAMName(name string) bool {
	return iamName.MatchString(name)
}

// ValidRoleSessionName reports whether name is a role session name STS
// accepts: the STS model's roleSessionNameType, 2 to 64 of letters, digits
// and _+=,.@-.
func ValidRoleSessionName(name string) bool {
	return roleSessionName.MatchString(name)
}

// The patterns are ASCII only, so their rune counts are byte counts.
var (
	iamPath         = regexp.MustCompile(`^/(?:[\x21-\x7E]+/)?$`)
	iamName         = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)
	roleSessionName = regexp.MustCompile(`^[\w+=,.@-]{2,64}$`)
)

// maxIAMPathLen is pathType's length limit.
const maxIAMPathLen = 512
