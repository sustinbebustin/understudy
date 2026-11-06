package shardlog

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"math"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// opaquePrefix is the 8 bytes every Opaque token starts with, so its text
// starts "AAAAAAAAAAE" as AWS's iterators do (kinesis.md 3.5).
const opaquePrefix = "\x00\x00\x00\x00\x00\x00\x00\x01"

// The Opaque payload, after the prefix and before the HMAC, is binary so a
// token stays within the model's 512 characters for a 128-character stream
// name and a 56-digit sequence number (kinesis.md 3.1, 3.4, 3.5):
//
//	u16 ARN length, ARN
//	i64 created seconds, u32 created nanoseconds
//	u8 shard id length, shard id
//	u8 position kind, u8 sequence number length, sequence number
//	i64 issued seconds, u32 issued nanoseconds
//
// all integers big-endian.

// encodeOpaque returns it as an Opaque token.
func (c Codec) encodeOpaque(it Iterator) []byte {
	kind, seq := positionKind(it.Pos)
	stream := it.Stream.String()
	raw := make([]byte, 0, len(opaquePrefix)+2+len(stream)+12+1+len(it.Shard)+2+len(seq)+12+sha256.Size)
	raw = append(raw, opaquePrefix...)
	raw = binary.BigEndian.AppendUint16(raw, uint16(min(len(stream), math.MaxUint16)))
	raw = append(raw, stream...)
	raw = appendStamp(raw, it.Created)
	raw = append(raw, byte(min(len(it.Shard), math.MaxUint8)))
	raw = append(raw, it.Shard...)
	raw = append(raw, kind[0], byte(min(len(seq), math.MaxUint8)))
	raw = append(raw, seq...)
	raw = appendStamp(raw, it.Issued)
	raw = append(raw, c.sign(nil, raw)...)
	return []byte(base64.StdEncoding.EncodeToString(raw))
}

func appendStamp(b []byte, i clock.Instant) []byte {
	s := stampOf(i)
	b = binary.BigEndian.AppendUint64(b, uint64(s[0]))    //nolint:gosec // two's complement, which stamp reads back
	return binary.BigEndian.AppendUint32(b, uint32(s[1])) //nolint:gosec // nanoseconds, 0 to 999999999
}

// decodeOpaque reads and verifies an Opaque token.
func (c Codec) decodeOpaque(b []byte) (Iterator, *MalformedError) {
	raw, err := base64.StdEncoding.Strict().DecodeString(string(b))
	if err != nil || len(raw) < len(opaquePrefix)+sha256.Size {
		return Iterator{}, &MalformedError{Reason: "the token is not base64 of a signed payload"}
	}
	signed, mac := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if !bytes.HasPrefix(signed, []byte(opaquePrefix)) || !hmac.Equal(mac, c.sign(nil, signed)) {
		return Iterator{}, &MalformedError{Reason: "bad signature"}
	}
	it, ok := parseOpaque(signed[len(opaquePrefix):])
	if !ok {
		return Iterator{}, &MalformedError{Reason: "the payload does not parse"}
	}
	return it, nil
}

// ClaimedOpaqueStream returns the stream an Opaque token's payload names,
// unverified, as IAM authorization needs it before the HMAC is checked;
// false when the token is not base64 of an Opaque payload.
func ClaimedOpaqueStream(token []byte) (aws.ARN, bool) {
	raw, err := base64.StdEncoding.Strict().DecodeString(string(token))
	if err != nil || len(raw) < len(opaquePrefix)+sha256.Size || !bytes.HasPrefix(raw, []byte(opaquePrefix)) {
		return aws.ARN{}, false
	}
	it, ok := parseOpaque(raw[len(opaquePrefix) : len(raw)-sha256.Size])
	return it.Stream, ok
}

// parseOpaque reads a payload; false unless it is exactly one well-formed
// payload.
func parseOpaque(p []byte) (Iterator, bool) {
	r := payloadReader{b: p}
	stream, err := aws.ParseARN(string(r.bytes(int(r.u16()))))
	created := r.stamp()
	shard := string(r.bytes(int(r.u8())))
	kind := string(r.bytes(1))
	seq := Seq(r.bytes(int(r.u8())))
	issued := r.stamp()
	if r.bad || len(r.b) != 0 || err != nil {
		return Iterator{}, false
	}
	pos, ok := positionOf(kind, seq)
	if !ok {
		return Iterator{}, false
	}
	return Iterator{Stream: stream, Created: created, Shard: shard, Pos: pos, Issued: issued}, true
}

// payloadReader consumes a payload front to back; a read past the end sets
// bad and returns zero values.
type payloadReader struct {
	b   []byte
	bad bool
}

func (r *payloadReader) bytes(n int) []byte {
	if r.bad || n > len(r.b) {
		r.bad = true
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *payloadReader) u8() uint8 {
	b := r.bytes(1)
	if b == nil {
		return 0
	}
	return b[0]
}

func (r *payloadReader) u16() uint16 {
	b := r.bytes(2)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func (r *payloadReader) stamp() clock.Instant {
	b := r.bytes(12)
	if b == nil {
		return clock.Instant{}
	}
	nanos := binary.BigEndian.Uint32(b[8:])
	if nanos >= uint32(time.Second) {
		r.bad = true
		return clock.Instant{}
	}
	return stamp{int64(binary.BigEndian.Uint64(b)), int64(nanos)}.instant() //nolint:gosec // appendStamp's two's complement
}
