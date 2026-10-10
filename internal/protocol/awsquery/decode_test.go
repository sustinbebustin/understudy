package awsquery_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// snsPublish is SNS's Publish input as the model shapes it: MessageAttributes
// is a map whose key and value carry xmlName Name and Value (sns.md 1).
func snsPublish() *smithy.Schema {
	attr := structure("com.amazonaws.sns#MessageAttributeValue", member("DataType", str), member("StringValue", str), member("BinaryValue", blob))
	return structure("com.amazonaws.sns#PublishInput",
		member("TopicArn", str), member("Message", str),
		member("MessageAttributes", mapOf("com.amazonaws.sns#MessageAttributeMap", renamed("key", "Name", false, str), renamed("value", "Value", false, attr))))
}

func snsService() *registry.Service {
	return newService("sns", "2010-03-31", snsNamespace,
		opSpec{name: "Publish", in: snsPublish()},
		opSpec{name: "CreateTopic", in: structure("com.amazonaws.sns#CreateTopicInput", member("Name", str), member("Attributes", stringMap("com.amazonaws.sns#TopicAttributesMap")))})
}

func decode(t *testing.T, svc *registry.Service, r *http.Request) (dynStruct, *awserr.Error) {
	t.Helper()
	claim := awsquery.Codec{}.Claim(r, newRegistry(t, svc))
	c, ok := claim.(awsquery.Claimed)
	if !ok {
		t.Fatalf("Claim = %#v, want Claimed", claim)
	}
	in := dynStruct{schema: c.Op.Input}
	aerr := awsquery.Codec{}.Decode(r, c.Op, &in)
	return in, aerr
}

// wire-json-query.md 7.1 and 7.5: Go v2's Publish body, keys sorted and
// spaces as '+', decodes the attribute map under its renamed key and value.
func TestDecodeSNSPublish(t *testing.T) {
	t.Parallel()
	body := "Action=Publish&Message=a+b%2Bc%26d%3D%C3%A9&MessageAttributes.entry.1.Name=aa" +
		"&MessageAttributes.entry.1.Value.DataType=String&MessageAttributes.entry.1.Value.StringValue=x" +
		"&TopicArn=arn%3Aaws%3Asns%3Aus-east-1%3A000000000000%3At&Version=2010-03-31"
	in, aerr := decode(t, snsService(), formPost("http://localhost/", body))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	attr := dynStruct{schema: snsPublish().Members[2].Target.Members[1].Target, fields: map[string]any{"DataType": "String", "StringValue": "x"}}
	want := map[string]any{
		"TopicArn": "arn:aws:sns:us-east-1:000000000000:t", "Message": "a b+c&d=é",
		"MessageAttributes": map[string]any{"aa": attr},
	}
	if !equalFields(in.fields, want) {
		t.Errorf("Decode = %#v\nwant %#v", in.fields, want)
	}
}

// sns.md 1: a map without xmlNames is entry.N.key and entry.N.value; an
// entry missing its value is skipped, and so is a non-index segment.
func TestDecodeDefaultMap(t *testing.T) {
	t.Parallel()
	body := "Action=CreateTopic&Version=2010-03-31&Name=t&Attributes.entry.2.key=FifoTopic&Attributes.entry.2.value=true" +
		"&Attributes.entry.1.key=DisplayName&Attributes.entry.1.value=d+n&Attributes.entry.3.key=Orphan&Attributes.entry.x.key=Nope"
	in, aerr := decode(t, snsService(), formPost("http://localhost/", body))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	want := map[string]any{"Name": "t", "Attributes": map[string]any{"DisplayName": "d n", "FifoTopic": "true"}}
	if !equalFields(in.fields, want) {
		t.Errorf("Decode = %#v\nwant %#v", in.fields, want)
	}
}

