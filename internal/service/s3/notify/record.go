package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// Event is one object change as its notification reports it.
type Event struct {
	// Time is when S3 finished processing the request.
	Time time.Time
	// Region is the bucket's home region, not the caller's.
	Region string
	Bucket Bucket
	// Key is the raw key; Record URL-encodes it.
	Key string
	// VersionID is the version the change made or removed; empty when the
	// change made or removed the null version (an unversioned or
	// versioning-suspended bucket), whose id the response does not report
	// either; the record then has no versionId.
	VersionID string
	Change    Change
	Request   Request
}

// Bucket is the bucket an event happened in.
type Bucket struct {
	Name string
	ARN  aws.ARN
	// Owner is ownerIdentity.principalId, which AWS keeps only for
	// backward compatibility.
	Owner string
	// Account is the bucket owner's account, which S3 sends as
	// aws:SourceAccount with every notification (iam-enforcement-surfaces.md
	// 8.1, aws-doc).
	Account aws.AccountID
}

// Request is the call that made the change, as the record and the test
// event echo it.
type Request struct {
	// ID and HostID are the x-amz-request-id and x-amz-id-2 the call
	// answered with.
	ID     string
	HostID string
	// Principal is userIdentity.principalId.
	Principal string
	// SourceIP is requestParameters.sourceIPAddress.
	SourceIP string
}

// Change is what happened to the object; the variant decides the event
// name and which object fields the record carries.
//
//sumtype:decl
type Change interface {
	// Name is the event name without the "s3:" prefix, such as
	// "ObjectCreated:Put".
	Name() string
	change()
}

// Created is an ObjectCreated event.
type Created struct {
	Action    CreatedAction
	Size      int64
	ETag      string // without quotes
	Sequencer Sequencer
}

// Removed is an ObjectRemoved event. The record carries no size, and no
// eTag except a delete marker's, which is the empty object's.
type Removed struct {
	Action    RemovedAction
	Sequencer Sequencer
}

// Tagged is an ObjectTagging event. The record carries the eTag but no size
// or sequencer.
type Tagged struct {
	Action TaggingAction
	ETag   string
}

// Retained is an ObjectRetention event: a retention placed by an explicit
// PutObjectRetention (aws-doc, s3-ssec-object-lock.md 7). Mode and
// RetainUntil are the version's retention after the change, which the
// record's objectRetentionEventData carries. It has no event-hold members,
// as event holds answer 501. Inferred: like a tagging record, the record
// carries the eTag but no size or sequencer.
type Retained struct {
	Action      RetentionAction
	ETag        string
	Mode        s3api.ObjectLockRetentionMode
	RetainUntil time.Time
}

// Name returns "ObjectCreated:<Action>".
func (c Created) Name() string { return "ObjectCreated:" + c.Action.String() }

// Name returns "ObjectRemoved:<Action>".
func (c Removed) Name() string { return "ObjectRemoved:" + c.Action.String() }

// Name returns "ObjectTagging:<Action>".
func (c Tagged) Name() string { return "ObjectTagging:" + c.Action.String() }

// Name returns "ObjectRetention:<Action>".
func (c Retained) Name() string { return "ObjectRetention:" + c.Action.String() }

func (Created) change()  {}
func (Removed) change()  {}
func (Tagged) change()   {}
func (Retained) change() {}

// CreatedAction is the request that created an object.
type CreatedAction uint8

// CreatedAction values.
const (
	CreatedPut CreatedAction = iota + 1
	CreatedPost
	CreatedCopy
	CreatedCompleteMultipartUpload
)

// String is the action as the event name spells it, such as "Put"; a value
// outside the declared ones reads "CreatedAction(n)".
func (a CreatedAction) String() string {
	switch a {
	case CreatedPut:
		return "Put"
	case CreatedPost:
		return "Post"
	case CreatedCopy:
		return "Copy"
	case CreatedCompleteMultipartUpload:
		return "CompleteMultipartUpload"
	}
	return fmt.Sprintf("CreatedAction(%d)", uint8(a))
}

