package s3_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // S3's multipart ETag is built from MD5s; the test recomputes it
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awschunked"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// TestMultipartOverHTTP sends a multipart upload through the edge: the
// Complete body's ETags may be quoted as &quot; or &#34; (sigv4-checksums-
// endpoints.md 4.4); a partNumber GET answers 206 with the part and
// x-amz-mp-parts-count (s3.md 5.8); and a StatusOKError rule on
// CompleteMultipartUpload lets the upload complete, then answers 200 with
// an <Error> document after the XML declaration (wire-rest.md 3.7).
func TestMultipartOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		send := func(method, target, body string, want int) *httptest.ResponseRecorder {
			t.Helper()
			rec := e.send(method, target, strings.NewReader(body), int64(len(body)), nil)
			if rec.Code != want {
				t.Fatalf("%s %s = %d %s, want %d", method, target, rec.Code, rec.Body, want)
			}
			return rec
		}
		start := func(key string) string {
			t.Helper()
			var doc struct {
				UploadID string `xml:"UploadId"`
			}
			if err := xml.Unmarshal(send(http.MethodPost, "/wire/"+key+"?uploads", "", http.StatusOK).Body.Bytes(), &doc); err != nil || doc.UploadID == "" {
				t.Fatalf("CreateMultipartUpload of %s: no upload id (%v)", key, err)
			}
			return doc.UploadID
		}
		send(http.MethodPut, "/wire", "", http.StatusOK)

		id := start("obj")
		first := strings.Repeat("a", minPart)
		e1 := send(http.MethodPut, "/wire/obj?partNumber=1&uploadId="+id, first, http.StatusOK).Header().Get("ETag")
		e2 := send(http.MethodPut, "/wire/obj?partNumber=2&uploadId="+id, "end", http.StatusOK).Header().Get("ETag")
		body := `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Part><ETag>` + strings.ReplaceAll(e1, `"`, "&quot;") + `</ETag><PartNumber>1</PartNumber></Part>` +
			`<Part><PartNumber>2</PartNumber><ETag>` + strings.ReplaceAll(e2, `"`, "&#34;") + `</ETag></Part></CompleteMultipartUpload>`
		var result struct {
			ETag string `xml:"ETag"`
		}
		if err := xml.Unmarshal(send(http.MethodPost, "/wire/obj?uploadId="+id, body, http.StatusOK).Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if want := multipartETag([]byte(first), []byte("end")); result.ETag != want {
			t.Fatalf("CompleteMultipartUpload ETag = %s, want %s", result.ETag, want)
		}
		part := send(http.MethodGet, "/wire/obj?partNumber=2", "", http.StatusPartialContent)
		if part.Body.String() != "end" || part.Header().Get("X-Amz-Mp-Parts-Count") != "2" || part.Header().Get("Content-Range") != "bytes 5242880-5242882/5242883" {
			t.Fatalf("partNumber=2 = %q with %v, want the part, 2 parts and its Content-Range", part.Body, part.Header())
		}

		if _, err := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{s3api.OpCompleteMultipartUpload.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.StatusOKError{Shape: s3api.IdentityInvalidArgument().Shape},
			Trigger: fault.NextN{N: 1},
		}); err != nil {
			t.Fatal(err)
		}
		id = start("faulted")
		only := send(http.MethodPut, "/wire/faulted?partNumber=1&uploadId="+id, "only", http.StatusOK).Header().Get("ETag")
		body = `<CompleteMultipartUpload><Part><ETag>` + only + `</ETag><PartNumber>1</PartNumber></Part></CompleteMultipartUpload>`
		got := send(http.MethodPost, "/wire/faulted?uploadId="+id, body, http.StatusOK).Body.String()
		if !strings.HasPrefix(got, `<?xml version="1.0" encoding="UTF-8"?>`) || !strings.Contains(got, "<Error><Code>InvalidArgument</Code>") {
			t.Fatalf("faulted Complete = 200 %q, want an <Error> document with the injected code after the XML declaration", got)
		}
		if read := send(http.MethodGet, "/wire/faulted", "", http.StatusOK).Body.String(); read != "only" {
			t.Fatalf("object after the faulted Complete = %q, want the upload completed", read)
		}
	}, kerneltest.Services(s3api.Service()))
}

// minPart is the least size of every part a Complete lists but the last
// (s3.md 10).
const minPart = 5 << 20

// payload is n bytes drawn from seed.
func payload(seed uint64, n int) []byte {
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], seed)
	r := rand.NewChaCha8(key)
	b := make([]byte, n+7)
	for i := 0; i < n; i += 8 {
		binary.LittleEndian.PutUint64(b[i:], r.Uint64())
	}
	return b[:n]
}

// fatal is what the checksum helpers need from *testing.T and *rapid.T.
type fatal interface {
	Helper()
	Fatal(args ...any)
}

// sumOf is data's checksum under alg in its wire form.
func sumOf(f fatal, alg checksum.Algorithm, data []byte) string {
	f.Helper()
	h, err := checksum.New(alg)
	if err != nil {
		f.Fatal(err)
	}
	_, _ = h.Write(data)
	return h.Checksum().String()
}

// multipartETag is the ETag S3 gives an object of parts (s3.md 5.3): the
// MD5 of the concatenated part MD5s, then "-N", quoted.
func multipartETag(parts ...[]byte) string {
	var digests []byte
	for _, p := range parts {
		d := md5.Sum(p) //nolint:gosec // see the import
		digests = append(digests, d[:]...)
	}
	d := md5.Sum(digests) //nolint:gosec // see the import
	return `"` + hex.EncodeToString(d[:]) + "-" + strconv.Itoa(len(parts)) + `"`
}

// startUpload creates an upload of bucket/key with in's other members and
// returns its id.
func (h *harness) startUpload(bucket, key string, in s3api.CreateMultipartUploadInput) string {
	h.Helper()
	in.Bucket, in.Key = bucket, key
	out := ok(h, who{}, s3api.OpCreateMultipartUpload, &in)
	if out.UploadId == nil || *out.UploadId == "" {
		h.Fatal("CreateMultipartUpload returned no upload id")
	}
	return *out.UploadId
}

// uploadPart sends data as part n of upload id with in's other members.
func uploadPart(h *harness, bucket, key, id string, n int32, data []byte, in s3api.UploadPartInput) (*s3api.UploadPartOutput, *awserr.Error) {
	in.Bucket, in.Key, in.UploadId, in.PartNumber, in.Body = bucket, key, id, n, bytesBody(string(data))
	return as(h, who{}, s3api.OpUploadPart, &in)
}

