package gosdk_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Research: docs/research/s3-ssec-object-lock.md. Recorded texts cite
// LocalStack's AWS-validated snapshot test_s3.snapshot.json
// (localstack@8b9a79f, validated 2026-02-21) by line; the rest name the
// fidelity switch whose declared value the test reads from understudy's
// coverage report.

// switchValue is the Value the running understudy declares for the
// fidelity switch name, from its coverage report.
func switchValue(t *testing.T, name string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.String()+"/_understudy/v1/coverage", http.NoBody)
	ok(t, err, "building the coverage request")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "GET /_understudy/v1/coverage")
	body := readAll(t, resp.Body, "the coverage report")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /_understudy/v1/coverage = %d: %s", resp.StatusCode, body)
	}
	var report struct {
		Switches []struct{ Name, Value string } `json:"switches"`
	}
	ok(t, json.Unmarshal(body, &report), "decoding the coverage report")
	for _, s := range report.Switches {
		if s.Name == name {
			return s.Value
		}
	}
	t.Fatalf("understudy declares no switch %s (s3-ssec-object-lock.md 11.4)", name)
	return ""
}

// ssecKey is an SSE-C key as the Go SDK sends it: the base64 key and the
// base64 MD5 of its bytes, passed through as given (s3-ssec-object-lock.md
// 3.1, 3.12).
type ssecKey struct{ key, md5 string }

func newSSECKey(raw string) ssecKey {
	sum := md5.Sum([]byte(raw))
	return ssecKey{key: base64.StdEncoding.EncodeToString([]byte(raw)), md5: base64.StdEncoding.EncodeToString(sum[:])}
}

// The keys of LocalStack's TestS3SSECEncryption (test_s3.py L12059-L12060).
var (
	ssecKey1 = newSSECKey("1234567890abcdef1234567890abcdef")
	ssecKey2 = newSSECKey("abcdef1234567890abcdef1234567890")
)

// allowSSEC unblocks SSE-C on bucket with a rule holding only
// BlockedEncryptionTypes NONE (API_PutBucketEncryption Example 4).
func allowSSEC(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	_, err := client.PutBucketEncryption(t.Context(), &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{Rules: []types.ServerSideEncryptionRule{{
			BlockedEncryptionTypes: &types.BlockedEncryptionTypes{EncryptionType: []types.EncryptionType{types.EncryptionTypeNone}},
		}}},
	})
	ok(t, err, "PutBucketEncryption BlockedEncryptionTypes NONE")
}

