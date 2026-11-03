package s3_test

import (
	"crypto/md5" //nolint:gosec // Content-MD5 is an MD5; the test computes it
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/checksum"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// Object Lock answers of s3-ssec-object-lock.md 6 and 7. Recorded ones cite
// LocalStack's test_s3.snapshot.json (localstack@8b9a79f, validated
// 2026-02-21) by line.
var (
	// L9478, L9639, L10044, L17856.
	errProtected = failure{status: 403, code: "AccessDenied", message: "Access Denied because object protected by object lock."}
	// L9468, L9500, L10002.
	errMalformed = failure{status: 400, code: "MalformedXML", message: "The XML you provided was not well-formed or did not validate against our published schema"}
	// L9458, L9919.
	errNoLockState = failure{status: 404, code: "NoSuchObjectLockConfiguration", message: "The specified object does not have a ObjectLock configuration"}
	// L9510, L9520, L9992, L10012.
	errNoLockConfig = failure{status: 400, code: "InvalidRequest", message: "Bucket is missing Object Lock Configuration"}
	// A write's lock headers on a bucket without Object Lock (S3SOL-17,
	// recorded 2026-11-03).
	errNoLockConfigOnWrite = failure{status: 400, code: "InvalidRequest", message: "Bucket is missing ObjectLockConfiguration"}
	// LocalStack test_s3_api.snapshot.json L3535, L3546, L3557.
	errBypassWithoutLock = failure{
		status: 400, code: "InvalidArgument", message: "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.",
		fields: map[string]string{"ArgumentName": "x-amz-bypass-governance-retention"},
	}
	// testdata/parity/s3/R12/11-put-without-checksum.json.
	errNeedsChecksum = failure{status: 400, code: "InvalidRequest", message: "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters"}
)

// withMD5 is a part declaring data's Content-MD5, which a part of an
// upload under Object Lock needs (S3SOL-15 15-part-no-checksum, recorded
// 2026-11-03).
func withMD5(data string) s3api.UploadPartInput {
	return s3api.UploadPartInput{ContentMD5: new(base64.StdEncoding.EncodeToString(md5Sum([]byte(data))))}
}

// errMarker is the 405 for a lock operation on a delete marker by version
// id (L9805, L9817).
func errMarker(method string) failure {
	return failure{
		status: 405, code: "MethodNotAllowed", message: "The specified method is not allowed against this resource.",
		fields: map[string]string{"Method": method, "ResourceType": "DeleteMarker"},
	}
}

// errEventHold is understudy's 501 for an event-hold header or element it
// does not implement (s3-ssec-object-lock.md 11.3, 11.7).
func errEventHold(name string) failure {
	return failure{
		status: 501, code: "NotImplemented",
		message: "understudy does not implement Object Lock event holds (variable retention), so it refuses " + name +
			"; nothing was changed. Send the request without it. Run 'understudy coverage --service s3' for what is supported.",
		fields: map[string]string{"Header": name},
	}
}

// javaDateOf is t as Java's Date.toString() renders it in US Pacific time,
// the form of a retain-until date in an error's ArgumentValue (L9488;
// switch s3.object-lock.header-texts). PDT runs from 2:00 PST on the second
// Sunday of March to 2:00 PDT on the first Sunday of November
// (15 U.S.C. 260a, https://www.law.cornell.edu/uscode/text/15/260a).
func javaDateOf(t time.Time) string {
	sunday := func(y int, m time.Month, n int) time.Time {
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		return first.AddDate(0, 0, (7-int(first.Weekday()))%7+7*(n-1))
	}
	t = t.UTC()
	zone := time.FixedZone("PST", -8*3600)
	if start, end := sunday(t.Year(), time.March, 2).Add(10*time.Hour), sunday(t.Year(), time.November, 1).Add(9*time.Hour); !t.Before(start) && t.Before(end) {
		zone = time.FixedZone("PDT", -7*3600)
	}
	return t.In(zone).Format("Mon Jan 02 15:04:05 MST 2006")
}

// hundredYears is the longest retention S3 accepts, 36,500 days
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html:
// "The maximum retention period is 100 years").
const hundredYears = 36500 * 24 * time.Hour

// lockBucket creates name with Object Lock, which turns versioning on
// (s3-ssec-object-lock.md 5.1).
func (h *harness) lockBucket(name string) {
	h.Helper()
	ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: name, ObjectLockEnabledForBucket: new(true)})
}

// setDefault puts bucket's Object Lock configuration with a default
// retention of mode for days or years.
func (h *harness) setDefault(bucket string, mode s3api.ObjectLockRetentionMode, days, years *int32) {
	h.Helper()
	ok(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: bucket, ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
		ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
		Rule:              &s3api.ObjectLockRule{DefaultRetention: &s3api.DefaultRetention{Mode: &mode, Days: days, Years: years}},
	}})
}

// contentMD5 is content's Content-MD5 header.
func contentMD5(content string) string {
	sum := md5.Sum([]byte(content)) //nolint:gosec // see the import
	return base64.StdEncoding.EncodeToString(sum[:])
}

// putLocked writes content to bucket/key with Content-MD5, which a write
// that gets a retention needs (s3-ssec-object-lock.md 6.3), and in's lock
// headers.
func (h *harness) putLocked(bucket, key, content string, in s3api.PutObjectInput) (*s3api.PutObjectOutput, *awserr.Error) {
	in.Bucket, in.Key, in.Body, in.ContentMD5 = bucket, key, bytesBody(content), new(contentMD5(content))
	return as(h, who{}, s3api.OpPutObject, &in)
}

// mustPutLocked is putLocked that fails the test on an error and returns
// the version id.
func (h *harness) mustPutLocked(bucket, key, content string, in s3api.PutObjectInput) *string {
	h.Helper()
	out, err := h.putLocked(bucket, key, content, in)
	if err != nil {
		h.Fatalf("PutObject %s/%s: %s %q", bucket, key, err.ID.QueryCode, err.Message)
	}
	if out.VersionId == nil {
		h.Fatalf("PutObject %s/%s to a lock bucket reported no version id", bucket, key)
	}
	return out.VersionId
}

// retention is the lock headers of a retention of mode until t.
func retention(mode s3api.ObjectLockMode, t time.Time) s3api.PutObjectInput {
	return s3api.PutObjectInput{ObjectLockMode: &mode, ObjectLockRetainUntilDate: &t}
}

// lockState is a version's lock headers as HEAD reports them.
type lockState struct {
	mode  *s3api.ObjectLockMode
	until *time.Time
	hold  *s3api.ObjectLockLegalHoldStatus
}

// headLock reads bucket/key's lock headers with HEAD; version nil is the
// current version.
func (h *harness) headLock(bucket, key string, version *string) lockState {
	h.Helper()
	out := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: bucket, Key: key, VersionId: version})
	return lockState{mode: out.ObjectLockMode, until: out.ObjectLockRetainUntilDate, hold: out.ObjectLockLegalHoldStatus}
}

// wantRetention fails the test unless l reports mode until want.
func wantRetention(t *testing.T, what string, l lockState, mode s3api.ObjectLockMode, want time.Time) {
	t.Helper()
	if l.mode == nil || *l.mode != mode || l.until == nil || !l.until.Equal(want) {
		t.Errorf("%s: mode %v until %v, want %s until %s", what, deref(l.mode), deref(l.until), mode, want.Format(time.RFC3339Nano))
	}
}

// wantNoRetention fails the test if l reports a retention.
func wantNoRetention(t *testing.T, what string, l lockState) {
	t.Helper()
	if l.mode != nil || l.until != nil {
		t.Errorf("%s: mode %v until %v, want no retention", what, deref(l.mode), deref(l.until))
	}
}