// sendPart is uploadPart that fails the test on an error.
func (h *harness) sendPart(bucket, key, id string, n int32, data []byte, in s3api.UploadPartInput) *s3api.UploadPartOutput {
	h.Helper()
	out, err := uploadPart(h, bucket, key, id, n, data, in)
	if err != nil {
		h.Fatalf("UploadPart %d: %s %s", n, err.ID.QueryCode, err.Message)
	}
	return out
}

// complete runs CompleteMultipartUpload of upload id listing parts.
func complete(h *harness, bucket, key, id string, parts []s3api.CompletedPart, in s3api.CompleteMultipartUploadInput) (*s3api.CompleteMultipartUploadOutput, *awserr.Error) {
	in.Bucket, in.Key, in.UploadId, in.MultipartUpload = bucket, key, id, &s3api.CompletedMultipartUpload{Parts: parts}
	return as(h, who{}, s3api.OpCompleteMultipartUpload, &in)
}

// listed is the Complete entry of an uploaded part, restating what the
// UploadPart response reported, as the SDK managers do (s3.md 6.4).
func listed(n int32, out *s3api.UploadPartOutput) s3api.CompletedPart {
	return s3api.CompletedPart{
		PartNumber: new(n), ETag: out.ETag,
		ChecksumCRC32: out.ChecksumCRC32, ChecksumCRC32C: out.ChecksumCRC32C, ChecksumCRC64NVME: out.ChecksumCRC64NVME,
		ChecksumSHA1: out.ChecksumSHA1, ChecksumSHA256: out.ChecksumSHA256, ChecksumSHA512: out.ChecksumSHA512, ChecksumMD5: out.ChecksumMD5,
		ChecksumXXHASH64: out.ChecksumXXHASH64, ChecksumXXHASH3: out.ChecksumXXHASH3, ChecksumXXHASH128: out.ChecksumXXHASH128,
	}
}

// readBytes reads in whole and fails the test on an error.
func (h *harness) readBytes(in *s3api.GetObjectInput) ([]byte, *s3api.GetObjectOutput) {
	h.Helper()
	got, out := h.read(in)
	return []byte(got), out
}

// TestMultipartUploadRoundTrip: a CRC32 upload of two parts completes to
// the concatenated bytes, with the MD5-of-MD5s ETag and the COMPOSITE
// checksum; metadata, tags and Last-Modified come from Create; partNumber
// reads return each part's exact bytes with x-amz-mp-parts-count; the
// upload is gone afterwards and only the object's bytes stay stored
// (s3.md 5.3, 5.8, 6.4, 10).
func TestMultipartUploadRoundTrip(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("mpu")
		created := ok(h, who{}, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{
			Bucket: "mpu", Key: "big", ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32),
			ContentType: new("text/plain"), Metadata: map[string]string{"origin": "test"}, Tagging: new("team=core"),
		})
		if *created.Bucket != "mpu" || *created.Key != "big" || *created.ChecksumAlgorithm != s3api.ChecksumAlgorithmCrc32 ||
			*created.ChecksumType != s3api.ChecksumTypeComposite || *created.ServerSideEncryption != s3api.ServerSideEncryptionAes256 {
			t.Fatalf("CreateMultipartUpload = %+v, want the bucket, key, CRC32 COMPOSITE and AES256 echoed", created)
		}
		id := *created.UploadId
		initiated := h.Clock.Now().Wire().Truncate(time.Second)
		if _, err := h.Clock.Advance(time.Hour); err != nil {
			t.Fatal(err)
		}
		first, second := payload(1, minPart), payload(2, 100)
		p1 := h.sendPart("mpu", "big", id, 1, first, s3api.UploadPartInput{ChecksumCRC32: new(sumOf(t, checksum.CRC32, first))})
		p2 := h.sendPart("mpu", "big", id, 2, second, s3api.UploadPartInput{})
		if *p1.ETag != `"`+md5hex(string(first))+`"` || *p2.ChecksumCRC32 != sumOf(t, checksum.CRC32, second) {
			t.Fatalf("UploadPart = ETag %s, CRC32 %v; want the part's MD5 and its CRC32 computed by S3", *p1.ETag, p2.ChecksumCRC32)
		}
		done, err := complete(h, "mpu", "big", id, []s3api.CompletedPart{listed(1, p1), listed(2, p2)}, s3api.CompleteMultipartUploadInput{})
		if err != nil {
			t.Fatalf("CompleteMultipartUpload: %s %s", err.ID.QueryCode, err.Message)
		}
		wantSum := compositeOf(t, checksum.CRC32, first, second)
		if *done.ETag != multipartETag(first, second) || *done.ChecksumCRC32 != wantSum || *done.ChecksumType != s3api.ChecksumTypeComposite {
			t.Fatalf("CompleteMultipartUpload = ETag %s, CRC32 %v %v; want %s and %s COMPOSITE", *done.ETag, done.ChecksumCRC32, done.ChecksumType, multipartETag(first, second), wantSum)
		}
		if *done.Location != "http://localhost:4566/mpu/big" || done.VersionId != nil {
			t.Fatalf("CompleteMultipartUpload Location %q, version %v; want the object's URL and no version on an unversioned bucket", *done.Location, done.VersionId)
		}
		got, get := h.readBytes(&s3api.GetObjectInput{Bucket: "mpu", Key: "big", ChecksumMode: new(s3api.ChecksumModeEnabled)})
		if !bytes.Equal(got, slices.Concat(first, second)) || *get.ContentType != "text/plain" || get.Metadata["origin"] != "test" || *get.TagCount != 1 {
			t.Fatalf("GetObject = %d bytes, %s, %v, tags %v; want the parts, text/plain, origin=test and one tag", len(got), *get.ContentType, get.Metadata, get.TagCount)
		}
		if !get.LastModified.Equal(initiated) || *get.ChecksumCRC32 != wantSum || *get.ChecksumType != s3api.ChecksumTypeComposite {
			t.Fatalf("GetObject Last-Modified %s, CRC32 %v; want the initiation %s and %s", get.LastModified, get.ChecksumCRC32, initiated, wantSum)
		}
		part2, out := h.readBytes(&s3api.GetObjectInput{Bucket: "mpu", Key: "big", PartNumber: new(int32(2))})
		if !bytes.Equal(part2, second) || *out.PartsCount != 2 || *out.ContentRange != "bytes 5242880-5242979/5242980" {
			t.Fatalf("partNumber=2 = %d bytes, parts %v, range %v; want part 2, 2 parts and bytes 5242880-5242979/5242980", len(part2), out.PartsCount, out.ContentRange)
		}
		part1, _ := h.readBytes(&s3api.GetObjectInput{Bucket: "mpu", Key: "big", PartNumber: new(int32(1))})
		if !bytes.Equal(part1, first) {
			t.Fatalf("partNumber=1 = %d bytes, want part 1's %d", len(part1), len(first))
		}
		_, lerr := as(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "mpu", Key: "big", UploadId: id})
		wantCode(t, lerr, "NoSuchUpload", http.StatusNotFound)
		if ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "mpu"}); len(ups.Uploads) != 0 {
			t.Fatalf("ListMultipartUploads after Complete = %d uploads, want none", len(ups.Uploads))
		}
		if refs := blobs.Refs(); len(refs) != 1 {
			t.Fatalf("blob store holds %d blobs after Complete, want only the object's", len(refs))
		}
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "mpu", Key: "big"})
		if refs := blobs.Refs(); len(refs) != 0 {
			t.Fatalf("blob store holds %d blobs after the object was deleted, want none", len(refs))
		}
	})
}

