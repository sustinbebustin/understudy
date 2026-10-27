package policydoc

import (
	"fmt"
	"slices"
	"strings"
)

// Grant is one Allow statement of a least-privilege policy: Actions on
// Resources, under Condition when it has one. Resources are patterns;
// Literal turns an exact ARN whose name holds * or ? into a pattern that
// names it alone.
type Grant struct {
	Actions   []string
	Resources []string
	// Condition is empty for an unconditional grant.
	Condition Condition
}

// Render returns the 2012-10-17 identity policy that allows every grant, in
// a deterministic form: one Allow statement per distinct grant, its actions
// and resources sorted and deduplicated, and the statements sorted by their
// actions, then their resources, then their conditions, so that
// per-service statements group by service. A grant without actions or
// without resources allows nothing and is dropped. ok is false when no
// grant is left, because a policy needs a statement. The input is not
// modified.
func Render(grants []Grant) (doc Document, ok bool) {
	kept := make([]Grant, 0, len(grants))
	for _, g := range grants {
		if len(g.Actions) == 0 || len(g.Resources) == 0 {
			continue
		}
		kept = append(kept, Grant{Actions: sortedSet(g.Actions), Resources: sortedSet(g.Resources), Condition: slices.Clone(g.Condition)})
	}
	if len(kept) == 0 {
		return Document{}, false
	}
	compare := func(a, b Grant) int {
		if c := slices.Compare(a.Actions, b.Actions); c != 0 {
			return c
		}
		if c := slices.Compare(a.Resources, b.Resources); c != 0 {
			return c
		}
		return strings.Compare(conditionText(a.Condition), conditionText(b.Condition))
	}
	slices.SortFunc(kept, compare)
	kept = slices.CompactFunc(kept, func(a, b Grant) bool { return compare(a, b) == 0 })
	statements := make([]Statement, 0, len(kept))
	for _, g := range kept {
		statements = append(statements, Statement{Effect: Allow, Action: Action(g.Actions), Resource: Resource(g.Resources), Condition: g.Condition})
	}
	return Document{Version: Version20121017, Statements: statements}, true
}

// conditionText is c as Render orders and compares it: each clause's
// operator, key and values, in order; "" for no condition.
func conditionText(c Condition) string {
	var b strings.Builder
	for _, clause := range c {
		fmt.Fprintf(&b, "%s %s %v;", clause.Operator, clause.Key, clause.Values)
	}
	return b.String()
}

// sortedSet returns the distinct values of s in sort order, as a new slice.
func sortedSet(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return slices.Compact(out)
}