// wire-json-query.md 7.2: ListArg= is an empty list, members arrive in
// numeric index order, and an unknown parameter is ignored.
func TestDecodeListsAndEmptyListKey(t *testing.T) {
	t.Parallel()
	input := structure("test#ListsInput", member("ListArg", stringList("test#L")), member("Other", stringList("test#O")))
	svc := newService("lists", "2020-01-08", "", opSpec{name: "QueryLists", in: input})
	body := "Action=QueryLists&Version=2020-01-08&ListArg=&Other.member.10=j&Other.member.2=b&Other.member.1=a&Unknown=1"
	in, aerr := decode(t, svc, formPost("http://localhost/", body))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	want := map[string]any{"ListArg": []any{}, "Other": []any{"a", "b", "j"}}
	if !equalFields(in.fields, want) {
		t.Errorf("Decode = %#v\nwant %#v", in.fields, want)
	}
}

// A value that does not fit its member is the service's malformed-request
// answer, SerializationException by default, naming the parameter.
func TestDecodeMalformedValues(t *testing.T) {
	t.Parallel()
	pin := &smithy.Member{Name: "Pin", Target: integer, Sensitive: true}
	input := structure("test#ScalarsInput", member("Count", integer), member("Flag", boolean), member("When", tstamp), member("Data", blob), pin,
		member("Counts", listOf("test#Counts", member("member", integer))))
	svc := newService("scalars", "2020-01-08", "", opSpec{name: "Scalars", in: input})
	cases := map[string]string{
		"Count=abc":        "Value abc for parameter Count is invalid. Reason: Must be an integer from -2147483648 to 2147483647.",
		"Count=2147483648": "Value 2147483648 for parameter Count is invalid. Reason: Must be an integer from -2147483648 to 2147483647.",
		"Flag=yes":         "Value yes for parameter Flag is invalid. Reason: Must be true or false.",
		"When=yesterday":   "Value yesterday for parameter When is invalid. Reason: Must be a date-time timestamp.",
		"Data=%21%21":      "Value !! for parameter Data is invalid. Reason: Must be base64-encoded.",
		// An oversized value is echoed only as its first 64 bytes.
		"Count=" + strings.Repeat("x", 1<<20): "Value " + strings.Repeat("x", 64) + "... for parameter Count is invalid. Reason: Must be an integer from -2147483648 to 2147483647.",
		// The cut does not split a character: é is two bytes, the 64th
		// and 65th.
		"Data=" + strings.Repeat("x", 63) + "%C3%A9": "Value " + strings.Repeat("x", 63) + "... for parameter Data is invalid. Reason: Must be base64-encoded.",
		// A sensitive member's value is not echoed at all.
		"Pin=secret": "The value for parameter Pin is invalid. Reason: Must be an integer from -2147483648 to 2147483647.",
		// An index segment of any length is read, but the key is echoed
		// only as its first 64 bytes.
		"Counts.member." + strings.Repeat("0", 100000) + "1=abc": "Value abc for parameter Counts.member." + strings.Repeat("0", 50) +
			"... is invalid. Reason: Must be an integer from -2147483648 to 2147483647.",
	}
	for param, message := range cases {
		_, aerr := decode(t, svc, formPost("http://localhost/", "Action=Scalars&Version=2020-01-08&"+param))
		if aerr == nil || aerr.ID.Shape != awserr.SerializationException().Shape || aerr.Message != message {
			t.Errorf("%s: Decode = %+v, want SerializationException %q", param, aerr, message)
		}
	}
	svc.MalformedRequestError = awserr.AccessDeniedException()
	_, aerr := decode(t, svc, formPost("http://localhost/", "Action=Scalars&Version=2020-01-08&Count=x"))
	if aerr == nil || aerr.ID.Shape != awserr.AccessDeniedException().Shape {
		t.Errorf("Decode with an overlay malformedRequestError = %+v", aerr)
	}
}

