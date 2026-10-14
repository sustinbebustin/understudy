package s3_test

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// TestUploadPartCopy: parts copied from ranges of an object, and from the
// whole of one, complete to those bytes; each part's ETag is the MD5 of the
// bytes copied and its checksum the upload's algorithm over them; the
// source's version is reported on a versioned source bucket (s3.md 7).
func TestUploadPartCopy(t *testing.T) {
	runObjectsWith(t, func(h *harness, blobs *blob.Store) {
		h.createBucket("source")
		ok(h, who{}, s3api.OpPutBucketVersioning, &s3api.PutBucketVersioningInput{
			Bucket: "source", VersioningConfiguration: s3api.VersioningConfiguration{Status: new(s3api.BucketVersioningStatusEnabled)},
		})
		src := payload(7, minPart+10)
		put := ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "source", Key: "src", Body: bytesBody(string(src))})
		h.put("source", "small", "tail bytes")
		h.createBucket("target")
		id := h.startUpload("target", "copy", s3api.CreateMultipartUploadInput{ChecksumAlgorithm: new(s3api.ChecksumAlgorithmSha256)})

		first := ok(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
			Bucket: "target", Key: "copy", UploadId: id, PartNumber: 1, CopySource: "source/src", CopySourceRange: new("bytes=0-5242879"),
		})
		r := first.CopyPartResult
		if *r.ETag != `"`+md5hex(string(src[:minPart]))+`"` || *r.ChecksumSHA256 != sumOf(t, checksum.SHA256, src[:minPart]) || r.LastModified == nil {
			t.Fatalf("UploadPartCopy result = %+v, want the range's MD5 and SHA256", r)
		}
		if first.CopySourceVersionId == nil || *first.CopySourceVersionId != *put.VersionId || *first.ServerSideEncryption != s3api.ServerSideEncryptionAes256 {
			t.Fatalf("UploadPartCopy source version %v, SSE %v; want %s and AES256", first.CopySourceVersionId, first.ServerSideEncryption, *put.VersionId)
		}
		second := ok(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
			Bucket: "target", Key: "copy", UploadId: id, PartNumber: 2, CopySource: "/source/src", CopySourceRange: new("bytes=5242880-5242889"),
		})
		third := ok(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{Bucket: "target", Key: "copy", UploadId: id, PartNumber: 3, CopySource: "source/small"})
		list := []s3api.CompletedPart{
			{PartNumber: new(int32(1)), ETag: first.CopyPartResult.ETag, ChecksumSHA256: first.CopyPartResult.ChecksumSHA256},
			{PartNumber: new(int32(2)), ETag: second.CopyPartResult.ETag, ChecksumSHA256: second.CopyPartResult.ChecksumSHA256},
			{PartNumber: new(int32(3)), ETag: third.CopyPartResult.ETag, ChecksumSHA256: third.CopyPartResult.ChecksumSHA256},
		}
		_, err := complete(h, "target", "copy", id, list, s3api.CompleteMultipartUploadInput{})
		wantCode(t, err, "EntityTooSmall", http.StatusBadRequest)
		if _, err := complete(h, "target", "copy", id, list[:2], s3api.CompleteMultipartUploadInput{}); err != nil {
			t.Fatalf("Complete of the copied parts: %s %s", err.ID.QueryCode, err.Message)
		}
		if got, _ := h.readBytes(&s3api.GetObjectInput{Bucket: "target", Key: "copy"}); !bytes.Equal(got, src) {
			t.Fatalf("object = %d bytes, want the %d bytes of the source", len(got), len(src))
		}
		if refs := blobs.Refs(); len(refs) != 2 {
			t.Fatalf("blob store holds %d blobs, want the two sources', which the copy's equal bytes share, and no part", len(refs))
		}
	})
}

