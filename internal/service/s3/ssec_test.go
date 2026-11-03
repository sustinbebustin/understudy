package s3_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // SSE-C sends the MD5 of the customer key; the test recomputes it
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// s3CodeStatus is the HTTP status of each S3 error code a switch may
// declare, from the S3 error code list
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html).
var s3CodeStatus = map[string]int{
	"AccessDenied":    403,
	"InvalidArgument": 400,
	"InvalidRequest":  400,
	"MalformedXML":    400,
}

// switchValue returns the Value of the fidelity switch name. A switch the
// service does not declare (s3-ssec-object-lock.md 11.4) fails the test,
// which goes on to check the rest.
func (h *harness) switchValue(name string) string {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	h.Errorf("the S3 service declares no switch %s (s3-ssec-object-lock.md 11.4)", name)
	return ""
}

// switchCode returns the error code a code-valued switch declares, with
// its status; a status of 0 when it declares none this test knows.
func (h *harness) switchCode(name string) (code string, status int) {
	h.Helper()
	code = h.switchValue(name)
	status, ok := s3CodeStatus[code]
	if !ok {
		h.Errorf("switch %s declares %q, which is not an S3 error code this test knows", name, code)
	}
	return code, status
}

// follows fails the test unless the switch name declares value: the
// behavior the test goes on to check is the one that value names.
func (h *harness) follows(name, value string) {
	h.Helper()
	if got := h.switchValue(name); got != value {
		h.Errorf("switch %s declares %q; this test checks the behavior of %q", name, got, value)
	}
}

// failure is an expected error: status, code, message and context elements
// by name (LocalStack's snapshots do not record element order).
type failure struct {
	status  int
	code    string
	message string
	fields  map[string]string
}

// is fails the test unless err is f.
func (f failure) is(t *testing.T, what string, err *awserr.Error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: got success, want %d %s %q", what, f.status, f.code, f.message)
		return
	}
	if err.ID.Status != f.status || err.ID.QueryCode != f.code || err.Message != f.message {
		t.Errorf("%s: got %d %s %q, want %d %s %q", what, err.ID.Status, err.ID.QueryCode, err.Message, f.status, f.code, f.message)
		return
	}
	got := map[string]string{}
	for _, x := range err.Fields {
		got[x.Name] = x.Value
	}
	if len(got) != len(f.fields) {
		t.Errorf("%s: elements %v, want %v", what, got, f.fields)
		return
	}
	for name, value := range f.fields {
		if got[name] != value {
			t.Errorf("%s: elements %v, want %v", what, got, f.fields)
			return
		}
	}
}

// SSE-C texts of s3-ssec-object-lock.md 3.2, 3.3 and 3.6; recorded ones
// cite LocalStack's test_s3.snapshot.json (localstack@8b9a79f, validated
// 2026-02-21).
const (
	textConflict    = "Server Side Encryption with Customer provided key is incompatible with the encryption method specified"                           // L12706
	textAlgorithm   = "The Encryption request you specified is not valid. Supported value: AES256."                                                      // L12718
	textNoAlgorithm = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."                // L12730
	textNoKey       = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."                   // L12741
	textNoMD5       = "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key." // S3SOL-03 03-put
	textBadKey      = "The secret key was invalid for the specified algorithm."                                                                          // L12763
	textBadMD5      = "The calculated MD5 hash of the key did not match the hash that was provided."                                                     // L12774
	textNeedsKey    = "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object."    // L12801, L12950
	textWrongKey    = "Requests specifying Server Side Encryption with Customer provided keys must provide the correct secret key."                      // L12811, L12855
	textNotSSEC     = "The encryption parameters are not applicable to this object."                                                                     // testdata/parity/s3/R15/x-ssec-on-non-ssec-read.json
	textInsecure    = "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection."
	textInitiate    = "The multipart upload initiate requested encryption. Subsequent part requests must include the appropriate encryption parameters." // L13133, L13154
	textMismatch    = "The provided encryption parameters did not match the ones used originally."                                                       // L13164
)

// sseArgument is the ArgumentName of every SSE-C validation refusal, copy
// source included (s3-ssec-object-lock.md 3.2).
const sseArgument = "x-amz-server-side-encryption"

// overTLS is the default account's root over HTTPS, as every SSE-C
// request must be (s3-ssec-object-lock.md 3.9).
var overTLS = who{tls: true}

// customer is an SSE-C key as a client sends it: the base64 of the 32 key
// bytes and the base64 MD5 of those bytes (s3-ssec-object-lock.md 3.1).
type customer struct {
	raw      []byte
	key, md5 string
}

func customerKey(raw string) customer {
	sum := md5.Sum([]byte(raw)) //nolint:gosec // see the import
	return customer{raw: []byte(raw), key: base64.StdEncoding.EncodeToString([]byte(raw)), md5: base64.StdEncoding.EncodeToString(sum[:])}
}

// The two keys of LocalStack's TestS3SSECEncryption (test_s3.py L12059-L12060).
var (
	key1 = customerKey("1234567890abcdef1234567890abcdef")
	key2 = customerKey("abcdef1234567890abcdef1234567890")
)

// doubled is key1 as botocore sends it when the caller passes the base64
// form without an MD5: base64 again, 44 bytes decoded, with its MD5
// (s3-ssec-object-lock.md 3.2, 3.12).
var doubled = customerKey(key1.key)

// alg, k and m are the three header values of c.
func (c customer) alg() *string { return new("AES256") }
func (c customer) k() *string   { return new(c.key) }
func (c customer) m() *string   { return new(c.md5) }

// allowSSEC unblocks SSE-C writes to bucket with the documented rule: the
// AES256 default with BlockedEncryptionTypes NONE
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/blocking-unblocking-s3-c-encryption-gpb.html).
func (h *harness) allowSSEC(bucket string) {
	h.Helper()
	h.setBlock(bucket, s3api.EncryptionTypeNone)
}

// setBlock puts bucket's encryption rule with BlockedEncryptionTypes of t.
func (h *harness) setBlock(bucket string, t s3api.EncryptionType) {
	h.Helper()
	ok(h, who{}, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{Bucket: bucket, ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{
		Rules: []s3api.ServerSideEncryptionRule{{
			ApplyServerSideEncryptionByDefault: &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256},
			BlockedEncryptionTypes:             &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{t}},
		}},
	}})
}

// putSSEC writes content to bucket/key under c over HTTPS.
func (h *harness) putSSEC(bucket, key, content string, c customer) (*s3api.PutObjectOutput, *awserr.Error) {
	return as(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{
		Bucket: bucket, Key: key, Body: bytesBody(content),
		SSECustomerAlgorithm: c.alg(), SSECustomerKey: c.k(), SSECustomerKeyMD5: c.m(),
	})
}

