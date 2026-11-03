package s3

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // SSE-C sends the MD5 of the customer key; the test recomputes it
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
)

// The SSE-C refusals of s3-ssec-object-lock.md 3.2 and 3.3, each with
// ArgumentName x-amz-server-side-encryption, even for copy-source headers.
// Recorded texts cite LocalStack's test_s3.snapshot.json (localstack@8b9a79f,
// validated 2026-02-21).
const (
	// L12706.
	msgSSECConflict = "Server Side Encryption with Customer provided key is incompatible with the encryption method specified"
	// L12718, L12821.
	msgSSECAlgorithm = "The Encryption request you specified is not valid. Supported value: AES256."
	// L12730.
	msgSSECNoAlgorithm = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
	// L12741.
	msgSSECNoKey = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
	// S3SOL-03 03-put, recorded 2026-11-03.
	msgSSECNoMD5 = "Requests specifying Server Side Encryption with Customer provided keys must provide the client calculated MD5 of the secret key."
	// L12763.
	msgSSECBadKey = "The secret key was invalid for the specified algorithm."
	// L12774.
	msgSSECBadMD5 = "The calculated MD5 hash of the key did not match the hash that was provided."
	// Switch s3.sse-c.plain-http (s3-ssec-object-lock.md 11.4).
	msgSSECInsecure = "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection."
)

// sseArg is the ArgumentName every SSE-C refusal carries (s3-ssec-object-lock.md 3.2).
const sseArg = "x-amz-server-side-encryption"

// keyWire is a customer key as the wire carries it: the base64 key and the
// base64 MD5 of its decoded bytes (s3-ssec-object-lock.md 3.1).
func keyWire(raw []byte) (key, keyMD5 string) {
	sum := md5.Sum(raw) //nolint:gosec // see the import
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(sum[:])
}

// keyForm is one way a test sends the key header.
type keyForm int

const (
	keyAbsent    keyForm = iota
	keyValid             // base64 of 32 bytes
	keyShort             // base64 of 10 bytes (LocalStack test_s3.snapshot.json L12763)
	keyLong              // base64 of 44 bytes, the botocore double encoding (L12752, L12855)
	keyNotBase64         // switch s3.sse-c.key-encoding
)

// md5Form is one way a test sends the key-MD5 header.
type md5Form int

const (
	md5Absent md5Form = iota
	md5Matching
	md5Other
)

// trio is one drawn SSE-C header trio and the decoded key it carries.
type trio struct {
	h       customerHeaders
	raw     []byte // the decoded key; nil when absent or not base64
	key     keyForm
	md5     md5Form
	algText string // "" when absent
}

func drawTrio(t *rapid.T) trio {
	var tr trio
	tr.algText = rapid.SampledFrom([]string{"", "AES256", "KMS", "aes256"}).Draw(t, "algorithm")
	if tr.algText != "" {
		tr.h.algorithm = new(tr.algText)
	}
	tr.key = keyForm(rapid.IntRange(int(keyAbsent), int(keyNotBase64)).Draw(t, "key"))
	var wire string
	switch tr.key {
	case keyValid:
		tr.raw = rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "raw")
	case keyShort:
		tr.raw = rapid.SliceOfN(rapid.Byte(), 10, 10).Draw(t, "raw")
	case keyLong:
		tr.raw = rapid.SliceOfN(rapid.Byte(), 44, 44).Draw(t, "raw")
	case keyNotBase64:
		wire = "%%not*base64%%"
	case keyAbsent:
	}
	if tr.raw != nil {
		wire, _ = keyWire(tr.raw)
	}
	if tr.key != keyAbsent {
		tr.h.key = new(wire)
	}
	tr.md5 = md5Form(rapid.IntRange(int(md5Absent), int(md5Other)).Draw(t, "md5"))
	switch tr.md5 {
	case md5Matching:
		_, sum := keyWire(tr.raw)
		tr.h.keyMD5 = new(sum)
	case md5Other:
		_, sum := keyWire([]byte("another key entirely, not this one"))
		tr.h.keyMD5 = new(sum)
	case md5Absent:
	}
	return tr
}

