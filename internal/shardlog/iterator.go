package shardlog

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Iterator is what a shard iterator token carries.
type Iterator struct {
	Stream aws.ARN
	// Created is the stream's creation instant, so a re-created stream
	// refuses its predecessor's iterators.
	Created clock.Instant
	Shard   string
	// Pos is resolved at issue: TrimHorizon, At or After.
	Pos    Position
	Issued clock.Instant
}

// Form is the text a token takes.
//
//sumtype:decl
type Form interface{ isForm() }

// ARNPrefixed is DynamoDB Streams' form, "<stream ARN>|1|<token>"
// (dynamodb-streams.md 4.4).
type ARNPrefixed struct{}

func (ARNPrefixed) isForm() {}

// Codec signs and checks iterator tokens with an HMAC key that comes from
// the seeded id streams and lives in the snapshot.
type Codec struct {
	key  []byte
	form Form
}

// NewCodec returns a codec signing with key in form.
func NewCodec(key []byte, form Form) Codec { return Codec{key: key, form: form} }

// Encode returns it as a token. Not implemented yet: it answers nil.
func (c Codec) Encode(_ Iterator) []byte { return nil }

// Decode checks a token's HMAC, its prefix against the stream it names and
// its age: *MalformedError for one that does not decode or verify,
// *ExpiredError when now is at or past Issued plus ttl or before Issued.
// Not implemented yet.
func (c Codec) Decode(_ []byte, _ clock.Instant, _ time.Duration) (Iterator, error) {
	return Iterator{}, errNotImplemented
}

// MalformedError is a token that does not decode or verify.
type MalformedError struct{ Reason string }

func (e *MalformedError) Error() string { return "malformed shard iterator: " + e.Reason }

// ExpiredError is a token past its lifetime, or issued after now.
type ExpiredError struct{ Issued, Now clock.Instant }

func (e *ExpiredError) Error() string {
	return "shard iterator issued at " + e.Issued.String() + " has expired at " + e.Now.String()
}

// WaitKey is what an append to stream wakes, for the services that write
// the log and the poller that reads it.
func WaitKey(stream aws.ARN) store.WaitKey { return store.WaitKey("shardlog:" + stream.String()) }
