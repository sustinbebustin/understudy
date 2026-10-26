package journal

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// AdminAction is one state-changing admin call that succeeded, written by
// the admin handler once the call has done its work (ADR 0007 pre-tag
// review amendment).
type AdminAction struct{ Op AdminOp }

// AdminOp is the admin call an AdminAction records.
//
//sumtype:decl
type AdminOp interface{ isAdminOp() }

// Reset is POST /reset.
type Reset struct {
	// Services are the services whose state was dropped, as
	// admin.ResetResult.Services lists them.
	Services []string
	// Accounts and Regions are as the request named them; empty selects
	// every account or region.
	Accounts []string
	Regions  []string
	// Clock reports that the clock was returned to how serve started it.
	Clock bool
	// Faults is how many fault rules the reset removed.
	Faults int
}

// SnapshotSave is POST /snapshots. Path is the archive's path under the
// snapshots directory, "" when the archive went in the response body.
type SnapshotSave struct{ Path string }

// SnapshotLoad is PUT /snapshots. Path is the archive's path under the
// snapshots directory, "" when the archive came in the request body.
type SnapshotLoad struct{ Path string }

// FaultRuleAdded is PUT /faults/{id}: the rule added or replaced.
type FaultRuleAdded struct{ Rule ref.RuleID }

// FaultRuleRemoved is DELETE /faults/{id}, one id, or DELETE /faults, every
// rule that was active in evaluation order and none when there were none.
type FaultRuleRemoved struct{ Rules []ref.RuleID }

func (Reset) isAdminOp()            {}
func (SnapshotSave) isAdminOp()     {}
func (SnapshotLoad) isAdminOp()     {}
func (FaultRuleAdded) isAdminOp()   {}
func (FaultRuleRemoved) isAdminOp() {}

// ClockOp is what changed the clock in a ClockChanged. The zero value means
// not recorded: a file written by an earlier build carries no op, and NDJSON
// omits it.
type ClockOp uint8

// ClockOp values, named on the wire as the admin clock endpoint names its
// ops (ADR 0017).
const (
	ClockFreeze ClockOp = iota + 1
	ClockResume
	ClockScale
	ClockAdvance
	ClockSet
	// ClockRestore is a reset that returns the clock to how serve started
	// it.
	ClockRestore

	// endClockOp follows the last value and is no ClockOp: MarshalText,
	// UnmarshalText and the ClockOpError text count up to it, so a new op
	// is one line above it plus its String case.
	endClockOp
)

// String returns the wire name, such as "advance".
func (o ClockOp) String() string {
	switch o {
	case ClockFreeze:
		return "freeze"
	case ClockResume:
		return "resume"
	case ClockScale:
		return "scale"
	case ClockAdvance:
		return "advance"
	case ClockSet:
		return "set"
	case ClockRestore:
		return "restore"
	default:
		return fmt.Sprintf("ClockOp(%d)", uint8(o))
	}
}

// ClockOpError reports text that names no clock op, or a ClockOp outside
// its values.
type ClockOpError struct {
	Text string
}

func (e *ClockOpError) Error() string {
	names := make([]string, 0, endClockOp-ClockFreeze)
	for o := ClockFreeze; o < endClockOp; o++ {
		names = append(names, o.String())
	}
	return fmt.Sprintf("%q names no clock op; known names are %s and %s", e.Text, strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// MarshalText encodes the wire name. Op 0, not recorded, has none: NDJSON
// omits it rather than encode it.
func (o ClockOp) MarshalText() ([]byte, error) {
	if o < ClockFreeze || o >= endClockOp {
		return nil, &ClockOpError{Text: o.String()}
	}
	return []byte(o.String()), nil
}

// UnmarshalText decodes a wire name.
func (o *ClockOp) UnmarshalText(text []byte) error {
	for op := ClockFreeze; op < endClockOp; op++ {
		if op.String() == string(text) {
			*o = op
			return nil
		}
	}
	return &ClockOpError{Text: string(text)}
}
