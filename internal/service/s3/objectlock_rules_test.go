package s3

import (
	"maps"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// property runs check as a rapid property in a subtest of its own, so it
// runs whatever the checks before it found: rapid refuses a test that has
// already failed.
func property(t *testing.T, check func(*rapid.T)) {
	t.Helper()
	t.Run("property", func(t *testing.T) { rapid.Check(t, check) })
}

// failer is what the error checks need from *testing.T and *rapid.T.
type failer interface {
	Helper()
	Errorf(format string, args ...any)
}

// wantError is an expected error: status, code, message and its context
// elements by name. LocalStack's snapshots record elements without their
// order, so order is not part of it.
type wantError struct {
	status  int
	code    string
	message string
	fields  map[string]string
}

// check fails f unless got is w.
func (w wantError) check(f failer, what string, got *awserr.Error) {
	f.Helper()
	if got == nil {
		f.Errorf("%s: got no error, want %d %s %q", what, w.status, w.code, w.message)
		return
	}
	if got.ID.Status != w.status || got.ID.QueryCode != w.code || got.Message != w.message {
		f.Errorf("%s: got %d %s %q, want %d %s %q", what, got.ID.Status, got.ID.QueryCode, got.Message, w.status, w.code, w.message)
		return
	}
	fields := map[string]string{}
	for _, x := range got.Fields {
		fields[x.Name] = x.Value
	}
	want := w.fields
	if want == nil {
		want = map[string]string{}
	}
	if !maps.Equal(fields, want) {
		f.Errorf("%s: elements %v, want %v", what, fields, want)
	}
}

// codeStatus is the HTTP status of each S3 error code these tests expect,
// from the S3 error code list
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html).
var codeStatus = map[string]int{
	"AccessDenied":    403,
	"InvalidArgument": 400,
	"InvalidRequest":  400,
	"MalformedXML":    400,
}

// switchValues is the Value of every fidelity switch the S3 service
// declares, by name, as Switches lists it.
func switchValues(t *testing.T) map[string]string {
	t.Helper()
	values := map[string]string{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		for _, e := range New(k.Deps(s3api.Service()), Options{}).Switches() {
			values[e.Name] = e.Value
		}
	}, kerneltest.Services(s3api.Service()))
	return values
}

// declared returns the Value of the switch name. A switch the service does
// not declare fails the test, which goes on to check the rest.
func declared(t *testing.T, values map[string]string, name string) string {
	t.Helper()
	v, ok := values[name]
	if !ok {
		t.Errorf("the S3 service declares no switch %s (s3-ssec-object-lock.md 11.4)", name)
	}
	return v
}

// codeOf is the error code a code-valued switch declares, with its status;
// a status of 0 when it declares none this test knows.
func codeOf(t *testing.T, values map[string]string, name string) (code string, status int) {
	t.Helper()
	code = declared(t, values, name)
	status, ok := codeStatus[code]
	if !ok {
		t.Errorf("switch %s declares %q, which is not an S3 error code this test knows", name, code)
	}
	return code, status
}

// lockNow is the emulator instant the rule tests decide at.
var lockNow = clock.FromWire(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))

// maxRetention is the longest retention S3 accepts: 100 years
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html,
// "The maximum retention period is 100 years"), counted as 36,500 days, the
// day bound of a retention duration
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-configure.html).
const maxRetention = 36500 * 24 * time.Hour

// errLocked is the answer to a change or delete Object Lock forbids:
// 403 AccessDenied (LocalStack test_s3.snapshot.json L9478, L9639, L10044,
// L17856; s3-ssec-object-lock.md 6.4, 7.1).
var errLocked = wantError{status: 403, code: "AccessDenied", message: "Access Denied because object protected by object lock."}

// wantMalformedXML is S3's standard MalformedXML (s3-ssec-object-lock.md 6.4;
// LocalStack test_s3.snapshot.json L9468, L10002).
var wantMalformedXML = wantError{status: 400, code: "MalformedXML", message: "The XML you provided was not well-formed or did not validate against our published schema"}

// eventHoldRefusal is understudy's 501 for an event-hold header or element
// it does not implement (s3-ssec-object-lock.md 11.3, 11.7).
func eventHoldRefusal(name string) wantError {
	return wantError{
		status: 501, code: "NotImplemented",
		message: "understudy does not implement Object Lock event holds (variable retention), so it refuses " + name +
			"; nothing was changed. Send the request without it. Run 'understudy coverage --service s3' for what is supported.",
		fields: map[string]string{"Header": name},
	}
}

