// Package journal records every API call and cross-service delivery as a
// causally linked Event, keeps them in a ring store with byte accounting, and
// serves queries and the resumable NDJSON stream.
//
// The kernel opens a call's APICall event with Begin at admission and records
// it with End; every other kind is recorded at once with Append. Recording
// order is the order events are recorded, not id order: a call's id comes
// from Begin, so a call that ends after its internal calls is recorded after
// them. Streams and the file follow recording order; Query and Trace order by
// id. The ring evicts the oldest recorded events while it holds more than
// Options.Capacity events or more than Options.MaxBytes estimated bytes.
// Begin stores a call input longer than MaxBytes/16 bytes as the marker
// {"$truncated":<bytes>} in every form, memory, streams and file alike, so
// one large body (journal.bodies with an S3 or Lambda payload) cannot push
// out every other event. An event still larger than MaxBytes on its own,
// which takes a MaxBytes smaller than its other text, is streamed and written
// to the file, then evicted with every older event: eviction keeps to
// recording order, which resumed streams count their Gap by.
//
// Decisions the architecture leaves open:
//
//   - Outcome has a Pending variant: a call between Begin and End is visible
//     to Query and Trace with it, so a long poll in flight and the parent of
//     a nested call are both findable. Streams and the file never see a
//     Pending call; they see it once, when it ends.
//   - End takes the ARNs the call touched as a trailing variadic argument,
//     because Call.Touch happens after Begin. They extend the event's
//     Resources in first-touch order without repeats.
//   - A Stream resumes after an event id by replaying, in recording order,
//     the retained events recorded after that event. When that event was
//     evicted, the stream starts with Gap{Missed: n}, n counting the evicted
//     events recorded after it, then replays everything retained. The ring
//     evicts in recording order, so the journal counts by remembering the
//     ids of the latest max(Capacity, 4096) evicted events in that order;
//     for an id older than that, or one skipped by Seek, n counts every
//     event evicted so far. A zero, open or future id starts the stream
//     live. Replay reads the ring a batch at a time, so a resumed consumer
//     that stops reading pins at most a batch; retained events evicted before
//     the replay reaches them arrive as one Gap. Each stream buffers up to
//     Options.StreamBuffer live events; events dropped while the buffer is
//     full, and every later one until the consumer reads up to them, reach
//     the consumer as one Gap in their place, so recording never waits. The
//     buffer and a replay batch are each also bounded to MaxBytes/8
//     estimated bytes (an event larger than that goes alone), so with
//     journal.bodies on a consumer that stops reading holds about MaxBytes/4
//     at most, not StreamBuffer large inputs.
//   - Append and Begin refuse, with a *RecordError and without using an id,
//     an event no consumer could read: an APICall given to Append, anything
//     else given to Begin, a Gap, an event that cannot be encoded or would
//     not decode back to itself (a nil Detail, a nil nested outcome or clock
//     mode, an enum outside its values, an ARN with an empty partition,
//     service or resource or a colon before the resource, an instant outside
//     years 0 to 9999), a Cause or link naming an id not issued yet, and a
//     link of 0, which names no event. End refuses touched ARNs the same
//     way. APICall.Resources never holds the zero ARN, which every other
//     part of the package treats as no resource.
//   - The journal stores every event in the form decoding produces, so a
//     recorded event equals its NDJSON round trip: empty Links, Resources and
//     Unclaimed.Reasons as nil, text with each byte that is not UTF-8
//     replaced by U+FFFD as encoding/json writes it (an unclaimed request's
//     raw host and path can hold such bytes), and a scaled clock's anchor
//     wall time in UTC without a monotonic reading.
//   - The journal stamps ID and Wall (UTC, no monotonic reading) on every
//     event and fills a zero At with emulator now. An event with a zero
//     Trace inherits its parent's when the parent is still held and gets its
//     own id otherwise, so a root, or a child whose parent was evicted, roots
//     its own trace.
//   - Trace(id) returns the whole tree holding the event id. A retained event
//     whose parent was evicted hangs from the root. When the root was
//     evicted, the tree is rooted at id's topmost retained ancestor and holds
//     the events descending from it through retained parents; it is missing
//     only when id itself was evicted.
//   - Canonical also zeroes Invocation Init and Duration and the wall-domain
//     fields of clock modes (Running.Offset, Scaled.AnchorWall), which are
//     wall-clock measurements like Latency, an External origin's RemoteAddr,
//     whose port is ephemeral, and APICall.TraceHeader, since an
//     X-Amzn-Trace-Id carries epoch seconds and random bits. Siblings that
//     tie on kind, operation and resources sort by a hash of their normalized
//     subtree and what their links point at (an ancestor by level, another
//     event in the tree by the climb to the common ancestor and its content,
//     or outside the tree), computed bottom-up so the cost grows with the
//     tree, not with its size times its depth. While siblings still tie and
//     one of their subtrees holds an endpoint of a link to an event that is
//     not its ancestor, so their order could show, the hashes are refined
//     with the hashes of each event's parent, children, link targets and
//     linkers (color refinement), and when that stops splitting, one tied
//     sibling is singled out and refinement goes on. Ties between
//     interchangeable siblings therefore never show ids. Known limit:
//     siblings that color refinement cannot separate but that no symmetry of
//     the tree swaps keep id order; that takes the highly regular link
//     patterns on which color refinement is known to fail. A tree with k
//     interchangeable linked siblings costs about k passes over the tree.
//   - Failed and Injected carry an ErrorIdentity holding the error's shape
//     id, since the journal does not import awserr.
//   - Redacted holds the redacted input as a compact JSON document with '<',
//     '>', '&', U+2028 and U+2029 escaped as json.Marshal escapes them, so it
//     round-trips through NDJSON unchanged.
//   - The Outcome filter classifies across kinds with Result (ok, error,
//     pending); see ResultOf. The Service filter takes a service's registry
//     id and its ARN namespace, which differ for some services (EventBridge
//     is "eventbridge" with ARNs under "events"): an API call matches by its
//     operation's service id, other kinds by the namespaces of their ARNs.
//     The journal does not import registry, so the admin layer fills both.
//   - The file follows the architecture's "no I/O under a lock" and "never
//     backpressure" rules: recording only queues an event, under the journal
//     lock and in recording order, and a writer goroutine encodes and writes
//     it with no lock held, so a slow or stalled disk delays no call. The
//     queue holds at most MaxBytes estimated bytes not yet written; an event
//     that would pass that stops the file with a queue *FileError wrapping
//     a *QueueFullError (event, size, bytes waiting, limit), the bound
//     on memory a stalled disk can take. The file rotates by renaming itself
//     to Path + ".1", replacing the previous one. Its first failure (queue,
//     encode, write or rotation) stops it, so the file always holds a prefix
//     of recording order with no holes; FileErr reports the failure while
//     the emulator runs and Close at shutdown, after waiting for the queue to
//     drain. An event recorded after Close is a write failure too. A file
//     that does not end in a newline when opened holds a record torn by a
//     crash; the journal ends that line before appending, and DecodeNDJSON
//     reads line by line, reporting a bad line by number and going on, so
//     the torn record costs only itself.
//   - Seek refuses to move below NextID, so an id is never issued twice. The
//     last id, math.MaxUint64, is never issued (Seek refuses it and recording
//     stops with IDsExhausted before it), so the next id never wraps to 0.
//   - Delivery.Mechanism is a delivery.Mechanism, so the package imports
//     delivery as the ADR 0007 amendment allows.
package journal
