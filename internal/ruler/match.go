package ruler

import (
	"cmp"
	"slices"
	"strconv"
)

// Match reports whether a JSON event matches the pattern. An event that is
// not well-formed JSON, or whose top level is not an object, is an
// *EventError. A pattern compiled for the SNS MessageAttributes scope matches
// attributes, not events, and is an *InputError. An event whose search
// exceeds the engine's work bound is a *SearchBudgetError.
func Match(p *Pattern, event []byte) (bool, error) {
	if want := p.profile.input(); want != InputEvent {
		return false, &InputError{Want: want}
	}
	fs, err := flattenEvent(event)
	if err != nil {
		return false, err
	}
	return p.matches(fs, maxSearchSteps)
}

// Attribute is a typed SNS message attribute value, the input of the
// MessageAttributes scope. Callers pass every attribute of a message,
// Binary ones included.
//
//sumtype:decl
type Attribute interface{ isAttribute() }

// StringAttribute is a String attribute: one string value.
type StringAttribute struct{ Value string }

// NumberAttribute is a Number attribute. Its StringValue is matched as a
// JSON number, which is how SNS satisfies numeric rules with it
// (matching-engines.md 5.4), so string rules never accept it and a string
// anything-but always does. sns.md 6.3 reads the documentation as comparing
// the StringValue text instead; the fixed switch
// sns.filter-policy.number-attribute-type records the choice.
type NumberAttribute struct{ Value string }

// StringArrayAttribute is a String.Array attribute: its StringValue, a JSON
// array whose elements are matched as separate values.
type StringArrayAttribute struct{ JSON string }

// BinaryAttribute is a Binary attribute. SNS ignores Binary attributes when
// it filters (sns.md 6.3), so its value is not needed, but it is still an
// attribute: with EmptyInputNeverMatches, a message whose only attribute is
// Binary is not "an empty set of attributes". The documentation does not
// settle that reading and no recording covers it (matching-engines.md H1
// records only String attributes).
type BinaryAttribute struct{}

func (StringAttribute) isAttribute()      {}
func (NumberAttribute) isAttribute()      {}
func (StringArrayAttribute) isAttribute() {}
func (BinaryAttribute) isAttribute()      {}

// MatchAttributes reports whether message attributes match the pattern
// (SNS MessageAttributes scope): each attribute name is a top-level key. A
// String.Array value that is not exactly one JSON array is an *EventError. A
// pattern compiled for any other profile or scope matches JSON events and is
// an *InputError. Attributes whose search exceeds the engine's work bound are
// a *SearchBudgetError.
func MatchAttributes(p *Pattern, attrs map[string]Attribute) (bool, error) {
	if want := p.profile.input(); want != InputAttributes {
		return false, &InputError{Want: want}
	}
	// One flattener numbers the arrays of every String.Array attribute, so
	// arrays from different attributes never share an id.
	f := newFlattener(nil)
	f.out.empty = len(attrs) == 0
	for name, a := range attrs {
		switch a := a.(type) {
		case StringAttribute:
			f.out.add(name, value{kind: kindString, text: a.Value}, nil)
		case NumberAttribute:
			n, ok := comparableNumber(a.Value)
			f.out.add(name, value{kind: kindNumber, text: a.Value, num: n, numOK: ok}, nil)
		case StringArrayAttribute:
			if err := f.addStringArray(name, a.JSON); err != nil {
				return false, err
			}
		case BinaryAttribute:
		}
	}
	return p.matches(f.out, maxSearchSteps)
}

func (f *flattener) addStringArray(name, src string) *EventError {
	notArray := &EventError{Detail: "String.Array attribute " + strconv.Quote(name) + " is not a JSON array"}
	invalid := func(err *syntaxError) *EventError {
		return &EventError{Detail: "String.Array attribute " + strconv.Quote(name) + " is not valid JSON: " + err.message}
	}
	f.lx = newLexer([]byte(src))
	t, err := f.lx.next()
	if err != nil {
		return invalid(err)
	}
	if t.kind != tokStartArray {
		return notArray
	}
	if err := f.array(name); err != nil {
		return invalid(err)
	}
	// Anything after the array, even another array, makes the value more
	// than one JSON array.
	if t, err := f.lx.next(); err != nil || t.kind != tokEOF {
		return notArray
	}
	return nil
}