// TestUploadPartCopyOverHTTP: a StatusOKError rule on UploadPartCopy lets
// the copy store its part, then answers 200 with an <Error> document after
// the XML declaration in place of the CopyPartResult (wire-rest.md 3.7);
// ListParts shows the part afterwards.
func TestUploadPartCopyOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		send := func(method, target, body string, header http.Header, want int) string {
			t.Helper()
			rec := e.send(method, target, strings.NewReader(body), int64(len(body)), header)
			if rec.Code != want {
				t.Fatalf("%s %s = %d %s, want %d", method, target, rec.Code, rec.Body, want)
			}
			return rec.Body.String()
		}
		send(http.MethodPut, "/wire", "", nil, http.StatusOK)
		const source = "source bytes"
		send(http.MethodPut, "/wire/src", source, nil, http.StatusOK)
		var created struct {
			UploadID string `xml:"UploadId"`
		}
		if err := xml.Unmarshal([]byte(send(http.MethodPost, "/wire/dst?uploads", "", nil, http.StatusOK)), &created); err != nil || created.UploadID == "" {
			t.Fatalf("CreateMultipartUpload: no upload id (%v)", err)
		}
		id := created.UploadID

		if _, err := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{s3api.OpUploadPartCopy.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.StatusOKError{Shape: s3api.IdentityInvalidArgument().Shape},
			Trigger: fault.NextN{N: 1},
		}); err != nil {
			t.Fatal(err)
		}
		got := send(http.MethodPut, "/wire/dst?partNumber=1&uploadId="+id, "", http.Header{"X-Amz-Copy-Source": {"/wire/src"}}, http.StatusOK)
		if !strings.HasPrefix(got, `<?xml version="1.0" encoding="UTF-8"?>`) || !strings.Contains(got, "<Error><Code>InvalidArgument</Code>") {
			t.Fatalf("faulted UploadPartCopy = 200 %q, want an <Error> document with the injected code after the XML declaration", got)
		}

		var listed struct {
			Parts []struct {
				PartNumber int32  `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.Unmarshal([]byte(send(http.MethodGet, "/wire/dst?uploadId="+id, "", nil, http.StatusOK)), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed.Parts) != 1 || listed.Parts[0].PartNumber != 1 || listed.Parts[0].ETag != `"`+md5hex(source)+`"` {
			t.Fatalf("ListParts after the faulted copy = %+v, want part 1 with the source's MD5", listed.Parts)
		}
	}, kerneltest.Services(s3api.Service()))
}

// TestUploadPartCopyErrors: an x-amz-copy-source-range that is not
// bytes=first-last is InvalidArgument naming it, as is one past the
// source's end; the copy-source conditions fail with 412; a missing source
// or upload is answered as such, and a part number outside 1 to 10,000 is
// InvalidArgument (s3.md 5.10, 7, 10).
func TestUploadPartCopyErrors(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("copies")
		h.put("copies", "src", "0123456789")
		id := h.startUpload("copies", "dst", s3api.CreateMultipartUploadInput{})
		copyPart := func(in s3api.UploadPartCopyInput) *awserr.Error {
			in.Bucket, in.Key = "copies", "dst"
			if in.UploadId == "" {
				in.UploadId = id
			}
			if in.PartNumber == 0 {
				in.PartNumber = 1
			}
			if in.CopySource == "" {
				in.CopySource = "copies/src"
			}
			_, err := as(h, who{}, s3api.OpUploadPartCopy, &in)
			return err
		}
		for _, bad := range []string{"0-8", "bytes=1-0", "bytes=-1-", "bytes=0--1", "bytes=a-b"} {
			_, err := as(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
				Bucket: "copies", Key: "dst", UploadId: id, PartNumber: 1, CopySource: "copies/src", CopySourceRange: new(bad),
			})
			wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
			if err.Message != "The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last bytes to copy" {
				t.Errorf("range %q: message %q", bad, err.Message)
			}
			wantFields(t, err, map[string]string{"ArgumentName": "x-amz-copy-source-range", "ArgumentValue": bad})
		}
		// testdata/parity/s3/R12/09-upload-part-copy-range-beyond.json
		_, err := as(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
			Bucket: "copies", Key: "dst", UploadId: id, PartNumber: 1, CopySource: "copies/src", CopySourceRange: new("bytes=0-99999999"),
		})
		wantCode(t, err, "InvalidArgument", http.StatusBadRequest)
		if err.Message != "Range specified is not valid for source object of size: 10" {
			t.Errorf("range past the end: message %q", err.Message)
		}
		wantFieldOrder(t, err, "ArgumentName", "ArgumentValue")
		wantFields(t, err, map[string]string{"ArgumentName": "x-amz-copy-source-range", "ArgumentValue": "bytes=0-99999999"})

		_, err = as(h, who{}, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{
			Bucket: "copies", Key: "dst", UploadId: id, PartNumber: 1, CopySource: "copies/src", CopySourceIfMatch: new(`"not-the-etag"`),
		})
		wantCode(t, err, "PreconditionFailed", http.StatusPreconditionFailed)
		wantFields(t, err, map[string]string{"Condition": "x-amz-copy-source-If-Match"})

		for name, tc := range map[string]struct {
			in   s3api.UploadPartCopyInput
			code string
		}{
			"missing key":    {s3api.UploadPartCopyInput{CopySource: "copies/none"}, "NoSuchKey"},
			"missing bucket": {s3api.UploadPartCopyInput{CopySource: "nowhere/src"}, "NoSuchBucket"},
			"missing upload": {s3api.UploadPartCopyInput{UploadId: "no-such-upload"}, "NoSuchUpload"},
			"part 10001":     {s3api.UploadPartCopyInput{PartNumber: 10001}, "InvalidArgument"},
		} {
			if err := copyPart(tc.in); err == nil || err.ID.QueryCode != tc.code {
				t.Errorf("%s = %v, want %s", name, err, tc.code)
			}
		}
		parts := ok(h, who{}, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "copies", Key: "dst", UploadId: id})
		if len(parts.Parts) != 0 {
			t.Fatalf("refused copies left parts %+v", parts.Parts)
		}
	})
}