// msInstant is t on the emulator clock with sub-millisecond precision
// dropped, the precision a retain-until date keeps (switch
// s3.object-lock.retain-until-format, s3-ssec-object-lock.md 11.4).
func msInstant(t time.Time) clock.Instant {
	return clock.FromWire(t.Truncate(time.Millisecond))
}

// TestRetentionModeText: the modes' texts are the model's ObjectLockRetentionMode
// values (s3-ssec-object-lock.md 8); any other text or value is refused.
func TestRetentionModeText(t *testing.T) {
	for m, text := range map[RetentionMode]string{RetentionGovernance: "GOVERNANCE", RetentionCompliance: "COMPLIANCE"} {
		got, err := m.MarshalText()
		if err != nil || string(got) != text {
			t.Errorf("MarshalText(%d) = %q, %v; want %q", m, got, err, text)
		}
		var back RetentionMode
		if err := back.UnmarshalText([]byte(text)); err != nil || back != m {
			t.Errorf("UnmarshalText(%q) = %d, %v; want %d", text, back, err, m)
		}
	}
	property(t, func(t *rapid.T) {
		m := RetentionMode(rapid.Uint8().Filter(func(v uint8) bool {
			return RetentionMode(v) != RetentionGovernance && RetentionMode(v) != RetentionCompliance
		}).Draw(t, "mode"))
		if got, err := m.MarshalText(); err == nil {
			t.Fatalf("MarshalText(%d) = %q, want an error", m, got)
		}
		text := rapid.String().Filter(func(s string) bool { return s != "GOVERNANCE" && s != "COMPLIANCE" }).Draw(t, "text")
		var back RetentionMode
		if err := back.UnmarshalText([]byte(text)); err == nil {
			t.Fatalf("UnmarshalText(%q) = %d, want an error", text, back)
		}
	})
}

// TestLegalHoldText: a hold is "ON" or "OFF" (model ObjectLockLegalHoldStatus,
// s3-ssec-object-lock.md 8); never-set has no text, since HEAD omits the
// header for it (6.7), and any other text is refused.
func TestLegalHoldText(t *testing.T) {
	for h, text := range map[LegalHold]string{LegalHoldOn: "ON", LegalHoldOff: "OFF"} {
		got, err := h.MarshalText()
		if err != nil || string(got) != text {
			t.Errorf("MarshalText(%d) = %q, %v; want %q", h, got, err, text)
		}
		var back LegalHold
		if err := back.UnmarshalText([]byte(text)); err != nil || back != h {
			t.Errorf("UnmarshalText(%q) = %d, %v; want %d", text, back, err, h)
		}
	}
	if got, err := LegalHoldNever.MarshalText(); err == nil {
		t.Errorf("MarshalText(LegalHoldNever) = %q, want an error", got)
	}
	property(t, func(t *rapid.T) {
		text := rapid.String().Filter(func(s string) bool { return s != "ON" && s != "OFF" }).Draw(t, "text")
		var back LegalHold
		if err := back.UnmarshalText([]byte(text)); err == nil {
			t.Fatalf("UnmarshalText(%q) = %d, want an error", text, back)
		}
	})
}

// nthSunday is the n-th Sunday of month m in year y, at midnight UTC.
func nthSunday(y int, m time.Month, n int) time.Time {
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	return first.AddDate(0, 0, (7-int(first.Weekday()))%7+7*(n-1))
}

// pacific is t in US Pacific time under the daylight-saving rule in force
// since 2007: PDT (UTC-7) from 2:00 PST on the second Sunday of March to
// 2:00 PDT on the first Sunday of November, PST (UTC-8) otherwise
// (15 U.S.C. 260a, https://www.law.cornell.edu/uscode/text/15/260a).
func pacific(t time.Time) time.Time {
	t = t.UTC()
	start := nthSunday(t.Year(), time.March, 2).Add(10 * time.Hour)
	end := nthSunday(t.Year(), time.November, 1).Add(9 * time.Hour)
	if !t.Before(start) && t.Before(end) {
		return t.In(time.FixedZone("PDT", -7*3600))
	}
	return t.In(time.FixedZone("PST", -8*3600))
}

// javaLayout is Java's Date.toString() pattern "EEE MMM dd HH:mm:ss zzz yyyy".
const javaLayout = "Mon Jan 02 15:04:05 MST 2006"