// TestS3SSEC: over HTTPS a new bucket refuses SSE-C writes (switch
// s3.sse-c.blocked-write) until it is unblocked; then the key round-trips
// through PutObject, GetObject and HeadObject, echoed with no
// x-amz-server-side-encryption (L12790, L13190, L13204), and a GET without
// the key or with another is refused (L12801, L12811). Over plain HTTP the
// write is refused under switch s3.sse-c.plain-http.
func TestS3SSEC(t *testing.T) {
	t.Parallel()
	if tlsEndpoint == nil {
		t.Skip(tlsEndpointEnv + " is not set; run.sh sets it to understudy's TLS listener")
	}
	ctx := t.Context()
	client := s3Client(func(o *s3.Options) { o.BaseEndpoint = aws.String(tlsEndpoint.String()) })
	bucket := createBucket(t, client)
	put := func(c *s3.Client, key string, k ssecKey) (*s3.PutObjectOutput, error) {
		return c.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("test_data"),
			SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(k.key), SSECustomerKeyMD5: aws.String(k.md5),
		})
	}

	_, err := put(client, "blocked", ssecKey1)
	apiErr(t, err, "SSE-C PutObject to a new bucket", switchValue(t, "s3.sse-c.blocked-write"))
	wantMessage(t, err, "SSE-C PutObject to a new bucket", "Access Denied")

	allowSSEC(t, client, bucket)
	out, err := put(client, "obj", ssecKey1)
	ok(t, err, "SSE-C PutObject over HTTPS")
	if aws.ToString(out.SSECustomerAlgorithm) != "AES256" || aws.ToString(out.SSECustomerKeyMD5) != ssecKey1.md5 || out.ServerSideEncryption != "" {
		t.Errorf("PutObject = algorithm %q key MD5 %q SSE %q, want AES256, %s and no SSE", aws.ToString(out.SSECustomerAlgorithm), aws.ToString(out.SSECustomerKeyMD5), ssecKey1.md5, out.ServerSideEncryption)
	}
	get, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("obj"),
		SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
	})
	ok(t, err, "GetObject with the key")
	if got := readAll(t, get.Body, "the SSE-C object"); !bytes.Equal(got, []byte("test_data")) {
		t.Errorf("GetObject with the key = %q, want test_data", got)
	}
	if aws.ToString(get.SSECustomerKeyMD5) != ssecKey1.md5 || get.ServerSideEncryption != "" {
		t.Errorf("GetObject = key MD5 %q SSE %q, want %s and no SSE", aws.ToString(get.SSECustomerKeyMD5), get.ServerSideEncryption, ssecKey1.md5)
	}
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("obj"),
		SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey1.key), SSECustomerKeyMD5: aws.String(ssecKey1.md5),
	})
	ok(t, err, "HeadObject with the key")
	if aws.ToString(head.SSECustomerAlgorithm) != "AES256" || aws.ToString(head.SSECustomerKeyMD5) != ssecKey1.md5 {
		t.Errorf("HeadObject = algorithm %q key MD5 %q, want AES256 and %s", aws.ToString(head.SSECustomerAlgorithm), aws.ToString(head.SSECustomerKeyMD5), ssecKey1.md5)
	}
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("obj")})
	apiErr(t, err, "GetObject without the key", "InvalidRequest")
	wantMessage(t, err, "GetObject without the key", "The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.")
	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("obj"),
		SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(ssecKey2.key), SSECustomerKeyMD5: aws.String(ssecKey2.md5),
	})
	apiErr(t, err, "GetObject with another key", "AccessDenied")
	wantStatus(t, err, "GetObject with another key", 403)
	wantMessage(t, err, "GetObject with another key", "Requests specifying Server Side Encryption with Customer provided keys must provide the correct secret key.")

	if v := switchValue(t, "s3.sse-c.plain-http"); v != "refused" {
		t.Fatalf("switch s3.sse-c.plain-http declares %q; this test checks %q", v, "refused")
	}
	_, err = put(s3Client(), "plain", ssecKey1)
	apiErr(t, err, "SSE-C PutObject over plain HTTP", "InvalidRequest")
	wantMessage(t, err, "SSE-C PutObject over plain HTTP", "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection.")
}

// lockBucket creates a bucket with Object Lock and, when the test ends,
// lifts every legal hold, deletes every version with the GOVERNANCE bypass
// and deletes the bucket. Tests here use GOVERNANCE only, so cleanup can
// always remove what they wrote (s3-ssec-object-lock.md 12).
func lockBucket(t *testing.T, client *s3.Client) string {
	t.Helper()
	bucket := name("cf-lock")
	_, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket), ObjectLockEnabledForBucket: aws.Bool(true)})
	ok(t, err, "CreateBucket with Object Lock")
	t.Cleanup(func() {
		ctx := context.Background()
		versions, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
		if err == nil {
			for _, v := range versions.Versions {
				_, _ = client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId, LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOff}})
				_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId, BypassGovernanceRetention: aws.Bool(true)})
			}
			for _, m := range versions.DeleteMarkers {
				_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: m.Key, VersionId: m.VersionId})
			}
		}
		_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})
	return bucket
}

// lockedText is the refusal of a delete or change Object Lock forbids
// (L9478, L9639, L10044).
const lockedText = "Access Denied because object protected by object lock."

