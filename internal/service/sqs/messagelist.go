package sqs

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"iter"
	"slices"
)

// messageChunkSize is how many messages a chunk of Messages holds when it
// is sealed. It weighs the messages a change copies (one chunk) against
// the chunk pointers it copies (the chunk list) at the backlogs load
// tests reach.
const messageChunkSize = 32

// Messages are a queue's messages in enqueue order, which receives
// follow. Like the rest of the state they are a value (ARCHITECTURE.md
// 3.5), laid out so a working copy (lend) changes a message without
// copying the backlog. The oldest messages sit in sealed chunks of at most
// messageChunkSize, the newest in the tail. A send appends to the tail
// past the length the stored record sees, so it copies nothing; once the
// tail is full it becomes a chunk. Changing or removing a message copies
// the chunk list and the one chunk that holds it, unless the working copy
// made that chunk itself. Removals shrink chunks and drop empty ones;
// chunks never merge. Messages persist as one flat JSON array.
type Messages struct {
	chunks []*messageChunk
	tail   []Message
	// lent reports that the chunk list is the stored record's, and so are
	// the chunks, whatever their owner says.
	lent bool
	// tailLent reports that the tail's backing array is the stored
	// record's up to the length that record sees.
	tailLent bool
	// owner marks the chunks this value may change in place: those whose
	// owner is the same token. It is nil until the value is first lent, so
	// a value never lent owns every chunk it holds, all of them its own.
	owner *chunkOwner
}

// messageChunk is one sealed chunk of Messages, never empty.
type messageChunk struct {
	owner *chunkOwner
	msgs  []Message
}

// chunkOwner is a token that names one working copy; it has a size so
// that each token has its own address.
type chunkOwner struct{ _ byte }

// IsZero reports a queue without messages, which the state leaves out.
func (ms *Messages) IsZero() bool { return len(ms.chunks) == 0 && len(ms.tail) == 0 }

// MarshalJSONTo writes ms as one array of messages, oldest first.
func (ms *Messages) MarshalJSONTo(enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginArray); err != nil {
		return err
	}
	for _, m := range ms.all() {
		if err := json.MarshalEncode(enc, m); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndArray)
}

// UnmarshalJSONFrom reads what MarshalJSONTo writes, in sealed chunks
// and a tail.
func (ms *Messages) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var flat []Message
	if err := json.UnmarshalDecode(dec, &flat); err != nil {
		return err
	}
	*ms = Messages{}
	for len(flat) >= messageChunkSize {
		ms.chunks = append(ms.chunks, &messageChunk{msgs: flat[:messageChunkSize:messageChunkSize]})
		flat = flat[messageChunkSize:]
	}
	ms.tail = flat
	return nil
}

// lend marks ms as a working copy's, sharing its chunks and tail with the
// record it was copied from.
func (ms *Messages) lend() {
	ms.lent = true
	ms.tailLent = true
}

// len is how many messages there are.
func (ms *Messages) len() int {
	n := len(ms.tail)
	for _, c := range ms.chunks {
		n += len(c.msgs)
	}
	return n
}

// all yields each message with its index, oldest first, for reading. A
// change through change between steps is seen; a removal is not allowed.
func (ms *Messages) all() iter.Seq2[int, *Message] {
	return func(yield func(int, *Message) bool) {
		i := 0
		for k := range len(ms.chunks) {
			for j := range len(ms.chunks[k].msgs) {
				if !yield(i, &ms.chunks[k].msgs[j]) {
					return
				}
				i++
			}
		}
		for j := range len(ms.tail) {
			if !yield(i, &ms.tail[j]) {
				return
			}
			i++
		}
	}
}

// pick yields the messages at the ascending indices at, for reading.
func (ms *Messages) pick(at []int) iter.Seq2[int, *Message] {
	return func(yield func(int, *Message) bool) {
		next, start := 0, 0
		segment := func(msgs []Message) bool {
			for ; next < len(at) && at[next] < start+len(msgs); next++ {
				if !yield(at[next], &msgs[at[next]-start]) {
					return false
				}
			}
			start += len(msgs)
			return true
		}
		for _, c := range ms.chunks {
			if !segment(c.msgs) {
				return
			}
		}
		segment(ms.tail)
	}
}

// index returns the index of the first message f reports, or -1.
func (ms *Messages) index(f func(*Message) bool) int {
	for i, m := range ms.all() {
		if f(m) {
			return i
		}
	}
	return -1
}

// locate returns the chunk holding message i and its index there; chunk
// len(ms.chunks) is the tail.
func (ms *Messages) locate(i int) (chunk, at int) {
	for k, c := range ms.chunks {
		if i < len(c.msgs) {
			return k, i
		}
		i -= len(c.msgs)
	}
	return len(ms.chunks), i
}