// Timestamps accept fractional seconds and offsets (wire-json-query.md
// 7.3), and booleans any case.
func TestDecodeLenientScalars(t *testing.T) {
	t.Parallel()
	input := structure("test#ScalarsInput", member("When", tstamp), member("Flag", boolean))
	svc := newService("scalars", "2020-01-08", "", opSpec{name: "Scalars", in: input})
	in, aerr := decode(t, svc, formPost("http://localhost/", "Action=Scalars&Version=2020-01-08&When=2014-04-29T20%3A30%3A38.1234%2B02%3A00&Flag=TRUE"))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	when, ok := in.fields["When"].(interface{ UnixMilli() int64 })
	if !ok || when.UnixMilli() != 1398796238123 || in.fields["Flag"] != true {
		t.Errorf("Decode = %#v", in.fields)
	}
}

// A union needs exactly one member.
func TestDecodeUnionMemberCount(t *testing.T) {
	t.Parallel()
	choice := withMembers(&smithy.Schema{ID: "test#Choice", Type: smithy.TypeUnion}, member("A", str), member("B", str))
	svc := newService("unions", "2020-01-08", "", opSpec{name: "Choose", in: structure("test#ChooseInput", member("Choice", choice))})
	for body, want := range map[string]string{
		"Choice.A=1&Choice.B=2": "union Choice has more than one member set: A and B",
		"Choice.C=1":            "union Choice has no member set; exactly one member must be set",
	} {
		_, aerr := decode(t, svc, formPost("http://localhost/", "Action=Choose&Version=2020-01-08&"+body))
		if aerr == nil || aerr.Message != want {
			t.Errorf("%s: Decode = %+v, want %q", body, aerr, want)
		}
	}
}

// A form Parse refuses, past Claim because its Action and Version read
// cleanly, is the service's malformed-request answer, a 400, never an
// InternalFailure. The message names the parameter by its raw key and
// never echoes the value: a sensitive member's password with a literal %,
// sent unescaped through curl -d, must stay out of the response and the
// journal.
func TestDecodeRefusedForm(t *testing.T) {
	t.Parallel()
	pin := &smithy.Member{Name: "Pin", Target: str, Sensitive: true}
	svc := newService("sns", "2010-03-31", snsNamespace, opSpec{name: "Publish", in: structure("com.amazonaws.sns#PublishInput", member("Message", str), pin)})
	const badEscape = " at byte 34 has a % not followed by two hexadecimal digits; percent-encode a literal % as %25."
	// Every pair follows the 33 bytes of Action and Version and an '&'.
	cases := map[string]struct{ extra, want string }{
		"bad escape":       {"&Message=%zz", "The parameter Message" + badEscape},
		"bad escape key":   {"&Mess%zzage=x", "The parameter Mess%zzage" + badEscape},
		"sensitive value":  {"&Pin=hunter2%zz", "The parameter Pin" + badEscape},
		"empty key":        {"&=%zz", "The parameter with an empty name" + badEscape},
		"key too deep":     {"&Message" + strings.Repeat(".a", form.MaxKeySegments) + "=x", "The parameter Message" + strings.Repeat(".a", 28) + ".... at byte 34 has more than 32 '.'-separated segments."},
		"long value cut":   {"&Message=" + strings.Repeat("v", 1<<20) + "%zz", "The parameter Message" + badEscape},
		"long raw key cut": {"&" + strings.Repeat("k", 1<<20) + "%zz=x", "The parameter " + strings.Repeat("k", 64) + "..." + badEscape},
	}
	for name, tc := range cases {
		_, aerr := decode(t, svc, formPost("http://localhost/", "Action=Publish&Version=2010-03-31"+tc.extra))
		if aerr == nil || aerr.ID.Shape != awserr.SerializationException().Shape || aerr.Message != tc.want {
			t.Errorf("%s: Decode = %+v, want SerializationException %q", name, aerr, tc.want)
			continue
		}
		if strings.Contains(aerr.Message, "hunter2") {
			t.Errorf("%s: message %q echoes the sensitive value", name, aerr.Message)
		}
		if status := (awsquery.Codec{}).EncodeError(svc, requestID, aerr).Status; status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
}

// cloudWatchService is CloudWatch's PutMetricData, the one phase 1
// operation with @requestCompression (wire-json-query.md 3.2).
func cloudWatchService() *registry.Service {
	datum := structure("com.amazonaws.cloudwatch#MetricDatum", member("MetricName", str), member("Value", double))
	input := structure("com.amazonaws.cloudwatch#PutMetricDataInput",
		member("Namespace", str), member("MetricData", listOf("com.amazonaws.cloudwatch#MetricData", member("member", datum))))
	return newService("cloudwatch", "2010-08-01", cwNamespace, opSpec{name: "PutMetricData", in: input})
}

func gzipped(t *testing.T, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodedPost(body []byte, encoding string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://localhost/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	r.Header.Set("Content-Encoding", encoding)
	return r
}

// wire-json-query.md 3.2: botocore gzips a Query body of 10240 bytes or
// more and says so in Content-Encoding. Claim finds the Action inside it,
// and Decode reads the decompressed form.
func TestDecodeGzipBody(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("Action=PutMetricData&Version=2010-08-01&Namespace=app")
	const n = 400
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "&MetricData.member.%d.MetricName=latency&MetricData.member.%d.Value=%d", i, i, i)
	}
	if b.Len() < 10240 {
		t.Fatalf("form is %d bytes, below botocore's compression threshold", b.Len())
	}
	in, aerr := decode(t, cloudWatchService(), encodedPost(gzipped(t, b.String()), "gzip"))
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	data, ok := in.fields["MetricData"].([]any)
	if !ok || len(data) != n || in.fields["Namespace"] != "app" {
		t.Fatalf("Decode = Namespace %v, %d MetricData", in.fields["Namespace"], len(data))
	}
	last, ok := data[n-1].(dynStruct)
	if !ok || last.fields["MetricName"] != "latency" || last.fields["Value"] != float64(n) {
		t.Errorf("MetricData.member.%d = %#v", n, data[n-1])
	}
}

