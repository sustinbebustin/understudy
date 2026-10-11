package restbind_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

var putOp = operation("/{Bucket}/{Key+}", putInput, putOutput)

// request builds the request an SDK sends for v: labels escaped (a
// greedy label keeps its slashes), query lists as repeated keys, header
// timestamps as http-date, lists comma-joined with quoting, metadata as
// prefix headers.
func request(v *put, header http.Header) *http.Request {
	segs := strings.Split(v.Key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	q := url.Values{}
	for _, t := range v.Tags {
		q.Add("tag", t)
	}
	if v.Limit != nil {
		q.Set("max-keys", strconv.Itoa(int(*v.Limit)))
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, "http://localhost/"+url.PathEscape(v.Bucket)+"/"+strings.Join(segs, "/")+"?"+q.Encode(), http.NoBody)
	maps.Copy(r.Header, header)
	return r
}

// headers renders v's header members as a client would.
func headers(v *put) http.Header {
	h := http.Header{}
	if v.When != nil {
		h.Set("X-When", v.When.UTC().Format(http.TimeFormat))
	}
	if len(v.Stamps) > 0 {
		ts := make([]string, 0, len(v.Stamps))
		for _, t := range v.Stamps {
			ts = append(ts, t.UTC().Format(http.TimeFormat))
		}
		h.Set("X-Stamps", restbind.JoinHeaderList(ts, false))
	}
	if len(v.Names) > 0 {
		h.Set("X-Names", restbind.JoinHeaderList(v.Names, true))
	}
	if v.Flag != nil {
		h.Set("X-Flag", strconv.FormatBool(*v.Flag))
	}
	for k, val := range v.Meta {
		h.Set("x-amz-meta-"+k, val)
	}
	return h
}

func decode(r *http.Request, op *registry.Operation, b smithy.Body) (*put, error) {
	if b == nil {
		b = &body{}
	}
	got := &put{}
	return got, restbind.Decode(r, op, b, got, nil)
}

func drawPut(t *rapid.T) *put {
	v := &put{
		Bucket: rapid.StringN(1, 12, -1).Draw(t, "bucket"),
		Key:    strings.Join(rapid.SliceOfN(rapid.StringN(0, 6, -1), 1, 4).Filter(func(s []string) bool { return strings.Join(s, "/") != "" }).Draw(t, "key"), "/"),
	}
	if tags := rapid.SliceOfN(rapid.String(), 0, 3).Draw(t, "tags"); len(tags) > 0 {
		v.Tags = tags
	}
	if rapid.Bool().Draw(t, "limit?") {
		v.Limit = new(rapid.Int32().Draw(t, "limit"))
	}
	stamp := rapid.Custom(func(t *rapid.T) time.Time {
		return time.Unix(rapid.Int64Range(0, 7e9).Draw(t, "sec"), 0).UTC()
	})
	if rapid.Bool().Draw(t, "when?") {
		v.When = new(stamp.Draw(t, "when"))
	}
	if s := rapid.SliceOfN(stamp, 0, 3).Draw(t, "stamps"); len(s) > 0 {
		v.Stamps = s
	}
	if n := rapid.SliceOfN(rapid.String(), 0, 4).Draw(t, "names"); len(n) > 0 {
		v.Names = n
	}
	if rapid.Bool().Draw(t, "flag?") {
		v.Flag = new(rapid.Bool().Draw(t, "flag"))
	}
	if m := rapid.MapOfN(rapid.StringMatching(`[a-z0-9][a-z0-9-]{0,8}`), rapid.String(), 0, 3).Draw(t, "meta"); len(m) > 0 {
		v.Meta = m
	}
	return v
}

// Binding a request an SDK built from a value gives the value back, for
// arbitrary labels (greedy ones with slashes, dot segments and literal %),
// query lists, header timestamps and lists, and prefix headers.
func TestBindRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := drawPut(t)
		got, err := decode(request(want, headers(want)), putOp, nil)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Decode = %+v\nwant     %+v", got, want)
		}
	})
}

