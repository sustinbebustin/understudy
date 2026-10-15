package s3_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // S3's ETag is an MD5; the test recomputes it
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awschunked"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/kms"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// runObjects runs fn against a fresh S3 service with a blob store whose
// bodies above 1 MiB spill to disk, beside a KMS service for SSE-KMS.
func runObjects(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runObjectsWith(t, func(h *harness, _ *blob.Store) { fn(h) })
}

// runObjectsWith is runObjects that also hands fn the blob store.
func runObjectsWith(t *testing.T, fn func(h *harness, blobs *blob.Store)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 1<<20)
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
		logs := &bytes.Buffer{}
		d.Log = slog.New(slog.NewTextHandler(logs, nil))
		kms.New(k.Deps(kmsapi.Service()), kms.Options{})
		fn(&harness{K: k, svc: s3.New(d, s3.Options{}), logs: logs}, blobs)
	}, kerneltest.Services(s3api.Service(), kmsapi.Service()))
}

// body is a request body as the edge hands it to a handler: a reader, the
// declared length, and HTTP trailers.
type body struct {
	r        io.Reader
	n        int64
	trailers map[string]string
}

func bytesBody(b string) *body { return &body{r: strings.NewReader(b), n: int64(len(b))} }

func (b *body) Open() (io.ReadCloser, error) { return io.NopCloser(b.r), nil }

func (b *body) Len() (int64, bool) { return b.n, b.n >= 0 }

func (b *body) Trailer(name string) (string, bool) {
	v, ok := b.trailers[strings.ToLower(name)]
	return v, ok
}

// unreadable is a body whose first read fails the test: the handler must
// answer before it pulls the body.
type unreadable struct{ t *testing.T }

func (u unreadable) Read([]byte) (int, error) {
	u.t.Error("the handler read the body of a request it had to refuse first")
	return 0, io.EOF
}

// withHeader runs op with in as the default account's root in us-east-1,
// from a request that carried header.
func withHeader[In, Out any](h *harness, op kernel.Op[In, Out], header http.Header, in *In) (*Out, *awserr.Error) {
	return verifiedWith(h, op, header, nil, in)
}

// verifiedWith is withHeader for a request whose credentials the edge
// verified as verified (sigv4 verify); nil is a request it did not verify.
func verifiedWith[In, Out any](h *harness, op kernel.Op[In, Out], header http.Header, verified auth.Auth, in *In) (*Out, *awserr.Error) {
	c := h.Kernel.NewCall(kernel.ExternalCall{
		Verified:     verified,
		RequestID:    h.Kernel.IDs().RequestID(),
		Operation:    op.Descriptor().ID,
		Scope:        aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		RegionSource: aws.CredentialScope{Region: aws.DefaultRegion()},
		Principal:    identity.Root{Account: aws.DefaultAccount()},
		Origin:       ref.External{Protocol: ref.RESTXML, Listener: ref.ListenerMain},
		Admitted:     h.Clock.Now(),
		Endpoint:     kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
		Header:       header,
	})
	return kerneltest.Dispatch[In, Out](h.Context(), h.K, c, in)
}

// put writes content to bucket/key and fails the test on an error.
func (h *harness) put(bucket, key, content string) *s3api.PutObjectOutput {
	h.Helper()
	return ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: bucket, Key: key, Body: bytesBody(content)})
}

// read reads bucket/key whole and fails the test on an error.
func (h *harness) read(in *s3api.GetObjectInput) (string, *s3api.GetObjectOutput) {
	h.Helper()
	out := ok(h, who{}, s3api.OpGetObject, in)
	rc, err := out.Body.Range(0, out.Body.Size())
	if err != nil {
		h.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		h.Fatal(err)
	}
	return string(b), out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

// TestPutObjectRoundTrip: the bytes come back; the ETag is the quoted MD5;
// with no checksum sent S3 stores and reports CRC64NVME (s3.md 6.2); the
// default content type is binary/octet-stream and SSE-S3 is always
// reported; an unversioned bucket reports no version id (s3.md 5.2 to
// 5.4).
func TestPutObjectRoundTrip(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("objects")
		out := h.put("objects", "a/b.txt", "hi\n")
		if *out.ETag != `"`+md5hex("hi\n")+`"` || out.VersionId != nil {
			t.Fatalf("PutObject = ETag %s, version %v; want the quoted MD5 and no version id", *out.ETag, out.VersionId)
		}
		if out.Size != nil {
			t.Fatalf("PutObject reports x-amz-object-size %d; S3 sends it only for appends to directory buckets", *out.Size)
		}
		if out.ChecksumCRC64NVME == nil || *out.ChecksumCRC64NVME != "JYwwIoi0PuE=" || *out.ChecksumType != s3api.ChecksumTypeFullObject {
			t.Fatalf("PutObject checksum = %v %v, want CRC64NVME JYwwIoi0PuE= FULL_OBJECT", out.ChecksumCRC64NVME, out.ChecksumType)
		}
		if *out.ServerSideEncryption != s3api.ServerSideEncryptionAes256 {
			t.Fatalf("PutObject SSE = %s, want AES256", *out.ServerSideEncryption)
		}
		got, get := h.read(&s3api.GetObjectInput{Bucket: "objects", Key: "a/b.txt", ChecksumMode: new(s3api.ChecksumModeEnabled)})
		if got != "hi\n" || *get.ContentType != "binary/octet-stream" || *get.ContentLength != 3 || *get.ChecksumCRC64NVME != "JYwwIoi0PuE=" {
			t.Fatalf("GetObject = %q, %s, %d, %v", got, *get.ContentType, *get.ContentLength, get.ChecksumCRC64NVME)
		}
		if get.StorageClass != nil || get.TagCount != nil {
			t.Fatalf("GetObject of a STANDARD object without tags reports class %v and tag count %v; S3 omits both", get.StorageClass, get.TagCount)
		}
	})
}

