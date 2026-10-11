package dynamodb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// jsonMediaType is awsJson1_0's media type, which Local is sent always.
const jsonMediaType = "application/x-amz-json-1.0"

// maxAnswerBytes bounds a Local answer held in memory: a BatchGetItem
// answers at most 16 MiB of items, so anything near this is not DynamoDB.
const maxAnswerBytes = 64 << 20

// requestBody is the body Local is sent. A client's original bytes go as
// they came, so Local decodes exactly what the client sent (ADR 0014); an
// empty body is {}, as Coral reads it. An internal caller's typed input,
// and a client body in a Content-Encoding Local does not read, are encoded
// through the awsJson codec in model order, the bytes boto3 sends.
func requestBody(op *registry.Operation, in smithy.Serializable, w kernel.Wire) ([]byte, *awserr.Error) {
	if fc, ok := w.(kernel.FromClient); ok && identityEncoded(fc.Request.Header) {
		if len(bytes.TrimSpace(fc.Request.Body)) == 0 {
			return []byte("{}"), nil
		}
		return fc.Request.Body, nil
	}
	return encodeInput(op, in)
}

// identityEncoded reports whether a client body is sent as is.
func identityEncoded(h http.Header) bool {
	enc := strings.TrimSpace(h.Get("Content-Encoding"))
	return enc == "" || strings.EqualFold(enc, "identity")
}

// encodeInput writes in as an awsJson 1.0 body. awsJson encodes inputs and
// outputs alike, members in model order, so the codec's encoder serves an
// input too; every DynamoDB operation has a structure output, so the codec
// writes the body.
func encodeInput(op *registry.Operation, in smithy.Serializable) ([]byte, *awserr.Error) {
	resp, err := awsjson.Codec{Version: awsjson.V10}.Encode(op, "", in, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// exchange is one request to Local: the account and region it partitions
// by, the operation it names and the instant it is dated. A forwarded
// call's exchange is its own; the shims' internal requests (snapshot export
// and import, an UpdateTable answered locally) name theirs.
type exchange struct {
	scope aws.Scope
	op    ref.OperationID
	at    clock.Instant
}

// exchangeOf is the exchange that forwards c.
func exchangeOf(c *kernel.Call) exchange {
	return exchange{scope: c.Scope(), op: c.OperationID(), at: c.Admitted()}
}

// credential is the SigV4 credential Local partitions by: the account as
// the access key and the region, which comes from understudy's validated
// region table, so it is safe as part of a file name under -dbPath
// (dynamodb-local.md 3). Local reads the scope and never checks the
// signature.
func credential(x exchange) string {
	date := x.at.Wire().UTC().Format("20060102")
	return x.scope.Account.String() + "/" + date + "/" + x.scope.Region.String() + "/dynamodb/aws4_request"
}

// unsignedSignature fills the 64 hex digits of signature Local requires to
// be present.
const unsignedSignature = "0000000000000000000000000000000000000000000000000000000000000000"

// upstream builds the request Local is sent: POST / with the operation's
// target, the rewritten credential and Accept-Encoding: identity, so that
// Local's x-amz-crc32 is over the bytes the client receives. The client's
// other headers stay behind: Local reads none of them, and its session
// token names a credential Local never sees.
func upstream(ctx context.Context, endpoint *url.URL, x exchange, body []byte) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.JoinPath("/").String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header = http.Header{
		"Content-Type":    {jsonMediaType},
		"X-Amz-Target":    {dynamodbapi.Service().JSONTargetPrefix + "." + x.op.Name},
		"X-Amz-Date":      {x.at.Wire().UTC().Format("20060102T150405Z")},
		"Accept-Encoding": {"identity"},
		"Authorization": {"AWS4-HMAC-SHA256 Credential=" + credential(x) +
			", SignedHeaders=content-type;host;x-amz-date;x-amz-target, Signature=" + unsignedSignature},
	}
	return r, nil
}

// send sends body to Local as x and returns its answer as received.
func (s *Service) send(ctx context.Context, endpoint *url.URL, x exchange, body []byte) (kernel.RawResponse, *awserr.Error) {
	req, err := upstream(ctx, endpoint, x, body)
	if err != nil {
		return kernel.RawResponse{}, awserr.Internalf(fmt.Errorf("building the request to DynamoDB Local at %s: %w", endpoint, err))
	}
	resp, err := s.local.Transport().RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return kernel.RawResponse{}, awserr.RequestAborted()
		}
		return kernel.RawResponse{}, errLocalUnavailable("reach DynamoDB Local at "+endpoint.String(), err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	switch {
	case err != nil:
		return kernel.RawResponse{}, errLocalUnavailable("read DynamoDB Local's answer", err)
	case len(answer) > maxAnswerBytes:
		return kernel.RawResponse{}, awserr.Internalf(&AnswerTooLargeError{Op: x.op, Limit: maxAnswerBytes})
	}
	return kernel.RawResponse{Status: resp.StatusCode, Header: resp.Header, Body: answer}, nil
}

// AnswerTooLargeError is a Local answer above the size any DynamoDB answer
// reaches.
type AnswerTooLargeError struct {
	Op    ref.OperationID
	Limit int
}

func (e *AnswerTooLargeError) Error() string {
	return fmt.Sprintf("DynamoDB Local answered %s with more than %d bytes, more than any DynamoDB answer; the answer was dropped", e.Op, e.Limit)
}
