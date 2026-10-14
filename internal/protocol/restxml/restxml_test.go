package restxml_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
)

const requestID = "4442587FB7D0A2F9"

var codec = restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect {
	d := restxml.S3Dialect(invalidArgument())
	d.Internal = s3InternalError()
	return d
}}

func newRequest(method, target, body string, header map[string]string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, "http://localhost:4566"+target, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// S3 answers a missing bucket with a bare <Error> root: Code, Message, the
// BucketName context element, RequestId and HostId, and the request id
// pair in headers (wire-rest.md 2.4; error-matrix.md 5.4).
func TestNoSuchBucketGolden(t *testing.T) {
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.s3#NoSuchBucket", QueryCode: "NoSuchBucket", Status: 404, Fault: awserr.Sender, MessageKey: awserr.MessageUpper},
		Message: "The specified bucket does not exist",
		Fields:  []awserr.Field{{Name: "BucketName", Value: "mybucket"}},
		Origin:  awserr.Modeled{},
	}
	resp := codec.EncodeError(newRequest(http.MethodGet, "/mybucket?list-type=2", "", nil), s3(), requestID, e)
	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<Error><Code>NoSuchBucket</Code><Message>The specified bucket does not exist</Message><BucketName>mybucket</BucketName>` +
		`<RequestId>4442587FB7D0A2F9</RequestId><HostId>` + restxml.HostID(requestID) + `</HostId></Error>`
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
	if resp.Status != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/xml" ||
		resp.Header.Get("X-Amz-Request-Id") != requestID || resp.Header.Get("X-Amz-Id-2") != restxml.HostID(requestID) {
		t.Errorf("status %d, headers %v", resp.Status, resp.Header)
	}

	// A HEAD error is the status and headers, with no body or Content-Type
	// (wire-rest.md 2.4).
	resp = codec.EncodeError(newRequest(http.MethodHead, "/mybucket", "", nil), s3(), requestID, e)
	if resp.Status != http.StatusNotFound || readAll(t, resp.Body) != "" || resp.Header.Get("Content-Type") != "" || resp.Header.Get("X-Amz-Request-Id") != requestID {
		t.Errorf("HEAD error = %d %v", resp.Status, resp.Header)
	}
}

// CompleteMultipartUpload can fail after S3 has sent 200: the body is an
// <Error> right after the XML declaration, with the request id pair
// (wire-rest.md 3.7). The pipeline sets the status to 200.
func TestCompleteMultipartUploadOKWithErrorGolden(t *testing.T) {
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.s3#InternalError", QueryCode: "InternalError", Status: http.StatusOK, Fault: awserr.Receiver, MessageKey: awserr.MessageUpper},
		Message: "We encountered an internal error. Please try again.",
	}
	resp := codec.EncodeError(newRequest(http.MethodPost, "/bucket/key?uploadId=abc", "", nil), s3(), requestID, e)
	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<Error><Code>InternalError</Code><Message>We encountered an internal error. Please try again.</Message>` +
		`<RequestId>4442587FB7D0A2F9</RequestId><HostId>` + restxml.HostID(requestID) + `</HostId></Error>`
	if got := readAll(t, resp.Body); resp.Status != http.StatusOK || got != want {
		t.Errorf("%d\n%s\nwant\n%s", resp.Status, got, want)
	}
	if resp.Header.Get("X-Amz-Request-Id") != requestID || resp.Header.Get("X-Amz-Id-2") == "" {
		t.Errorf("headers = %v", resp.Header)
	}
}

