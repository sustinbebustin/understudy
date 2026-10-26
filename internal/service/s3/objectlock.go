package s3

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
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

// parse checks the headers that need no bucket, in the order of
// s3-ssec-object-lock.md 11.3: event holds refused with 501, then the
// mode and date pairing, the mode, the legal hold and the date against
// now (6.2). It returns the settings they ask for, the zero ObjectLock
// when none was sent. Not implemented yet: it answers
// errNotImplementedYet.
func (objectLockHeaders) parse(clock.Instant) (ObjectLock, *awserr.Error) {
	return ObjectLock{}, errNotImplementedYet("objectLockHeaders.parse")
}

// ObjectLock is one object version's Object Lock state
// (s3-ssec-object-lock.md 11.2): its retention and its legal hold, which
// are independent (6.5). It lives on Object, so a delete marker has none.
type ObjectLock struct {
	// Retention is nil when the version has no retention period, or had
	// it removed.
	Retention *Retention `json:"retention,omitempty"`
	// LegalHold is LegalHoldNever until a hold is first set (6.7).
	LegalHold LegalHold `json:"legalHold,omitzero"`
}

// withRetention applies a PutObjectRetention's change rules
// (s3-ssec-object-lock.md 6.4, 11.3) to l: next replaces the retention,
// nil removes it. A retention in force refuses removal, an earlier date
// or a change of mode unless it is GOVERNANCE and bypass is true;
// COMPLIANCE refuses them always. Not implemented yet: it answers
// errNotImplementedYet.
func (ObjectLock) withRetention(*Retention, clock.Instant, bool) (ObjectLock, *awserr.Error) {
	return ObjectLock{}, errNotImplementedYet("ObjectLock.withRetention")
}

// checkDelete refuses a versioned delete of a version under a legal hold,
// or under a retention in force that is COMPLIANCE, or GOVERNANCE without
// bypass, with 403 AccessDenied (s3-ssec-object-lock.md 7.1). Not
// implemented yet: it answers errNotImplementedYet.
func (ObjectLock) checkDelete(clock.Instant, bool) *awserr.Error {
	return errNotImplementedYet("ObjectLock.checkDelete")
}

// Retention is a retention period: its mode and the instant it ends on the
// emulator clock, truncated to the millisecond (switch
// s3.object-lock.retain-until-format).
type Retention struct {
	Mode        RetentionMode `json:"mode"`
	RetainUntil clock.Instant `json:"retainUntil"`
}

// inForce reports whether the retention still protects its version: now
// is before RetainUntil (switch s3.object-lock.retain-until-format). Not
// implemented yet: it reports false.
func (Retention) inForce(clock.Instant) bool { return false }

// RetentionMode is a retention's mode (s3-ssec-object-lock.md 6.6).
type RetentionMode uint8

// RetentionMode values.
const (
	// RetentionGovernance can be shortened or removed with bypass.
	RetentionGovernance RetentionMode = iota + 1
	// RetentionCompliance can be neither shortened nor removed before it
	// ends.
	RetentionCompliance
)

// MarshalText writes GOVERNANCE or COMPLIANCE. Not implemented yet: it
// fails.
func (RetentionMode) MarshalText() ([]byte, error) {
	return nil, &NotImplementedYetError{Func: "RetentionMode.MarshalText"}
}

// UnmarshalText reads GOVERNANCE or COMPLIANCE and refuses any other text.
// Not implemented yet: it fails.
func (*RetentionMode) UnmarshalText([]byte) error {
	return &NotImplementedYetError{Func: "RetentionMode.UnmarshalText"}
}

// LegalHold is a version's legal hold (s3-ssec-object-lock.md 6.5): three
// states, since HEAD omits the header only for a version that never had
// one (6.7).
type LegalHold uint8

// LegalHold values.
const (
	// LegalHoldNever is a version no hold was ever set on.
	LegalHoldNever LegalHold = iota
	// LegalHoldOn blocks a versioned delete whatever the retention.
	LegalHoldOn
	// LegalHoldOff is a hold released, or set OFF.
	LegalHoldOff
)

// MarshalText writes ON or OFF; LegalHoldNever is never written (the field
// is omitzero). Not implemented yet: it fails.
func (LegalHold) MarshalText() ([]byte, error) {
	return nil, &NotImplementedYetError{Func: "LegalHold.MarshalText"}
}

// UnmarshalText reads ON or OFF and refuses any other text. Not
// implemented yet: it fails.
func (*LegalHold) UnmarshalText([]byte) error {
	return &NotImplementedYetError{Func: "LegalHold.UnmarshalText"}
}

