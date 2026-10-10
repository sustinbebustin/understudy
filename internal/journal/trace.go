package journal

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Tree is a causal tree: an event and the events it caused, children in id
// order.
type Tree struct {
	Event    Event
	Children []*Tree
}

// Trace returns the causal tree that holds the event id, rooted at the
// trace's root event. Open calls appear with a Pending outcome. A retained
// event whose parent was evicted hangs from the root. When the root was
// evicted, the tree is rooted at id's topmost retained ancestor and holds
// the events that descend from it through retained parents, since where an
// event with an evicted parent belongs is then unknown. It reports false only
// when id is no longer retained.
func (j *Journal) Trace(id ref.EventID) (*Tree, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.trace(id)
}

// TraceByRequestID returns the causal tree of the API call with request id
// rid, as Trace does.
func (j *Journal) TraceByRequestID(rid ids.RequestID) (*Tree, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	calls := j.index.request[rid]
	if len(calls) == 0 {
		return nil, false
	}
	return j.trace(calls[0])
}

// trace builds the tree holding id. Called with mu held.
func (j *Journal) trace(id ref.EventID) (*Tree, bool) {
	e, ok := j.byID[id]
	if !ok {
		return nil, false
	}
	traceID := e.ev.Cause.Trace
	top, rooted := j.byID[traceID]
	if !rooted {
		// A parent's id is below its child's (the cause is checked when an
		// event is recorded), so this walk ends.
		top = e
		for {
			p, ok := j.byID[top.ev.Cause.Parent]
			if !ok || p.ev.Cause.Trace != traceID {
				break
			}
			top = p
		}
	}
	tree := &Tree{Event: top.ev}
	nodes := map[ref.EventID]*Tree{top.ev.ID: tree}
	// Members come in id order, so a parent is placed before its children.
	for _, member := range j.index.trace[traceID] {
		if member == top.ev.ID {
			continue
		}
		ev := j.byID[member].ev
		parent, ok := nodes[ev.Cause.Parent]
		switch {
		case ok:
		case rooted:
			parent = tree
		default:
			continue
		}
		node := &Tree{Event: ev}
		parent.Children = append(parent.Children, node)
		nodes[member] = node
	}
	return tree, true
}

// OutsideTrace is the position of a linked event outside the tree.
const OutsideTrace = "-"

// CanonicalTree is the normalized view of a Tree that two runs of the same
// workload agree on (ARCHITECTURE.md 12): event ids are replaced by tree
// positions, wall-clock and transport values are dropped and children are
// sorted by kind, operation and resource rather than by id.
type CanonicalTree struct {
	// Position is the path from the root: "0" for the root, "0.2" for its
	// third child.
	Position string `json:"position"`
	// Event has zero ID, Cause, Links and Wall; an APICall has zero Latency
	// and TraceHeader (an X-Amzn-Trace-Id holds epoch seconds and random
	// bits) and, when its origin is External, a zero RemoteAddr (the client's
	// ephemeral port); an Invocation has zero Init and Duration, and the
	// clock modes of a ClockChanged drop Running.Offset and
	// Scaled.AnchorWall.
	Event Event `json:"event"`
	// Links are the positions of the linked events, OutsideTrace for one
	// outside this tree.
	Links    []string        `json:"links,omitempty"`
	Children []CanonicalTree `json:"children,omitempty"`
}

// Canonical returns the normalized view of t. Siblings sort by kind,
// operation and resources, then by a color that no id shows in: a hash of
// the node's subtree and what its links point at, refined with the colors of
// its neighbors while siblings whose order would show still tie (see the
// package doc).
func (t *Tree) Canonical() CanonicalTree {
	byID := map[ref.EventID]*cnode{}
	root := canonicalNode(t, nil, byID)
	root.hashContent()
	root.hashColor(byID)
	nodes := slices.Collect(maps.Values(byID))
	for _, n := range nodes {
		n.resolveLinks(byID)
	}
	settle(root, nodes)
	positions := map[ref.EventID]string{}
	root.place("0", positions)
	return root.render(positions)
}

// digest is a node hash.
type digest = [sha256.Size]byte

// cnode is a tree with normalized events, before positions are known.
type cnode struct {
	id       ref.EventID
	links    []ref.EventID
	event    Event
	parent   *cnode
	depth    int
	children []*cnode
	prefix   string   // kind, operation and resources; siblings sort by it first
	base     digest   // prefix and the normalized event
	content  digest   // base and the content of the subtree, links ignored
	color    digest   // content plus links, refined by settle; siblings then sort by it
	out      []*cnode // per link, its target in the tree, or nil for one outside it
	in       []inLink // the links into n from events in the tree
	crossed  bool     // n's subtree holds an endpoint of a link to a non-ancestor
	position string
}

// inLink is the link at index of the event from.
type inLink struct {
	from  *cnode
	index int
}

