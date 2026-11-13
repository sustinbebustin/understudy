package s3_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// TestS3ControlTagsOverHTTP sends Terraform's tag round trip through the
// edge as a client does: CreateBucket with tags on the S3 host, then the S3
// Control tag operations on the account host <account>.<base> with the
// bucket ARN percent-encoded in the path (s3.md section 2, ADR 0013), and
// GetBucketTagging to read the shared tag set back.
func TestS3ControlTagsOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		send := edgeClient(k)
		tags := tagsPath("tagged")
		const createBody = `<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Tags><Tag><Key>Name</Key><Value>probe</Value></Tag></Tags></CreateBucketConfiguration>`
		if code, body := send(http.MethodPut, s3Host, "/tagged", createBody); code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s, want 200", code, body)
		}

		const listPrefix = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<ListTagsForResourceResult xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags>`
		if code, body := send(http.MethodGet, controlHost, tags, ""); code != http.StatusOK || body != listPrefix+`<Tag><Key>Name</Key><Value>probe</Value></Tag></Tags></ListTagsForResourceResult>` {
			t.Fatalf("ListTagsForResource = %d %s, want 200 with the tags CreateBucket carried", code, body)
		}

		tagBody := `<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags><Tag><Key>team</Key><Value>storage</Value></Tag></Tags></TagResourceRequest>`
		if code, body := send(http.MethodPost, controlHost, tags, tagBody); code != http.StatusNoContent {
			t.Fatalf("TagResource = %d %s, want 204", code, body)
		}
		if code, body := send(http.MethodDelete, controlHost, tags+"?tagKeys=Name", ""); code != http.StatusNoContent {
			t.Fatalf("UntagResource = %d %s, want 204", code, body)
		}

		code, body := send(http.MethodGet, s3Host, "/tagged?tagging", "")
		if code != http.StatusOK || !strings.Contains(body, "<TagSet><Tag><Key>team</Key><Value>storage</Value></Tag></TagSet>") {
			t.Fatalf("GetBucketTagging = %d %s, want the tag set TagResource and UntagResource left: team=storage", code, body)
		}
	}, kerneltest.Services(s3api.Service()))
}

// TestS3ControlErrorsOverHTTP: S3 Control is restXml without
// noErrorWrapping, so its errors are <ErrorResponse><Error>, which the
// SDKs' s3control deserializers read as Error>Code; S3's bare <Error>
// would reach them as UnknownError. ListTagsForResource of a missing
// bucket and of another account's bucket each answer 404 with the body AWS
// sent, byte for byte once the request id and host id values are masked,
// and a request id of the recorded form, the same in the x-amz-request-id
// header and the body (testdata/parity/s3/R06/06-s3control-list-missing.json,
// 06-s3control-list-not-owned.json).
func TestS3ControlErrorsOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		sendAs := edgeExchangeAs(k)
		if code, _, body := sendAs(otherAccount, http.MethodPut, s3Host, "/foreign", ""); code != http.StatusOK {
			t.Fatalf("CreateBucket as %s = %d %s, want 200", otherAccount, code, body)
		}
		for bucket, step := range map[string]string{"missing": "06-s3control-list-missing.json", "foreign": "06-s3control-list-not-owned.json"} {
			recorded := readR06Response(t, step)
			if !s3RequestIDForm.MatchString(recorded.requestID) {
				t.Fatalf("%s: recorded request id %q does not have the form the test expects, %s", step, recorded.requestID, s3RequestIDForm)
			}
			code, header, body := sendAs(aws.DefaultAccount().String(), http.MethodGet, controlHost, tagsPath(bucket), "")
			if code != recorded.status || maskIDs(body) != maskIDs(recorded.body) {
				t.Errorf("ListTagsForResource of %s = %d\n%s\nwant %d (%s)\n%s", bucket, code, body, recorded.status, step, recorded.body)
			}
			id := header.Get("X-Amz-Request-Id")
			if !s3RequestIDForm.MatchString(id) || !strings.Contains(body, "<RequestId>"+id+"</RequestId>") {
				t.Errorf("ListTagsForResource of %s: x-amz-request-id %q with body %s, want a request id of the recorded form %s, as in %s, in both", bucket, id, body, s3RequestIDForm, step)
			}
		}
	}, kerneltest.Services(s3api.Service()))
}

// s3RequestIDForm is the form of S3's and S3 Control's request ids: 16
// characters of Crockford's base32 alphabet, digits and capitals without
// I, L, O and U, as every x-amz-request-id under testdata/parity/s3 is.
var s3RequestIDForm = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{16}$`)

// idElements are the elements of a restXml error that hold the request id
// and host id, whose values differ from call to call.
var idElements = regexp.MustCompile(`<(RequestId|HostId)>[^<]*</(RequestId|HostId)>`)

