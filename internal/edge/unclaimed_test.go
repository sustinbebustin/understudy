package edge_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
)

// queryFault is the awsQuery ErrorResponse, its namespace and Detail kept.
type queryFault struct {
	XMLName xml.Name `xml:"ErrorResponse"`
	Error   struct {
		Type    string  `xml:"Type"`
		Code    string  `xml:"Code"`
		Message string  `xml:"Message"`
		Detail  *string `xml:"Detail"`
	} `xml:"Error"`
	RequestID string `xml:"RequestId"`
}

// An unknown Action is answered per service, as each was recorded: SNS
// and SES v1 in Coral's AWSFault form (sns/R11/05-sns-unknown-action.json,
// ses/SES-24/01-v1-unknown.json; ses.md 3.1), SQS in its own namespace
// with Detail (sqs/R11/05-sqs-unknown-action.json). STS is unrecorded and
// keeps the form it had.
func TestUnknownQueryActionsAreAnsweredInEachServicesForm(t *testing.T) {
	const awsFault = "http://webservices.amazon.com/AWSFault/2005-15-09"
	cases := []struct {
		name, body, signedFor, namespace, message string
		detail                                    bool
	}{
		{"sns", "Action=NoSuchAction&Version=2010-03-31", "sns", awsFault, "Could not find operation NoSuchAction for version 2010-03-31", false},
		{"ses", "Action=FooBar&Version=2010-12-01", "ses", awsFault, "Could not find operation FooBar for version 2010-12-01", false},
		{"sqs", "Action=NoSuchAction&Version=2012-11-05", "sqs", "http://queue.amazonaws.com/doc/2012-11-05/", "The action NoSuchAction is not valid for this endpoint.", true},
		{"sts", "Action=NoSuchAction&Version=2011-06-15", "sts", "https://sts.amazonaws.com/doc/2011-06-15/", "The action NoSuchAction is not valid for this endpoint.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, server.Codecs(), snsapi.Service(), sesapi.Service(), sesv2api.Service(), stsapi.Service())
			rec := httptest.NewRecorder()
			header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, c.body, c.signedFor))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/xml" {
				t.Errorf("Content-Type = %q, want text/xml", got)
			}
			var got queryFault
			if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body %s: %v", rec.Body, err)
			}
			if got.XMLName.Space != c.namespace {
				t.Errorf("xmlns = %q, want %q", got.XMLName.Space, c.namespace)
			}
			if got.Error.Type != "Sender" || got.Error.Code != "InvalidAction" || got.Error.Message != c.message {
				t.Errorf("Error = %s %s %q, want Sender InvalidAction %q", got.Error.Type, got.Error.Code, got.Error.Message, c.message)
			}
			if (got.Error.Detail != nil) != c.detail {
				t.Errorf("Detail present = %t, want %t", got.Error.Detail != nil, c.detail)
			}
			if got.RequestID == "" {
				t.Error("RequestId is empty")
			}
		})
	}
}

// A REST request signed for ses that no sesv2 route matches is Coral's
// 404: the body <UnknownOperationException/> and a newline, with no
// Content-Type and no x-amzn-ErrorType (ses/SES-24/02-v2-unknown.json;
// ses.md 3.2).
func TestSESv2UnknownPathIsCoralsNotFound(t *testing.T) {
	f := newFront(t, server.Codecs(), sesapi.Service(), sesv2api.Service())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/v2/email/nonexistent", nil, "", "ses"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); got != "<UnknownOperationException/>\n" {
		t.Errorf("body = %q, want %q", got, "<UnknownOperationException/>\n")
	}
	for _, name := range []string{"Content-Type", "X-Amzn-Errortype"} {
		// A nil entry is how a handler keeps net/http from sniffing a
		// Content-Type; it sends no header.
		if got := rec.Result().Header.Values(name); len(got) != 0 {
			t.Errorf("%s = %q, want none", name, got)
		}
	}
}