// TestMultipartUploadSSEKMS is REC test_s3_multipart_upload_sse: under
// SSE-KMS each part's ETag, uploaded or copied, is random rather than the
// MD5 of its bytes, the object's ETag is the MD5 of the raw part ETags
// with "-N", and the Complete result reports no checksum though reads
// report the object's CRC64NVME (s3.md 5.3).
func TestMultipartUploadSSEKMS(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("sealed")
		h.put("sealed", "plain", "copied")
		key := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata
		id := h.startUpload("sealed", "k", s3api.CreateMultipartUploadInput{ServerSideEncryption: new(s3api.ServerSideEncryptionAwsKms), SSEKMSKeyId: new(key.KeyId)})
		const data = "test-sse"
		part := h.sendPart("sealed", "k", id, 1, []byte(data), s3api.UploadPartInput{ChecksumCRC32: new(sumOf(t, checksum.CRC32, []byte(data)))})
		raw, err := hex.DecodeString(unquote(*part.ETag))
		if err != nil || len(raw) != md5.Size || *part.ETag == `"`+md5hex(data)+`"` {
			t.Fatalf("UploadPart ETag under SSE-KMS = %s, want 128 random bits, not the MD5 %s", *part.ETag, md5hex(data))
		}
		if *part.ServerSideEncryption != s3api.ServerSideEncryptionAwsKms || *part.SSEKMSKeyId != *key.Arn {
			t.Fatalf("UploadPart = SSE %s key %s, want aws:kms under %s", *part.ServerSideEncryption, *part.SSEKMSKeyId, *key.Arn)
		}
		copied := ok(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{Bucket: "sealed", Key: "k", UploadId: id, PartNumber: 2, CopySource: "sealed/plain"})
		if e := *copied.CopyPartResult.ETag; len(e) != 2+2*md5.Size || e == `"`+md5hex("copied")+`"` {
			t.Fatalf("UploadPartCopy ETag under SSE-KMS = %s, want 128 random bits, not the MD5 %s", e, md5hex("copied"))
		}

		done, cerr := complete(h, "sealed", "k", id, []s3api.CompletedPart{{PartNumber: new(int32(1)), ETag: part.ETag}}, s3api.CompleteMultipartUploadInput{})
		if cerr != nil {
			t.Fatalf("CompleteMultipartUpload: %s %s", cerr.ID.QueryCode, cerr.Message)
		}
		d := md5.Sum(raw) //nolint:gosec // see the import
		if want := `"` + hex.EncodeToString(d[:]) + `-1"`; *done.ETag != want {
			t.Fatalf("Complete ETag = %s, want the MD5 of the part ETag's bytes %s", *done.ETag, want)
		}
		if done.ChecksumType != nil || done.ChecksumCRC64NVME != nil || *done.ServerSideEncryption != s3api.ServerSideEncryptionAwsKms {
			t.Fatalf("Complete under SSE-KMS = %v %v %s, want no checksum and aws:kms", done.ChecksumType, done.ChecksumCRC64NVME, *done.ServerSideEncryption)
		}
		got, get := h.read(&s3api.GetObjectInput{Bucket: "sealed", Key: "k", ChecksumMode: new(s3api.ChecksumModeEnabled)})
		if got != data || *get.ETag != *done.ETag || *get.ChecksumCRC64NVME != sumOf(t, checksum.CRC64NVME, []byte(data)) || *get.ChecksumType != s3api.ChecksumTypeFullObject {
			t.Fatalf("GetObject = %q ETag %s CRC64NVME %v; want the part, %s and its CRC64NVME FULL_OBJECT", got, *get.ETag, get.ChecksumCRC64NVME, *done.ETag)
		}
	})
}

// compositeOf is the COMPOSITE checksum of parts under alg computed part by
// part: the checksum of the concatenated raw part digests, then "-N".
func compositeOf(f fatal, alg checksum.Algorithm, parts ...[]byte) string {
	f.Helper()
	var digests []byte
	for _, p := range parts {
		s, err := checksum.Decode(alg, sumOf(f, alg, p))
		if err != nil {
			f.Fatal(err)
		}
		digests = append(digests, s.Bytes()...)
	}
	return sumOf(f, alg, digests) + "-" + strconv.Itoa(len(parts))
}

// invalidPartMessage is S3's InvalidPart text, two spaces included
// (wire-rest.md 3.6; REC test_complete_multipart_parts_checksum_default).
const invalidPartMessage = "One or more of the specified parts could not be found.  The part may not have been uploaded, or the specified entity tag may not match the part's entity tag."