// sent is whether any of the three headers is present.
func (tr trio) sent() bool { return tr.h.algorithm != nil || tr.h.key != nil || tr.h.keyMD5 != nil }

// expectCustomer is the refusal the checks of s3-ssec-object-lock.md 3.2 give
// tr, in their order (conflict, algorithm, presence, key, MD5), or nil when
// it passes. read leaves out the conflict and key-length checks, as a read
// compares a wrong-length key with the stored one instead (3.3, LocalStack
// test_s3.snapshot.json L12855; S3SOL-04 for a 10-byte key), and the MD5's
// presence, as a read computes a missing one (S3SOL-03 03-get, recorded
// 2026-11-03). A key that is not base64 gets the MD5-mismatch text
// (S3SOL-04 04-put-not-base64).
func expectCustomer(tr trio, sse *s3api.ServerSideEncryption, read bool) *wantError {
	arg := map[string]string{"ArgumentName": sseArg}
	switch {
	case !read && sse != nil:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECConflict, fields: map[string]string{"ArgumentName": sseArg, "ArgumentValue": string(*sse)}}
	case tr.algText != "" && tr.algText != "AES256":
		return &wantError{status: 400, code: "InvalidEncryptionAlgorithmError", message: msgSSECAlgorithm, fields: map[string]string{"ArgumentName": sseArg, "ArgumentValue": tr.algText}}
	case tr.algText == "":
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECNoAlgorithm, fields: arg}
	case tr.key == keyAbsent:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECNoKey, fields: arg}
	case !read && tr.md5 == md5Absent:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECNoMD5, fields: arg}
	case tr.key == keyNotBase64:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECBadMD5, fields: arg}
	case !read && tr.key != keyValid:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECBadKey, fields: arg}
	case tr.md5 == md5Other:
		return &wantError{status: 400, code: "InvalidArgument", message: msgSSECBadMD5, fields: arg}
	}
	return nil
}

// TestCustomerHeadersSent: a trio is sent when any of its headers is.
func TestCustomerHeadersSent(t *testing.T) {
	property(t, func(t *rapid.T) {
		tr := drawTrio(t)
		if tr.h.sent() != tr.sent() {
			t.Fatalf("%+v.sent() = %v, want %v", tr.h, tr.h.sent(), tr.sent())
		}
	})
}

// TestParseCustomerWrite checks every drawn trio against the write-side
// order of s3-ssec-object-lock.md 3.2: only "conflict before algorithm" is
// recorded (LocalStack test_s3.py L12123-L12131), the rest follows 11.3. A
// key that is not base64 gets the MD5-mismatch text (S3SOL-04), a missing
// MD5 the recorded InvalidArgument (S3SOL-03, both recorded 2026-11-03).
// A trio that passes yields its decoded key and the MD5 header as sent;
// none sent is no SSE-C, whatever x-amz-server-side-encryption says.
func TestParseCustomerWrite(t *testing.T) {
	property(t, func(rt *rapid.T) {
		tr := drawTrio(rt)
		sse := rapid.SampledFrom([]*s3api.ServerSideEncryption{nil, new(s3api.ServerSideEncryptionAes256), new(s3api.ServerSideEncryptionAwsKms)}).Draw(rt, "sse")
		got, err := parseCustomerWrite(tr.h, sse)
		if !tr.sent() {
			if got != nil || err != nil {
				rt.Fatalf("no SSE-C headers: %+v, %v; want nil, nil", got, err)
			}
			return
		}
		if want := expectCustomer(tr, sse, false); want != nil {
			want.check(rt, "parseCustomerWrite", err)
			return
		}
		if err != nil {
			rt.Fatalf("parseCustomerWrite(%+v) = %s %q, want accepted", tr.h, err.ID.QueryCode, err.Message)
		}
		if got == nil || !bytes.Equal(got.key, tr.raw) || got.keyMD5 != *tr.h.keyMD5 {
			rt.Fatalf("parseCustomerWrite = %+v, want key %x and MD5 %s", got, tr.raw, *tr.h.keyMD5)
		}
	})
}

