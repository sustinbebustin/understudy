package events

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the event bus target names with its stored policy
// parsed, for the delivery access advisory and IAM enforcement
// (outbox.PolicySource, ARCHITECTURE.md 16). Bus policies are not
// implemented yet, so it reports every bus as absent, which skips the
// advisory.
func (*Service) ResourcePolicy(aws.ARN) (doc *policydoc.Document, ok bool) {
	return nil, false
}
