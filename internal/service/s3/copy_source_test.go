package s3

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// TestParseCopySource: x-amz-copy-source is decoded leniently (wire-rest.md
// 3.3): valid %XX sequences are decoded, any other '%' is the key's own, as
// the Go SDK sends a key unencoded; '+' stays literal; the slash between
// bucket and key may itself be escaped, as Terraform sends it.
func TestParseCopySource(t *testing.T) {
	for _, tc := range []struct {
		header, bucket, key string
		version             *string
	}{
		{"/bucket/test%percent", "bucket", "test%percent", nil},
		{"bucket/test%25percent", "bucket", "test%percent", nil},
		{"bucket%2Fdir%2Fa%20b.txt", "bucket", "dir/a b.txt", nil},
		{"bucket/a+b", "bucket", "a+b", nil},
		{"bucket/100%", "bucket", "100%", nil},
		{"bucket/%zz%4", "bucket", "%zz%4", nil},
		{"bucket/caf%C3%A9", "bucket", "café", nil},
		{"bucket/k?versionId=gS53zabD7XTvkrwbjMnXlBylVWetO8ym", "bucket", "k", new("gS53zabD7XTvkrwbjMnXlBylVWetO8ym")},
	} {
		got, err := parseCopySource(tc.header)
		switch {
		case err != nil:
			t.Errorf("%q: %s %s", tc.header, err.ID.QueryCode, err.Message)
		case got.bucket != tc.bucket || got.key != tc.key:
			t.Errorf("%q = bucket %q key %q, want %q %q", tc.header, got.bucket, got.key, tc.bucket, tc.key)
		case (got.versionID == nil) != (tc.version == nil) || got.versionID != nil && *got.versionID != *tc.version:
			t.Errorf("%q = version %v, want %v", tc.header, got.versionID, tc.version)
		}
	}
	for _, bad := range []string{"no-key", "/bucket/", "/", "%2Fkey"} {
		_, err := parseCopySource(bad)
		if err == nil || err.ID.QueryCode != "InvalidArgument" {
			t.Errorf("%q = %v, want InvalidArgument", bad, err)
		}
	}
}

// TestCopySourceOverLimit: a copy source over 5 GiB, which only a
// multipart upload makes, is 400 InvalidRequest naming the limit (s3.md 7),
// and the reference the source read took is released.
func TestCopySourceOverLimit(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		d := k.Deps(s3api.Service())
		d.Blob = blobs
		f := commitFixture{k: k, s: New(d, Options{}), blobs: blobs}
		_, cerr := kerneltest.Dispatch[s3api.CreateBucketInput, s3api.CreateBucketOutput](k.Context(), k, f.call(s3api.OpCreateBucket), &s3api.CreateBucketInput{Bucket: "limits"})
		f.must(cerr)
		ref, _, berr := blobs.Put(k.Context(), strings.NewReader("stands for 5 GiB"), -1)
		if berr != nil {
			k.Fatal(berr)
		}
		huge := &Object{Blob: ref, Size: maxSinglePut + 1, Header: SystemMetadata{ContentType: defaultContentType}, StorageClass: s3api.StorageClassStandard}
		_, cerr = f.s.commitOwned(k.Context(), f.call(s3api.OpPutObject), at("limits", nil), huge, f.s.addVersion("huge", unconditional{}, huge, "ObjectCreated:CompleteMultipartUpload"))
		f.must(cerr)

		_, cerr = f.s.copyObject(k.Context(), f.call(s3api.OpCopyObject), &s3api.CopyObjectInput{Bucket: "limits", Key: "copy", CopySource: "limits/huge"})
		if cerr == nil || cerr.ID.QueryCode != "InvalidRequest" || cerr.ID.Status != http.StatusBadRequest || cerr.Message != copySourceTooLargeMessage {
			k.Fatalf("CopyObject of a source over 5 GiB = %v, want 400 InvalidRequest %q", cerr, copySourceTooLargeMessage)
		}
		f.remove("limits", "huge")
		if refs := blobs.Refs(); len(refs) != 0 {
			k.Fatalf("blob store holds %v after the refused copy and the source's delete, want nothing", refs)
		}
	}, kerneltest.Services(s3api.Service()))
}