// TestPutObjectHeaderChecksum: a CRC32 header is verified and stored
// ("hello" is NhCmhg==, PROBE E4); a wrong one is BadDigest, a malformed
// one InvalidRequest, two of them InvalidRequest (s3.md 6.2).
func TestPutObjectHeaderChecksum(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("sums")
		out := ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "sums", Key: "k", Body: bytesBody("hello"), ChecksumCRC32: new("NhCmhg==")})
		if out.ChecksumCRC32 == nil || *out.ChecksumCRC32 != "NhCmhg==" || out.ChecksumCRC64NVME != nil {
			t.Fatalf("PutObject with CRC32 reports %v / %v, want CRC32 NhCmhg== only", out.ChecksumCRC32, out.ChecksumCRC64NVME)
		}
		// The Content-MD5 BadDigest's elements, calculated first, for a
		// Content-MD5 other than "hello"'s
		// (testdata/parity/s3/R08/04-checksum-plus-wrong-content-md5.json).
		wrongMD5 := "eV8yArF8trw9S3cdjGyerw=="
		md5Fields := []awserr.Field{{Name: "CalculatedDigest", Value: "XUFAKrxLKna5cZ2REBfFkg=="}, {Name: "ExpectedDigest", Value: "795f3202b17cb6bc3d4b771d8c6c9eaf"}}
		for _, tc := range []struct {
			name   string
			in     s3api.PutObjectInput
			code   string
			msg    string
			fields []awserr.Field
		}{
			{"wrong CRC32", s3api.PutObjectInput{ChecksumCRC32: new("AAAAAA==")}, "BadDigest", "The CRC32 you specified did not match the calculated checksum.", nil},
			{"malformed CRC32", s3api.PutObjectInput{ChecksumCRC32: new("nope")}, "InvalidRequest", "Value for x-amz-checksum-crc32 header is invalid.", nil},
			{
				"two checksums",
				s3api.PutObjectInput{ChecksumCRC32: new("NhCmhg=="), ChecksumSHA1: new("qvTGHdzF6KLavt4PO0gs2a6pQ00=")},
				"InvalidRequest",
				"Expecting a single x-amz-checksum- header. Multiple checksum Types are not allowed.",
				nil,
			},
			{
				"algorithm without a value",
				s3api.PutObjectInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmSha256)},
				"InvalidRequest",
				"x-amz-sdk-checksum-algorithm specified, but no corresponding x-amz-checksum-* or x-amz-trailer headers were found.",
				nil,
			},
			{"Content-MD5 not base64", s3api.PutObjectInput{ContentMD5: new("__invalid__")}, "InvalidDigest", "The Content-MD5 you specified was invalid.", nil},
			{
				"Content-MD5 differs",
				s3api.PutObjectInput{ContentMD5: new(base64.StdEncoding.EncodeToString(make([]byte, 16)))},
				"BadDigest",
				"The Content-MD5 you specified did not match what we received.",
				nil,
			},
			{
				"right checksum, Content-MD5 differs",
				s3api.PutObjectInput{ChecksumXXHASH64: new("JseCfYifbaM="), ContentMD5: new(wrongMD5)},
				"BadDigest", "The Content-MD5 you specified did not match what we received.", md5Fields,
			},
			{
				// Content-MD5 wins
				// (testdata/parity/s3/R08/04-wrong-checksum-plus-wrong-content-md5.json).
				"checksum and Content-MD5 both differ",
				s3api.PutObjectInput{ChecksumXXHASH64: new("MCcs6qbmYlo="), ContentMD5: new(wrongMD5)},
				"BadDigest", "The Content-MD5 you specified did not match what we received.", md5Fields,
			},
		} {
			in := tc.in
			in.Bucket, in.Key, in.Body = "sums", "bad", bytesBody("hello")
			_, err := as(h, who{}, s3api.OpPutObject, &in)
			if err == nil || err.ID.QueryCode != tc.code || err.Message != tc.msg {
				t.Errorf("%s: got %v, want %s %q", tc.name, err, tc.code, tc.msg)
				continue
			}
			if tc.fields != nil && !slices.Equal(err.Fields, tc.fields) {
				t.Errorf("%s: elements %v, want %v", tc.name, err.Fields, tc.fields)
			}
		}
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "sums", Key: "bad"}); err == nil {
			t.Fatal("a refused PutObject left an object behind")
		}
	})
}

// TestPutObjectToMissingBucketReadsNoBody: the bucket is checked before the
// body is pulled, so net/http never sends 100 Continue (s3.md 6.3).
func TestPutObjectToMissingBucketReadsNoBody(t *testing.T) {
	runObjects(t, func(h *harness) {
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "nowhere", Key: "k", Body: &body{r: unreadable{t: h.T}, n: 5}})
		wantCode(t, err, "NoSuchBucket", http.StatusNotFound)
	})
}

// TestPutObjectWithoutLengthIsRefused: a body of unknown length without
// aws-chunked framing (HTTP chunked transfer coding) is 411
// MissingContentLength before the body is read (s3.md 5.2).
func TestPutObjectWithoutLengthIsRefused(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("lengths")
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "lengths", Key: "k", Body: &body{r: unreadable{t: h.T}, n: -1}})
		wantCode(t, err, "MissingContentLength", http.StatusLengthRequired)
		if err.Message != "You must provide the Content-Length HTTP header." {
			t.Fatalf("MissingContentLength message = %q", err.Message)
		}
	})
}

