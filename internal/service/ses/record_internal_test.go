package ses

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/blob"
)

// Evicting the oldest record hands back the blob it held, for the caller
// to release once the write commits (ses.md 11.2); a record without a raw
// message hands back none.
func TestEvictionReturnsTheEvictedBlob(t *testing.T) {
	ref, err := blob.ParseRef(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	var st state
	st.Messages = make([]record, MaxStoredMessages)
	st.Messages[0].Raw = &ref
	if got := st.record(record{MessageID: "new"}); !slices.Equal(got, []blob.Ref{ref}) {
		t.Fatalf("record evicting a raw message released %v, want [%s]", got, ref)
	}
	if got := st.record(record{MessageID: "newer"}); len(got) != 0 {
		t.Fatalf("record evicting a Simple message released %v, want none", got)
	}
	if st.Evicted != 2 || len(st.Messages) != MaxStoredMessages || st.Messages[len(st.Messages)-1].MessageID != "newer" {
		t.Fatalf("after two evictions: evicted %d, %d records, last %q", st.Evicted, len(st.Messages), st.Messages[len(st.Messages)-1].MessageID)
	}
	if refs := st.BlobRefs(); len(refs) != 0 {
		t.Fatalf("BlobRefs = %v after the raw record was evicted, want none", refs)
	}
}
