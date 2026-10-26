package parity

import "github.com/sustinbebustin/understudy/internal/normalize"

// DiffKind says how a compared field differs.
type DiffKind uint8

// The difference kinds.
const (
	// Changed is a field both sides hold with different values.
	Changed DiffKind = iota + 1
	// Missing is a field only AWS's response holds.
	Missing
	// Extra is a field only understudy's response holds.
	Extra
	// Reordered is the same members or child elements in another order.
	Reordered
)

// String returns "changed", "missing", "extra" or "reordered". Not
// implemented yet: it returns "".
func (k DiffKind) String() string { return "" }

// Difference is one compared field where understudy's response differs
// from the recorded one, both normalized.
type Difference struct {
	Path Path
	Kind DiffKind
	// Expected is the recorded value, empty for Extra.
	Expected string
	// Actual is understudy's value, empty for Missing.
	Actual string
}

// String renders the difference on one line. Not implemented yet: it
// returns "".
func (d Difference) String() string { return "" }

// CompareOptions are a comparison's bound values and waivers.
type CompareOptions struct {
	// Bindings replace understudy's bound values with the AWS values
	// (Old: understudy's, New: AWS's), the bound normalizer.
	Bindings []normalize.Replacement
	// Waivers drop the differences at and below each path.
	Waivers []Path
}

// Compare returns the differences between the recorded response and
// understudy's, both normalized with normalize.Exchange, in comparison
// order, without those a waiver covers. Not implemented yet: it reports
// none.
func Compare(_, _ Response, _ CompareOptions) []Difference { return nil }

// ComparedHeader reports whether the comparison compares the response
// header name: Content-Type, and x-amz- and x-amzn- headers that are not
// ids, dates or a body digest. Not implemented yet: it reports false.
func ComparedHeader(string) bool { return false }