// putRetention runs PutObjectRetention on bucket/key at version; mode ""
// with a zero until sends an empty <Retention/>.
func (h *harness) putRetention(bucket, key string, version *string, mode s3api.ObjectLockRetentionMode, until time.Time, bypass *bool) *awserr.Error {
	r := &s3api.ObjectLockRetention{}
	if mode != "" {
		r.Mode = &mode
	}
	if !until.IsZero() {
		r.RetainUntilDate = &until
	}
	_, err := as(h, who{}, s3api.OpPutObjectRetention, &s3api.PutObjectRetentionInput{Bucket: bucket, Key: key, VersionId: version, Retention: r, BypassGovernanceRetention: bypass})
	return err
}

// putHold runs PutObjectLegalHold with status on bucket/key at version.
func (h *harness) putHold(bucket, key string, version *string, status s3api.ObjectLockLegalHoldStatus) *awserr.Error {
	_, err := as(h, who{}, s3api.OpPutObjectLegalHold, &s3api.PutObjectLegalHoldInput{
		Bucket: bucket, Key: key, VersionId: version, LegalHold: &s3api.ObjectLockLegalHold{Status: &status},
	})
	return err
}

// advance moves the emulator clock forward by d.
func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// deleteVersion runs DeleteObject on bucket/key at version with the bypass
// header bypass.
func (h *harness) deleteVersion(bucket, key string, version *string, bypass *bool) (*s3api.DeleteObjectOutput, *awserr.Error) {
	return as(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: bucket, Key: key, VersionId: version, BypassGovernanceRetention: bypass})
}

// lockWrites runs PutObject, CopyObject (from plain/src) and
// CreateMultipartUpload of key in bucket with l's lock headers, as each
// write that carries them (s3-ssec-object-lock.md 6.1).
func lockWrites(h *harness, bucket, key string, l s3api.PutObjectInput) map[string]*awserr.Error {
	_, put := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
		Bucket: bucket, Key: key, Body: &body{r: unreadable{t: h.T}, n: 1}, ContentMD5: new(contentMD5("x")),
		ObjectLockMode: l.ObjectLockMode, ObjectLockRetainUntilDate: l.ObjectLockRetainUntilDate, ObjectLockLegalHoldStatus: l.ObjectLockLegalHoldStatus,
		ObjectLockEventHold: l.ObjectLockEventHold, ObjectLockEventHoldDurationDays: l.ObjectLockEventHoldDurationDays, ObjectLockEventHoldDurationYears: l.ObjectLockEventHoldDurationYears,
	})
	_, cp := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{
		Bucket: bucket, Key: key, CopySource: "plain/src",
		ObjectLockMode: l.ObjectLockMode, ObjectLockRetainUntilDate: l.ObjectLockRetainUntilDate, ObjectLockLegalHoldStatus: l.ObjectLockLegalHoldStatus,
		ObjectLockEventHold: l.ObjectLockEventHold, ObjectLockEventHoldDurationDays: l.ObjectLockEventHoldDurationDays, ObjectLockEventHoldDurationYears: l.ObjectLockEventHoldDurationYears,
	})
	_, mpu := as(h, who{}, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{
		Bucket: bucket, Key: key,
		ObjectLockMode: l.ObjectLockMode, ObjectLockRetainUntilDate: l.ObjectLockRetainUntilDate, ObjectLockLegalHoldStatus: l.ObjectLockLegalHoldStatus,
		ObjectLockEventHold: l.ObjectLockEventHold, ObjectLockEventHoldDurationDays: l.ObjectLockEventHoldDurationDays, ObjectLockEventHoldDurationYears: l.ObjectLockEventHoldDurationYears,
	})
	return map[string]*awserr.Error{"PutObject": put, "CopyObject": cp, "CreateMultipartUpload": mpu}
}

// nothingWritten fails the test if bucket holds key or an upload.
func nothingWritten(t *testing.T, h *harness, bucket, key string) {
	t.Helper()
	if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: bucket, Key: key}); err == nil {
		t.Errorf("a refused write left %s/%s", bucket, key)
	}
	if ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: bucket}); len(ups.Uploads) != 0 {
		t.Errorf("a refused CreateMultipartUpload left %d uploads in %s", len(ups.Uploads), bucket)
	}
}

// TestObjectLockEventHoldsAreNotImplemented: understudy does not implement
// variable retention, so the event-hold headers of the three writes, the
// EventHold and EventHoldDuration elements of PutObjectRetention and
// DefaultEventHold of PutObjectLockConfiguration are 501 naming the first
// one present, on any bucket, and change nothing (s3-ssec-object-lock.md
// 6.9, 11.3, 11.7).
func TestObjectLockEventHoldsAreNotImplemented(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "src", "source")
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(24 * time.Hour)
		for _, tc := range []struct {
			name string
			l    s3api.PutObjectInput
		}{
			{"x-amz-object-lock-event-hold", s3api.PutObjectInput{ObjectLockEventHold: new(s3api.ObjectLockEventHoldOn), ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until}},
			{"x-amz-object-lock-event-hold-duration-days", s3api.PutObjectInput{ObjectLockEventHoldDurationDays: new(int32(1)), ObjectLockEventHoldDurationYears: new(int32(1))}},
			{"x-amz-object-lock-event-hold-duration-years", s3api.PutObjectInput{ObjectLockEventHoldDurationYears: new(int32(1)), ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn)}},
		} {
			for _, bucket := range []string{"plain", "locked"} {
				for op, err := range lockWrites(h, bucket, "refused", tc.l) {
					errEventHold(tc.name).is(t, op+" on "+bucket+" with "+tc.name, err)
				}
				nothingWritten(t, h, bucket, "refused")
			}
		}

		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		gov := s3api.ObjectLockRetentionModeGovernance
		_, err := as(h, who{}, s3api.OpPutObjectRetention, &s3api.PutObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v, Retention: &s3api.ObjectLockRetention{
			Mode: &gov, RetainUntilDate: &until, EventHold: new(s3api.ObjectLockEventHoldOn),
		}})
		errEventHold("EventHold").is(t, "PutObjectRetention with EventHold", err)
		_, err = as(h, who{}, s3api.OpPutObjectRetention, &s3api.PutObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v, Retention: &s3api.ObjectLockRetention{
			Mode: &gov, RetainUntilDate: &until, EventHoldDuration: &s3api.EventHoldDuration{Days: new(int32(1))},
		}})
		errEventHold("EventHoldDuration").is(t, "PutObjectRetention with EventHoldDuration", err)
		wantNoRetention(t, "after the refused retentions", h.headLock("locked", "k", v))

		_, err = as(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: "locked", ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
			ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
			Rule: &s3api.ObjectLockRule{DefaultRetention: &s3api.DefaultRetention{
				Mode: &gov, Days: new(int32(1)), DefaultEventHold: &s3api.EventHoldDuration{Days: new(int32(1))},
			}},
		}})
		errEventHold("DefaultEventHold").is(t, "PutObjectLockConfiguration with DefaultEventHold", err)
		cfg := ok(h, who{}, s3api.OpGetObjectLockConfiguration, &s3api.GetObjectLockConfigurationInput{Bucket: "locked"})
		if cfg.ObjectLockConfiguration == nil || cfg.ObjectLockConfiguration.Rule != nil {
			t.Errorf("the refused configuration left a rule: %+v", cfg.ObjectLockConfiguration)
		}
	})
}

