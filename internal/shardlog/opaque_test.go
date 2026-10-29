package shardlog_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// kinesisIteratorTTL: "A shard iterator expires 5 minutes after it is
// returned to the requester" (aws-doc, kinesis.md 3.5).
const kinesisIteratorTTL = 5 * time.Minute

// opaquePrefix is the 8-byte prefix every Kinesis iterator decodes to
// (kinesis.md 3.5: aws-doc examples begin AAAAAAAAAA, kinesalite observed
// 00 00 00 00 00 00 00 01; contract 2).
var opaquePrefix = []byte{0, 0, 0, 0, 0, 0, 0, 1}

// kinesisIteratorGen draws an iterator as Kinesis's GetShardIterator issues
// one: a stream ARN arn:aws:kinesis:<region>:<account>:stream/<name> with a
// name up to the model's 128 characters (kinesis.md 3.1), a shard id
// shardId-<12 digits> (3.2), a resolved position with a sequence number up
// to 56 digits (3.4), and instants on the emulator clock.
func kinesisIteratorGen() *rapid.Generator[shardlog.Iterator] {
	return rapid.Custom(func(t *rapid.T) shardlog.Iterator {
		name := rapid.StringMatching(`[a-zA-Z0-9_.-]{1,128}`).Draw(t, "name")
		region := rapid.SampledFrom([]string{"us-east-1", "eu-west-1", "ap-southeast-2"}).Draw(t, "region")
		stream := mustARN(t, "arn:aws:kinesis:"+region+":"+rapid.StringMatching(`[0-9]{12}`).Draw(t, "account")+":stream/"+name)
		created := epoch.Add(time.Duration(rapid.Int64Range(0, 1000*3600*1000).Draw(t, "createdMillis")) * time.Millisecond)
		seq := shardlog.Seq(decimal(t, "seq", rapid.IntRange(1, 56).Draw(t, "digits")))
		pos := rapid.SampledFrom([]shardlog.Position{shardlog.TrimHorizon{}, shardlog.At{Seq: seq}, shardlog.After{Seq: seq}}).Draw(t, "pos")
		return shardlog.Iterator{
			Stream:  stream,
			Created: created,
			Shard:   fmt.Sprintf("shardId-%012d", rapid.IntRange(0, 9999).Draw(t, "shard")),
			Pos:     pos,
			Issued:  created.Add(time.Duration(rapid.Int64Range(0, int64(48*time.Hour)).Draw(t, "issued"))),
		}
	})
}

// Decode(Encode(it)) is it; the token is padded standard base64 of the
// 8-byte prefix, so it starts "AAAAAAAAAAE", has no "|1|", and is at most
// 512 characters (model ShardIterator length, kinesis.md 3.5) for names up
// to 128 characters and 56-digit numbers (contract 2 properties).
func TestOpaqueRoundTrip(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.Opaque{})
	rapid.Check(t, func(t *rapid.T) {
		it := kinesisIteratorGen().Draw(t, "it")
		b := c.Encode(it)
		raw, err := base64.StdEncoding.Strict().DecodeString(string(b))
		if err != nil {
			t.Fatalf("token %q is not padded standard base64: %v", b, err)
		}
		if !bytes.HasPrefix(raw, opaquePrefix) || !strings.HasPrefix(string(b), "AAAAAAAAAAE") || bytes.Contains(b, []byte("|1|")) {
			t.Fatalf("token %q does not have Kinesis's form", b)
		}
		if len(b) > 512 {
			t.Fatalf("token is %d characters, more than 512", len(b))
		}
		got, err := c.Decode(b, it.Issued, kinesisIteratorTTL)
		if err != nil {
			t.Fatalf("Decode at issue: %v", err)
		}
		if !sameIterator(got, it) {
			t.Fatalf("Decode(Encode(%+v)) = %+v", it, got)
		}
		if claimed, ok := shardlog.ClaimedOpaqueStream(b); !ok || claimed != it.Stream {
			t.Fatalf("ClaimedOpaqueStream = %s, %v; want %s", claimed, ok, it.Stream)
		}
	})
}

// A token decodes 1ns before its 5 minutes end and has expired at them, or
// when issued after now (contract 2: *ExpiredError when now >= Issued+ttl
// or now < Issued).
func TestOpaqueExpiresAtTTL(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.Opaque{})
	rapid.Check(t, func(t *rapid.T) {
		it := kinesisIteratorGen().Draw(t, "it")
		b := c.Encode(it)
		if _, err := c.Decode(b, it.Issued.Add(kinesisIteratorTTL-time.Nanosecond), kinesisIteratorTTL); err != nil {
			t.Fatalf("Decode 1ns before expiry: %v", err)
		}
		for _, now := range []clock.Instant{it.Issued.Add(kinesisIteratorTTL), it.Issued.Add(-time.Nanosecond)} {
			if _, err := c.Decode(b, now, kinesisIteratorTTL); !isExpired(err) {
				t.Fatalf("Decode at %s of a token issued at %s: %v, want *ExpiredError", now, it.Issued, err)
			}
		}
	})
}

func isExpired(err error) bool {
	_, ok := errors.AsType[*shardlog.ExpiredError](err)
	return ok
}

// A flipped byte anywhere in the token, another key's signature, other
// prefix bytes or text that is not base64 is *MalformedError (contract 2);
// ClaimedOpaqueStream refuses what is not base64 of an Opaque payload.
func TestOpaqueRefusesTampering(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.Opaque{})
	other := shardlog.NewCodec(bytes.Repeat([]byte{0xa5}, 32), shardlog.Opaque{})
	rapid.Check(t, func(t *rapid.T) {
		it := kinesisIteratorGen().Draw(t, "it")
		b := c.Encode(it)
		malformed := func(what string, tok []byte) {
			_, err := c.Decode(tok, it.Issued, kinesisIteratorTTL)
			if _, ok := errors.AsType[*shardlog.MalformedError](err); !ok {
				t.Fatalf("%s: Decode(%q) = %v, want *MalformedError", what, tok, err)
			}
		}
		raw, err := base64.StdEncoding.DecodeString(string(b))
		if err != nil {
			t.Fatalf("token %q: %v", b, err)
		}
		flipped := bytes.Clone(raw)
		flipped[rapid.IntRange(0, len(raw)-1).Draw(t, "at")] ^= rapid.ByteRange(1, 255).Draw(t, "mask")
		malformed("flipped byte", []byte(base64.StdEncoding.EncodeToString(flipped)))
		malformed("another key", other.Encode(it))
		malformed("not base64", []byte("AAAAAAAAAAE!!"))
		malformed("empty", nil)
		malformed("an ARNPrefixed token", shardlog.NewCodec(testKey, shardlog.ARNPrefixed{}).Encode(it))
	})
	for _, tok := range []string{"", "!!", "AAAA", base64.StdEncoding.EncodeToString([]byte("not a payload"))} {
		if s, ok := shardlog.ClaimedOpaqueStream([]byte(tok)); ok {
			t.Errorf("ClaimedOpaqueStream(%q) = %s, true", tok, s)
		}
	}
}