// TestJavaDate: a retain-until date in an error's ArgumentValue is Java's
// Date.toString() in US Pacific time. 2020-01-01T00:00:00Z renders as AWS
// rendered it (LocalStack test_s3.snapshot.json L9488, validated
// 2026-02-21); the summer form and every other instant follow the switch
// s3.object-lock.header-texts (s3-ssec-object-lock.md 11.4: PST or PDT).
func TestJavaDate(t *testing.T) {
	declared(t, switchValues(t), "s3.object-lock.header-texts")
	for in, want := range map[time.Time]string{
		time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC): "Tue Dec 31 16:00:00 PST 2019",
		time.Date(2020, 7, 1, 0, 0, 0, 0, time.UTC): "Tue Jun 30 17:00:00 PDT 2020",
		// The 2026 changes: 8 March 10:00 UTC and 1 November 09:00 UTC.
		time.Date(2026, 3, 8, 9, 59, 59, 0, time.UTC):  "Sun Mar 08 01:59:59 PST 2026",
		time.Date(2026, 3, 8, 10, 0, 0, 0, time.UTC):   "Sun Mar 08 03:00:00 PDT 2026",
		time.Date(2026, 11, 1, 8, 59, 59, 0, time.UTC): "Sun Nov 01 01:59:59 PDT 2026",
		time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC):   "Sun Nov 01 01:00:00 PST 2026",
	} {
		if got := javaDate(in); got != want {
			t.Errorf("javaDate(%s) = %q, want %q", in.Format(time.RFC3339), got, want)
		}
	}
	property(t, func(t *rapid.T) {
		base := time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC)
		in := base.Add(time.Duration(rapid.Int64Range(0, 400*365*24*3600).Draw(t, "seconds")) * time.Second)
		if got, want := javaDate(in), pacific(in).Format(javaLayout); got != want {
			t.Fatalf("javaDate(%s) = %q, want %q", in.Format(time.RFC3339), got, want)
		}
	})
}

// pastDate is the refusal of a retain-until date that is not in the
// future, naming the header or element (LocalStack test_s3.snapshot.json
// L9488).
func pastDate(name string, until time.Time) wantError {
	return wantError{
		status: 400, code: "InvalidArgument", message: "The retain until date must be in the future!",
		fields: map[string]string{"ArgumentName": name, "ArgumentValue": javaDate(until)},
	}
}

// tooFar is the refusal of a retain-until date more than 100 years ahead:
// the code of the switch s3.object-lock.header-texts, with understudy's
// text naming the header or element (s3-ssec-object-lock.md 11.4).
func tooFar(t *testing.T, values map[string]string, name string, until time.Time) wantError {
	t.Helper()
	code, status := codeOf(t, values, "s3.object-lock.header-texts")
	return wantError{
		status: status, code: code, message: "The retain until date in " + name + " must be no more than 100 years in the future.",
		fields: map[string]string{"ArgumentName": name, "ArgumentValue": javaDate(until)},
	}
}

// TestCheckRetainUntil: a date at or before now is the past-date refusal
// (LocalStack test_s3.snapshot.json L9488; an equal instant is no longer in
// the future under switch s3.object-lock.retain-until-format), one up to
// 100 years ahead is accepted, and one beyond is refused (switch
// s3.object-lock.header-texts).
func TestCheckRetainUntil(t *testing.T) {
	values := switchValues(t)
	declared(t, values, "s3.object-lock.retain-until-format")
	property(t, func(rt *rapid.T) {
		name := rapid.SampledFrom([]string{"RetainUntilDate", "x-amz-object-lock-retain-until-date"}).Draw(rt, "name")
		var offset time.Duration
		switch rapid.IntRange(0, 2).Draw(rt, "side") {
		case 0:
			offset = -time.Duration(rapid.Int64Range(0, int64(200*365*24*time.Hour)).Draw(rt, "past"))
		case 1:
			offset = time.Duration(rapid.Int64Range(1, int64(maxRetention)).Draw(rt, "ahead"))
		default:
			offset = maxRetention + time.Duration(rapid.Int64Range(1, int64(50*365*24*time.Hour)).Draw(rt, "beyond"))
		}
		until := lockNow.Add(offset)
		got := checkRetainUntil(until, lockNow, name)
		switch {
		case offset <= 0:
			pastDate(name, until.Wire()).check(rt, "until "+until.String(), got)
		case offset <= maxRetention:
			if got != nil {
				rt.Fatalf("until %s, %s ahead: %s %q, want accepted", until, offset, got.ID.QueryCode, got.Message)
			}
		default:
			tooFar(t, values, name, until.Wire()).check(rt, "until "+until.String(), got)
		}
	})
}