// A bug (awserr.Internalf, InternalFailure) is S3's InternalError with
// S3's text, and keeps its status, so the 200-with-error path still
// answers 200 (wire-rest.md 3.6, 3.7). Without the dialect's identity it
// stays InternalFailure.
func TestInternalErrorGolden(t *testing.T) {
	r := newRequest(http.MethodGet, "/bucket/key", "", nil)
	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<Error><Code>InternalError</Code><Message>We encountered an internal error. Please try again.</Message>` +
		`<RequestId>4442587FB7D0A2F9</RequestId><HostId>` + restxml.HostID(requestID) + `</HostId></Error>`
	e := awserr.Internalf(errors.New("a handler bug"))
	resp := codec.EncodeError(r, s3(), requestID, e)
	if got := readAll(t, resp.Body); resp.Status != http.StatusInternalServerError || got != want {
		t.Errorf("%d\n%s\nwant\n%s", resp.Status, got, want)
	}

	e.ID.Status = http.StatusOK
	resp = codec.EncodeError(r, s3(), requestID, e)
	if got := readAll(t, resp.Body); resp.Status != http.StatusOK || got != want {
		t.Errorf("200-with-error: %d\n%s\nwant\n%s", resp.Status, got, want)
	}

	plain := restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect { return restxml.S3Dialect(invalidArgument()) }}
	resp = plain.EncodeError(r, s3(), requestID, awserr.Internalf(errors.New("a handler bug")))
	if got := readAll(t, resp.Body); !strings.Contains(got, "<Code>InternalFailure</Code>") {
		t.Errorf("without Dialect.Internal: %s", got)
	}
}

// Without noErrorWrapping, errors take Smithy's <ErrorResponse> form.
func TestWrappedErrors(t *testing.T) {
	e := &awserr.Error{ID: awserr.Identity{Shape: "aws.protocoltests.restxml#InvalidGreeting", Status: 400, Fault: awserr.Sender}, Message: "Hi"}
	resp := restxml.Codec{}.EncodeError(newRequest(http.MethodPost, "/", "", nil), nil, "foo-id", e)
	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<ErrorResponse><Error><Type>Sender</Type><Code>InvalidGreeting</Code><Message>Hi</Message></Error><RequestId>foo-id</RequestId></ErrorResponse>`
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("body = %s", got)
	}
}

