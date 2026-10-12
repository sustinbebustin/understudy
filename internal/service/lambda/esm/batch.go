package esm

import (
	"bytes"
	"encoding/json"
	"slices"
)

// MaxPayload is the largest event a batch makes: the 6 MB synchronous
// Invoke limit, counted over the event's JSON as the function receives it
// (lambda-events.md 2.1).
const MaxPayload = 6_291_456

// Batch is the records of one invocation, in the order they were
// received, and the size of the event they make. It holds each message
// once.
type Batch struct {
	records []Record
	encoded [][]byte
	// encodedSize is the sum of the encoded records' lengths.
	encodedSize int
	// ids are the message ids of records.
	ids map[string]struct{}
}

// eventFrame is the event's JSON around its records: {"Records":[...]}.
const (
	eventOpen  = `{"Records":[`
	eventClose = `]}`
)

// Len returns the number of records.
func (b *Batch) Len() int { return len(b.records) }

// Size returns the length of Event's JSON.
func (b *Batch) Size() int {
	return sizeOf(len(b.records), b.encodedSize)
}

// sizeOf is the length of an event of n records encoded in encoded bytes:
// the frame, the records and the commas between them.
func sizeOf(n, encoded int) int {
	return len(eventOpen) + encoded + max(n-1, 0) + len(eventClose)
}

// Add appends r and reports true, or reports false when r would take the
// event past MaxPayload. The first record is always added, so a batch
// makes progress whatever its size; SQS caps a message at 1 MiB, far below
// the limit.
//
// A record of a message the batch holds already, received again because
// its visibility timeout ended while the batch waited for its window,
// replaces the held one: that one is removed first and r is appended, so
// the function never receives a message twice and the batch keeps the
// receipt handle that can still delete it. The held one is removed even
// when r is then refused, since its handle no longer reaches the message;
// any other refused record leaves the batch unchanged.
func (b *Batch) Add(r Record) (bool, error) {
	enc, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if _, held := b.ids[r.MessageID]; held {
		b.remove(r.MessageID)
	}
	if len(b.records) > 0 && sizeOf(len(b.records)+1, b.encodedSize+len(enc)) > MaxPayload {
		return false, nil
	}
	if b.ids == nil {
		b.ids = map[string]struct{}{}
	}
	b.records = append(b.records, r)
	b.encoded = append(b.encoded, enc)
	b.encodedSize += len(enc)
	b.ids[r.MessageID] = struct{}{}
	return true, nil
}

// Records returns the records in order. The caller must not change them.
func (b *Batch) Records() []Record { return b.records }

// MessageIDs returns the records' message ids in order.
func (b *Batch) MessageIDs() []string {
	out := make([]string, len(b.records))
	for i := range b.records {
		out[i] = b.records[i].MessageID
	}
	return out
}

// Event returns the invocation's payload: {"Records":[...]}.
func (b *Batch) Event() []byte {
	out := make([]byte, 0, b.Size())
	out = append(out, eventOpen...)
	out = append(out, bytes.Join(b.encoded, []byte(","))...)
	return append(out, eventClose...)
}

// remove drops the record of message id, which the batch holds.
func (b *Batch) remove(id string) {
	i := slices.IndexFunc(b.records, func(r Record) bool { return r.MessageID == id })
	b.encodedSize -= len(b.encoded[i])
	b.records = slices.Delete(b.records, i, i+1)
	b.encoded = slices.Delete(b.encoded, i, i+1)
	delete(b.ids, id)
}
