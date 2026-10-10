package journal

import (
	"fmt"
	"net/netip"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// shape renders a tree as nested ids, for comparisons.
func shape(t *Tree) string {
	s := fmt.Sprint(t.Event.ID)
	if len(t.Children) == 0 {
		return s
	}
	parts := make([]string, len(t.Children))
	for i, c := range t.Children {
		parts[i] = shape(c)
	}
	return s + fmt.Sprint(parts)
}

func TestTraceFollowsPublishToDeliveryToSendToInvocation(t *testing.T) {
	j := newTestJournal(t, Options{})
	topic, queue, fn := arn("sns", "events"), arn("sqs", "orders"), arn("lambda", "function:worker")

	publish := mustBegin(t, j, Event{Scope: scope(), Detail: APICall{
		Op: ref.OperationID{Service: "sns", Name: "Publish"}, RequestID: "req-publish",
		Origin: ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerMain}, Resources: []aws.ARN{topic},
	}})

	deliver := mustAppend(t, j, Event{Cause: ref.Cause{Parent: publish}, Scope: scope(), Detail: Delivery{
		From: topic, To: queue, Mechanism: delivery.SNSToSQS, Attempt: 1, Outcome: Delivered{},
	}})

	send := mustBegin(t, j, callEvent(deliver))
	if err := j.End(send, OK{}, time.Millisecond, queue); err != nil {
		t.Fatal(err)
	}
	if err := j.End(publish, OK{}, 3*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	unrelated := mustAppend(t, j, timerEvent())
	invoke := mustAppend(t, j, Event{Cause: ref.Cause{Parent: send}, Links: []ref.EventID{unrelated}, Scope: scope(), Detail: Invocation{
		Function: fn, Version: "$LATEST", Outcome: InvocationOK{},
	}})

	want := fmt.Sprintf("%d[%d[%d[%d]]]", publish, deliver, send, invoke)
	for name, lookup := range map[string]func() (*Tree, bool){
		"Trace(root)":       func() (*Tree, bool) { return j.Trace(publish) },
		"Trace(leaf)":       func() (*Tree, bool) { return j.Trace(invoke) },
		"TraceByRequestID":  func() (*Tree, bool) { return j.TraceByRequestID("req-publish") },
		"Trace(middle hop)": func() (*Tree, bool) { return j.Trace(deliver) },
	} {
		tree, ok := lookup()
		if !ok || shape(tree) != want {
			t.Errorf("%s = %v, %v; want %s", name, tree, ok, want)
			continue
		}
		if tree.Children[0].Children[0].Children[0].Event.Cause.Trace != publish {
			t.Errorf("%s: the invocation's trace is not the publish", name)
		}
	}
	if tree, ok := j.Trace(unrelated); !ok || shape(tree) != fmt.Sprint(unrelated) {
		t.Errorf("Trace(unrelated) = %v, %v; want a tree of the timer alone", tree, ok)
	}
	if _, ok := j.Trace(999); ok {
		t.Error("Trace of an id never issued reported a tree")
	}
	if _, ok := j.TraceByRequestID("req-unknown"); ok {
		t.Error("TraceByRequestID of an unknown request id reported a tree")
	}
}

func TestTraceShowsCallsInFlightAsPending(t *testing.T) {
	j := newTestJournal(t, Options{})
	root := mustBegin(t, j, callEvent(0))
	child := mustBegin(t, j, callEvent(root))
	tree, ok := j.Trace(root)
	if !ok || shape(tree) != fmt.Sprintf("%d[%d]", root, child) {
		t.Fatalf("Trace = %v, %v; want the open root and child", tree, ok)
	}
	for _, e := range []Event{tree.Event, tree.Children[0].Event} {
		if call, ok := e.Detail.(APICall); !ok || call.Outcome != (Pending{}) {
			t.Errorf("open call %d has outcome %v, want Pending", e.ID, e.Detail)
		}
	}
}

func TestTraceHangsOrphansFromTheRoot(t *testing.T) {
	j := newTestJournal(t, Options{Capacity: 2})
	root := mustBegin(t, j, callEvent(0)) // open, so never evicted
	a := mustAppend(t, j, Event{Cause: ref.Cause{Parent: root}, Detail: TimerFired{}})
	b := mustAppend(t, j, Event{Cause: ref.Cause{Parent: a}, Detail: TimerFired{}})
	c := mustAppend(t, j, Event{Cause: ref.Cause{Parent: root}, Detail: TimerFired{}})
	tree, ok := j.Trace(root)
	if want := fmt.Sprintf("%d[%d %d]", root, b, c); !ok || shape(tree) != want {
		t.Fatalf("Trace after evicting %d = %v, %v; want %s", a, tree, ok, want)
	}
}

func TestTraceWithoutItsRootStartsAtTheTopRetainedAncestor(t *testing.T) {
	// The root Publish is evicted while the queued invocation it caused is
	// still held: "why didn't my Lambda run?" must still find it.
	j := newTestJournal(t, Options{Capacity: 4})
	publish := mustAppend(t, j, timerEvent())
	a := mustAppend(t, j, Event{Cause: ref.Cause{Parent: publish}, Detail: TimerFired{}})
	b := mustAppend(t, j, Event{Cause: ref.Cause{Parent: a}, Detail: TimerFired{}})
	send := mustAppend(t, j, Event{Cause: ref.Cause{Parent: publish}, Detail: TimerFired{}})
	invoke := mustAppend(t, j, Event{Cause: ref.Cause{Parent: send}, Detail: TimerFired{}})
	if _, ok := j.Trace(publish); ok {
		t.Fatalf("Trace(%d) of the evicted root reported a tree", publish)
	}
	for id, want := range map[ref.EventID]string{
		send:   fmt.Sprintf("%d[%d]", send, invoke),
		invoke: fmt.Sprintf("%d[%d]", send, invoke),
		b:      fmt.Sprintf("%d[%d]", a, b),
	} {
		if tree, ok := j.Trace(id); !ok || shape(tree) != want {
			t.Errorf("Trace(%d) after root %d was evicted = %v, %v; want %s, without the root's other subtree", id, publish, tree, ok, want)
		}
	}
}

func TestEventWhoseParentWasEvictedRootsItsOwnTrace(t *testing.T) {
	// A message enqueued long ago carries its enqueue event as the cause of
	// the invocation it feeds; that event may be gone by then.
	j := newTestJournal(t, Options{Capacity: 2})
	gone := mustAppend(t, j, timerEvent())
	mustAppend(t, j, timerEvent())
	mustAppend(t, j, timerEvent())
	orphan := mustAppend(t, j, Event{Cause: ref.Cause{Parent: gone}, Detail: TimerFired{}})
	tree, ok := j.Trace(orphan)
	if !ok || shape(tree) != fmt.Sprint(orphan) || tree.Event.Cause.Trace != orphan {
		t.Fatalf("Trace(%d) = %v, %v; want a tree of the event alone, tracing itself", orphan, tree, ok)
	}
}

// treeSpec is a workload that records one causal tree.
type treeSpec struct {
	parents []int // parents[i] < i; parents[0] is unused
	details []Detail
	ats     []clock.Instant
	links   [][]int // per node: indices of earlier nodes, or -1 for an event outside the tree
}

func genTreeSpec() *rapid.Generator[treeSpec] {
	return genTreeSpecOf(genTreeDetail(), genAt())
}

// genTieHeavyTreeSpec draws trees whose events all share the instant and draw
// their details from a pool of one or two, so most siblings are identical and
// only the links tell them apart.
func genTieHeavyTreeSpec() *rapid.Generator[treeSpec] {
	return rapid.Custom(func(t *rapid.T) treeSpec {
		pool := rapid.SliceOfN(genTreeDetail(), 1, 2).Draw(t, "pool")
		return genTreeSpecOf(rapid.SampledFrom(pool), rapid.Just(clock.Instant{})).Draw(t, "tree")
	})
}

func genTreeDetail() *rapid.Generator[Detail] {
	return genDetail().Filter(func(d Detail) bool { return KindOf(d) != KindGap })
}

func genTreeSpecOf(details *rapid.Generator[Detail], ats *rapid.Generator[clock.Instant]) *rapid.Generator[treeSpec] {
	return rapid.Custom(func(t *rapid.T) treeSpec {
		n := rapid.IntRange(1, 12).Draw(t, "nodes")
		s := treeSpec{parents: make([]int, n), details: make([]Detail, n), ats: make([]clock.Instant, n), links: make([][]int, n)}
		for i := range n {
			if i > 0 {
				s.parents[i] = rapid.IntRange(0, i-1).Draw(t, fmt.Sprintf("parent%d", i))
			}
			s.details[i] = details.Draw(t, fmt.Sprintf("detail%d", i))
			s.ats[i] = ats.Draw(t, fmt.Sprintf("at%d", i))
			choices := []int{-1}
			for a := range i {
				choices = append(choices, a)
			}
			if i > 0 {
				s.links[i] = rapid.SliceOfN(rapid.SampledFrom(choices), 0, 2).Draw(t, fmt.Sprintf("links%d", i))
			}
		}
		return s
	})
}

// record records the spec's events in a random order that puts each event
// after its parent and the events it links to, with random wall-clock
// durations, and returns the root's id.
func (s treeSpec) record(t *rapid.T, j *Journal, label string) ref.EventID {
	outside := mustAppend(t, j, timerEvent())
	ids := make([]ref.EventID, len(s.parents))
	recorded := func(i int) bool { return i < 0 || ids[i] != 0 }
	for range s.parents {
		var ready []int
		for i := range s.parents {
			if !recorded(i) && (i == 0 || recorded(s.parents[i])) && !slices.ContainsFunc(s.links[i], func(l int) bool { return !recorded(l) }) {
				ready = append(ready, i)
			}
		}
		i := rapid.SampledFrom(ready).Draw(t, label+" next")
		e := Event{At: s.ats[i], Scope: scope(), Detail: withWallNoise(t, s.details[i], label)}
		if i > 0 {
			e.Cause.Parent = ids[s.parents[i]]
		}
		for _, l := range s.links[i] {
			if l < 0 {
				e.Links = append(e.Links, outside)
			} else {
				e.Links = append(e.Links, ids[l])
			}
		}
		ids[i] = mustRecord(t, j, e)
	}
	return ids[0]
}

// withWallNoise redraws the wall-clock measurements of d.
func withWallNoise(t *rapid.T, d Detail, label string) Detail {
	dur := func(name string) time.Duration {
		return time.Duration(rapid.Int64Range(0, int64(time.Minute)).Draw(t, label+" "+name))
	}
	switch d := d.(type) {
	case APICall:
		d.Latency = dur("latency")
		return d
	case Invocation:
		d.Init, d.Duration = dur("init"), dur("duration")
		return d
	case ClockChanged:
		if r, ok := d.From.(clock.Running); ok {
			r.Offset = dur("offset")
			d.From = r
		}
		if s, ok := d.To.(clock.Scaled); ok {
			s.AnchorWall = wall0.Add(dur("anchor"))
			d.To = s
		}
		return d
	case Delivery, TimerFired, StateChange, FaultInjected, Advisory, Unclaimed, Gap:
		return d
	}
	return d
}

// canonicalAgrees records s into two journals with different ids, wall
// clocks and recording orders and checks that the canonical trees are equal.
func canonicalAgrees(t *rapid.T, s treeSpec) {
	a := newTestJournal(t, Options{})
	b := newJournalWithWall(t, Options{}, wall0.Add(time.Duration(rapid.Int64Range(1, int64(time.Hour)).Draw(t, "wall shift"))))
	if err := b.Seek(ref.EventID(rapid.Uint64Range(2, 1<<40).Draw(t, "id offset"))); err != nil {
		t.Fatal(err)
	}
	treeA, okA := a.Trace(s.record(t, a, "a"))
	treeB, okB := b.Trace(s.record(t, b, "b"))
	if !okA || !okB {
		t.Fatalf("Trace found the trees: %v, %v", okA, okB)
	}
	if ca, cb := treeA.Canonical(), treeB.Canonical(); !reflect.DeepEqual(ca, cb) {
		t.Fatalf("canonical trees differ\n a %+v\n b %+v", ca, cb)
	}
}

func TestCanonicalIgnoresIDsWallAndLatency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) { canonicalAgrees(t, genTreeSpec().Draw(t, "tree")) })
}