// mustPutSSEC is putSSEC that fails the test on an error.
func (h *harness) mustPutSSEC(bucket, key, content string, c customer) *s3api.PutObjectOutput {
	h.Helper()
	out, err := h.putSSEC(bucket, key, content, c)
	if err != nil {
		h.Fatalf("SSE-C PutObject %s/%s: %s %q", bucket, key, err.ID.QueryCode, err.Message)
	}
	return out
}

// readAs reads in whole as w and fails the test on an error.
func (h *harness) readAs(w who, in *s3api.GetObjectInput) (string, *s3api.GetObjectOutput) {
	h.Helper()
	out := ok(h, w, s3api.OpGetObject, in)
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

// randomETag is an SSE-C ETag: 32 hex digits that are not the content's
// MD5 (s3-ssec-object-lock.md 3.4).
var randomETag = regexp.MustCompile(`^"[0-9a-f]{32}"$`)

// echoesKey fails the test unless an SSE-C response reports AES256 and the
// request's key MD5 and no other server-side encryption
// (s3-ssec-object-lock.md 3.4; LocalStack test_s3.snapshot.json L12790).
func echoesKey(t *testing.T, what string, alg, keyMD5 *string, sse *s3api.ServerSideEncryption, kmsKey *string, c customer) {
	t.Helper()
	if alg == nil || *alg != "AES256" || keyMD5 == nil || *keyMD5 != c.md5 || sse != nil || kmsKey != nil {
		t.Errorf("%s: SSECustomerAlgorithm %v, SSECustomerKeyMD5 %v, ServerSideEncryption %v, SSEKMSKeyId %v; want AES256, %s and no other encryption",
			what, deref(alg), deref(keyMD5), deref(sse), deref(kmsKey), c.md5)
	}
}

// deref is *p, or "<nil>".
func deref[T any](p *T) any {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// str is *p, or "".
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// TestSSECRoundTrip: on a bucket that allows SSE-C, a PUT over HTTPS
// echoes the algorithm and key MD5 and no x-amz-server-side-encryption;
// its ETag is 32 hex digits that are not the MD5 and differ per write of
// the same bytes (LocalStack test_s3.snapshot.json L12790, L12902, L13179);
// with no checksum sent the default CRC64NVME is stored as for any object
// (L17518; s3-ssec-object-lock.md 3.8). GET, HEAD and GetObjectAttributes
// with the key succeed (L13190, L13204, L13221) and DeleteObject needs no
// key (L13230).
func TestSSECRoundTrip(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		const content = "test_data"
		out := h.mustPutSSEC("ssec", "obj", content, key1)
		echoesKey(t, "PutObject", out.SSECustomerAlgorithm, out.SSECustomerKeyMD5, out.ServerSideEncryption, out.SSEKMSKeyId, key1)
		if out.BucketKeyEnabled != nil {
			t.Errorf("PutObject reports BucketKeyEnabled %v for SSE-C", *out.BucketKeyEnabled)
		}
		if out.ETag == nil || !randomETag.MatchString(*out.ETag) || *out.ETag == `"`+md5hex(content)+`"` {
			t.Errorf("PutObject ETag = %v, want 32 hex digits that are not md5(%q)", deref(out.ETag), content)
		}
		again := h.mustPutSSEC("ssec", "obj", content, key1)
		if again.ETag == nil || out.ETag == nil || *again.ETag == *out.ETag {
			t.Errorf("two SSE-C PUTs of the same bytes gave ETags %v and %v, want different", deref(out.ETag), deref(again.ETag))
		}
		got, get := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		if got != content {
			t.Errorf("GetObject with the key = %q, want %q", got, content)
		}
		echoesKey(t, "GetObject", get.SSECustomerAlgorithm, get.SSECustomerKeyMD5, get.ServerSideEncryption, get.SSEKMSKeyId, key1)
		head := ok(h, overTLS, s3api.OpHeadObject, &s3api.HeadObjectInput{
			Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
			ChecksumMode: new(s3api.ChecksumModeEnabled),
		})
		echoesKey(t, "HeadObject", head.SSECustomerAlgorithm, head.SSECustomerKeyMD5, head.ServerSideEncryption, head.SSEKMSKeyId, key1)
		if head.ChecksumCRC64NVME == nil || head.ChecksumType == nil || *head.ChecksumType != s3api.ChecksumTypeFullObject {
			t.Errorf("HeadObject checksum = %v %v, want the default CRC64NVME, FULL_OBJECT", deref(head.ChecksumCRC64NVME), deref(head.ChecksumType))
		}
		attrs := ok(h, overTLS, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{
			Bucket: "ssec", Key: "obj", ObjectAttributes: []s3api.ObjectAttributes{s3api.ObjectAttributesEtag, s3api.ObjectAttributesObjectSize},
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		if attrs.ObjectSize == nil || *attrs.ObjectSize != int64(len(content)) {
			t.Errorf("GetObjectAttributes ObjectSize = %v, want %d", deref(attrs.ObjectSize), len(content))
		}
		ok(h, overTLS, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "ssec", Key: "obj"})
		if _, err := as(h, overTLS, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()}); err == nil {
			t.Error("the SSE-C object is still there after DeleteObject")
		}
	})
}

