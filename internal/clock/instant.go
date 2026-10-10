package clock

import (
	"fmt"
	"time"
)

// Instant is a point in emulator time. It is not a time.Time, so an emulator
// deadline cannot be compared with a wall timestamp by accident: Wire is the
// one conversion to time.Time and FromWire the one conversion back, both meant
// for codecs, configuration and snapshots. The zero Instant is the zero time.
type Instant struct{ t time.Time }

// FromWire returns the Instant for t, in UTC with any monotonic reading
// dropped.
func FromWire(t time.Time) Instant {
	return Instant{t: t.Round(0).UTC()}
}

// Add returns i+d.
func (i Instant) Add(d time.Duration) Instant { return Instant{t: i.t.Add(d)} }

// Sub returns i-j.
func (i Instant) Sub(j Instant) time.Duration { return i.t.Sub(j.t) }

// Before reports whether i is earlier than j.
func (i Instant) Before(j Instant) bool { return i.t.Before(j.t) }

// Compare returns -1, 0 or +1 as i is before, equal to or after j.
func (i Instant) Compare(j Instant) int { return i.t.Compare(j.t) }

// IsZero reports whether i is the zero Instant.
func (i Instant) IsZero() bool { return i.t.IsZero() }

// Wire returns i as a UTC time.Time for serialization.
func (i Instant) Wire() time.Time { return i.t }

// String formats i as RFC 3339 with nanoseconds.
func (i Instant) String() string { return i.t.Format(time.RFC3339Nano) }

// MarshalText formats i as RFC 3339 with nanoseconds, so persisted state that
// holds an Instant round-trips through JSON. It fails for a year outside
// [0, 9999], which RFC 3339 cannot hold, so such an Instant is rejected when
// state is saved rather than when it is loaded.
func (i Instant) MarshalText() ([]byte, error) {
	b, err := i.t.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("clock: instant cannot be written as RFC 3339: %w", err)
	}
	return b, nil
}

// UnmarshalText parses an RFC 3339 timestamp.
func (i *Instant) UnmarshalText(b []byte) error {
	var t time.Time
	if err := t.UnmarshalText(b); err != nil {
		return fmt.Errorf("clock: instant %q is not an RFC 3339 timestamp: %w", b, err)
	}
	*i = FromWire(t)
	return nil
}
