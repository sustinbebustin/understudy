package s3

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// fieldOf is the value of an error's context element name, or "".
func fieldOf(fields []awserr.Field, name string) string {
	for _, f := range fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// TestReadConditionMatrix is the conditional read table of s3.md 5.10,
// recorded against AWS for GetObject and HeadObject, plus the DOC
// combinations: If-Match true with If-Unmodified-Since false proceeds, and
// If-None-Match false with If-Modified-Since true proceeds.
func TestReadConditionMatrix(t *testing.T) {
	modified := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	now := modified.Add(time.Hour)
	v := validator{etag: "abc", lastModified: modified.Add(400 * time.Millisecond)}
	before, after, future := modified.Add(-time.Minute), modified.Add(time.Minute), now.Add(time.Hour)
	for _, tc := range []struct {
		name string
		rc   readConditions
		want readOutcome
	}{
		{"no conditions", readConditions{}, proceed{}},
		{"If-Match equal, quoted", readConditions{ifMatch: new(`"abc"`)}, proceed{}},
		{"If-Match equal, unquoted", readConditions{ifMatch: new("abc")}, proceed{}},
		{"If-Match in a list", readConditions{ifMatch: new(`"x", "abc"`)}, proceed{}},
		{"If-Match differs", readConditions{ifMatch: new(`"x"`)}, preconditionFailed{condition: "If-Match"}},
		{"If-Unmodified-Since earlier", readConditions{ifUnmodifiedSince: &before}, preconditionFailed{condition: "If-Unmodified-Since"}},
		{"If-Unmodified-Since equal to the second", readConditions{ifUnmodifiedSince: &modified}, proceed{}},
		{"If-Unmodified-Since later", readConditions{ifUnmodifiedSince: &after}, proceed{}},
		{"If-None-Match equal", readConditions{ifNoneMatch: new(`"abc"`)}, notModified{}},
		{"If-None-Match equal without quotes", readConditions{ifNoneMatch: new("abc")}, notModified{}},
		{"If-None-Match other", readConditions{ifNoneMatch: new(`"x"`)}, proceed{}},
		{"If-Modified-Since equal", readConditions{ifModifiedSince: &modified}, notModified{}},
		{"If-Modified-Since later", readConditions{ifModifiedSince: &after}, notModified{}},
		{"If-Modified-Since earlier", readConditions{ifModifiedSince: &before}, proceed{}},
		{"If-Modified-Since in the future is ignored", readConditions{ifModifiedSince: &future}, proceed{}},
		{"If-Match true wins over If-Unmodified-Since false", readConditions{ifMatch: new("abc"), ifUnmodifiedSince: &before}, proceed{}},
		{"If-None-Match false wins over If-Modified-Since true", readConditions{ifNoneMatch: new("x"), ifModifiedSince: &after}, proceed{}},
		{"If-Match false before If-None-Match", readConditions{ifMatch: new("x"), ifNoneMatch: new("abc")}, preconditionFailed{condition: "If-Match"}},
	} {
		if got := tc.rc.evaluate(v, now); got != tc.want {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

// TestCopySourceConditionsAlwaysFailWith412 checks that the copy-source
// conditions that a read answers with 304 fail CopyObject with 412 naming
// the x-amz-copy-source header (REC TestS3CopySourcePreconditions).
func TestCopySourceConditionsAlwaysFailWith412(t *testing.T) {
	modified := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	v := validator{etag: "abc", lastModified: modified}
	for _, tc := range []struct {
		rc        readConditions
		condition string
	}{
		{readConditions{ifMatch: new("x")}, "x-amz-copy-source-If-Match"},
		{readConditions{ifNoneMatch: new("abc")}, "x-amz-copy-source-If-None-Match"},
		{readConditions{ifModifiedSince: &modified}, "x-amz-copy-source-If-Modified-Since"},
		{readConditions{ifUnmodifiedSince: new(modified.Add(-time.Hour))}, "x-amz-copy-source-If-Unmodified-Since"},
	} {
		err := copySourceError(tc.rc.evaluate(v, modified.Add(time.Hour)), tc.rc)
		if err == nil || err.ID.Status != 412 || err.ID.QueryCode != "PreconditionFailed" {
			t.Fatalf("%+v: got %v, want 412 PreconditionFailed", tc.rc, err)
		}
		if got := fieldOf(err.Fields, "Condition"); got != tc.condition {
			t.Errorf("%+v: Condition = %q, want %q", tc.rc, got, tc.condition)
		}
	}
}

// TestWriteConditionMatrix is the conditional write table of s3.md 5.10
// (REC TestS3ObjectWritePrecondition).
func TestWriteConditionMatrix(t *testing.T) {
	existing := &validator{etag: "abc"}
	for _, tc := range []struct {
		name                 string
		ifMatch, ifNoneMatch *string
		current              *validator
		code                 string
		status               int
		elements             map[string]string
	}{
		{name: "If-None-Match * on a missing key", ifNoneMatch: new("*")},
		{
			name: "If-None-Match * on an existing key", ifNoneMatch: new("*"), current: existing,
			code: "PreconditionFailed", status: 412, elements: map[string]string{"Condition": "If-None-Match"},
		},
		{
			name: "If-None-Match with an etag", ifNoneMatch: new(`"abc"`), code: "NotImplemented", status: 501,
			elements: map[string]string{"Header": "If-None-Match", "additionalMessage": "We don't accept the provided value of If-None-Match header for this API"},
		},
		{name: "If-Match equal", ifMatch: new(`"abc"`), current: existing},
		{name: "If-Match unquoted and equal", ifMatch: new("abc"), current: existing},
		{
			name: "If-Match differs", ifMatch: new(`"x"`), current: existing,
			code: "PreconditionFailed", status: 412, elements: map[string]string{"Condition": "If-Match"},
		},
		{name: "If-Match on a missing key", ifMatch: new(`"abc"`), code: "NoSuchKey", status: 404, elements: map[string]string{"Key": "k"}},
		{
			name: "If-Match *", ifMatch: new("*"), current: existing, code: "NotImplemented", status: 501,
			elements: map[string]string{"Header": "If-Match", "additionalMessage": "We don't accept the provided value of If-Match header for this API"},
		},
		{
			name: "both headers", ifMatch: new("abc"), ifNoneMatch: new("*"), code: "NotImplemented", status: 501,
			elements: map[string]string{"Header": "If-Match,If-None-Match", "additionalMessage": "Multiple conditional request headers present in the request"},
		},
	} {
		w, err := parseWriteCondition(tc.ifMatch, tc.ifNoneMatch)
		if err == nil {
			err = checkWrite(w, "k", tc.current)
		}
		if tc.code == "" {
			if err != nil {
				t.Errorf("%s: got %s %d, want success", tc.name, err.ID.QueryCode, err.ID.Status)
			}
			continue
		}
		if err == nil || err.ID.QueryCode != tc.code || err.ID.Status != tc.status {
			t.Errorf("%s: got %v, want %s %d", tc.name, err, tc.code, tc.status)
			continue
		}
		for name, want := range tc.elements {
			if got := fieldOf(err.Fields, name); got != want {
				t.Errorf("%s: %s = %q, want %q", tc.name, name, got, want)
			}
		}
	}
}