// checkLockTarget refuses explicit lock settings on a bucket without
// Object Lock with 400 InvalidRequest (switch
// s3.object-lock.headers-without-lock). Not implemented yet: it answers
// errNotImplementedYet.
func checkLockTarget(*Bucket, ObjectLock) *awserr.Error {
	return errNotImplementedYet("checkLockTarget")
}

// defaultRetention is a bucket's default retention rule: its mode, and its
// Days or Years as a duration, a year being 365 days (switch
// s3.object-lock.default-years).
type defaultRetention struct {
	mode   RetentionMode
	period time.Duration
}

// bucketDefaultRetention is cfg's default retention rule; false when cfg
// has none. Not implemented yet: it reports none.
func bucketDefaultRetention(*s3api.ObjectLockConfiguration) (defaultRetention, bool) {
	return defaultRetention{}, false
}

// from is the retention the rule gives a version created at created
// (s3-ssec-object-lock.md 5.3). Not implemented yet: it returns the zero
// Retention.
func (defaultRetention) from(clock.Instant) Retention { return Retention{} }

// lockOnWrite is a new version's lock state: the write's explicit
// settings, with the bucket default's retention when they name none
// (s3-ssec-object-lock.md 5.3). Not implemented yet: it returns the zero
// ObjectLock.
func lockOnWrite(*s3api.ObjectLockConfiguration, ObjectLock, clock.Instant) ObjectLock {
	return ObjectLock{}
}

// needsIntegrity reports whether a PutObject's new version gets a
// retention period, explicit or the bucket default's, and so needs
// Content-MD5 or a checksum (s3-ssec-object-lock.md 6.3). Not implemented
// yet: it reports false.
func needsIntegrity(*s3api.ObjectLockConfiguration, ObjectLock) bool { return false }

// parseRetentionBody reads a PutObjectRetention body
// (s3-ssec-object-lock.md 6.4): nil for an empty Retention, which removes
// the retention; MalformedXML for no body or a mode without a date or a
// date without a mode; 501 for event holds. Not implemented yet: it
// answers errNotImplementedYet.
func parseRetentionBody(*s3api.ObjectLockRetention) (*Retention, *awserr.Error) {
	return nil, errNotImplementedYet("parseRetentionBody")
}

// checkRetainUntil refuses a retain-until date that is not in the future,
// or more than 100 years ahead, with InvalidArgument naming the header or
// element (s3-ssec-object-lock.md 6.4, switch
// s3.object-lock.header-texts). Not implemented yet: it answers
// errNotImplementedYet.
func checkRetainUntil(clock.Instant, clock.Instant, string) *awserr.Error {
	return errNotImplementedYet("checkRetainUntil")
}

// parseLegalHoldBody reads a PutObjectLegalHold body
// (s3-ssec-object-lock.md 6.5): MalformedXML for no body, no Status or a
// Status other than ON or OFF. Not implemented yet: it answers
// errNotImplementedYet.
func parseLegalHoldBody(*s3api.ObjectLockLegalHold) (LegalHold, *awserr.Error) {
	return LegalHoldNever, errNotImplementedYet("parseLegalHoldBody")
}

// javaDate renders t as Java's Date.toString() in US Pacific time, as S3
// writes a refused date's ArgumentValue: "Tue Dec 31 16:00:00 PST 2019"
// for 2020-01-01T00:00:00Z (s3-ssec-object-lock.md 6.4). Not implemented
// yet: it returns "".
func javaDate(time.Time) string { return "" }

// checkBypassHeader refuses x-amz-bypass-governance-retention, whatever its
// value, on a bucket without Object Lock (s3-ssec-object-lock.md 7.3,
// switch s3.object-lock.bypass-without-lock). Not implemented yet: it
// answers errNotImplementedYet.
func checkBypassHeader(*Bucket, *bool) *awserr.Error {
	return errNotImplementedYet("checkBypassHeader")
}

// governanceBypass reports whether a request lifts GOVERNANCE retention on
// the object version object names (s3-ssec-object-lock.md 6.6). It is the
// seam IAM enforcement plugs into: under iam.enforce off, the only mode
// this build has, x-amz-bypass-governance-retention: true alone bypasses
// and s3:BypassGovernanceRetention is not evaluated (the coverage notes).
// Callers run it before kernel.Write, holding no lock. Not implemented
// yet: it reports false.
func (*Service) governanceBypass(*kernel.Call, *bool, aws.ARN) bool { return false }