// A ListObjectsV2 page: the S3 namespace on the root, flattened Contents
// and CommonPrefixes, millisecond timestamps, quoted ETags escaped, and
// with encoding-type=url every key field percent-encoded with "+" as %2B
// (wire-rest.md 2.2, 3.4; s3.md 9.1).
func TestListBucketResultGolden(t *testing.T) {
	svc := s3()
	o := op(svc, "ListObjectsV2", http.MethodGet, "/{Bucket}?list-type=2", listIn, listOut)
	out := &listOutput{
		Contents: []s3Object{{
			Key: "photos/2006/a+b c=d.jpg", LastModified: time.Date(2009, 10, 12, 17, 50, 30, 0, time.UTC),
			ETag: `"fba9dede5f27731c9771645a39863328"`, Size: 434234, StorageClass: "STANDARD",
		}},
		Name: "bucket", Prefix: "photos/", Delimiter: new("/"), MaxKeys: 1000,
		CommonPrefixes: []string{"photos/2006/"}, EncodingType: new("url"), KeyCount: 2,
	}
	resp, err := codec.Encode(newRequest(http.MethodGet, "/bucket?list-type=2&prefix=photos/&delimiter=/&encoding-type=url", "", nil), o, requestID, out, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>` +
		`<Contents><Key>photos/2006/a%2Bb%20c%3Dd.jpg</Key><LastModified>2009-10-12T17:50:30.000Z</LastModified>` +
		`<ETag>&#34;fba9dede5f27731c9771645a39863328&#34;</ETag><Size>434234</Size><StorageClass>STANDARD</StorageClass></Contents>` +
		`<Name>bucket</Name><Prefix>photos/</Prefix><Delimiter>/</Delimiter><MaxKeys>1000</MaxKeys>` +
		`<CommonPrefixes><Prefix>photos/2006/</Prefix></CommonPrefixes><EncodingType>url</EncodingType><KeyCount>2</KeyCount></ListBucketResult>`
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("body =\n%s\nwant\n%s", got, want)
	}
	if resp.Header.Get("Content-Type") != "application/xml" || resp.Header.Get("Content-Length") != "587" || resp.Header.Get("X-Amz-Request-Id") != requestID {
		t.Errorf("headers = %v", resp.Header)
	}

	// Without encoding-type=url, keys are written as they are.
	out.EncodingType = nil
	resp, err = codec.Encode(newRequest(http.MethodGet, "/bucket?list-type=2", "", nil), o, requestID, out, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	if got := readAll(t, resp.Body); !strings.Contains(got, "<Key>photos/2006/a+b c=d.jpg</Key>") {
		t.Errorf("raw keys: %s", got)
	}
}

// ListObjectsV2's parameters arrive in the query, percent-decoded once; a
// max-keys that is not an integer is InvalidArgument naming it (s3.md 9.1).
func TestListObjectsRequest(t *testing.T) {
	o := op(s3(), "ListObjectsV2", http.MethodGet, "/{Bucket}?list-type=2", listIn, listOut)
	r := newRequest(http.MethodGet, "/my.bucket?list-type=2&prefix=a%2Bb%20c%2F&max-keys=10&encoding-type=url&x-id=ListObjectsV2", "", nil)
	got := &listInput{}
	if err := codec.Decode(r, o, edgeBody{r}, got); err != nil {
		t.Fatal(err.AsError())
	}
	want := &listInput{Bucket: "my.bucket", Prefix: new("a+b c/"), MaxKeys: new(int32(10)), EncodingType: new("url")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Decode = %+v", got)
	}
	r = newRequest(http.MethodGet, "/b?list-type=2&max-keys=ten", "", nil)
	err := codec.Decode(r, o, edgeBody{r}, &listInput{})
	if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Fields[0].Value != "max-keys" || err.Fields[1].Value != "ten" {
		t.Errorf("max-keys=ten: %+v", err)
	}

	// An invalid escape is InvalidArgument, never a parameter dropped as
	// absent; a ';' is part of the value.
	r = newRequest(http.MethodGet, "/b?list-type=2&prefix=%zz", "", nil)
	err = codec.Decode(r, o, edgeBody{r}, &listInput{})
	if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Fields[0].Value != "prefix" || err.Fields[1].Value != "%zz" {
		t.Errorf("prefix=%%zz: %+v", err)
	}
	r = newRequest(http.MethodGet, "/b?list-type=2&prefix=a;b", "", nil)
	got = &listInput{}
	if err := codec.Decode(r, o, edgeBody{r}, got); err != nil || got.Prefix == nil || *got.Prefix != "a;b" {
		t.Errorf("prefix=a;b: %+v, %v", got, err)
	}
}

// S3's NotModified is a 304 with no body: no Content-Type, no
// Content-Length, only the request id pair (wire-rest.md 3.6).
func TestNotModifiedHasNoBody(t *testing.T) {
	e := &awserr.Error{
		ID:      awserr.Identity{Shape: "com.amazonaws.s3#NotModified", QueryCode: "NotModified", Status: http.StatusNotModified, Fault: awserr.Sender, MessageKey: awserr.MessageUpper},
		Message: "Not Modified",
	}
	resp := codec.EncodeError(newRequest(http.MethodGet, "/b/k", "", map[string]string{"If-None-Match": `"etag"`}), s3(), requestID, e)
	if resp.Status != http.StatusNotModified || readAll(t, resp.Body) != "" ||
		resp.Header.Get("Content-Type") != "" || resp.Header.Get("Content-Length") != "" || resp.Header.Get("X-Amz-Request-Id") != requestID {
		t.Errorf("NotModified = %d %v", resp.Status, resp.Header)
	}
}

// GetBucketLocation's output is unwrapped: the constraint is the root's
// text (the Smithy case GetBucketLocationUnwrappedOutput); a us-east-1
// bucket's is empty.
func TestGetBucketLocationGolden(t *testing.T) {
	o := op(s3(), "GetBucketLocation", http.MethodGet, "/{Bucket}?location", locIn, locOut)
	resp, err := codec.Encode(newRequest(http.MethodGet, "/bucket?location", "", nil), o, requestID, &locationOutput{LocationConstraint: new("us-west-2")}, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<LocationConstraint xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\">us-west-2</LocationConstraint>"
	if got := readAll(t, resp.Body); got != want {
		t.Errorf("body = %q\nwant   %q", got, want)
	}
	resp, err = codec.Encode(newRequest(http.MethodGet, "/bucket?location", "", nil), o, requestID, &locationOutput{}, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	if got := readAll(t, resp.Body); got != "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<LocationConstraint xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\"></LocationConstraint>" {
		t.Errorf("us-east-1 body = %q", got)
	}
}

// An ACL grantee binds the xsi prefix and carries xsi:type on its start
// tag although Type follows ID in the model (wire-rest.md 2.2).
func TestGranteeXSIType(t *testing.T) {
	o := op(s3(), "GetBucketAcl", http.MethodGet, "/{Bucket}?acl", locIn, aclOut)
	out := &aclOutput{Grants: []aclGrant{{ID: "abc", Type: "CanonicalUser", Permission: "FULL_CONTROL"}}}
	resp, err := codec.Encode(newRequest(http.MethodGet, "/b?acl", "", nil), o, requestID, out, http.StatusOK)
	if err != nil {
		t.Fatal(err.AsError())
	}
	want := `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><AccessControlList><Grant>` +
		`<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>abc</ID></Grantee>` +
		`<Permission>FULL_CONTROL</Permission></Grant></AccessControlList></AccessControlPolicy>`
	if got := readAll(t, resp.Body); !strings.HasSuffix(got, want) {
		t.Errorf("body = %s", got)
	}
}

// A CompleteMultipartUpload body is read on local names, with or without
// the S3 namespace (wire-rest.md 2.3), its flattened Part list gathered.
func TestCompleteMultipartUploadRequest(t *testing.T) {
	o := op(s3(), "CompleteMultipartUpload", http.MethodPost, "/{Bucket}/{Key+}", completeIn, nil)
	for _, root := range []string{`<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`, `<CompleteMultipartUpload>`} {
		body := root + `<Part><ETag>"a"</ETag><PartNumber>1</PartNumber></Part><Unknown/><Part><PartNumber>2</PartNumber><ETag>&quot;b&quot;</ETag></Part></CompleteMultipartUpload>`
		r := newRequest(http.MethodPost, "/bucket/dir/my%20key?uploadId=up-1", body, map[string]string{"x-amz-mp-object-size": "42"})
		got := &complete{}
		if err := codec.Decode(r, o, edgeBody{r}, got); err != nil {
			t.Fatalf("%s: %v", root, err.AsError())
		}
		want := &complete{
			Bucket: "bucket", Key: "dir/my key", UploadID: "up-1", Size: new(int64(42)), hasUpload: true,
			Parts: []completedPart{{ETag: new(`"a"`), PartNumber: new(int32(1))}, {ETag: new(`"b"`), PartNumber: new(int32(2))}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v", root, got)
		}
	}
}

// Malformed XML is S3's MalformedXML with its text, and so is a
// well-formed body whose root is not the payload's element (wire-rest.md
// 2.3); an unparsable header value is InvalidArgument naming it
// (error-matrix.md 5.4 T1, T2).
func TestMalformedRequests(t *testing.T) {
	o := op(s3(), "CompleteMultipartUpload", http.MethodPost, "/{Bucket}/{Key+}", completeIn, nil)
	for _, body := range []string{
		"<CompleteMultipartUpload>", "not xml", "<a/><b/>", "<C><Part><PartNumber>x</PartNumber></Part></C>",
		"<Foo><Part><PartNumber>1</PartNumber></Part></Foo>",
	} {
		r := newRequest(http.MethodPost, "/b/k?uploadId=u", body, nil)
		err := codec.Decode(r, o, edgeBody{r}, &complete{})
		if err == nil || err.ID.QueryCode != "MalformedXML" || err.Message != "The XML you provided was not well-formed or did not validate against our published schema" {
			t.Errorf("body %q: %+v", body, err)
		}
	}
	r := newRequest(http.MethodPost, "/b/k?uploadId=u", "", map[string]string{"x-amz-mp-object-size": "big"})
	err := codec.Decode(r, o, edgeBody{r}, &complete{})
	want := []awserr.Field{{Name: "ArgumentName", Value: "x-amz-mp-object-size"}, {Name: "ArgumentValue", Value: "big"}}
	if err == nil || err.ID.QueryCode != "InvalidArgument" || !reflect.DeepEqual(err.Fields, want) {
		t.Errorf("bad header: %+v", err)
	}
}

// A @streaming payload is handed over unread, capped at S3's 5 GiB
// single PUT; a trailer-bound member resolves at its EOF (restbind).
func TestStreamingPayload(t *testing.T) {
	o := op(s3(), "PutObject", http.MethodPut, "/{Bucket}/{Key+}", putIn, nil)
	if got := codec.BodyLimit(o); got != 5<<30 {
		t.Errorf("BodyLimit = %d", got)
	}
	r := newRequest(http.MethodPut, "/b/k", "object bytes", nil)
	in := &putInput{}
	if err := codec.Decode(r, o, edgeBody{r}, in); err != nil {
		t.Fatal(err.AsError())
	}
	b, ok := in.Body.(*restbind.Body)
	if !ok {
		t.Fatalf("Body is %T", in.Body)
	}
	rc, err := b.Open()
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, rc); got != "object bytes" {
		t.Errorf("body = %q", got)
	}
}

// Encoding a payload then decoding it gives the parts back, whatever the
// text (within XML 1.0's characters).
func TestPayloadRoundTrip(t *testing.T) {
	svc := s3()
	o := op(svc, "CompleteMultipartUpload", http.MethodPost, "/{Bucket}/{Key+}", completeIn, completeIn)
	text := rapid.String().Filter(func(s string) bool {
		if !utf8.ValidString(s) {
			return false
		}
		for _, r := range s {
			if r != '\t' && r != '\n' && r != '\r' && (r < 0x20 || r > 0xD7FF && r < 0xE000 || r == 0xFFFE || r == 0xFFFF) {
				return false
			}
		}
		return true
	})
	rapid.Check(t, func(t *rapid.T) {
		parts := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) completedPart {
			var p completedPart
			if rapid.Bool().Draw(t, "etag?") {
				p.ETag = new(text.Draw(t, "etag"))
			}
			if rapid.Bool().Draw(t, "number?") {
				p.PartNumber = new(rapid.Int32().Draw(t, "number"))
			}
			return p
		}), 1, 5).Draw(t, "parts")
		resp, aerr := codec.Encode(nil, o, requestID, &complete{Parts: parts}, http.StatusOK)
		if aerr != nil {
			t.Fatal(aerr.AsError())
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		r := newRequest(http.MethodPost, "/b/k?uploadId=u", string(body), nil)
		got := &complete{}
		if aerr := codec.Decode(r, o, edgeBody{r}, got); aerr != nil {
			t.Fatalf("Decode(%s): %v", body, aerr.AsError())
		}
		if !reflect.DeepEqual(got.Parts, parts) {
			t.Fatalf("round trip of %s = %+v, want %+v", body, got.Parts, parts)
		}
	})
}

// Random bytes as an XML body never panic the decoder, and never make an
// internal error: a client's body decodes or is MalformedXML.
func TestRandomBodiesNeverPanic(t *testing.T) {
	o := op(s3(), "CompleteMultipartUpload", http.MethodPost, "/{Bucket}/{Key+}", completeIn, nil)
	pieces := []string{"<", ">", "/", "Part", "ETag", "PartNumber", "CompleteMultipartUpload", "1", "x", "&quot;", ` xmlns="a"`, `<?xml version="1.0"?>`}
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.OneOf(
			rapid.SliceOf(rapid.Byte()),
			rapid.Map(rapid.SliceOf(rapid.SampledFrom(pieces)), func(s []string) []byte { return []byte(strings.Join(s, "")) }),
		).Draw(t, "body")
		r := newRequest(http.MethodPost, "/b/k?uploadId=u", string(body), nil)
		if err := codec.Decode(r, o, edgeBody{r}, &complete{}); err != nil {
			if _, internal := err.Origin.(awserr.Internal); internal {
				t.Fatalf("body %q produced an internal error: %v", body, err.AsError())
			}
		}
	})
}