// Encoding an output's header members and binding them back from a
// request gives the same members: the response side writes what the
// request side reads.
func TestEncodeThenBindRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		want := drawPut(t)
		want.Bucket, want.Key, want.Tags, want.Limit = "b", "k", nil, nil
		enc, err := restbind.Encode(putOp, want, http.StatusOK, restbind.HasResponseDocument, nil)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		got, err := decode(request(want, enc.Header), putOp, nil)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip = %+v\nwant         %+v", got, want)
		}
	})
}

// serializeFunc is an output whose Serialize is f.
type serializeFunc func(smithy.Serializer)

func (f serializeFunc) Serialize(s smithy.Serializer) { f(s) }

// An httpHeader member wins over a prefix-header entry for the same
// header, whichever the model declares, and so writes, first (wire-rest.md
// 1.4); other prefix entries still go out.
func TestHeaderMemberWinsOverPrefixHeaders(t *testing.T) {
	foo := &smithy.Member{Name: "Foo", Target: stringSchema, HTTP: smithy.HTTPHeader{Name: "X-Foo"}}
	prefixed := &smithy.Member{Name: "Prefixed", Target: stringMap, HTTP: smithy.HTTPPrefixHeaders{Prefix: "x-"}}
	write := map[*smithy.Member]func(smithy.Serializer){
		foo: func(s smithy.Serializer) { s.WriteString(foo, "specific") },
		prefixed: func(s smithy.Serializer) {
			s.WriteMap(prefixed, 2)
			s.WriteMapKey("bar")
			s.WriteString(stringMap.Members[1], "other")
			s.WriteMapKey("foo")
			s.WriteString(stringMap.Members[1], "prefix")
			s.CloseMap()
		},
	}
	for name, members := range map[string][]*smithy.Member{
		"header member first": {foo, prefixed},
		"prefix map first":    {prefixed, foo},
	} {
		out := &smithy.Schema{ID: "test#Out", Type: smithy.TypeStructure, Members: members}
		enc, err := restbind.Encode(operation("/", nil, out), serializeFunc(func(s smithy.Serializer) {
			s.WriteStruct(nil, out)
			for _, m := range members {
				write[m](s)
			}
			s.CloseStruct()
		}), http.StatusOK, restbind.HasResponseDocument, nil)
		if err != nil || enc.Header.Get("X-Foo") != "specific" || enc.Header.Get("X-Bar") != "other" {
			t.Errorf("%s: Encode = %v, %v; want X-Foo: specific and X-Bar: other", name, enc.Header, err)
		}
	}
}

// A header-bound list that is set but empty still sends its header, with
// an empty value; an unset one sends none (the Smithy cases
// RestJsonNullAndEmptyHeaders and NullAndEmptyHeaders).
func TestEmptyHeaderListSendsEmptyHeader(t *testing.T) {
	out := &smithy.Schema{ID: "test#Out", Type: smithy.TypeStructure, Members: []*smithy.Member{mNames}}
	enc, err := restbind.Encode(operation("/", nil, out), serializeFunc(func(s smithy.Serializer) {
		s.WriteStruct(nil, out)
		s.WriteList(mNames, 0)
		s.CloseList()
		s.CloseStruct()
	}), http.StatusOK, restbind.HasResponseDocument, nil)
	if got, ok := enc.Header["X-Names"]; err != nil || !ok || !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("Encode = %v, %v; want X-Names with one empty value", enc.Header, err)
	}
}

// A status that allows no body sends none, whatever the output holds, and
// the codec adds no Content-Type or Content-Length for it.
func TestFinishSendsNoBodyForBodilessStatus(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		enc := restbind.Encoded{Status: status, Header: http.Header{}, Body: restbind.DocumentBody{Bytes: []byte("{}")}}
		r, err := enc.Finish("application/json")
		if err != nil || r != http.NoBody || len(enc.Header) != 0 {
			t.Errorf("%d: Finish = %v, %v, header %v", status, r, err, enc.Header)
		}
	}
}