// TestRetentionInForce: a retention protects its version while now is
// before its date and no longer from the date on (switch
// s3.object-lock.retain-until-format: deletable once now >= RetainUntil).
func TestRetentionInForce(t *testing.T) {
	declared(t, switchValues(t), "s3.object-lock.retain-until-format")
	property(t, func(t *rapid.T) {
		mode := rapid.SampledFrom([]RetentionMode{RetentionGovernance, RetentionCompliance}).Draw(t, "mode")
		r := Retention{Mode: mode, RetainUntil: lockNow.Add(time.Duration(rapid.Int64Range(-int64(time.Hour), int64(time.Hour)).Draw(t, "offset")))}
		if got, want := r.inForce(lockNow), lockNow.Before(r.RetainUntil); got != want {
			t.Fatalf("%+v.inForce(%s) = %v, want %v", r, lockNow, got, want)
		}
	})
}

// retentionGen draws a retention of either mode whose date is in force at
// lockNow, expired, or exactly at lockNow.
func retentionGen() *rapid.Generator[Retention] {
	return rapid.Custom(func(t *rapid.T) Retention {
		mode := rapid.SampledFrom([]RetentionMode{RetentionGovernance, RetentionCompliance}).Draw(t, "mode")
		offset := time.Duration(rapid.Int64Range(-int64(48*time.Hour), int64(48*time.Hour)).Draw(t, "offset")).Truncate(time.Millisecond)
		return Retention{Mode: mode, RetainUntil: lockNow.Add(offset)}
	})
}

// changeAllowed is whether PutObjectRetention may replace current with
// next at lockNow (s3-ssec-object-lock.md 6.4, 6.6). An expired retention
// imposes nothing: protection lasts for the retention period
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html,
// "Retention periods"). In force:
//   - GOVERNANCE: a same-or-later GOVERNANCE date needs no bypass
//     (LocalStack test_s3.snapshot.json L9871); an earlier date or removal
//     needs it (L9478, L9677); a change to COMPLIANCE needs it (switch
//     s3.object-lock.mode-upgrade).
//   - COMPLIANCE: only a same-or-later COMPLIANCE date, bypass or not
//     (L17884, L17894; "its retention mode can't be changed, and its
//     retention period can't be shortened", object-lock.html).
func changeAllowed(current, next *Retention, bypass bool) bool {
	if current == nil || !lockNow.Before(current.RetainUntil) {
		return true
	}
	keeps := next != nil && next.Mode == current.Mode && next.RetainUntil.Compare(current.RetainUntil) >= 0
	if current.Mode == RetentionCompliance {
		return keeps
	}
	return keeps || bypass
}

// TestWithRetention checks every retention change against changeAllowed: an
// allowed change stores next (nil removes the retention) and keeps the
// legal hold, which is independent of retention (s3-ssec-object-lock.md
// 6.5); a refused one is the lock 403.
func TestWithRetention(t *testing.T) {
	declared(t, switchValues(t), "s3.object-lock.mode-upgrade")
	property(t, func(t *rapid.T) {
		var current, next *Retention
		if rapid.Bool().Draw(t, "has current") {
			current = new(retentionGen().Draw(t, "current"))
		}
		if rapid.Bool().Draw(t, "has next") {
			r := retentionGen().Draw(t, "next")
			if !lockNow.Before(r.RetainUntil) {
				r.RetainUntil = lockNow.Add(time.Millisecond)
			}
			next = &r
		}
		hold := rapid.SampledFrom([]LegalHold{LegalHoldNever, LegalHoldOn, LegalHoldOff}).Draw(t, "hold")
		bypass := rapid.Bool().Draw(t, "bypass")
		got, err := ObjectLock{Retention: current, LegalHold: hold}.withRetention(next, lockNow, bypass)
		what := "withRetention"
		if !changeAllowed(current, next, bypass) {
			errLocked.check(t, what, err)
			return
		}
		if err != nil {
			t.Fatalf("change %+v -> %+v bypass %v: %s %q, want allowed", current, next, bypass, err.ID.QueryCode, err.Message)
		}
		if got.LegalHold != hold || (got.Retention == nil) != (next == nil) || next != nil && *got.Retention != *next {
			t.Fatalf("change %+v -> %+v = %+v, want retention %+v and hold %d", current, next, got, next, hold)
		}
	})
}

