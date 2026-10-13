package edge_test

import (
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
		{
			"awsJson1_1", http.MethodPost, "/", `{}`, "kinesis",
			jsonHeader("Kinesis_20131202.ListStreams", "application/x-amz-json-1.1"), "application/x-amz-json-1.1",
		},
		{"restXml", http.MethodGet, "/2013-04-01/hostedzone", "", "route53", nil, "application/xml"},
		{
			"restJson1", http.MethodPost, "/restapis", `{"name":"api"}`, "apigateway",
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
