package fidelity

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/evidence"
)

// Switch is a behavior chosen without AWS-recorded evidence. It is not
// user-configurable in phase 1; the coverage report lists every switch.
type Switch[T comparable] struct {
	Name   string         // "logs.filter.term-boundary"
	Value  T              // the behavior in effect
	Label  evidence.Label // what backs Value
	Ref    string         // "docs/research/phase1/logs-filter-patterns.md#5"
	Record string         // the record-backlog item that settles it, "R10/C1"
}

// Entry is the type-erased view of a Switch for the coverage report.
type Entry struct {
	Name   string
	Value  string // the switch value formatted with %v
	Label  evidence.Label
	Ref    string
	Record string
}

// Entry returns the type-erased view of s.
func (s Switch[T]) Entry() Entry {
	return Entry{Name: s.Name, Value: fmt.Sprint(s.Value), Label: s.Label, Ref: s.Ref, Record: s.Record}
}
