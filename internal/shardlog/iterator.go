package shardlog

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

// arnSeparator ends the stream ARN of an ARNPrefixed token. A table name
// cannot hold '|' and base64 never does, so the last one is the separator.
const arnSeparator = "|1|"

// Codec signs and checks iterator tokens with an HMAC key that comes from
// the seeded id streams and lives in the snapshot.
type Codec struct {
	key  []byte
	form Form
}

// NewCodec returns a codec signing with key in form.
func NewCodec(key []byte, form Form) Codec { return Codec{key: key, form: form} }

// payload is the signed part of a token. The stream is not in it: the HMAC
// covers the ARN prefix, so a token moved under another stream's prefix
// fails to verify.
type payload struct {
	Created stamp  `json:"c"`
	Shard   string `json:"h"`
	Kind    string `json:"k"`
	Seq     Seq    `json:"q,omitempty"`
	Issued  stamp  `json:"i"`
}

// stamp is an instant as Unix seconds and nanoseconds, exact for every
// instant including the zero one.
type stamp [2]int64

func stampOf(i clock.Instant) stamp {
	t := i.Wire()
	return stamp{t.Unix(), int64(t.Nanosecond())}
}

func (s stamp) instant() clock.Instant { return clock.FromWire(time.Unix(s[0], s[1]).UTC()) }

// Position kinds in a payload.
const (
	kindTrimHorizon = "t"
	kindAt          = "a"
	kindAfter       = "f"
)

// Encode returns it as a token. Pos must be resolved (TrimHorizon, At or
// After); Latest and AtTime encode as TrimHorizon, which no caller issues.
func (c Codec) Encode(it Iterator) []byte {
	p := payload{Created: stampOf(it.Created), Shard: it.Shard, Kind: kindTrimHorizon, Issued: stampOf(it.Issued)}
	switch pos := it.Pos.(type) {
	case At:
		p.Kind, p.Seq = kindAt, pos.Seq
	case After:
		p.Kind, p.Seq = kindAfter, pos.Seq
	case TrimHorizon, Latest, AtTime:
	}
	body, err := json.Marshal(p)
	if err != nil {
		// A struct of strings and integers always marshals.
		return nil
	}
	prefix := []byte(it.Stream.String() + arnSeparator)
	body = append(body, c.sign(prefix, body)...)
	out := make([]byte, len(prefix)+base64.StdEncoding.EncodedLen(len(body)))
	copy(out, prefix)
	base64.StdEncoding.Encode(out[len(prefix):], body)
	return out
}

// ClaimedStream returns the stream an ARNPrefixed token names in its
// prefix, unverified: what a call reading through the token addresses,
// known before the token's HMAC is checked, as IAM authorization needs it.
// False when the token has no prefix that parses as an ARN.
func ClaimedStream(token []byte) (aws.ARN, bool) {
	stream, _, err := splitPrefix(token)
	return stream, err == nil
}

// splitPrefix splits an ARNPrefixed token into the stream its prefix names
// and the rest, the prefix's separator included in neither.
func splitPrefix(b []byte) (aws.ARN, []byte, *MalformedError) {
	before, after, ok := bytes.CutLast(b, []byte(arnSeparator))
	if !ok {
		return aws.ARN{}, nil, &MalformedError{Reason: "no stream ARN prefix"}
	}
	stream, err := aws.ParseARN(string(before))
	if err != nil {
		return aws.ARN{}, nil, &MalformedError{Reason: "the prefix is not a stream ARN"}
	}
	return stream, after, nil
}

// Decode checks a token's HMAC, its prefix against the stream it names and
// its age: *MalformedError for one that does not decode or verify,
// *ExpiredError when now is at or past Issued plus ttl or before Issued.
func (c Codec) Decode(b []byte, now clock.Instant, ttl time.Duration) (Iterator, error) {
	stream, enc, merr := splitPrefix(b)
	if merr != nil {
		return Iterator{}, merr
	}
	prefix := b[:len(b)-len(enc)]
	signed := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
	n, err := base64.StdEncoding.Strict().Decode(signed, enc)
	if err != nil || n <= sha256.Size {
		return Iterator{}, &MalformedError{Reason: "the token is not base64 of a signed payload"}
	}
	body, mac := signed[:n-sha256.Size], signed[n-sha256.Size:n]
	if !hmac.Equal(mac, c.sign(prefix, body)) {
		return Iterator{}, &MalformedError{Reason: "bad signature"}
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Iterator{}, &MalformedError{Reason: "the payload does not parse"}
	}
	it := Iterator{Stream: stream, Created: p.Created.instant(), Shard: p.Shard, Issued: p.Issued.instant()}
	switch p.Kind {
	case kindTrimHorizon:
		it.Pos = TrimHorizon{}
	case kindAt:
		it.Pos = At{Seq: p.Seq}
	case kindAfter:
		it.Pos = After{Seq: p.Seq}
	default:
		return Iterator{}, &MalformedError{Reason: "unknown position kind " + p.Kind}
	}
	if now.Before(it.Issued) || now.Compare(it.Issued.Add(ttl)) >= 0 {
		return Iterator{}, &ExpiredError{Issued: it.Issued, Now: now}
	}
	return it, nil
}

func (c Codec) sign(prefix, body []byte) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write(prefix)
	m.Write(body)
	return m.Sum(nil)
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