// TestCompleteMultipartUploadPartErrors is the Complete error table of
// s3.md 10: no parts, an unknown part or ETag, a part under 5 MiB before
// the last, a wrong x-amz-mp-object-size, no body, which lists no parts
// (REC test_multipart_complete_multipart_too_small step
// complete-exc-no-parts), and an unknown upload or another key's
// NoSuchUpload, with or without a body (REC test_multipart_no_such_upload
// step complete-exc). A list may skip numbers, takes quoted and unquoted
// ETags, and discards the parts it does not list.
func TestCompleteMultipartUploadPartErrors(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("parts")
		id := h.startUpload("parts", "k", s3api.CreateMultipartUploadInput{})
		big, small := payload(3, minPart), []byte("x")
		p1 := h.sendPart("parts", "k", id, 1, big, s3api.UploadPartInput{})
		p2 := h.sendPart("parts", "k", id, 2, small, s3api.UploadPartInput{})
		p3 := h.sendPart("parts", "k", id, 3, []byte("tail"), s3api.UploadPartInput{})
		part := func(n int32, etag string) s3api.CompletedPart {
			return s3api.CompletedPart{PartNumber: new(n), ETag: new(etag)}
		}

		_, err := complete(h, "parts", "k", id, nil, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "You must specify at least one part" {
			t.Errorf("no parts: message %q", err.Message)
		}

		_, err = complete(h, "parts", "k", id, []s3api.CompletedPart{part(1, `"0123456789abcdef0123456789abcdef"`)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "InvalidPart", http.StatusBadRequest)
		if err.Message != invalidPartMessage {
			t.Errorf("wrong ETag: message %q", err.Message)
		}
		wantFields(t, err, map[string]string{"ETag": "0123456789abcdef0123456789abcdef", "PartNumber": "1", "UploadId": id})

		_, err = complete(h, "parts", "k", id, []s3api.CompletedPart{part(1, *p1.ETag), part(9, *p1.ETag)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "InvalidPart", http.StatusBadRequest)
		wantFields(t, err, map[string]string{"PartNumber": "9"})

		_, err = complete(h, "parts", "k", id, []s3api.CompletedPart{part(2, *p2.ETag), part(3, *p3.ETag)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "EntityTooSmall", http.StatusBadRequest)
		if err.Message != "Your proposed upload is smaller than the minimum allowed size" {
			t.Errorf("small part: message %q", err.Message)
		}
		wantFields(t, err, map[string]string{"ProposedSize": "1", "MinSizeAllowed": "5242880", "PartNumber": "2", "ETag": md5hex("x")})

		_, err = complete(h, "parts", "k", id, []s3api.CompletedPart{part(1, *p1.ETag), part(3, *p3.ETag)}, s3api.CompleteMultipartUploadInput{MpuObjectSize: new(int64(5))})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "The provided 'x-amz-mp-object-size' header value 5 does not match what was computed: 5242884" {
			t.Errorf("x-amz-mp-object-size: message %q", err.Message)
		}

		_, err = as(h, who{}, s3api.OpCompleteMultipartUpload, &s3api.CompleteMultipartUploadInput{Bucket: "parts", Key: "k", UploadId: id})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "You must specify at least one part" {
			t.Errorf("no body: message %q", err.Message)
		}
		_, err = as(h, who{}, s3api.OpCompleteMultipartUpload, &s3api.CompleteMultipartUploadInput{Bucket: "parts", Key: "fake-key", UploadId: "fakeid"})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
		wantFields(t, err, map[string]string{"UploadId": "fakeid"})

		_, err = complete(h, "parts", "other", id, []s3api.CompletedPart{part(1, *p1.ETag)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
		if err.Message != "The specified upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed." {
			t.Errorf("wrong key: message %q", err.Message)
		}
		wantFields(t, err, map[string]string{"UploadId": id})

		done, err := complete(h, "parts", "k", id, []s3api.CompletedPart{part(1, unquote(*p1.ETag)), part(3, *p3.ETag)}, s3api.CompleteMultipartUploadInput{MpuObjectSize: new(int64(minPart + 4))})
		if err != nil {
			t.Fatalf("Complete of parts 1 and 3 with an unquoted ETag: %s %s", err.ID.QueryCode, err.Message)
		}
		if *done.ETag != multipartETag(big, []byte("tail")) || *done.ChecksumType != s3api.ChecksumTypeFullObject ||
			*done.ChecksumCRC64NVME != sumOf(t, checksum.CRC64NVME, slices.Concat(big, []byte("tail"))) {
			t.Fatalf("Complete = ETag %s, %v %v; want the two parts' ETag and their CRC64NVME FULL_OBJECT", *done.ETag, done.ChecksumCRC64NVME, done.ChecksumType)
		}
		if got, _ := h.readBytes(&s3api.GetObjectInput{Bucket: "parts", Key: "k"}); !bytes.Equal(got, slices.Concat(big, []byte("tail"))) {
			t.Fatalf("object = %d bytes, want parts 1 and 3 only", len(got))
		}
		if refs := blobs.Refs(); len(refs) != 1 {
			t.Fatalf("blob store holds %d blobs, want the object's alone: the unlisted part 2 is discarded", len(refs))
		}
		_, err = complete(h, "parts", "k", id, []s3api.CompletedPart{part(1, *p1.ETag)}, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
	})
}

// TestCompleteMultipartUploadPartOrder: a part list that is not strictly
// ascending is 400 InvalidPartOrder with UploadId, and the upload stays
// in progress (s3.md 10).
func TestCompleteMultipartUploadPartOrder(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("order")
		id := h.startUpload("order", "k", s3api.CreateMultipartUploadInput{})
		p1 := h.sendPart("order", "k", id, 1, payload(4, minPart), s3api.UploadPartInput{})
		p2 := h.sendPart("order", "k", id, 2, []byte("tail"), s3api.UploadPartInput{})
		for name, parts := range map[string][]s3api.CompletedPart{
			"descending": {listed(2, p2), listed(1, p1)},
			"repeated":   {listed(1, p1), listed(1, p1)},
		} {
			_, err := complete(h, "order", "k", id, parts, s3api.CompleteMultipartUploadInput{})
			wantCode(h.T, err, "InvalidPartOrder", http.StatusBadRequest)
			if err.Message != "The list of parts was not in ascending order. Parts must be ordered by part number." {
				h.Errorf("%s: message %q", name, err.Message)
			}
			wantFields(h.T, err, map[string]string{"UploadId": id})
		}
		if parts := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "order", Key: "k", UploadId: id}); len(parts.Parts) != 2 {
			h.Fatalf("ListParts after the refused Completes = %d parts, want both", len(parts.Parts))
		}
	})
}

// TestSnapshotKeepsUploadParts: an upload in progress survives an export,
// a reset and a load as TestSnapshotKeepsObjectBodies runs them: the
// sections name one reference per part, the archive embeds the parts'
// bytes, and Complete after the load assembles the object from them
// (ARCHITECTURE.md 8).
func TestSnapshotKeepsUploadParts(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("snap")
		id := h.startUpload("snap", "k", s3api.CreateMultipartUploadInput{})
		first, second := payload(5, minPart), []byte("tail")
		p1 := h.sendPart("snap", "k", id, 1, first, s3api.UploadPartInput{})
		p2 := h.sendPart("snap", "k", id, 2, second, s3api.UploadPartInput{})

		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		var named []string
		for _, sec := range sections {
			named = append(named, sec.Blobs...)
		}
		archive := fstest.MapFS{}
		for _, hex := range named {
			archive["sha256/"+hex] = &fstest.MapFile{Data: blobBytes(h, blobs, hex)}
		}
		if len(named) != 2 || len(archive) != 2 {
			h.Fatalf("sections name %d references to %d bodies, want one reference per part, 2, to 2 bodies", len(named), len(archive))
		}

		h.svc.Reset(kernel.ResetScope{})
		for _, hex := range named {
			if err := blobs.Release(parseRef(h, hex)); err != nil {
				h.Fatal(err)
			}
		}
		if refs := blobs.Refs(); len(refs) != 0 {
			h.Fatalf("blob store holds %v once the reset state's references are released, want nothing", refs)
		}
		if err := blobs.Import(h.Context(), archive); err != nil {
			h.Fatal(err)
		}
		for _, hex := range named {
			if err := blobs.Retain(parseRef(h, hex)); err != nil {
				h.Fatal(err)
			}
		}
		if err := blobs.Prune(); err != nil {
			h.Fatal(err)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}

		if _, err := complete(h, "snap", "k", id, []s3api.CompletedPart{listed(1, p1), listed(2, p2)}, s3api.CompleteMultipartUploadInput{}); err != nil {
			h.Fatalf("CompleteMultipartUpload after the load: %s %s", err.ID.QueryCode, err.Message)
		}
		if got, _ := h.readBytes(&s3api.GetObjectInput{Bucket: "snap", Key: "k"}); !bytes.Equal(got, slices.Concat(first, second)) {
			h.Fatalf("object after the load = %d bytes, want the %d bytes of both parts", len(got), len(first)+len(second))
		}
		if refs := blobs.Refs(); len(refs) != 1 {
			h.Fatalf("blob store holds %d blobs after Complete, want only the object's", len(refs))
		}
	})
}

