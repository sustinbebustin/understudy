package s3

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// TestCopyRecomputesCompositeChecksum: a copy's checksum is FULL_OBJECT
// (s3.md 7), so a multipart source's COMPOSITE checksum is recomputed over
// the bytes even when the algorithm stays the same, never relabelled.
// "hello" has the CRC32 NhCmhg== (PROBE E4).
func TestCopyRecomputesCompositeChecksum(t *testing.T) {
	blobs, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	}()
	ref, size, err := blobs.Put(t.Context(), strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	so := &Object{
		Blob: ref, Size: size, ETag: "5d41402abc4b2a76b9719d911017c592",
		Checksum: StoredChecksum{Algorithm: checksum.CRC32.String(), Value: "AAAAAA==-2", Type: s3api.ChecksumTypeComposite},
	}
	obj, aerr := (&Service{}).copied(blobs, so, copyRequest{}, &s3api.CopyObjectInput{}, Encryption{})
	if aerr != nil {
		t.Fatal(aerr)
	}
	want := StoredChecksum{Algorithm: checksum.CRC32.String(), Value: "NhCmhg==", Type: s3api.ChecksumTypeFullObject}
	if obj.Checksum != want {
		t.Fatalf("copied checksum = %+v, want %+v", obj.Checksum, want)
	}
}