// TestSSECReads: reads of an SSE-C object compare the request with the
// stored key (s3-ssec-object-lock.md 3.3): no headers, the wrong key, a
// wrong algorithm, a 44-byte key with its MD5, SSE-C headers on a plain
// object, and plain HTTP each get their refusal; each version has its own
// key (3.7).
func TestSSECReads(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		h.mustPutSSEC("ssec", "enc", "test_data", key1)
		h.put("ssec", "plain", "plain")
		get := func(w who, key string, alg, k, m *string) *awserr.Error {
			_, err := as(h, w, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ssec", Key: key, SSECustomerAlgorithm: alg, SSECustomerKey: k, SSECustomerKeyMD5: m})
			return err
		}
		head := func(w who, key string, alg, k, m *string) *awserr.Error {
			_, err := as(h, w, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: key, SSECustomerAlgorithm: alg, SSECustomerKey: k, SSECustomerKeyMD5: m})
			return err
		}
		attrs := func(w who, key string, alg, k, m *string) *awserr.Error {
			_, err := as(h, w, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{
				Bucket: "ssec", Key: key, ObjectAttributes: []s3api.ObjectAttributes{s3api.ObjectAttributesEtag},
				SSECustomerAlgorithm: alg, SSECustomerKey: k, SSECustomerKeyMD5: m,
			})
			return err
		}
		needsKey := failure{status: 400, code: "InvalidRequest", message: textNeedsKey}
		wrongKey := failure{status: 403, code: "AccessDenied", message: textWrongKey}
		badAlg := failure{status: 400, code: "InvalidEncryptionAlgorithmError", message: textAlgorithm, fields: map[string]string{"ArgumentName": sseArgument, "ArgumentValue": "KMS"}}
		kms := new("KMS")

		needsKey.is(t, "GET without the key (L12801)", get(overTLS, "enc", nil, nil, nil))
		wrongKey.is(t, "GET with another key (L12811)", get(overTLS, "enc", key2.alg(), key2.k(), key2.m()))
		badAlg.is(t, "GET with algorithm KMS (L12821)", get(overTLS, "enc", kms, key1.k(), key1.m()))
		wrongKey.is(t, "GET with the 44-byte key (L12855)", get(overTLS, "enc", doubled.alg(), doubled.k(), doubled.m()))
		wrongKey.is(t, "HEAD with the 44-byte key (L12865)", head(overTLS, "enc", doubled.alg(), doubled.k(), doubled.m()))
		badAlg.is(t, "HEAD with algorithm KMS (L12833)", head(overTLS, "enc", kms, key1.k(), key1.m()))
		badAlg.is(t, "GetObjectAttributes with algorithm KMS (L12843)", attrs(overTLS, "enc", kms, key1.k(), key1.m()))
		failure{status: 400, code: "InvalidRequest", message: textNotSSEC}.is(t, "GET of a plain object with SSE-C headers (R15)", get(overTLS, "plain", key1.alg(), key1.k(), key1.m()))

		// S3SOL-03 and S3SOL-04, recorded 2026-11-03: a GET with the key and
		// no MD5 succeeds and echoes the MD5 S3 computed (03-get); a 10-byte
		// key with its MD5 is a wrong key (04-get-short-key); an MD5 of
		// another key is the write-side MD5 text (04-get-bad-md5).
		if got, out := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "ssec", Key: "enc", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k()}); got != "test_data" {
			t.Errorf("GET with the key and no MD5 = %q, want test_data", got)
		} else {
			echoesKey(t, "GET with the key and no MD5", out.SSECustomerAlgorithm, out.SSECustomerKeyMD5, out.ServerSideEncryption, out.SSEKMSKeyId, key1)
		}
		short := customerKey("0123456789")
		wrongKey.is(t, "GET with a 10-byte key and its MD5", get(overTLS, "enc", short.alg(), short.k(), short.m()))
		failure{status: 400, code: "InvalidArgument", message: textBadMD5, fields: map[string]string{"ArgumentName": sseArgument}}.is(t, "GET with another key's MD5", get(overTLS, "enc", key1.alg(), key1.k(), key2.m()))

		// Switch s3.sse-c.metadata-without-key: HEAD and GetObjectAttributes
		// without the key are refused as GET is.
		code, status := h.switchCode("s3.sse-c.metadata-without-key")
		wantCode(t, head(overTLS, "enc", nil, nil, nil), code, status)
		failure{status: status, code: code, message: textNeedsKey}.is(t, "GetObjectAttributes without the key", attrs(overTLS, "enc", nil, nil, nil))

		// Switch s3.sse-c.plain-http: reads with SSE-C headers over plain
		// HTTP are refused too, before the stored encryption is compared.
		h.follows("s3.sse-c.plain-http", "refused")
		insecure := failure{status: 400, code: "InvalidRequest", message: textInsecure}
		insecure.is(t, "GET over HTTP with the key", get(who{}, "enc", key1.alg(), key1.k(), key1.m()))
		insecure.is(t, "GET of a plain object over HTTP with SSE-C headers", get(who{}, "plain", key1.alg(), key1.k(), key1.m()))
		wantCode(t, head(who{}, "enc", key1.alg(), key1.k(), key1.m()), "InvalidRequest", 400)
		insecure.is(t, "GetObjectAttributes over HTTP with the key", attrs(who{}, "enc", key1.alg(), key1.k(), key1.m()))

		h.createBucket("versions")
		h.enableVersioning("versions")
		h.allowSSEC("versions")
		v1 := h.mustPutSSEC("versions", "k", "one", key1)
		h.mustPutSSEC("versions", "k", "two", key2)
		_, err := as(h, overTLS, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "versions", Key: "k", VersionId: v1.VersionId, SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m()})
		wrongKey.is(t, "GET of version 1 with version 2's key (L13301)", err)
		if got, _ := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "versions", Key: "k", VersionId: v1.VersionId, SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()}); got != "one" {
			t.Errorf("GET of version 1 with its key = %q, want %q", got, "one")
		}
	})
}

// ssecCase is one SSE-C header trio and the refusal it gets.
type ssecCase struct {
	name      string
	sse       *s3api.ServerSideEncryption
	alg, k, m *string
	want      failure
}

// writeCases are the write-side refusals of s3-ssec-object-lock.md 3.2 in
// the order of its checks (11.3).
func writeCases() []ssecCase {
	arg := map[string]string{"ArgumentName": sseArgument}
	short := customerKey("1234567890")
	return []ssecCase{
		{
			name: "SSE header with SSE-C (L12706)", sse: new(s3api.ServerSideEncryptionAes256), alg: new("KMS"), k: key1.k(), m: key1.m(),
			want: failure{status: 400, code: "InvalidArgument", message: textConflict, fields: map[string]string{"ArgumentName": sseArgument, "ArgumentValue": "AES256"}},
		},
		{
			name: "algorithm KMS (L12718)", alg: new("KMS"), k: key1.k(), m: key1.m(),
			want: failure{status: 400, code: "InvalidEncryptionAlgorithmError", message: textAlgorithm, fields: map[string]string{"ArgumentName": sseArgument, "ArgumentValue": "KMS"}},
		},
		{name: "no algorithm (L12730)", k: key1.k(), m: key1.m(), want: failure{status: 400, code: "InvalidArgument", message: textNoAlgorithm, fields: arg}},
		{name: "no key (L12741)", alg: key1.alg(), m: key1.m(), want: failure{status: 400, code: "InvalidArgument", message: textNoKey, fields: arg}},
		{name: "no MD5 (S3SOL-03 03-put)", alg: key1.alg(), k: key1.k(), want: failure{status: 400, code: "InvalidArgument", message: textNoMD5, fields: arg}},
		{name: "10-byte key (L12763)", alg: short.alg(), k: short.k(), m: short.m(), want: failure{status: 400, code: "InvalidArgument", message: textBadKey, fields: arg}},
		{name: "44-byte key (L12752)", alg: doubled.alg(), k: doubled.k(), m: doubled.m(), want: failure{status: 400, code: "InvalidArgument", message: textBadKey, fields: arg}},
		{name: "key not base64 (S3SOL-04 04-put-not-base64)", alg: key1.alg(), k: new("not base64!"), m: key1.m(), want: failure{status: 400, code: "InvalidArgument", message: textBadMD5, fields: arg}},
		{name: "MD5 of another key (L12774)", alg: key1.alg(), k: key1.k(), m: key2.m(), want: failure{status: 400, code: "InvalidArgument", message: textBadMD5, fields: arg}},
	}
}