func TestCanonicalIgnoresIDsAmongIdenticalSiblings(t *testing.T) {
	// Ties between identical siblings are where an id order would show, and
	// the generator above rarely draws them. rapid runs 100 checks per Check,
	// so this runs it 100 times (10 under -short).
	rounds := 100
	if testing.Short() {
		rounds = 10
	}
	for i := range rounds {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) { canonicalAgrees(t, genTieHeavyTreeSpec().Draw(t, "tree")) })
		})
	}
}

func TestCanonicalOrdersIdenticalLeavesByWhoLinksToTheirLinkers(t *testing.T) {
	// Identical leaves L1 and L2 are told apart only by their linkers A and
	// B, which are identical but for Z linking to A.
	build := func(swap bool) CanonicalTree {
		j := newTestJournal(t, Options{})
		root := mustAppend(t, j, timerEvent())
		child := func(parent ref.EventID, links ...ref.EventID) ref.EventID {
			e := timerEvent()
			e.Cause.Parent, e.Links = parent, links
			return mustAppend(t, j, e)
		}
		l1, l2 := child(root), child(root)
		if swap {
			l1, l2 = l2, l1
		}
		m := child(root)
		a := child(m, l1)
		child(m, l2)
		child(root, a)
		tree, ok := j.Trace(root)
		if !ok {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	if a, b := build(false), build(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ when only the leaves' ids are swapped\n a %+v\n b %+v", a, b)
	}
}

func TestCanonicalOrdersIdenticalSubtreesByWhichBranchTheirLinksEnter(t *testing.T) {
	// A and A' each hold X{Z}, Y{Z} and W. W links to the Z under X in one
	// copy and to the Z under Y in the other, so A and A' look alike until
	// refinement tells which branch each link enters.
	timer := func(parent ref.EventID, id string, links ...ref.EventID) Event {
		return Event{
			Cause:  ref.Cause{Parent: parent},
			Links:  links,
			Scope:  scope(),
			Detail: TimerFired{Key: clock.TimerKey{Kind: "test", ID: id}},
		}
	}
	build := func(yBranchFirst bool) CanonicalTree {
		j := newTestJournal(t, Options{})
		root := mustAppend(t, j, timer(0, "root"))
		subtree := func(linkToY bool) {
			a := mustAppend(t, j, timer(root, "A"))
			x := mustAppend(t, j, timer(a, "X"))
			zx := mustAppend(t, j, timer(x, "Z"))
			y := mustAppend(t, j, timer(a, "Y"))
			zy := mustAppend(t, j, timer(y, "Z"))
			target := zx
			if linkToY {
				target = zy
			}
			mustAppend(t, j, timer(a, "W", target))
		}
		subtree(yBranchFirst)
		subtree(!yBranchFirst)
		tree, ok := j.Trace(root)
		if !ok {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	if a, b := build(false), build(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ when only the subtrees' recording order differs\n a %+v\n b %+v", a, b)
	}
}

func TestCanonicalOrdersIdenticalLinkTargetsByTheLinks(t *testing.T) {
	// An ESM invocation's parent is the first message of its batch and its
	// links are the others, which are often identical sends recorded in
	// either order.
	build := func(secondFirst bool) CanonicalTree {
		j := newTestJournal(t, Options{})
		publish := mustAppend(t, j, timerEvent())
		send := func() ref.EventID {
			return mustAppend(t, j, Event{Cause: ref.Cause{Parent: publish}, Detail: TimerFired{}})
		}
		first := send()
		second, third := send(), send()
		if secondFirst {
			third, second = second, third
		}
		mustAppend(t, j, Event{Cause: ref.Cause{Parent: first}, Links: []ref.EventID{second, third}, Detail: Invocation{
			Function: arn("lambda", "function:worker"), Outcome: InvocationOK{},
		}})
		tree, ok := j.Trace(publish)
		if !ok {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	a, b := build(false), build(true)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ with the batch's sends recorded in another order\n a %+v\n b %+v", a, b)
	}
}

func TestCanonicalOrdersIdenticalLinkersByTheirTargets(t *testing.T) {
	// Identical invocations E and F each link one of the identical sends C
	// and D; which send got the lower id must not show.
	build := func(swap bool) CanonicalTree {
		j := newTestJournal(t, Options{})
		root := mustAppend(t, j, timerEvent())
		c := mustAppend(t, j, Event{Cause: ref.Cause{Parent: root}, Detail: TimerFired{}})
		d := mustAppend(t, j, Event{Cause: ref.Cause{Parent: root}, Detail: TimerFired{}})
		if swap {
			c, d = d, c
		}
		invoke := func(target ref.EventID) {
			mustAppend(t, j, Event{Cause: ref.Cause{Parent: root}, Links: []ref.EventID{target}, Detail: Invocation{
				Function: arn("lambda", "function:worker"), Outcome: InvocationOK{},
			}})
		}
		invoke(c)
		invoke(d)
		tree, ok := j.Trace(root)
		if !ok {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	if a, b := build(false), build(true); !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ when only the sends' ids are swapped\n a %+v\n b %+v", a, b)
	}
}

func TestCanonicalOfADeepChainAllocatesLinearly(t *testing.T) {
	// A self-feeding queue-to-function loop records one long causal chain.
	j := newTestJournal(t, Options{})
	parent := mustAppend(t, j, timerEvent())
	root := parent
	for range 2000 {
		parent = mustAppend(t, j, Event{Cause: ref.Cause{Parent: parent}, Scope: scope(), Detail: TimerFired{Key: clock.TimerKey{Kind: "sqs.visibility", ID: "orders/m-1"}}})
	}
	tree, ok := j.Trace(root)
	if !ok {
		t.Fatal("Trace found no tree")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	tree.Canonical()
	runtime.ReadMemStats(&after)
	// Hashing every descendant's JSON at every ancestor took over 1 GiB here.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Fatalf("Canonical of a 2001-event chain allocated %d MiB, want under 64 MiB", allocated>>20)
	}
}

func TestCanonicalDropsTheCallersEphemeralAddress(t *testing.T) {
	build := func(port uint16) CanonicalTree {
		j := newTestJournal(t, Options{})
		root := mustRecord(t, j, Event{Scope: scope(), Detail: APICall{
			Op:      ref.OperationID{Service: "sqs", Name: "SendMessage"},
			Origin:  ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain, RemoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port), UserAgent: "aws-sdk-go-v2"},
			Outcome: OK{},
		}})
		tree, ok := j.Trace(root)
		if !ok {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	a, b := build(50111), build(50222)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ by the caller's port\n a %+v\n b %+v", a, b)
	}
	if call, ok := a.Event.Detail.(APICall); !ok || call.Origin != (ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain, UserAgent: "aws-sdk-go-v2"}) {
		t.Fatalf("canonical call = %s; want the origin without its address", detailName(a.Event.Detail))
	}
}

func TestCanonicalDropsTheTraceHeader(t *testing.T) {
	// An X-Amzn-Trace-Id holds epoch seconds and random bits, so two runs
	// of one workload carry different ones.
	build := func(header string) CanonicalTree {
		j := newTestJournal(t, Options{})
		e := callEvent(0)
		call, ok := e.Detail.(APICall)
		if !ok {
			t.Fatal("callEvent is not an APICall")
		}
		call.TraceHeader, call.Outcome = header, OK{}
		e.Detail = call
		tree, found := j.Trace(mustRecord(t, j, e))
		if !found {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	a := build("Root=1-5759e988-bd862e3fe1be46a994272793;Sampled=1")
	b := build("Root=1-6759e988-0123456789abcdef01234567;Sampled=1")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical trees differ by the trace header\n a %+v\n b %+v", a, b)
	}
	if call, ok := a.Event.Detail.(APICall); !ok || call.TraceHeader != "" {
		t.Fatalf("canonical call = %s; want an empty TraceHeader", detailName(a.Event.Detail))
	}
}

func TestCanonicalKeepsWhatDiffers(t *testing.T) {
	build := func(op string) CanonicalTree {
		j := newTestJournal(t, Options{})
		root := mustAppend(t, j, timerEvent())
		e := callEvent(root)
		call, ok := e.Detail.(APICall)
		if !ok {
			t.Fatal("callEvent is not an APICall")
		}
		call.Op.Name = op
		call.Outcome = OK{}
		e.Detail = call
		mustRecord(t, j, e)
		tree, found := j.Trace(root)
		if !found {
			t.Fatal("Trace found no tree")
		}
		return tree.Canonical()
	}
	if reflect.DeepEqual(build("SendMessage"), build("DeleteMessage")) {
		t.Fatal("canonical trees of different operations are equal")
	}
	c := build("SendMessage")
	if c.Position != "0" || len(c.Children) != 1 || c.Children[0].Position != "0.0" || c.Event.ID != 0 || !c.Event.Wall.IsZero() {
		t.Fatalf("canonical tree = %+v; want positions 0 and 0.0 with ids and wall dropped", c)
	}
}
