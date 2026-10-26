package s3

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The per-object Object Lock request headers (s3-ssec-object-lock.md 6.1),
// as refusals name them.
const (
	headerLockMode        = "x-amz-object-lock-mode"
	headerLockRetainUntil = "x-amz-object-lock-retain-until-date"
	headerLockLegalHold   = "x-amz-object-lock-legal-hold"
)

// eventHoldNote ends the coverage note of the writes that carry lock
// headers: they apply retention and legal holds, and refuse event holds
// (errEventHold).
const eventHoldNote = "Object Lock event-hold headers are refused with NotImplemented"

// retentionLimit is the longest retention S3 accepts, 100 years counted
// as 36,500 days, the day bound of a retention period
// (s3-ssec-object-lock.md 5.2, 11.5).
const retentionLimit = 36500 * dayLength

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

// parse checks the headers that need no bucket, in the order of
// s3-ssec-object-lock.md 11.3: event holds refused with 501 (11.7), then
// the mode and date pairing, checked before the mode's value (6.2), the
// mode, the legal hold, and the date against now (switch
// s3.object-lock.header-texts). It returns the settings they ask for, the
// zero ObjectLock when none was sent; the date keeps its milliseconds
// (switch s3.object-lock.retain-until-format).
func (h objectLockHeaders) parse(now clock.Instant) (ObjectLock, *awserr.Error) {
	for _, e := range []struct {
		header string
		set    bool
	}{
		{"x-amz-object-lock-event-hold", h.eventHold != nil},
		{"x-amz-object-lock-event-hold-duration-days", h.eventHoldDays != nil},
		{"x-amz-object-lock-event-hold-duration-years", h.eventHoldYears != nil},
	} {
		if e.set {
			return ObjectLock{}, errEventHold(e.header)
		}
	}
	switch {
	case h.mode != nil && h.retainUntil == nil:
		return ObjectLock{}, errLockPairing(headerLockRetainUntil)
	case h.mode == nil && h.retainUntil != nil:
		return ObjectLock{}, errLockPairing(headerLockMode)
	}
	var mode RetentionMode
	if h.mode != nil {
		m, ok := retentionModeTexts().value(string(*h.mode))
		if !ok {
			return ObjectLock{}, errInvalidArgument("Unknown wormMode directive.", headerLockMode, string(*h.mode))
		}
		mode = m
	}
	var l ObjectLock
	if h.legalHold != nil {
		hold, ok := legalHoldTexts().value(string(*h.legalHold))
		if !ok {
			return ObjectLock{}, errInvalidArgument("Invalid value for the "+headerLockLegalHold+" header.", headerLockLegalHold, string(*h.legalHold))
		}
		l.LegalHold = hold
	}
	if h.retainUntil != nil {
		if err := checkRetainUntil(clock.FromWire(*h.retainUntil), now, headerLockRetainUntil); err != nil {
			return ObjectLock{}, err
		}
		l.Retention = &Retention{Mode: mode, RetainUntil: clock.FromWire(h.retainUntil.Truncate(time.Millisecond))}
	}
	return l, nil
}

// errLockPairing answers a mode without a date, or a date without a mode,
// naming the header that is missing (LocalStack test_s3.snapshot.json
// L9754, L17927; s3-ssec-object-lock.md 6.2).
func errLockPairing(missing string) *awserr.Error {
	return withField(s3api.ErrInvalidArgument(headerLockRetainUntil+" and "+headerLockMode+" must both be supplied"), "ArgumentName", missing)
}

// errEventHold answers an event-hold header or element, which understudy
// does not implement (s3-ssec-object-lock.md 6.9, 11.3, 11.7): 501
// NotImplemented naming it, in the style of the directory-bucket headers
// (conditional.go). Nothing is stored.
func errEventHold(name string) *awserr.Error {
	return withField(s3api.ErrNotImplemented("understudy does not implement Object Lock event holds (variable retention), so it refuses "+name+
		"; nothing was changed. Send the request without it. Run 'understudy coverage --service s3' for what is supported."), "Header", name)
}

