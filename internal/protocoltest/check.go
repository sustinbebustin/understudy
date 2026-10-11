package protocoltest

import (
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Finding is one way a corpus run disagrees with the baseline or the skip
// list. Any finding fails the corpus test.
//
//sumtype:decl
type Finding interface {
	String() string
	isFinding()
}

// Regressed is a case the baseline records as passing that failed.
type Regressed struct {
	Key     Key
	Failure error
}

// Unrecorded is a case that passed but is missing from the baseline, so
// the ratchet would not catch it regressing.
type Unrecorded struct{ Key Key }

// Unexplained is a failing case that is neither in the baseline nor in
// the skip list.
type Unexplained struct {
	Key     Key
	Failure error
}

// StaleSkip is a skipped case that passes: its skip should go.
type StaleSkip struct{ Key Key }

// Unknown is a case the baseline or the skip list names that the run did
// not hold: renamed or removed by a corpus update.
type Unknown struct {
	Key Key
	// In is "baseline.json" or "skips.json".
	In string
}

// Recounted is a suite whose case count differs from the baseline's.
type Recounted struct {
	Protocol ref.ProtocolID
	Kind     Kind
	Ran      int
	Recorded int
}

func (f Regressed) String() string {
	return fmt.Sprintf("%s regressed: the baseline records it passing, now %v", f.Key, f.Failure)
}

func (f Unrecorded) String() string {
	return fmt.Sprintf("%s passes but is not in the baseline; run just protocoltest-baseline and commit baseline.json", f.Key)
}

func (f Unexplained) String() string {
	return fmt.Sprintf("%s fails and is neither in the baseline nor in skips.json; fix it or add a skip with a reason: %v", f.Key, f.Failure)
}

func (f StaleSkip) String() string {
	return fmt.Sprintf("%s passes though skips.json skips it; remove the skip and run just protocoltest-baseline", f.Key)
}

func (f Unknown) String() string {
	return fmt.Sprintf("%s is in %s but the corpus has no such case; remove it", f.Key, f.In)
}

func (f Recounted) String() string {
	return fmt.Sprintf("%s %s: the corpus holds %d cases, the baseline records %d; run just protocoltest-baseline",
		f.Protocol, f.Kind, f.Ran, f.Recorded)
}

func (Regressed) isFinding()   {}
func (Unrecorded) isFinding()  {}
func (Unexplained) isFinding() {}
func (StaleSkip) isFinding()   {}
func (Unknown) isFinding()     {}
func (Recounted) isFinding()   {}

// Check compares a run with the baseline and the skip list. Every case is
// either recorded as passing or skipped with a reason; anything else is a
// finding: a recorded case that fails, a passing case the baseline lacks,
// a failing case nobody explained, a skip that no longer fails, a name
// the corpus no longer has, a suite whose size changed.
func (b Baseline) Check(run []Result, skips Skips) []Finding {
	var out []Finding
	ran := make(map[Key]bool, len(run))
	for _, r := range run {
		ran[r.Key] = true
		if f, ok := b.judge(r, skips); ok {
			out = append(out, f)
		}
	}
	for _, s := range b.Suites {
		for _, id := range s.Passing {
			if k := (Key{Protocol: s.Protocol, Kind: s.Kind, ID: id}); !ran[k] {
				out = append(out, Unknown{Key: k, In: "baseline.json"})
			}
		}
	}
	for _, s := range skips {
		if !ran[s.Key] {
			out = append(out, Unknown{Key: s.Key, In: "skips.json"})
		}
	}
	out = append(out, b.recount(FromRun(run))...)
	slices.SortStableFunc(out, func(x, y Finding) int { return compareKeys(findingKey(x), findingKey(y)) })
	return out
}

// judge returns the finding one result makes, if any.
func (b Baseline) judge(r Result, skips Skips) (Finding, bool) {
	_, skipped := skips.Lookup(r.Key)
	recorded := b.passes(r.Key)
	switch {
	case r.Failure == nil && skipped:
		return StaleSkip{Key: r.Key}, true
	case r.Failure == nil && !recorded:
		return Unrecorded{Key: r.Key}, true
	case r.Failure != nil && recorded:
		return Regressed(r), true
	case r.Failure != nil && !skipped:
		return Unexplained(r), true
	default:
		return nil, false
	}
}

// recount compares the suite sizes of a run with the recorded ones.
func (b Baseline) recount(got Baseline) []Finding {
	var out []Finding
	for _, s := range b.Suites {
		if g, _ := got.Suite(s.Protocol, s.Kind); g.Cases != s.Cases {
			out = append(out, Recounted{Protocol: s.Protocol, Kind: s.Kind, Ran: g.Cases, Recorded: s.Cases})
		}
	}
	for _, g := range got.Suites {
		if _, ok := b.Suite(g.Protocol, g.Kind); !ok {
			out = append(out, Recounted{Protocol: g.Protocol, Kind: g.Kind, Ran: g.Cases})
		}
	}
	return out
}

func findingKey(f Finding) Key {
	switch f := f.(type) {
	case Regressed:
		return f.Key
	case Unrecorded:
		return f.Key
	case Unexplained:
		return f.Key
	case StaleSkip:
		return f.Key
	case Unknown:
		return f.Key
	case Recounted:
		return Key{Protocol: f.Protocol, Kind: f.Kind}
	}
	return Key{}
}