// TestObjectLockHeaderValidation: on a lock bucket the three writes check
// their lock headers before the body: mode and date must come together,
// checked before the mode's value (L9754, L17916, L17927), the mode must be
// GOVERNANCE or COMPLIANCE (L17938, L17950), and the legal hold, the past
// date and the date over 100 years follow switch
// s3.object-lock.header-texts. Nothing is written.
func TestObjectLockHeaderValidation(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "src", "source")
		h.lockBucket("locked")
		now := h.Clock.Now().Wire()
		future, past, far := now.Add(24*time.Hour), now.Add(-time.Second), now.Add(hundredYears+time.Second)
		bad := s3api.ObjectLockMode("BAD-VALUE")
		gov := s3api.ObjectLockModeGovernance
		both := "x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied"
		code, status := h.switchCode("s3.object-lock.header-texts")
		for _, tc := range []struct {
			name string
			l    s3api.PutObjectInput
			want failure
		}{
			{"mode without date (L9754)", s3api.PutObjectInput{ObjectLockMode: &gov}, failure{status: 400, code: "InvalidArgument", message: both, fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date"}}},
			{"bad mode without date (L17916)", s3api.PutObjectInput{ObjectLockMode: &bad}, failure{status: 400, code: "InvalidArgument", message: both, fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date"}}},
			{"date without mode (L17927)", s3api.PutObjectInput{ObjectLockRetainUntilDate: &future}, failure{status: 400, code: "InvalidArgument", message: both, fields: map[string]string{"ArgumentName": "x-amz-object-lock-mode"}}},
			{"bad mode (L17938, L17950)", s3api.PutObjectInput{ObjectLockMode: &bad, ObjectLockRetainUntilDate: &future}, failure{
				status: 400, code: "InvalidArgument", message: "Unknown wormMode directive.",
				fields: map[string]string{"ArgumentName": "x-amz-object-lock-mode", "ArgumentValue": "BAD-VALUE"},
			}},
			{"bad legal hold", s3api.PutObjectInput{ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatus("MAYBE"))}, failure{
				status: status, code: code, message: "Invalid value for the x-amz-object-lock-legal-hold header.",
				fields: map[string]string{"ArgumentName": "x-amz-object-lock-legal-hold", "ArgumentValue": "MAYBE"},
			}},
			{"past date", retention(gov, past), failure{
				status: status, code: code, message: "The retain until date must be in the future!",
				fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date", "ArgumentValue": javaDateOf(past)},
			}},
			{"date over 100 years", retention(gov, far), failure{
				status: status, code: code, message: "The retain until date in x-amz-object-lock-retain-until-date must be no more than 100 years in the future.",
				fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date", "ArgumentValue": javaDateOf(far)},
			}},
		} {
			for op, err := range lockWrites(h, "locked", "refused", tc.l) {
				tc.want.is(t, op+" "+tc.name, err)
			}
		}
		nothingWritten(t, h, "locked", "refused")
	})
}

// TestObjectLockHeadersNeedALockBucket: valid retention or legal-hold
// headers on a bucket without Object Lock are refused with 400
// InvalidRequest "Bucket is missing ObjectLockConfiguration", without the
// spaces of the retention operations' text, and nothing is written
// (S3SOL-17, recorded 2026-11-03: PutObject, CreateMultipartUpload and
// CopyObject, versioned bucket or not; a hold set OFF is inferred).
func TestObjectLockHeadersNeedALockBucket(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "src", "source")
		missing := errNoLockConfigOnWrite
		until := h.Clock.Now().Wire().Add(24 * time.Hour)
		for name, l := range map[string]s3api.PutObjectInput{
			"retention":      retention(s3api.ObjectLockModeGovernance, until),
			"legal hold ON":  {ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn)},
			"legal hold OFF": {ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOff)},
		} {
			for op, err := range lockWrites(h, "plain", "refused", l) {
				missing.is(t, op+" with "+name, err)
			}
		}
		nothingWritten(t, h, "plain", "refused")
	})
}

// TestObjectLockChecksumRequirement: a PutObject with Object Lock
// parameters needs Content-MD5 or a checksum: from the bucket default with
// no lock header it is refused without one (testdata/parity/s3/R12/11-put-without-checksum.json),
// as are an explicit retention and a legal hold alone, a signed payload
// hash does not count, and a header or trailer checksum does (S3SOL-14,
// recorded 2026-11-03). UploadPart of an upload created with lock headers
// needs one too, with its own text (S3SOL-15 15-part-no-checksum, recorded
// 2026-11-03; under a bucket default it is inferred to need one as
// PutObject does), and CopyObject needs none and gets the default (switch
// s3.object-lock.copy-default).
func TestObjectLockChecksumRequirement(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.checksum-scope", "any-lock-parameter-and-parts")
		h.follows("s3.object-lock.copy-default", "default-applied")
		h.createBucket("plain")
		h.put("plain", "src", "source")
		h.lockBucket("defaulted")
		h.setDefault("defaulted", s3api.ObjectLockRetentionModeGovernance, new(int32(1)), nil)
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "defaulted", Key: "bare", Body: &body{r: unreadable{t: h.T}, n: 10}})
		errNeedsChecksum.is(t, "PutObject without a checksum into a default-retention bucket", err)
		sum := sha256.Sum256([]byte("hello"))
		_, err = withHeader(h, s3api.OpPutObject, http.Header{"X-Amz-Content-Sha256": {hex.EncodeToString(sum[:])}}, &s3api.PutObjectInput{Bucket: "defaulted", Key: "bare", Body: bytesBody("hello")})
		errNeedsChecksum.is(t, "PutObject with only a signed payload hash", err)
		nothingWritten(t, h, "defaulted", "bare")

		h.mustPutLocked("defaulted", "md5", "hello", s3api.PutObjectInput{})
		crc := headerFor(checksum.CRC32, sumOf(h, checksum.CRC32, []byte("hello")))
		crc.Bucket, crc.Key, crc.Body = "defaulted", "crc", bytesBody("hello")
		if _, err := as(h, who{}, s3api.OpPutObject, &crc); err != nil {
			t.Errorf("PutObject with x-amz-checksum-crc32: %s %q", err.ID.QueryCode, err.Message)
		}
		const streamed = "a locked body"
		if _, err := withHeader(h, s3api.OpPutObject, chunkedHeader(len(streamed)), &s3api.PutObjectInput{
			Bucket: "defaulted", Key: "trailer", ContentEncoding: new("aws-chunked"), ChecksumAlgorithm: new(s3api.ChecksumAlgorithmCrc32),
			Body: bytesBody(chunked(t, streamed, sumOf(h, checksum.CRC32, []byte(streamed)))),
		}); err != nil {
			t.Errorf("PutObject with a CRC32 trailer: %s %q", err.ID.QueryCode, err.Message)
		}

		copied := ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "defaulted", Key: "copied", CopySource: "plain/src"})
		state := h.headLock("defaulted", "copied", copied.VersionId)
		if state.mode == nil || *state.mode != s3api.ObjectLockModeGovernance || state.until == nil {
			t.Errorf("a copy into the default-retention bucket reports %v until %v, want the GOVERNANCE default", deref(state.mode), deref(state.until))
		}
		errNeedsPartChecksum := failure{status: 400, code: "InvalidRequest", message: "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Part requests with Object Lock parameters"}
		id := h.startUpload("defaulted", "parts", s3api.CreateMultipartUploadInput{})
		_, err = uploadPart(h, "defaulted", "parts", id, 1, []byte("part"), s3api.UploadPartInput{})
		errNeedsPartChecksum.is(t, "UploadPart without a checksum into a default-retention bucket", err)

		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(time.Hour)
		_, err = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "locked", Key: "explicit", Body: &body{r: unreadable{t: h.T}, n: 1},
			ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until,
		})
		errNeedsChecksum.is(t, "PutObject with a retention and no checksum", err)
		_, err = as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "locked", Key: "held", Body: &body{r: unreadable{t: h.T}, n: 1}, ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn),
		})
		errNeedsChecksum.is(t, "PutObject with only a legal hold and no checksum", err)

		locked := h.startUpload("locked", "parts", s3api.CreateMultipartUploadInput{ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until})
		_, err = uploadPart(h, "locked", "parts", locked, 1, []byte("part"), s3api.UploadPartInput{})
		errNeedsPartChecksum.is(t, "UploadPart without a checksum of an upload with lock headers", err)
		h.sendPart("locked", "parts", locked, 1, []byte("part"), withMD5("part"))
		plain := h.startUpload("locked", "plain", s3api.CreateMultipartUploadInput{})
		if _, err := uploadPart(h, "locked", "plain", plain, 1, []byte("part"), s3api.UploadPartInput{}); err != nil {
			t.Errorf("UploadPart without a checksum of an upload without lock settings: %s %q", err.ID.QueryCode, err.Message)
		}
	})
}