// maxSearchSteps bounds the work of one Match or MatchAttributes call across
// every branch: the indexing of the fields at the pattern's paths and of the
// arrays they share (links), the split of each branch into components, and
// the search of each component, where a step costs one unit per element
// chosen so far (search.cost). It is an engine bound with no AWS
// counterpart, like maxExpansion on the compile side. The events the
// complexity tests build, and realistic ones, need a few thousand steps per
// branch searched; an event that joins several arrays into one component
// (see search) can need a state per combination of their elements, about
// 16x more work per nesting level at 16 elements per array. This bound turns
// that into a *SearchBudgetError: the TestSearchBudgetCoversChosenFields
// shapes, patterns under 4096 bytes with up to a hundred keys per level,
// stop within about 0.2 s and 50 MB on a desktop machine; without this
// bound they take seconds and gigabytes.
const maxSearchSteps = 1 << 22

// budget is the work one Match call may still do, shared by every search it
// runs.
type budget struct{ used, limit int }

// spend charges n steps and reports whether the budget still holds.
func (b *budget) spend(n int) bool {
	b.used += n
	return b.used <= b.limit
}

// charge adds n steps of work done in bulk.
func (b *budget) charge(n int) { b.used += n }

func (b *budget) exceeded() bool { return b.used > b.limit }

