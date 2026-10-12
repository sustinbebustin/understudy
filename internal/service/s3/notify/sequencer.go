package notify

import (
	"fmt"
	"time"
)

// Sequencer orders the events of one key: S3 tells consumers to compare
// two sequencers of a key as hex strings left-padded to one length.
type Sequencer uint64

// Next is the sequencer of a change at the given time after one whose
// sequencer was last: the time in Unix nanoseconds, or last+1 when that is
// not later, so sequencers strictly increase when the clock stands still
// (frozen) or steps back (a snapshot load). After math.MaxUint64 it stays
// there, which no emulator time reaches.
func Next(last Sequencer, at time.Time) Sequencer {
	if last == ^Sequencer(0) {
		return last
	}
	return max(Sequencer(max(at.UnixNano(), 0)), last+1)
}

// String is 18 uppercase hex digits, the width of AWS's sequencers
// (0055AED6DCD90281E5).
func (s Sequencer) String() string { return fmt.Sprintf("%018X", uint64(s)) }