// TestSSECWriteValidation: PutObject, CopyObject and CreateMultipartUpload
// validate the SSE-C trio as s3-ssec-object-lock.md 3.2 orders it, before
// the bucket's block (4.3; S3SOL-01 01-put-bad-algo, recorded 2026-11-03)
// and before the body is read; nothing is written.
func TestSSECWriteValidation(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("blocked")
		h.put("blocked", "src", "source")
		for _, c := range writeCases() {
			want := c.want
			_, err := as(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{
				Bucket: "blocked", Key: "refused", Body: &body{r: unreadable{t: h.T}, n: 1},
				ServerSideEncryption: c.sse, SSECustomerAlgorithm: c.alg, SSECustomerKey: c.k, SSECustomerKeyMD5: c.m,
			})
			want.is(t, "PutObject "+c.name, err)
			_, err = as(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{
				Bucket: "blocked", Key: "refused", CopySource: "blocked/src",
				ServerSideEncryption: c.sse, SSECustomerAlgorithm: c.alg, SSECustomerKey: c.k, SSECustomerKeyMD5: c.m,
			})
			want.is(t, "CopyObject target "+c.name, err)
			_, err = as(h, overTLS, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{
				Bucket: "blocked", Key: "refused",
				ServerSideEncryption: c.sse, SSECustomerAlgorithm: c.alg, SSECustomerKey: c.k, SSECustomerKeyMD5: c.m,
			})
			want.is(t, "CreateMultipartUpload "+c.name, err)
		}
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "blocked", Key: "refused"}); err == nil {
			t.Error("a refused SSE-C write left an object")
		}
		if ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: "blocked"}); len(ups.Uploads) != 0 {
			t.Errorf("a refused SSE-C CreateMultipartUpload left %d uploads", len(ups.Uploads))
		}
	})
}

// blocked is the 403 of an SSE-C write by the default account's root to
// bucket/key while the bucket blocks SSE-C, naming action: s3:PutObject
// for PutObject and CreateMultipartUpload, s3:CopyObject for CopyObject
// (s3-ssec-object-lock.md 4.3; S3SOL-01, recorded 2026-11-03).
func blocked(action, bucket, key string) failure {
	return failure{status: 403, code: "AccessDenied", message: "User: arn:aws:iam::" + aws.DefaultAccount().String() + ":root is not authorized to perform: " + action +
		` on resource: "arn:aws:s3:::` + bucket + "/" + key + `" because this bucket has blocked upload requests that specify Server Side Encryption with Customer provided keys (SSE-C).` +
		" Please specify a different server-side encryption type."}
}

// TestSSECBlockedBucket: a new bucket blocks SSE-C writes (testdata/parity/s3/R06/01-get-get-bucket-encryption.json;
// s3-ssec-object-lock.md 4.1): PutObject, CopyObject's target,
// CreateMultipartUpload and UploadPart are refused with the recorded 403
// (4.3; S3SOL-01, recorded 2026-11-03), before the scheme is checked,
// while copy-source SSE-C headers are a read the block does not refuse
// (S3SOL-01 01-copy-source-only). A bucket in me-south-1 allows SSE-C from
// the start (4.1).
func TestSSECBlockedBucket(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("blocked")
		h.put("blocked", "src", "source")
		_, err := h.putSSEC("blocked", "k", "v", key1)
		blocked("s3:PutObject", "blocked", "k").is(t, "PutObject", err)
		_, err = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "blocked", Key: "k", Body: bytesBody("v"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		blocked("s3:PutObject", "blocked", "k").is(t, "PutObject over HTTP", err)
		_, err = as(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "blocked", Key: "k", CopySource: "blocked/src", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		blocked("s3:CopyObject", "blocked", "k").is(t, "CopyObject target", err)
		_, err = as(h, overTLS, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{Bucket: "blocked", Key: "k", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		blocked("s3:PutObject", "blocked", "k").is(t, "CreateMultipartUpload", err)
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "blocked", Key: "k"}); err == nil {
			t.Error("a blocked SSE-C write left an object")
		}

		h.createBucket("open")
		h.allowSSEC("open")
		h.mustPutSSEC("open", "enc", "secret", key1)
		copied := ok(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "blocked", Key: "decrypted", CopySource: "open/enc",
			CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m(),
		})
		if copied.SSECustomerAlgorithm != nil {
			t.Errorf("a copy out of SSE-C reports SSECustomerAlgorithm %s", *copied.SSECustomerAlgorithm)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "blocked", Key: "decrypted"}); got != "secret" {
			t.Errorf("copy into the blocked bucket = %q, want %q", got, "secret")
		}

		id := h.startUploadAs(overTLS, "open", "parts", s3api.CreateMultipartUploadInput{SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		h.setBlock("open", s3api.EncryptionTypeSseC)
		_, err = as(h, overTLS, s3api.OpUploadPart, &s3api.UploadPartInput{
			Bucket: "open", Key: "parts", UploadId: id, PartNumber: 1, Body: bytesBody("part"),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		// UploadPart names s3:PutObject, its IAM action (inferred: S3SOL-01
		// recorded no part).
		blocked("s3:PutObject", "open", "parts").is(t, "UploadPart after the bucket blocked SSE-C again", err)
		_, err = h.putSSEC("open", "late", "v", key1)
		blocked("s3:PutObject", "open", "late").is(t, "PutObject after the bucket blocked SSE-C again", err)

		ok(h, signed("me-south-1"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "bahrain", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("me-south-1")),
		}})
		if _, err := as(h, who{region: "me-south-1", tls: true}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "bahrain", Key: "k", Body: bytesBody("v"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		}); err != nil {
			t.Errorf("SSE-C PutObject to a new me-south-1 bucket: %s %q, want accepted", err.ID.QueryCode, err.Message)
		}
	})
}

// startUploadAs creates an upload of bucket/key as w and returns its id.
func (h *harness) startUploadAs(w who, bucket, key string, in s3api.CreateMultipartUploadInput) string {
	h.Helper()
	in.Bucket, in.Key = bucket, key
	out := ok(h, w, s3api.OpCreateMultipartUpload, &in)
	if out.UploadId == nil || *out.UploadId == "" {
		h.Fatal("CreateMultipartUpload returned no upload id")
	}
	return *out.UploadId
}

