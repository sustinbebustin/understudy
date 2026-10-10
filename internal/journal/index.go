package journal

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// idList is a set of event ids sorted ascending. Ids are allocated in
// ascending order and evicted roughly oldest first, so inserts land at the
// end and removals near the front in the common case: behind the few calls
// still open, which keep the lowest ids.
type idList []ref.EventID

// insert adds id, keeping the order; an id already present is kept once.
func (l idList) insert(id ref.EventID) idList {
	if n := len(l); n == 0 || l[n-1] < id {
		return append(l, id)
	}
	i, found := slices.BinarySearch(l, id)
	if found {
		return l
	}
	return slices.Insert(l, i, id)
}

// remove deletes id when present. It moves whichever side of id is shorter,
// so a removal near either end costs little however long l is.
func (l idList) remove(id ref.EventID) idList {
	i, found := slices.BinarySearch(l, id)
	switch {
	case !found:
		return l
	case i < len(l)/2:
		copy(l[1:i+1], l[:i])
		return l[1:]
	default:
		return slices.Delete(l, i, i+1)
	}
}

// from returns the ids not below id.
func (l idList) from(id ref.EventID) idList {
	i, _ := slices.BinarySearch(l, id)
	return l[i:]
}

// indexes maps the keys queries look events up by to the ids of the retained
// and open events that carry them.
type indexes struct {
	trace    map[ref.EventID]idList
	op       map[ref.OperationID]idList
	request  map[ids.RequestID]idList
	resource map[string]idList
}

func newIndexes() indexes {
	return indexes{
		trace:    map[ref.EventID]idList{},
		op:       map[ref.OperationID]idList{},
		request:  map[ids.RequestID]idList{},
		resource: map[string]idList{},
	}
}

// add indexes e under every key it carries.
func (x indexes) add(e Event) {
	if e.Cause.Trace != 0 {
		x.trace[e.Cause.Trace] = x.trace[e.Cause.Trace].insert(e.ID)
	}
	if call, ok := e.Detail.(APICall); ok {
		x.op[call.Op] = x.op[call.Op].insert(e.ID)
		if call.RequestID != "" {
			x.request[call.RequestID] = x.request[call.RequestID].insert(e.ID)
		}
	}
	for _, a := range resourcesOf(e.Detail) {
		k := a.String()
		x.resource[k] = x.resource[k].insert(e.ID)
	}
}

// drop removes e from every key it carries.
func (x indexes) drop(e Event) {
	dropFrom(x.trace, e.Cause.Trace, e.ID)
	if call, ok := e.Detail.(APICall); ok {
		dropFrom(x.op, call.Op, e.ID)
		dropFrom(x.request, call.RequestID, e.ID)
	}
	for _, a := range resourcesOf(e.Detail) {
		dropFrom(x.resource, a.String(), e.ID)
	}
}

func dropFrom[K comparable](m map[K]idList, k K, id ref.EventID) {
	l, ok := m[k]
	if !ok {
		return
	}
	if l = l.remove(id); len(l) == 0 {
		delete(m, k)
		return
	}
	m[k] = l
}