// Labels are split on the escaped path and decoded once: %2F inside a
// segment is a slash in the value, %252F stays %2F, dot segments and a
// greedy label's trailing slash are kept (wire-rest.md 1.2, 3.3; the
// Smithy S3 cases S3PreservesLeadingDotSegmentInUriLabel and
// S3PreservesEmbeddedDotSegmentInUriLabel).
func TestLabels(t *testing.T) {
	greedy := operation("/{Bucket}/{Key+}", nil, nil).HTTP
	lambda := operation("/2015-03-31/functions/{FunctionName}/invocations", nil, nil).HTTP
	tests := []struct {
		pattern []registry.Segment
		path    string
		want    map[string]string
	}{
		{greedy.Path, "/mybucket/../key.txt", map[string]string{"Bucket": "mybucket", "Key": "../key.txt"}},
		{greedy.Path, "/mybucket/foo/../key.txt", map[string]string{"Bucket": "mybucket", "Key": "foo/../key.txt"}},
		{greedy.Path, "/b/a%2Fb/c", map[string]string{"Bucket": "b", "Key": "a/b/c"}},
		{greedy.Path, "/b/a%252F", map[string]string{"Bucket": "b", "Key": "a%2F"}},
		{greedy.Path, "/b/dir/", map[string]string{"Bucket": "b", "Key": "dir/"}},
		{greedy.Path, "/b//lead", map[string]string{"Bucket": "b", "Key": "/lead"}},
		{greedy.Path, "/b/my%20key+1.txt", map[string]string{"Bucket": "b", "Key": "my key+1.txt"}},
		{lambda.Path, "/2015-03-31/functions/my%3Afn/invocations", map[string]string{"FunctionName": "my:fn"}},
	}
	for _, tt := range tests {
		got, err := restbind.Labels(tt.pattern, tt.path)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Labels(%q) = %v, %v; want %v", tt.path, got, err, tt.want)
		}
	}
	var bind *restbind.BindError
	if _, err := restbind.Labels(greedy.Path, "/b/%zz"); !errors.As(err, &bind) || bind.Location != restbind.InLabel {
		t.Errorf("an invalid escape: %v, want a label *BindError", err)
	}
}

// Header lists parse in the spec tests' quoted form and boto3's bare
// form; http-date lists pair their commas back up (wire-rest.md 1.4).
func TestHeaderLists(t *testing.T) {
	if got := restbind.SplitHeaderList(`"b,c", "\"def\"", a`); !reflect.DeepEqual(got, []string{"b,c", `"def"`, "a"}) {
		t.Errorf("quoted list = %q", got)
	}
	if got := restbind.SplitHeaderList("a,b ,c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("bare list = %q", got)
	}
	r := request(&put{Bucket: "b", Key: "k"}, http.Header{
		"X-Stamps": {"Mon, 16 Dec 2019 23:48:18 GMT, Mon, 16 Dec 2019 23:48:18 GMT"},
		"X-Names":  {"a, b", "c"},
	})
	got, err := decode(r, putOp, nil)
	stamp := time.Date(2019, 12, 16, 23, 48, 18, 0, time.UTC)
	if err != nil || !reflect.DeepEqual(got.Stamps, []time.Time{stamp, stamp}) || !reflect.DeepEqual(got.Names, []string{"a", "b", "c"}) {
		t.Errorf("Decode = %+v, %v", got, err)
	}
}

// A header value that does not parse as its member's type is a
// *BindError naming the header, which codecs answer as a malformed
// request.
func TestBindErrors(t *testing.T) {
	r := request(&put{Bucket: "b", Key: "k"}, http.Header{"X-Flag": {"yes"}})
	_, err := decode(r, putOp, nil)
	var bind *restbind.BindError
	if !errors.As(err, &bind) || bind.Location != restbind.InHeader || bind.Name != "X-Flag" || bind.Value != "yes" {
		t.Errorf("Decode error = %v, want a header *BindError for X-Flag", err)
	}
}