// TestObjectLockDefaultRetention: a version written into a bucket with a
// default retention gets its creation instant plus the period
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html,
// "Retention periods"; LocalStack test_s3.snapshot.json L9711), a year
// being a calendar year (switch s3.object-lock.default-years); explicit headers
// override it; changing the rule changes only later versions; a rule put
// without a default removes it (LocalStack test_s3_api.snapshot.json L2501).
// A multipart object is dated at its initiation (testdata/parity/s3/R12/notes.md
// step 8) and carries Create's lock headers, which understudy must apply
// rather than drop (s3-ssec-object-lock.md 11.7).
func TestObjectLockDefaultRetention(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.default-years", "calendar-years")
		day := 24 * time.Hour
		h.lockBucket("locked")
		h.setDefault("locked", s3api.ObjectLockRetentionModeGovernance, new(int32(1)), nil)
		created := h.Clock.Now().Wire()
		v1 := h.mustPutLocked("locked", "k", "one", s3api.PutObjectInput{})
		wantRetention(t, "default of 1 day", h.headLock("locked", "k", v1), s3api.ObjectLockModeGovernance, created.Add(day))

		explicit := created.Add(5*day + 250*time.Millisecond)
		v2 := h.mustPutLocked("locked", "k", "two", retention(s3api.ObjectLockModeCompliance, explicit))
		wantRetention(t, "explicit headers over the default", h.headLock("locked", "k", v2), s3api.ObjectLockModeCompliance, explicit)

		h.advance(time.Hour)
		// Years are calendar years: 2 years from 2026 cross 2028-02-29, 731
		// days (S3SOL-13 13-head, recorded 2026-11-03).
		h.setDefault("locked", s3api.ObjectLockRetentionModeCompliance, nil, new(int32(2)))
		later := h.Clock.Now().Wire()
		v3 := h.mustPutLocked("locked", "k", "three", s3api.PutObjectInput{})
		wantRetention(t, "default of 2 years", h.headLock("locked", "k", v3), s3api.ObjectLockModeCompliance, later.AddDate(2, 0, 0))
		wantRetention(t, "the version written before the rule changed", h.headLock("locked", "k", v1), s3api.ObjectLockModeGovernance, created.Add(day))

		ok(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: "locked", ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
			ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
		}})
		v4 := h.mustPutLocked("locked", "k", "four", s3api.PutObjectInput{})
		wantNoRetention(t, "after the default was removed", h.headLock("locked", "k", v4))

		h.setDefault("locked", s3api.ObjectLockRetentionModeGovernance, new(int32(2)), nil)
		initiated := h.Clock.Now().Wire()
		id := h.startUpload("locked", "mpu", s3api.CreateMultipartUploadInput{})
		h.advance(time.Hour)
		part := h.sendPart("locked", "mpu", id, 1, []byte("only part"), withMD5("only part"))
		done, err := complete(h, "locked", "mpu", id, []s3api.CompletedPart{listed(1, part)}, s3api.CompleteMultipartUploadInput{})
		if err != nil {
			t.Fatalf("Complete in a default-retention bucket: %s %q", err.ID.QueryCode, err.Message)
		}
		wantRetention(t, "a multipart object under the default", h.headLock("locked", "mpu", done.VersionId), s3api.ObjectLockModeGovernance, initiated.Add(2*day))

		until := h.Clock.Now().Wire().Add(3*day + 7*time.Millisecond)
		id = h.startUpload("locked", "mpu", s3api.CreateMultipartUploadInput{
			ObjectLockMode: new(s3api.ObjectLockModeCompliance), ObjectLockRetainUntilDate: &until, ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn),
		})
		part = h.sendPart("locked", "mpu", id, 1, []byte("only part"), withMD5("only part"))
		done, err = complete(h, "locked", "mpu", id, []s3api.CompletedPart{listed(1, part)}, s3api.CompleteMultipartUploadInput{})
		if err != nil {
			t.Fatalf("Complete of an upload with lock headers: %s %q", err.ID.QueryCode, err.Message)
		}
		state := h.headLock("locked", "mpu", done.VersionId)
		wantRetention(t, "a multipart object with Create's lock headers", state, s3api.ObjectLockModeCompliance, until)
		if state.hold == nil || *state.hold != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("a multipart object with Create's legal hold reports %v, want ON", deref(state.hold))
		}
	})
}

// TestObjectLockReadHeaders: GET and HEAD report the mode and the date to
// the millisecond whenever a version has a retention (s3-ssec-object-lock.md
// 6.7; switch s3.object-lock.retain-until-format), expired or not, and the
// legal hold once one was ever set (L9935; "not returned if the specified
// version of this object has never had a legal hold applied",
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html).
func TestObjectLockReadHeaders(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.retain-until-format", "milliseconds-header-trimmed")
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(time.Hour + 123456789)
		l := retention(s3api.ObjectLockModeGovernance, until)
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		v := h.mustPutLocked("locked", "k", "v", l)
		ms := until.Truncate(time.Millisecond)
		state := h.headLock("locked", "k", v)
		wantRetention(t, "HEAD", state, s3api.ObjectLockModeGovernance, ms)
		if state.hold == nil || *state.hold != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("HEAD legal hold = %v, want ON", deref(state.hold))
		}
		_, get := h.read(&s3api.GetObjectInput{Bucket: "locked", Key: "k", VersionId: v})
		wantRetention(t, "GET", lockState{mode: get.ObjectLockMode, until: get.ObjectLockRetainUntilDate}, s3api.ObjectLockModeGovernance, ms)
		if get.ObjectLockLegalHoldStatus == nil || *get.ObjectLockLegalHoldStatus != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("GET legal hold = %v, want ON", deref(get.ObjectLockLegalHoldStatus))
		}
		plain := h.mustPutLocked("locked", "plain", "v", s3api.PutObjectInput{})
		if state := h.headLock("locked", "plain", plain); state.hold != nil || state.mode != nil {
			t.Errorf("HEAD of a version never locked = %v %v, want no lock headers", deref(state.mode), deref(state.hold))
		}
		h.advance(2 * time.Hour)
		wantRetention(t, "HEAD after the retention expired", h.headLock("locked", "k", v), s3api.ObjectLockModeGovernance, ms)
	})
}

