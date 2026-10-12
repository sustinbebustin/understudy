package rpcv2cbor_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
)

func TestClaimRoutesGoClientRequests(t *testing.T) {
	reg, cw := newRegistry(t)
	tests := []struct {
		name   string
		path   string
		header map[string]string
		want   string
	}{
		{"service shape name, as the Go client sends", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", nil, "DescribeAlarms"},
		{"absolute service id with dots", "/service/com.amazonaws.cloudwatch.GraniteServiceVersion20100801/operation/PutMetricAlarm", nil, "PutMetricAlarm"},
		{"prefix before /service/ is ignored", "/some/prefix/service/GraniteServiceVersion20100801/operation/DeleteAlarms", nil, "DeleteAlarms"},
		{"no Accept header", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Accept": ""}, "DescribeAlarms"},
		{"no Content-Type", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Content-Type": ""}, "DescribeAlarms"},
		{"Content-Type with a parameter", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Content-Type": "application/cbor; charset=binary"}, "DescribeAlarms"},
		{"header value case", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Smithy-Protocol": "RPC-V2-CBOR"}, "DescribeAlarms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goRequest(t, "x", nil)
			r.URL.Path = tt.path
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			got, ok := rpcv2cbor.Codec{}.Claim(r, reg).(rpcv2cbor.Claimed)
			if !ok {
				t.Fatalf("Claim = %#v, want Claimed", rpcv2cbor.Codec{}.Claim(r, reg))
			}
			if want := operation(t, cw, tt.want); got.Op != want {
				t.Errorf("Claimed %s, want %s", got.Op.ID, want.ID)
			}
		})
	}
}

// An operation segment that is not a modeled operation's name, including
// an absolute shape id (which the specification forbids there) and an
// internal operation, is the service's unknown operation.
func TestClaimUnknownOperation(t *testing.T) {
	reg, cw := newRegistry(t)
	for _, op := range []string{"NoSuchOperation", "describeAlarms", "com.amazonaws.cloudwatch%23DescribeAlarms", "SweepAlarms"} {
		r := goRequest(t, op, nil)
		got, ok := rpcv2cbor.Codec{}.Claim(r, reg).(rpcv2cbor.UnknownOperation)
		if !ok || got.Service != cw || got.Operation != op {
			t.Errorf("Claim of %s = %#v, want UnknownOperation of cloudwatch", op, rpcv2cbor.Codec{}.Claim(r, reg))
		}
	}
}

func TestClaimDeclinesOtherRequests(t *testing.T) {
	reg, _ := newRegistry(t)
	tests := []struct {
		name   string
		path   string
		header map[string]string
	}{
		{"no Smithy-Protocol", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Smithy-Protocol": ""}},
		{"another Smithy protocol", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms", map[string]string{"Smithy-Protocol": "rpc-v2-json"}},
		{"not a /service/ path", "/", nil},
		{"trailing slash", "/service/GraniteServiceVersion20100801/operation/DescribeAlarms/", nil},
		{"unknown service", "/service/NoSuchService/operation/DescribeAlarms", nil},
		{"service that does not speak rpcv2Cbor", "/service/AmazonSQS/operation/ListQueues", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goRequest(t, "x", nil)
			r.URL.Path = tt.path
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			got, ok := rpcv2cbor.Codec{}.Claim(r, reg).(rpcv2cbor.Declined)
			if !ok || got.Reason == "" {
				t.Errorf("Claim = %#v, want Declined with a reason", rpcv2cbor.Codec{}.Claim(r, reg))
			}
		})
	}
}

// A request the Smithy-Protocol header and path make rpcv2Cbor but that
// breaks the protocol is rejected with the generic
// UnknownOperationException, the identity the edge answers unroutable
// requests with (ARCHITECTURE.md 4.1 rules 5 and 9), and the answer is a
// CBOR error a CBOR client can read.
func TestClaimRejectsRequestsTheProtocolForbids(t *testing.T) {
	reg, cw := newRegistry(t)
	tests := []struct {
		name   string
		method string
		header map[string]string
	}{
		{"X-Amz-Target as well", http.MethodPost, map[string]string{"X-Amz-Target": "GraniteServiceVersion20100801.DescribeAlarms"}},
		{"X-Amzn-Target as well", http.MethodPost, map[string]string{"X-Amzn-Target": "GraniteServiceVersion20100801.DescribeAlarms"}},
		{"GET", http.MethodGet, nil},
		{"JSON Content-Type", http.MethodPost, map[string]string{"Content-Type": "application/json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := goRequest(t, "DescribeAlarms", nil)
			r.Method = tt.method
			for k, v := range tt.header {
				r.Header.Set(k, v)
			}
			got, ok := rpcv2cbor.Codec{}.Claim(r, reg).(rpcv2cbor.Rejected)
			if !ok {
				t.Fatalf("Claim = %#v, want Rejected", rpcv2cbor.Codec{}.Claim(r, reg))
			}
			if got.Err.ID != awserr.UnknownOperationException() || got.Err.Message == "" {
				t.Errorf("Rejected with %+v, want UnknownOperationException with a message", got.Err)
			}
			resp := rpcv2cbor.Codec{}.EncodeError(r, cw, "req-1", got.Err)
			if resp.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.Status)
			}
			if resp.Header.Get("Smithy-Protocol") != "rpc-v2-cbor" || resp.Header.Get("Content-Type") != "application/cbor" {
				t.Errorf("headers = %v, want Smithy-Protocol and CBOR Content-Type", resp.Header)
			}
			body, err := cbor.Decode(resp.Body)
			if err != nil {
				t.Fatalf("decoding the error body: %v", err)
			}
			m, ok := body.(cbor.Map)
			if !ok || len(m) != 2 || m[0] != (cbor.Entry{Key: "__type", Value: cbor.Text("com.amazon.coral.service#UnknownOperationException")}) ||
				m[1] != (cbor.Entry{Key: "message", Value: cbor.Text(got.Err.Message)}) {
				t.Errorf("error body = %#v, want __type UnknownOperationException and the message", body)
			}
		})
	}
}