// Claim puts a compressed body back as it was sent.
func TestClaimPutsTheCompressedBodyBack(t *testing.T) {
	t.Parallel()
	body := gzipped(t, "Action=PutMetricData&Version=2010-08-01&Namespace=app")
	r := encodedPost(body, "gzip")
	if c, ok := (awsquery.Codec{}).Claim(r, newRegistry(t, cloudWatchService())).(awsquery.Claimed); !ok {
		t.Fatalf("Claim = %#v, want Claimed", c)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("body after Claim = %q, %v; want the gzip bytes sent", got, err)
	}
}

// A body whose Content-Encoding cannot be undone is declined by Claim,
// since its Action cannot be read, and is the service's malformed-request
// answer from Decode. A gzip body that inflates past the operation's cap is
// refused by Decode; Claim's cap is the larger DefaultMaxRequestBytes, so
// it still claims this one.
func TestContentEncodingRefused(t *testing.T) {
	t.Parallel()
	svc := cloudWatchService()
	op := operation(t, svc, "PutMetricData")
	op.MaxRequestBytes = 1024
	prefix := "Action=PutMetricData&Version=2010-08-01&Namespace="
	cases := []struct {
		name     string
		body     []byte
		encoding string
		declined bool
		want     string
	}{
		{"corrupt gzip", []byte(prefix + "app"), "gzip", true, "the request body is not valid gzip data: "},
		{"truncated gzip", gzipped(t, prefix+"app")[:20], "gzip", true, "the request body is not valid gzip data: "},
		{"unsupported encoding", []byte(prefix + "app"), "br", true, "Content-Encoding br is not supported; send the body uncompressed or gzip-compressed"},
		{"gzip bomb", gzipped(t, prefix+strings.Repeat("a", 1<<20)), "gzip", false, "the decompressed request body exceeds 1024 bytes"},
	}
	for _, tc := range cases {
		claim := awsquery.Codec{}.Claim(encodedPost(tc.body, tc.encoding), newRegistry(t, svc))
		if c, ok := claim.(awsquery.Declined); ok != tc.declined || (ok && !strings.Contains(c.Reason, tc.want)) {
			t.Errorf("%s: Claim = %#v, want declined %t for %q", tc.name, claim, tc.declined, tc.want)
		}
		in := dynStruct{schema: op.Input}
		aerr := awsquery.Codec{}.Decode(encodedPost(tc.body, tc.encoding), op, &in)
		if aerr == nil || aerr.ID.Shape != awserr.SerializationException().Shape || !strings.HasPrefix(aerr.Message, tc.want) {
			t.Errorf("%s: Decode = %+v, want SerializationException %q", tc.name, aerr, tc.want)
		}
	}
}

