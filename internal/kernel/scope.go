package kernel

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// ScopeRef is how a resource reference in an input picks the scope it
// resolves in (ARCHITECTURE.md 8 rule 7).
//
//sumtype:decl
type ScopeRef interface{ isScopeRef() }

// SameScope is an ARN that must name the caller's own account and region;
// any other answers Mismatch, the service's own text (SNS "Invalid
// parameter: TopicArn", Lambda's "not reachable in this region").
type SameScope struct {
	ARN      aws.ARN
	Mismatch *awserr.Error
}

// CrossAccount is a reference whose account wins over the caller's, in the
// caller's region: an SQS queue URL's path, GetQueueUrl's
// QueueOwnerAWSAccountId.
type CrossAccount struct {
	Account aws.AccountID
}

// Global is a reference to a partition-wide namespace (S3 bucket names);
// it resolves to the caller's partition with no account or region.
type Global struct{}

func (SameScope) isScopeRef()    {}
func (CrossAccount) isScopeRef() {}
func (Global) isScopeRef()       {}

// ResolveScope returns the scope a reference names from c's point of view,
// or the error the service answers for a reference outside it.
func ResolveScope(c *Call, r ScopeRef) (aws.Scope, *awserr.Error) {
	scope := c.Scope()
	switch r := r.(type) {
	case SameScope:
		if r.ARN.Account != scope.Account.String() || r.ARN.Region != scope.Region.String() || r.ARN.Partition != scope.Partition.String() {
			if r.Mismatch == nil {
				return aws.Scope{}, awserr.Internalf(&TargetError{Op: c.opID, Reason: "SameScope has no Mismatch error for an ARN outside the caller's scope; give it the service's own error"})
			}
			return aws.Scope{}, r.Mismatch
		}
		return scope, nil
	case CrossAccount:
		if !r.Account.IsZero() {
			scope.Account = r.Account
		}
		return scope, nil
	case Global:
		return aws.Scope{Partition: scope.Partition}, nil
	}
	return aws.Scope{}, awserr.Internalf(&TargetError{Op: c.opID, Reason: "ResolveScope got no scope reference"})
}