// TestDeleteBucketReleasesUploadParts: an upload in progress does not
// block DeleteBucket (s3.md 3.3, UNVERIFIED), and deleting the bucket
// releases its parts' bytes with it.
func TestDeleteBucketReleasesUploadParts(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("gone")
		id := h.startUpload("gone", "k", s3api.CreateMultipartUploadInput{})
		h.sendPart("gone", "k", id, 1, []byte("part"), s3api.UploadPartInput{})
		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "gone"})
		if refs := blobs.Refs(); len(refs) != 0 {
			h.Fatalf("blob store holds %v after DeleteBucket, want the upload's part released", refs)
		}
	})
}

// TestPartNumberReadChecksum: with x-amz-checksum-mode ENABLED, a
// partNumber read of a COMPOSITE object reports that part's own checksum
// with type COMPOSITE, never the object's, which boto3 would validate
// against the part's bytes (s3.md 5.8).
func TestPartNumberReadChecksum(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("sums")
		id := h.startUpload("sums", "k", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32)})
		first, second := payload(6, minPart), []byte("tail")
		p1 := h.sendPart("sums", "k", id, 1, first, s3api.UploadPartInput{})
		p2 := h.sendPart("sums", "k", id, 2, second, s3api.UploadPartInput{})
		if _, err := complete(h, "sums", "k", id, []s3api.CompletedPart{listed(1, p1), listed(2, p2)}, s3api.CompleteMultipartUploadInput{}); err != nil {
			h.Fatalf("CompleteMultipartUpload: %s %s", err.ID.QueryCode, err.Message)
		}
		for n, part := range map[int32][]byte{1: first, 2: second} {
			in := &s3api.HeadObjectInput{Bucket: "sums", Key: "k", PartNumber: new(n), ChecksumMode: new(s3api.ChecksumModeEnabled)}
			out := ok(h, who{}, s3api.OpHeadObject, in)
			if want := sumOf(h.T, checksum.CRC32, part); out.ChecksumCRC32 == nil || *out.ChecksumCRC32 != want ||
				out.ChecksumType == nil || *out.ChecksumType != s3api.ChecksumTypeComposite {
				h.Errorf("HeadObject partNumber=%d = CRC32 %v type %v, want the part's %s COMPOSITE", n, out.ChecksumCRC32, out.ChecksumType, want)
			}
		}
	})
}

// TestUploadPartContentEncoding: a part sent with Content-Encoding:
// aws-chunked and no STREAMING-* x-amz-content-sha256 is decoded as
// PutObject decodes one, so the part holds the decoded bytes and its ETag
// is their MD5, not the framing's (s3.md 6.3).
func TestUploadPartContentEncoding(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("encoded")
		id := h.startUpload("encoded", "k", s3api.CreateMultipartUploadInput{})
		const data = "an encoded part"
		var framed bytes.Buffer
		w := awschunked.NewWriter(&framed, awschunked.PlainChunked, awschunked.WriterOptions{})
		if _, err := w.Write([]byte(data)); err != nil {
			h.Fatal(err)
		}
		if err := w.Finish(); err != nil {
			h.Fatal(err)
		}
		header := http.Header{"Content-Encoding": {"aws-chunked"}, "X-Amz-Decoded-Content-Length": {strconv.Itoa(len(data))}}
		out, err := withHeader(h, s3api.OpUploadPart, header, &s3api.UploadPartInput{
			Bucket: "encoded", Key: "k", UploadId: id, PartNumber: 1, Body: bytesBody(framed.String()),
		})
		if err != nil {
			h.Fatalf("UploadPart with Content-Encoding aws-chunked: %s %s", err.ID.QueryCode, err.Message)
		}
		if *out.ETag != `"`+md5hex(data)+`"` {
			h.Fatalf("UploadPart ETag = %s, want the MD5 of the decoded bytes %q", *out.ETag, md5hex(data))
		}
	})
}