// TestS3ObjectLock: on a bucket with Object Lock a version written with
// GOVERNANCE retention reports it on HEAD (L9623); its versioned delete is
// refused (L9639) unless x-amz-bypass-governance-retention is true (L9649);
// a legal hold reads back ON (L9950), refuses the versioned delete (L10044)
// and reads OFF once lifted (switch s3.object-lock.cleared-reads); a bucket
// default retention is echoed (LocalStack test_s3_api.snapshot.json L2486)
// and dates a new version at its creation plus the period, within 2
// minutes of Last-Modified as LocalStack asserts it (test_s3.py
// L9945-L9953; L9711).
func TestS3ObjectLock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := s3Client()
	bucket := lockBucket(t, client)

	t.Run("GOVERNANCE retention", func(t *testing.T) {
		until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		out, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("gov"), Body: strings.NewReader("v"),
			ObjectLockMode: types.ObjectLockModeGovernance, ObjectLockRetainUntilDate: aws.Time(until),
		})
		ok(t, err, "PutObject with GOVERNANCE retention")
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("gov"), VersionId: out.VersionId})
		ok(t, err, "HeadObject")
		if head.ObjectLockMode != types.ObjectLockModeGovernance || !aws.ToTime(head.ObjectLockRetainUntilDate).Equal(until) {
			t.Errorf("HeadObject = mode %q until %v, want GOVERNANCE until %v", head.ObjectLockMode, aws.ToTime(head.ObjectLockRetainUntilDate), until)
		}
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("gov"), VersionId: out.VersionId})
		apiErr(t, err, "versioned DeleteObject under GOVERNANCE", "AccessDenied")
		wantStatus(t, err, "versioned DeleteObject under GOVERNANCE", 403)
		wantMessage(t, err, "versioned DeleteObject under GOVERNANCE", lockedText)
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("gov"), VersionId: out.VersionId, BypassGovernanceRetention: aws.Bool(true)})
		ok(t, err, "versioned DeleteObject with the GOVERNANCE bypass")
	})

	t.Run("legal hold", func(t *testing.T) {
		out, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("held"), Body: strings.NewReader("v")})
		ok(t, err, "PutObject")
		_, err = client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId, LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOn}})
		ok(t, err, "PutObjectLegalHold ON")
		hold, err := client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId})
		ok(t, err, "GetObjectLegalHold")
		if hold.LegalHold == nil || hold.LegalHold.Status != types.ObjectLockLegalHoldStatusOn {
			t.Errorf("GetObjectLegalHold = %+v, want ON", hold.LegalHold)
		}
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId, BypassGovernanceRetention: aws.Bool(true)})
		apiErr(t, err, "versioned DeleteObject under a legal hold", "AccessDenied")
		wantMessage(t, err, "versioned DeleteObject under a legal hold", lockedText)
		_, err = client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId, LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOff}})
		ok(t, err, "PutObjectLegalHold OFF")
		if v := switchValue(t, "s3.object-lock.cleared-reads"); v != "404-and-OFF" {
			t.Fatalf("switch s3.object-lock.cleared-reads declares %q; this test checks %q", v, "404-and-OFF")
		}
		hold, err = client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId})
		ok(t, err, "GetObjectLegalHold after OFF")
		if hold.LegalHold == nil || hold.LegalHold.Status != types.ObjectLockLegalHoldStatusOff {
			t.Errorf("GetObjectLegalHold after OFF = %+v, want OFF", hold.LegalHold)
		}
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("held"), VersionId: out.VersionId})
		ok(t, err, "versioned DeleteObject after the hold was lifted")
	})

	t.Run("default retention", func(t *testing.T) {
		defaulted := lockBucket(t, client)
		_, err := client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String(defaulted), ObjectLockConfiguration: &types.ObjectLockConfiguration{
			ObjectLockEnabled: types.ObjectLockEnabledEnabled,
			Rule:              &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{Mode: types.ObjectLockRetentionModeGovernance, Days: aws.Int32(1)}},
		}})
		ok(t, err, "PutObjectLockConfiguration GOVERNANCE 1 day")
		cfg, err := client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(defaulted)})
		ok(t, err, "GetObjectLockConfiguration")
		if cfg.ObjectLockConfiguration == nil || cfg.ObjectLockConfiguration.Rule == nil || cfg.ObjectLockConfiguration.Rule.DefaultRetention == nil {
			t.Fatalf("GetObjectLockConfiguration = %+v, want the default retention", cfg.ObjectLockConfiguration)
		}
		d := cfg.ObjectLockConfiguration.Rule.DefaultRetention
		if d.Mode != types.ObjectLockRetentionModeGovernance || aws.ToInt32(d.Days) != 1 || d.Years != nil {
			t.Errorf("default retention = %s days %v years %v, want GOVERNANCE 1 day", d.Mode, aws.ToInt32(d.Days), d.Years)
		}
		out, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(defaulted), Key: aws.String("k"), Body: strings.NewReader("v")})
		ok(t, err, "PutObject into the default-retention bucket (the SDK sends a checksum)")
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(defaulted), Key: aws.String("k"), VersionId: out.VersionId})
		ok(t, err, "HeadObject")
		want := aws.ToTime(head.LastModified).Add(24 * time.Hour)
		if got := aws.ToTime(head.ObjectLockRetainUntilDate); head.ObjectLockMode != types.ObjectLockModeGovernance || got.Sub(want).Abs() > 2*time.Minute {
			t.Errorf("HeadObject = mode %q until %v, want GOVERNANCE until about %v", head.ObjectLockMode, got, want)
		}
	})
}
