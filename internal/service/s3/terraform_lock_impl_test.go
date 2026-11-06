package s3_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// TestTerraformLockedObjectRoundTrips follows aws_s3_object's calls for an
// object with lock arguments in a bucket with a GOVERNANCE default
// (s3-ssec-object-lock.md 9.2, 9.3, 11.6). The upload carries the retention,
// the legal hold OFF and the CRC32 the provider adds for the checksum rule
// (6.3). The refresh's HEAD dates re-render under time.RFC3339 to the
// configured whole-second date, so the second plan is empty. Moving the date
// later needs no bypass (6.4). force_destroy deletes the version with the
// bypass header, which lifts GOVERNANCE (6.6).
func TestTerraformLockedObjectRoundTrips(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		gov, off := s3api.ObjectLockModeGovernance, s3api.ObjectLockLegalHoldStatusOff
		h.lockBucket("locked")
		h.setDefault("locked", s3api.ObjectLockRetentionModeGovernance, new(int32(1)), nil)
		configured := h.Clock.Now().Wire().UTC().Truncate(time.Second).AddDate(0, 0, 30).Format(time.RFC3339)
		until, perr := time.Parse(time.RFC3339, configured)
		if perr != nil {
			t.Fatal(perr)
		}
		refresh := func(what, want string) {
			t.Helper()
			state := h.headLock("locked", "locked.txt", nil)
			var got string
			if state.until != nil {
				got = state.until.Format(time.RFC3339)
			}
			if deref(state.mode) != gov || got != want || deref(state.hold) != off {
				t.Errorf("%s: HEAD = mode %v until %q hold %v; want %s until %q hold %s", what, deref(state.mode), got, deref(state.hold), gov, want, off)
			}
		}

		put := headerFor(checksum.CRC32, sumOf(h, checksum.CRC32, []byte("locked")))
		put.Bucket, put.Key, put.Body = "locked", "locked.txt", bytesBody("locked")
		put.ObjectLockMode, put.ObjectLockRetainUntilDate, put.ObjectLockLegalHoldStatus = &gov, &until, &off
		out, err := as(h, who{}, s3api.OpPutObject, &put)
		if err != nil {
			t.Fatalf("PutObject with lock arguments and a CRC32: %s %q", err.ID.QueryCode, err.Message)
		}
		refresh("refresh after create", configured)

		later := until.AddDate(1, 0, 0)
		if err := h.putRetention("locked", "locked.txt", nil, s3api.ObjectLockRetentionModeGovernance, later, nil); err != nil {
			t.Fatalf("PutObjectRetention to a later date without bypass: %s %q", err.ID.QueryCode, err.Message)
		}
		refresh("refresh after update", later.Format(time.RFC3339))

		if _, err := h.deleteVersion("locked", "locked.txt", out.VersionId, new(true)); err != nil {
			t.Fatalf("force_destroy's DeleteObject with bypass: %s %q", err.ID.QueryCode, err.Message)
		}
		if left := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "locked"}); len(left.Versions)+len(left.DeleteMarkers) != 0 {
			t.Errorf("after force_destroy: %d versions and %d markers left, want none", len(left.Versions), len(left.DeleteMarkers))
		}
	})
}

// TestTerraformObjectDestroyLiftsAHold follows aws_s3_object's force_destroy
// of a version under a legal hold and GOVERNANCE (s3-ssec-object-lock.md 9.6,
// deleteAllObjectVersions). The bypass delete gets AccessDenied, the code the
// provider matches. The provider then HEADs the version, puts the hold OFF
// and deletes again with bypass, which succeeds. Unlike the bucket's
// emptying, whose retry drops the bypass, this destroy succeeds the first
// time.
func TestTerraformObjectDestroyLiftsAHold(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		l := retention(s3api.ObjectLockModeGovernance, h.Clock.Now().Wire().Add(time.Hour))
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		v := h.mustPutLocked("locked", "k", "v", l)

		_, err := h.deleteVersion("locked", "k", v, new(true))
		errProtected.is(t, "the bypass delete of a held version", err)
		if state := h.headLock("locked", "k", v); deref(state.hold) != s3api.ObjectLockLegalHoldStatusOn {
			t.Fatalf("HEAD legal hold = %v, want ON", deref(state.hold))
		}
		if err := h.putHold("locked", "k", v, s3api.ObjectLockLegalHoldStatusOff); err != nil {
			t.Fatalf("PutObjectLegalHold OFF: %s %q", err.ID.QueryCode, err.Message)
		}
		if _, err := h.deleteVersion("locked", "k", v, new(true)); err != nil {
			t.Errorf("the bypass delete after the hold was lifted: %s %q, want the version deleted", err.ID.QueryCode, err.Message)
		}
		if left := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "locked"}); len(left.Versions)+len(left.DeleteMarkers) != 0 {
			t.Errorf("after force_destroy: %d versions and %d markers left, want none", len(left.Versions), len(left.DeleteMarkers))
		}
	})
}
