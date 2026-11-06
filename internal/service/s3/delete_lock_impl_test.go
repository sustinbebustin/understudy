package s3_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// TestDefaultRetentionHoldsDeletesOnTheEmulatorClock: a version that took
// its bucket's default retention is protected as one with its own: a
// versioned delete is refused until the emulator clock reaches the date the
// rule gave it, creation plus the period (s3-ssec-object-lock.md 5.3), and
// then succeeds without bypass (7.1; LocalStack test_s3.snapshot.json
// L17904). Bypass lifts a GOVERNANCE default but not a COMPLIANCE one
// (6.6; L9649, L17874).
func TestDefaultRetentionHoldsDeletesOnTheEmulatorClock(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		versions := map[string]*string{}
		var until time.Time
		for bucket, mode := range map[string]s3api.ObjectLockRetentionMode{
			"gov":    s3api.ObjectLockRetentionModeGovernance,
			"comp":   s3api.ObjectLockRetentionModeCompliance,
			"bypass": s3api.ObjectLockRetentionModeGovernance,
		} {
			h.lockBucket(bucket)
			h.setDefault(bucket, mode, new(int32(1)), nil)
			versions[bucket] = h.mustPutLocked(bucket, "k", "v", s3api.PutObjectInput{})
			state := h.headLock(bucket, "k", versions[bucket])
			if state.until == nil {
				t.Fatalf("version in %s reports no retention, want the bucket default's", bucket)
			}
			if state.until.After(until) {
				until = *state.until
			}
		}

		_, err := h.deleteVersion("gov", "k", versions["gov"], nil)
		errProtected.is(t, "versioned delete under a default GOVERNANCE retention", err)
		_, err = h.deleteVersion("comp", "k", versions["comp"], new(true))
		errProtected.is(t, "versioned delete under a default COMPLIANCE retention with bypass", err)
		if _, err := h.deleteVersion("bypass", "k", versions["bypass"], new(true)); err != nil {
			t.Errorf("versioned delete under a default GOVERNANCE retention with bypass: %s %q, want 204", err.ID.QueryCode, err.Message)
		}

		h.advance(until.Sub(h.Clock.Now().Wire()))
		for _, bucket := range []string{"gov", "comp"} {
			if _, err := h.deleteVersion(bucket, "k", versions[bucket], nil); err != nil {
				t.Errorf("versioned delete in %s once the clock reached the default's date: %s %q, want 204", bucket, err.ID.QueryCode, err.Message)
			}
		}
	})
}

// TestDeleteObjectsUnderComplianceAndExpiry: in DeleteObjects, bypass lifts
// GOVERNANCE but not COMPLIANCE, which stays a per-key AccessDenied
// (s3-ssec-object-lock.md 6.6, 7.2; LocalStack test_s3.snapshot.json
// L17874, L10054); once the emulator clock reaches the date, the batch
// deletes the version without bypass (7.1; L17904).
func TestDeleteObjectsUnderComplianceAndExpiry(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(time.Hour)
		comp := h.mustPutLocked("locked", "comp", "v", retention(s3api.ObjectLockModeCompliance, until))
		gov := h.mustPutLocked("locked", "gov", "v", retention(s3api.ObjectLockModeGovernance, until))
		batch := func(bypass *bool, objects ...s3api.ObjectIdentifier) *s3api.DeleteObjectsOutput {
			return ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "locked", BypassGovernanceRetention: bypass, Delete: s3api.Delete{Objects: objects}})
		}

		out := batch(new(true), s3api.ObjectIdentifier{Key: "comp", VersionId: comp}, s3api.ObjectIdentifier{Key: "gov", VersionId: gov})
		if len(out.Errors) != 1 || str(out.Errors[0].Key) != "comp" || str(out.Errors[0].VersionId) != *comp ||
			str(out.Errors[0].Code) != "AccessDenied" || str(out.Errors[0].Message) != errProtected.message {
			t.Errorf("batch with bypass = errors %+v, want AccessDenied %q for comp/%s only", out.Errors, errProtected.message, *comp)
		}
		if len(out.Deleted) != 1 || str(out.Deleted[0].Key) != "gov" || str(out.Deleted[0].VersionId) != *gov {
			t.Errorf("batch with bypass deleted %+v, want gov/%s", out.Deleted, *gov)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "locked", Key: "comp", VersionId: comp}); got != "v" {
			t.Errorf("COMPLIANCE version after the refused entry = %q, want it kept", got)
		}

		h.advance(time.Hour)
		out = batch(nil, s3api.ObjectIdentifier{Key: "comp", VersionId: comp})
		if len(out.Errors) != 0 || len(out.Deleted) != 1 || str(out.Deleted[0].VersionId) != *comp {
			t.Errorf("batch at the COMPLIANCE date = errors %+v deleted %+v, want comp/%s deleted", out.Errors, out.Deleted, *comp)
		}
	})
}

