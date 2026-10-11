package s3

import (
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The conditional request rules of s3.md 5.10: reads follow RFC 9110
// section 13.2.2, copy-source conditions fail with 412 where a read would
// answer 304, and writes accept If-None-Match: * or If-Match: <etag> alone.

// The texts of the conditional answers (s3.md 5.10, REC
// TestS3ObjectWritePrecondition and test_s3_get_object_preconditions).
const (
	preconditionFailedMessage  = "At least one of the pre-conditions you specified did not hold"
	conditionalConflictMessage = "The conditional request cannot succeed due to a conflicting operation against this resource."
	notImplementedHeader       = "A header you provided implies functionality that is not implemented"
	unacceptedValueMessage     = "We don't accept the provided value of "
	multipleConditionsMessage  = "Multiple conditional request headers present in the request"
)

// The conditional request headers, as error elements name them.
const (
	headerIfMatch           = "If-Match"
	headerIfNoneMatch       = "If-None-Match"
	headerIfModifiedSince   = "If-Modified-Since"
	headerIfUnmodifiedSince = "If-Unmodified-Since"
	copySourcePrefix        = "x-amz-copy-source-"
)

// validator is what a condition is judged against: the current version's
// ETag (unquoted) and Last-Modified, which S3 keeps to the second.
type validator struct {
	etag         string
	lastModified time.Time
}

// readConditions are the conditional headers of GetObject and HeadObject,
// or the x-amz-copy-source-if-* headers of CopyObject.
type readConditions struct {
	ifMatch           *string
	ifNoneMatch       *string
	ifModifiedSince   *time.Time
	ifUnmodifiedSince *time.Time
}

// readOutcome is how a read's conditions resolve: the read proceeds, or it
// answers 304 Not Modified, or 412 naming the condition that failed.
//
//sumtype:decl
type readOutcome interface{ isReadOutcome() }

type proceed struct{}

type notModified struct{}

type preconditionFailed struct{ condition string }

func (proceed) isReadOutcome()            {}
func (notModified) isReadOutcome()        {}
func (preconditionFailed) isReadOutcome() {}

// evaluate applies RFC 9110 section 13.2.2 at second resolution:
// If-Match, else If-Unmodified-Since; then If-None-Match, else
// If-Modified-Since, which a date after now leaves unapplied (s3.md 5.10).
func (rc readConditions) evaluate(v validator, now time.Time) readOutcome {
	switch {
	case rc.ifMatch != nil:
		if !etagListMatches(*rc.ifMatch, v.etag) {
			return preconditionFailed{condition: headerIfMatch}
		}
	case rc.ifUnmodifiedSince != nil:
		if seconds(v.lastModified).After(seconds(*rc.ifUnmodifiedSince)) {
			return preconditionFailed{condition: headerIfUnmodifiedSince}
		}
	}
	switch {
	case rc.ifNoneMatch != nil:
		if etagListMatches(*rc.ifNoneMatch, v.etag) {
			return notModified{}
		}
	case rc.ifModifiedSince != nil:
		since := seconds(*rc.ifModifiedSince)
		if !since.After(now) && !seconds(v.lastModified).After(since) {
			return notModified{}
		}
	}
	return proceed{}
}

// readError is the answer of a read whose conditions do not let it
// proceed, or nil.
func readError(o readOutcome) *awserr.Error {
	switch o := o.(type) {
	case notModified:
		return s3api.ErrNotModified("Not Modified")
	case preconditionFailed:
		return errPreconditionFailed(o.condition)
	case proceed:
	}
	return nil
}

// copySourceError is the answer of a CopyObject whose source conditions do
// not hold: always 412, naming the x-amz-copy-source- header, even where a
// read would answer 304 (s3.md 5.10).
func copySourceError(o readOutcome, rc readConditions) *awserr.Error {
	switch o := o.(type) {
	case notModified:
		if rc.ifNoneMatch != nil {
			return errPreconditionFailed(copySourcePrefix + headerIfNoneMatch)
		}
		return errPreconditionFailed(copySourcePrefix + headerIfModifiedSince)
	case preconditionFailed:
		return errPreconditionFailed(copySourcePrefix + o.condition)
	case proceed:
	}
	return nil
}

// writeCondition is a conditional write's precondition on the key's
// current version (s3.md 5.10).
//
//sumtype:decl
type writeCondition interface{ isWriteCondition() }

// unconditional is a write without If-Match or If-None-Match.
type unconditional struct{}

// ifAbsent is If-None-Match: *: the key must have no current object.
type ifAbsent struct{}

// ifMatches is If-Match: <etag>: the current object must carry etag.
type ifMatches struct{ etag string }

func (unconditional) isWriteCondition() {}
func (ifAbsent) isWriteCondition()      {}
func (ifMatches) isWriteCondition()     {}

// parseWriteCondition reads the conditional headers of a write. Only
// If-None-Match: * and If-Match: <etag>, each alone, are implemented by S3
// on general purpose buckets; anything else is 501 naming the header.
func parseWriteCondition(ifMatch, ifNoneMatch *string) (writeCondition, *awserr.Error) {
	switch {
	case ifMatch != nil && ifNoneMatch != nil:
		return nil, errConditionNotImplemented(headerIfMatch+","+headerIfNoneMatch, multipleConditionsMessage)
	case ifNoneMatch != nil:
		if strings.TrimSpace(*ifNoneMatch) != "*" {
			return nil, errConditionNotImplemented(headerIfNoneMatch, unacceptedValueMessage+headerIfNoneMatch+" header for this API")
		}
		return ifAbsent{}, nil
	case ifMatch != nil:
		if strings.TrimSpace(*ifMatch) == "*" {
			return nil, errConditionNotImplemented(headerIfMatch, unacceptedValueMessage+headerIfMatch+" header for this API")
		}
		return ifMatches{etag: unquote(*ifMatch)}, nil
	}
	return unconditional{}, nil
}

// checkWrite judges w against the key's current object: current is nil
// when the key has none or its current version is a delete marker.
func checkWrite(w writeCondition, key string, current *validator) *awserr.Error {
	switch w := w.(type) {
	case ifAbsent:
		if current != nil {
			return errPreconditionFailed(headerIfNoneMatch)
		}
	case ifMatches:
		if current == nil {
			return errNoSuchKey(key)
		}
		if current.etag != w.etag {
			return errPreconditionFailed(headerIfMatch)
		}
	case unconditional:
	}
	return nil
}

// etagListMatches reports whether a conditional header value, a
// comma-separated list of entity tags with or without quotes or "*",
// names etag.
func etagListMatches(header, etag string) bool {
	for item := range strings.SplitSeq(header, ",") {
		item = strings.TrimSpace(item)
		if item == "*" || unquote(strings.TrimPrefix(item, "W/")) == etag {
			return true
		}
	}
	return false
}

// unquote strips the double quotes around an entity tag; S3 matches an
// ETag sent with or without them (s3.md 5.3).
func unquote(etag string) string {
	etag = strings.TrimSpace(etag)
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		return etag[1 : len(etag)-1]
	}
	return etag
}

// quote is an ETag in its header and XML form.
func quote(etag string) string { return `"` + etag + `"` }

// seconds is t truncated to the second, the resolution S3 compares
// conditional dates at (s3.md 5.5).
func seconds(t time.Time) time.Time { return t.Truncate(time.Second) }

func errPreconditionFailed(condition string) *awserr.Error {
	return withField(s3api.ErrPreconditionFailed(preconditionFailedMessage), "Condition", condition)
}

// errConditionNotImplemented answers a conditional write header S3 does
// not implement on general purpose buckets (s3.md 5.10).
func errConditionNotImplemented(header, additional string) *awserr.Error {
	return withField(withField(s3api.ErrNotImplemented(notImplementedHeader), "Header", header), "additionalMessage", additional)
}

func errNoSuchKey(key string) *awserr.Error {
	return withField(s3api.ErrNoSuchKey("The specified key does not exist."), "Key", key)
}
