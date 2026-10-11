package server

import (
	"bytes"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocol/restjson"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/rpcv2cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Codecs returns every wire protocol adapted to the edge, keyed by the id
// each registers under. The edge may not import a codec, so the
// composition root does the adapting.
func Codecs() map[ref.ProtocolID]edge.Codec {
	out := map[ref.ProtocolID]edge.Codec{}
	for _, c := range []edge.Codec{
		JSONCodec(awsjson.V10), JSONCodec(awsjson.V11),
		queryCodec{},
		cborCodec{},
		restJSONCodec{c: restjson.Codec{ClientFaultFields: clientFaultFields}},
		restXMLCodec{c: restxml.Codec{Dialect: dialect}},
	} {
		out[c.Protocol()] = c
	}
	return out
}

// JSONCodec adapts the awsJson codec of version v to the edge.
func JSONCodec(v awsjson.Version) edge.Codec {
	return jsonCodec{c: awsjson.Codec{Version: v}}
}

// withBody returns r reading the edge's capped body; the caller closes it.
// The RPC codecs read the whole request themselves, so DecodeBody hands them
// the capped body as the request's.
func withBody(r *http.Request, body smithy.Body) (*http.Request, *awserr.Error) {
	rc, err := body.Open()
	if err != nil {
		return nil, awserr.Internalf(err)
	}
	capped := r.WithContext(r.Context())
	capped.Body = rc
	return capped, nil
}

func response(status int, header http.Header, body []byte) edge.Response {
	return edge.Response{Status: status, Header: header, Body: bytes.NewReader(body)}
}

type jsonCodec struct{ c awsjson.Codec }

func (j jsonCodec) Protocol() ref.ProtocolID { return j.c.Protocol() }

func (j jsonCodec) Claim(r *http.Request, reg *registry.Registry) edge.Claim {
	switch c := j.c.Claim(r, reg).(type) {
	case awsjson.Claimed:
		return edge.Claimed{Op: c.Op}
	case awsjson.UnknownOperation:
		return edge.UnknownOperation{Service: c.Service, Name: c.Operation}
	case awsjson.Declined:
		return edge.Declined{Reason: c.Reason}
	}
	return edge.Declined{Reason: "the awsJson codec gave no answer"}
}

func (jsonCodec) BodyLimit(op *registry.Operation) int64 { return awsjson.MaxRequestBytes(op) }

// DecodeHead binds nothing: awsJson carries every member in the body.
func (jsonCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (j jsonCodec) DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	capped, err := withBody(r, body)
	if err != nil {
		return err
	}
	defer capped.Body.Close()
	return j.c.Decode(capped, op, in)
}

func (j jsonCodec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := j.c.Encode(op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return response(resp.Status, resp.Header, resp.Body), nil
}

func (j jsonCodec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := j.c.EncodeError(r, svc, requestID, e)
	return response(resp.Status, resp.Header, resp.Body)
}

// queryCodec adapts the awsQuery codec.
type queryCodec struct{ c awsquery.Codec }

func (q queryCodec) Protocol() ref.ProtocolID { return q.c.Protocol() }

func (q queryCodec) Claim(r *http.Request, reg *registry.Registry) edge.Claim {
	switch c := q.c.Claim(r, reg).(type) {
	case awsquery.Claimed:
		return edge.Claimed{Op: c.Op}
	case awsquery.UnknownOperation:
		return edge.UnknownOperation{Service: c.Service, Name: c.Operation}
	case awsquery.Declined:
		return edge.Declined{Reason: c.Reason}
	}
	return edge.Declined{Reason: "the awsQuery codec gave no answer"}
}

func (queryCodec) BodyLimit(op *registry.Operation) int64 { return awsquery.MaxRequestBytes(op) }

// DecodeHead binds nothing: the form, in the body or the query string of a
// GET, carries every member.
func (queryCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (q queryCodec) DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	capped, err := withBody(r, body)
	if err != nil {
		return err
	}
	defer capped.Body.Close()
	return q.c.Decode(capped, op, in)
}

func (q queryCodec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := q.c.Encode(op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return response(resp.Status, resp.Header, resp.Body), nil
}

func (q queryCodec) EncodeError(_ *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := q.c.EncodeError(svc, requestID, e)
	return response(resp.Status, resp.Header, resp.Body)
}

// cborCodec adapts the rpcv2Cbor codec.
type cborCodec struct{ c rpcv2cbor.Codec }

func (c cborCodec) Protocol() ref.ProtocolID { return c.c.Protocol() }

// Claim maps a request the codec rejects (rpcv2Cbor by its header and path
// but breaking a rule of the protocol) to Declined: the edge has no claim
// that answers with the codec's error, so it answers as for an unclaimed
// request, whose identity is the same UnknownOperationException.
func (c cborCodec) Claim(r *http.Request, reg *registry.Registry) edge.Claim {
	switch cl := c.c.Claim(r, reg).(type) {
	case rpcv2cbor.Claimed:
		return edge.Claimed{Op: cl.Op}
	case rpcv2cbor.UnknownOperation:
		return edge.UnknownOperation{Service: cl.Service, Name: cl.Operation}
	case rpcv2cbor.Declined:
		return edge.Declined{Reason: cl.Reason}
	case rpcv2cbor.Rejected:
		return edge.Declined{Reason: cl.Err.Message}
	}
	return edge.Declined{Reason: "the rpcv2Cbor codec gave no answer"}
}

func (cborCodec) BodyLimit(op *registry.Operation) int64 { return rpcv2cbor.MaxRequestBytes(op) }

// DecodeHead binds nothing: rpcv2Cbor carries every member in the body.
func (cborCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (c cborCodec) DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	capped, err := withBody(r, body)
	if err != nil {
		return err
	}
	defer capped.Body.Close()
	return c.c.Decode(capped, op, in)
}

func (c cborCodec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := c.c.Encode(op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return response(resp.Status, resp.Header, resp.Body), nil
}

func (c cborCodec) EncodeError(_ *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := c.c.EncodeError(svc, requestID, e)
	return response(resp.Status, resp.Header, resp.Body)
}

// restJSONCodec adapts the restJson1 codec (Lambda). The edge routes REST
// requests through the service's route table, so Claim is never asked.
type restJSONCodec struct{ c restjson.Codec }

func (r restJSONCodec) Protocol() ref.ProtocolID { return r.c.Protocol() }

func (restJSONCodec) Claim(*http.Request, *registry.Registry) edge.Claim {
	return edge.Declined{Reason: "restJson1 requests are routed by the service's route table, not claimed"}
}

func (r restJSONCodec) BodyLimit(op *registry.Operation) int64 { return r.c.BodyLimit(op) }

// DecodeHead binds nothing: Decode binds head and body in one call, since a
// generated Deserialize resets its required-member tracking each run.
func (restJSONCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (r restJSONCodec) DecodeBody(req *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	return r.c.Decode(req, op, body, in)
}

func (r restJSONCodec) Encode(req *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := r.c.Encode(req, op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}, nil
}

func (r restJSONCodec) EncodeError(req *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := r.c.EncodeError(req, svc, requestID, e)
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}
}

// restXMLCodec adapts the restXml codec (S3). The edge routes REST requests
// through the service's route table, so Claim is never asked.
type restXMLCodec struct{ c restxml.Codec }

// clientFaultFields are the fields a restJson1 service puts on the client
// errors the codec raises itself: Lambda's Type User (error-matrix.md 5.6
// T6), none elsewhere.
func clientFaultFields(svc ref.ServiceID) []awserr.Field {
	if svc == lambdaapi.Service().ID {
		return []awserr.Field{{Name: "Type", Value: "User"}}
	}
	return nil
}

// S3ControlCodec renders the errors of the S3 Control tag operations,
// which S3 serves as raw operations: S3's dialect with wrapped errors.
func S3ControlCodec() edge.Codec {
	return restXMLCodec{c: restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect {
		d := restxml.S3ControlDialect(s3api.IdentityInvalidArgument())
		d.Internal = s3api.IdentityInternalError()
		return d
	}}}
}

// dialect is S3's restXml dialect, which renders internal failures as S3's
// own InternalError; other services speak Smithy's.
func dialect(svc ref.ServiceID) restxml.Dialect {
	if svc == s3api.Service().ID {
		d := restxml.S3Dialect(s3api.IdentityInvalidArgument())
		d.Internal = s3api.IdentityInternalError()
		return d
	}
	return restxml.Dialect{}
}

func (r restXMLCodec) Protocol() ref.ProtocolID { return r.c.Protocol() }

func (restXMLCodec) Claim(*http.Request, *registry.Registry) edge.Claim {
	return edge.Declined{Reason: "restXml requests are routed by the service's route table, not claimed"}
}

func (r restXMLCodec) BodyLimit(op *registry.Operation) int64 { return r.c.BodyLimit(op) }

// DecodeHead binds nothing: Decode binds head and body in one call.
func (restXMLCodec) DecodeHead(*http.Request, *registry.Operation, smithy.Deserializable) *awserr.Error {
	return nil
}

func (r restXMLCodec) DecodeBody(req *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	return r.c.Decode(req, op, body, in)
}

func (r restXMLCodec) Encode(req *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := r.c.Encode(req, op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}, nil
}

func (r restXMLCodec) EncodeError(req *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := r.c.EncodeError(req, svc, requestID, e)
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}
}