// RemovedAction is how an object was removed: its version deleted, or a
// delete marker put over it.
type RemovedAction uint8

// RemovedAction values.
const (
	RemovedDelete RemovedAction = iota + 1
	RemovedDeleteMarkerCreated
)

// String is the action as the event name spells it, such as "Delete"; a
// value outside the declared ones reads "RemovedAction(n)".
func (a RemovedAction) String() string {
	switch a {
	case RemovedDelete:
		return "Delete"
	case RemovedDeleteMarkerCreated:
		return "DeleteMarkerCreated"
	}
	return fmt.Sprintf("RemovedAction(%d)", uint8(a))
}

// TaggingAction is the change to an object's tag set.
type TaggingAction uint8

// TaggingAction values.
const (
	TaggingPut TaggingAction = iota + 1
	TaggingDelete
)

// String is the action as the event name spells it, such as "Put"; a value
// outside the declared ones reads "TaggingAction(n)".
func (a TaggingAction) String() string {
	switch a {
	case TaggingPut:
		return "Put"
	case TaggingDelete:
		return "Delete"
	}
	return fmt.Sprintf("TaggingAction(%d)", uint8(a))
}

// RetentionAction is the change to an object's retention. The model's
// Event enum names only s3:ObjectRetention:Put.
type RetentionAction uint8

// RetentionAction values.
const (
	RetentionPut RetentionAction = iota + 1
)

// String is the action as the event name spells it, "Put"; a value outside
// the declared ones reads "RetentionAction(n)".
func (a RetentionAction) String() string {
	if a == RetentionPut {
		return "Put"
	}
	return fmt.Sprintf("RetentionAction(%d)", uint8(a))
}

// eventVersion is every record's version: R17 recorded 2.6 for Put, Copy,
// CompleteMultipartUpload and DeleteMarkerCreated (sqs/R17/02-events-*.json),
// and S3 has used one version for every event type since 2.4 (aws-doc,
// s3-ssec-object-lock.md 7).
const eventVersion = "2.6"

// emptyETag is the eTag of an empty object, the MD5 of no bytes, which a
// delete marker's record carries (sqs/R17/02-events-1.json).
const emptyETag = "d41d8cd98f00b204e9800998ecf8427e"

// Record returns the notification message of ev for the configuration
// configID: {"Records":[...]} with one record, in AWS's key order. A change
// that is nil or names no event S3 reports is a *ChangeError.
func Record(ev Event, configID string) ([]byte, error) {
	if err := checkChange(ev.Change); err != nil {
		return nil, err
	}
	obj := object{Key: EncodeKey(ev.Key), VersionID: ev.VersionID}
	var retention *objectRetentionEventData
	switch c := ev.Change.(type) {
	case Created:
		obj.Size, obj.ETag, obj.Sequencer = &c.Size, c.ETag, c.Sequencer.String()
		if c.Action == CreatedCopy {
			obj.HasObjectAnnotation = new(false)
		}
	case Removed:
		obj.Sequencer = c.Sequencer.String()
		if c.Action == RemovedDeleteMarkerCreated {
			obj.ETag = emptyETag
		}
	case Tagged:
		obj.ETag = c.ETag
	case Retained:
		obj.ETag = c.ETag
		// Inferred: the documentation says only "ISO-8601"; this is
		// GetObjectRetention's millisecond form.
		retention = &objectRetentionEventData{Mode: string(c.Mode), RetainUntilDate: wireTime(c.RetainUntil)}
	}
	r := record{
		EventVersion:      eventVersion,
		EventSource:       "aws:s3",
		AWSRegion:         ev.Region,
		EventTime:         wireTime(ev.Time),
		EventName:         ev.Change.Name(),
		UserIdentity:      principal{ID: ev.Request.Principal},
		RequestParameters: requestParameters{SourceIP: ev.Request.SourceIP},
		ResponseElements:  responseElements{RequestID: ev.Request.ID, HostID: ev.Request.HostID},
		S3: entity{
			SchemaVersion:   "1.0",
			ConfigurationID: configID,
			Bucket:          bucket{Name: ev.Bucket.Name, OwnerIdentity: principal{ID: ev.Bucket.Owner}, ARN: ev.Bucket.ARN.String()},
			Object:          obj,
		},
		ObjectRetentionEventData: retention,
	}
	return encode(records{Records: []record{r}})
}