// TestPutObjectDeclaredLength: a write's length is its Content-Length when
// set, even over a body of unknown length; else the body's own length; an
// input built without a body, as an SDK client can send, writes an empty
// object.
func TestPutObjectDeclaredLength(t *testing.T) {
	cases := []struct {
		name   string
		length *int64
		body   func() *body
		want   string
	}{
		{name: "no body, no Content-Length", want: ""},
		{name: "no body, Content-Length 0", length: new(int64(0)), want: ""},
		{name: "empty body, Content-Length 0", length: new(int64(0)), body: func() *body { return bytesBody("") }, want: ""},
		{name: "body of known length, no Content-Length", body: func() *body { return bytesBody("abc") }, want: "abc"},
		{
			name: "body of unknown length, Content-Length set", length: new(int64(3)),
			body: func() *body { return &body{r: strings.NewReader("abc"), n: -1} }, want: "abc",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runObjects(t, func(h *harness) {
				h.createBucket("lengths")
				in := &s3api.PutObjectInput{Bucket: "lengths", Key: "k", ContentLength: tc.length}
				if tc.body != nil {
					in.Body = tc.body()
				}
				out := ok(h, who{}, s3api.OpPutObject, in)
				if *out.ETag != `"`+md5hex(tc.want)+`"` {
					t.Fatalf("PutObject ETag = %s, want the quoted MD5 of %q", *out.ETag, tc.want)
				}
				got, get := h.read(&s3api.GetObjectInput{Bucket: "lengths", Key: "k"})
				if got != tc.want || *get.ContentLength != int64(len(tc.want)) {
					t.Fatalf("GetObject = %q, Content-Length %d; want %q", got, *get.ContentLength, tc.want)
				}
			})
		})
	}
}

// TestObjectACLsOnOwnerEnforcedBuckets: while ownership is
// BucketOwnerEnforced, the default, PutObject and CopyObject refuse a
// canned ACL other than bucket-owner-full-control and any grant header with
// 400 AccessControlListNotSupported, before the body is read; with ACLs
// enabled the write succeeds (s3.md 4.2.4, 5.2).
func TestObjectACLsOnOwnerEnforcedBuckets(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("enforced")
		h.put("enforced", "src", "v")
		for name, in := range map[string]s3api.PutObjectInput{
			"public-read": {ACL: new(s3api.ObjectCannedACLPublicRead)},
			"private":     {ACL: new(s3api.ObjectCannedACLPrivate)},
			"grant":       {GrantRead: new(`uri="http://acs.amazonaws.com/groups/global/AllUsers"`)},
		} {
			in.Bucket, in.Key, in.Body = "enforced", "k", &body{r: unreadable{t: h.T}, n: 1}
			_, err := as(h, who{}, s3api.OpPutObject, &in)
			if err == nil || err.ID.QueryCode != "AccessControlListNotSupported" || err.ID.Status != http.StatusBadRequest {
				t.Errorf("PutObject with %s = %v, want 400 AccessControlListNotSupported", name, err)
			}
		}
		_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "enforced", Key: "copy", CopySource: "enforced/src", ACL: new(s3api.ObjectCannedACLPublicRead)})
		wantCode(t, err, "AccessControlListNotSupported", http.StatusBadRequest)
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "enforced", Key: "k", Body: bytesBody("v"), ACL: new(s3api.ObjectCannedACLBucketOwnerFullControl)})

		h.createBucket("writer")
		ok(h, who{}, s3api.OpPutBucketOwnershipControls, &s3api.PutBucketOwnershipControlsInput{
			Bucket: "writer", OwnershipControls: s3api.OwnershipControls{Rules: []s3api.OwnershipControlsRule{{ObjectOwnership: s3api.ObjectOwnershipObjectWriter}}},
		})
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "writer", Key: "k", Body: bytesBody("v"), ACL: new(s3api.ObjectCannedACLPublicRead)})
	})
}

// chunkedHeader is the header of an aws-chunked upload with an unsigned
// CRC32 trailer of decoded bytes, as the JS SDK sends it (PROBE E7).
func chunkedHeader(decoded int) http.Header {
	return http.Header{
		"X-Amz-Content-Sha256":         {"STREAMING-UNSIGNED-PAYLOAD-TRAILER"},
		"X-Amz-Trailer":                {"x-amz-checksum-crc32"},
		"X-Amz-Decoded-Content-Length": {strconv.Itoa(decoded)},
	}
}

// chunked frames data as an unsigned-trailer aws-chunked body ending with
// the CRC32 trailer value.
func chunked(t *testing.T, data, crc32 string) string {
	t.Helper()
	return chunkedWith(t, awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: crc32}, data)
}

// chunkedWith frames chunks, one aws-chunked chunk each, as an
// unsigned-trailer body ending with trailer.
func chunkedWith(t *testing.T, trailer awschunked.Trailer, chunks ...string) string {
	t.Helper()
	var b bytes.Buffer
	w := awschunked.NewWriter(&b, awschunked.UnsignedTrailer, awschunked.WriterOptions{})
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finish(trailer); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// chunkSigner signs aws-chunked chunks and trailers as a client holding
// secret does for a, the reference signer of awschunked's tests
// (sigv4-checksums-endpoints.md 3.3).
func chunkSigner(a auth.SigV4Header, secret string) awschunked.SignFunc {
	mac := func(key []byte, data string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(data))
		return m.Sum(nil)
	}
	c := a.Credential
	key := mac(mac(mac(mac([]byte("AWS4"+secret), c.Date), c.Region.String()), c.Service), "aws4_request")
	stamp, scope := a.Time.UTC().Format("20060102T150405Z"), c.Date+"/"+c.Region.String()+"/"+c.Service+"/aws4_request"
	empty := sha256.Sum256(nil)
	return func(prev string, signed awschunked.SignedPayload) string {
		sts := "AWS4-HMAC-SHA256-TRAILER\n" + stamp + "\n" + scope + "\n" + prev + "\n" + hex.EncodeToString(signed.SHA256[:])
		if signed.Kind == awschunked.ChunkSigned {
			sts = "AWS4-HMAC-SHA256-PAYLOAD\n" + stamp + "\n" + scope + "\n" + prev + "\n" + hex.EncodeToString(empty[:]) + "\n" + hex.EncodeToString(signed.SHA256[:])
		}
		return hex.EncodeToString(mac(key, sts))
	}
}

