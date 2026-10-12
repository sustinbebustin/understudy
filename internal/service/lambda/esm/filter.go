package esm

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ruler"
)

// Switches lists the filter pattern behaviors chosen without AWS-recorded
// evidence, each with the record-backlog item that settles it
// (matching-engines.md 3, 8 and 10).
func Switches() []fidelity.Entry {
	meta := ruler.Switches(ruler.LambdaESM{})
	out := make([]fidelity.Entry, 0, len(meta))
	for _, sw := range meta {
		out = append(out, fidelity.Switch[string]{
			Name: sw.Name, Value: sw.Value, Label: evidence.Inferred, Ref: sw.Ref, Record: sw.Record,
		}.Entry())
	}
	return out
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
