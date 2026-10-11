package s3

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/blob"
)

// TestCountingReaderRefusesOversizeBody: a body that runs past the size
// limit while it streams into the blob store, as an aws-chunked upload
// without x-amz-decoded-content-length can, is 400 EntityTooLarge with
// the size read so far, and nothing is stored (s3.md 5.2).
func TestCountingReaderRefusesOversizeBody(t *testing.T) {
	blobs, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	}()
	_, _, perr := blobs.Put(t.Context(), &limitReader{r: strings.NewReader("abcdef"), limit: 3}, -1)
	if perr == nil {
		t.Fatal("a body over the limit was stored")
	}
	got := bodyError(t.Context(), perr)
	if got.ID.QueryCode != "EntityTooLarge" || got.ID.Status != 400 {
		t.Fatalf("body over the limit = %s %d (%s), want 400 EntityTooLarge", got.ID.QueryCode, got.ID.Status, got.Message)
	}
	fields := map[string]string{}
	for _, f := range got.Fields {
		fields[f.Name] = f.Value
	}
	if fields["MaxSizeAllowed"] != "5368709120" || fields["ProposedSize"] != "6" {
		t.Fatalf("EntityTooLarge elements = %v, want MaxSizeAllowed 5368709120 and ProposedSize 6", fields)
	}
	if refs := blobs.Refs(); len(refs) != 0 {
		t.Fatalf("blob store holds %v after a refused body, want nothing", refs)
	}
}