// TestSSECPlainHTTP: on a bucket that allows SSE-C, SSE-C writes over
// plain HTTP are refused (switch s3.sse-c.plain-http; "Amazon S3 rejects
// any requests made over HTTP when using SSE-C",
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html).
func TestSSECPlainHTTP(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.sse-c.plain-http", "refused")
		insecure := failure{status: 400, code: "InvalidRequest", message: textInsecure}
		h.createBucket("open")
		h.allowSSEC("open")
		h.put("open", "src", "source")
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "open", Key: "k", Body: bytesBody("v"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		insecure.is(t, "PutObject", err)
		_, err = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "open", Key: "k", CopySource: "open/src", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		insecure.is(t, "CopyObject target", err)
		_, err = as(h, who{}, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{Bucket: "open", Key: "k", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		insecure.is(t, "CreateMultipartUpload", err)
		id := h.startUploadAs(overTLS, "open", "parts", s3api.CreateMultipartUploadInput{SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		_, err = as(h, who{}, s3api.OpUploadPart, &s3api.UploadPartInput{
			Bucket: "open", Key: "parts", UploadId: id, PartNumber: 1, Body: bytesBody("part"),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		insecure.is(t, "UploadPart", err)
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "open", Key: "k"}); err == nil {
			t.Error("an SSE-C write over HTTP left an object")
		}
	})
}

// TestSSECCopy: copy-source headers decrypt the source and target headers
// encrypt the target (s3-ssec-object-lock.md 3.5). An SSE-C source copied
// to a plain target gets the plaintext MD5 ETag and SSE-S3 (LocalStack
// test_s3.snapshot.json L12913), to an SSE-C target a random ETag and the
// target key's echo (L12926, L12930); source refusals follow 3.3 and 3.5,
// on CopyObject and UploadPartCopy alike: SSE-C headers on a plain source
// are "not applicable" (S3SOL-06), and another key is 400 InvalidRequest
// "did not match the ones used originally", not the GET's 403 (S3SOL-07
// 07-copy, 07-upload-part-copy; both recorded 2026-11-03).
func TestSSECCopy(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("ssec")
		h.allowSSEC("ssec")
		const content = "test_data"
		h.mustPutSSEC("ssec", "enc", content, key1)
		h.put("ssec", "plain", "plain")
		srcKey := func(c customer) s3api.CopyObjectInput {
			return s3api.CopyObjectInput{CopySourceSSECustomerAlgorithm: c.alg(), CopySourceSSECustomerKey: c.k(), CopySourceSSECustomerKeyMD5: c.m()}
		}
		copyAs := func(dst, src string, in s3api.CopyObjectInput) (*s3api.CopyObjectOutput, *awserr.Error) {
			in.Bucket, in.Key, in.CopySource = "ssec", dst, "ssec/"+src
			return as(h, overTLS, s3api.OpCopyObject, &in)
		}

		out, err := copyAs("decrypted", "enc", srcKey(key1))
		if err != nil {
			t.Fatalf("copy of an SSE-C source to a plain target: %s %q", err.ID.QueryCode, err.Message)
		}
		if out.ServerSideEncryption == nil || *out.ServerSideEncryption != s3api.ServerSideEncryptionAes256 || out.SSECustomerAlgorithm != nil {
			t.Errorf("plain target reports SSE %v, SSE-C %v; want AES256 and no SSE-C", deref(out.ServerSideEncryption), deref(out.SSECustomerAlgorithm))
		}
		if out.CopyObjectResult == nil || deref(out.CopyObjectResult.ETag) != `"`+md5hex(content)+`"` {
			t.Errorf("plain target ETag = %v, want the plaintext MD5 %s", out.CopyObjectResult, md5hex(content))
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "ssec", Key: "decrypted"}); got != content {
			t.Errorf("plain target = %q, want %q", got, content)
		}

		in := srcKey(key1)
		in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = key2.alg(), key2.k(), key2.m()
		out, err = copyAs("reencrypted", "enc", in)
		if err != nil {
			t.Fatalf("copy of an SSE-C source to an SSE-C target: %s %q", err.ID.QueryCode, err.Message)
		}
		echoesKey(t, "CopyObject to SSE-C", out.SSECustomerAlgorithm, out.SSECustomerKeyMD5, out.ServerSideEncryption, out.SSEKMSKeyId, key2)
		if out.CopyObjectResult == nil || out.CopyObjectResult.ETag == nil || !randomETag.MatchString(*out.CopyObjectResult.ETag) || *out.CopyObjectResult.ETag == `"`+md5hex(content)+`"` {
			t.Errorf("SSE-C target ETag = %v, want 32 hex digits that are not the MD5", out.CopyObjectResult)
		}
		if got, _ := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "ssec", Key: "reencrypted", SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m()}); got != content {
			t.Errorf("SSE-C target read with its key = %q, want %q", got, content)
		}

		_, err = copyAs("x", "enc", s3api.CopyObjectInput{})
		failure{status: 400, code: "InvalidRequest", message: textNeedsKey}.is(t, "copy of an SSE-C source without its key (L12950)", err)
		_, err = copyAs("x", "enc", s3api.CopyObjectInput{CopySourceSSECustomerAlgorithm: new("KMS"), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m()})
		failure{status: 400, code: "InvalidEncryptionAlgorithmError", message: textAlgorithm, fields: map[string]string{"ArgumentName": sseArgument, "ArgumentValue": "KMS"}}.is(t, "copy source algorithm KMS (L12960)", err)
		h.follows("s3.sse-c.key-encoding", "write-side-texts")
		_, err = copyAs("x", "enc", s3api.CopyObjectInput{CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key2.m()})
		failure{status: 400, code: "InvalidArgument", message: textBadMD5, fields: map[string]string{"ArgumentName": sseArgument}}.is(t, "copy source MD5 mismatch", err)

		notApplicable := failure{status: 400, code: "InvalidRequest", message: textNotSSEC}
		mismatch := failure{status: 400, code: "InvalidRequest", message: textMismatch}
		_, err = copyAs("x", "plain", srcKey(key1))
		notApplicable.is(t, "copy-source SSE-C headers on a plain source", err)
		_, err = copyAs("x", "enc", srcKey(key2))
		mismatch.is(t, "copy source with another key", err)
		if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: "x"}); err == nil {
			t.Error("a refused copy left an object")
		}

		id := h.startUploadAs(overTLS, "ssec", "parts", s3api.CreateMultipartUploadInput{})
		partCopy := func(src string, c *customer) *awserr.Error {
			in := &s3api.UploadPartCopyInput{Bucket: "ssec", Key: "parts", UploadId: id, PartNumber: 1, CopySource: "ssec/" + src}
			if c != nil {
				in.CopySourceSSECustomerAlgorithm, in.CopySourceSSECustomerKey, in.CopySourceSSECustomerKeyMD5 = c.alg(), c.k(), c.m()
			}
			_, err := as(h, overTLS, s3api.OpUploadPartCopy, in)
			return err
		}
		failure{status: 400, code: "InvalidRequest", message: textNeedsKey}.is(t, "UploadPartCopy of an SSE-C source without its key", partCopy("enc", nil))
		mismatch.is(t, "UploadPartCopy source with another key", partCopy("enc", &key2))
		notApplicable.is(t, "UploadPartCopy SSE-C headers on a plain source", partCopy("plain", &key1))
		if err := partCopy("enc", &key1); err != nil {
			t.Errorf("UploadPartCopy of an SSE-C source with its key: %s %q", err.ID.QueryCode, err.Message)
		}
	})
}

