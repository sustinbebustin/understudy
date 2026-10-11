package protocoltest

import (
	_ "embed"
	"fmt"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

//go:embed baseline.json
var baselineJSON []byte

// Baseline is the checked-in record of a corpus run: per protocol and
// kind, how many server-runnable cases the generated tables hold and which
// of them pass. The corpus test holds every run to it, so the pass count
// only grows (ARCHITECTURE.md 6.4).
type Baseline struct {
	Suites []Suite
}

// Suite is one protocol's cases of one kind.
type Suite struct {
	Protocol ref.ProtocolID
	Kind     Kind
	// Cases is how many cases of the kind the generated tables hold.
	Cases int
	// Passing are the ids of the cases that pass, sorted.
	Passing []string
}

// BaselineError reports a baseline document that does not parse or
// breaks a rule.
type BaselineError struct{ Reason string }

func (e *BaselineError) Error() string {
	return "internal/protocoltest/baseline.json: " + e.Reason + "; rewrite it with just protocoltest-baseline"
}

// CheckedInBaseline returns internal/protocoltest/baseline.json.
func CheckedInBaseline() (Baseline, error) { return ParseBaseline(baselineJSON) }

type baselineDoc struct {
	Suites []suiteJSON `json:"suites"`
}

type suiteJSON struct {
	Protocol string   `json:"protocol"`
	Kind     string   `json:"kind"`
	Cases    int      `json:"cases"`
	Passing  []string `json:"passing"`
}

// ParseBaseline reads a baseline document.
func ParseBaseline(data []byte) (Baseline, error) {
	var doc baselineDoc
	if err := jsonx.Unmarshal(data, &doc); err != nil {
		return Baseline{}, &BaselineError{Reason: err.Error()}
	}
	var b Baseline
	for _, s := range doc.Suites {
		kind, ok := parseKind(s.Kind)
		if !ok {
			return Baseline{}, &BaselineError{Reason: fmt.Sprintf("kind %q is not one of %q", s.Kind, Kinds())}
		}
		passing := slices.Sorted(slices.Values(s.Passing))
		if len(slices.Compact(slices.Clone(passing))) != len(passing) || len(passing) > s.Cases {
			return Baseline{}, &BaselineError{Reason: fmt.Sprintf("%s %s lists a case twice or more passing cases than its %d", s.Protocol, kind, s.Cases)}
		}
		b.Suites = append(b.Suites, Suite{Protocol: ref.ProtocolID(s.Protocol), Kind: kind, Cases: s.Cases, Passing: passing})
	}
	sortSuites(b.Suites)
	for i := 1; i < len(b.Suites); i++ {
		if p, s := b.Suites[i-1], b.Suites[i]; p.Protocol == s.Protocol && p.Kind == s.Kind {
			return Baseline{}, &BaselineError{Reason: fmt.Sprintf("%s %s is listed twice", s.Protocol, s.Kind)}
		}
	}
	return b, nil
}

// FromRun returns the baseline a run records: every case it ran, counted,
// and the ones that passed.
func FromRun(run []Result) Baseline {
	suites := map[[2]string]*Suite{}
	for _, r := range run {
		at := [2]string{string(r.Key.Protocol), string(r.Key.Kind)}
		s, ok := suites[at]
		if !ok {
			s = &Suite{Protocol: r.Key.Protocol, Kind: r.Key.Kind}
			suites[at] = s
		}
		s.Cases++
		if r.Failure == nil {
			s.Passing = append(s.Passing, r.Key.ID)
		}
	}
	var b Baseline
	for _, s := range suites {
		slices.Sort(s.Passing)
		b.Suites = append(b.Suites, *s)
	}
	sortSuites(b.Suites)
	return b
}

// Suite returns the suite of a protocol and kind.
func (b Baseline) Suite(p ref.ProtocolID, k Kind) (Suite, bool) {
	i := slices.IndexFunc(b.Suites, func(s Suite) bool { return s.Protocol == p && s.Kind == k })
	if i < 0 {
		return Suite{}, false
	}
	return b.Suites[i], true
}

// Encode writes the baseline as checked in: one id per line, so a diff
// shows exactly which cases started or stopped passing.
func (b Baseline) Encode() []byte {
	out := []byte("{\n  \"suites\": [")
	for i, s := range b.Suites {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, "\n    {\n      \"protocol\": "...)
		out = jsonx.AppendString(out, string(s.Protocol))
		out = append(out, ",\n      \"kind\": "...)
		out = jsonx.AppendString(out, string(s.Kind))
		out = append(out, ",\n      \"cases\": "...)
		out = strconv.AppendInt(out, int64(s.Cases), 10)
		out = append(out, ",\n      \"passing\": ["...)
		for j, id := range s.Passing {
			if j > 0 {
				out = append(out, ',')
			}
			out = append(out, "\n        "...)
			out = jsonx.AppendString(out, id)
		}
		if len(s.Passing) > 0 {
			out = append(out, "\n      "...)
		}
		out = append(out, "]\n    }"...)
	}
	return append(out, "\n  ]\n}\n"...)
}

// passes reports whether the baseline records k as passing.
func (b Baseline) passes(k Key) bool {
	s, ok := b.Suite(k.Protocol, k.Kind)
	if !ok {
		return false
	}
	_, found := slices.BinarySearch(s.Passing, k.ID)
	return found
}

// sortSuites orders suites by protocol, then kind.
func sortSuites(s []Suite) {
	slices.SortFunc(s, func(x, y Suite) int {
		return compareKeys(Key{Protocol: x.Protocol, Kind: x.Kind}, Key{Protocol: y.Protocol, Kind: y.Kind})
	})
}
