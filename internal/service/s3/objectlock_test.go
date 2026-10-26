package s3_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// lockHeaders are the per-object Object Lock members PutObject, CopyObject
// and CreateMultipartUpload share (s3-ssec-object-lock.md 6.1).
type lockHeaders struct {
	mode                          *s3api.ObjectLockMode
	until                         *time.Time
	hold                          *s3api.ObjectLockLegalHoldStatus
	eventHold                     *s3api.ObjectLockEventHold
	eventHoldDays, eventHoldYears *int32
}

// understudy does not implement retention or legal holds on objects, so a
// write that sets any of their headers is 501 NotImplemented naming the
// first one, on a bucket with Object Lock or without, and writes nothing.
// Storing the object without the protection it asked for would be a
// silent fake (s3-ssec-object-lock.md 11.7). AWS refuses the headers on a
// bucket without Object Lock and applies them on one with it (6.2, 6.3).
func TestObjectLockHeadersAreNotImplemented(t *testing.T) {
	until := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		set    lockHeaders
		header string
	}{
		{"retention", lockHeaders{mode: new(s3api.ObjectLockModeGovernance), until: &until}, "x-amz-object-lock-mode"},
		{"date alone", lockHeaders{until: &until}, "x-amz-object-lock-retain-until-date"},
		{"legal hold", lockHeaders{hold: new(s3api.ObjectLockLegalHoldStatusOn)}, "x-amz-object-lock-legal-hold"},
		{"legal hold off", lockHeaders{hold: new(s3api.ObjectLockLegalHoldStatusOff)}, "x-amz-object-lock-legal-hold"},
		{"event hold", lockHeaders{eventHold: new(s3api.ObjectLockEventHoldOn)}, "x-amz-object-lock-event-hold"},
		{"event hold days", lockHeaders{eventHoldDays: new(int32(1))}, "x-amz-object-lock-event-hold-duration-days"},
		{"event hold years", lockHeaders{eventHoldYears: new(int32(1))}, "x-amz-object-lock-event-hold-duration-years"},
	}
	runObjects(t, func(h *harness) {
		h.createBucket("plain")
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "locked", ObjectLockEnabledForBucket: new(true)})
		h.put("plain", "src", "source")
		writes := []struct {
			op    string
			write func(bucket string, l lockHeaders) *awserr.Error
		}{
			{"PutObject", func(bucket string, l lockHeaders) *awserr.Error {
				_, err := as(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
					Bucket: bucket, Key: "refused", Body: &body{r: unreadable{t: h.T}, n: 1},
					ObjectLockMode: l.mode, ObjectLockRetainUntilDate: l.until, ObjectLockLegalHoldStatus: l.hold,
					ObjectLockEventHold: l.eventHold, ObjectLockEventHoldDurationDays: l.eventHoldDays, ObjectLockEventHoldDurationYears: l.eventHoldYears,
				})
				return err
			}},
			{"CopyObject", func(bucket string, l lockHeaders) *awserr.Error {
				_, err := as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{
					Bucket: bucket, Key: "refused", CopySource: "plain/src",
					ObjectLockMode: l.mode, ObjectLockRetainUntilDate: l.until, ObjectLockLegalHoldStatus: l.hold,
					ObjectLockEventHold: l.eventHold, ObjectLockEventHoldDurationDays: l.eventHoldDays, ObjectLockEventHoldDurationYears: l.eventHoldYears,
				})
				return err
			}},
			{"CreateMultipartUpload", func(bucket string, l lockHeaders) *awserr.Error {
				_, err := as(h, who{}, s3api.OpCreateMultipartUpload, &s3api.CreateMultipartUploadInput{
					Bucket: bucket, Key: "refused",
					ObjectLockMode: l.mode, ObjectLockRetainUntilDate: l.until, ObjectLockLegalHoldStatus: l.hold,
					ObjectLockEventHold: l.eventHold, ObjectLockEventHoldDurationDays: l.eventHoldDays, ObjectLockEventHoldDurationYears: l.eventHoldYears,
				})
				return err
			}},
		}
		for _, bucket := range []string{"plain", "locked"} {
			for _, w := range writes {
				for _, c := range cases {
					err := w.write(bucket, c.set)
					if err == nil || err.ID.QueryCode != "NotImplemented" || err.ID.Status != 501 {
						t.Errorf("%s %s on %s: got %v, want 501 NotImplemented", w.op, c.name, bucket, err)
						continue
					}
					wantFields(t, err, map[string]string{"Header": c.header})
					if !strings.HasPrefix(err.Message, "understudy does not implement Object Lock retention or legal holds on objects") || !strings.Contains(err.Message, c.header) {
						t.Errorf("%s %s on %s: message %q does not name understudy and %s", w.op, c.name, bucket, err.Message, c.header)
					}
				}
			}
			if _, err := as(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: bucket, Key: "refused"}); err == nil {
				t.Errorf("a refused write left an object in %s", bucket)
			}
			if ups := ok(h, who{}, s3api.OpListMultipartUploads, &s3api.ListMultipartUploadsInput{Bucket: bucket}); len(ups.Uploads) != 0 {
				t.Errorf("a refused CreateMultipartUpload left %d uploads in %s", len(ups.Uploads), bucket)
			}
		}
		for _, op := range []*registry.Operation{s3api.OpPutObject.Descriptor(), s3api.OpCopyObject.Descriptor(), s3api.OpCreateMultipartUpload.Descriptor()} {
			bound, isBound := h.Kernel.Bindings().Status(op).(kernel.Bound)
			p, partial := bound.Coverage.(registry.Partial)
			if !isBound || !partial || !strings.Contains(p.Note, "Object Lock retention and legal-hold headers answer 501") {
				t.Errorf("%s: coverage %#v does not say the Object Lock headers answer 501", op.ID.Name, bound.Coverage)
			}
		}
	})
}