// unquote strips an ETag's quotes.
func unquote(etag string) string { return etag[1 : len(etag)-1] }

// TestUploadPartErrors: a part number outside 1 to 10,000 is
// InvalidArgument; an unknown upload or another key's NoSuchUpload; a part
// checksum of another algorithm than the upload's InvalidRequest, a wrong
// one BadDigest; a part without one is checksummed by S3; a part number
// sent again replaces the part (s3.md 6.4, 10).
func TestUploadPartErrors(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("uploads")
		id := h.startUpload("uploads", "k", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmSha256)})
		for _, n := range []int32{0, 10001} {
			_, err := uploadPart(h, "uploads", "k", id, n, []byte("v"), s3api.UploadPartInput{})
			wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
			if err.Message != "Part number must be an integer between 1 and 10000, inclusive" {
				t.Errorf("part %d: message %q", n, err.Message)
			}
			wantFields(t, err, map[string]string{"ArgumentName": "partNumber", "ArgumentValue": strconv.Itoa(int(n))})
		}
		_, err := uploadPart(h, "uploads", "k", "no-such-upload", 1, []byte("v"), s3api.UploadPartInput{})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
		wantFields(t, err, map[string]string{"UploadId": "no-such-upload"})
		_, err = uploadPart(h, "uploads", "other", id, 1, []byte("v"), s3api.UploadPartInput{})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)

		_, err = uploadPart(h, "uploads", "k", id, 1, []byte("hello"), s3api.UploadPartInput{ChecksumCRC32: new("NhCmhg==")})
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "Checksum Type mismatch occurred, expected checksum Type: sha256, actual checksum Type: crc32" {
			t.Errorf("mismatched algorithm: message %q", err.Message)
		}
		_, err = uploadPart(h, "uploads", "k", id, 1, []byte("hello"), s3api.UploadPartInput{ChecksumSHA256: new(sumOf(t, checksum.SHA256, []byte("other")))})
		wantCode(t, err, "BadDigest", http.StatusBadRequest)
		if err.Message != "The SHA256 you specified did not match the calculated checksum." {
			t.Errorf("wrong SHA256: message %q", err.Message)
		}

		out := h.sendPart("uploads", "k", id, 1, []byte("hello"), s3api.UploadPartInput{})
		if out.ChecksumSHA256 == nil || *out.ChecksumSHA256 != sumOf(t, checksum.SHA256, []byte("hello")) {
			t.Fatalf("UploadPart without a checksum reports SHA256 %v, want S3's own", out.ChecksumSHA256)
		}
		h.sendPart("uploads", "k", id, 1, []byte("hello, again"), s3api.UploadPartInput{})
		parts := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "uploads", Key: "k", UploadId: id})
		if len(parts.Parts) != 1 || *parts.Parts[0].Size != 12 || *parts.Parts[0].ETag != `"`+md5hex("hello, again")+`"` {
			t.Fatalf("ListParts after a part was sent again = %+v, want the second body alone", parts.Parts)
		}
		if refs := blobs.Refs(); len(refs) != 1 {
			t.Fatalf("blob store holds %d blobs, want the replacing part's alone", len(refs))
		}
	})
}

// TestUploadPartTrailer: a part sent as an aws-chunked body with a
// checksum trailer, as the JS SDK sends every part (s3.md 6.3), is decoded
// and its trailer verified at EOF; a wrong trailer value is BadDigest and a
// trailer of another algorithm than the upload's InvalidRequest, and
// neither stores the part (s3.md 6.4).
func TestUploadPartTrailer(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("trailers")
		id := h.startUpload("trailers", "k", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32)})
		const data = "a streamed part"
		crc32 := sumOf(t, checksum.CRC32, []byte(data))
		part := func(n int32, alg s3api.ChecksumAlgorithm, trailer awschunked.Trailer) *s3api.UploadPartInput {
			return &s3api.UploadPartInput{Bucket: "trailers", Key: "k", UploadId: id, PartNumber: n, ChecksumAlgorithm: new(alg), Body: bytesBody(chunkedWith(t, trailer, data))}
		}
		out, err := withHeader(h, s3api.OpUploadPart, chunkedHeader(len(data)), part(1, s3api.ChecksumAlgorithmCrc32, awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: crc32}))
		if err != nil {
			t.Fatalf("UploadPart of an aws-chunked body: %s %s", err.ID.QueryCode, err.Message)
		}
		if *out.ChecksumCRC32 != crc32 || *out.ETag != `"`+md5hex(data)+`"` {
			t.Fatalf("UploadPart = CRC32 %s ETag %s, want the trailer's %s and the MD5 of the decoded bytes", *out.ChecksumCRC32, *out.ETag, crc32)
		}

		_, err = withHeader(h, s3api.OpUploadPart, chunkedHeader(len(data)), part(2, s3api.ChecksumAlgorithmCrc32, awschunked.Trailer{Name: "x-amz-checksum-crc32", Value: "AAAAAA=="}))
		wantCode(t, err, "BadDigest", http.StatusBadRequest)
		if err.Message != "The CRC32 you specified did not match the calculated checksum." {
			t.Errorf("wrong trailer: message %q", err.Message)
		}
		header := chunkedHeader(len(data))
		header.Set("X-Amz-Trailer", "x-amz-checksum-sha256")
		_, err = withHeader(h, s3api.OpUploadPart, header, part(3, s3api.ChecksumAlgorithmSha256, awschunked.Trailer{Name: "x-amz-checksum-sha256", Value: sumOf(t, checksum.SHA256, []byte(data))}))
		wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		if err.Message != "Checksum Type mismatch occurred, expected checksum Type: crc32, actual checksum Type: sha256" {
			t.Errorf("trailer of another algorithm: message %q", err.Message)
		}

		parts := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "trailers", Key: "k", UploadId: id})
		if len(parts.Parts) != 1 || *parts.Parts[0].PartNumber != 1 || *parts.Parts[0].Size != int64(len(data)) {
			t.Fatalf("ListParts = %+v, want part 1 of %d decoded bytes alone", parts.Parts, len(data))
		}
	})
}

