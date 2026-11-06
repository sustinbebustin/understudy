package shardlog_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/shardlog"
	"github.com/sustinbebustin/understudy/internal/store"
)

// iteratorTTL is a DynamoDB Streams iterator's lifetime: "A shard iterator
// expires 15 minutes after it is returned to the requester"
// (dynamodb-streams.md 4.4, aws-doc).
const iteratorTTL = 15 * time.Minute

func mustARN(t interface{ Fatal(...any) }, s string) aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var testKey = bytes.Repeat([]byte{0x5a}, 32)

// iteratorGen draws an iterator as GetShardIterator issues one: a stream
// ARN of the documented form (dynamodb-streams.md 4.1), a resolved position
// and instants on the emulator clock.
func iteratorGen() *rapid.Generator[shardlog.Iterator] {
	return rapid.Custom(func(t *rapid.T) shardlog.Iterator {
		table := rapid.StringMatching(`[a-zA-Z0-9_.-]{3,40}`).Draw(t, "table")
		created := epoch.Add(time.Duration(rapid.Int64Range(0, 1000*3600*1000).Draw(t, "createdMillis")) * time.Millisecond)
		label := created.Wire().UTC().Format("2006-01-02T15:04:05.000")
		stream := mustARN(t, "arn:aws:dynamodb:us-east-1:111111111111:table/"+table+"/stream/"+label)
		seq := shardlog.Seq(decimal(t, "seq", 25))
		pos := rapid.SampledFrom([]shardlog.Position{shardlog.TrimHorizon{}, shardlog.At{Seq: seq}, shardlog.After{Seq: seq}}).Draw(t, "pos")
		return shardlog.Iterator{
			Stream:  stream,
			Created: created,
			Shard:   "shardId-" + rapid.StringMatching(`[0-9]{20}-[0-9a-f]{8}`).Draw(t, "shard"),
			Pos:     pos,
			Issued:  created.Add(time.Duration(rapid.Int64Range(0, int64(48*time.Hour)).Draw(t, "issued"))),
		}
	})
}

func sameIterator(a, b shardlog.Iterator) bool {
	return a.Stream == b.Stream && a.Created.Compare(b.Created) == 0 && a.Shard == b.Shard && a.Pos == b.Pos && a.Issued.Compare(b.Issued) == 0
}

// Decode(Encode(it)) is it, and the token has DynamoDB Streams' form:
// "<stream ARN>|1|<token>" (dynamodb-streams.md 4.4, aws-doc examples),
// the token standard base64 with padding (contract 0), at most 2048
// characters (dynamodb-streams.md 3.4, model).
func TestCodecRoundTrip(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.ARNPrefixed{})
	rapid.Check(t, func(t *rapid.T) {
		it := iteratorGen().Draw(t, "it")
		b := c.Encode(it)
		prefix := it.Stream.String() + "|1|"
		if !bytes.HasPrefix(b, []byte(prefix)) {
			t.Fatalf("token %q does not start with %q", b, prefix)
		}
		if _, err := base64.StdEncoding.Strict().DecodeString(string(b[len(prefix):])); err != nil {
			t.Fatalf("token body %q is not padded standard base64: %v", b[len(prefix):], err)
		}
		if len(b) > 2048 {
			t.Fatalf("token is %d bytes, more than 2048", len(b))
		}
		got, err := c.Decode(b, it.Issued, iteratorTTL)
		if err != nil {
			t.Fatalf("Decode at issue: %v", err)
		}
		if !sameIterator(got, it) {
			t.Fatalf("Decode(Encode(%+v)) = %+v", it, got)
		}
	})
}

