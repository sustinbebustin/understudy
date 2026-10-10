package identity

import (
	"github.com/sustinbebustin/understudy/internal/aws"
)

// Principal is who makes a call.
//
//sumtype:decl
type Principal interface{ isPrincipal() }

// Root is an account's root user: a 12-digit access key, a key understudy
// did not mint, or root credentials minted by the store.
type Root struct{ Account aws.AccountID }

// User is an IAM user.
type User struct {
	Account aws.AccountID
	// Path is "/" or begins and ends with "/"; empty means "/".
	Path string
	Name string
	// ID is the user's unique AIDA... id.
	ID string
}

// AssumedRole is a role session from STS AssumeRole or a Lambda execution
// environment.
type AssumedRole struct {
	// Account owns the role.
	Account aws.AccountID
	Role    RoleRef
	// Session is the role session name.
	Session string
	Source  SessionSource
}

// ServicePrincipal is an AWS service acting on its own behalf, such as
// "sns.amazonaws.com" on an internal delivery.
type ServicePrincipal struct{ Name string }

// Anonymous is an unsigned request, or a function URL with AuthType NONE.
type Anonymous struct{}

func (Root) isPrincipal()             {}
func (User) isPrincipal()             {}
func (AssumedRole) isPrincipal()      {}
func (ServicePrincipal) isPrincipal() {}
func (Anonymous) isPrincipal()        {}

// RoleRef names the role behind an assumed-role session.
type RoleRef struct {
	Name string
	// Path is "/" or begins and ends with "/"; empty means "/". The
	// assumed-role ARN never shows it.
	Path string
	// ID is the role's unique AROA... id.
	ID string
}

// SessionSource says how a role session began.
//
//sumtype:decl
type SessionSource interface{ isSessionSource() }

// FromAssumeRole is a session STS AssumeRole created.
type FromAssumeRole struct{}

// FromExecutionEnvironment is the execution-role session of one Lambda
// execution environment.
type FromExecutionEnvironment struct {
	Function aws.ARN
	Env      EnvID
}

func (FromAssumeRole) isSessionSource()           {}
func (FromExecutionEnvironment) isSessionSource() {}

// EnvID names one Lambda execution environment.
type EnvID string

// Render returns the principal as GetCallerIdentity's Arn shows it
// (iam-sts-kms.md 4.1): arn:aws:iam::<acct>:root, arn:aws:iam::<acct>:user/<path><name>
// and arn:aws:sts::<acct>:assumed-role/<role-name>/<session>, with no role
// path. A service principal renders as its service name and an anonymous
// caller as "anonymous"; neither has an ARN.
func Render(p Principal) string {
	switch p := p.(type) {
	case Root:
		return iamARN(p.Account, "root")
	case User:
		return iamARN(p.Account, "user"+pathOrRoot(p.Path)+p.Name)
	case AssumedRole:
		return aws.ARN{
			Partition: aws.PartitionAWS().String(),
			Service:   "sts",
			Account:   p.Account.String(),
			Resource:  "assumed-role/" + p.Role.Name + "/" + p.Session,
		}.String()
	case ServicePrincipal:
		return p.Name
	case Anonymous:
		return "anonymous"
	}
	return ""
}

// AccountOf returns the account a principal acts in, and false for a service
// principal or an anonymous caller, which belong to no account.
func AccountOf(p Principal) (aws.AccountID, bool) {
	switch p := p.(type) {
	case Root:
		return p.Account, true
	case User:
		return p.Account, true
	case AssumedRole:
		return p.Account, true
	case ServicePrincipal, Anonymous:
		return aws.AccountID{}, false
	}
	return aws.AccountID{}, false
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