// TestParseCustomerRead: a read checks the trio as a write does, minus the
// conflict, the key length and the MD5's presence. A decodable key of the
// wrong length with its MD5 passes, to fail the stored-key comparison with
// 403 as the 44-byte key did (LocalStack test_s3.snapshot.json L12855) and
// a 10-byte one does (S3SOL-04 04-get-short-key); a key without its MD5
// passes with the MD5 S3 computes, which the response echoes (S3SOL-03
// 03-get); a key that is not base64 or an MD5 mismatch gets the
// MD5-mismatch text (S3SOL-04 04-get-bad-md5; all recorded 2026-11-03).
func TestParseCustomerRead(t *testing.T) {
	property(t, func(rt *rapid.T) {
		tr := drawTrio(rt)
		if !tr.sent() {
			return
		}
		got, err := parseCustomerRead(tr.h)
		if want := expectCustomer(tr, nil, true); want != nil {
			want.check(rt, "parseCustomerRead", err)
			return
		}
		if err != nil {
			rt.Fatalf("parseCustomerRead(%+v) = %s %q, want accepted", tr.h, err.ID.QueryCode, err.Message)
		}
		_, wantMD5 := keyWire(tr.raw)
		if got == nil || !bytes.Equal(got.key, tr.raw) || got.keyMD5 != wantMD5 {
			rt.Fatalf("parseCustomerRead = %+v, want key %x and MD5 %s", got, tr.raw, wantMD5)
		}
	})
}

// TestCustomerEncryptionFingerprint: S3 keeps "a randomly salted
// Hash-based Message Authentication Code (HMAC) value of the encryption
// key", never the key (s3-ssec-object-lock.md 3.11): the fingerprint is the
// salt and HMAC-SHA256(salt, key) (11.2), it matches the key it was made
// with and no other, and neither part holds the key.
func TestCustomerEncryptionFingerprint(t *testing.T) {
	property(t, func(t *rapid.T) {
		salt := rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "salt")
		raw := rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "key")
		other := rapid.SliceOfN(rapid.Byte(), 1, 64).Filter(func(b []byte) bool { return !bytes.Equal(b, raw) }).Draw(t, "other")
		_, sum := keyWire(raw)
		k := customerKey{key: raw, keyMD5: sum}
		e := newCustomerEncryption(salt, k)
		mac := hmac.New(sha256.New, salt)
		_, _ = mac.Write(raw)
		if !bytes.Equal(e.Salt, salt) || !bytes.Equal(e.MAC, mac.Sum(nil)) {
			t.Fatalf("newCustomerEncryption = salt %x mac %x, want salt %x and HMAC-SHA256(salt, key)", e.Salt, e.MAC, salt)
		}
		if bytes.Contains(e.MAC, raw) || bytes.Contains(e.Salt, raw) {
			t.Fatal("the fingerprint holds the key")
		}
		if !e.matches(k) {
			t.Fatal("the fingerprint does not match the key it was made with")
		}
		if e.matches(customerKey{key: other, keyMD5: sum}) {
			t.Fatalf("the fingerprint matches another key %x", other)
		}
	})
}

// TestNewBucketBlock: a new bucket blocks SSE-C writes in every Region but
// Middle East (Bahrain) and Middle East (UAE), me-south-1 and me-central-1
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/default-s3-c-encryption-setting-faq.html,
// question 1; s3-ssec-object-lock.md 4.1).
func TestNewBucketBlock(t *testing.T) {
	for _, name := range []string{"us-east-1", "us-east-2", "us-west-2", "eu-west-1", "ap-southeast-2", "il-central-1", "sa-east-1", "me-south-1", "me-central-1"} {
		r, err := aws.KnownRegions().Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		want := BlockSSEC
		if name == "me-south-1" || name == "me-central-1" {
			want = BlockNone
		}
		if got := newBucketBlock(r); got != want {
			t.Errorf("newBucketBlock(%s) = %d, want %d", name, got, want)
		}
	}
}

