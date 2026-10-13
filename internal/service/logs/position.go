package logs

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/clock"
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

// cursor is what a paging token points at: a direction and the gap before
// at.
type cursor struct {
	dir direction
	at  position
}

// token is the cursor's paging token of l issued at the given emulator
// instant: "f/" or "b/", as AWS's GetLogEvents tokens start, then the
// 56-digit position, l's fingerprint and the 20-digit issue stamp.
func (c cursor) token(l listing, issued clock.Instant) string {
	dir := "f/"
	if c.dir == backward {
		dir = "b/"
	}
	return stamp(dir+c.at.String()+l.fingerprint(), issued)
}

// issuedCursor is a cursor with the emulator instant its token was issued
// at, which an empty GetLogEvents page hands back unchanged.
type issuedCursor struct {
	cursor
	issued clock.Instant
}

// parseCursor reads a token of l; ok is false for anything this service
// did not issue for l and for a token expired at now.
func parseCursor(s string, l listing, now clock.Instant) (issuedCursor, bool) {
	stamped, issued, ok := unstamp(s, now)
	if !ok {
		return issuedCursor{}, false
	}
	body, ok := strings.CutSuffix(stamped, l.fingerprint())
	if !ok || len(body) < 2 || body[1] != '/' {
		return issuedCursor{}, false
	}
	var dir direction
	switch body[0] {
	case 'f':
		dir = forward
	case 'b':
		dir = backward
	default:
		return issuedCursor{}, false
	}
	at, ok := parsePosition(body[2:])
	if !ok {
		return issuedCursor{}, false
	}
	return issuedCursor{dir: dir, at: at, issued: issued}, true
}