// An iterator decodes until 1ns before its 15 minutes end and has expired
// exactly at them; one issued after now has expired too, as Local refuses
// an iterator issued in the future (dynamodb-streams.md 4.4; spec A3).
func TestCodecExpiresExactlyAtTTL(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.ARNPrefixed{})
	rapid.Check(t, func(t *rapid.T) {
		it := iteratorGen().Draw(t, "it")
		b := c.Encode(it)
		if _, err := c.Decode(b, it.Issued.Add(iteratorTTL-time.Nanosecond), iteratorTTL); err != nil {
			t.Fatalf("Decode 1ns before expiry: %v", err)
		}
		for _, now := range []clock.Instant{
			it.Issued.Add(iteratorTTL),
			it.Issued.Add(iteratorTTL + time.Duration(rapid.Int64Range(0, int64(time.Hour)).Draw(t, "late"))),
			it.Issued.Add(-time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(t, "early"))),
		} {
			_, err := c.Decode(b, now, iteratorTTL)
			var ee *shardlog.ExpiredError
			if !errors.As(err, &ee) {
				t.Fatalf("Decode at %s of an iterator issued at %s: %v, want *ExpiredError", now, it.Issued, err)
			}
			if ee.Issued.Compare(it.Issued) != 0 || ee.Now.Compare(now) != 0 {
				t.Fatalf("ExpiredError %+v, want issued %s now %s", ee, it.Issued, now)
			}
		}
	})
}

// A token with a flipped byte, another stream's prefix, or another key's
// signature is malformed (spec A3; dynamodb-streams.md 11.1 item 4: "A
// prefix that disagrees with the token is a malformed iterator").
func TestCodecRefusesTampering(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.ARNPrefixed{})
	other := shardlog.NewCodec(bytes.Repeat([]byte{0xa5}, 32), shardlog.ARNPrefixed{})
	rapid.Check(t, func(t *rapid.T) {
		it := iteratorGen().Draw(t, "it")
		b := c.Encode(it)
		malformed := func(what string, tok []byte) {
			_, err := c.Decode(tok, it.Issued, iteratorTTL)
			if _, ok := errors.AsType[*shardlog.MalformedError](err); !ok {
				t.Fatalf("%s: Decode(%q) = %v, want *MalformedError", what, tok, err)
			}
		}
		flipped := bytes.Clone(b)
		i := rapid.IntRange(0, len(b)-1).Draw(t, "at")
		flipped[i] ^= rapid.ByteRange(1, 255).Draw(t, "mask")
		malformed("flipped byte", flipped)

		prefix := it.Stream.String() + "|1|"
		otherStream := mustARN(t, strings.Replace(it.Stream.String(), "/stream/", "x/stream/", 1))
		malformed("another stream's prefix", append([]byte(otherStream.String()+"|1|"), b[len(prefix):]...))

		malformed("another key", other.Encode(it))
		malformed("empty", nil)
		malformed("prefix only", []byte(prefix))
	})
}

// The contract's error texts.
func TestCodecErrorTexts(t *testing.T) {
	t.Parallel()
	issued := epoch
	now := epoch.Add(iteratorTTL)
	if got, want := (&shardlog.ExpiredError{Issued: issued, Now: now}).Error(), "shard iterator issued at "+issued.String()+" has expired at "+now.String(); got != want {
		t.Errorf("ExpiredError = %q, want %q", got, want)
	}
	if got, want := (&shardlog.MalformedError{Reason: "bad signature"}).Error(), "malformed shard iterator: bad signature"; got != want {
		t.Errorf("MalformedError = %q, want %q", got, want)
	}
}

// WaitKey is one key per stream, shared by the writers and the poller
// (contract 2: "shardlog:" + the stream ARN).
func TestWaitKey(t *testing.T) {
	t.Parallel()
	a := mustARN(t, "arn:aws:dynamodb:us-east-1:111111111111:table/t/stream/2026-10-25T21:13:37.075")
	b := mustARN(t, "arn:aws:dynamodb:us-east-1:111111111111:table/u/stream/2026-10-25T21:13:37.075")
	if got, want := shardlog.WaitKey(a), store.WaitKey("shardlog:"+a.String()); got != want {
		t.Errorf("WaitKey = %q, want %q", got, want)
	}
	if shardlog.WaitKey(a) == shardlog.WaitKey(b) {
		t.Error("two streams share a wait key")
	}
}
