package s3

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// S3 Control tagging (s3.md 4.2.9, ADR 0013): ListTagsForResource,
// TagResource and UntagResource at /v20180820/tags/{ResourceArn+} on an
// <account>.<base> host. They are raw operations, so the handlers read the
// request and write S3 Control's own XML; they share each bucket's tag set
// with the S3 tagging operations.
const (
	controlNamespace = "http://awss3control.amazonaws.com/doc/2018-08-20/"
	controlTagsPath  = "/v20180820/tags/"
	// maxControlBody bounds a TagResource body: 50 tags of at most 384
	// characters each fit well inside it.
	maxControlBody = 64 << 10
)

// bindControl binds the S3 Control tag operations.
func (s *Service) bindControl(b s3api.Binder) {
	b.ListTagsForResource(s.listTagsForResource, registry.Full{})
	b.TagResource(s.tagResource, registry.Full{})
	b.UntagResource(s.untagResource, registry.Full{})
}

// controlCall reads the bucket an S3 Control tag request names and touches
// it, as touches does for the modeled bucket operations, so the journal,
// the access recorder and fault rules see the bucket.
func controlCall(c *kernel.Call, r *http.Request) (access, *awserr.Error) {
	a, err := controlAccess(r)
	if err != nil {
		return access{}, err
	}
	c.Touch(bucketARN(a.name))
	return a, nil
}

// controlAccess reads the bucket an S3 Control tag request names: the
// percent-encoded bucket ARN after /v20180820/tags/, owned by the account
// x-amz-account-id names.
func controlAccess(r *http.Request) (access, *awserr.Error) {
	escaped, ok := strings.CutPrefix(r.URL.EscapedPath(), controlTagsPath)
	if !ok {
		return access{}, errInvalidRequest("The request path must be " + controlTagsPath + "{ResourceArn}")
	}
	raw, err := url.PathUnescape(escaped)
	if err != nil {
		return access{}, errInvalidRequest("The resource ARN " + escaped + " is not percent-encoded correctly")
	}
	name, ok := bucketOfARN(raw)
	if !ok {
		return access{}, errInvalidRequest("understudy serves S3 Control tags of general purpose buckets only (arn:aws:s3:::<bucket>); " + raw + " names another resource")
	}
	a := access{name: name}
	if v := r.Header.Get("X-Amz-Account-Id"); v != "" {
		a.owner = &ownerClaim{header: accountIDHeader, account: v}
	}
	return a, nil
}

// bucketOfARN returns the bucket a general purpose bucket ARN names:
// arn:aws:s3:::<bucket>, with no region, account or key.
func bucketOfARN(s string) (string, bool) {
	a, err := aws.ParseARN(s)
	if err != nil || a.Partition != aws.PartitionAWS().String() || a.Service != "s3" || a.Region != "" || a.Account != "" || strings.Contains(a.Resource, "/") {
		return "", false
	}
	return a.Resource, true
}

// listTagsForResource answers the bucket's tag set as a
// ListTagsForResourceResult; a bucket without tags answers an empty list.
func (s *Service) listTagsForResource(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	a, err := controlCall(c, r)
	if err != nil {
		return err
	}
	tags, err := read(ctx, s, c, a, func(b *Bucket) ([]s3api.Tag, *awserr.Error) { return tagList(b.Tags), nil })
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	x := xmlw.New(&buf)
	x.Declaration()
	x.StartNS("ListTagsForResourceResult", controlNamespace)
	x.Start("Tags")
	for _, t := range tags {
		x.Start("Tag")
		x.Start("Key")
		x.Text(t.Key)
		x.End()
		x.Start("Value")
		x.Text(t.Value)
		x.End()
		x.End()
	}
	x.End()
	x.End()
	if err := x.Close(); err != nil {
		return awserr.Internalf(err)
	}
	h := controlHeaders(w, c)
	h.Set("Content-Type", "application/xml")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(buf.Bytes()); werr != nil {
		s.log.DebugContext(ctx, "s3 control response not fully written; the client went away")
	}
	return nil
}

// tagResource adds tags to the bucket's set, replacing the values of keys
// it already has.
func (s *Service) tagResource(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	a, err := controlCall(c, r)
	if err != nil {
		return err
	}
	tags, err := readControlTags(r)
	if err != nil {
		return err
	}
	if _, err := tagSet(tags, systemReservedTag); err != nil {
		return err
	}
	err = s.change(ctx, c, a, func(b *Bucket, _ clock.Instant) *awserr.Error {
		merged := tagList(b.Tags)
		for _, t := range tags {
			merged = upsertTag(merged, t)
		}
		set, err := tagSet(merged, systemReservedTag)
		if err != nil {
			return err
		}
		b.Tags = set
		return nil
	})
	if err != nil {
		return err
	}
	controlHeaders(w, c)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// upsertTag sets t in tags, replacing the value of a tag with its key.
func upsertTag(tags []s3api.Tag, t s3api.Tag) []s3api.Tag {
	for i := range tags {
		if tags[i].Key == t.Key {
			tags[i].Value = t.Value
			return tags
		}
	}
	return append(tags, t)
}

// untagResource removes the tagKeys query values from the bucket's set.
func (s *Service) untagResource(ctx context.Context, c *kernel.Call, r *http.Request, w http.ResponseWriter) *awserr.Error {
	a, err := controlCall(c, r)
	if err != nil {
		return err
	}
	keys := r.URL.Query()["tagKeys"]
	err = s.change(ctx, c, a, func(b *Bucket, _ clock.Instant) *awserr.Error {
		tags := b.Tags
		for _, k := range keys {
			tags = without(tags, k)
		}
		if len(tags) == 0 {
			tags = nil
		}
		b.Tags = tags
		return nil
	})
	if err != nil {
		return err
	}
	controlHeaders(w, c)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// readControlTags reads a TagResource body: Tags/Tag{Key,Value} under a
// root of any name, since no recording shows the root the SDKs send
// (tf-storage.md 1.1). A body without Tags, or a tag without Key or Value,
// is MalformedXML, as S3 answers.
func readControlTags(r *http.Request) ([]s3api.Tag, *awserr.Error) {
	root, err := xmlw.Decode(r.Body, maxControlBody)
	if err != nil {
		if tooLarge := (*xmlw.TooLargeError)(nil); errors.As(err, &tooLarge) {
			return nil, errInvalidRequest("The TagResource body is larger than " + strconv.Itoa(maxControlBody) + " bytes")
		}
		return nil, errMalformedXML()
	}
	list, ok := root.Child("Tags")
	if !ok {
		return nil, errMalformedXML()
	}
	var tags []s3api.Tag
	for n := range list.ChildrenNamed("Tag") {
		key, okKey := n.Child("Key")
		value, okValue := n.Child("Value")
		if !okKey || !okValue {
			return nil, errMalformedXML()
		}
		tags = append(tags, s3api.Tag{Key: key.Text, Value: value.Text})
	}
	return tags, nil
}

// controlHeaders sets the request id headers S3 Control answers with and
// returns the header for the caller to add to.
func controlHeaders(w http.ResponseWriter, c *kernel.Call) http.Header {
	h := w.Header()
	h.Set("X-Amz-Request-Id", string(c.ID()))
	h.Set("X-Amz-Id-2", restxml.HostID(string(c.ID())))
	return h
}