// TestSignedAWSChunkedUploadsVerifyEachSignature: under sigv4 verify, a
// signed aws-chunked body's chunk and trailer signatures are verified
// against the secret of the key the edge verified the header with, and a
// tampered chunk or trailer is SignatureDoesNotMatch with nothing stored;
// without a verified header (sigv4 parse) the signatures are not checked.
func TestSignedAWSChunkedUploadsVerifyEachSignature(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("signed")
		header := auth.SigV4Header{
			Credential:    auth.Credential{Key: "test", Date: "20261013", Region: aws.DefaultRegion(), Service: "s3"},
			SignedHeaders: []string{"host", "x-amz-content-sha256", "x-amz-date"},
			Signature:     strings.Repeat("ab", 32),
			PayloadHash:   auth.StreamingSigned{},
			Time:          time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		}
		frame := func(mode awschunked.Mode, secret string, trailers ...awschunked.Trailer) string {
			var b bytes.Buffer
			w := awschunked.NewWriter(&b, mode, awschunked.WriterOptions{Seed: header.Signature, Sign: chunkSigner(header, secret)})
			if _, err := w.Write([]byte("streamed-body")); err != nil {
				t.Fatal(err)
			}
			if err := w.Finish(trailers...); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
		crc := awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: "Nyb53Q=="}
		signedHeader := http.Header{"X-Amz-Content-Sha256": {"STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}, "X-Amz-Decoded-Content-Length": {"13"}}
		trailerHeader := http.Header{
			"X-Amz-Content-Sha256": {"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"}, "X-Amz-Trailer": {"x-amz-checksum-crc32"},
			"X-Amz-Decoded-Content-Length": {"13"},
		}
		in := func(key, wire string) *s3api.PutObjectInput {
			return &s3api.PutObjectInput{Bucket: "signed", Key: key, ContentEncoding: new("aws-chunked"), Body: bytesBody(wire)}
		}
		// The edge verifies the payload hash the request sent, so the
		// trailer requests' verified credentials name the trailer mode.
		trailerAuth := header
		trailerAuth.PayloadHash = auth.StreamingSignedTrailer{}
		good := frame(awschunked.Signed, "test")
		goodTrailer := frame(awschunked.SignedTrailer, "test", crc)
		for _, tc := range []struct {
			name     string
			header   http.Header
			verified auth.Auth
			wire     string
			wantErr  bool
		}{
			{"signed chunks", signedHeader, header, good, false},
			{"signed chunks and trailer", trailerHeader, trailerAuth, goodTrailer, false},
			{"a tampered chunk", signedHeader, header, strings.Replace(good, "streamed-body", "streamed-bodY", 1), true},
			{"chunks signed with another secret", signedHeader, header, frame(awschunked.Signed, "other"), true},
			{"a tampered trailer", trailerHeader, trailerAuth, strings.Replace(goodTrailer, "Nyb53Q==", "AAAAAA==", 1), true},
			{"another secret under parse", signedHeader, nil, frame(awschunked.Signed, "other"), false},
		} {
			key := strings.ReplaceAll(tc.name, " ", "-")
			_, err := verifiedWith(h, s3api.OpPutObject, tc.header, tc.verified, in(key, tc.wire))
			switch {
			case tc.wantErr && (err == nil || err.ID != s3api.IdentitySignatureDoesNotMatch()):
				t.Errorf("%s: PutObject = %v, want SignatureDoesNotMatch", tc.name, err)
			case !tc.wantErr && err != nil:
				t.Errorf("%s: PutObject = %s %s", tc.name, err.ID.Shape, err.Message)
			}
			_, getErr := invoke(h, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "signed", Key: key})
			if stored := getErr == nil; stored == tc.wantErr {
				t.Errorf("%s: stored is %v", tc.name, stored)
			}
		}

		// UploadPart reads its body through the same framing: a signed
		// part is stored, a tampered chunk is refused and stores nothing.
		id := h.startUpload("signed", "parts", s3api.CreateMultipartUploadInput{})
		part := func(n int32, wire string) *s3api.UploadPartInput {
			return &s3api.UploadPartInput{Bucket: "signed", Key: "parts", UploadId: id, PartNumber: n, Body: bytesBody(wire)}
		}
		if _, err := verifiedWith(h, s3api.OpUploadPart, signedHeader, header, part(1, good)); err != nil {
			t.Errorf("UploadPart of signed chunks = %s %s", err.ID.Shape, err.Message)
		}
		_, err := verifiedWith(h, s3api.OpUploadPart, signedHeader, header, part(2, strings.Replace(good, "streamed-body", "streamed-bodY", 1)))
		if err == nil || err.ID != s3api.IdentitySignatureDoesNotMatch() {
			t.Errorf("UploadPart of a tampered chunk = %v, want SignatureDoesNotMatch", err)
		}
		parts := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "signed", Key: "parts", UploadId: id})
		if len(parts.Parts) != 1 || *parts.Parts[0].PartNumber != 1 {
			t.Errorf("ListParts = %+v, want the signed part 1 alone", parts.Parts)
		}
	})
}

