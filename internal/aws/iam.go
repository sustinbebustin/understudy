package aws

import (
	"regexp"
	"strings"
)

// RootARN is the ARN of account's root user in partition p, the principal
// a resource policy names when it grants an account:
// arn:<partition>:iam::<account>:root. account is kept as given, as a
// policy stores it.
func RootARN(p Partition, account string) ARN {
	return ARN{Partition: p.String(), Service: "iam", Account: account, Resource: "root"}
}

// SplitIAMPath splits the part of an IAM ARN's resource after its type,
// such as "service-role/name", into the IAM path ("/service-role/") and
// the name. A resource without a slash is on the root path "/". It does
// not validate either part: check them with ValidIAMPath and ValidIAMName.
func SplitIAMPath(rest string) (path, name string) {
	i := strings.LastIndexByte(rest, '/')
	if i < 0 {
		return "/", rest
	}
	return "/" + rest[:i+1], rest[i+1:]
}

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