// A query pair with an invalid escape is a query *BindError, not a
// parameter dropped as absent; pairs split on '&' only, so a ';' stays in
// its value.
func TestQueryEscapes(t *testing.T) {
	r := request(&put{Bucket: "b", Key: "k"}, nil)
	r.URL.RawQuery = "max-keys=%zz"
	_, err := decode(r, putOp, nil)
	var bind *restbind.BindError
	if !errors.As(err, &bind) || bind.Location != restbind.InQuery || bind.Name != "max-keys" || bind.Value != "%zz" {
		t.Errorf("max-keys=%%zz: %v, want a query *BindError", err)
	}
	r.URL.RawQuery = "tag=a;b&tag=c+d%2B&max-keys=1"
	got, err := decode(r, putOp, nil)
	if err != nil || !reflect.DeepEqual(got.Tags, []string{"a;b", "c d+"}) || got.Limit == nil || *got.Limit != 1 {
		t.Errorf("Decode = %+v, %v", got, err)
	}
}

// Arbitrary text in every head location never panics: it binds or fails
// with an error.
func TestRandomHeadTextNeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := rapid.String()
		h := http.Header{}
		for _, name := range []string{"X-When", "X-Stamps", "X-Names", "X-Flag", "X-Amz-Meta-A"} {
			h[name] = []string{text.Draw(t, name)}
		}
		r := request(&put{Bucket: "b", Key: "k"}, h)
		r.URL.RawQuery = "max-keys=" + url.QueryEscape(text.Draw(t, "limit")) + "&tag=" + url.QueryEscape(text.Draw(t, "tag"))
		_, _ = decode(r, putOp, nil) //nolint:errcheck // only a panic fails the property
	})
}

// A blob payload binds the raw body; the output's httpResponseCode member
// sets the status and its blob payload becomes the body.
func TestBlobPayload(t *testing.T) {
	r := request(&put{Bucket: "b", Key: "k"}, nil)
	got, err := decode(r, putOp, &body{data: []byte("raw \x00 bytes")})
	if err != nil || string(got.Data) != "raw \x00 bytes" {
		t.Errorf("Decode = %q, %v", got.Data, err)
	}
	// Whitespace is no document but it is a blob payload's bytes; only an
	// empty body leaves the payload unset.
	got, err = decode(r, putOp, &body{data: []byte(" \n")})
	if err != nil || string(got.Data) != " \n" {
		t.Errorf("whitespace payload = %q, %v", got.Data, err)
	}
	got, err = decode(r, putOp, &body{})
	if err != nil || got.Data != nil {
		t.Errorf("empty payload = %q, %v", got.Data, err)
	}
	enc, err := restbind.Encode(putOp, &put{Status: 202, Data: []byte("out")}, http.StatusOK, restbind.HasResponseDocument, nil)
	if err != nil || enc.Status != 202 || !reflect.DeepEqual(enc.Body, restbind.BlobBody{Bytes: []byte("out")}) {
		t.Errorf("Encode = %+v, %v", enc, err)
	}
	enc, err = restbind.Encode(putOp, &put{}, http.StatusNoContent, restbind.HasResponseDocument, nil)
	if err != nil || enc.Status != http.StatusNoContent || !reflect.DeepEqual(enc.Body, restbind.NoBody{}) {
		t.Errorf("Encode of an empty output = %+v, %v", enc, err)
	}
}