// TestObjectRetentionRefusals: PutObjectRetention and GetObjectRetention
// refuse in the order of s3-ssec-object-lock.md 11.3: the body
// (MalformedXML for a mode without a date or a bad mode, L9468, L9500, and
// for a date without a mode under switch s3.object-lock.date-without-mode),
// a missing bucket (L9436), a bucket without Object Lock (L9510, L9520),
// checked before the key under switch s3.object-lock.check-order, a missing
// key (L9447), a delete marker (L9805, L9817), a version never given a
// retention (L9458), and the date: in the past (L9488) or over 100 years
// ahead (switch s3.object-lock.header-texts).
func TestObjectRetentionRefusals(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "k", "v")
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		now := h.Clock.Now().Wire()
		future := now.Add(24 * time.Hour)
		gov := s3api.ObjectLockRetentionModeGovernance
		getRetention := func(bucket, key string, version *string) *awserr.Error {
			_, err := as(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: bucket, Key: key, VersionId: version})
			return err
		}

		err := h.putRetention("no-such-bucket", "k", nil, gov, future, nil)
		wantCode(t, err, "NoSuchBucket", http.StatusNotFound)
		wantFields(t, err, map[string]string{"BucketName": "no-such-bucket"})
		errNoLockConfig.is(t, "PutObjectRetention on a bucket without Object Lock (L9510)", h.putRetention("plain", "k", nil, gov, future, nil))
		errNoLockConfig.is(t, "GetObjectRetention on a bucket without Object Lock (L9520)", getRetention("plain", "k", nil))
		h.follows("s3.object-lock.check-order", "lock-state-first")
		errNoLockConfig.is(t, "PutObjectRetention of a missing key on a bucket without Object Lock", h.putRetention("plain", "missing", nil, gov, future, nil))
		err = h.putRetention("locked", "non-existing-key", nil, gov, future, nil)
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		wantFields(t, err, map[string]string{"Key": "non-existing-key"})
		errNoLockState.is(t, "GetObjectRetention of a version never given one (L9458)", getRetention("locked", "k", v))

		errMalformed.is(t, "Mode without RetainUntilDate (L9468)", h.putRetention("locked", "k", v, gov, time.Time{}, new(true)))
		errMalformed.is(t, "a bad Mode (L9500)", h.putRetention("locked", "k", v, "BAD-VALUE", future, nil))
		code, status := h.switchCode("s3.object-lock.date-without-mode")
		failure{status: status, code: code, message: errMalformed.message}.is(t, "RetainUntilDate without Mode", h.putRetention("locked", "k", v, "", future, nil))
		_, err = as(h, who{}, s3api.OpPutObjectRetention, &s3api.PutObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v})
		errMalformed.is(t, "no Retention body (s3-ssec-object-lock.md 11.3)", err)

		past := now.Add(-time.Second)
		failure{
			status: 400, code: "InvalidArgument", message: "The retain until date must be in the future!",
			fields: map[string]string{"ArgumentName": "RetainUntilDate", "ArgumentValue": javaDateOf(past)},
		}.is(t, "a past RetainUntilDate (L9488)", h.putRetention("locked", "k", v, gov, past, nil))
		far := now.Add(hundredYears + time.Second)
		farCode, farStatus := h.switchCode("s3.object-lock.header-texts")
		failure{
			status: farStatus, code: farCode, message: "The retain until date in RetainUntilDate must be no more than 100 years in the future.",
			fields: map[string]string{"ArgumentName": "RetainUntilDate", "ArgumentValue": javaDateOf(far)},
		}.is(t, "a RetainUntilDate over 100 years ahead", h.putRetention("locked", "k", v, gov, far, nil))
		errNoLockState.is(t, "GetObjectRetention after only refused puts", getRetention("locked", "k", v))

		marker := ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "locked", Key: "k"})
		errMarker("PUT").is(t, "PutObjectRetention on a delete marker (L9805)", h.putRetention("locked", "k", marker.VersionId, gov, future, nil))
		errMarker("GET").is(t, "GetObjectRetention on a delete marker (L9817)", getRetention("locked", "k", marker.VersionId))
	})
}

// TestObjectRetentionChanges walks a version through the change rules of
// s3-ssec-object-lock.md 6.4 and 6.6: set (L9607), extend GOVERNANCE
// without bypass (L9871), shorten it only with bypass (L9478), remove it
// only with bypass (L9677), read it back removed (switch
// s3.object-lock.cleared-reads), set a new one without bypass (L9683),
// move to COMPLIANCE only with bypass (switch s3.object-lock.mode-upgrade),
// and never shorten COMPLIANCE or turn it into GOVERNANCE, bypass or not
// (L17884, L17894; "its retention mode can't be changed, and its retention
// period can't be shortened",
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html).
func TestObjectRetentionChanges(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.cleared-reads", "404-and-OFF")
		h.follows("s3.object-lock.mode-upgrade", "refused")
		h.follows("s3.object-lock.retain-until-format", "milliseconds-header-trimmed")
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		now := h.Clock.Now().Wire()
		day := 24 * time.Hour
		gov, comp := s3api.ObjectLockRetentionModeGovernance, s3api.ObjectLockRetentionModeCompliance
		get := func() (*s3api.ObjectLockRetention, *awserr.Error) {
			out, err := as(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v})
			if err != nil {
				return nil, err
			}
			return out.Retention, nil
		}
		wantStored := func(what string, mode s3api.ObjectLockRetentionMode, until time.Time) {
			t.Helper()
			r, err := get()
			if err != nil || r == nil || r.Mode == nil || *r.Mode != mode || r.RetainUntilDate == nil || !r.RetainUntilDate.Equal(until.Truncate(time.Millisecond)) {
				t.Errorf("%s: GetObjectRetention = %+v, %v; want %s until %s", what, r, err, mode, until.Format(time.RFC3339Nano))
			}
		}
		must := func(what string, err *awserr.Error) {
			t.Helper()
			if err != nil {
				t.Errorf("%s: %s %q, want accepted", what, err.ID.QueryCode, err.Message)
			}
		}

		must("set GOVERNANCE (L9607)", h.putRetention("locked", "k", v, gov, now.Add(2*day+5*time.Millisecond), nil))
		wantStored("after set", gov, now.Add(2*day+5*time.Millisecond))
		wantRetention(t, "HEAD after set", h.headLock("locked", "k", v), s3api.ObjectLockModeGovernance, now.Add(2*day+5*time.Millisecond))
		must("extend GOVERNANCE without bypass (L9871)", h.putRetention("locked", "k", v, gov, now.Add(3*day), nil))
		errProtected.is(t, "shorten GOVERNANCE without bypass (L9478)", h.putRetention("locked", "k", v, gov, now.Add(day), nil))
		errProtected.is(t, "shorten GOVERNANCE with bypass false", h.putRetention("locked", "k", v, gov, now.Add(day), new(false)))
		errProtected.is(t, "remove GOVERNANCE without bypass", h.putRetention("locked", "k", v, "", time.Time{}, nil))
		wantStored("after the refused changes", gov, now.Add(3*day))
		must("shorten GOVERNANCE with bypass", h.putRetention("locked", "k", v, gov, now.Add(day), new(true)))
		must("remove GOVERNANCE with bypass (L9677)", h.putRetention("locked", "k", v, "", time.Time{}, new(true)))
		_, err := get()
		errNoLockState.is(t, "GetObjectRetention after removal", err)
		wantNoRetention(t, "HEAD after removal", h.headLock("locked", "k", v))
		must("a short retention after removal, without bypass (L9683)", h.putRetention("locked", "k", v, gov, now.Add(time.Hour), nil))

		errProtected.is(t, "GOVERNANCE to COMPLIANCE without bypass", h.putRetention("locked", "k", v, comp, now.Add(2*day), nil))
		must("GOVERNANCE to COMPLIANCE with bypass", h.putRetention("locked", "k", v, comp, now.Add(2*day), new(true)))
		wantStored("after the move to COMPLIANCE", comp, now.Add(2*day))
		errProtected.is(t, "shorten COMPLIANCE (L17884)", h.putRetention("locked", "k", v, comp, now.Add(day), nil))
		errProtected.is(t, "COMPLIANCE to GOVERNANCE (L17894)", h.putRetention("locked", "k", v, gov, now.Add(3*day), nil))
		errProtected.is(t, "shorten COMPLIANCE with bypass", h.putRetention("locked", "k", v, comp, now.Add(day), new(true)))
		errProtected.is(t, "remove COMPLIANCE with bypass", h.putRetention("locked", "k", v, "", time.Time{}, new(true)))
		must("extend COMPLIANCE", h.putRetention("locked", "k", v, comp, now.Add(4*day), nil))
		wantStored("after the COMPLIANCE refusals", comp, now.Add(4*day))
	})
}

