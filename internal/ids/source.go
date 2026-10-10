package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"maps"
	mathrand "math/rand/v2"
	"slices"
	"sync"
	"uuid"
)

// Source draws the bytes every generated identifier is made from. A random
// source reads crypto/rand. A seeded source gives each Kind its own ChaCha8
// stream derived from (seed, kind) and counts the 64-bit words drawn from it,
// so a snapshot can record the positions and Seek can restore them.
// A Source is safe for concurrent use.
type Source struct {
	seeded *seededStreams // nil for a random source
}

type seededStreams struct {
	seed    uint64
	streams [kindCount]stream
}

type stream struct {
	mu  sync.Mutex
	rng *mathrand.ChaCha8
	pos uint64 // words drawn
}

// NewRandom returns a source that draws from crypto/rand.
func NewRandom() *Source {
	return &Source{}
}

// NewSeeded returns a source whose streams are fully determined by seed.
func NewSeeded(seed uint64) *Source {
	s := &seededStreams{seed: seed}
	for k := range Kind(kindCount) {
		s.streams[k].rng = mathrand.NewChaCha8(streamSeed(seed, k))
	}
	return &Source{seeded: s}
}

// MaxPosition is the largest stream position Positions reports and Seek
// accepts for one kind, in 64-bit words. It is far beyond what an emulator
// session draws (about 500 million UUIDs of one kind) and bounds the replay
// Seek does to a few seconds per kind, so a corrupt manifest cannot stall
// snapshot load.
const MaxPosition = 1 << 30

// Draw returns n bytes from the kind's stream. The count is unsigned so a
// caller that computes it as an int checks its sign where it converts.
//
// Draw, Uint64 and UUID require k to be one of the declared Kind constants.
// Only a conversion such as Kind(n) can make another value (UnmarshalText
// refuses unknown names), and its behavior is undefined: a seeded source
// panics, a random source draws as for any kind.
func (s *Source) Draw(k Kind, n uint) []byte {
	b := make([]byte, n)
	s.fill(k, b)
	return b
}

// Uint64 returns one number from the kind's stream.
func (s *Source) Uint64(k Kind) uint64 {
	var b [8]byte
	s.fill(k, b[:])
	return binary.LittleEndian.Uint64(b[:])
}

// UUID returns a version 4 UUID drawn from the kind's stream.
func (s *Source) UUID(k Kind) uuid.UUID {
	var u uuid.UUID
	s.fill(k, u[:])
	u[6] = u[6]&0x0f | 0x40 // version 4
	u[8] = u[8]&0x3f | 0x80 // RFC 9562 variant
	return u
}

// Positions reports how far each stream of a seeded source has been drawn,
// for every kind. A random source has none and refuses with *NotSeededError.
// A stream drawn past MaxPosition is refused with a *PositionLimitError, so a
// snapshot is never written with positions that Seek would refuse to load.
// Drawing is not limited.
func (s *Source) Positions() (map[Kind]uint64, error) {
	if s.seeded == nil {
		return nil, &NotSeededError{}
	}
	out := make(map[Kind]uint64, kindCount)
	for k := range Kind(kindCount) {
		st := &s.seeded.streams[k]
		st.mu.Lock()
		pos := st.pos
		st.mu.Unlock()
		if pos > MaxPosition {
			return nil, &PositionLimitError{Kind: k, Pos: pos}
		}
		out[k] = pos
	}
	return out, nil
}

// Seek moves every stream of a seeded source to the position recorded for
// its kind, and kinds absent from positions to the start, so the next draws
// repeat those that followed the recording. It replays the skipped words, so
// it costs time proportional to the positions. A random source refuses with
// *NotSeededError, an unknown kind with *UnknownKindError and a position
// above MaxPosition with *PositionError; on error no stream moves.
func (s *Source) Seek(positions map[Kind]uint64) error {
	if s.seeded == nil {
		return &NotSeededError{}
	}
	for _, k := range slices.Sorted(maps.Keys(positions)) {
		if !k.valid() {
			return &UnknownKindError{Name: k.String()}
		}
		if pos := positions[k]; pos > MaxPosition {
			return &PositionError{Kind: k, Pos: pos}
		}
	}
	for k := range Kind(kindCount) {
		st := &s.seeded.streams[k]
		st.mu.Lock()
		st.rng = mathrand.NewChaCha8(streamSeed(s.seeded.seed, k))
		st.pos = 0
		for range positions[k] {
			st.rng.Uint64()
		}
		st.pos = positions[k]
		st.mu.Unlock()
	}
	return nil
}

func (s *Source) fill(k Kind, b []byte) {
	if s.seeded == nil {
		_, _ = rand.Read(b) // crypto/rand.Read never returns an error; it crashes the program instead
		return
	}
	st := &s.seeded.streams[k]
	st.mu.Lock()
	defer st.mu.Unlock()
	for len(b) > 0 {
		var w [8]byte
		binary.LittleEndian.PutUint64(w[:], st.rng.Uint64())
		st.pos++
		b = b[copy(b, w[:]):]
	}
}

// streamSeed derives the ChaCha8 key for one kind from the seed and the
// kind's name, so reordering the Kind constants never changes a stream.
func streamSeed(seed uint64, k Kind) [32]byte {
	h := sha256.New()
	h.Write([]byte("understudy/ids/v1\x00"))
	h.Write([]byte(k.String()))
	h.Write([]byte{0})
	h.Write(binary.LittleEndian.AppendUint64(nil, seed))
	return [32]byte(h.Sum(nil))
}
