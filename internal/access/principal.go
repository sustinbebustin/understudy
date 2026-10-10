package access

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Principal is whom the recorder attributes a call to: an IAM Identity,
// which an identity policy attaches to, or the ServicePrincipal of a
// delivery.
//
// A role session, from STS AssumeRole or a Lambda execution environment, is
// recorded as its Role and never as its sts assumed-role ARN: a session is
// not something a policy attaches to, each execution environment has its own
// session name, and the session ARN drops the role's path. So a function's
// own calls and the calls AWS makes as its execution role land on one Role.
//
//sumtype:decl
type Principal interface {
	isPrincipal()
	// String is the IAM ARN of an Identity or the name of a service
	// principal, the text ParsePrincipal reads back.
	String() string
}

// Identity is a Principal an identity policy attaches to: Root, User or
// Role.
//
//sumtype:decl
type Identity interface {
	Principal
	isIdentity()
}

// Root is an account's root user, arn:aws:iam::<account>:root.
type Root struct{ Account aws.AccountID }

// User is an IAM user, arn:aws:iam::<account>:user<path><name>.
type User struct {
	Account aws.AccountID
	// Path is "/" or begins and ends with "/"; empty means "/".
	Path string
	Name string
}

// Role is an IAM role, arn:aws:iam::<account>:role<path><name>, and every
// session of it.
type Role struct {
	Account aws.AccountID
	// Path is "/" or begins and ends with "/"; empty means "/".
	Path string
	Name string
}

// ServicePrincipal is an AWS service making a delivery, such as
// "sns.amazonaws.com".
type ServicePrincipal struct{ Name string }

// String returns the root user's IAM ARN, the text ParsePrincipal reads
// back.
func (p Root) String() string { return iamARN(p.Account, "root") }

// String returns the user's IAM ARN, the text ParsePrincipal reads back.
func (p User) String() string { return iamARN(p.Account, "user"+pathOrRoot(p.Path)+p.Name) }

// String returns the role's IAM ARN, the text ParsePrincipal reads back.
func (p Role) String() string { return iamARN(p.Account, "role"+pathOrRoot(p.Path)+p.Name) }

// String returns the service principal's name, the text ParsePrincipal
// reads back.
func (p ServicePrincipal) String() string { return p.Name }

func (Root) isPrincipal()             {}
func (User) isPrincipal()             {}
func (Role) isPrincipal()             {}
func (ServicePrincipal) isPrincipal() {}

func (Root) isIdentity() {}
func (User) isIdentity() {}
func (Role) isIdentity() {}

// canonical spells an empty path as "/", so one identity is one recorder
// key however the caller spelled its path.
func canonical(p Principal) Principal {
	switch p := p.(type) {
	case User:
		p.Path = pathOrRoot(p.Path)
		return p
	case Role:
		p.Path = pathOrRoot(p.Path)
		return p
	case Root, ServicePrincipal:
		return p
	}
	return p
}

// servicePrincipalSuffix ends every service principal name of the aws
// partition, the only partition the recorder renders.
const servicePrincipalSuffix = ".amazonaws.com"

// ParsePrincipal reads the text a Principal's String writes: the IAM ARN of
// a root user, user or role, or a service principal name. An sts
// assumed-role ARN is refused with PrincipalRoleSession, because the
// recorder attributes a session's calls to its role.
func ParsePrincipal(s string) (Principal, error) {
	if !strings.HasPrefix(s, "arn:") {
		name, ok := strings.CutSuffix(s, servicePrincipalSuffix)
		if !ok || name == "" || strings.ContainsAny(s, " \t\r\n:/") {
			return nil, &PrincipalError{Input: s, Problem: PrincipalUnrecognized}
		}
		return ServicePrincipal{Name: s}, nil
	}
	a, err := aws.ParseARN(s)
	if err != nil {
		return nil, &PrincipalError{Input: s, Problem: PrincipalUnrecognized}
	}
	if a.Service == "sts" && strings.HasPrefix(a.Resource, "assumed-role/") {
		return nil, &PrincipalError{Input: s, Problem: PrincipalRoleSession}
	}
	if a.Partition != aws.PartitionAWS().String() || a.Service != "iam" || a.Region != "" {
		return nil, &PrincipalError{Input: s, Problem: PrincipalUnrecognized}
	}
	account, err := aws.ParseAccountID(a.Account)
	if err != nil {
		return nil, &PrincipalError{Input: s, Problem: PrincipalUnrecognized}
	}
	if a.Resource == "root" {
		return Root{Account: account}, nil
	}
	if rest, ok := strings.CutPrefix(a.Resource, "user/"); ok {
		if path, name, ok := splitPath(rest); ok {
			return User{Account: account, Path: path, Name: name}, nil
		}
	}
	if rest, ok := strings.CutPrefix(a.Resource, "role/"); ok {
		if path, name, ok := splitPath(rest); ok {
			return Role{Account: account, Path: path, Name: name}, nil
		}
	}
	return nil, &PrincipalError{Input: s, Problem: PrincipalUnrecognized}
}

// splitPath splits what follows "user/" or "role/" into the path and the
// name, "service-role/worker" into "/service-role/" and "worker", and
// reports whether both are ones IAM's CreateUser and CreateRole accept.
func splitPath(rest string) (path, name string, ok bool) {
	i := strings.LastIndex(rest, "/")
	path, name = "/"+rest[:i+1], rest[i+1:]
	return path, name, aws.ValidIAMPath(path) && aws.ValidIAMName(name)
}

// PrincipalProblem is why ParsePrincipal refused its input.
type PrincipalProblem uint8

// PrincipalProblem values.
const (
	// PrincipalUnrecognized is text that is neither the IAM ARN of a root
	// user, user or role in the aws partition nor a service principal name.
	PrincipalUnrecognized PrincipalProblem = iota + 1
	// PrincipalRoleSession is an sts assumed-role ARN.
	PrincipalRoleSession
)

// PrincipalError is text ParsePrincipal refused.
type PrincipalError struct {
	Input   string
	Problem PrincipalProblem
}

// Error describes the refused text and the principal form to give instead.
func (e *PrincipalError) Error() string {
	switch e.Problem {
	case PrincipalUnrecognized:
		return fmt.Sprintf("access: %q is not a principal the access recorder attributes calls to; "+
			"give the IAM ARN of a root user, user or role (arn:aws:iam::111111111111:role/worker) or a service principal name (sns.amazonaws.com)", e.Input)
	case PrincipalRoleSession:
		return fmt.Sprintf("access: %q is a role session; the access recorder attributes a session's calls to its role, "+
			"so give the role's IAM ARN, arn:aws:iam::<account>:role/<path><role-name>, with the role's path, which the session ARN omits", e.Input)
	default:
		return fmt.Sprintf("access: principal %q refused with problem %d", e.Input, uint8(e.Problem))
	}
}

func iamARN(account aws.AccountID, resource string) string {
	return aws.ARN{
		Partition: aws.PartitionAWS().String(),
		Service:   "iam",
		Account:   account.String(),
		Resource:  resource,
	}.String()
}

func pathOrRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