// TestExpiredRetentionDoesNotLiftAHold: a legal hold outlasts the retention
// beside it, so a versioned delete stays refused, bypass or not, until the
// hold is released ("an expired retention does not lift a hold",
// s3-ssec-object-lock.md 6.5), and the refused delete keeps the version.
func TestExpiredRetentionDoesNotLiftAHold(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		l := retention(s3api.ObjectLockModeGovernance, h.Clock.Now().Wire().Add(time.Minute))
		l.ObjectLockLegalHoldStatus = new(s3api.ObjectLockLegalHoldStatusOn)
		v := h.mustPutLocked("locked", "k", "v", l)
		h.advance(2 * time.Minute)

		for _, bypass := range []*bool{nil, new(true)} {
			_, err := h.deleteVersion("locked", "k", v, bypass)
			errProtected.is(t, "versioned delete under a hold after its retention expired", err)
		}
		if got, _ := h.read(&s3api.GetObjectInput{Bucket: "locked", Key: "k", VersionId: v}); got != "v" {
			t.Fatalf("held version after the refused deletes = %q, want it kept", got)
		}
		if err := h.putHold("locked", "k", v, s3api.ObjectLockLegalHoldStatusOff); err != nil {
			t.Fatalf("PutObjectLegalHold OFF: %s %q", err.ID.QueryCode, err.Message)
		}
		if _, err := h.deleteVersion("locked", "k", v, nil); err != nil {
			t.Errorf("versioned delete after the hold was released: %s %q, want 204", err.ID.QueryCode, err.Message)
		}
	})
}

// TestImportedLockStateIsEnforcedAndResetClearsIt: lock state a snapshot
// restores still refuses a versioned delete (s3-ssec-object-lock.md 11.2),
// so a COMPLIANCE version keeps its bucket from being deleted
// (BucketNotEmpty, s3.md 3), and reset, which applies no lock, clears both
// (spec risk: reset clears retention): the name is free for a new, empty
// bucket.
func TestImportedLockStateIsEnforcedAndResetClearsIt(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", retention(s3api.ObjectLockModeCompliance, h.Clock.Now().Wire().Add(24*time.Hour)))
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import: %v", err)
		}
		_, aerr := h.deleteVersion("locked", "k", v, new(true))
		errProtected.is(t, "versioned delete of an imported COMPLIANCE version", aerr)
		_, aerr = as(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "locked"})
		wantCode(t, aerr, "BucketNotEmpty", http.StatusConflict)

		h.svc.Reset(kernel.ResetScope{})
		h.lockBucket("locked")
		if out := ok(h, who{}, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "locked"}); len(out.Versions) != 0 || len(out.DeleteMarkers) != 0 {
			t.Errorf("bucket recreated after reset lists %d versions and %d markers, want none", len(out.Versions), len(out.DeleteMarkers))
		}
		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "locked"})
	})
}