// TestAbortMultipartUpload: Abort drops the upload and its parts' bytes;
// the id is dead afterwards, for parts, listings and a second Abort
// (s3.md 10).
func TestAbortMultipartUpload(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("abort")
		id := h.startUpload("abort", "k", s3api.CreateMultipartUploadInput{})
		h.sendPart("abort", "k", id, 1, []byte("part"), s3api.UploadPartInput{})
		ok(h, who{}, s3api.OpAbortMultipartUpload, &s3api.AbortMultipartUploadInput{Bucket: "abort", Key: "k", UploadId: id})
		if refs := blobs.Refs(); len(refs) != 0 {
			t.Fatalf("blob store holds %d blobs after Abort, want none", len(refs))
		}
		_, err := uploadPart(h, "abort", "k", id, 2, []byte("late"), s3api.UploadPartInput{})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
		_, err = as(h, who{}, s3api.OpAbortMultipartUpload, &s3api.AbortMultipartUploadInput{Bucket: "abort", Key: "k", UploadId: id})
		wantCode(t, err, "NoSuchUpload", http.StatusNotFound)
		if ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "abort"}); len(ups.Uploads) != 0 {
			t.Fatalf("ListMultipartUploads after Abort = %d uploads, want none", len(ups.Uploads))
		}
		if refs := blobs.Refs(); len(refs) != 0 {
			t.Fatalf("blob store holds %d blobs after a part for an aborted upload, want none", len(refs))
		}
	})
}

// TestListMultipartUploads: uploads list by key, then by initiation within
// a key; max-uploads pages through them with the key and upload id
// markers, which name the last upload listed; a delimiter groups keys into
// common prefixes; an empty listing has empty markers (s3.md 9.2).
func TestListMultipartUploads(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("ups")
		keys := []string{"b", "a", "a", "dir/x", "dir/y"}
		ids := make([]string, 0, len(keys))
		for _, key := range keys {
			ids = append(ids, h.startUpload("ups", key, s3api.CreateMultipartUploadInput{}))
			if _, err := h.Clock.Advance(time.Second); err != nil {
				t.Fatal(err)
			}
		}
		listUps := func(in s3api.ListMultipartUploadsInput) *s3api.ListMultipartUploadsOutput {
			in.Bucket = "ups"
			return ok(h, who{}, s3api.OpListMultipartUploads, &in)
		}
		keysOf := func(out *s3api.ListMultipartUploadsOutput) []string {
			got := make([]string, 0, len(out.Uploads)+len(out.CommonPrefixes))
			for _, u := range out.Uploads {
				got = append(got, *u.Key+"/"+*u.UploadId)
			}
			for _, p := range out.CommonPrefixes {
				got = append(got, *p.Prefix)
			}
			return got
		}
		all := listUps(s3api.ListMultipartUploadsInput{})
		if want := []string{"a/" + ids[1], "a/" + ids[2], "b/" + ids[0], "dir/x/" + ids[3], "dir/y/" + ids[4]}; !slices.Equal(keysOf(all), want) || *all.IsTruncated {
			t.Fatalf("ListMultipartUploads = %v, want %v", keysOf(all), want)
		}
		if *all.NextKeyMarker != "dir/y" || *all.NextUploadIdMarker != ids[4] || all.Uploads[0].Initiator.ID == nil || all.Uploads[0].Owner.ID == nil {
			t.Fatalf("an untruncated listing's next markers = %s %s, want the last upload; initiator and owner reported", *all.NextKeyMarker, *all.NextUploadIdMarker)
		}
		first := listUps(s3api.ListMultipartUploadsInput{MaxUploads: new(int32(2))})
		if want := []string{"a/" + ids[1], "a/" + ids[2]}; !slices.Equal(keysOf(first), want) || !*first.IsTruncated || *first.NextUploadIdMarker != ids[2] {
			t.Fatalf("first page = %v truncated %v, want %v truncated", keysOf(first), *first.IsTruncated, want)
		}
		second := listUps(s3api.ListMultipartUploadsInput{MaxUploads: new(int32(2)), KeyMarker: new("a"), UploadIdMarker: new(ids[1])})
		if want := []string{"a/" + ids[2], "b/" + ids[0]}; !slices.Equal(keysOf(second), want) || *second.KeyMarker != "a" || *second.UploadIdMarker != ids[1] {
			t.Fatalf("page after a/%s = %v, want %v with the markers echoed", ids[1], keysOf(second), want)
		}
		// REC test_list_multiparts_next_marker step list-multiparts-next-upload-only.
		idOnly := listUps(s3api.ListMultipartUploadsInput{MaxUploads: new(int32(1)), UploadIdMarker: new(ids[2])})
		if want := []string{"a/" + ids[1]}; !slices.Equal(keysOf(idOnly), want) || !*idOnly.IsTruncated || *idOnly.KeyMarker != "" || *idOnly.UploadIdMarker != "" {
			t.Fatalf("upload-id-marker without key-marker = %v truncated %v markers %q %q, want %v truncated with empty markers",
				keysOf(idOnly), *idOnly.IsTruncated, *idOnly.KeyMarker, *idOnly.UploadIdMarker, want)
		}
		if all.Prefix != nil {
			t.Fatalf("a listing without prefix reports Prefix %q, want none", *all.Prefix)
		}
		grouped := listUps(s3api.ListMultipartUploadsInput{Delimiter: new("/"), KeyMarker: new("a")})
		if want := []string{"b/" + ids[0], "dir/"}; !slices.Equal(keysOf(grouped), want) {
			t.Fatalf("delimited listing after a = %v, want %v", keysOf(grouped), want)
		}
		if *grouped.NextKeyMarker != "b" || *grouped.NextUploadIdMarker != ids[0] {
			t.Fatalf("delimited listing next markers = %q %q, want the last upload, not the prefix after it", *grouped.NextKeyMarker, *grouped.NextUploadIdMarker)
		}
		if under := listUps(s3api.ListMultipartUploadsInput{Prefix: new("dir/")}); len(under.Uploads) != 2 || *under.Prefix != "dir/" {
			t.Fatalf("listing under dir/ = %v, want its two uploads", keysOf(under))
		}
		empty := listUps(s3api.ListMultipartUploadsInput{Prefix: new("none")})
		if len(empty.Uploads) != 0 || *empty.NextKeyMarker != "" || *empty.NextUploadIdMarker != "" || *empty.IsTruncated {
			t.Fatalf("empty listing = %v, next %q %q; want empty markers", keysOf(empty), *empty.NextKeyMarker, *empty.NextUploadIdMarker)
		}
		_, err := as(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "ups", EncodingType: new(s3api.EncodingType("base64"))})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)

		// REC test_list_multiparts_next_marker step list-multiparts-wrong-id-for-key.
		_, err = as(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "ups", MaxUploads: new(int32(1)), KeyMarker: new("b"), UploadIdMarker: new(ids[1])})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		if err.Message != "Invalid uploadId marker" {
			t.Errorf("upload-id-marker of another key: message %q", err.Message)
		}
		wantFields(t, err, map[string]string{"ArgumentName": "upload-id-marker", "ArgumentValue": ids[1]})
	})
}

