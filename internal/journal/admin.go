package journal

import (
	"errors"

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
// not recorded: a file written by an earlier build carries no op.
type ClockOp uint8

// ClockOp values.
const (
	ClockFreeze ClockOp = iota + 1
	ClockResume
	ClockScale
	ClockAdvance
	ClockSet
	// ClockRestore is a reset that returns the clock to how serve started
	// it.
	ClockRestore
)

// errNotImplemented is what the parts of the admin-action and clock-op wire
// forms not implemented yet answer.
var errNotImplemented = errors.New("journal: the admin-action and clock-op wire forms are not implemented yet")

// String returns the wire name, such as "advance". Not implemented yet: it
// returns "".
func (ClockOp) String() string { return "" }

// MarshalText encodes the wire name. Not implemented yet: it returns an
// error.
func (ClockOp) MarshalText() ([]byte, error) { return nil, errNotImplemented }

// UnmarshalText decodes a wire name. Not implemented yet: it returns an
// error.
func (*ClockOp) UnmarshalText([]byte) error { return errNotImplemented }

// Kinds returns every declared Kind in value order. Not implemented yet: it
// returns nil.
func Kinds() []Kind { return nil }