// TestBlockedEncryptionOf reads a PutBucketEncryption rule's
// BlockedEncryptionTypes (model EncryptionType, s3-ssec-object-lock.md 4.2):
// absent is reported as absent, so the stored block is kept (switch
// s3.encryption.blocked-types-omitted); [SSE-C] blocks and [NONE] unblocks.
func TestBlockedEncryptionOf(t *testing.T) {
	declared(t, switchValues(t), "s3.encryption.blocked-types-omitted")
	aes := &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAes256}
	if _, ok, err := blockedEncryptionOf(s3api.ServerSideEncryptionRule{ApplyServerSideEncryptionByDefault: aes}); ok || err != nil {
		t.Errorf("rule without the element: ok %v, err %v; want absent", ok, err)
	}
	for types, want := range map[s3api.EncryptionType]BlockedEncryption{s3api.EncryptionTypeSseC: BlockSSEC, s3api.EncryptionTypeNone: BlockNone} {
		rule := s3api.ServerSideEncryptionRule{BlockedEncryptionTypes: &s3api.BlockedEncryptionTypes{EncryptionType: []s3api.EncryptionType{types}}}
		got, ok, err := blockedEncryptionOf(rule)
		if !ok || err != nil || got != want {
			t.Errorf("[%s]: %d, %v, %v; want %d", types, got, ok, err, want)
		}
	}
}

// TestCheckCustomerWrite: an SSE-C write to a bucket that blocks SSE-C is
// refused with 403 AccessDenied naming the caller, the action and the
// object, checked before the scheme (s3-ssec-object-lock.md 4.3; S3SOL-01,
// recorded 2026-11-03), and one over plain HTTP to a bucket that allows it
// is refused under switch s3.sse-c.plain-http; HTTPS to an unblocked
// bucket passes.
func TestCheckCustomerWrite(t *testing.T) {
	values := switchValues(t)
	role := identity.Root{Account: aws.DefaultAccount()}
	blocked := wantError{status: 403, code: "AccessDenied", message: "User: arn:aws:iam::" + aws.DefaultAccount().String() + ":root is not authorized to perform: s3:CopyObject" +
		` on resource: "arn:aws:s3:::b/k" because this bucket has blocked upload requests that specify Server Side Encryption with Customer provided keys (SSE-C).` +
		" Please specify a different server-side encryption type."}
	if v := declared(t, values, "s3.sse-c.plain-http"); v != "refused" {
		t.Fatalf("switch s3.sse-c.plain-http declares %q; this test knows %q", v, "refused")
	}
	insecure := wantError{status: 400, code: "InvalidRequest", message: msgSSECInsecure}
	call := func(scheme string) writeCall {
		return writeCall{principal: role, action: "s3:CopyObject", scheme: scheme}
	}
	b := func(block BlockedEncryption) *Bucket { return &Bucket{Name: "b", BlockedEncryption: block} }
	blocked.check(t, "blocked bucket over HTTPS", checkCustomerWrite(b(BlockSSEC), "k", call("https")))
	blocked.check(t, "blocked bucket over HTTP", checkCustomerWrite(b(BlockSSEC), "k", call("http")))
	insecure.check(t, "unblocked bucket over HTTP", checkCustomerWrite(b(BlockNone), "k", call("http")))
	if err := checkCustomerWrite(b(BlockNone), "k", call("https")); err != nil {
		t.Errorf("unblocked bucket over HTTPS: %s %q, want accepted", err.ID.QueryCode, err.Message)
	}
}