func canonicalNode(t *Tree, parent *cnode, byID map[ref.EventID]*cnode) *cnode {
	n := &cnode{id: t.Event.ID, links: t.Event.Links, event: normalize(t.Event), parent: parent}
	if parent != nil {
		n.depth = parent.depth + 1
	}
	byID[n.id] = n
	var prefix strings.Builder
	prefix.WriteString(KindOf(n.event.Detail).String())
	prefix.WriteByte(0)
	if call, ok := n.event.Detail.(APICall); ok {
		prefix.WriteString(call.Op.String())
	}
	prefix.WriteByte(0)
	for _, a := range resourcesOf(n.event.Detail) {
		prefix.WriteString(a.String())
		prefix.WriteByte(' ')
	}
	n.prefix = prefix.String()
	h := sha256.New()
	h.Write([]byte(n.prefix))
	h.Write([]byte{0})
	// An event that cannot be encoded sorts by the fields above alone.
	if b, err := n.event.MarshalJSON(); err == nil {
		h.Write(b)
	}
	h.Sum(n.base[:0])
	for _, c := range t.Children {
		n.children = append(n.children, canonicalNode(c, n, byID))
	}
	return n
}

// hashContent sets content for the subtree, independent of child order.
func (n *cnode) hashContent() digest {
	parts := make([]digest, len(n.children))
	for i, c := range n.children {
		parts[i] = c.hashContent()
	}
	slices.SortFunc(parts, compareDigests)
	h := sha256.New()
	h.Write(n.base[:])
	for i := range parts {
		h.Write(parts[i][:])
	}
	h.Sum(n.content[:0])
	return n.content
}

// hashColor sets the first color for the subtree: base, what each link points
// at (an ancestor by how many levels up, another tree event by the climb to
// the common ancestor and the target's content, outside the tree otherwise)
// and the children's colors in sibling order. The first color settles most
// trees at once, since it covers whole subtrees in one bottom-up pass; no
// description depends on ids.
func (n *cnode) hashColor(byID map[ref.EventID]*cnode) {
	for _, c := range n.children {
		c.hashColor(byID)
	}
	slices.SortFunc(n.children, compareSiblings)
	h := sha256.New()
	h.Write(n.base[:])
	for _, l := range n.links {
		target, ok := byID[l]
		if !ok {
			h.Write([]byte(" " + OutsideTrace))
			continue
		}
		if up, down := climb(n, target); down == 0 {
			fmt.Fprintf(h, " ^%d", up)
		} else {
			fmt.Fprintf(h, " ~%d/%d=%x", up, down, target.content)
		}
	}
	// The descriptions above hold no '[', so it ends them.
	h.Write([]byte{'['})
	for _, c := range n.children {
		h.Write(c.color[:])
	}
	h.Sum(n.color[:0])
}

// resolveLinks records n's links at both ends and marks every subtree that
// holds an endpoint of a link to a non-ancestor as crossed, up to the root:
// the first color describes such a link only by its climb and the target's
// content, not by the branch the target sits under, so tied subtrees above
// the common ancestor may still differ in where it lands. A link to an
// ancestor of n marks none: the ancestor's position is the same however n
// and its kin are ordered.
func (n *cnode) resolveLinks(byID map[ref.EventID]*cnode) {
	n.out = make([]*cnode, len(n.links))
	for i, l := range n.links {
		target, ok := byID[l]
		if !ok {
			continue
		}
		n.out[i] = target
		target.in = append(target.in, inLink{from: n, index: i})
		if _, down := climb(n, target); down == 0 {
			continue
		}
		for m := n; m != nil; m = m.parent {
			m.crossed = true
		}
		for m := target; m != nil; m = m.parent {
			m.crossed = true
		}
	}
}

func compareDigests(a, b digest) int { return bytes.Compare(a[:], b[:]) }

func compareSiblings(a, b *cnode) int {
	return cmp.Or(strings.Compare(a.prefix, b.prefix), compareDigests(a.color, b.color))
}

// climb returns how many levels a and b each climb to their lowest common
// ancestor; down is 0 when b is an ancestor of a.
func climb(a, b *cnode) (up, down int) {
	for a.depth > b.depth {
		a, up = a.parent, up+1
	}
	for b.depth > a.depth {
		b, down = b.parent, down+1
	}
	for a != b {
		a, b, up, down = a.parent, b.parent, up+1, down+1
	}
	return up, down
}

// settle orders every node's children by prefix and color so that no id
// order shows, by color refinement with individualization. While some run
// of siblings ties and one of their subtrees holds an endpoint of a link to a
// non-ancestor, it recolors every node from its own color and its parent's,
// children's, link targets' and linkers' colors, until a round splits no
// color class; then it gives the first such sibling a color of its own and
// goes on. Each step splits a class, so it ends within len(nodes) steps, each
// a pass over the tree: a tree without such ties takes none, one with k
// interchangeable linked siblings takes about k. Tied siblings whose subtrees
// hold no endpoint of a link to a non-ancestor are identical subtrees that
// nothing links into or out of, so their order cannot show and they are left
// alone.
//
// When the tied siblings are interchangeable (some symmetry of the tree and
// its links swaps them), which one is individualized does not show. Siblings
// that refinement cannot separate yet no symmetry swaps keep id order; that
// takes the highly regular link patterns among identical events on which
// color refinement is known to fail, which recorded workloads are not
// expected to produce.
func settle(root *cnode, nodes []*cnode) {
	distinct := -1
	for {
		root.sortChildren()
		tie := root.firstTie()
		if tie == nil {
			return
		}
		if distinct < 0 {
			distinct = countDistinct(colorsOf(nodes))
		}
		next := refined(nodes)
		if d := countDistinct(next); d > distinct {
			for i, n := range nodes {
				n.color = next[i]
			}
			distinct = d
			continue
		}
		h := sha256.New()
		h.Write(tie.color[:])
		h.Write([]byte{'!'})
		h.Sum(tie.color[:0])
		distinct++
	}
}

