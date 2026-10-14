package s3_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// harness is a kernel running the S3 service, with the log it writes to.
type harness struct {
	*kerneltest.K
	svc  *s3.Service
	logs *bytes.Buffer
}

// run runs fn against a fresh S3 service built with o.
func run(t *testing.T, o s3.Options, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		logs := &bytes.Buffer{}
		d := k.Deps(s3api.Service())
		d.Log = slog.New(slog.NewTextHandler(logs, nil))
		fn(&harness{K: k, svc: s3.New(d, o), logs: logs})
	}, kerneltest.Services(s3api.Service()))
}

// who is the caller of a request: SigV4 credentials of account scoped to
// region, or an anonymous request; host is the Host it was sent to.
type who struct {
	account   string
	region    string
	anonymous bool
	// sigV2 signs with SigV2, which asserts no region: the scope is the
	// default region's.
	sigV2 bool
	host  string
}

// signed is the default account's root signing for region.
func signed(region string) who { return who{region: region} }

// call builds the call of a client request for op from w.
func (h *harness) call(op *registry.Operation, w who) *kernel.Call {
	h.Helper()
	account := aws.DefaultAccount()
	if w.account != "" {
		a, err := aws.ParseAccountID(w.account)
		if err != nil {
			h.Fatal(err)
		}
		account = a
	}
	region := aws.DefaultRegion()
	if w.region != "" {
		r, err := aws.KnownRegions().Parse(w.region)
		if err != nil {
			h.Fatal(err)
		}
		region = r
	}
	var principal identity.Principal = identity.Root{Account: account}
	var src aws.RegionSource = aws.CredentialScope{Region: region}
	switch {
	case w.anonymous:
		principal, src = identity.Anonymous{}, aws.None{}
	case w.sigV2:
		src = aws.None{}
	}
	host := w.host
	if host == "" {
		host = "localhost:4566"
	}
	return h.Kernel.NewCall(kernel.ExternalCall{
		RequestID:    h.Kernel.IDs().RequestID(),
		Operation:    op.ID,
		Scope:        aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region},
		RegionSource: src,
		Principal:    principal,
		Origin:       ref.External{Protocol: ref.RESTXML, Listener: ref.ListenerMain},
		Admitted:     h.Clock.Now(),
		Endpoint:     kernel.Endpoint{Scheme: "http", Host: host},
	})
}

// as runs op with in for w through the full pipeline.
func as[In, Out any](h *harness, w who, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return kerneltest.Dispatch[In, Out](h.Context(), h.K, h.call(op.Descriptor(), w), in)
}

// invoke runs op with in as the default account's root in us-east-1.
func invoke[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return as(h, who{}, op, in)
}

// ok runs op with in for w and fails the test on an error.
func ok[In, Out any](h *harness, w who, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := as(h, w, op, in)
	if err != nil {
		h.Fatalf("%s: unexpected error %s: %s", op.Descriptor().ID, err.ID.Shape, err.Message)
	}
	return out
}

// createBucket creates name in us-east-1 as the default account.
func (h *harness) createBucket(name string) {
	h.Helper()
	ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: name})
}

// raw runs the raw S3 Control operation name as the default account with
// a request to target on its S3 Control host, and returns the recorded
// answer.
func (h *harness) raw(name, method, target, body string) (*httptest.ResponseRecorder, *awserr.Error) {
	h.Helper()
	return h.rawFor(aws.DefaultAccount().String(), name, method, target, body)
}

// rawFor is raw with account as the request's x-amz-account-id.
func (h *harness) rawFor(account, name, method, target, body string) (*httptest.ResponseRecorder, *awserr.Error) {
	h.Helper()
	op, ok := h.Kernel.Registry().Operation(ref.OperationID{Service: "s3", Name: name})
	if !ok {
		h.Fatalf("no operation s3.%s", name)
	}
	r := httptest.NewRequestWithContext(h.Context(), method, "http://000000000000.localhost:4566"+target, strings.NewReader(body))
	r.Header.Set("X-Amz-Account-Id", account)
	rec := httptest.NewRecorder()
	switch res := h.Kernel.Dispatch(h.Context(), h.call(op, who{}), kernel.Ok{In: kernel.RawExchange{Request: r, Writer: rec}}).(type) {
	case kernel.Failure:
		return nil, res.Err
	case kernel.StatusOKFailure:
		return nil, res.Err
	case kernel.Output:
	}
	return rec, nil
}

// rendered is an error as the restXml codec sends it for request id REQ.
type rendered struct {
	status int
	header http.Header
	body   string
}

// requestID is the request id errors are rendered with.
const requestID = "REQ"

// render encodes e as S3 answers a GET with it.
func render(t *testing.T, e *awserr.Error) rendered {
	t.Helper()
	c := restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect { return restxml.S3Dialect(s3api.IdentityInvalidArgument()) }}
	resp := c.EncodeError(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody), s3api.Service(), requestID, e)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return rendered{status: resp.Status, header: resp.Header, body: string(body)}
}

// errorBody is the exact S3 error document of code and message with the
// context elements fields, name and value in turn.
func errorBody(code, message string, fields ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<Error><Code>" + code + "</Code><Message>" + message + "</Message>")
	for i := 0; i+1 < len(fields); i += 2 {
		b.WriteString("<" + fields[i] + ">" + fields[i+1] + "</" + fields[i] + ">")
	}
	b.WriteString("<RequestId>" + requestID + "</RequestId><HostId>" + restxml.HostID(requestID) + "</HostId></Error>")
	return b.String()
}

// wantFields fails the test unless e carries every context element of
// want with its value.
func wantFields(t *testing.T, e *awserr.Error, want map[string]string) {
	t.Helper()
	for name, value := range want {
		i := slices.IndexFunc(e.Fields, func(f awserr.Field) bool { return f.Name == name })
		switch {
		case i < 0:
			t.Fatalf("%s: no %s element, want %q", e.ID.QueryCode, name, value)
		case e.Fields[i].Value != value:
			t.Fatalf("%s: %s = %q, want %q", e.ID.QueryCode, name, e.Fields[i].Value, value)
		}
	}
}

// wantFieldOrder fails the test unless e's context elements are names, in
// that order, as S3 renders them.
func wantFieldOrder(t *testing.T, e *awserr.Error, names ...string) {
	t.Helper()
	got := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		got[i] = f.Name
	}
	if !slices.Equal(got, names) {
		t.Fatalf("%s: elements %v, want %v", e.ID.QueryCode, got, names)
	}
}

// header returns the value of e's response header name.
func header(e *awserr.Error, name string) string {
	for _, x := range e.Headers {
		if strings.EqualFold(x.Name, name) {
			return x.Value
		}
	}
	return ""
}

// wantCode fails the test unless err is the error code with status.
func wantCode(t *testing.T, err *awserr.Error, code string, status int) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("got success, want %s", code)
	case err.ID.QueryCode != code || err.ID.Status != status:
		t.Fatalf("got %s %d (%s), want %s %d", err.ID.QueryCode, err.ID.Status, err.Message, code, status)
	}
}