// TestAWSChunkedUploads: an aws-chunked body is decoded and its trailer
// checksum verified at EOF; a wrong trailer value is BadDigest, a trailer
// other than the one x-amz-trailer names MalformedTrailerError, a chunk
// under 8,192 bytes before the last InvalidChunkSizeError, a body shorter
// than x-amz-decoded-content-length IncompleteBody, and an
// x-amz-sdk-checksum-algorithm other than the trailer's BadDigest before
// the body is read; nothing is stored for any of them (s3.md 6.2, 6.3).
func TestAWSChunkedUploads(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("chunks")
		in := func(key, wire string) *s3api.PutObjectInput {
			return &s3api.PutObjectInput{
				Bucket: "chunks", Key: key, ContentEncoding: new("aws-chunked"), ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32),
				Body: bytesBody(wire),
			}
		}
		out, err := withHeader(h, s3api.OpPutObject, chunkedHeader(13), in("good", chunked(t, "streamed-body", "Nyb53Q==")))
		if err != nil {
			t.Fatalf("PutObject of a JS SDK aws-chunked body: %s %s", err.ID.QueryCode, err.Message)
		}
		if *out.ChecksumCRC32 != "Nyb53Q==" {
			t.Fatalf("PutObject CRC32 = %s, want the trailer's Nyb53Q==", *out.ChecksumCRC32)
		}
		got, get := h.read(&s3api.GetObjectInput{Bucket: "chunks", Key: "good"})
		if got != "streamed-body" || get.ContentEncoding != nil {
			t.Fatalf("GetObject = %q with Content-Encoding %v; want the decoded bytes and aws-chunked dropped", got, get.ContentEncoding)
		}

		sha256Algorithm := in("algorithm-differs", "")
		sha256Algorithm.ChecksumAlgorithm, sha256Algorithm.Body = new(s3api.ChecksumAlgorithmSha256), &body{r: unreadable{t: h.T}, n: 1}
		for _, tc := range []struct {
			name   string
			header http.Header
			in     *s3api.PutObjectInput
			code   string
			msg    string
			fields map[string]string
		}{
			{"trailer value differs", chunkedHeader(13), in("mismatch", chunked(t, "streamed-body", "AAAAAA==")), "BadDigest", "The CRC32 you specified did not match the calculated checksum.", nil},
			{
				"trailer x-amz-trailer did not name", chunkedHeader(13),
				in("misnamed", chunkedWith(t, awschunked.Trailer{Name: "x-amz-checksum-crc32c", Value: "AAAAAA=="}, "streamed-body")),
				"MalformedTrailerError", "The request contained trailing data that was not well-formed or did not conform to our published schema.", nil,
			},
			{
				"small chunk before the last", chunkedHeader(13), in("small-chunk", chunkedWith(t, awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: "Nyb53Q=="}, "streamed", "-body")),
				"InvalidChunkSizeError", "Only the last chunk is allowed to have a size less than 8192 bytes",
				map[string]string{"Chunk": "2", "BadChunkSize": "8"},
			},
			{"body shorter than declared", chunkedHeader(20), in("short", chunked(t, "streamed-body", "Nyb53Q==")), "IncompleteBody", incompleteBody, nil},
			{"sdk algorithm differs from the trailer's", chunkedHeader(13), sha256Algorithm, "BadDigest", "The SHA256 you specified did not match the calculated checksum.", nil},
			{"body shorter than Content-Length", nil, &s3api.PutObjectInput{Bucket: "chunks", Key: "short-plain", Body: &body{r: strings.NewReader("abc"), n: 10}}, "IncompleteBody", incompleteBody, nil},
		} {
			_, err := withHeader(h, s3api.OpPutObject, tc.header, tc.in)
			if err == nil || err.ID.QueryCode != tc.code || err.Message != tc.msg {
				t.Errorf("%s: got %v, want %s %q", tc.name, err, tc.code, tc.msg)
				continue
			}
			wantFields(t, err, tc.fields)
			if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "chunks", Key: tc.in.Key}); err == nil {
				t.Errorf("%s: the refused upload left %s behind", tc.name, tc.in.Key)
			}
		}
	})
}

// incompleteBody is S3's IncompleteBody text.
const incompleteBody = "You did not provide the number of bytes specified by the Content-Length HTTP header."

// TestConcurrentCreateOnlyPutsHaveOneWinner: If-None-Match: * puts racing
// for one key leave exactly one success; the losers answer 412 or 409
// ConditionalRequestConflict (s3.md 5.10).
func TestConcurrentCreateOnlyPutsHaveOneWinner(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("race")
		const writers = 8
		var wg sync.WaitGroup
		results := make([]*awserr.Error, writers)
		for i := range writers {
			wg.Go(func() {
				_, results[i] = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
					Bucket: "race", Key: "lock", IfNoneMatch: new("*"), Body: bytesBody("writer " + strconv.Itoa(i)),
				})
			})
		}
		wg.Wait()
		wins := 0
		for _, err := range results {
			switch {
			case err == nil:
				wins++
			case err.ID.QueryCode != "PreconditionFailed" && err.ID.QueryCode != "ConditionalRequestConflict":
				t.Errorf("a losing writer got %s, want PreconditionFailed or ConditionalRequestConflict", err.ID.QueryCode)
			}
		}
		if wins != 1 {
			t.Fatalf("%d writers won, want exactly 1", wins)
		}
	})
}

// racer is a body whose first read runs a competing write before it
// yields its bytes: the write lands between the put's pre-check and its
// commit.
type racer struct {
	r       io.Reader
	compete func()
	once    sync.Once
}

func (b *racer) Read(p []byte) (int, error) {
	b.once.Do(b.compete)
	return b.r.Read(p)
}

// TestConditionalPutLosingAtCommitIsConflict: an If-None-Match: * put whose
// pre-check passed, but which another put of the key beat while its body
// streamed, answers 409 ConditionalRequestConflict naming the condition and
// the key; the winner's object stays and the loser's bytes are released
// (s3.md 5.10).
func TestConditionalPutLosingAtCommitIsConflict(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("race")
		var winner *awserr.Error
		loser := &racer{r: strings.NewReader("loser"), compete: func() {
			_, winner = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "race", Key: "lock", IfNoneMatch: new("*"), Body: bytesBody("winner")})
		}}
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "race", Key: "lock", IfNoneMatch: new("*"), Body: &body{r: loser, n: 5}})
		if winner != nil {
			t.Fatalf("the competing put failed: %s %s", winner.ID.QueryCode, winner.Message)
		}
		wantCode(t, err, "ConditionalRequestConflict", http.StatusConflict)
		wantFields(t, err, map[string]string{"Condition": "If-None-Match", "Key": "lock"})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "race", Key: "lock"}); got != "winner" {
			t.Fatalf("GetObject after the race = %q, want the winner's object", got)
		}
		if refs := blobs.Refs(); len(refs) != 1 {
			t.Fatalf("blob store holds %d bodies after the race, want only the winner's: %v", len(refs), refs)
		}
	})
}