// TestSSECMultipart: an SSE-C upload echoes the key on Create and every
// part (LocalStack test_s3.snapshot.json L13001, L13012); identical parts
// get different random ETags (L13012-L13032); each part must repeat the
// key (L13133, L13154, L13164); ListParts and Complete need no headers
// (L13042, L13083), Complete reports no SSE and no checksum, and the
// object's ETag keeps "-N" without being the MD5 of the part MD5s (L13083).
// Complete on an upload with a checksum algorithm checks the key under
// switch s3.sse-c.complete-headers.
func TestSSECMultipart(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("mpu")
		h.allowSSEC("mpu")
		withKey := func(c customer) s3api.CreateMultipartUploadInput {
			return s3api.CreateMultipartUploadInput{SSECustomerAlgorithm: c.alg(), SSECustomerKey: c.k(), SSECustomerKeyMD5: c.m()}
		}
		in := withKey(key1)
		in.Bucket, in.Key = "mpu", "big"
		created := ok(h, overTLS, s3api.OpCreateMultipartUpload, &in)
		echoesKey(t, "CreateMultipartUpload", created.SSECustomerAlgorithm, created.SSECustomerKeyMD5, created.ServerSideEncryption, created.SSEKMSKeyId, key1)
		id := *created.UploadId
		part := func(n int32, data []byte, c *customer) (*s3api.UploadPartOutput, *awserr.Error) {
			in := &s3api.UploadPartInput{Bucket: "mpu", Key: "big", UploadId: id, PartNumber: n, Body: bytesBody(string(data))}
			if c != nil {
				in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = c.alg(), c.k(), c.m()
			}
			return as(h, overTLS, s3api.OpUploadPart, in)
		}
		a := payload(1, minPart)
		tail := []byte("tail")
		parts := make([]s3api.CompletedPart, 0, 3)
		etags := make([]string, 0, 3)
		for n, data := range [][]byte{a, a, tail} {
			out, err := part(int32(n+1), data, &key1)
			if err != nil {
				t.Fatalf("UploadPart %d with the key: %s %q", n+1, err.ID.QueryCode, err.Message)
			}
			echoesKey(t, "UploadPart", out.SSECustomerAlgorithm, out.SSECustomerKeyMD5, out.ServerSideEncryption, out.SSEKMSKeyId, key1)
			if out.ETag == nil || !randomETag.MatchString(*out.ETag) || *out.ETag == `"`+hex.EncodeToString(md5Sum(data))+`"` {
				t.Errorf("part %d ETag = %v, want 32 hex digits that are not its MD5", n+1, deref(out.ETag))
			}
			etags = append(etags, str(out.ETag))
			parts = append(parts, s3api.CompletedPart{PartNumber: new(int32(n + 1)), ETag: out.ETag})
		}
		if etags[0] == etags[1] {
			t.Errorf("two identical SSE-C parts got the same ETag %s", etags[0])
		}
		_, err := part(4, tail, nil)
		failure{status: 400, code: "InvalidRequest", message: textInitiate}.is(t, "UploadPart without the key (L13154)", err)
		_, err = part(4, tail, &key2)
		failure{status: 400, code: "InvalidRequest", message: textMismatch}.is(t, "UploadPart with another key (L13164)", err)

		listedParts := ok(h, overTLS, s3api.OpListParts, &s3api.ListPartsInput{Bucket: "mpu", Key: "big", UploadId: id})
		if len(listedParts.Parts) != 3 {
			t.Errorf("ListParts without the key lists %d parts, want 3", len(listedParts.Parts))
		}
		done := ok(h, overTLS, s3api.OpCompleteMultipartUpload, &s3api.CompleteMultipartUploadInput{
			Bucket: "mpu", Key: "big", UploadId: id, MultipartUpload: &s3api.CompletedMultipartUpload{Parts: parts},
		})
		if done.ServerSideEncryption != nil || done.SSEKMSKeyId != nil || done.ChecksumType != nil || done.ChecksumCRC64NVME != nil || done.ChecksumCRC32 != nil {
			t.Errorf("Complete of an SSE-C upload reports SSE %v, checksum %v %v %v; want none",
				deref(done.ServerSideEncryption), deref(done.ChecksumType), deref(done.ChecksumCRC64NVME), deref(done.ChecksumCRC32))
		}
		if done.ETag == nil || !regexp.MustCompile(`^"[0-9a-f]{32}-3"$`).MatchString(*done.ETag) || *done.ETag == multipartETag(a, a, tail) {
			t.Errorf("Complete ETag = %v, want 32 hex digits and -3 that are not the MD5 of the part MD5s", deref(done.ETag))
		}
		got, _ := h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "mpu", Key: "big", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		if !bytes.Equal([]byte(got), append(append(append([]byte{}, a...), a...), tail...)) {
			t.Errorf("GET of the completed SSE-C object returned %d bytes, want the three parts", len(got))
		}
		_, err = as(h, overTLS, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "mpu", Key: "big"})
		failure{status: 400, code: "InvalidRequest", message: textNeedsKey}.is(t, "GET of the completed object without the key (L13093)", err)

		plainID := h.startUploadAs(overTLS, "mpu", "plain", s3api.CreateMultipartUploadInput{})
		_, err = as(h, overTLS, s3api.OpUploadPart, &s3api.UploadPartInput{
			Bucket: "mpu", Key: "plain", UploadId: plainID, PartNumber: 1, Body: bytesBody("p"),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
		})
		failure{status: 400, code: "InvalidRequest", message: textInitiate}.is(t, "UploadPart with a key on a plain upload (L13133)", err)

		h.put("mpu", "src", "source bytes")
		_, err = as(h, overTLS, s3api.OpUploadPartCopy, &s3api.UploadPartCopyInput{Bucket: "mpu", Key: "big2", UploadId: h.startUploadAs(overTLS, "mpu", "big2", withKey(key1)), PartNumber: 1, CopySource: "mpu/src"})
		failure{status: 400, code: "InvalidRequest", message: textInitiate}.is(t, "UploadPartCopy into an SSE-C upload without the key", err)

		h.follows("s3.sse-c.complete-headers", "checked-when-sent")
		summed := withKey(key1)
		summed.ChecksumAlgorithm = new(s3api.ChecksumAlgorithmCrc32)
		sumID := h.startUploadAs(overTLS, "mpu", "summed", summed)
		summedPart := partHeaderFor(checksum.CRC32, sumOf(h, checksum.CRC32, []byte("only part")))
		summedPart.Bucket, summedPart.Key, summedPart.UploadId, summedPart.PartNumber, summedPart.Body = "mpu", "summed", sumID, 1, bytesBody("only part")
		summedPart.ChecksumAlgorithm = new(s3api.ChecksumAlgorithmCrc32)
		summedPart.SSECustomerAlgorithm, summedPart.SSECustomerKey, summedPart.SSECustomerKeyMD5 = key1.alg(), key1.k(), key1.m()
		out, err := as(h, overTLS, s3api.OpUploadPart, &summedPart)
		if err != nil {
			t.Fatalf("UploadPart of the CRC32 upload: %s %q", err.ID.QueryCode, err.Message)
		}
		finish := func(c *customer) *awserr.Error {
			in := &s3api.CompleteMultipartUploadInput{Bucket: "mpu", Key: "summed", UploadId: sumID, MultipartUpload: &s3api.CompletedMultipartUpload{
				Parts: []s3api.CompletedPart{{PartNumber: new(int32(1)), ETag: out.ETag, ChecksumCRC32: out.ChecksumCRC32}},
			}}
			if c != nil {
				in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5 = c.alg(), c.k(), c.m()
			}
			_, err := as(h, overTLS, s3api.OpCompleteMultipartUpload, in)
			return err
		}
		failure{status: 400, code: "InvalidRequest", message: textInitiate}.is(t, "Complete of a CRC32 SSE-C upload without the key", finish(nil))
		failure{status: 400, code: "InvalidRequest", message: textMismatch}.is(t, "Complete of a CRC32 SSE-C upload with another key", finish(&key2))
		if err := finish(&key1); err != nil {
			t.Errorf("Complete of a CRC32 SSE-C upload with its key: %s %q", err.ID.QueryCode, err.Message)
		}
	})
}