// at returns message i for reading.
func (ms *Messages) at(i int) *Message {
	k, j := ms.locate(i)
	if k == len(ms.chunks) {
		return &ms.tail[j]
	}
	return &ms.chunks[k].msgs[j]
}

// change returns message i of a working copy for a change in place,
// copying the chunk that holds it first while that is not the copy's own.
func (ms *Messages) change(i int) *Message {
	k, j := ms.locate(i)
	if k == len(ms.chunks) {
		ms.ownTail()
		return &ms.tail[j]
	}
	ms.ownList()
	c := ms.chunks[k]
	if c.owner != ms.owner {
		c = &messageChunk{owner: ms.owner, msgs: slices.Clone(c.msgs)}
		ms.chunks[k] = c
	}
	return &c.msgs[j]
}

// push appends m and returns it, as stored, for reading.
func (ms *Messages) push(m Message) *Message {
	ms.tail = append(ms.tail, m)
	last := &ms.tail[len(ms.tail)-1]
	if len(ms.tail) == messageChunkSize {
		ms.seal()
	}
	return last
}

// seal turns the full tail into the newest chunk. A tail still the stored
// record's goes to a chunk nobody owns, which no write changes in place.
func (ms *Messages) seal() {
	ms.ownList()
	owner := ms.owner
	if ms.tailLent {
		owner = &chunkOwner{}
	}
	ms.chunks = append(ms.chunks, &messageChunk{owner: owner, msgs: ms.tail})
	ms.tail, ms.tailLent = nil, false
}

// ownList copies the chunk list of a working copy while it is the stored
// record's, and gives the copy the token of the chunks it goes on to make.
func (ms *Messages) ownList() {
	if ms.lent {
		ms.chunks = slices.Clone(ms.chunks)
		ms.owner = &chunkOwner{}
		ms.lent = false
	}
}

// ownTail copies the tail of a working copy while it is the stored
// record's.
func (ms *Messages) ownTail() {
	if ms.tailLent {
		ms.tail = slices.Clone(ms.tail)
		ms.tailLent = false
	}
}

// remove takes the messages at the ascending indices at out of a working
// copy in one pass. A chunk losing only its oldest messages is resliced
// rather than copied; any other chunk that loses messages is copied first
// unless it is the copy's own.
func (ms *Messages) remove(at []int) {
	if len(at) == 0 {
		return
	}
	start := 0
	// in returns the indices of at that fall in the next n messages,
	// relative to the first of them, and moves past them.
	in := func(n int) []int {
		k := 0
		for k < len(at) && at[k] < start+n {
			k++
		}
		local := make([]int, k)
		for i := range local {
			local[i] = at[i] - start
		}
		at, start = at[k:], start+n
		return local
	}
	if sealed := ms.len() - len(ms.tail); at[0] < sealed {
		ms.ownList()
		kept := ms.chunks[:0]
		for _, c := range ms.chunks {
			if c = ms.without(c, in(len(c.msgs))); c != nil {
				kept = append(kept, c)
			}
		}
		clear(ms.chunks[len(kept):])
		ms.chunks = kept
	} else {
		start = sealed
	}
	ms.removeFromTail(in(len(ms.tail)))
}

// without returns chunk c of a working copy, whose list it owns, without
// the messages at the ascending indices out, or nil when none is left.
func (ms *Messages) without(c *messageChunk, out []int) *messageChunk {
	owned := c.owner == ms.owner
	switch {
	case len(out) == 0:
		return c
	case len(out) == len(c.msgs):
		return nil
	case out[len(out)-1] == len(out)-1 && owned:
		clear(c.msgs[:len(out)])
		c.msgs = c.msgs[len(out):]
		return c
	case out[len(out)-1] == len(out)-1:
		return &messageChunk{owner: &chunkOwner{}, msgs: c.msgs[len(out):]}
	case owned:
		c.msgs = compact(c.msgs, out)
		return c
	default:
		return &messageChunk{owner: ms.owner, msgs: compact(slices.Clone(c.msgs), out)}
	}
}

// removeFromTail takes the messages at the ascending indices out of
// the tail of a working copy.
func (ms *Messages) removeFromTail(out []int) {
	switch {
	case len(out) == 0:
	case out[len(out)-1] == len(out)-1:
		if !ms.tailLent {
			clear(ms.tail[:len(out)])
		}
		ms.tail = ms.tail[len(out):]
	default:
		ms.ownTail()
		ms.tail = compact(ms.tail, out)
	}
}

// compact removes the messages at the ascending indices out from ms in
// place, clearing what is left past the new end.
func compact(ms []Message, out []int) []Message {
	kept, next := out[0], 0
	for i := out[0]; i < len(ms); i++ {
		if next < len(out) && out[next] == i {
			next++
			continue
		}
		ms[kept] = ms[i]
		kept++
	}
	clear(ms[kept:])
	return ms[:kept]
}
