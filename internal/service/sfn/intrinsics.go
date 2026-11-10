package sfn

import (
	mathrand "math/rand/v2"

	"github.com/sustinbebustin/understudy/internal/ids"
)

// intrinsicSource is the intrinsic.Source of executions: States.UUID and
// unseeded States.MathRandom draw from their own ids kinds, so a seeded
// emulator repeats them.
type intrinsicSource struct{ ids *ids.Source }

func (s intrinsicSource) UUID() string { return s.ids.UUID(ids.KindSFNUUID).String() }

// Random is uniform in [start, end); the caller guarantees start < end.
func (s intrinsicSource) Random(start, end int64) int64 {
	rng := mathrand.New(mathRandomStream(s)) //nolint:gosec // the draws come from the ids source, seeded or crypto/rand
	return start + rng.Int64N(end-start)
}

// mathRandomStream is the KindSFNMathRandom stream as a math/rand source.
type mathRandomStream struct{ ids *ids.Source }

func (m mathRandomStream) Uint64() uint64 { return m.ids.Uint64(ids.KindSFNMathRandom) }