// A request signed for a service understudy does not emulate is 501
// NotImplemented, which no SDK retries, in the envelope of the protocol the
// request was sent in, so the client can parse it, naming the service and
// the emulated ones. The rule trace stays in the journal.
func TestServicesNotEmulatedAreNamedInTheRequestsProtocol(t *testing.T) {
	cases := []struct {
		name, method, path, body, signedFor string
		header                              http.Header
		contentType                         string
	}{
		{
			"query", http.MethodPost, "/", "Action=DescribeVpcs&Version=2016-11-15", "ec2",
			http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}},
			"text/xml",
		},
		{"query GET", http.MethodGet, "/?Action=DescribeDBInstances&Version=2014-10-31", "", "rds", nil, "text/xml"},
		// Services no planned item adds, so the examples stay not emulated.
		{
			"awsJson1_1", http.MethodPost, "/", `{}`, "firehose",
			jsonHeader("Firehose_20150804.ListDeliveryStreams", "application/x-amz-json-1.1"), "application/x-amz-json-1.1",
		},
		{"restXml", http.MethodGet, "/2013-04-01/hostedzone", "", "route53", nil, "application/xml"},
		{
			"restJson1", http.MethodPost, "/2015-01-01/es/domain", `{"DomainName":"search"}`, "es",
			http.Header{"Content-Type": {"application/json"}},
			"application/json",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, server.Codecs())
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, request(t, c.method, c.path, c.header, c.body, c.signedFor))
			// restJson1 names the error in X-Amzn-Errortype, not the body.
			body := rec.Header().Get("X-Amzn-Errortype") + rec.Body.String()
			if rec.Code != http.StatusNotImplemented {
				t.Errorf("status = %d, want 501", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, c.contentType) {
				t.Errorf("Content-Type = %q, want %q", got, c.contentType)
			}
			for _, want := range []string{"NotImplemented", "signed for the AWS service " + c.signedFor + ", which understudy does not emulate", "dynamodb, sqs", "understudy coverage"} {
				if !strings.Contains(body, want) {
					t.Errorf("body %s does not contain %q", body, want)
				}
			}
			if strings.Contains(body, "rule ") {
				t.Errorf("body %s carries the routing trace; it belongs in the journal", body)
			}
			reasons := f.unclaimedReasons(t)
			if !slices.ContainsFunc(reasons, func(r string) bool { return strings.HasPrefix(r, "rule 8: ") }) {
				t.Errorf("journaled reasons = %q, want rule 8's", reasons)
			}
		})
	}
}

// DynamoDB Streams is emulated: rule 6 claims its target prefix through
// the registered service, with no unemulated entry. AWS refuses a
// request whose credential is scoped to the endpoint prefix
// streams.dynamodb with 400 InvalidSignatureException (aws-recorded,
// testdata/parity/dynamodb/R11/07-streams-signed-streams.dynamodb.json;
// dynamodb-streams.md 3.2), whatever understudy's sigv4 mode: it is a
// credential-scope check, not signature verification. The request never
// reaches the service.
func TestDynamoDBStreamsScopedToStreamsDynamoDBIsRefused(t *testing.T) {
	for _, verify := range []bool{false, true} {
		name := "sigv4 parse"
		if verify {
			name = "sigv4 verify"
		}
		t.Run(name, func(t *testing.T) {
			f := newFrontWith(t, nil, func(o *edge.Options) { o.Verify = verify }, dynamoDBStreams())
			rec := httptest.NewRecorder()
			header := jsonHeader("DynamoDBStreams_20120810.ListStreams", "application/x-amz-json-1.0")
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, `{}`, "streams.dynamodb"))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/x-amz-json-1.0" {
				t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", got)
			}
			// The recorded body, byte for byte, trailing space included.
			want := `{"__type":"com.amazon.coral.service#InvalidSignatureException","message":"Credential should be scoped to correct service: 'dynamodb'. "}`
			if got := rec.Body.String(); got != want {
				t.Errorf("body = %s, want %s", got, want)
			}
			if slices.ContainsFunc(f.calls(t), func(op ref.OperationID) bool { return op.Service == "dynamodbstreams" }) {
				t.Error("a request scoped to streams.dynamodb reached dynamodbstreams")
			}
		})
	}
}

// Signed for dynamodb, as every SDK signs it (aws-recorded,
// testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json), a Streams
// call reaches the Streams service under either sigv4 mode.
func TestDynamoDBStreamsSignedForDynamoDBIsServed(t *testing.T) {
	for _, verify := range []bool{false, true} {
		f := newFrontWith(t, nil, func(o *edge.Options) { o.Verify = verify }, dynamoDBStreams())
		header := jsonHeader("DynamoDBStreams_20120810.ListStreams", "application/x-amz-json-1.0")
		f.serve(t, request(t, http.MethodPost, "/", header, `{}`, "dynamodb"))
		if got := f.lastCall(t); got.Service != "dynamodbstreams" {
			t.Errorf("verify %v: the request reached %s, want dynamodbstreams", verify, got)
		}
	}
}

// A Streams target signed for another emulated service is that service's
// to answer (ARCHITECTURE.md 4.1 rule 8), and a target naming no Streams
// operation keeps Coral's unknown operation (error-matrix.md 5.5 T5).
func TestDynamoDBStreamsTargetsOutsideTheServiceAreNotClaimed(t *testing.T) {
	cases := []struct{ name, target, signedFor string }{
		{"signed for sqs", "DynamoDBStreams_20120810.ListStreams", "sqs"},
		{"unknown operation", "DynamoDBStreams_20120810.ListTables", "dynamodb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFront(t, nil)
			rec := httptest.NewRecorder()
			header := jsonHeader(c.target, "application/x-amz-json-1.0")
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, `{}`, c.signedFor))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "UnknownOperationException") {
				t.Errorf("answer = %d %s, want 400 UnknownOperationException", rec.Code, rec.Body)
			}
		})
	}
}
