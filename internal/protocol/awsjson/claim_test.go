package awsjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
)

func TestClaim(t *testing.T) {
	f := newFixture(t)
	v10 := awsjson.Codec{Version: awsjson.V10}
	v11 := awsjson.Codec{Version: awsjson.V11}

	claimed := func(t *testing.T, c awsjson.Codec, r *http.Request, want string) {
		t.Helper()
		got, ok := c.Claim(r, f.reg).(awsjson.Claimed)
		if !ok || got.Op.ID.String() != want {
			t.Errorf("Claim = %#v, want Claimed %s", c.Claim(r, f.reg), want)
		}
	}
	t.Run("SQS as the CLI sends it", func(t *testing.T) {
		r := request("AmazonSQS.CreateQueue", "application/x-amz-json-1.0", []byte(`{}`))
		r.Header.Set("X-Amzn-Query-Mode", "true")
		claimed(t, v10, r, "sqs.CreateQueue")
	})
	t.Run("media type parameters and case are ignored", func(t *testing.T) {
		r := request("Logs_20140328.DescribeLogGroups", "Application/X-Amz-Json-1.1; charset=utf-8", nil)
		claimed(t, v11, r, "logs.DescribeLogGroups")
	})
	// DynamoDB treats application/json as JSON (error-matrix.md 5.5 T6).
	t.Run("plain JSON media type", func(t *testing.T) {
		claimed(t, v10, request("AmazonSQS.CreateQueue", "application/json", nil), "sqs.CreateQueue")
		claimed(t, v11, request("Logs_20140328.DescribeLogGroups", "application/json; charset=utf-8", nil), "logs.DescribeLogGroups")
	})
	t.Run("a malformed parameter does not hide the media type", func(t *testing.T) {
		claimed(t, v10, request("AmazonSQS.CreateQueue", "application/x-amz-json-1.0; charset", nil), "sqs.CreateQueue")
	})
	t.Run("the path is not checked", func(t *testing.T) {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/000000000000/q", http.NoBody)
		r.Header.Set("X-Amz-Target", "AmazonSQS.DeleteMessage")
		r.Header.Set("Content-Type", "application/x-amz-json-1.0")
		claimed(t, v10, r, "sqs.DeleteMessage")
	})

	unknown := map[string]string{
		"operation name is case sensitive": "AmazonSQS.createqueue",
		"operation not in the model":       "AmazonSQS.Bogus",
		"internal operation":               "AmazonSQS.HoldVisibility",
		"no operation":                     "AmazonSQS",
		"empty operation":                  "AmazonSQS.",
		"dotted operation":                 "AmazonSQS.Foo.Bar",
	}
	for name, target := range unknown {
		t.Run(name, func(t *testing.T) {
			got, ok := v10.Claim(request(target, "application/x-amz-json-1.0", nil), f.reg).(awsjson.UnknownOperation)
			if !ok || got.Service != f.sqs {
				t.Errorf("Claim(%q) = %#v, want UnknownOperation for sqs", target, got)
			}
		})
	}

	declined := map[string]*http.Request{
		"no target":                  request("", "application/x-amz-json-1.0", nil),
		"form body":                  request("", "application/x-www-form-urlencoded", nil),
		"other version media type":   request("AmazonSQS.CreateQueue", "application/x-amz-json-1.1", nil),
		"JSON-like media type":       request("AmazonSQS.CreateQueue", "application/jsonasdf", nil),
		"unparsable content type":    request("AmazonSQS.CreateQueue", "application/", nil),
		"no content type":            request("AmazonSQS.CreateQueue", "", nil),
		"plain JSON, other version":  request("Logs_20140328.DescribeLogGroups", "application/json", nil),
		"unknown prefix":             request("DynamoDB_20120810.ListTables", "application/x-amz-json-1.0", nil),
		"service of another version": request("Logs_20140328.DescribeLogGroups", "application/x-amz-json-1.0", nil),
		"GET": func() *http.Request {
			r := request("AmazonSQS.CreateQueue", "application/x-amz-json-1.0", nil)
			r.Method = http.MethodGet
			return r
		}(),
	}
	for name, r := range declined {
		t.Run(name, func(t *testing.T) {
			got, ok := v10.Claim(r, f.reg).(awsjson.Declined)
			if !ok || got.Reason == "" {
				t.Errorf("Claim = %#v, want Declined with a reason", v10.Claim(r, f.reg))
			}
		})
	}
	t.Run("the zero codec claims nothing", func(t *testing.T) {
		r := request("AmazonSQS.CreateQueue", "application/x-amz-json-1.0", nil)
		if _, ok := (awsjson.Codec{}).Claim(r, f.reg).(awsjson.Declined); !ok {
			t.Error("the zero Codec claimed a request")
		}
	})
}