func (n *cnode) sortChildren() {
	slices.SortStableFunc(n.children, compareSiblings)
	for _, c := range n.children {
		c.sortChildren()
	}
}

// firstTie returns, in placement order, the first sibling of a run that ties
// on prefix and color and holds a crossed sibling, or nil when none is left.
func (n *cnode) firstTie() *cnode {
	for start := 0; start < len(n.children); {
		end, crossed := start+1, n.children[start].crossed
		for end < len(n.children) && compareSiblings(n.children[start], n.children[end]) == 0 {
			crossed = crossed || n.children[end].crossed
			end++
		}
		if crossed && end-start > 1 {
			return n.children[start]
		}
		start = end
	}
	for _, c := range n.children {
		if tie := c.firstTie(); tie != nil {
			return tie
		}
	}
	return nil
}

// refined returns, per node, a hash of its color and its neighbors' colors.
// Children are in sibling order and tied ones have equal colors, so the
// order of nodes and ids shows nowhere.
func refined(nodes []*cnode) []digest {
	next := make([]digest, len(nodes))
	type incoming struct {
		index int
		color digest
	}
	for i, n := range nodes {
		h := sha256.New()
		h.Write(n.color[:])
		// Each tag but '<' is followed by a fixed-size color or nothing.
		if n.parent != nil {
			h.Write([]byte{'^'})
			h.Write(n.parent.color[:])
		}
		for _, c := range n.children {
			h.Write([]byte{'v'})
			h.Write(c.color[:])
		}
		for _, target := range n.out {
			if target == nil {
				h.Write([]byte(OutsideTrace))
				continue
			}
			h.Write([]byte{'>'})
			h.Write(target.color[:])
		}
		in := make([]incoming, len(n.in))
		for k, l := range n.in {
			in[k] = incoming{index: l.index, color: l.from.color}
		}
		slices.SortFunc(in, func(a, b incoming) int {
			return cmp.Or(cmp.Compare(a.index, b.index), compareDigests(a.color, b.color))
		})
		for _, l := range in {
			fmt.Fprintf(h, "<%d=", l.index)
			h.Write(l.color[:])
		}
		h.Sum(next[i][:0])
	}
	return next
}

func colorsOf(nodes []*cnode) []digest {
	colors := make([]digest, len(nodes))
	for i, n := range nodes {
		colors[i] = n.color
	}
	return colors
}

func countDistinct(colors []digest) int {
	seen := make(map[digest]struct{}, len(colors))
	for _, c := range colors {
		seen[c] = struct{}{}
	}
	return len(seen)
}

func (n *cnode) place(position string, positions map[ref.EventID]string) {
	n.position = position
	positions[n.id] = position
	for i, c := range n.children {
		c.place(position+"."+strconv.Itoa(i), positions)
	}
}

func (n *cnode) render(positions map[ref.EventID]string) CanonicalTree {
	out := CanonicalTree{Position: n.position, Event: n.event}
	for _, l := range n.links {
		p, ok := positions[l]
		if !ok {
			p = OutsideTrace
		}
		out.Links = append(out.Links, p)
	}
	for _, c := range n.children {
		out.Children = append(out.Children, c.render(positions))
	}
	return out
}

// normalize drops what differs between two runs of the same workload: ids,
// wall-clock values, trace headers and the caller's address, whose port is
// ephemeral.
func normalize(e Event) Event {
	e.ID, e.Cause, e.Links, e.Wall = 0, ref.Cause{}, nil, time.Time{}
	switch d := e.Detail.(type) {
	case APICall:
		d.Latency, d.TraceHeader = 0, ""
		if ext, ok := d.Origin.(ref.External); ok {
			ext.RemoteAddr = netip.AddrPort{}
			d.Origin = ext
		}
		e.Detail = d
	case Invocation:
		d.Init, d.Duration = 0, 0
		e.Detail = d
	case ClockChanged:
		d.From, d.To = normalizeMode(d.From), normalizeMode(d.To)
		e.Detail = d
	case Delivery, TimerFired, StateChange, FaultInjected, Advisory, Unclaimed, Gap:
	}
	return e
}

func normalizeMode(m clock.Mode) clock.Mode {
	switch m := m.(type) {
	case clock.Running:
		return clock.Running{}
	case clock.Scaled:
		m.AnchorWall = time.Time{}
		return m
	case clock.Frozen:
		return m
	}
	return m
}