// TestCheckDelete: a versioned delete is refused while a legal hold is on,
// whatever the bypass, or while a retention is in force, COMPLIANCE always
// and GOVERNANCE without bypass (LocalStack test_s3.snapshot.json L9639,
// L9649, L10044, L17856, L17874, L17904; bypass never lifts a hold,
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html).
func TestCheckDelete(t *testing.T) {
	property(t, func(t *rapid.T) {
		var r *Retention
		if rapid.Bool().Draw(t, "has retention") {
			r = new(retentionGen().Draw(t, "retention"))
		}
		hold := rapid.SampledFrom([]LegalHold{LegalHoldNever, LegalHoldOn, LegalHoldOff}).Draw(t, "hold")
		bypass := rapid.Bool().Draw(t, "bypass")
		inForce := r != nil && lockNow.Before(r.RetainUntil)
		locked := hold == LegalHoldOn || inForce && (r.Mode == RetentionCompliance || !bypass)
		err := ObjectLock{Retention: r, LegalHold: hold}.checkDelete(lockNow, bypass)
		if locked {
			errLocked.check(t, "checkDelete", err)
			return
		}
		if err != nil {
			t.Fatalf("delete of %+v hold %d bypass %v: %s %q, want allowed", r, hold, bypass, err.ID.QueryCode, err.Message)
		}
	})
}

// TestObjectLockHeadersParse checks the stateless header checks of
// PutObject, CopyObject and CreateMultipartUpload in their order: event
// holds (understudy's 501), the mode and date pairing before the mode value
// (LocalStack test_s3.snapshot.json L9754, L17916, L17927), the mode value
// (L17938), the legal-hold value, then the date (switch
// s3.object-lock.header-texts).
func TestObjectLockHeadersParse(t *testing.T) {
	values := switchValues(t)
	holdCode, holdStatus := codeOf(t, values, "s3.object-lock.header-texts")
	future := lockNow.Wire().Add(24*time.Hour + 123456789)
	past := lockNow.Wire().Add(-time.Second)
	far := lockNow.Wire().Add(maxRetention + time.Second)
	bad := s3api.ObjectLockMode("BAD-VALUE")
	gov := s3api.ObjectLockModeGovernance
	on := s3api.ObjectLockLegalHoldStatusOn
	cases := []struct {
		name string
		h    objectLockHeaders
		want wantError
	}{
		{"event hold first", objectLockHeaders{eventHold: new(s3api.ObjectLockEventHoldOn), mode: &bad}, eventHoldRefusal("x-amz-object-lock-event-hold")},
		{"event hold days", objectLockHeaders{eventHoldDays: new(int32(1)), eventHoldYears: new(int32(1))}, eventHoldRefusal("x-amz-object-lock-event-hold-duration-days")},
		{"event hold years", objectLockHeaders{eventHoldYears: new(int32(1)), retainUntil: &future}, eventHoldRefusal("x-amz-object-lock-event-hold-duration-years")},
		{"mode without date", objectLockHeaders{mode: &gov}, wantError{
			status: 400, code: "InvalidArgument", message: "x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied",
			fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date"},
		}},
		{"bad mode without date", objectLockHeaders{mode: &bad}, wantError{
			status: 400, code: "InvalidArgument", message: "x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied",
			fields: map[string]string{"ArgumentName": "x-amz-object-lock-retain-until-date"},
		}},
		{"date without mode", objectLockHeaders{retainUntil: &future}, wantError{
			status: 400, code: "InvalidArgument", message: "x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied",
			fields: map[string]string{"ArgumentName": "x-amz-object-lock-mode"},
		}},
		{"bad mode", objectLockHeaders{mode: &bad, retainUntil: &past}, wantError{
			status: 400, code: "InvalidArgument", message: "Unknown wormMode directive.",
			fields: map[string]string{"ArgumentName": "x-amz-object-lock-mode", "ArgumentValue": "BAD-VALUE"},
		}},
		{"bad legal hold before the date", objectLockHeaders{mode: &gov, retainUntil: &past, legalHold: new(s3api.ObjectLockLegalHoldStatus("MAYBE"))}, wantError{
			status: holdStatus, code: holdCode, message: "Invalid value for the x-amz-object-lock-legal-hold header.",
			fields: map[string]string{"ArgumentName": "x-amz-object-lock-legal-hold", "ArgumentValue": "MAYBE"},
		}},
		{"past date", objectLockHeaders{mode: &gov, retainUntil: &past}, pastDate("x-amz-object-lock-retain-until-date", past)},
		{"date over 100 years", objectLockHeaders{mode: &gov, retainUntil: &far, legalHold: &on}, tooFar(t, values, "x-amz-object-lock-retain-until-date", far)},
	}
	for _, tc := range cases {
		got, err := tc.h.parse(lockNow)
		tc.want.check(t, tc.name, err)
		if got != (ObjectLock{}) {
			t.Errorf("%s: parse returned %+v beside its error", tc.name, got)
		}
	}
	property(t, func(t *rapid.T) {
		var h objectLockHeaders
		var want ObjectLock
		if rapid.Bool().Draw(t, "retention") {
			mode := rapid.SampledFrom([]s3api.ObjectLockMode{s3api.ObjectLockModeGovernance, s3api.ObjectLockModeCompliance}).Draw(t, "mode")
			until := lockNow.Wire().Add(time.Duration(rapid.Int64Range(1, int64(maxRetention)).Draw(t, "ahead")))
			h.mode, h.retainUntil = &mode, &until
			wantMode := RetentionGovernance
			if mode == s3api.ObjectLockModeCompliance {
				wantMode = RetentionCompliance
			}
			want.Retention = &Retention{Mode: wantMode, RetainUntil: msInstant(until)}
		}
		switch rapid.IntRange(0, 2).Draw(t, "hold") {
		case 1:
			h.legalHold, want.LegalHold = new(s3api.ObjectLockLegalHoldStatusOn), LegalHoldOn
		case 2:
			h.legalHold, want.LegalHold = new(s3api.ObjectLockLegalHoldStatusOff), LegalHoldOff
		}
		got, err := h.parse(lockNow)
		if err != nil {
			t.Fatalf("parse(%+v) = %s %q, want accepted", h, err.ID.QueryCode, err.Message)
		}
		if got.LegalHold != want.LegalHold || (got.Retention == nil) != (want.Retention == nil) || want.Retention != nil && *got.Retention != *want.Retention {
			t.Fatalf("parse = %+v, want %+v", got, want)
		}
	})
}

