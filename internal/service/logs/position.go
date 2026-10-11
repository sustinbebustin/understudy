package logs

import (
	"cmp"
	"fmt"
	"strconv"
)

// position is a point in the event order: a timestamp and an ingestion
// sequence number. An event's own position is its eventId; a token names
// the gap just before a position.
type position struct {
	ts  int64
	seq uint64
}

// positionDigits is the width of an eventId and of a token's number, the 56
// digits AWS's documented examples show: 20 for the timestamp, 36 for the
// sequence.
const positionDigits = 56

// String is the 56-digit decimal form of p.
func (p position) String() string { return fmt.Sprintf("%020d%036d", p.ts, p.seq) }

func (p position) compare(q position) int {
	if c := cmp.Compare(p.ts, q.ts); c != 0 {
		return c
	}
	return cmp.Compare(p.seq, q.seq)
}

// next is the first position after p.
func (p position) next() position { return position{ts: p.ts, seq: p.seq + 1} }

// parsePosition reads the 56-digit form back.
func parsePosition(s string) (position, bool) {
	if len(s) != positionDigits {
		return position{}, false
	}
	ts, err := strconv.ParseInt(s[:20], 10, 64)
	if err != nil || ts < 0 {
		return position{}, false
	}
	seq, err := strconv.ParseUint(s[20:], 10, 64)
	if err != nil {
		return position{}, false
	}
	return position{ts: ts, seq: seq}, true
}

// direction is which way a page of events is read.
type direction uint8

const (
	// forward reads events at or after the cursor, oldest first.
	forward direction = iota + 1
	// backward reads events before the cursor, newest first.
	backward
)

// cursor is a paging token: a direction and the gap before at.
type cursor struct {
	dir direction
	at  position
}

// String is the token form, "f/<56 digits>" or "b/<56 digits>", as AWS's
// GetLogEvents tokens look.
func (c cursor) String() string {
	if c.dir == backward {
		return "b/" + c.at.String()
	}
	return "f/" + c.at.String()
}

// parseCursor reads a token; ok is false for anything this service did not
// issue.
func parseCursor(s string) (cursor, bool) {
	if len(s) < 2 || s[1] != '/' {
		return cursor{}, false
	}
	var dir direction
	switch s[0] {
	case 'f':
		dir = forward
	case 'b':
		dir = backward
	default:
		return cursor{}, false
	}
	at, ok := parsePosition(s[2:])
	if !ok {
		return cursor{}, false
	}
	return cursor{dir: dir, at: at}, true
}
