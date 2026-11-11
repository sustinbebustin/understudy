package esm

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ruler"
)

// Switches lists the filter pattern behaviors chosen without AWS-recorded
// evidence, each with the record-backlog item that settles it
// (matching-engines.md 3, 8 and 10), and the label, operations and
// settling step switchTable states for it.
func Switches() []fidelity.Entry {
	meta := ruler.Switches(ruler.LambdaESM{})
	table := switchTable()
	out := make([]fidelity.Entry, 0, len(meta))
	for _, sw := range meta {
		m := table[sw.Name]
		out = append(out, fidelity.Switch[string]{
			Name: sw.Name, Value: sw.Value, Label: m.label, Ref: sw.Ref, Record: sw.Record, Ops: m.ops, Settles: m.settles,
		}.Entry())
	}
	return out
}

// switchMeta is what Lambda states about one of Ruler's Lambda profile
// switches: what backs its value, the operations it governs and what
// settles it.
type switchMeta struct {
	label   evidence.Label
	ops     []string
	settles fidelity.Settles
}

// switchTable holds the switchMeta of every Lambda profile switch by name.
// A name it lacks gets no label, operations or settles, which the coverage
// checks refuse. A mapping's filter criteria are compiled by
// CreateEventSourceMapping and UpdateEventSourceMapping and matched by its
// poller, which no operation exposes, so every switch governs those two.
// The lambda/R03 steps are REST requests, and the ones that match need a
// function under Docker; the replayer skips both.
func switchTable() map[string]switchMeta {
	ops := []string{"lambda.CreateEventSourceMapping", "lambda.UpdateEventSourceMapping"}
	r03 := func(steps string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "recorded in lambda/R03 (" + steps + "); the replayer skips REST and Docker steps"}
	}
	return map[string]switchMeta{
		"lambda.esm-filter.exists-true-empty-value": {evidence.Inferred, ops, r03("H2-01-create")},
		"lambda.esm-filter.empty-input": {
			evidence.Inferred, ops,
			fidelity.NoScenario{Reason: "unrecorded for Lambda: matching-engines.md H1 covers SNS and EventBridge only"},
		},
		"lambda.esm-filter.bare-number":  {evidence.Inferred, ops, r03("H7a-01-create, H7a-02-create")},
		"lambda.esm-filter.malformed-or": {evidence.Inferred, ops, r03("H10-01-create to H10-06-create")},
	}
}

// Filters are a mapping's filter patterns compiled for Lambda. A record
// passes when it matches any of them; with none, every record passes
// (lambda-events.md 2.4).
type Filters struct {
	patterns []*ruler.Pattern
}

// CompileFilters compiles patterns with the Lambda profile. The control
// plane accepted them with the same profile, so an error means a stored
// pattern no longer compiles.
func CompileFilters(patterns []string) (Filters, *ruler.CompileError) {
	f := Filters{patterns: make([]*ruler.Pattern, 0, len(patterns))}
	for _, p := range patterns {
		compiled, err := ruler.Compile([]byte(p), ruler.LambdaESM{})
		if err != nil {
			return Filters{}, err
		}
		f.patterns = append(f.patterns, compiled)
	}
	return f, nil
}

// Match reports whether r passes the filters. The body is matched as JSON
// when it is valid JSON and as a string otherwise, so a JSON pattern never
// matches a plain-text body and a plain pattern never matches a JSON one.
// An error is a record the engine could not evaluate within its work
// bound; no pattern matched it.
func (f Filters) Match(r *Record) (bool, error) {
	if len(f.patterns) == 0 {
		return true, nil
	}
	event, err := filterEvent(r)
	if err != nil {
		return false, err
	}
	var first error
	for _, p := range f.patterns {
		ok, err := ruler.Match(p, event)
		switch {
		case ok:
			return true, nil
		case err != nil && first == nil:
			first = err
		}
	}
	return false, first
}
