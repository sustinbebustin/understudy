package s3

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// delivering is add followed by a Deliver effect, as the notifications of
// commitObject raise one: in a kernel without a DeliverSink the write
// commits and the call then answers Internal.
func delivering(add objectWrite[committed]) objectWrite[committed] {
	return func(b *Bucket, w objectTx) (committed, []blob.Ref, *awserr.Error) {
		r, released, err := add(b, w)
		if err == nil {
			w.tx.Deliver(delivery.Request{Source: bucketARN(b.Name)})
		}
		return r, released, err
	}
}

// commitFixture is an S3 service over a blob store in a kernel without a
// DeliverSink, with calls made as the default account's root.
type commitFixture struct {
	k     *kerneltest.K
	s     *Service
	blobs *blob.Store
}

func (f commitFixture) call(op interface{ Descriptor() *registry.Operation }) *kernel.Call {
	k := f.k
	return k.Kernel.NewCall(kernel.ExternalCall{
		RequestID:    k.Kernel.IDs().RequestID(),
		Operation:    op.Descriptor().ID,
		Scope:        k.Scope,
		RegionSource: aws.CredentialScope{Region: k.Scope.Region},
		Principal:    identity.Root{Account: k.Scope.Account},
		Origin:       ref.External{Protocol: ref.RESTXML, Listener: ref.ListenerMain},
		Admitted:     k.Clock.Now(),
		Endpoint:     k.Endpoint,
	})
}

// must fails the test on err.
func (f commitFixture) must(err *awserr.Error) {
	f.k.Helper()
	if err != nil {
		f.k.Fatalf("%s: %s", err.ID.QueryCode, err.Message)
	}
}

func (f commitFixture) put(bucket, key, content string) {
	f.k.Helper()
	_, err := f.s.putObject(f.k.Context(), f.call(s3api.OpPutObject), &s3api.PutObjectInput{
		Bucket: bucket, Key: key, Body: stringBody(content), ContentLength: new(int64(len(content))),
	})
	f.must(err)
}

func (f commitFixture) get(bucket, key string) string {
	f.k.Helper()
	out, err := f.s.getObject(f.k.Context(), f.call(s3api.OpGetObject), &s3api.GetObjectInput{Bucket: bucket, Key: key})
	f.must(err)
	rc, rerr := out.Body.Range(0, out.Body.Size())
	if rerr != nil {
		f.k.Fatal(rerr)
	}
	defer func() { _ = rc.Close() }()
	b, rerr := io.ReadAll(rc)
	if rerr != nil {
		f.k.Fatal(rerr)
	}
	return string(b)
}

func (f commitFixture) remove(bucket, key string) {
	f.k.Helper()
	_, err := f.s.deleteObject(f.k.Context(), f.call(s3api.OpDeleteObject), &s3api.DeleteObjectInput{Bucket: bucket, Key: key})
	f.must(err)
}

// TestLandedWriteKeepsItsBody: kernel.Write commits a write before an
// effect of it fails, so a put or copy answered Internal after its version
// landed must leave the version its body. The put's new body and the
// copy's body, shared with its source, stay readable after the source is
// deleted; the body the put replaced is released; once every object is
// deleted the store is empty, so no reference was lost or leaked.
func TestLandedWriteKeepsItsBody(t *testing.T) {
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
		s := f.s
		_, cerr := kerneltest.Dispatch[s3api.CreateBucketInput, s3api.CreateBucketOutput](k.Context(), k, f.call(s3api.OpCreateBucket), &s3api.CreateBucketInput{Bucket: "landed"})
		f.must(cerr)
		f.put("landed", "k", "old")
		f.put("landed", "src", "shared")

		newRef, size, berr := blobs.Put(k.Context(), strings.NewReader("new"), 3)
		if berr != nil {
			k.Fatal(berr)
		}
		put := &Object{Blob: newRef, Size: size, Header: SystemMetadata{ContentType: defaultContentType}, StorageClass: s3api.StorageClassStandard}
		_, cerr = s.commitOwned(k.Context(), f.call(s3api.OpPutObject), at("landed", nil), put, delivering(s.addVersion("k", unconditional{}, put, notify.CreatedPut)))
		wantInternal(k.T, "put", cerr)

		src, serr := s.readSource(blobs, copySource{bucket: "landed", key: "src"}, nil, readConditions{}, k.Clock.Now().Wire())
		f.must(serr)
		cp := *src.version.Object
		_, cerr = s.commitOwned(k.Context(), f.call(s3api.OpCopyObject), at("landed", nil), &cp, delivering(s.addVersion("copy", unconditional{}, &cp, notify.CreatedCopy)))
		wantInternal(k.T, "copy", cerr)

		f.remove("landed", "src")
		if got := f.get("landed", "k"); got != "new" {
			k.Fatalf("GetObject of the put answered Internal = %q, want the new body", got)
		}
		if got := f.get("landed", "copy"); got != "shared" {
			k.Fatalf("GetObject of the copy answered Internal, after its source was deleted = %q, want the shared body", got)
		}
		if refs := blobs.Refs(); len(refs) != 2 {
			k.Fatalf("blob store holds %d bodies, want the put's and the copy's only, the replaced body released", len(refs))
		}
		f.remove("landed", "k")
		f.remove("landed", "copy")
		if refs := blobs.Refs(); len(refs) != 0 {
			k.Fatalf("blob store holds %v after every object was deleted, want nothing", refs)
		}
	}, kerneltest.Services(s3api.Service()))
}

// wantInternal fails the test unless err is the Internal answer of a write
// whose effect failed after it committed.
func wantInternal(t *testing.T, name string, err *awserr.Error) {
	t.Helper()
	if err == nil || err.ID.Status != http.StatusInternalServerError {
		t.Fatalf("%s with a refused delivery = %v, want the Internal answer", name, err)
	}
}

// stringBody is a request body of s.
type stringBody string

func (b stringBody) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(b))), nil
}

func (b stringBody) Len() (int64, bool) { return int64(len(b)), true }

func (stringBody) Trailer(string) (string, bool) { return "", false }