// TestObjectLegalHold: PutObjectLegalHold checks its body before the
// bucket's lock state (L10002), then the bucket (L9970), the lock state
// (L9992, L10012) and the key (L9981); a version never held reads 404
// (L9919), ON reads ON on GET and HEAD (L9950, L9935), OFF reads OFF
// (switch s3.object-lock.cleared-reads; HEAD L10149), a delete marker is
// the 405 (switch s3.object-lock.check-order), and a hold is independent of
// retention (s3-ssec-object-lock.md 6.5).
func TestObjectLegalHold(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.cleared-reads", "404-and-OFF")
		h.follows("s3.object-lock.check-order", "lock-state-first")
		h.createBucket("plain")
		h.put("plain", "k", "v")
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		getHold := func(bucket, key string, version *string) (*s3api.ObjectLockLegalHold, *awserr.Error) {
			out, err := as(h, who{}, s3api.OpGetObjectLegalHold, &s3api.GetObjectLegalHoldInput{Bucket: bucket, Key: key, VersionId: version})
			if err != nil {
				return nil, err
			}
			return out.LegalHold, nil
		}
		on, off := s3api.ObjectLockLegalHoldStatusOn, s3api.ObjectLockLegalHoldStatusOff

		_, err := as(h, who{}, s3api.OpPutObjectLegalHold, &s3api.PutObjectLegalHoldInput{Bucket: "plain", Key: "k"})
		errMalformed.is(t, "no body on a bucket without Object Lock (L10002)", err)
		err = h.putHold("no-such-bucket", "k", nil, on)
		wantCode(t, err, "NoSuchBucket", http.StatusNotFound)
		errNoLockConfig.is(t, "PutObjectLegalHold on a bucket without Object Lock (L9992)", h.putHold("plain", "k", nil, on))
		_, err = getHold("plain", "k", nil)
		errNoLockConfig.is(t, "GetObjectLegalHold on a bucket without Object Lock (L10012)", err)
		errNoLockConfig.is(t, "PutObjectLegalHold of a missing key on a bucket without Object Lock", h.putHold("plain", "missing", nil, on))
		err = h.putHold("locked", "non-existing-key", nil, on)
		wantCode(t, err, "NoSuchKey", http.StatusNotFound)
		wantFields(t, err, map[string]string{"Key": "non-existing-key"})
		_, err = getHold("locked", "k", v)
		errNoLockState.is(t, "GetObjectLegalHold of a version never held (L9919)", err)

		until := h.Clock.Now().Wire().Add(time.Hour)
		if err := h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, until, nil); err != nil {
			t.Fatalf("PutObjectRetention: %s %q", err.ID.QueryCode, err.Message)
		}
		if err := h.putHold("locked", "k", v, on); err != nil {
			t.Fatalf("PutObjectLegalHold ON: %s %q", err.ID.QueryCode, err.Message)
		}
		if got, err := getHold("locked", "k", v); err != nil || got == nil || deref(got.Status) != on {
			t.Errorf("GetObjectLegalHold after ON = %+v, %v; want ON (L9950)", got, err)
		}
		state := h.headLock("locked", "k", v)
		if deref(state.hold) != on {
			t.Errorf("HEAD legal hold after ON = %v, want ON (L9935)", deref(state.hold))
		}
		wantRetention(t, "HEAD retention beside the hold", state, s3api.ObjectLockModeGovernance, until)
		if err := h.putHold("locked", "k", v, off); err != nil {
			t.Fatalf("PutObjectLegalHold OFF: %s %q", err.ID.QueryCode, err.Message)
		}
		if got, err := getHold("locked", "k", v); err != nil || got == nil || deref(got.Status) != off {
			t.Errorf("GetObjectLegalHold after OFF = %+v, %v; want OFF", got, err)
		}
		state = h.headLock("locked", "k", v)
		if deref(state.hold) != off {
			t.Errorf("HEAD legal hold after OFF = %v, want OFF (L10149)", deref(state.hold))
		}
		wantRetention(t, "HEAD retention after the hold was lifted", state, s3api.ObjectLockModeGovernance, until)

		marker := ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "locked", Key: "k"})
		errMarker("PUT").is(t, "PutObjectLegalHold on a delete marker", h.putHold("locked", "k", marker.VersionId, on))
		_, err = getHold("locked", "k", marker.VersionId)
		errMarker("GET").is(t, "GetObjectLegalHold on a delete marker", err)
	})
}

// TestObjectLockConfigurationRules: GetObjectLockConfiguration echoes the
// rule as put, Mode and the one of Days or Years (LocalStack
// test_s3_api.snapshot.json L2486; s3-ssec-object-lock.md 9.2); an enabled
// configuration without a rule removes it (L2501, L2507); a bad mode is
// MalformedXML (L2571); a period under 1 is InvalidArgument "Default
// retention period must be a positive integer value." and one over 36500
// days or 100 years "Default retention period too large.", each naming
// Days or Years and echoing the value (s3-ssec-object-lock.md 5.2;
// S3SOL-13 13-days-0, 13-days-36501, 13-years-101, recorded 2026-11-03;
// Years 0 is inferred from Days 0); the token header is accepted and
// ignored (s3-ssec-object-lock.md 11.1).
func TestObjectLockConfigurationRules(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		put := func(mode s3api.ObjectLockRetentionMode, days, years *int32) *awserr.Error {
			_, err := as(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: "locked", Token: new("token"), ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
				ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
				Rule:              &s3api.ObjectLockRule{DefaultRetention: &s3api.DefaultRetention{Mode: &mode, Days: days, Years: years}},
			}})
			return err
		}
		read := func() *s3api.ObjectLockConfiguration {
			return ok(h, who{}, s3api.OpGetObjectLockConfiguration, &s3api.GetObjectLockConfigurationInput{Bucket: "locked"}).ObjectLockConfiguration
		}
		gov, comp := s3api.ObjectLockRetentionModeGovernance, s3api.ObjectLockRetentionModeCompliance
		for _, tc := range []struct {
			mode        s3api.ObjectLockRetentionMode
			days, years *int32
		}{
			{gov, new(int32(1)), nil}, {comp, nil, new(int32(2))}, {gov, new(int32(36500)), nil}, {comp, nil, new(int32(100))},
		} {
			if err := put(tc.mode, tc.days, tc.years); err != nil {
				t.Errorf("PutObjectLockConfiguration %s days %v years %v: %s %q", tc.mode, deref(tc.days), deref(tc.years), err.ID.QueryCode, err.Message)
				continue
			}
			got := read()
			if got == nil || got.Rule == nil || got.Rule.DefaultRetention == nil {
				t.Errorf("GetObjectLockConfiguration = %+v, want the rule", got)
				continue
			}
			d := got.Rule.DefaultRetention
			if deref(d.Mode) != tc.mode || deref(d.Days) != deref(tc.days) || deref(d.Years) != deref(tc.years) || d.DefaultEventHold != nil {
				t.Errorf("rule read back as mode %v days %v years %v, want %s days %v years %v", deref(d.Mode), deref(d.Days), deref(d.Years), tc.mode, deref(tc.days), deref(tc.years))
			}
		}
		period := func(message, name, value string) failure {
			return failure{status: 400, code: "InvalidArgument", message: message, fields: map[string]string{"ArgumentName": name, "ArgumentValue": value}}
		}
		const positive, tooLarge = "Default retention period must be a positive integer value.", "Default retention period too large."
		period(positive, "Days", "0").is(t, "Days 0", put(gov, new(int32(0)), nil))
		period(tooLarge, "Days", "36501").is(t, "Days 36501", put(gov, new(int32(36501)), nil))
		period(positive, "Years", "0").is(t, "Years 0", put(gov, nil, new(int32(0))))
		period(tooLarge, "Years", "101").is(t, "Years 101", put(gov, nil, new(int32(101))))
		errMalformed.is(t, "bad mode", put("BAD-VALUE", new(int32(1)), nil))
		ok(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: "locked", ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
			ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
		}})
		if got := read(); got == nil || got.Rule != nil || deref(got.ObjectLockEnabled) != s3api.ObjectLockEnabledEnabled {
			t.Errorf("GetObjectLockConfiguration after an enabled-only put = %+v, want Enabled with no rule", got)
		}
	})
}