// A trailer-bound member the head does not carry arrives after the body:
// read before EOF it is not yet available; after EOF it is the trailer
// (ARCHITECTURE.md 6.1, s3.md 6.3).
func TestTrailerBoundMemberResolvesAtEOF(t *testing.T) {
	op := operation("/{Bucket}/{Key+}", streamInput, nil)
	r := request(&put{Bucket: "b", Key: "k"}, http.Header{"X-Amz-Trailer": {"x-amz-checksum-crc32"}})
	got, err := decode(r, op, &body{data: []byte("hello"), trailers: map[string]string{"X-Amz-Checksum-Crc32": "NhCmhg=="}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Checksum != nil {
		t.Errorf("the trailer-bound member was bound from the head: %q", *got.Checksum)
	}
	b, ok := got.Body.(*restbind.Body)
	if !ok {
		t.Fatalf("the payload is a %T, want *restbind.Body", got.Body)
	}
	var pending *restbind.PendingError
	if _, _, err := b.Deferred("x-amz-checksum-crc32"); !errors.As(err, &pending) {
		t.Errorf("Deferred before EOF = %v, want *PendingError", err)
	}
	if _, ok := got.Body.Trailer("x-amz-checksum-crc32"); ok {
		t.Error("smithy.Body.Trailer before EOF reported a value")
	}
	rc, err := b.Open()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(rc); err != nil || !bytes.Equal(data, []byte("hello")) {
		t.Fatalf("body = %q, %v", data, err)
	}
	if v, sent, err := b.Deferred("x-amz-checksum-crc32"); err != nil || !sent || v != "NhCmhg==" {
		t.Errorf("Deferred after EOF = %q, %v, %v", v, sent, err)
	}
	if v, ok := got.Body.Trailer("x-amz-checksum-crc32"); !ok || v != "NhCmhg==" {
		t.Errorf("smithy.Body.Trailer after EOF = %q, %v", v, ok)
	}

	// Sent as a header instead, the member binds from the head.
	r = request(&put{Bucket: "b", Key: "k"}, http.Header{"X-Amz-Checksum-Crc32": {"NhCmhg=="}})
	got, err = decode(r, op, &body{data: []byte("hello")})
	if err != nil || got.Checksum == nil || *got.Checksum != "NhCmhg==" {
		t.Errorf("header checksum = %v, %v", got.Checksum, err)
	}
}

func TestTimestampText(t *testing.T) {
	at := time.Date(2019, 12, 16, 23, 48, 18, 500e6, time.UTC)
	for f, want := range map[smithy.TimestampFormat]string{
		smithy.DateTime:     "2019-12-16T23:48:18.5Z",
		smithy.HTTPDate:     "Mon, 16 Dec 2019 23:48:18 GMT",
		smithy.EpochSeconds: "1576540098.5",
	} {
		if got := restbind.FormatTimestamp(at, f); got != want {
			t.Errorf("FormatTimestamp(%v) = %q, want %q", f, got, want)
		}
	}
	if got := restbind.FormatTimestamp(time.UnixMilli(-1500), smithy.EpochSeconds); got != "-1.5" {
		t.Errorf("pre-1970 epoch seconds = %q", got)
	}
	rapid.Check(t, func(t *rapid.T) {
		at := time.UnixMilli(rapid.Int64Range(-1e13, 1e13).Draw(t, "ms")).UTC()
		for _, f := range []smithy.TimestampFormat{smithy.DateTime, smithy.EpochSeconds} {
			got, err := restbind.ParseTimestamp(restbind.FormatTimestamp(at, f), f)
			if err != nil || !got.Equal(at) {
				t.Fatalf("format %v: %v round trips to %v, %v", f, at, got, err)
			}
		}
	})
	for _, bad := range []string{"1/2", "0x10", "1e", "", "NaN", "1e999999", "1e-999999", "1e99999999999999999999"} {
		if _, err := restbind.ParseTimestamp(bad, smithy.EpochSeconds); err == nil {
			t.Errorf("epoch seconds %q parsed", bad)
		}
	}
	if got, err := restbind.ParseTimestamp("1.5e3", smithy.EpochSeconds); err != nil || !got.Equal(time.UnixMilli(1500000)) {
		t.Errorf("1.5e3 = %v, %v", got, err)
	}
}

// An epoch-seconds exponent beyond 999 is refused before big.Rat expands
// it, so a body full of huge exponents costs no more than its size. 1e-1000
// is a value big.Rat would accept as the epoch, so its rejection shows the
// bound applies ahead of the expansion; 1e-999 and 1e-40, inside it, are
// the epoch.
func TestEpochExponentBound(t *testing.T) {
	for _, bad := range []string{"1e1000", "1e-1000", "1E+1000"} {
		if _, err := restbind.ParseTimestamp(bad, smithy.EpochSeconds); err == nil {
			t.Errorf("epoch seconds %q parsed", bad)
		}
	}
	for _, tiny := range []string{"1e-999", "1e-40"} {
		if got, err := restbind.ParseTimestamp(tiny, smithy.EpochSeconds); err != nil || !got.Equal(time.Unix(0, 0)) {
			t.Errorf("epoch seconds %q = %v, %v; want the epoch", tiny, got, err)
		}
	}
}
