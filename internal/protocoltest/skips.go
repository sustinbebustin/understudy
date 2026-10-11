package protocoltest

import (
	_ "embed"
	"fmt"
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

//go:embed skips.json
var skipsJSON []byte

// Skip is a case the harness does not hold understudy to, and why.
type Skip struct {
	Key      Key
	Category Category
	// Reason says what the case asserts and why understudy does otherwise,
	// specifically enough to tell when the skip can go.
	Reason string
}

// Category is why a case is skipped.
//
//sumtype:decl
type Category interface {
	isCategory()
	// Name is the category's spelling in skips.json.
	Name() string
}

// CorpusBug is a case whose expectation is wrong or self-contradictory:
// a request missing the header that routes it, a body escaped twice.
type CorpusBug struct{}

// ClientOnly is a case only a client can run though the corpus does not
// mark it appliesTo client: the endpoint trait's host prefix, request
// compression, a value a server cannot tell from its absence.
type ClientOnly struct{}

// AWSDiffers is a case whose Smithy-framework expectation real AWS does
// not meet, with the evidence that says what AWS does.
type AWSDiffers struct {
	Label  evidence.Label
	Source string
}

// Gap is a known failure: a case understudy fails because a part it builds
// on cannot yet express what the case asserts; the reason names that part's
// package. It is understudy's failure, not a skip: the coverage report
// counts it as failed (ARCHITECTURE.md 6.4).
type Gap struct{}

// Name returns "corpus-bug".
func (CorpusBug) Name() string { return "corpus-bug" }

// Name returns "client-only".
func (ClientOnly) Name() string { return "client-only" }

// Name returns "aws-differs".
func (AWSDiffers) Name() string { return "aws-differs" }

// Name returns "gap".
func (Gap) Name() string { return "gap" }

func (CorpusBug) isCategory()  {}
func (ClientOnly) isCategory() {}
func (AWSDiffers) isCategory() {}
func (Gap) isCategory()        {}

// Skips is the skip list, sorted by key.
type Skips []Skip

// Lookup returns the skip of a case.
func (s Skips) Lookup(k Key) (Skip, bool) {
	i, ok := slices.BinarySearchFunc(s, k, func(x Skip, k Key) int { return compareKeys(x.Key, k) })
	if !ok {
		return Skip{}, false
	}
	return s[i], true
}

// SkipsError reports a skip list that does not parse or breaks a rule.
type SkipsError struct {
	// Entry is the zero-based index of the offending entry; -1 when the
	// document itself is unreadable.
	Entry  int
	Reason string
}

func (e *SkipsError) Error() string {
	if e.Entry < 0 {
		return "internal/protocoltest/skips.json: " + e.Reason
	}
	return fmt.Sprintf("internal/protocoltest/skips.json entry %d: %s", e.Entry, e.Reason)
}

// CheckedInSkips returns internal/protocoltest/skips.json.
func CheckedInSkips() (Skips, error) { return ParseSkips(skipsJSON) }

type skipsDoc struct {
	Skips []skipJSON `json:"skips"`
}

type skipJSON struct {
	Protocol string `json:"protocol"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Category string `json:"category"`
	Label    string `json:"label,omitempty"`
	Source   string `json:"source,omitempty"`
	Reason   string `json:"reason"`
}

// ParseSkips reads a skip list: every entry names a case, a category and a
// reason; aws-differs entries also carry an evidence label and a source,
// and no case is listed twice.
func ParseSkips(data []byte) (Skips, error) {
	var doc skipsDoc
	if err := jsonx.Unmarshal(data, &doc); err != nil {
		return nil, &SkipsError{Entry: -1, Reason: err.Error()}
	}
	out := make(Skips, 0, len(doc.Skips))
	for i, e := range doc.Skips {
		s, reason := e.parse()
		if reason != "" {
			return nil, &SkipsError{Entry: i, Reason: reason}
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Skip) int { return compareKeys(a.Key, b.Key) })
	for i := 1; i < len(out); i++ {
		if out[i].Key == out[i-1].Key {
			return nil, &SkipsError{Entry: -1, Reason: fmt.Sprintf("%s is listed twice", out[i].Key)}
		}
	}
	return out, nil
}

// parse returns the skip, or what is wrong with the entry.
func (e skipJSON) parse() (s Skip, problem string) {
	kind, ok := parseKind(e.Kind)
	switch {
	case e.Protocol == "" || e.ID == "":
		return Skip{}, "protocol and id are required"
	case !ok:
		return Skip{}, fmt.Sprintf("kind %q is not one of %q", e.Kind, Kinds())
	case e.Reason == "":
		return Skip{}, "reason is required: say what the case asserts and why understudy does otherwise"
	}
	s = Skip{Key: Key{Protocol: ref.ProtocolID(e.Protocol), Kind: kind, ID: e.ID}, Reason: e.Reason}
	if e.Category == (AWSDiffers{}).Name() {
		label, err := evidence.ParseLabel(e.Label)
		if err != nil {
			return Skip{}, "aws-differs needs an evidence label: " + err.Error()
		}
		if e.Source == "" {
			return Skip{}, "aws-differs needs a source: a link or research section showing what AWS does"
		}
		s.Category = AWSDiffers{Label: label, Source: e.Source}
		return s, ""
	}
	if e.Label != "" || e.Source != "" {
		return Skip{}, fmt.Sprintf("label and source belong to aws-differs entries, not %q", e.Category)
	}
	for _, c := range []Category{CorpusBug{}, ClientOnly{}, Gap{}} {
		if c.Name() == e.Category {
			s.Category = c
			return s, ""
		}
	}
	return Skip{}, fmt.Sprintf("category %q is not one of corpus-bug, client-only, aws-differs, gap", e.Category)
}
