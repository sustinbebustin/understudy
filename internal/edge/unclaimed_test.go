package edge_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

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

// SDKs sign DynamoDB Streams for dynamodb, the signing name of a service
// understudy emulates (dynamodb-streams.md 3.2,
// testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json). Its target
// prefix names a service understudy does not emulate, so a call is 501
// NotImplemented naming the operation (ADR 0003) whichever of its signing
// name and endpoint prefix signed it, never DynamoDB's unknown operation,
// and it is journaled as Unclaimed.
func TestDynamoDBStreamsTargetsAreNotImplemented(t *testing.T) {
	for _, signedFor := range []string{"dynamodb", "streams.dynamodb", ""} {
		t.Run("signed for "+signedFor, func(t *testing.T) {
			f := newFront(t, nil)
			rec := httptest.NewRecorder()
			header := jsonHeader("DynamoDBStreams_20120810.ListStreams", "application/x-amz-json-1.0")
			f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, `{}`, signedFor))
			if rec.Code != http.StatusNotImplemented {
				t.Errorf("status = %d, want 501", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/x-amz-json-1.0" {
				t.Errorf("Content-Type = %q, want application/x-amz-json-1.0", got)
			}
			var body struct {
				Type    string `json:"__type"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %s: %v", rec.Body, err)
			}
			want := "understudy does not implement DynamoDB Streams.ListStreams. Run 'understudy coverage --service dynamodb' for what is supported; " +
				"to ask for it, see https://github.com/sustinbebustin/understudy/issues?q=DynamoDB+Streams.ListStreams"
			if body.Type != "NotImplemented" || body.Message != want {
				t.Errorf("body = %+v, want NotImplemented %q", body, want)
			}
			reasons := f.unclaimedReasons(t)
			if !slices.ContainsFunc(reasons, func(r string) bool {
				return strings.HasPrefix(r, "rule 6: ") && strings.Contains(r, "DynamoDB Streams")
			}) {
				t.Errorf("journaled reasons = %q, want rule 6's naming DynamoDB Streams", reasons)
			}
		})
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