// TestListMultipartUploadsInitiatedPrecision is REC
// test_s3_list_multiparts_timestamp_precision: Initiated is reported in
// whole seconds, whatever the clock's precision.
func TestListMultipartUploadsInitiatedPrecision(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("precise")
		if _, err := h.Clock.Advance(1500 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		now := h.Clock.Now().Wire()
		if now.Equal(now.Truncate(time.Second)) {
			t.Fatalf("clock at %s after a sub-second advance, want a fraction of a second to truncate", now)
		}
		h.startUpload("precise", "k", s3api.CreateMultipartUploadInput{})
		ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "precise"})
		if len(ups.Uploads) != 1 || !ups.Uploads[0].Initiated.Equal(now.Truncate(time.Second)) {
			t.Fatalf("ListMultipartUploads = %+v, want one upload Initiated at %s", ups.Uploads, now.Truncate(time.Second))
		}
	})
}

// TestListMultipartUploadsCommonPrefixMarkers is REC
// test_list_multipart_uploads_marker_common_prefixes: a page of common
// prefixes alone is truncated with empty next markers, since they name
// only uploads, and a key-marker at or inside a common prefix skips all of
// it (s3.md 9.2).
func TestListMultipartUploadsCommonPrefixMarkers(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("folders")
		keys := []string{"folder/aSubfolder/subFile1", "folder/aSubfolder/subFile2", "folder/file1", "folder/file2"}
		ids := make([]string, 0, len(keys))
		for _, key := range keys {
			ids = append(ids, h.startUpload("folders", key, s3api.CreateMultipartUploadInput{}))
		}
		listUps := func(marker string) *s3api.ListMultipartUploadsOutput {
			in := &s3api.ListMultipartUploadsInput{Bucket: "folders", Prefix: new("folder/"), Delimiter: new("/"), MaxUploads: new(int32(1))}
			if marker != "" {
				in.KeyMarker = new(marker)
			}
			return ok(h, who{}, s3api.OpListMultipartUploads, in)
		}
		start := listUps("")
		if len(start.Uploads) != 0 || len(start.CommonPrefixes) != 1 || *start.CommonPrefixes[0].Prefix != "folder/aSubfolder/" || !*start.IsTruncated ||
			*start.NextKeyMarker != "" || *start.NextUploadIdMarker != "" || *start.Prefix != "folder/" {
			t.Fatalf("list-multiparts-start = %+v, want folder/aSubfolder/ alone, truncated, with empty next markers", start)
		}
		for _, marker := range []string{"folder/aSubfolder/", "folder/aSubfolder/subFile1"} {
			out := listUps(marker)
			if len(out.Uploads) != 1 || *out.Uploads[0].Key != "folder/file1" || !*out.IsTruncated || *out.NextKeyMarker != "folder/file1" || *out.NextUploadIdMarker != ids[2] {
				t.Fatalf("listing after %s = %+v, want folder/file1's upload, truncated, as the next markers", marker, out)
			}
		}
	})
}

// TestListParts: parts list in part number order after part-number-marker;
// NextPartNumberMarker is the last part listed, 0 for none; a declared
// upload reports each part's checksum and its algorithm and type
// (s3.md 9.2).
func TestListParts(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("listed")
		id := h.startUpload("listed", "k", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32c)})
		for _, n := range []int32{3, 1, 2} {
			h.sendPart("listed", "k", id, n, []byte(strconv.Itoa(int(n))), s3api.UploadPartInput{})
		}
		page := func(limit int32, marker string) *s3api.ListPartsOutput {
			in := &s3api.ListPartsInput{Bucket: "listed", Key: "k", UploadId: id, MaxParts: new(limit)}
			if marker != "" {
				in.PartNumberMarker = new(marker)
			}
			return ok(h, who{}, s3api.OpListParts, in)
		}
		numbers := func(out *s3api.ListPartsOutput) []int32 {
			got := make([]int32, 0, len(out.Parts))
			for _, p := range out.Parts {
				got = append(got, *p.PartNumber)
			}
			return got
		}
		first := page(2, "")
		if !slices.Equal(numbers(first), []int32{1, 2}) || !*first.IsTruncated || *first.NextPartNumberMarker != "2" || *first.PartNumberMarker != "0" {
			t.Fatalf("first page = %v truncated %v next %s, want [1 2] truncated next 2", numbers(first), *first.IsTruncated, *first.NextPartNumberMarker)
		}
		if *first.Parts[0].ChecksumCRC32C != sumOf(t, checksum.CRC32C, []byte("1")) || *first.ChecksumAlgorithm != s3api.ChecksumAlgorithmCrc32c ||
			*first.ChecksumType != s3api.ChecksumTypeComposite || *first.StorageClass != s3api.StorageClassStandard || first.Initiator == nil || first.Owner == nil {
			t.Fatalf("ListParts = %+v, want CRC32C per part, CRC32C COMPOSITE, STANDARD, an initiator and an owner", first)
		}
		rest := page(2, "2")
		if !slices.Equal(numbers(rest), []int32{3}) || *rest.IsTruncated || *rest.NextPartNumberMarker != "3" || *rest.PartNumberMarker != "2" {
			t.Fatalf("page after 2 = %v next %s, want [3] next 3", numbers(rest), *rest.NextPartNumberMarker)
		}
		past := page(2, "5")
		if len(past.Parts) != 0 || *past.NextPartNumberMarker != "0" || *past.IsTruncated {
			t.Fatalf("page after the last part = %v next %s, want none and next 0", numbers(past), *past.NextPartNumberMarker)
		}
		zero := page(0, "")
		if len(zero.Parts) != 0 || *zero.IsTruncated || *zero.NextPartNumberMarker != "0" || *zero.MaxParts != 0 {
			t.Fatalf("max-parts 0 = %v truncated %v next %s, want none, not truncated, next 0", numbers(zero), *zero.IsTruncated, *zero.NextPartNumberMarker)
		}
	})
}