// TestObjectLockDeletes: a simple delete always adds a marker (L9797,
// L17866); a versioned delete is refused while a legal hold is on, bypass
// or not (L10044; https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html),
// while COMPLIANCE is in force, bypass or not (L17856, L17874), and while
// GOVERNANCE is in force without bypass (L9639; bypass false is none under
// switch s3.object-lock.bypass-without-lock), but bypass lifts GOVERNANCE
// (L9649). Retention runs on the emulator clock: the delete succeeds once it
// passes the date (L17904), at the date itself under switch
// s3.object-lock.retain-until-format. Delete markers are not protected and
// a locked key takes new versions (L10111).
func TestObjectLockDeletes(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.bypass-without-lock", "refused")
		h.follows("s3.object-lock.retain-until-format", "milliseconds-header-trimmed")
		h.lockBucket("locked")
		now := h.Clock.Now().Wire()
		gov := h.mustPutLocked("locked", "gov", "v", retention(s3api.ObjectLockModeGovernance, now.Add(time.Hour)))
		comp := h.mustPutLocked("locked", "comp", "v", retention(s3api.ObjectLockModeCompliance, now.Add(2*time.Hour)))
		held := h.mustPutLocked("locked", "held", "v", s3api.PutObjectInput{ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn)})

		_, err := h.deleteVersion("locked", "gov", gov, nil)
		errProtected.is(t, "versioned delete under GOVERNANCE (L9639)", err)
		_, err = h.deleteVersion("locked", "gov", gov, new(false))
		errProtected.is(t, "versioned delete under GOVERNANCE with bypass false", err)
		_, err = h.deleteVersion("locked", "comp", comp, nil)
		errProtected.is(t, "versioned delete under COMPLIANCE (L17856)", err)
		_, err = h.deleteVersion("locked", "comp", comp, new(true))
		errProtected.is(t, "versioned delete under COMPLIANCE with bypass (L17874)", err)
		_, err = h.deleteVersion("locked", "held", held, nil)
		errProtected.is(t, "versioned delete under a legal hold (L10044)", err)
		_, err = h.deleteVersion("locked", "held", held, new(true))
		errProtected.is(t, "versioned delete under a legal hold with bypass", err)

		marker, err := h.deleteVersion("locked", "comp", nil, nil)
		if err != nil || marker.DeleteMarker == nil || !*marker.DeleteMarker || marker.VersionId == nil {
			t.Fatalf("simple delete of a COMPLIANCE key = %+v, %v; want a delete marker (L17866)", marker, err)
		}
		if out, err := h.deleteVersion("locked", "comp", marker.VersionId, nil); err != nil || out.DeleteMarker == nil || !*out.DeleteMarker {
			t.Errorf("versioned delete of the marker = %+v, %v; want it removed: delete markers are not WORM-protected", out, err)
		}
		if _, err := h.putLocked("locked", "held", "second", s3api.PutObjectInput{}); err != nil {
			t.Errorf("a new version of a held key: %s %q, want accepted (L10111)", err.ID.QueryCode, err.Message)
		}

		if _, err := h.deleteVersion("locked", "gov", gov, new(true)); err != nil {
			t.Errorf("versioned delete under GOVERNANCE with bypass: %s %q, want 204 (L9649)", err.ID.QueryCode, err.Message)
		}
		if err := h.putHold("locked", "held", held, s3api.ObjectLockLegalHoldStatusOff); err != nil {
			t.Fatalf("PutObjectLegalHold OFF: %s %q", err.ID.QueryCode, err.Message)
		}
		if _, err := h.deleteVersion("locked", "held", held, nil); err != nil {
			t.Errorf("versioned delete after the hold was lifted: %s %q", err.ID.QueryCode, err.Message)
		}

		h.advance(2*time.Hour - time.Millisecond)
		_, err = h.deleteVersion("locked", "comp", comp, nil)
		errProtected.is(t, "versioned delete a millisecond before the COMPLIANCE date", err)
		h.advance(time.Millisecond)
		if _, err := h.deleteVersion("locked", "comp", comp, nil); err != nil {
			t.Errorf("versioned delete at the COMPLIANCE date: %s %q, want 204", err.ID.QueryCode, err.Message)
		}
		late := h.mustPutLocked("locked", "late", "v", retention(s3api.ObjectLockModeCompliance, h.Clock.Now().Wire().Add(time.Minute)))
		h.advance(time.Minute + time.Second)
		if _, err := h.deleteVersion("locked", "late", late, nil); err != nil {
			t.Errorf("versioned delete after the emulator clock passed the COMPLIANCE date: %s %q, want 204 (L17904)", err.ID.QueryCode, err.Message)
		}
	})
}

// TestDeleteObjectsUnderLock: DeleteObjects reports each protected version
// as a per-key AccessDenied with its key and version in a 200 (L10054),
// in Quiet mode too ("In quiet mode the response includes only keys where
// the delete action encountered an error",
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html),
// deletes the rest, adds markers for entries without a version, and lifts
// GOVERNANCE with bypass but never a hold.
func TestDeleteObjectsUnderLock(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(time.Hour)
		gov := h.mustPutLocked("locked", "gov", "v", retention(s3api.ObjectLockModeGovernance, until))
		held := h.mustPutLocked("locked", "held", "v", s3api.PutObjectInput{ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn)})
		free := h.mustPutLocked("locked", "free", "v", s3api.PutObjectInput{})
		batch := func(quiet, bypass *bool, objects ...s3api.ObjectIdentifier) *s3api.DeleteObjectsOutput {
			return ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "locked", BypassGovernanceRetention: bypass, Delete: s3api.Delete{Objects: objects, Quiet: quiet}})
		}
		protected := func(what string, out *s3api.DeleteObjectsOutput, want map[string]string) {
			t.Helper()
			got := map[string]string{}
			for _, e := range out.Errors {
				if str(e.Code) != "AccessDenied" || str(e.Message) != errProtected.message {
					t.Errorf("%s: error %s %q for %s, want AccessDenied %q", what, str(e.Code), str(e.Message), str(e.Key), errProtected.message)
				}
				got[str(e.Key)] = str(e.VersionId)
			}
			if len(got) != len(want) {
				t.Errorf("%s: errors for %v, want %v", what, got, want)
				return
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s: errors for %v, want %v", what, got, want)
					return
				}
			}
		}
		out := batch(new(true), nil, s3api.ObjectIdentifier{Key: "gov", VersionId: gov}, s3api.ObjectIdentifier{Key: "held", VersionId: held})
		protected("quiet batch", out, map[string]string{"gov": *gov, "held": *held})
		if len(out.Deleted) != 0 {
			t.Errorf("quiet batch reports %d deleted", len(out.Deleted))
		}
		out = batch(nil, nil, s3api.ObjectIdentifier{Key: "gov", VersionId: gov}, s3api.ObjectIdentifier{Key: "held", VersionId: held},
			s3api.ObjectIdentifier{Key: "free", VersionId: free}, s3api.ObjectIdentifier{Key: "other"})
		protected("batch without bypass", out, map[string]string{"gov": *gov, "held": *held})
		deleted := map[string]bool{}
		for _, d := range out.Deleted {
			deleted[str(d.Key)+"/"+str(d.VersionId)] = true
			if str(d.Key) == "other" && (d.DeleteMarker == nil || !*d.DeleteMarker) {
				t.Errorf("the entry without a version was deleted as %+v, want a delete marker", d)
			}
		}
		if len(out.Deleted) != 2 || !deleted["free/"+*free] {
			t.Errorf("batch without bypass deleted %v, want free/%s and a marker for other", deleted, *free)
		}
		out = batch(nil, new(true), s3api.ObjectIdentifier{Key: "gov", VersionId: gov}, s3api.ObjectIdentifier{Key: "held", VersionId: held})
		protected("batch with bypass", out, map[string]string{"held": *held})
		if len(out.Deleted) != 1 || str(out.Deleted[0].Key) != "gov" {
			t.Errorf("batch with bypass deleted %+v, want gov", out.Deleted)
		}
	})
}

