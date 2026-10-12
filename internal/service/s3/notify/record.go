package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// Removed is an ObjectRemoved event. The record carries no size and no
// eTag.
type Removed struct {
	Action    RemovedAction
	Sequencer Sequencer
}

// Tagged is an ObjectTagging event. The record is eventVersion 2.3 and
// carries the eTag but no size or sequencer.
type Tagged struct {
	Action TaggingAction
	ETag   string
}

// Name returns "ObjectCreated:<Action>".
func (c Created) Name() string { return "ObjectCreated:" + c.Action.String() }

// Name returns "ObjectRemoved:<Action>".
func (c Removed) Name() string { return "ObjectRemoved:" + c.Action.String() }

// Name returns "ObjectTagging:<Action>".
func (c Tagged) Name() string { return "ObjectTagging:" + c.Action.String() }

func (Created) change() {}
func (Removed) change() {}
func (Tagged) change()  {}

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

// Record returns the notification message of ev for the configuration
// configID: {"Records":[...]} with one record, in AWS's key order. A change
// that is nil or names no event S3 reports is a *ChangeError.
func Record(ev Event, configID string) ([]byte, error) {
	if err := checkChange(ev.Change); err != nil {
		return nil, err
	}
	obj := object{Key: EncodeKey(ev.Key), VersionID: ev.VersionID}
	version := "2.1"
	switch c := ev.Change.(type) {
	case Created:
		obj.Size, obj.ETag, obj.Sequencer = &c.Size, c.ETag, c.Sequencer.String()
	case Removed:
		obj.Sequencer = c.Sequencer.String()
	case Tagged:
		obj.ETag = c.ETag
		version = "2.3"
	}
	r := record{
		EventVersion:      version,
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
}

type testEvent struct {
	Service   string `json:"Service"`
	Event     string `json:"Event"`
	Time      string `json:"Time"`
	Bucket    string `json:"Bucket"`
	RequestID string `json:"RequestId"`
	HostID    string `json:"HostId"`
}