// md5Sum is data's MD5.
func md5Sum(data []byte) []byte {
	sum := md5.Sum(data) //nolint:gosec // see the import
	return sum[:]
}

// syncBuffer is a log sink the kernel's goroutines may write concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.b.Bytes())
}

// secretForms are the ways a customer key could leak: its bytes, its
// base64 as sent, and its hex.
func secretForms(c customer) [][]byte {
	return [][]byte{c.raw, []byte(c.key), []byte(hex.EncodeToString(c.raw))}
}

// leaks names the first form of a key in data, or "".
func leaks(data []byte, forms [][]byte) string {
	for _, f := range forms {
		if bytes.Contains(data, f) {
			return string(f)
		}
	}
	return ""
}

// TestSSECKeyNeverStored: S3 "does not store the encryption key that you
// provide" (s3-ssec-object-lock.md 3.11), so after SSE-C writes, reads,
// copies, a multipart upload and refused requests, no form of either key
// is in the journal file, the kernel's or the service's logs, any error,
// or the snapshot, which also holds no key MD5 (11.2).
func TestSSECKeyNeverStored(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal.ndjson")
	kernelLog, serviceLog := &syncBuffer{}, &syncBuffer{}
	debug := &slog.HandlerOptions{Level: slog.LevelDebug}
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
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
		d.Log = slog.New(slog.NewTextHandler(serviceLog, debug))
		h := &harness{K: k, svc: s3.New(d, s3.Options{}), logs: &bytes.Buffer{}}
		var errs []*awserr.Error
		keep := func(err *awserr.Error) {
			if err != nil {
				errs = append(errs, err)
			}
		}

		h.createBucket("ssec")
		h.allowSSEC("ssec")
		h.mustPutSSEC("ssec", "obj", "secret bytes", key1)
		h.readAs(overTLS, &s3api.GetObjectInput{Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		ok(h, overTLS, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		ok(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "ssec", Key: "copy", CopySource: "ssec/obj",
			CopySourceSSECustomerAlgorithm: key1.alg(), CopySourceSSECustomerKey: key1.k(), CopySourceSSECustomerKeyMD5: key1.m(),
			SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m(),
		})
		id := h.startUploadAs(overTLS, "ssec", "mpu", s3api.CreateMultipartUploadInput{SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m()})
		part := ok(h, overTLS, s3api.OpUploadPart, &s3api.UploadPartInput{
			Bucket: "ssec", Key: "mpu", UploadId: id, PartNumber: 1, Body: bytesBody("one part"),
			SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m(),
		})
		ok(h, overTLS, s3api.OpCompleteMultipartUpload, &s3api.CompleteMultipartUploadInput{
			Bucket: "ssec", Key: "mpu", UploadId: id, MultipartUpload: &s3api.CompletedMultipartUpload{Parts: []s3api.CompletedPart{{PartNumber: new(int32(1)), ETag: part.ETag}}},
		})
		_, err1 := as(h, overTLS, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "ssec", Key: "obj", SSECustomerAlgorithm: key2.alg(), SSECustomerKey: key2.k(), SSECustomerKeyMD5: key2.m()})
		keep(err1)
		_, err2 := as(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "ssec", Key: "bad", Body: bytesBody("x"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key2.m()})
		keep(err2)
		_, err3 := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "ssec", Key: "bad", Body: bytesBody("x"), SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m()})
		keep(err3)
		_, err4 := as(h, overTLS, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "ssec", Key: "bad", CopySource: "ssec/obj",
			CopySourceSSECustomerAlgorithm: key2.alg(), CopySourceSSECustomerKey: key2.k(), CopySourceSSECustomerKeyMD5: key2.m(),
		})
		keep(err4)
		if len(errs) != 4 {
			t.Errorf("%d of the four refused requests failed", len(errs))
		}

		forms := append(secretForms(key1), secretForms(key2)...)
		for _, e := range errs {
			parts := []string{e.Message}
			for _, f := range e.Fields {
				parts = append(parts, f.Value)
			}
			for _, x := range e.Headers {
				parts = append(parts, x.Value)
			}
			if l := leaks([]byte(strings.Join(parts, "\n")), append(forms, []byte(key1.md5), []byte(key2.md5))); l != "" {
				t.Errorf("error %s %q carries the key or its MD5 %q", e.ID.QueryCode, e.Message, l)
			}
		}

		sections, xerr := h.svc.Export()
		if xerr != nil {
			t.Fatalf("Export: %v", xerr)
		}
		for _, sec := range sections {
			if l := leaks(sec.Data, append(forms, []byte(key1.md5), []byte(key2.md5))); l != "" {
				t.Errorf("snapshot section %s holds %q", sec.Path, l)
			}
		}
		if err := k.Journal.Close(); err != nil {
			t.Fatalf("closing the journal: %v", err)
		}
		journal, rerr := os.ReadFile(journalPath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if len(journal) == 0 {
			t.Error("the journal file is empty; the test grepped nothing")
		}
		if l := leaks(journal, forms); l != "" {
			t.Errorf("the journal file holds the key %q", l)
		}
		if l := leaks(kernelLog.Bytes(), forms); l != "" {
			t.Errorf("the kernel log holds the key %q", l)
		}
		if l := leaks(serviceLog.Bytes(), forms); l != "" {
			t.Errorf("the S3 log holds the key %q", l)
		}
	}, kerneltest.Services(s3api.Service()), kerneltest.JournalFile(journalPath), kerneltest.Log(slog.New(slog.NewTextHandler(kernelLog, debug))))
}