// zeros reads n zero bytes without holding them.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.n == 0 {
		return 0, io.EOF
	}
	k := min(int64(len(p)), z.n)
	clear(p[:k])
	z.n -= k
	return int(k), nil
}

// rss is the process's resident set in bytes, from /proc/self/statm.
func rss(t *testing.T) int64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		t.Skip("no /proc/self/statm on this platform: ", err)
	}
	f := strings.Fields(string(b))
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return pages * int64(os.Getpagesize())
}

// TestLargePutStreams: a 256 MiB PutObject is streamed into the blob
// store, never buffered, and a ranged GetObject streams exactly the range
// (ARCHITECTURE.md 6.1).
func TestLargePutStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 256 MiB")
	}
	runObjects(t, func(h *harness) {
		h.createBucket("big")
		const size = 256 << 20
		before := rss(h.T)
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "big", Key: "zeros", Body: &body{r: &zeros{n: size}, n: size}})
		if grown := rss(h.T) - before; grown > 32<<20 {
			t.Fatalf("RSS grew by %d MiB over a 256 MiB upload, want under 32 MiB", grown>>20)
		}
		got, get := h.read(&s3api.GetObjectInput{Bucket: "big", Key: "zeros", Range: new("bytes=1048576-1048585")})
		if got != string(make([]byte, 10)) || *get.ContentRange != "bytes 1048576-1048585/268435456" || *get.ContentLength != 10 {
			t.Fatalf("ranged GetObject = %d bytes, Content-Range %v, Content-Length %d", len(got), get.ContentRange, *get.ContentLength)
		}
	})
}

// TestSSEKMSCallsKMSAsTheCaller: an SSE-KMS put asks KMS for a data key and
// a get decrypts it, both charged to the caller, so the access recorder
// holds kms:GenerateDataKey and kms:Decrypt for it (ARCHITECTURE.md 16);
// the response reports the full key ARN and a non-MD5 ETag (s3.md 5.3,
// 5.7); a disabled key refuses the read with KMS.DisabledException.
func TestSSEKMSCallsKMSAsTheCaller(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("sealed")
		key := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		out := ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "sealed", Key: "secret", Body: bytesBody("test-sse"),
			ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(key.KeyId),
		})
		if *out.SSEKMSKeyId != *key.Arn || *out.ETag == `"`+md5hex("test-sse")+`"` {
			t.Fatalf("SSE-KMS PutObject = key %s, ETag %s; want the key ARN %s and an ETag that is not the MD5", *out.SSEKMSKeyId, *out.ETag, *key.Arn)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "sealed", Key: "secret"}); got != "test-sse" {
			t.Fatalf("GetObject of an SSE-KMS object = %q", got)
		}
		root, perr := access.ParsePrincipal("arn:aws:iam::000000000000:root")
		if perr != nil {
			t.Fatal(perr)
		}
		actions := map[string]bool{}
		for _, u := range h.Access.Usage(access.Filter{Principal: root, Service: "kms"}) {
			if _, ok := u.Mode.(access.AsCaller); ok {
				actions[u.Action.String()] = true
			}
		}
		if !actions["kms:GenerateDataKey"] || !actions["kms:Decrypt"] {
			t.Fatalf("forward-access KMS actions recorded for the caller = %v, want kms:GenerateDataKey and kms:Decrypt", actions)
		}
		ok(h, who{}, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: key.KeyId})
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "sealed", Key: "secret"})
		if err == nil || err.ID.QueryCode != "KMS.DisabledException" || err.ID.Status != http.StatusBadRequest {
			t.Fatalf("GetObject under a disabled key = %v, want 400 KMS.DisabledException", err)
		}
		_, err = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "sealed", Key: "other", Body: &body{r: unreadable{t: h.T}, n: 1},
			ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new("fake-key-id"),
		})
		if err == nil || err.ID.QueryCode != "KMS.NotFoundException" {
			t.Fatalf("PutObject under an unknown key = %v, want KMS.NotFoundException before the body is read", err)
		}
	})
}

// TestSSEKMSBucketDefault: a bucket whose default rule is aws:kms without
// a key encrypts a put that sends no SSE headers under alias/aws/s3, which
// KMS creates on first use, and reports the full key ARN and the bucket
// key on the put and on HEAD, the read Terraform's empty plan depends on
// (s3.md 5.7, tf-remaining.md 8).
func TestSSEKMSBucketDefault(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("kms-default")
		ok(h, who{}, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{
			Bucket: "kms-default",
			ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{{
				ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAwsKms},
				BucketKeyEnabled:                   new(true),
			}}},
		})
		out := h.put("kms-default", "k", "sealed")
		managed := ok(h, who{}, kmsapi.OpDescribeKey, &kmsapi.DescribeKeyInput{KeyId: "alias/aws/s3"}).KeyMetadata
		if *out.ServerSideEncryption != s3api.ServerSideEncryptionAwsKms || out.SSEKMSKeyId == nil || *out.SSEKMSKeyId != *managed.Arn {
			t.Fatalf("PutObject under an aws:kms default = %s key %v, want aws:kms under alias/aws/s3's key %s", *out.ServerSideEncryption, out.SSEKMSKeyId, *managed.Arn)
		}
		if out.BucketKeyEnabled == nil || !*out.BucketKeyEnabled {
			t.Fatalf("PutObject under a bucket-key default reports BucketKeyEnabled %v, want true", out.BucketKeyEnabled)
		}
		head := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "kms-default", Key: "k"})
		if *head.ServerSideEncryption != s3api.ServerSideEncryptionAwsKms || *head.SSEKMSKeyId != *managed.Arn || head.BucketKeyEnabled == nil || !*head.BucketKeyEnabled {
			t.Fatalf("HeadObject = %s key %s bucket key %v, want aws:kms, %s, true", *head.ServerSideEncryption, *head.SSEKMSKeyId, head.BucketKeyEnabled, *managed.Arn)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "kms-default", Key: "k"}); got != "sealed" {
			t.Fatalf("GetObject = %q, want the bytes written", got)
		}
	})
}