// ARCHITECTURE.md 4.1 rule 7: the form media type with any parameters, or a
// GET with Action; the service from (Action, Version).
func TestClaim(t *testing.T) {
	t.Parallel()
	reg := newRegistry(t, snsService())
	get := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost/?Action=Publish&Version=2010-03-31&TopicArn=t", http.NoBody)
	json := formPost("http://localhost/", "{}")
	json.Header.Set("Content-Type", "application/x-amz-json-1.0")
	cases := []struct {
		name string
		r    *http.Request
		want string
	}{
		{"form POST", formPost("http://localhost/", "Version=2010-03-31&Action=Publish"), "Claimed Publish"},
		{"GET", get, "Claimed Publish"},
		{"unknown action", formPost("http://localhost/", "Action=FooBar&Version=2010-03-31"), "UnknownOperation FooBar"},
		{"unknown version", formPost("http://localhost/", "Action=Publish&Version=1999-01-01"), "Declined"},
		{"no action", formPost("http://localhost/", "Version=2010-03-31"), "Declined"},
		{"bad escape elsewhere", formPost("http://localhost/", "Action=Publish&Version=2010-03-31&x=%zz"), "Claimed Publish"},
		{"bad escape in Action", formPost("http://localhost/", "Action=Pub%zz&Version=2010-03-31"), "Declined"},
		{"bad escape in Version", formPost("http://localhost/", "Action=Publish&Version=%zz"), "Declined"},
		{"JSON body", json, "Declined"},
	}
	for _, tc := range cases {
		var got string
		switch c := (awsquery.Codec{}).Claim(tc.r, reg).(type) {
		case awsquery.Claimed:
			got = "Claimed " + c.Op.ID.Name
		case awsquery.UnknownOperation:
			got = "UnknownOperation " + c.Operation
		case awsquery.Declined:
			got = "Declined"
			if c.Reason == "" {
				t.Errorf("%s: Declined without a reason", tc.name)
			}
		}
		if got != tc.want {
			t.Errorf("%s: Claim = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Claim reads a form body to find Action; Decode then reads it again.
func TestClaimPutsTheBodyBack(t *testing.T) {
	t.Parallel()
	body := "Action=Publish&Version=2010-03-31&Message=m"
	r := formPost("http://localhost/", body)
	awsquery.Codec{}.Claim(r, newRegistry(t, snsService()))
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != body {
		t.Errorf("body after Claim = %q, %v", got, err)
	}
	if err := r.Body.Close(); err != nil {
		t.Error(err)
	}
}

func TestMaxRequestBytes(t *testing.T) {
	t.Parallel()
	if got := awsquery.MaxRequestBytes(&registry.Operation{}); got != awsquery.DefaultMaxRequestBytes {
		t.Errorf("default cap = %d", got)
	}
	if got := awsquery.MaxRequestBytes(&registry.Operation{MaxRequestBytes: 8 << 20}); got != 8<<20 {
		t.Errorf("overlay cap = %d", got)
	}
	if (awsquery.Codec{}).Protocol() != "awsquery" {
		t.Error("Protocol is not awsquery")
	}
}

// equalFields compares decoded fields, unwrapping nested dynStructs.
func equalFields(got, want map[string]any) bool {
	return reflect.DeepEqual(normalize(got), normalize(want))
}

func normalize(v any) any {
	switch x := v.(type) {
	case dynStruct:
		return normalize(x.fields)
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []byte:
		return "blob:" + string(x)
	case time.Time:
		return x.UnixMilli()
	case float64:
		if math.IsNaN(x) {
			return "NaN"
		}
		return x
	default:
		return v
	}
}
