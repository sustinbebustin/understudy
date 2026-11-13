package s3_test

import (
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

const (
	ssecAlgorithmHeader = "X-Amz-Server-Side-Encryption-Customer-Algorithm"
	ssecKeyHeader       = "X-Amz-Server-Side-Encryption-Customer-Key"
	ssecKeyMD5Header    = "X-Amz-Server-Side-Encryption-Customer-Key-Md5"
)

// A Complete of an SSE-C upload that declared a checksum algorithm, whose
// request carried the key, answers the customer algorithm and key MD5
// headers, which CompleteMultipartUploadOutput does not model
// (testdata/parity/s3/S3SOL/08-right-key.json: AES256 and the key's MD5).
// A Complete of an upload that is not SSE-C answers neither. The retried
// Complete answers the same headers when it resends the key (inferred;
// unrecorded).
func TestCompleteMultipartUploadEchoesTheCustomerKey(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k).overHTTPS()
		if rec := e.send(http.MethodPut, "/echo", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		unblock := `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault>` +
			`<BlockedEncryptionTypes><EncryptionType>NONE</EncryptionType></BlockedEncryptionTypes></Rule></ServerSideEncryptionConfiguration>`
		if rec := e.send(http.MethodPut, "/echo?encryption", strings.NewReader(unblock), int64(len(unblock)), nil); rec.Code != http.StatusOK {
			t.Fatalf("PutBucketEncryption = %d %s", rec.Code, rec.Body)
		}
		keyed := func() http.Header {
			h := http.Header{}
			h.Set(ssecAlgorithmHeader, *key1.alg())
			h.Set(ssecKeyHeader, key1.key)
			h.Set(ssecKeyMD5Header, key1.md5)
			return h
		}
		// finish uploads one part to key, declaring CRC32 when sum is
		// set, and completes it with completing headers.
		finish := func(key string, upload, completing http.Header) (rec1, rec2 *httptest.ResponseRecorder) {
			create := e.send(http.MethodPost, "/echo/"+key+"?uploads", strings.NewReader(""), 0, upload)
			if create.Code != http.StatusOK {
				t.Fatalf("CreateMultipartUpload %s = %d %s", key, create.Code, create.Body)
			}
			id := between(create.Body.String(), "<UploadId>", "</UploadId>")
			data := "only part"
			part := keyedOrNone(upload)
			if upload.Get("X-Amz-Checksum-Algorithm") != "" {
				sum := binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE([]byte(data)))
				part.Set("X-Amz-Checksum-Crc32", base64.StdEncoding.EncodeToString(sum))
			}
			up := e.send(http.MethodPut, "/echo/"+key+"?partNumber=1&uploadId="+id, strings.NewReader(data), int64(len(data)), part)
			if up.Code != http.StatusOK {
				t.Fatalf("UploadPart %s = %d %s", key, up.Code, up.Body)
			}
			xmlPart := "<Part><PartNumber>1</PartNumber><ETag>" + up.Header().Get("ETag") + "</ETag>"
			if v := up.Header().Get("X-Amz-Checksum-Crc32"); v != "" {
				xmlPart += "<ChecksumCRC32>" + v + "</ChecksumCRC32>"
			}
			body := "<CompleteMultipartUpload>" + xmlPart + "</Part></CompleteMultipartUpload>"
			send := func() *httptest.ResponseRecorder {
				return e.send(http.MethodPost, "/echo/"+key+"?uploadId="+id, strings.NewReader(body), int64(len(body)), completing.Clone())
			}
			return send(), send()
		}

		declared := keyed()
		declared.Set("X-Amz-Checksum-Algorithm", string(s3api.ChecksumAlgorithmCrc32))
		first, again := finish("summed", declared, keyed())
		for name, rec := range map[string]*httptest.ResponseRecorder{"first": first, "retried": again} {
			if rec.Code != http.StatusOK {
				t.Fatalf("%s Complete of the SSE-C upload = %d %s", name, rec.Code, rec.Body)
			}
			if got := rec.Header().Values(ssecAlgorithmHeader); len(got) != 1 || got[0] != "AES256" {
				t.Errorf("%s Complete: customer algorithm = %q, want [AES256]", name, got)
			}
			if got := rec.Header().Values(ssecKeyMD5Header); len(got) != 1 || got[0] != key1.md5 {
				t.Errorf("%s Complete: customer key MD5 = %q, want [%s]", name, got, key1.md5)
			}
		}

		plain, _ := finish("plain", http.Header{}, http.Header{})
		if plain.Code != http.StatusOK {
			t.Fatalf("Complete of the plain upload = %d %s", plain.Code, plain.Body)
		}
		for _, name := range []string{ssecAlgorithmHeader, ssecKeyMD5Header} {
			if got := plain.Header().Values(name); len(got) != 0 {
				t.Errorf("Complete of a non-SSE-C upload answers %s = %q, want none", name, got)
			}
		}
	}, kerneltest.Services(s3api.Service()))
}

// keyedOrNone is the SSE-C headers of upload, which an UploadPart repeats.
func keyedOrNone(upload http.Header) http.Header {
	h := http.Header{}
	for _, name := range []string{ssecAlgorithmHeader, ssecKeyHeader, ssecKeyMD5Header} {
		if v := upload.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	return h
}

// between is the text of s between the first from and the next to after it.
func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	out, _, _ := strings.Cut(rest, to)
	return out
}