// lockConfig is an Object Lock configuration with a default retention of
// mode for days or years, or none when both are nil.
func lockConfig(mode s3api.ObjectLockRetentionMode, days, years *int32) *s3api.ObjectLockConfiguration {
	cfg := &s3api.ObjectLockConfiguration{ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled)}
	if days != nil || years != nil {
		cfg.Rule = &s3api.ObjectLockRule{DefaultRetention: &s3api.DefaultRetention{Mode: &mode, Days: days, Years: years}}
	}
	return cfg
}

// TestCheckLockTarget: explicit lock settings on a bucket without Object
// Lock are refused (switch s3.object-lock.headers-without-lock), none are
// fine anywhere, and any are fine on a lock bucket.
func TestCheckLockTarget(t *testing.T) {
	code, status := codeOf(t, switchValues(t), "s3.object-lock.headers-without-lock")
	missing := wantError{status: status, code: code, message: "Bucket is missing Object Lock Configuration"}
	explicit := []ObjectLock{
		{LegalHold: LegalHoldOn},
		{LegalHold: LegalHoldOff},
		{Retention: &Retention{Mode: RetentionGovernance, RetainUntil: lockNow.Add(time.Hour)}},
	}
	for _, l := range explicit {
		missing.check(t, "plain bucket", checkLockTarget(&Bucket{}, l))
		if err := checkLockTarget(&Bucket{ObjectLock: lockConfig("", nil, nil)}, l); err != nil {
			t.Errorf("lock bucket, %+v: %s %q, want accepted", l, err.ID.QueryCode, err.Message)
		}
	}
	if err := checkLockTarget(&Bucket{}, ObjectLock{}); err != nil {
		t.Errorf("plain bucket, no settings: %s %q, want accepted", err.ID.QueryCode, err.Message)
	}
}

