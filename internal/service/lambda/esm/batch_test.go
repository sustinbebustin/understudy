package esm_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// Assembling a batch never takes the event past 6 MB, keeps the order the
// records were added in, holds a message received again once, at the
// place of its last receive, and its Event is the JSON the function
// receives: {"Records":[...]} with exactly the records held, of the size
// reported.
func TestBatchProperties(t *testing.T) {
	queue := queueARN(t, "arn:aws:sqs:us-east-1:000000000000:q")
	rapid.Check(t, func(t *rapid.T) {
		var b esm.Batch
		var added []string
		bodies := rapid.SliceOfN(rapid.IntRange(0, 1<<20), 1, 30).Draw(t, "bodySizes")
		for i, n := range bodies {
			id := "m" + strconv.Itoa(rapid.IntRange(0, 9).Draw(t, "message"+strconv.Itoa(i)))
			ok, err := b.Add(esm.NewRecord(&sqsapi.Message{MessageId: new(id), Body: new(strings.Repeat("a", n))}, queue))
			if err != nil {
				t.Fatal(err)
			}
			added = slices.DeleteFunc(added, func(held string) bool { return held == id })
			if ok {
				added = append(added, id)
			}
			if !ok && b.Len() == 0 {
				t.Fatal("an empty batch refused a record")
			}
		}
		event := b.Event()
		if len(event) != b.Size() {
			t.Fatalf("Event is %d bytes, Size says %d", len(event), b.Size())
		}
		if b.Len() > 1 && len(event) > esm.MaxPayload {
			t.Fatalf("a batch of %d records is %d bytes, more than %d", b.Len(), len(event), esm.MaxPayload)
		}
		var decoded struct{ Records []esm.Record }
		if err := json.Unmarshal(event, &decoded); err != nil {
			t.Fatalf("Event is not JSON: %v", err)
		}
		got := make([]string, len(decoded.Records))
		for i, r := range decoded.Records {
			got[i] = r.MessageID
		}
		if !slices.Equal(got, added) || !slices.Equal(b.MessageIDs(), added) {
			t.Fatalf("records %v, ids %v, want %v in order", got, b.MessageIDs(), added)
		}
	})
}

// A record that would take a full batch past 6 MB is refused and leaves it
// as it was.
func TestBatchRefusesPastLimit(t *testing.T) {
	queue := queueARN(t, "arn:aws:sqs:us-east-1:000000000000:q")
	var b esm.Batch
	body := strings.Repeat("a", 1<<20)
	for i := range 5 {
		if ok, err := b.Add(esm.NewRecord(&sqsapi.Message{MessageId: new(strings.Repeat("i", i+1)), Body: new(body)}, queue)); err != nil || !ok {
			t.Fatalf("record %d of 1 MiB refused (%v)", i, err)
		}
	}
	size := b.Size()
	if ok, err := b.Add(esm.NewRecord(&sqsapi.Message{MessageId: new("last"), Body: new(body + body)}, queue)); err != nil || ok {
		t.Fatalf("a 2 MiB record was added to %d bytes (%v)", size, err)
	}
	if b.Len() != 5 || b.Size() != size {
		t.Fatalf("the refused record changed the batch: %d records, %d bytes", b.Len(), b.Size())
	}
}

func TestEmptyBatchEvent(t *testing.T) {
	var b esm.Batch
	if got := string(b.Event()); got != `{"Records":[]}` || b.Size() != len(got) {
		t.Fatalf("empty batch event = %s (size %d)", got, b.Size())
	}
}