// TestBypassHeaderWithoutObjectLock: x-amz-bypass-governance-retention,
// true or false, on a bucket without Object Lock refuses the whole request
// (LocalStack test_s3_api.snapshot.json L3535, L3546, L3557; on a versioned
// bucket and with a version id under switch
// s3.object-lock.bypass-without-lock) and deletes nothing.
func TestBypassHeaderWithoutObjectLock(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.bypass-without-lock", "refused")
		h.createBucket("plain")
		h.put("plain", "k", "v")
		for _, v := range []bool{true, false} {
			_, err := h.deleteVersion("plain", "k", nil, &v)
			errBypassWithoutLock.is(t, "DeleteObject on an unversioned bucket", err)
		}
		_, err := as(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "plain", BypassGovernanceRetention: new(true), Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{{Key: "k"}}}})
		errBypassWithoutLock.is(t, "DeleteObjects on an unversioned bucket", err)
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "plain", Key: "k"}); got != "v" {
			t.Errorf("object after the refused deletes = %q, want it kept", got)
		}

		h.createBucket("versioned")
		h.enableVersioning("versioned")
		v := h.put("versioned", "k", "v").VersionId
		_, err = h.deleteVersion("versioned", "k", v, new(true))
		errBypassWithoutLock.is(t, "DeleteObject of a version on a versioned bucket without Object Lock", err)
		_, err = as(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "versioned", BypassGovernanceRetention: new(false), Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{{Key: "k", VersionId: v}}}})
		errBypassWithoutLock.is(t, "DeleteObjects of a version on a versioned bucket without Object Lock", err)
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "versioned", Key: "k", VersionId: v}); got != "v" {
			t.Errorf("version after the refused deletes = %q, want it kept", got)
		}
	})
}

// TestHoldAndGovernanceSurviveTheFirstTerraformDestroy follows Terraform's
// bucket emptying (s3-ssec-object-lock.md 9.6): a quiet DeleteObjects with
// bypass reports a version under both a hold and GOVERNANCE as
// AccessDenied; lifting the hold and retrying DeleteObject without bypass
// is still refused, so the first destroy fails as on AWS; a second
// DeleteObjects with bypass deletes it.
func TestHoldAndGovernanceSurviveTheFirstTerraformDestroy(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		l := retention(s3api.ObjectLockModeGovernance, h.Clock.Now().Wire().Add(time.Hour))
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		v := h.mustPutLocked("locked", "k", "v", l)
		out := ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "locked", BypassGovernanceRetention: new(true), Delete: s3api.Delete{
			Objects: []s3api.ObjectIdentifier{{Key: "k", VersionId: v}}, Quiet: new(true),
		}})
		if len(out.Errors) != 1 || str(out.Errors[0].Code) != "AccessDenied" {
			t.Fatalf("first DeleteObjects = errors %+v, want one AccessDenied", out.Errors)
		}
		if err := h.putHold("locked", "k", v, s3api.ObjectLockLegalHoldStatusOff); err != nil {
			t.Fatalf("PutObjectLegalHold OFF: %s %q", err.ID.QueryCode, err.Message)
		}
		_, err := h.deleteVersion("locked", "k", v, nil)
		errProtected.is(t, "the retry without bypass", err)
		out = ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "locked", BypassGovernanceRetention: new(true), Delete: s3api.Delete{
			Objects: []s3api.ObjectIdentifier{{Key: "k", VersionId: v}}, Quiet: new(true),
		}})
		if len(out.Errors) != 0 {
			t.Errorf("second DeleteObjects with bypass = errors %+v, want the version deleted", out.Errors)
		}
	})
}

// TestCopyObjectDoesNotCarryLockState: a copy of a version under
// GOVERNANCE and a legal hold has neither (LocalStack
// test_s3.snapshot.json L9562-L9577, L10202-L10217); it gets its own
// headers, or the target bucket's default under switch
// s3.object-lock.copy-default.
func TestCopyObjectDoesNotCarryLockState(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.copy-default", "default-applied")
		h.lockBucket("locked")
		now := h.Clock.Now().Wire()
		l := retention(s3api.ObjectLockModeGovernance, now.Add(time.Hour))
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		h.mustPutLocked("locked", "src", "v", l)
		copied := ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "locked", Key: "dst", CopySource: "locked/src"})
		if state := h.headLock("locked", "dst", copied.VersionId); state.mode != nil || state.until != nil || state.hold != nil {
			t.Errorf("copy of a locked version reports %v %v %v, want no lock state", deref(state.mode), deref(state.until), deref(state.hold))
		}
		own := now.Add(3 * time.Hour)
		copied = ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{
			Bucket: "locked", Key: "own", CopySource: "locked/src", ObjectLockMode: new(s3api.ObjectLockModeCompliance), ObjectLockRetainUntilDate: &own,
		})
		state := h.headLock("locked", "own", copied.VersionId)
		wantRetention(t, "copy with its own headers", state, s3api.ObjectLockModeCompliance, own)
		if state.hold != nil {
			t.Errorf("copy with its own retention reports the source's hold %v", deref(state.hold))
		}
		h.lockBucket("defaulted")
		h.setDefault("defaulted", s3api.ObjectLockRetentionModeGovernance, new(int32(1)), nil)
		copied = ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "defaulted", Key: "dst", CopySource: "locked/src"})
		state = h.headLock("defaulted", "dst", copied.VersionId)
		wantRetention(t, "copy into a default-retention bucket", state, s3api.ObjectLockModeGovernance, now.Add(24*time.Hour))
		if state.hold != nil {
			t.Errorf("copy into a default-retention bucket reports the source's hold %v", deref(state.hold))
		}
	})
}

// TestLockStateSurvivesSnapshotsAndResetDropsIt: a version's retention and
// hold are state a snapshot keeps, and reset drops the bucket with its
// locked versions, applying no lock (spec risk: reset clears retention).
func TestLockStateSurvivesSnapshotsAndResetDropsIt(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(24 * time.Hour)
		l := retention(s3api.ObjectLockModeCompliance, until)
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		v := h.mustPutLocked("locked", "k", "v", l)
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if _, err := as(h, who{}, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "locked"}); err == nil {
			t.Fatal("reset left the bucket of locked versions")
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import: %v", err)
		}
		state := h.headLock("locked", "k", v)
		wantRetention(t, "after a snapshot round trip", state, s3api.ObjectLockModeCompliance, until)
		if deref(state.hold) != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("legal hold after a snapshot round trip = %v, want ON", deref(state.hold))
		}
	})
}
