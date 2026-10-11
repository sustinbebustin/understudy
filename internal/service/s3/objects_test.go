package s3_test

import (
	"io"
	"testing"
	"testing/fstest"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// TestSnapshotKeepsObjectBodies runs a snapshot save and load the way the
// snapshot coordinator does: each bucket section names one blob reference
// per object version, so the archive embeds every body; after a reset whose
// dropped references are released, the load imports the archive's blobs,
// retains the references the sections name, prunes, and imports the state.
// Every version reads back its bytes, and a copy sharing a version's blob
// counts as its own reference (ARCHITECTURE.md 8).
func TestSnapshotKeepsObjectBodies(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("snap")
		h.enableVersioning("snap")
		first := *h.put("snap", "k", "one").VersionId
		second := *h.put("snap", "k", "two").VersionId
		copied := ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "snap", Key: "copy", CopySource: "snap/k?versionId=" + first})

		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		var named []string
		for _, sec := range sections {
			named = append(named, sec.Blobs...)
		}
		archive := fstest.MapFS{}
		for _, hex := range named {
			archive["sha256/"+hex] = &fstest.MapFile{Data: blobBytes(h, blobs, hex)}
		}
		if len(named) != 3 || len(archive) != 2 {
			t.Fatalf("sections name %d references to %d bodies, want 3 references (one per version) to 2 bodies", len(named), len(archive))
		}

		h.svc.Reset(kernel.ResetScope{})
		for _, hex := range named {
			if err := blobs.Release(parseRef(h, hex)); err != nil {
				t.Fatal(err)
			}
		}
		if refs := blobs.Refs(); len(refs) != 0 {
			t.Fatalf("blob store holds %v once the reset state's references are released, want nothing", refs)
		}

		if err := blobs.Import(h.Context(), archive); err != nil {
			t.Fatal(err)
		}
		for _, hex := range named {
			if err := blobs.Retain(parseRef(h, hex)); err != nil {
				t.Fatal(err)
			}
		}
		if err := blobs.Prune(); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct{ key, version, body string }{
			{"k", first, "one"}, {"k", second, "two"}, {"copy", *copied.VersionId, "one"},
		} {
			if got, _ := h.read(&s3api.GetObjectInput{Bucket: "snap", Key: want.key, VersionId: new(want.version)}); got != want.body {
				t.Fatalf("GetObject %s version %s after the load = %q, want %q", want.key, want.version, got, want.body)
			}
		}
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "snap", Key: "copy", VersionId: copied.VersionId})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "snap", Key: "k", VersionId: new(first)}); got != "one" {
			t.Fatalf("GetObject of the version the deleted copy shared = %q, want one", got)
		}
	})
}

// blobBytes reads the blob hex names whole.
func blobBytes(h *harness, blobs *blob.Store, hex string) []byte {
	h.Helper()
	r, err := blobs.Open(parseRef(h, hex))
	if err != nil {
		h.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		h.Fatal(err)
	}
	return data
}

func parseRef(h *harness, hex string) blob.Ref {
	h.Helper()
	ref, err := blob.ParseRef(hex)
	if err != nil {
		h.Fatal(err)
	}
	return ref
}
