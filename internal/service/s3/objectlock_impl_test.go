package s3_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// TestLockTargetBeforeTheChecksumRule: a PutObject with a retention and no
// checksum on a bucket without Object Lock is refused for the bucket, which
// s3-ssec-object-lock.md 11.3 checks before the checksum rule, and nothing
// is written.
func TestLockTargetBeforeTheChecksumRule(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		until := h.Clock.Now().Wire().Add(time.Hour)
		_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "plain", Key: "k", Body: bytesBody("v"),
			ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until,
		})
		code, status := h.switchCode("s3.object-lock.headers-without-lock")
		failure{status: status, code: code, message: "Bucket is missing Object Lock Configuration"}.is(t, "retention without a checksum on a plain bucket", err)
		nothingWritten(t, h, "plain", "k")
	})
}

// TestEventHoldRefusedBeforeOtherChecks: understudy's 501 for an event
// hold comes before any check that would judge the rest of the request
// (s3-ssec-object-lock.md 11.3, 11.7): a default retention of a mode and
// DefaultEventHold alone, which variable retention may allow (6.9), is not
// called MalformedXML, and an event-hold value other than ON or OFF is not
// refused by the model's enum check. Nothing is changed.
func TestEventHoldRefusedBeforeOtherChecks(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.createBucket("plain")
		h.put("plain", "src", "source")
		h.lockBucket("locked")
		gov := s3api.ObjectLockRetentionModeGovernance
		_, err := as(h, who{}, s3api.OpPutObjectLockConfiguration, &s3api.PutObjectLockConfigurationInput{Bucket: "locked", ObjectLockConfiguration: &s3api.ObjectLockConfiguration{
			ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled),
			Rule:              &s3api.ObjectLockRule{DefaultRetention: &s3api.DefaultRetention{Mode: &gov, DefaultEventHold: &s3api.EventHoldDuration{Days: new(int32(3))}}},
		}})
		errEventHold("DefaultEventHold").is(t, "PutObjectLockConfiguration of a mode and DefaultEventHold without a period", err)
		cfg := ok(h, who{}, s3api.OpGetObjectLockConfiguration, &s3api.GetObjectLockConfigurationInput{Bucket: "locked"})
		if cfg.ObjectLockConfiguration == nil || cfg.ObjectLockConfiguration.Rule != nil {
			t.Errorf("the refused configuration left a rule: %+v", cfg.ObjectLockConfiguration)
		}

		bad := s3api.ObjectLockEventHold("BAD")
		for op, err := range lockWrites(h, "locked", "bad-hold", s3api.PutObjectInput{ObjectLockEventHold: &bad}) {
			errEventHold("x-amz-object-lock-event-hold").is(t, op+" with x-amz-object-lock-event-hold: BAD", err)
		}
		nothingWritten(t, h, "locked", "bad-hold")

		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{})
		_, err = as(h, who{}, s3api.OpPutObjectRetention, &s3api.PutObjectRetentionInput{Bucket: "locked", Key: "k", VersionId: v, Retention: &s3api.ObjectLockRetention{EventHold: &bad}})
		errEventHold("EventHold").is(t, "PutObjectRetention with EventHold BAD", err)
		wantNoRetention(t, "after the refused retention", h.headLock("locked", "k", v))
	})
}

// TestLegalHoldOffIsReported: a version written with the legal hold OFF has
// had one applied, so GET and HEAD report OFF ("not returned if the
// specified version of this object has never had a legal hold applied",
// https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html;
// s3-ssec-object-lock.md 6.7).
func TestLegalHoldOffIsReported(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		v := h.mustPutLocked("locked", "k", "v", s3api.PutObjectInput{ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOff)})
		if state := h.headLock("locked", "k", v); deref(state.hold) != s3api.ObjectLockLegalHoldStatusOff || state.mode != nil {
			t.Errorf("HEAD = hold %v mode %v, want OFF and no retention", deref(state.hold), deref(state.mode))
		}
		_, get := h.read(&s3api.GetObjectInput{Bucket: "locked", Key: "k", VersionId: v})
		if deref(get.ObjectLockLegalHoldStatus) != s3api.ObjectLockLegalHoldStatusOff {
			t.Errorf("GET legal hold = %v, want OFF", deref(get.ObjectLockLegalHoldStatus))
		}
	})
}

// TestUploadLockSurvivesASnapshot: the lock settings CreateMultipartUpload
// took are upload state a snapshot keeps (ARCHITECTURE.md 13), so an upload
// completed after a reset and a load still gives the object Create's
// retention and hold (s3-ssec-object-lock.md 11.7: applied, never dropped).
func TestUploadLockSurvivesASnapshot(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		until := h.Clock.Now().Wire().Add(48*time.Hour + 42*time.Millisecond)
		id := h.startUpload("locked", "mpu", s3api.CreateMultipartUploadInput{
			ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until,
			ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn),
		})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			t.Fatalf("Import: %v", err)
		}
		part := h.sendPart("locked", "mpu", id, 1, []byte("only part"), withMD5("only part"))
		done, cerr := complete(h, "locked", "mpu", id, []s3api.CompletedPart{listed(1, part)}, s3api.CompleteMultipartUploadInput{})
		if cerr != nil {
			t.Fatalf("Complete after a snapshot round trip: %s %q", cerr.ID.QueryCode, cerr.Message)
		}
		state := h.headLock("locked", "mpu", done.VersionId)
		wantRetention(t, "the completed object", state, s3api.ObjectLockModeGovernance, until.Truncate(time.Millisecond))
		if deref(state.hold) != s3api.ObjectLockLegalHoldStatusOn {
			t.Errorf("the completed object's legal hold = %v, want ON", deref(state.hold))
		}
	})
}
