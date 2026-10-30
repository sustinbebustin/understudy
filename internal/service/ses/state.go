package ses

import (
	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// stateVersion is the version of the "ses" snapshot section.
const stateVersion = 1

// state is the SES state of one account and Region, the "ses" snapshot
// section's data (contract 1.3).
type state struct {
	// Identities are in creation order, the order ListIdentities and
	// Resources list them in (SES-14).
	Identities []identity `json:"identities"`
	// Messages are the recorded messages, oldest first, at most
	// MaxStoredMessages (ses.md 11.2).
	Messages []record `json:"messages,omitempty"`
	// Evicted counts the records evicted to keep MaxStoredMessages.
	Evicted int `json:"evicted,omitempty"`
}

func newState(aws.Scope) state { return state{} }

// IsZero reports a scope without identities, messages or evictions, which
// snapshots skip.
func (s *state) IsZero() bool {
	return len(s.Identities) == 0 && len(s.Messages) == 0 && s.Evicted == 0
}

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*state)(nil)

// find is the index of the identity named name, byte for byte (ses.md
// 9.1), or -1.
func (s *state) find(name string) int {
	for i := range s.Identities {
		if s.Identities[i].Name == name {
			return i
		}
	}
	return -1
}

// identity is one verified or verifying email address or domain (ses.md
// 5.1).
type identity struct {
	Name   string              `json:"name"`
	Kind   sesapi.IdentityType `json:"kind"`
	Status status              `json:"status"`
	// LinkToken is the token of an email identity's verification link,
	// the same in every verification email it is sent (inferred, SES-09).
	LinkToken string        `json:"linkToken,omitempty"`
	Created   clock.Instant `json:"created"`
}

// identityARN is the ARN of identity name in scope,
// arn:aws:ses:<region>:<account>:identity/<name> (ses.md 5.1).
func identityARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "ses",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "identity/" + name,
	}
}

// status is an identity's verification status (ses.md 5.2). NotStarted is
// not one: it only answers for an identity the store does not hold.
type status interface {
	// v1 is the status as v1 VerificationStatus spells it.
	v1() sesapi.VerificationStatus
}

// statusPending is an identity whose verification started at Since and has
// not settled.
type statusPending struct {
	Since clock.Instant `json:"since"`
}

// statusSuccess is a verified identity.
type statusSuccess struct{}

// statusFailed is an identity whose verification expired.
type statusFailed struct{}

// statusTemporaryFailure is an identity whose verification failed for a
// reason that may pass.
type statusTemporaryFailure struct{}

func (statusPending) v1() sesapi.VerificationStatus { return sesapi.VerificationStatusPending }
func (statusSuccess) v1() sesapi.VerificationStatus { return sesapi.VerificationStatusSuccess }
func (statusFailed) v1() sesapi.VerificationStatus  { return sesapi.VerificationStatusFailed }
func (statusTemporaryFailure) v1() sesapi.VerificationStatus {
	return sesapi.VerificationStatusTemporaryFailure
}

// codec is the state codec: jsonx with the status union. The table is
// static, so an error building it is a bug in it; the codec then fails
// every section with it.
func codec() store.Codec {
	statuses, err := jsonx.NewUnion("ses.status",
		jsonx.Variant[status, statusPending]("Pending"),
		jsonx.Variant[status, statusSuccess]("Success"),
		jsonx.Variant[status, statusFailed]("Failed"),
		jsonx.Variant[status, statusTemporaryFailure]("TemporaryFailure"),
	)
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	c := jsonx.NewCodec(statuses)
	return store.Codec{Marshal: c.Marshal, Unmarshal: c.Unmarshal}
}