// TestDefaultRetention: a bucket rule's default dates a new version at its
// creation plus the period (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html,
// "Retention periods"), a year being 365 days (switch
// s3.object-lock.default-years); explicit settings override it and a legal
// hold comes only from them. A version needs Content-MD5 or a checksum when
// it gets a retention (testdata/parity/s3/R12/11-put-without-checksum.json:
// a bucket default and no lock header), and not for a legal hold alone
// (switch s3.object-lock.checksum-scope).
func TestDefaultRetention(t *testing.T) {
	values := switchValues(t)
	declared(t, values, "s3.object-lock.default-years")
	declared(t, values, "s3.object-lock.checksum-scope")
	gov, comp := s3api.ObjectLockRetentionModeGovernance, s3api.ObjectLockRetentionModeCompliance
	for _, cfg := range []*s3api.ObjectLockConfiguration{nil, lockConfig(gov, nil, nil), {ObjectLockEnabled: new(s3api.ObjectLockEnabledEnabled), Rule: &s3api.ObjectLockRule{}}} {
		if d, ok := bucketDefaultRetention(cfg); ok {
			t.Errorf("bucketDefaultRetention(%+v) = %+v, want none", cfg, d)
		}
		if needsIntegrity(cfg, ObjectLock{LegalHold: LegalHoldOn}) {
			t.Errorf("needsIntegrity(%+v, legal hold only) = true, want false", cfg)
		}
	}
	created := lockNow.Add(123456789)
	for _, tc := range []struct {
		cfg  *s3api.ObjectLockConfiguration
		want defaultRetention
	}{
		{lockConfig(gov, new(int32(1)), nil), defaultRetention{mode: RetentionGovernance, period: 24 * time.Hour}},
		{lockConfig(comp, new(int32(36500)), nil), defaultRetention{mode: RetentionCompliance, period: 36500 * 24 * time.Hour}},
		{lockConfig(gov, nil, new(int32(1))), defaultRetention{mode: RetentionGovernance, period: 365 * 24 * time.Hour}},
		{lockConfig(comp, nil, new(int32(4))), defaultRetention{mode: RetentionCompliance, period: 4 * 365 * 24 * time.Hour}},
	} {
		got, ok := bucketDefaultRetention(tc.cfg)
		if !ok || got != tc.want {
			t.Errorf("bucketDefaultRetention(%+v) = %+v, %v; want %+v", tc.cfg.Rule.DefaultRetention, got, ok, tc.want)
			continue
		}
		dated := Retention{Mode: tc.want.mode, RetainUntil: msInstant(created.Wire().Add(tc.want.period))}
		if r := got.from(created); r != dated {
			t.Errorf("%+v.from(%s) = %+v, want %+v", got, created, r, dated)
		}
		if l := lockOnWrite(tc.cfg, ObjectLock{LegalHold: LegalHoldOff}, created); l.LegalHold != LegalHoldOff || l.Retention == nil || *l.Retention != dated {
			t.Errorf("lockOnWrite(default, hold OFF) = %+v, want %+v with hold OFF", l, dated)
		}
		explicit := Retention{Mode: RetentionCompliance, RetainUntil: lockNow.Add(5 * time.Hour)}
		if l := lockOnWrite(tc.cfg, ObjectLock{Retention: &explicit}, created); l.LegalHold != LegalHoldNever || l.Retention == nil || *l.Retention != explicit {
			t.Errorf("lockOnWrite(default, explicit %+v) = %+v, want the explicit retention", explicit, l)
		}
		if !needsIntegrity(tc.cfg, ObjectLock{}) {
			t.Errorf("needsIntegrity(%+v, none) = false, want true for a bucket default", tc.cfg.Rule.DefaultRetention)
		}
	}
	explicit := ObjectLock{Retention: &Retention{Mode: RetentionGovernance, RetainUntil: lockNow.Add(time.Hour)}, LegalHold: LegalHoldOn}
	if l := lockOnWrite(lockConfig(gov, nil, nil), explicit, created); l.LegalHold != explicit.LegalHold || l.Retention == nil || *l.Retention != *explicit.Retention {
		t.Errorf("lockOnWrite(no default, %+v) = %+v, want the explicit settings", explicit, l)
	}
	if l := lockOnWrite(lockConfig(gov, nil, nil), ObjectLock{}, created); l != (ObjectLock{}) {
		t.Errorf("lockOnWrite(no default, none) = %+v, want none", l)
	}
	if !needsIntegrity(lockConfig(gov, nil, nil), explicit) {
		t.Error("needsIntegrity(no default, explicit retention) = false, want true")
	}
}

// TestParseRetentionBody: an empty <Retention/> removes the retention
// (LocalStack test_s3.snapshot.json L9677); event-hold elements are
// understudy's 501; a mode without a date or a bad mode is MalformedXML
// (L9468, L9500), as is a date without a mode under switch
// s3.object-lock.date-without-mode.
func TestParseRetentionBody(t *testing.T) {
	values := switchValues(t)
	dateOnlyCode, dateOnlyStatus := codeOf(t, values, "s3.object-lock.date-without-mode")
	until := lockNow.Wire().Add(time.Hour + 987654321)
	gov := s3api.ObjectLockRetentionModeGovernance
	if r, err := parseRetentionBody(&s3api.ObjectLockRetention{}); r != nil || err != nil {
		t.Errorf("parseRetentionBody(<Retention/>) = %+v, %v; want removal", r, err)
	}
	for _, tc := range []struct {
		name string
		in   s3api.ObjectLockRetention
		want wantError
	}{
		{"event hold", s3api.ObjectLockRetention{EventHold: new(s3api.ObjectLockEventHoldOn), Mode: &gov}, eventHoldRefusal("EventHold")},
		{"event hold duration", s3api.ObjectLockRetention{EventHoldDuration: &s3api.EventHoldDuration{Days: new(int32(1))}, Mode: &gov, RetainUntilDate: &until}, eventHoldRefusal("EventHoldDuration")},
		{"mode without date", s3api.ObjectLockRetention{Mode: &gov}, wantMalformedXML},
		{"bad mode", s3api.ObjectLockRetention{Mode: new(s3api.ObjectLockRetentionMode("BAD-VALUE")), RetainUntilDate: &until}, wantMalformedXML},
		{"date without mode", s3api.ObjectLockRetention{RetainUntilDate: &until}, wantError{
			status: dateOnlyStatus, code: dateOnlyCode, message: wantMalformedXML.message,
		}},
	} {
		r, err := parseRetentionBody(&tc.in)
		tc.want.check(t, tc.name, err)
		if r != nil {
			t.Errorf("%s: parseRetentionBody returned %+v beside its error", tc.name, r)
		}
	}
	for mode, want := range map[s3api.ObjectLockRetentionMode]RetentionMode{gov: RetentionGovernance, s3api.ObjectLockRetentionModeCompliance: RetentionCompliance} {
		r, err := parseRetentionBody(&s3api.ObjectLockRetention{Mode: &mode, RetainUntilDate: &until})
		if err != nil || r == nil || *r != (Retention{Mode: want, RetainUntil: msInstant(until)}) {
			t.Errorf("parseRetentionBody(%s, %s) = %+v, %v; want the retention to the millisecond", mode, until.Format(time.RFC3339Nano), r, err)
		}
	}
}

