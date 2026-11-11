package server_test

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/server"
)

// s3Put sends a header-signed PUT of body to target with headers: a
// PutObject, or a CopyObject with x-amz-copy-source.
func s3Put(t *testing.T, s signer, target, body string, headers map[string]string) wire {
	t.Helper()
	req := newRequest(t, http.MethodPut, target, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return signedCall(t, s, "s3", req, body)
}

// s3Upload sends a header-signed CreateMultipartUpload to target with
// headers.
func s3Upload(t *testing.T, s signer, target string, headers map[string]string) wire {
	t.Helper()
	req := newRequest(t, http.MethodPost, target, "")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return signedCall(t, s, "s3", req, "")
}

// putBucketDefaultKMS gives bucket the default encryption SSE-KMS under
// key, as root.
func putBucketDefaultKMS(t *testing.T, endpoint, bucket, key string) {
	t.Helper()
	config := `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ApplyServerSideEncryptionByDefault>` +
		`<SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>` + key + `</KMSMasterKeyID></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/"+bucket+"?encryption", config); got.status != http.StatusOK {
		t.Fatalf("PutBucketEncryption = %d %s", got.status, got.body)
	}
}

// putBucketNullDeny sets bucket's policy to the documented "require
// SSE-KMS" deny of s3:PutObject when key is absent (S3 bucket policy
// examples; iam-enforcement-surfaces.md 13.32), as root.
func putBucketNullDeny(t *testing.T, endpoint, bucket, key string) {
	t.Helper()
	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"DenyAbsent","Effect":"Deny","Principal":"*","Action":"s3:PutObject",` +
		`"Resource":"arn:aws:s3:::` + bucket + `/*","Condition":{"Null":{"` + key + `":"true"}}}]}`
	if got := s3Call(t, dummy, http.MethodPut, endpoint+"/"+bucket+"?policy", policy); got.status/100 != 2 {
		t.Fatalf("PutBucketPolicy = %d %s", got.status, got.body)
	}
}

// The coverage report lists switch s3.condition.sse-from-default as
// aws-recorded for PutObject, which IAM-ENF-30 recorded, and inferred for
// CopyObject and CreateMultipartUpload.
func TestSSEFromDefaultSwitchIsReported(t *testing.T) {
	want := map[evidence.Label][]string{
		evidence.AWSRecorded: {"s3.PutObject"},
		evidence.Inferred:    {"s3.CopyObject", "s3.CreateMultipartUpload"},
	}
	got := map[evidence.Label][]string{}
	for _, sw := range wiredReport(t).Switches {
		if sw.Name == "s3.condition.sse-from-default" {
			got[sw.Label] = append(got[sw.Label], sw.Ops...)
		}
	}
	if !maps.EqualFunc(got, want, func(g, w []string) bool { return slices.Equal(slices.Sorted(slices.Values(g)), w) }) {
		t.Errorf("s3.condition.sse-from-default ops by label = %v, want %v", got, want)
	}
}

// Under enforce PutObject supplies the s3:x-amz-server-side-encryption*
// condition keys as IAM-ENF-30 recorded (recorded 2026-11-03,
// testdata/parity/s3/IAM-ENF/30-*.json; iam-enforcement-surfaces.md 9.2):
// from the request headers, and the key-id key from an SSE-KMS bucket
// default when the request names no encryption, which the algorithm key
// never is (switch s3.condition.sse-from-default).
func TestEnforcePutObjectSuppliesTheSSEConditionKeys(t *testing.T) {
	const (
		algorithm = "s3:x-amz-server-side-encryption"
		keyID     = "s3:x-amz-server-side-encryption-aws-kms-key-id"
	)
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	key := createKey(t, endpoint)
	for _, b := range []string{"enf-b2", "plain"} {
		if w := s3Call(t, dummy, http.MethodPut, endpoint+"/"+b, ""); w.status != http.StatusOK {
			t.Fatalf("CreateBucket %s = %d %s", b, w.status, w.body)
		}
	}
	putBucketDefaultKMS(t, endpoint, "enf-b2", key)
	kms := map[string]string{"x-amz-server-side-encryption": "aws:kms", "x-amz-server-side-encryption-aws-kms-key-id": key}
	denied := func(object string) string {
		return `User: arn:aws:iam::000000000000:user/u is not authorized to perform: s3:PutObject on resource: "arn:aws:s3:::` + object +
			`" with an explicit deny in a resource-based policy`
	}

	// 30-no-header: the bucket default supplies the key id.
	putBucketNullDeny(t, endpoint, "enf-b2", keyID)
	if w := s3Put(t, u, endpoint+"/enf-b2/a", "x", nil); w.status != http.StatusOK {
		t.Errorf("30-no-header: header-less PutObject under a key-id Null deny = %d %s, want 200", w.status, w.body)
	}
	// 30-with-header: the headers supply both keys.
	if w := s3Put(t, u, endpoint+"/enf-b2/b", "x", kms); w.status != http.StatusOK {
		t.Errorf("30-with-header: PutObject with both headers = %d %s, want 200", w.status, w.body)
	}
	// 30-no-header-sse: the default never supplies the algorithm key.
	putBucketNullDeny(t, endpoint, "enf-b2", algorithm)
	w := s3Put(t, u, endpoint+"/enf-b2/c", "x", nil)
	if e := parseXMLError(t, w); w.status != http.StatusForbidden || e.S3Code != "AccessDenied" || e.S3Message != denied("enf-b2/c") {
		t.Errorf("30-no-header-sse: header-less PutObject under an algorithm Null deny = %d %s, want 403 AccessDenied %q", w.status, w.body, denied("enf-b2/c"))
	}

	// Without a bucket default nothing supplies the key id.
	putBucketNullDeny(t, endpoint, "plain", keyID)
	w = s3Put(t, u, endpoint+"/plain/a", "x", nil)
	if e := parseXMLError(t, w); w.status != http.StatusForbidden || e.S3Message != denied("plain/a") {
		t.Errorf("header-less PutObject without a bucket default under a key-id Null deny = %d %s, want 403 %q", w.status, w.body, denied("plain/a"))
	}

	// CopyObject and CreateMultipartUpload authorize as s3:PutObject with
	// the same keys (inferred: IAM-ENF-30 recorded PutObject only).
	if w := s3Put(t, u, endpoint+"/plain/copied", "", map[string]string{"x-amz-copy-source": "enf-b2/a"}); w.status != http.StatusForbidden {
		t.Errorf("header-less CopyObject under a key-id Null deny = %d %s, want 403", w.status, w.body)
	}
	if w := s3Put(t, u, endpoint+"/plain/copied", "", map[string]string{"x-amz-copy-source": "enf-b2/a", "x-amz-server-side-encryption": "aws:kms", "x-amz-server-side-encryption-aws-kms-key-id": key}); w.status != http.StatusOK {
		t.Errorf("CopyObject with both headers under a key-id Null deny = %d %s, want 200", w.status, w.body)
	}
	if w := s3Upload(t, u, endpoint+"/plain/parts?uploads", nil); w.status != http.StatusForbidden {
		t.Errorf("header-less CreateMultipartUpload under a key-id Null deny = %d %s, want 403", w.status, w.body)
	}
	if w := s3Upload(t, u, endpoint+"/plain/parts?uploads", kms); w.status != http.StatusOK {
		t.Errorf("CreateMultipartUpload with both headers under a key-id Null deny = %d %s, want 200", w.status, w.body)
	}

	if got := enforcementAdvisories(t, c, "condition-key-unsupplied"); len(got) != 0 {
		t.Errorf("condition-key-unsupplied advisories = %q, want none", got)
	}
}

// The SSE condition keys describe a copy's destination write and are
// documented for s3:PutObject alone (iam-enforcement-surfaces.md 9.2), so
// CopyObject's read of its source, s3:GetObject, does not carry the
// destination's headers: a Null deny on the source read finds the key
// unsupplied, evaluated as absent, and denies the copy.
func TestEnforceCopyObjectSourceReadCarriesNoSSEConditionKeys(t *testing.T) {
	const algorithm = "s3:x-amz-server-side-encryption"
	c, endpoint, u := enforced(t, server.IAMEnforceEnforce)
	putUserPolicy(t, endpoint, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)
	for _, b := range []string{"src", "dst"} {
		if w := s3Call(t, dummy, http.MethodPut, endpoint+"/"+b, ""); w.status != http.StatusOK {
			t.Fatalf("CreateBucket %s = %d %s", b, w.status, w.body)
		}
	}
	if w := s3Call(t, dummy, http.MethodPut, endpoint+"/src/obj", "x"); w.status != http.StatusOK {
		t.Fatalf("PutObject src/obj = %d %s", w.status, w.body)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"DenyRead","Effect":"Deny","Principal":"*","Action":"s3:GetObject",` +
		`"Resource":"arn:aws:s3:::src/*","Condition":{"Null":{"` + algorithm + `":"true"}}}]}`
	if w := s3Call(t, dummy, http.MethodPut, endpoint+"/src?policy", policy); w.status/100 != 2 {
		t.Fatalf("PutBucketPolicy = %d %s", w.status, w.body)
	}

	w := s3Put(t, u, endpoint+"/dst/copy", "", map[string]string{"x-amz-copy-source": "src/obj", "x-amz-server-side-encryption": "AES256"})
	want := `User: arn:aws:iam::000000000000:user/u is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::src/obj"` +
		` with an explicit deny in a resource-based policy`
	if e := parseXMLError(t, w); w.status != http.StatusForbidden || e.S3Code != "AccessDenied" || e.S3Message != want {
		t.Errorf("CopyObject with an SSE header under a source-read Null deny = %d %s, want 403 AccessDenied %q", w.status, w.body, want)
	}
	got := enforcementAdvisories(t, c, "condition-key-unsupplied")
	if !slices.ContainsFunc(got, func(text string) bool {
		return strings.Contains(text, algorithm) && strings.Contains(text, "for s3:GetObject on arn:aws:s3:::src/obj")
	}) {
		t.Errorf("condition-key-unsupplied advisories = %q, want one for %s on the source read", got, algorithm)
	}
}
