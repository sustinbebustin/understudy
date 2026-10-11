package s3_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
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
// would reach them as UnknownError.
func TestS3ControlErrorsOverHTTP(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		send := edgeClient(k)
		code, body := send(http.MethodGet, controlHost, tagsPath("missing"), "")
		var doc struct {
			XMLName xml.Name `xml:"ErrorResponse"`
			Error   struct {
				Code string `xml:"Code"`
			} `xml:"Error"`
		}
		if err := xml.Unmarshal([]byte(body), &doc); err != nil || code != http.StatusNotFound || doc.Error.Code != "NoSuchBucket" {
			t.Fatalf("ListTagsForResource of a missing bucket = %d %s, want 404 <ErrorResponse><Error><Code>NoSuchBucket", code, body)
		}
	}, kerneltest.Services(s3api.Service()))
}

// The hosts the edge tests send to: S3's, and the S3 Control account host
// of the default account.
const (
	s3Host      = "localhost:4566"
	controlHost = "000000000000.localhost:4566"
)

// edgeClient returns a sender of signed requests through an edge in front
// of a new S3 service; a request to the account host carries
// x-amz-account-id as the SDKs send it. It answers the status and body.
func edgeClient(k *kerneltest.K) func(method, host, target, body string) (int, string) {
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
	account := aws.DefaultAccount().String()
	return func(method, host, target, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequestWithContext(k.Context(), method, "http://"+host+target, strings.NewReader(body))
		if strings.HasPrefix(host, account+".") {
			r.Header.Set("X-Amz-Account-Id", account)
		}
		auth.Sign(r, []byte(body), auth.Signer{AccessKeyID: account, SecretAccessKey: "secret", Region: aws.DefaultRegion(), Service: "s3"}, k.Clock.Wall())
		rec := httptest.NewRecorder()
		front.ServeHTTP(rec, r)
		got, rerr := io.ReadAll(rec.Result().Body)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return rec.Code, string(got)
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

func (xmlCodec) Claim(*http.Request, *registry.Registry) edge.Claim {
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