// TestBucketEncryptionBlock: PutBucketEncryption accepts a rule holding
// only BlockedEncryptionTypes (API_PutBucketEncryption Example 4,
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketEncryption.html);
// GetBucketEncryption then reads back the AES256 default with NONE (switch
// s3.encryption.unblocked-read); a Put without the element and a Delete
// keep the block as it was (switch s3.encryption.blocked-types-omitted;
// from the default state, testdata/parity/s3/R06/02-get-encryption-arn.json
// and 02-get-encryption-after-delete.json); a rule with neither member is
// MalformedXML (switch s3.encryption.unblocked-read), as is a
// configuration without rules (s3.md 4.2.2).
func TestBucketEncryptionBlock(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.encryption.unblocked-read", "NONE-element")
		h.follows("s3.encryption.blocked-types-omitted", "kept")
		aes := &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256}
		kmsArn := ok(h, who{}, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.Arn
		kms := &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAwsKms, KMSMasterKeyID: kmsArn}
		types := func(t s3api.EncryptionType) *s3api.BlockedEncryptionTypes {
			return &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{t}}
		}
		put := func(bucket string, rules ...s3api.ServerSideEncryptionRule) *awserr.Error {
			_, err := as(h, who{}, s3api.OpPutBucketEncryption, &s3api.PutBucketEncryptionInput{Bucket: bucket, ServerSideEncryptionConfiguration: s3api.ServerSideEncryptionConfiguration{Rules: rules}})
			return err
		}
		read := func(what, bucket string, want s3api.ServerSideEncryptionRule) {
			t.Helper()
			out := ok(h, who{}, s3api.OpGetBucketEncryption, &s3api.GetBucketEncryptionInput{Bucket: bucket})
			if out.ServerSideEncryptionConfiguration == nil || len(out.ServerSideEncryptionConfiguration.Rules) != 1 {
				t.Errorf("%s: GetBucketEncryption = %+v, want one rule", what, out.ServerSideEncryptionConfiguration)
				return
			}
			got := out.ServerSideEncryptionConfiguration.Rules[0]
			if !ruleEqual(got, want) {
				t.Errorf("%s: rule %s, want %s", what, ruleString(got), ruleString(want))
			}
		}

		h.createBucket("unblocked")
		if err := put("unblocked", s3api.ServerSideEncryptionRule{BlockedEncryptionTypes: types(s3api.EncryptionTypeNone)}); err != nil {
			t.Fatalf("a rule of BlockedEncryptionTypes NONE alone: %s %q", err.ID.QueryCode, err.Message)
		}
		read("after NONE alone", "unblocked", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: aes, BucketKeyEnabled: new(false), BlockedEncryptionTypes: types(s3api.EncryptionTypeNone)})
		h.mustPutSSEC("unblocked", "k", "v", key1)
		if err := put("unblocked", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: kms, BucketKeyEnabled: new(true)}); err != nil {
			t.Fatalf("a KMS rule without the element: %s %q", err.ID.QueryCode, err.Message)
		}
		read("KMS rule after NONE", "unblocked", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: kms, BucketKeyEnabled: new(true), BlockedEncryptionTypes: types(s3api.EncryptionTypeNone)})
		ok(h, who{}, s3api.OpDeleteBucketEncryption, &s3api.DeleteBucketEncryptionInput{Bucket: "unblocked"})
		read("deleted after NONE", "unblocked", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: aes, BucketKeyEnabled: new(false), BlockedEncryptionTypes: types(s3api.EncryptionTypeNone)})
		h.mustPutSSEC("unblocked", "k", "v", key1)

		h.setBlock("unblocked", s3api.EncryptionTypeSseC)
		read("blocked again", "unblocked", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: aes, BucketKeyEnabled: new(false), BlockedEncryptionTypes: types(s3api.EncryptionTypeSseC)})
		_, err := h.putSSEC("unblocked", "k", "v", key1)
		blocked("s3:PutObject", "unblocked", "k").is(t, "SSE-C PutObject after blocking again", err)

		h.createBucket("default")
		if err := put("default", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: kms}); err != nil {
			t.Fatalf("a KMS rule: %s %q", err.ID.QueryCode, err.Message)
		}
		read("KMS rule from the default state (R06/02-get-encryption-arn)", "default", s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: kms, BucketKeyEnabled: new(false), BlockedEncryptionTypes: types(s3api.EncryptionTypeSseC)})

		malformed := failure{status: 400, code: "MalformedXML", message: "The XML you provided was not well-formed or did not validate against our published schema"}
		malformed.is(t, "a rule with neither member", put("default", s3api.ServerSideEncryptionRule{}))
		malformed.is(t, "a rule with only BucketKeyEnabled", put("default", s3api.ServerSideEncryptionRule{BucketKeyEnabled: new(true)}))
		malformed.is(t, "no rules", put("default"))
	})
}

// ruleEqual compares two encryption rules member by member.
func ruleEqual(a, b s3api.ServerSideEncryptionRule) bool {
	return ruleString(a) == ruleString(b)
}

// ruleString renders a rule's members for comparison and messages.
func ruleString(r s3api.ServerSideEncryptionRule) string {
	var b strings.Builder
	if d := r.ApplyServerSideEncryptionByDefault; d != nil {
		b.WriteString("default{" + string(d.SSEAlgorithm) + " " + str(d.KMSMasterKeyID) + "} ")
	}
	if r.BucketKeyEnabled != nil {
		if *r.BucketKeyEnabled {
			b.WriteString("bucketKey{true} ")
		} else {
			b.WriteString("bucketKey{false} ")
		}
	}
	if r.BlockedEncryptionTypes != nil {
		b.WriteString("blocked{")
		for _, t := range r.BlockedEncryptionTypes.EncryptionType {
			b.WriteString(string(t) + " ")
		}
		b.WriteString("}")
	}
	return b.String()
}
