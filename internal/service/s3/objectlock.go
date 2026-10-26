package s3

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// objectLockNote is the coverage note of the writes that refuse the
// headers (refuse).
const objectLockNote = "Object Lock retention and legal-hold headers answer 501"

// objectLockHeaders are the per-object Object Lock members of PutObject,
// CopyObject and CreateMultipartUpload, the only writes that carry them
// (s3-ssec-object-lock.md 6.1).
type objectLockHeaders struct {
	mode           *s3api.ObjectLockMode
	retainUntil    *time.Time
	legalHold      *s3api.ObjectLockLegalHoldStatus
	eventHold      *s3api.ObjectLockEventHold
	eventHoldDays  *int32
	eventHoldYears *int32
}

// refuse answers a write that sets any of the headers with 501
// NotImplemented naming the first one set, in the style of the directory
// bucket headers (conditional.go): understudy does not implement retention
// or legal holds on objects, and writing the object without them would
// leave it unprotected with no sign (s3-ssec-object-lock.md 11.7). It runs
// before the bucket is read, so every bucket refuses them, where AWS
// refuses them on a bucket without Object Lock and applies them on one
// with it (6.2, 6.3).
func (h objectLockHeaders) refuse() *awserr.Error {
	for _, m := range []struct {
		header string
		set    bool
	}{
		{"x-amz-object-lock-mode", h.mode != nil},
		{"x-amz-object-lock-retain-until-date", h.retainUntil != nil},
		{"x-amz-object-lock-legal-hold", h.legalHold != nil},
		{"x-amz-object-lock-event-hold", h.eventHold != nil},
		{"x-amz-object-lock-event-hold-duration-days", h.eventHoldDays != nil},
		{"x-amz-object-lock-event-hold-duration-years", h.eventHoldYears != nil},
	} {
		if m.set {
			return withField(s3api.ErrNotImplemented("understudy does not implement Object Lock retention or legal holds on objects, so it refuses "+
				m.header+"; nothing was written. Send the request without x-amz-object-lock-* headers. Run 'understudy coverage --service s3' for what is supported."),
				"Header", m.header)
		}
	}
	return nil
}
