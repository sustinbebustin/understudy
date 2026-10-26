package fidelity

import (
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
)

// Switch is a behavior chosen without AWS-recorded evidence. It is not
// user-configurable; the coverage report lists every switch.
type Switch[T comparable] struct {
	Name   string         // "logs.filter.term-boundary"
	Value  T              // the behavior in effect
	Label  evidence.Label // what backs Value
	Ref    string         // "docs/research/logs-filter-patterns.md#5"
	Record string         // the record-backlog item that settles it, "R10/C1"
	// Ops are the operations whose behavior the switch governs, as
	// ref.OperationID strings: "sqs.DeleteMessage".
	Ops []string
	// Settles is the parity scenario step whose replay settles the switch,
	// or why none exists.
	Settles Settles
}

// Entry is the type-erased view of a Switch for the coverage report.
type Entry struct {
	Name    string
	Value   string // the switch value formatted with %v
	Label   evidence.Label
	Ref     string
	Record  string
	Ops     []string
	Settles Settles
}

// Entry returns the type-erased view of s.
func (s Switch[T]) Entry() Entry {
	return Entry{
		Name: s.Name, Value: fmt.Sprint(s.Value), Label: s.Label, Ref: s.Ref, Record: s.Record,
		Ops: slices.Clone(s.Ops), Settles: s.Settles,
	}
}