// lockRequest is what a write asks of Object Lock, read before its bucket:
// the explicit settings of its headers, and whether it left its bytes'
// integrity undeclared.
type lockRequest struct {
	explicit ObjectLock
	// undeclared is set for a PutObject that sent neither Content-MD5 nor a
	// checksum. CopyObject and CreateMultipartUpload never set it: neither
	// needs one (switches s3.object-lock.copy-default and
	// s3.object-lock.checksum-scope).
	undeclared bool
}

// check judges the request against its bucket (s3-ssec-object-lock.md
// 11.3): explicit settings need a bucket with Object Lock, and a PutObject
// whose version gets a retention, its own or the bucket default's, needs
// Content-MD5 or a checksum (testdata/parity/s3/R12/11-put-without-checksum.json).
func (r lockRequest) check(b *Bucket) *awserr.Error {
	if err := checkLockTarget(b, r.explicit); err != nil {
		return err
	}
	if r.undeclared && needsIntegrity(b.ObjectLock, r.explicit) {
		return errInvalidRequest("Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters")
	}
	return nil
}

// lockHeaders are a version's lock headers on GET and HEAD
// (s3-ssec-object-lock.md 6.7): the mode and date whenever it has a
// retention, expired or not, and the legal hold once one was ever set.
func lockHeaders(l ObjectLock) (mode *s3api.ObjectLockMode, until *time.Time, hold *s3api.ObjectLockLegalHoldStatus) {
	if r := l.Retention; r != nil {
		mode = new(s3api.ObjectLockMode(retentionModeTexts()[r.Mode]))
		until = new(r.RetainUntil.Wire())
	}
	if text, ok := legalHoldTexts()[l.LegalHold]; ok {
		hold = new(s3api.ObjectLockLegalHoldStatus(text))
	}
	return mode, until, hold
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

// RetentionMode is a retention's mode (s3-ssec-object-lock.md 6.6). The
// zero value is no mode and has no text.
type RetentionMode uint8

// RetentionMode values.
const (
	// RetentionGovernance can be shortened or removed with bypass.
	RetentionGovernance RetentionMode = iota + 1
	// RetentionCompliance can be neither shortened nor removed before it
	// ends.
	RetentionCompliance
)

// retentionModeTexts are the model's ObjectLockRetentionMode values
// (s3-ssec-object-lock.md 8).
func retentionModeTexts() textTable[RetentionMode] {
	return textTable[RetentionMode]{
		RetentionGovernance: string(s3api.ObjectLockRetentionModeGovernance),
		RetentionCompliance: string(s3api.ObjectLockRetentionModeCompliance),
	}
}

// MarshalText writes GOVERNANCE or COMPLIANCE.
func (m RetentionMode) MarshalText() ([]byte, error) {
	return retentionModeTexts().marshal("RetentionMode", m)
}

// UnmarshalText reads GOVERNANCE or COMPLIANCE and refuses any other text.
func (m *RetentionMode) UnmarshalText(text []byte) error {
	return retentionModeTexts().unmarshal("RetentionMode", text, m)
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

// legalHoldTexts are the model's ObjectLockLegalHoldStatus values
// (s3-ssec-object-lock.md 8). LegalHoldNever has none: HEAD omits the
// header for it (6.7), and the field is omitzero, so it is never written.
func legalHoldTexts() textTable[LegalHold] {
	return textTable[LegalHold]{
		LegalHoldOn:  string(s3api.ObjectLockLegalHoldStatusOn),
		LegalHoldOff: string(s3api.ObjectLockLegalHoldStatusOff),
	}
}

// MarshalText writes ON or OFF and refuses LegalHoldNever.
func (h LegalHold) MarshalText() ([]byte, error) {
	return legalHoldTexts().marshal("LegalHold", h)
}

// UnmarshalText reads ON or OFF and refuses any other text.
func (h *LegalHold) UnmarshalText(text []byte) error {
	return legalHoldTexts().unmarshal("LegalHold", text, h)
}

// checkLockTarget refuses explicit lock settings on a bucket without
// Object Lock with 400 InvalidRequest (switch
// s3.object-lock.headers-without-lock).
func checkLockTarget(b *Bucket, explicit ObjectLock) *awserr.Error {
	if explicit != (ObjectLock{}) && b.ObjectLock == nil {
		return errInvalidRequest(missingLockConfigText)
	}
	return nil
}

// missingLockConfigText is S3's refusal of a lock operation on a bucket
// without Object Lock (LocalStack test_s3.snapshot.json L9510, L9520).
const missingLockConfigText = "Bucket is missing Object Lock Configuration"

// defaultRetention is a bucket's default retention rule: its mode, and its
// Days or Years as a duration, a year being 365 days (switch
// s3.object-lock.default-years).
type defaultRetention struct {
	mode   RetentionMode
	period time.Duration
}

// dayLength and yearLength are a default retention's units: a year is 365
// days (switch s3.object-lock.default-years).
const (
	dayLength  = 24 * time.Hour
	yearLength = 365 * dayLength
)

// bucketDefaultRetention is cfg's default retention rule; false when cfg
// has none. checkObjectLock admits only a rule with a mode and one period
// in range, so a stored rule of another form has none either.
func bucketDefaultRetention(cfg *s3api.ObjectLockConfiguration) (defaultRetention, bool) {
	if cfg == nil || cfg.Rule == nil || cfg.Rule.DefaultRetention == nil || cfg.Rule.DefaultRetention.Mode == nil {
		return defaultRetention{}, false
	}
	d := cfg.Rule.DefaultRetention
	mode, ok := retentionModeTexts().value(string(*d.Mode))
	if !ok {
		return defaultRetention{}, false
	}
	switch {
	case d.Days != nil && d.Years == nil:
		return defaultRetention{mode: mode, period: time.Duration(*d.Days) * dayLength}, true
	case d.Years != nil && d.Days == nil:
		return defaultRetention{mode: mode, period: time.Duration(*d.Years) * yearLength}, true
	}
	return defaultRetention{}, false
}

// from is the retention the rule gives a version created at created: the
// creation instant plus the period, to the millisecond
// (s3-ssec-object-lock.md 5.3).
func (d defaultRetention) from(created clock.Instant) Retention {
	return Retention{Mode: d.mode, RetainUntil: clock.FromWire(created.Add(d.period).Wire().Truncate(time.Millisecond))}
}

// lockOnWrite is a new version's lock state: the write's explicit
// settings, with the bucket default's retention when they name none
// (s3-ssec-object-lock.md 5.3). A legal hold comes only from the write.
func lockOnWrite(cfg *s3api.ObjectLockConfiguration, explicit ObjectLock, created clock.Instant) ObjectLock {
	if explicit.Retention != nil {
		return explicit
	}
	if d, ok := bucketDefaultRetention(cfg); ok {
		explicit.Retention = new(d.from(created))
	}
	return explicit
}

// lockedAt is obj as the version a write commits at created holds it:
// obj.Lock, the write's explicit settings, through lockOnWrite with b's
// rule at the commit. obj is left as it is, since kernel.Write may run a
// write more than once.
func lockedAt(b *Bucket, obj *Object, created clock.Instant) *Object {
	o := *obj
	o.Lock = lockOnWrite(b.ObjectLock, obj.Lock, created)
	return &o
}

// needsIntegrity reports whether a PutObject's new version gets a
// retention period, explicit or the bucket default's, and so needs
// Content-MD5 or a checksum (s3-ssec-object-lock.md 6.3). A legal hold
// alone does not (switch s3.object-lock.checksum-scope).
func needsIntegrity(cfg *s3api.ObjectLockConfiguration, explicit ObjectLock) bool {
	if explicit.Retention != nil {
		return true
	}
	_, ok := bucketDefaultRetention(cfg)
	return ok
}

// parseRetentionBody reads a PutObjectRetention body
// (s3-ssec-object-lock.md 6.4): nil for an empty Retention, which removes
// the retention; MalformedXML for no body or a mode without a date or a
// date without a mode; 501 for event holds. Not implemented yet: it
// answers errNotImplementedYet.
func parseRetentionBody(*s3api.ObjectLockRetention) (*Retention, *awserr.Error) {
	return nil, errNotImplementedYet("parseRetentionBody")
}

// checkRetainUntil refuses a retain-until date that is not in the future
// (LocalStack test_s3.snapshot.json L9488; an instant equal to now is no
// longer in the future under switch s3.object-lock.retain-until-format),
// or more than 100 years ahead, with InvalidArgument naming the header or
// element name and rendering the date as Java does (switch
// s3.object-lock.header-texts).
func checkRetainUntil(until, now clock.Instant, name string) *awserr.Error {
	switch {
	case !now.Before(until):
		return errInvalidArgument("The retain until date must be in the future!", name, javaDate(until.Wire()))
	case until.Compare(now.Add(retentionLimit)) > 0:
		return errInvalidArgument("The retain until date in "+name+" must be no more than 100 years in the future.", name, javaDate(until.Wire()))
	}
	return nil
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
// for 2020-01-01T00:00:00Z (LocalStack test_s3.snapshot.json L9488). The
// summer form, PDT, follows switch s3.object-lock.header-texts. The US
// rule in force since 2007 is applied to every year, with fixed zones, so
// no tzdata is read: PDT (UTC-7) from 2:00 PST on the second Sunday of
// March to 2:00 PDT on the first Sunday of November, PST (UTC-8)
// otherwise (15 U.S.C. 260a).
func javaDate(t time.Time) string {
	t = t.UTC()
	zone := time.FixedZone("PST", -8*3600)
	start := sundayOf(t.Year(), time.March, 2).Add(10 * time.Hour)
	end := sundayOf(t.Year(), time.November, 1).Add(9 * time.Hour)
	if !t.Before(start) && t.Before(end) {
		zone = time.FixedZone("PDT", -7*3600)
	}
	return t.In(zone).Format("Mon Jan 02 15:04:05 MST 2006")
}

// sundayOf is the n-th Sunday of month m in year y, at midnight UTC.
func sundayOf(y int, m time.Month, n int) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 0, (7-int(first.Weekday()))%7+7*(n-1))
}

// checkBypassHeader refuses x-amz-bypass-governance-retention, true or
// false, on a bucket without Object Lock (LocalStack
// test_s3_api.snapshot.json L3535, L3546, L3557; recorded on an
// unversioned bucket, and on every other one under switch
// s3.object-lock.bypass-without-lock). The header's presence, not its
// value, decides (s3-ssec-object-lock.md 7.3).
func checkBypassHeader(b *Bucket, header *bool) *awserr.Error {
	if header != nil && b.ObjectLock == nil {
		return withField(s3api.ErrInvalidArgument("x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets."),
			"ArgumentName", "x-amz-bypass-governance-retention")
	}
	return nil
}

// governanceBypass reports whether a request lifts GOVERNANCE retention on
// the object version object names (s3-ssec-object-lock.md 6.6). It is the
// seam IAM enforcement plugs into: under iam.enforce off, the only mode
// this build has, x-amz-bypass-governance-retention: true alone bypasses
// and s3:BypassGovernanceRetention is not evaluated (the coverage notes).
// Callers run it before kernel.Write, holding no lock. Not implemented
// yet: it reports false.
func (*Service) governanceBypass(*kernel.Call, *bool, aws.ARN) bool { return false }
