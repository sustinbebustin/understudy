package authz

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Via is how a call reaches its service.
//
//sumtype:decl
type Via interface{ isVia() }

// Direct is an external call.
type Direct struct{}

// ForwardAccess is an internal call made as the caller (kernel.AsCaller).
type ForwardAccess struct{}

// ExecutionRole is an internal call made as a function's execution role.
type ExecutionRole struct{}

// Delivery is a service principal acting for Source. SourceAccount is zero
// when the delivery sends no aws:SourceAccount
// (iam-enforcement-surfaces.md 8.1).
type Delivery struct {
	Source        aws.ARN
	SourceAccount aws.AccountID
}

func (Direct) isVia()        {}
func (ForwardAccess) isVia() {}
func (ExecutionRole) isVia() {}
func (Delivery) isVia()      {}

// ContextInput is what BuildContext reads.
type ContextInput struct {
	Principal     identity.Principal
	PrincipalTags []Tag
	Action        registry.IAMAction
	Resource      Resource
	Now           clock.Instant
	Region        string
	// UserAgent and Referer are the request's headers, "" when absent.
	UserAgent string
	Referer   string
	Via       Via
	// Keys are request keys the binding or the handler supplies
	// (kernel.ConditionKeys); a key with no values is one the request does
	// not carry.
	Keys map[string][]string
}

// BuildContext returns the request context of iam-evaluation.md 8.1 for one
// pair: the principal keys, the request keys understudy computes, the
// delivery's source keys and in.Keys, with every key AWS would send that
// understudy cannot compute marked Unsupplied.
//
// Not implemented yet: it returns an empty context for in.Action's service.
func BuildContext(in ContextInput) *policydoc.Context {
	return policydoc.NewContext(in.Action.Prefix)
}

// Multivalued reports whether AWS defines key as multivalued: aws:TagKeys,
// aws:CalledVia, aws:PrincipalServiceNamesList, aws:PrincipalOrgPaths,
// aws:ResourceOrgPaths, aws:SourceOrgPaths, aws:VpceOrgPaths,
// sts:TransitiveTagKeys and kms:EncryptionContextKeys (iam-evaluation.md
// 6.12).
//
// Not implemented yet: it reports false.
func Multivalued(_ string) bool {
	return false
}
