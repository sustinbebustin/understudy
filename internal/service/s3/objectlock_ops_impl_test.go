package s3_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// instant is *p, or the zero time when p is nil.
func instant(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// TestRetentionBodyOnTheWire: a PutObjectRetention without a body decodes
// to no Retention, which is MalformedXML (s3-ssec-object-lock.md 11.3),
// while an empty <Retention/> decodes to an empty one, which removes the
// retention (LocalStack test_s3.snapshot.json L9677).
func TestRetentionBodyOnTheWire(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		codec := restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect { return restxml.S3Dialect(s3api.IdentityInvalidArgument()) }}
		decode := func(payload string) *s3api.PutObjectRetentionInput {
			t.Helper()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/locked/k?retention", http.NoBody)
			var in s3api.PutObjectRetentionInput
			if err := codec.Decode(req, s3api.OpPutObjectRetention.Descriptor(), bytesBody(payload), &in); err != nil {
				t.Fatalf("decode %q: %s %q", payload, err.ID.QueryCode, err.Message)
			}
			return &in
		}
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		if err := h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, h.Clock.Now().Wire().Add(time.Hour), nil); err != nil {
			t.Fatalf("set a retention: %s %q", err.ID.QueryCode, err.Message)
		}

		none := decode("")
		if none.Retention != nil {
			t.Fatalf("an empty body decoded to Retention %+v, want none", none.Retention)
		}
		_, err := as(h, who{}, s3api.OpPutObjectRetention, none)
		errMalformed.is(t, "PutObjectRetention without a body", err)

		empty := decode(`<Retention xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
		if empty.Retention == nil || *empty.Retention != (s3api.ObjectLockRetention{}) {
			t.Fatalf("<Retention/> decoded to %+v, want an empty Retention", empty.Retention)
		}
		empty.VersionId, empty.BypassGovernanceRetention = v, new(true)
		if _, err := as(h, who{}, s3api.OpPutObjectRetention, empty); err != nil {
			t.Fatalf("<Retention/> with bypass: %s %q, want the retention removed", err.ID.QueryCode, err.Message)
		}
		wantNoRetention(t, "HEAD after <Retention/>", h.headLock("locked", "k", v))
	})
}

// TestBodyCheckedBeforeTheBucket: the retention and legal-hold bodies are
// checked before the bucket is looked up, so a bad body on a missing
// bucket is MalformedXML, not NoSuchBucket (inferred order,
// s3-ssec-object-lock.md 11.3; only the body before the lock state is
// recorded, LocalStack test_s3.snapshot.json L10002).
func TestBodyCheckedBeforeTheBucket(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		errMalformed.is(t, "PutObjectRetention of a mode without a date on a missing bucket",
			h.putRetention("no-such-bucket", "k", nil, s3api.ObjectLockRetentionModeGovernance, time.Time{}, nil))
		errMalformed.is(t, "PutObjectLegalHold of a bad status on a missing bucket", h.putHold("no-such-bucket", "k", nil, "MAYBE"))
	})
}

// TestRetentionTargetsOneVersion: retention and legal holds belong to one
// object version (s3-ssec-object-lock.md 11.2): a change by version id
// leaves the other versions alone, adds no version, and keeps the
// version's ETag and Last-Modified; without a version id it changes the
// current version.
func TestRetentionTargetsOneVersion(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		old := h.mustPutLocked("locked", "k", "old", s3api.PutObjectInput{})
		current := h.mustPutLocked("locked", "k", "current", s3api.PutObjectInput{})
		before := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "locked", Key: "k", VersionId: old})
		until := h.Clock.Now().Wire().Add(time.Hour)
		h.advance(time.Minute)

		if err := h.putRetention("locked", "k", old, s3api.ObjectLockRetentionModeGovernance, until, nil); err != nil {
			t.Fatalf("PutObjectRetention of the old version: %s %q", err.ID.QueryCode, err.Message)
		}
		if err := h.putHold("locked", "k", old, s3api.ObjectLockLegalHoldStatusOn); err != nil {
			t.Fatalf("PutObjectLegalHold of the old version: %s %q", err.ID.QueryCode, err.Message)
		}
		wantRetention(t, "HEAD of the old version", h.headLock("locked", "k", old), s3api.ObjectLockModeGovernance, until)
		if state := h.headLock("locked", "k", current); state.mode != nil || state.hold != nil {
			t.Errorf("HEAD of the current version = mode %v hold %v, want neither", deref(state.mode), deref(state.hold))
		}
		after := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "locked", Key: "k", VersionId: old})
		if deref(after.ETag) != deref(before.ETag) || !instant(after.LastModified).Equal(instant(before.LastModified)) {
			t.Errorf("the old version's ETag and Last-Modified went from %v %v to %v %v, want them kept",
				deref(before.ETag), deref(before.LastModified), deref(after.ETag), deref(after.LastModified))
		}
		if vs := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "locked"}); len(vs.Versions) != 2 || len(vs.DeleteMarkers) != 0 {
			t.Errorf("ListObjectVersions = %d versions and %d markers, want the 2 versions written", len(vs.Versions), len(vs.DeleteMarkers))
		}

		if err := h.putRetention("locked", "k", nil, s3api.ObjectLockRetentionModeCompliance, until, nil); err != nil {
			t.Fatalf("PutObjectRetention without a version id: %s %q", err.ID.QueryCode, err.Message)
		}
		wantRetention(t, "HEAD of the current version after a put without a version id", h.headLock("locked", "k", current), s3api.ObjectLockModeCompliance, until)
		wantRetention(t, "HEAD of the old version after a put without a version id", h.headLock("locked", "k", old), s3api.ObjectLockModeGovernance, until)
	})
}

// TestExpiredRetention: GetObjectRetention reads a retention after its date
// has passed, as HEAD does (s3-ssec-object-lock.md 6.7; LocalStack
// test_s3.snapshot.json L9623), but an expired retention no longer limits
// a change, so even COMPLIANCE can then be removed or turned into
// GOVERNANCE without bypass: protection lasts for the retention period
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html,
// "Retention periods").
func TestExpiredRetention(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		until := h.Clock.Now().Wire().Add(time.Hour)
		if err := h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeCompliance, until, nil); err != nil {
			t.Fatalf("set COMPLIANCE: %s %q", err.ID.QueryCode, err.Message)
		}
		h.advance(time.Hour)

		out := ok(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v})
		if r := out.Retention; r == nil || deref(r.Mode) != s3api.ObjectLockRetentionModeCompliance || !instant(r.RetainUntilDate).Equal(until) {
			t.Errorf("GetObjectRetention of an expired retention = %+v, want COMPLIANCE until %s", r, until)
		}
		later := h.Clock.Now().Wire().Add(time.Minute)
		if err := h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, later, nil); err != nil {
			t.Errorf("expired COMPLIANCE to GOVERNANCE without bypass: %s %q, want accepted", err.ID.QueryCode, err.Message)
		}
		h.advance(time.Minute)
		if err := h.putRetention("locked", "k", v, "", time.Time{}, nil); err != nil {
			t.Errorf("removal of an expired GOVERNANCE without bypass: %s %q, want accepted", err.ID.QueryCode, err.Message)
		}
		_, err := as(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v})
		errNoLockState.is(t, "GetObjectRetention after the removal", err)
	})
}

// TestLockOperationsSeeTheDefaultRetention: a version that got the bucket's
// default retention has a retention like any other (s3-ssec-object-lock.md
// 5.3, 6.7): GetObjectRetention reads it, and it is extended like an
// explicit one (LocalStack test_s3.snapshot.json L9871).
func TestLockOperationsSeeTheDefaultRetention(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		h.setDefault("locked", s3api.ObjectLockRetentionModeGovernance, new(int32(1)), nil)
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		want := h.Clock.Now().Wire().Add(24 * time.Hour)

		out := ok(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v})
		if r := out.Retention; r == nil || deref(r.Mode) != s3api.ObjectLockRetentionModeGovernance || !instant(r.RetainUntilDate).Equal(want) {
			t.Errorf("GetObjectRetention of a default retention = %+v, want GOVERNANCE until %s", r, want)
		}
		errProtected.is(t, "shorten the default retention without bypass", h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, want.Add(-time.Hour), nil))
		if err := h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, want.Add(time.Hour), nil); err != nil {
			t.Errorf("extend the default retention: %s %q, want accepted", err.ID.QueryCode, err.Message)
		}
	})
}

// TestHoldSurvivesRetentionChanges: a legal hold is independent of
// retention (s3-ssec-object-lock.md 6.5), so setting and removing a
// retention keeps a hold set before it.
func TestHoldSurvivesRetentionChanges(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		must := func(what string, err *awserr.Error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s: %s %q", what, err.ID.QueryCode, err.Message)
			}
		}
		must("PutObjectLegalHold ON", h.putHold("locked", "k", v, s3api.ObjectLockLegalHoldStatusOn))
		must("PutObjectRetention", h.putRetention("locked", "k", v, s3api.ObjectLockRetentionModeGovernance, h.Clock.Now().Wire().Add(time.Hour), nil))
		must("remove the retention", h.putRetention("locked", "k", v, "", time.Time{}, new(true)))
		hold := ok(h, who{}, s3api.OpGetObjectLegalHold, &s3api.GetObjectLegalHoldInput{Bucket: "locked", Key: "k", VersionId: v})
		if hold.LegalHold == nil || deref(hold.LegalHold.Status) != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("GetObjectLegalHold after the retention changes = %+v, want ON", hold.LegalHold)
		}
	})
}
