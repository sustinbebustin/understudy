package dynamodb

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// preForward answers what Local would get wrong before it sees the call
// (error-matrix.md finding 6). The edge already answers an unknown or
// missing target, a Content-Type DynamoDB does not read, a body over
// 16 MiB, a body that does not decode, a credential region outside the
// region table and the generated validation. What reaches the handler
// unchecked is a client request without credentials, which real DynamoDB
// answers with MissingAuthenticationTokenException (error-matrix.md 5.5
// T8) and Local with its own MissingAuthenticationToken.
func preForward(c *kernel.Call) *awserr.Error {
	if _, external := c.Origin().(ref.External); external {
		if _, anonymous := c.Principal().(identity.Anonymous); anonymous {
			v, _ := auth.VocabularyFor(awserr.JSONFamily)
			return auth.Missing(v)
		}
	}
	if c.Scope().Region.IsZero() || c.Scope().Account.IsZero() {
		return awserr.Internalf(&ScopeError{Op: c.OperationID()})
	}
	return nil
}

// ScopeError is a call without the account or region Local partitions by.
// The edge and kernel.Invoke always set both, so it is a bug.
type ScopeError struct{ Op ref.OperationID }

func (e *ScopeError) Error() string {
	return fmt.Sprintf("the %s call has no account or region to forward to DynamoDB Local; every call needs both, because Local partitions tables by them", e.Op)
}
