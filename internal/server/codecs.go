package server

import (
	"bytes"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// JSONCodec adapts the awsJson codec of version v to the edge. The edge may
// not import a codec, so the composition root does the adapting.
func JSONCodec(v awsjson.Version) edge.Codec {
	return jsonCodec{c: awsjson.Codec{Version: v}}
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
	rc, err := body.Open()
	if err != nil {
		return awserr.Internalf(err)
	}
	defer rc.Close()
	capped := r.WithContext(r.Context())
	capped.Body = rc
	return j.c.Decode(capped, op, in)
}

func (j jsonCodec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (edge.Response, *awserr.Error) {
	resp, err := j.c.Encode(op, requestID, out, status)
	if err != nil {
		return edge.Response{}, err
	}
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: bytes.NewReader(resp.Body)}, nil
}

func (j jsonCodec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) edge.Response {
	resp := j.c.EncodeError(r, svc, requestID, e)
	return edge.Response{Status: resp.Status, Header: resp.Header, Body: bytes.NewReader(resp.Body)}
}