// TestEvent returns the s3:TestEvent message S3 sends a queue or topic when
// a configuration names it. It is not in the Records format.
func TestEvent(bucket string, at time.Time, req Request) ([]byte, error) {
	return encode(testEvent{
		Service:   "Amazon S3",
		Event:     "s3:TestEvent",
		Time:      wireTime(at),
		Bucket:    bucket,
		RequestID: req.ID,
		HostID:    req.HostID,
	})
}

// EncodeKey is object.key as the record carries it: form-encoded, a space
// as '+' and every byte outside letters, digits, "-_.*" and '/' as %XX
// (AWS-recorded: a@b is a%40b, testupload/dir1/testfile.txt unchanged).
func EncodeKey(key string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(key) {
		c := key[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-_.*/", c) >= 0:
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xF])
		}
	}
	return b.String()
}

// wireTime is ISO 8601 UTC with milliseconds, as eventTime and Time are.
func wireTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// encode is compact JSON without HTML escaping: a configuration id is the
// caller's text, and AWS sends '<' and '&' as they are.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

type records struct {
	Records []record `json:"Records"`
}

type record struct {
	EventVersion      string            `json:"eventVersion"`
	EventSource       string            `json:"eventSource"`
	AWSRegion         string            `json:"awsRegion"`
	EventTime         string            `json:"eventTime"`
	EventName         string            `json:"eventName"`
	UserIdentity      principal         `json:"userIdentity"`
	RequestParameters requestParameters `json:"requestParameters"`
	ResponseElements  responseElements  `json:"responseElements"`
	S3                entity            `json:"s3"`
	// ObjectRetentionEventData is set on an ObjectRetention record only,
	// after s3 (aws-doc, s3-ssec-object-lock.md 7).
	ObjectRetentionEventData *objectRetentionEventData `json:"objectRetentionEventData,omitempty"`
}

// objectRetentionEventData holds the members the documentation says are
// always present; eventHold and eventHoldDuration belong to event-hold
// operations, which answer 501.
type objectRetentionEventData struct {
	Mode            string `json:"mode"`
	RetainUntilDate string `json:"retainUntilDate"`
}

type principal struct {
	ID string `json:"principalId"`
}

type requestParameters struct {
	SourceIP string `json:"sourceIPAddress"`
}

type responseElements struct {
	RequestID string `json:"x-amz-request-id"`
	HostID    string `json:"x-amz-id-2"`
}

type entity struct {
	SchemaVersion   string `json:"s3SchemaVersion"`
	ConfigurationID string `json:"configurationId"`
	Bucket          bucket `json:"bucket"`
	Object          object `json:"object"`
}

type bucket struct {
	Name          string    `json:"name"`
	OwnerIdentity principal `json:"ownerIdentity"`
	ARN           string    `json:"arn"`
}

type object struct {
	Key       string `json:"key"`
	Size      *int64 `json:"size,omitempty"`
	ETag      string `json:"eTag,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Sequencer string `json:"sequencer,omitempty"`
	// HasObjectAnnotation is set on a copy's record only, which R17
	// recorded as false (sqs/R17/02-events-3.json).
	HasObjectAnnotation *bool `json:"hasObjectAnnotation,omitempty"`
}

type testEvent struct {
	Service   string `json:"Service"`
	Event     string `json:"Event"`
	Time      string `json:"Time"`
	Bucket    string `json:"Bucket"`
	RequestID string `json:"RequestId"`
	HostID    string `json:"HostId"`
}