// maskIDs replaces the values of an error body's RequestId and HostId.
func maskIDs(body string) string {
	return idElements.ReplaceAllString(body, "<$1>masked</$2>")
}

// recordedResponse is the response of a one-exchange capture.
type recordedResponse struct {
	status          int
	requestID, body string
}

// readR06Response reads the response of a capture under
// testdata/parity/s3/R06.
func readR06Response(t *testing.T, step string) recordedResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", "s3", "R06", step))
	if err != nil {
		t.Fatalf("read capture %s: %v", step, err)
	}
	var c struct {
		Exchanges []struct {
			Response struct {
				Status  int         `json:"status"`
				Headers [][2]string `json:"headers"`
				Body    string      `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || len(c.Exchanges) != 1 {
		t.Fatalf("capture %s does not hold one exchange (%v)", step, err)
	}
	resp := c.Exchanges[0].Response
	r := recordedResponse{status: resp.Status, body: resp.Body}
	for _, h := range resp.Headers {
		if strings.EqualFold(h[0], "x-amz-request-id") {
			r.requestID = h[1]
		}
	}
	return r
}

// The hosts the edge tests send to: S3's, and the S3 Control account host
// of the default account.
const (
	s3Host      = "localhost:4566"
	controlHost = "000000000000.localhost:4566"
)

// edgeClient returns a sender of requests signed by the default account
// through an edge in front of a new S3 service, as edgeClientAs does.
func edgeClient(k *kerneltest.K) func(method, host, target, body string) (int, string) {
	sendAs := edgeClientAs(k)
	account := aws.DefaultAccount().String()
	return func(method, host, target, body string) (int, string) {
		k.Helper()
		return sendAs(account, method, host, target, body)
	}
}

// edgeClientAs is edgeExchangeAs answering the status and body.
func edgeClientAs(k *kerneltest.K) func(account, method, host, target, body string) (int, string) {
	send := edgeExchangeAs(k)
	return func(account, method, host, target, body string) (int, string) {
		k.Helper()
		code, _, got := send(account, method, host, target, body)
		return code, got
	}
}

// edgeExchangeAs returns a sender of requests signed by an account's root
// through an edge in front of a new S3 service; a request to the default
// account's S3 Control host carries x-amz-account-id as the SDKs send it.
// It answers the status, headers and body.
func edgeExchangeAs(k *kerneltest.K) func(account, method, host, target, body string) (int, http.Header, string) {
	t := k.T
	svc := s3.New(k.Deps(s3api.Service()), s3.Options{})
	front, err := edge.New(edge.Options{
		Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
		Codecs:       map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{c: restxml.Codec{Dialect: s3Dialect}}},
		BucketExists: svc.BucketExists, S3BaseDomains: []string{"localhost"},
		S3Control: xmlCodec{c: restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect {
			return restxml.S3ControlDialect(s3api.IdentityInvalidArgument())
		}}},
		Listener: ref.ListenerMain, Unclaimed: ref.RESTXML,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostAccount := aws.DefaultAccount().String()
	return func(account, method, host, target, body string) (int, http.Header, string) {
		t.Helper()
		r := httptest.NewRequestWithContext(k.Context(), method, "http://"+host+target, strings.NewReader(body))
		if strings.HasPrefix(host, hostAccount+".") {
			r.Header.Set("X-Amz-Account-Id", hostAccount)
		}
		auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: account, SecretAccessKey: "secret", Region: aws.DefaultRegion(), Service: "s3"}, k.Clock.Wall())
		rec := httptest.NewRecorder()
		front.ServeHTTP(rec, r)
		got, rerr := io.ReadAll(rec.Result().Body)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return rec.Code, rec.Result().Header, string(got)
	}
}

// xmlCodec adapts the restXml codec to the edge as server/codecs.go does;
// the server package wires every service, so the test adapts the one codec
// S3 speaks.
type xmlCodec struct{ c restxml.Codec }

// s3Dialect is S3's restXml dialect, as the server wires it.
func s3Dialect(ref.ServiceID) restxml.Dialect {
	d := restxml.S3Dialect(s3api.IdentityInvalidArgument())
	d.Internal = s3api.IdentityInternalError()
	return d
}

func (x xmlCodec) Protocol() ref.ProtocolID { return x.c.Protocol() }

func (xmlCodec) Claim(*http.Request, *registry.Registry, string) edge.Claim {
	return edge.Declined{Reason: "restXml requests are routed by the service's route table"}
}

func (x xmlCodec) BodyLimit(op *registry.Operation) int64 { return x.c.BodyLimit(op) }

func (xmlCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (x xmlCodec) DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	return x.c.Decode(r, op, body, in)
}

func (x xmlCodec) Encode(r *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := x.c.Encode(r, op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}, nil
}

func (x xmlCodec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := x.c.EncodeError(r, svc, requestID, e)
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}
}