// TestPutObjectRefusals: each refusal of s3.md 5.1, 5.2, 5.4, 5.6, 5.7 and
// 6.3 that needs no body answers before the body is read, with its code,
// status and context elements; a payload hash that does not match the
// bytes is judged at EOF.
func TestPutObjectRefusals(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("refusals")
		sum := sha256.Sum256([]byte("other"))
		for _, tc := range []struct {
			name   string
			in     s3api.PutObjectInput
			header http.Header
			// body is read by the handler; empty means it must not be.
			body   string
			code   string
			status int
			fields map[string]string
			// message and order, when set, are the recorded text and
			// element order.
			message string
			order   []string
		}{
			{
				name: "x-amz-content-sha256 differs", header: http.Header{"X-Amz-Content-Sha256": {hex.EncodeToString(sum[:])}}, body: "hello",
				code: "XAmzContentSHA256Mismatch", status: http.StatusBadRequest,
			},
			{
				name: "declared size over 5 GiB", in: s3api.PutObjectInput{ContentLength: new(int64(5<<30 + 1))},
				code: "EntityTooLarge", status: http.StatusBadRequest, fields: map[string]string{"MaxSizeAllowed": "5368709120", "ProposedSize": "5368709121"},
			},
			{
				name: "SigV4a streaming", header: http.Header{"X-Amz-Content-Sha256": {"STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD"}},
				code: "NotImplemented", status: http.StatusNotImplemented,
			},
			{
				name: "trailer mode without x-amz-trailer", header: http.Header{"X-Amz-Content-Sha256": {"STREAMING-UNSIGNED-PAYLOAD-TRAILER"}},
				code: "InvalidRequest", status: http.StatusBadRequest,
			},
			{
				name: "key over 1,024 bytes", in: s3api.PutObjectInput{Key: strings.Repeat("k", 1025)},
				code: "KeyTooLongError", status: http.StatusBadRequest, fields: map[string]string{"MaxSizeAllowed": "1024", "Size": "1025"},
				// testdata/parity/s3/R12/09-key-1025.json
				message: "Your key is too long", order: []string{"Size", "MaxSizeAllowed"},
			},
			{
				name: "user metadata over 2 KB", in: s3api.PutObjectInput{Metadata: map[string]string{"big": strings.Repeat("m", 2100)}},
				code: "MetadataTooLarge", status: http.StatusBadRequest, fields: map[string]string{"MaxSizeAllowed": "2048", "Size": "2103"},
				// testdata/parity/s3/R12/09-metadata-over-2k.json
				message: "Your metadata headers exceed the maximum allowed metadata size", order: []string{"MaxSizeAllowed", "Size"},
			},
			{
				name: "storage class of another bucket type", in: s3api.PutObjectInput{StorageClass: new(s3api.StorageClassOutposts)},
				code: "InvalidStorageClass", status: http.StatusBadRequest, fields: map[string]string{"StorageClassRequested": "OUTPOSTS"},
			},
			{
				name: "SSE-C", in: s3api.PutObjectInput{SSECustomerAlgorithm: new("AES256"), SSECustomerKey: new("a2V5")},
				code: "AccessDenied", status: http.StatusForbidden,
			},
			{
				name: "KMS key id without aws:kms", in: s3api.PutObjectInput{SSEKMSKeyId: new("alias/x")},
				code: "InvalidArgument", status: http.StatusBadRequest,
				fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption", "ArgumentValue": "AES256"},
			},
			{
				name: "aws:fsx encryption", in: s3api.PutObjectInput{ServerSideEncryption: new(s3api.ServerSideEncryptionAwsFsx)},
				code: "InvalidArgument", status: http.StatusBadRequest,
				fields: map[string]string{"ArgumentName": "x-amz-server-side-encryption", "ArgumentValue": "aws:fsx"},
			},
		} {
			in := tc.in
			in.Bucket = "refusals"
			if in.Key == "" {
				in.Key = "refused"
			}
			in.Body = &body{r: unreadable{t: h.T}, n: 1}
			if tc.body != "" {
				in.Body = bytesBody(tc.body)
			}
			_, err := withHeader(h, s3api.OpPutObject, tc.header, &in)
			if err == nil || err.ID.QueryCode != tc.code || err.ID.Status != tc.status {
				t.Errorf("%s: got %v, want %d %s", tc.name, err, tc.status, tc.code)
				continue
			}
			wantFields(t, err, tc.fields)
			if tc.message != "" && err.Message != tc.message {
				t.Errorf("%s: message %q, want %q", tc.name, err.Message, tc.message)
			}
			if tc.order != nil {
				wantFieldOrder(t, err, tc.order...)
			}
		}
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "refusals", Key: "refused"}); err == nil {
			t.Fatal("a refused PutObject left an object behind")
		}
		good := sha256.Sum256([]byte("hello"))
		in := &s3api.PutObjectInput{Bucket: "refusals", Key: "hashed", Body: bytesBody("hello")}
		if _, err := withHeader(h, s3api.OpPutObject, http.Header{"X-Amz-Content-Sha256": {hex.EncodeToString(good[:])}}, in); err != nil {
			t.Fatalf("PutObject with the body's x-amz-content-sha256 = %s %s", err.ID.QueryCode, err.Message)
		}
	})
}