// matches reports whether any branch is satisfied. It gives up with a
// *SearchBudgetError once the call has done limit steps.
func (p *Pattern) matches(fs *fields, limit int) (bool, error) {
	o := p.profile.options()
	if fs.empty && o.EmptyInput == EmptyInputNeverMatches {
		return false, nil
	}
	work := &budget{limit: limit}
	ln := newLinks(p.branches, fs, work)
	if work.exceeded() {
		return false, &SearchBudgetError{Steps: limit}
	}
	for _, b := range p.branches {
		ok := branchSatisfied(b, ln, o, work)
		if work.exceeded() {
			return false, &SearchBudgetError{Steps: limit}
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// branchSatisfied decides one branch, one connected component of its
// conditions at a time (links.components). Fields that share no array never
// conflict, so a component's choices leave every field of another component
// consistent, and an absence, which looks only at fields at its own path,
// sees nothing another component chose. The branch is therefore satisfied
// exactly when each component is. A component is searched only when the
// ones before it are satisfied.
//
// Searched together, independent arrays would multiply: the failure memo
// keeps one anchor per array a remaining condition sees, so a branch whose
// sorted paths alternate between arrays ("a.b", "a.m.b", "a.m.z", "a.z")
// would hold a state per combination of their elements.
func branchSatisfied(conds []*condition, ln *links, o Options, work *budget) bool {
	for _, g := range ln.components(conds, work) {
		cs, ps := make([]*condition, len(g)), make([]*pathFields, len(g))
		for j, i := range g {
			cs[j], ps[j] = conds[i], ln.paths[ln.id[conds[i].path]]
		}
		if !newSearch(cs, ps, o, work).assign(0) {
			return false
		}
	}
	return true
}

// search looks for an array-consistent assignment for one connected
// component of a branch, taking the conditions in the order Ruler's
// traversal does: sorted by path. Every condition takes a field at its path
// that one of its matchers accepts, or, when it holds {"exists": false},
// takes no field. No two chosen fields may come from different elements of
// one array (matching-engines.md 4.3). An absence is checked when the search
// reaches its condition, against the fields chosen for the paths before it:
// Ruler checks it against the array positions those paths pinned
// (ACFinder.tryMustNotExistMatch with NameState.getNameTransitions), so a
// field the later paths rule out still breaks it.
//
// The search backtracks, and remembers every state it has seen fail. What
// assign(i) can still depend on is i and, for each chosen element, its
// anchor: the deepest element on its chain in an array that a condition from
// i on has fields in. A choice in an array no remaining condition sees
// therefore drops out of the state, so a failing condition is not retried
// once per earlier choice that cannot affect it. A choice the remaining
// conditions still see stays: when they see several arrays of one component,
// the states can number the product of those arrays' sizes. That happens
// when one path has fields in several arrays (duplicate keys, or a dotted key
// next to the nesting it spells), which joins the arrays into one component.
// No exact search avoids it for every such event: with one array per
// variable and one path per clause, array consistency decides
// satisfiability. The shared budget (maxSearchSteps) bounds that case.
type search struct {
	conds []*condition
	o     Options
	// paths[i] indexes the fields at conds[i].path.
	paths []*pathFields
	// chosen holds the innermost elements of the chosen fields that sit in
	// an array.
	chosen []*element
	failed map[string]struct{}
	// last caches, per array id, the last condition with fields in it, or -1.
	last map[int]int
	// anchors caches anchor by element and condition.
	anchors map[anchorKey]*element
	// walked, ids and key are scratch space that anchor and state reuse, so
	// a step allocates only the memo key it may keep.
	walked []*element
	ids    []int
	key    []byte
	// work is charged cost() per assign call and examined field, and is
	// shared by every search of one Match call. The search gives up, failing
	// every call, once it is spent. The complexity tests read its use to
	// bound the work adversarial events cost in these units rather than in
	// time.
	work *budget
}

type anchorKey struct {
	e *element
	i int
}

// pathFields is the fields at one path, whether or not a matcher accepts
// them, in document order, with the arrays they sit in.
type pathFields struct {
	all []field
	// arrays holds every array id on the fields' chains.
	arrays map[int]struct{}
	// shared is the number of arrays every field sits in, the first ones on
	// rep's chain; rep is the first field's element. A path can have fields
	// both inside and outside an array (a duplicate key, or a scalar next to
	// a nested array), so shared can be less than the depth of each field.
	shared int
	rep    *element
	// roots holds, in ascending order, the outermost arrays on the fields'
	// chains: the arrays in arrays that sit in no other array.
	roots []int
}

// newPathFields indexes the fields at one path, charging one step per field
// and per element on their chains.
func newPathFields(all []field, work *budget) *pathFields {
	pf := &pathFields{all: all, arrays: map[int]struct{}{}}
	if len(all) == 0 {
		return pf
	}
	pf.rep = all[0].elem
	pf.shared = depthOf(pf.rep)
	seen := map[*element]struct{}{}
	for _, f := range all {
		work.charge(1)
		if pf.shared > 0 {
			n, _ := split(f.elem, pf.rep)
			pf.shared = min(pf.shared, n)
		}
		for e := f.elem; e != nil; e = e.parent {
			if _, ok := seen[e]; ok {
				break
			}
			work.charge(1)
			seen[e] = struct{}{}
			pf.arrays[e.array] = struct{}{}
			// The fields come in document order, so their outermost elements
			// do too, and the elements of one outermost array are next to each
			// other: comparing with the last root keeps roots unique.
			if e.parent == nil && (len(pf.roots) == 0 || pf.roots[len(pf.roots)-1] != e.array) {
				pf.roots = append(pf.roots, e.array)
			}
		}
	}
	return pf
}

// newSearch searches conds, whose fields paths holds index for index.
func newSearch(conds []*condition, paths []*pathFields, o Options, work *budget) *search {
	return &search{
		conds: conds, o: o, paths: paths, work: work,
		failed: map[string]struct{}{}, last: map[int]int{}, anchors: map[anchorKey]*element{},
	}
}

// cost is the work of one step: an assign call, which builds the failure
// memo's key (state) and scans for a pin (fieldsAt, at most twice per call),
// or an examined field, which is checked for consistency. Each looks at every
// chosen element once, so a step costs one unit per chosen element, and at
// least one.
func (s *search) cost() int { return max(1, len(s.chosen)) }

func (s *search) assign(i int) bool {
	if !s.work.spend(s.cost()) {
		return false
	}
	if i == len(s.conds) {
		return true
	}
	key := s.state(i)
	if _, seen := s.failed[key]; seen {
		return false
	}
	if s.extend(i) {
		return true
	}
	s.failed[key] = struct{}{}
	return false
}

// extend tries every way to satisfy conds[i] given the choices so far, then
// the rest of the branch.
func (s *search) extend(i int) bool {
	for _, e := range s.candidates(i) {
		mark := len(s.chosen)
		// An element already chosen adds no constraint. Leaving it out keeps
		// the cost of a step to the distinct elements chosen, so conditions
		// under one array element cost linear work, not quadratic.
		if e != nil && !slices.Contains(s.chosen, e) {
			s.chosen = append(s.chosen, e)
		}
		if s.assign(i + 1) {
			return true
		}
		s.chosen = s.chosen[:mark]
	}
	return s.conds[i].absent && s.absent(i) && s.assign(i+1)
}

// absent reports whether no field at conds[i].path is consistent with the
// choices so far.
func (s *search) absent(i int) bool {
	for _, f := range s.fieldsAt(i) {
		if !s.work.spend(s.cost()) || s.consistent(f.elem) {
			return false
		}
	}
	return true
}

// candidates returns the elements of the fields at the condition's path that
// are consistent with the choices so far and accepted by a matcher; nil
// stands for a field outside every array. Fields of one element are
// interchangeable. The flattener emits the fields of one element at one path
// next to each other, so comparing with the last kept element drops most
// repeats in linear time; a repeat that gets through only leads to a state
// the failure memo already holds.
func (s *search) candidates(i int) []*element {
	c := s.conds[i]
	var out []*element
	for _, f := range s.fieldsAt(i) {
		if !s.work.spend(s.cost()) {
			return nil
		}
		if !s.consistent(f.elem) || !accepted(c.matchers, f.value, s.o) {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == f.elem {
			continue
		}
		out = append(out, f.elem)
	}
	return out
}

// fieldsAt returns the fields at conds[i].path that the choices so far leave
// possible. A chosen element in an array every field at the path sits in
// pins that array, so only the fields inside the pinned element can be
// consistent, and a condition under an array an earlier one pinned looks at
// one element instead of rescanning the whole array.
func (s *search) fieldsAt(i int) []field {
	pf := s.paths[i]
	var pin *element
	if pf.shared > 0 {
		for _, c := range s.chosen {
			n, _ := split(c, pf.rep)
			if n = min(n, pf.shared); n > depthOf(pin) {
				pin = ancestorAt(c, n)
			}
		}
	}
	if pin == nil {
		return pf.all
	}
	bySeq := func(f field, seq int) int { return cmp.Compare(f.seq, seq) }
	lo, _ := slices.BinarySearchFunc(pf.all, pin.first, bySeq)
	hi, _ := slices.BinarySearchFunc(pf.all, pin.end, bySeq)
	return pf.all[lo:hi]
}

// consistent reports whether a field in element e can join the chosen ones.
func (s *search) consistent(e *element) bool {
	for _, c := range s.chosen {
		if !compatible(e, c) {
			return false
		}
	}
	return true
}

// state encodes what assign(i) depends on: i and the anchors of the chosen
// elements.
func (s *search) state(i int) string {
	ids := s.ids[:0]
	for _, c := range s.chosen {
		if a := s.anchor(c, i); a != nil {
			ids = append(ids, a.id)
		}
	}
	slices.Sort(ids)
	key := strconv.AppendInt(s.key[:0], int64(i), 10)
	for _, id := range slices.Compact(ids) {
		key = strconv.AppendInt(append(key, ' '), int64(id), 10)
	}
	s.ids, s.key = ids, key
	return string(key)
}

// anchor returns the deepest element on e's chain in an array that a
// condition from conds[i] on has fields in, or nil. The arrays below it reach
// no remaining condition, so e and its anchor rule out the same remaining
// fields.
func (s *search) anchor(e *element, i int) *element {
	walked := s.walked[:0]
	var found *element
	for ; e != nil; e = e.parent {
		if a, ok := s.anchors[anchorKey{e, i}]; ok {
			found = a
			break
		}
		if s.lastWith(e.array) >= i {
			found = e
			break
		}
		walked = append(walked, e)
	}
	for _, w := range walked {
		s.anchors[anchorKey{w, i}] = found
	}
	s.walked = walked
	return found
}

// lastWith returns the last condition with fields in the array, or -1.
func (s *search) lastWith(array int) int {
	if j, ok := s.last[array]; ok {
		return j
	}
	j := len(s.paths) - 1
	for ; j >= 0; j-- {
		if _, ok := s.paths[j].arrays[array]; ok {
			break
		}
	}
	s.last[array] = j
	return j
}

func accepted(ms []matcher, v value, o Options) bool {
	for _, m := range ms {
		if m.accepts(v, o) {
			return true
		}
	}
	return false
}