// TestParseLegalHoldBody: no body, no Status or another value is
// MalformedXML (LocalStack test_s3.snapshot.json L10002); ON and OFF set
// the hold.
func TestParseLegalHoldBody(t *testing.T) {
	for name, in := range map[string]*s3api.ObjectLockLegalHold{
		"no body":   nil,
		"no status": {},
		"bad value": {Status: new(s3api.ObjectLockLegalHoldStatus("MAYBE"))},
	} {
		_, err := parseLegalHoldBody(in)
		wantMalformedXML.check(t, name, err)
	}
	for status, want := range map[s3api.ObjectLockLegalHoldStatus]LegalHold{
		s3api.ObjectLockLegalHoldStatusOn: LegalHoldOn, s3api.ObjectLockLegalHoldStatusOff: LegalHoldOff,
	} {
		if got, err := parseLegalHoldBody(&s3api.ObjectLockLegalHold{Status: &status}); err != nil || got != want {
			t.Errorf("parseLegalHoldBody(%s) = %d, %v; want %d", status, got, err, want)
		}
	}
}

// TestCheckBypassHeader: x-amz-bypass-governance-retention, true or false,
// on a bucket without Object Lock is refused (LocalStack
// test_s3_api.snapshot.json L3535, L3546; switch
// s3.object-lock.bypass-without-lock for every such bucket); absent, or on
// a lock bucket, it passes.
func TestCheckBypassHeader(t *testing.T) {
	declared(t, switchValues(t), "s3.object-lock.bypass-without-lock")
	refused := wantError{
		status: 400, code: "InvalidArgument", message: "x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.",
		fields: map[string]string{"ArgumentName": "x-amz-bypass-governance-retention"},
	}
	locked := &Bucket{ObjectLock: lockConfig("", nil, nil)}
	for _, v := range []bool{true, false} {
		refused.check(t, "plain bucket", checkBypassHeader(&Bucket{}, &v))
		if err := checkBypassHeader(locked, &v); err != nil {
			t.Errorf("lock bucket, header %v: %s %q, want accepted", v, err.ID.QueryCode, err.Message)
		}
	}
	for _, b := range []*Bucket{{}, locked} {
		if err := checkBypassHeader(b, nil); err != nil {
			t.Errorf("no header: %s %q, want accepted", err.ID.QueryCode, err.Message)
		}
	}
}

// TestBlockedEncryptionText: the bucket block's texts are the model's
// EncryptionType values (s3-ssec-object-lock.md 4.2); others are refused.
func TestBlockedEncryptionText(t *testing.T) {
	for b, text := range map[BlockedEncryption]string{BlockSSEC: "SSE-C", BlockNone: "NONE"} {
		got, err := b.MarshalText()
		if err != nil || string(got) != text {
			t.Errorf("MarshalText(%d) = %q, %v; want %q", b, got, err, text)
		}
		var back BlockedEncryption
		if err := back.UnmarshalText([]byte(text)); err != nil || back != b {
			t.Errorf("UnmarshalText(%q) = %d, %v; want %d", text, back, err, b)
		}
	}
	for _, text := range []string{"", "sse-c", "AES256", "none"} {
		var back BlockedEncryption
		if err := back.UnmarshalText([]byte(text)); err == nil {
			t.Errorf("UnmarshalText(%q) = %d, want an error", text, back)
		}
	}
}