// TestSSEKMSKeyForms runs PutObject under each form of key reference
// (ARCHITECTURE.md 8 rule 7, s3.md 5.7): a key id, key ARN, alias name and
// alias ARN in the bucket's scope encrypt under the key and report its
// ARN; an ARN in another Region is KMS.NotFoundException "Invalid arn
// <region>" and one in another account names a key that does not exist,
// both before the body is read; a key pending deletion refuses a put and
// the read of an object written under it with KMS.KMSInvalidStateException.
func TestSSEKMSKeyForms(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("forms")
		key := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		ok(h, who{}, kmsapi.OpCreateAlias, &kmsapi.CreateAliasInput{AliasName: "alias/forms", TargetKeyId: key.KeyId})
		put := func(name, keyRef string, b *body) (*s3api.PutObjectOutput, *awserr.Error) {
			return as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
				Bucket: "forms", Key: name, Body: b,
				ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(keyRef),
			})
		}
		for name, ref := range map[string]string{
			"id": key.KeyId, "key-arn": *key.Arn,
			"alias": "alias/forms", "alias-arn": "arn:aws:kms:us-east-1:000000000000:alias/forms",
		} {
			out, err := put(name, ref, bytesBody("v"))
			if err != nil {
				t.Fatalf("PutObject under %s %s = %s %q", name, ref, err.ID.QueryCode, err.Message)
			}
			if *out.SSEKMSKeyId != *key.Arn {
				t.Errorf("PutObject under %s reports key %s, want %s", name, *out.SSEKMSKeyId, *key.Arn)
			}
		}
		for _, tc := range []struct{ ref, message string }{
			{"arn:aws:kms:us-west-2:000000000000:key/" + key.KeyId, "Invalid arn us-west-2"},
			{"arn:aws:kms:us-east-1:111111111111:key/" + key.KeyId, "Key 'arn:aws:kms:us-east-1:111111111111:key/" + key.KeyId + "' does not exist"},
		} {
			_, err := put("refused", tc.ref, &body{r: unreadable{t: h.T}, n: 1})
			wantCode(t, err, "KMS.NotFoundException", http.StatusBadRequest)
			if err.Message != tc.message {
				t.Errorf("PutObject under %s = %q, want %q", tc.ref, err.Message, tc.message)
			}
		}
		ok(h, who{}, kmsapi.OpScheduleKeyDeletion, &kmsapi.ScheduleKeyDeletionInput{KeyId: key.KeyId, PendingWindowInDays: new(int32(7))})
		pending := *key.Arn + " is pending deletion."
		_, err := put("late", key.KeyId, &body{r: unreadable{t: h.T}, n: 1})
		wantCode(t, err, "KMS.KMSInvalidStateException", http.StatusBadRequest)
		if err.Message != pending {
			t.Errorf("PutObject under a key pending deletion = %q, want %q", err.Message, pending)
		}
		_, err = as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "forms", Key: "id"})
		wantCode(t, err, "KMS.KMSInvalidStateException", http.StatusBadRequest)
	})
}

// TestKMSKeyIDNeedsKMSOnAKMSDefaultBucket: a write naming a KMS key
// without asking for aws:kms is refused before its body is read even on a
// bucket whose default is aws:kms, rather than encrypted under the
// bucket's key; PutObject and CopyObject alike.
func TestKMSKeyIDNeedsKMSOnAKMSDefaultBucket(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("kms-default")
		ok(h, who{}, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{Bucket: "kms-default", ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{
			Rules: []s3api.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAwsKms}}},
		}})
		h.put("kms-default", "source", "bytes")
		named := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "kms-default", Key: "k", SSEKMSKeyId: new(named), Body: &body{r: unreadable{t: h.T}, n: 1}})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		wantFields(t, err, map[string]string{"ArgumentName": "x-amz-server-side-encryption", "ArgumentValue": "aws:kms"})
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "kms-default", Key: "k", CopySource: "kms-default/source", SSEKMSKeyId: new(named)})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "kms-default", Key: "k"}); err == nil {
			t.Fatal("a refused write left an object behind")
		}
	})
}

// TestSSECHeadersOnReadsAreRefused: no object is SSE-C encrypted, since
// SSE-C writes are refused, so SSE-C headers on GetObject, HeadObject and
// a copy source are 400 InvalidRequest rather than ignored (s3.md 5.7).
func TestSSECHeadersOnReadsAreRefused(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("plain")
		h.put("plain", "k", "bytes")
		alg := new("AES256")
		_, err := as(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "plain", Key: "k", SSECustomerAlgorithm: alg})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		_, err = as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "plain", Key: "k", SSECustomerKeyMD5: new("bWQ1")})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "plain", Key: "copy", CopySource: "plain/k", CopySourceSSECustomerAlgorithm: alg})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "The encryption parameters are not applicable to this object." {
			t.Errorf("message = %q", err.Message)
		}
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "plain", Key: "copy"}); err == nil {
			t.Fatal("a refused copy left an object behind")
		}
	})
}

// TestConditionalPutIfMatch: If-Match writes only over a current object
// carrying the ETag; another ETag is 412 PreconditionFailed naming
// If-Match, a key without an object is 404 NoSuchKey, and a refused write
// leaves the object as it was (s3.md 5.10).
func TestConditionalPutIfMatch(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("if-match")
		etag := *h.put("if-match", "k", "one").ETag
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "if-match", Key: "k", IfMatch: new(`"other"`), Body: &body{r: unreadable{t: h.T}, n: 1}})
		wantCode(t, err, "PreconditionFailed", http.StatusPreconditionFailed)
		wantFields(t, err, map[string]string{"Condition": "If-Match"})
		_, err = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "if-match", Key: "absent", IfMatch: new(etag), Body: &body{r: unreadable{t: h.T}, n: 1}})
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "if-match", Key: "k"}); got != "one" {
			t.Fatalf("GetObject after refused If-Match puts = %q, want the first object", got)
		}
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "if-match", Key: "k", IfMatch: new(etag), Body: bytesBody("two")})
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "if-match", Key: "k"}); got != "two" {
			t.Fatalf("GetObject after an If-Match put with the current ETag = %q, want the new object", got)
		}
	})
}
