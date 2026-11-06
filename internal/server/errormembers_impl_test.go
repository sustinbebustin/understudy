package server_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonprotocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/rpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// errorWire is one protocol's codec with a corpus service of it and that
// service's InvalidGreeting value, whose one member is its message.
type errorWire struct {
	protocol ref.ProtocolID
	svc      *registry.Service
	greeting func(message string) awserr.Members
}

func errorWires() []errorWire {
	return []errorWire{
		{ref.AWSJSON10, jsonrpc10.Service(), func(m string) awserr.Members { return &jsonrpc10.InvalidGreeting{Message: &m} }},
		{ref.AWSJSON11, jsonprotocol.Service(), func(m string) awserr.Members { return &jsonprotocol.InvalidGreeting{Message: &m} }},
		{ref.AWSQuery, awsquery.Service(), func(m string) awserr.Members { return &awsquery.InvalidGreeting{Message: &m} }},
		{ref.RESTJSON1, restjson.Service(), func(m string) awserr.Members { return &restjson.InvalidGreeting{Message: &m} }},
		{ref.RESTXML, restxml.Service(), func(m string) awserr.Members { return &restxml.InvalidGreeting{Message: &m} }},
		{ref.RPCv2CBOR, rpcv2protocol.Service(), func(m string) awserr.Members { return &rpcv2protocol.InvalidGreeting{Message: &m} }},
	}
}

func encodeError(t *testing.T, w errorWire, e *awserr.Error) (status int, body []byte) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", http.NoBody)
	resp := server.Codecs()[w.protocol].EncodeError(r, w.svc, "rid", e)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Status, b
}

// Every codec writes the error's message under the identity's key and
// never the value's own message member, so the two cannot both appear or
// disagree (smithy-protocol-tests.md 14.7).
func TestEveryCodecWritesTheMessageOnce(t *testing.T) {
	for _, w := range errorWires() {
		e := awserr.WithMembers(w.greeting("value text"), "error text", awserr.Modeled{})
		_, body := encodeError(t, w, e)
		if !bytes.Contains(body, []byte("error text")) || bytes.Contains(body, []byte("value text")) {
			t.Errorf("%s: body %q, want the error's message and not the value's", w.protocol, body)
		}
	}
}

// brokenMembers drives the serializer out of step, closing a list it never
// opened, as generated code that disagrees with its codec would.
type brokenMembers struct{ id awserr.Identity }

func (b brokenMembers) ErrorIdentity() awserr.Identity { return b.id }

func (brokenMembers) Serialize(s smithy.Serializer) {
	s.WriteStruct(nil, nil)
	s.CloseList()
	s.CloseStruct()
}

// A value a codec cannot serialize is a bug, answered with the 500
// InternalFailure rather than a half-written error.
func TestEveryCodecAnswersAnUnserializableValueAsInternal(t *testing.T) {
	for _, w := range errorWires() {
		id := w.greeting("").ErrorIdentity()
		status, body := encodeError(t, w, awserr.WithMembers(brokenMembers{id: id}, "error text", awserr.Modeled{}))
		if status != http.StatusInternalServerError || !bytes.Contains(body, []byte("The request processing has failed")) || bytes.Contains(body, []byte("error text")) {
			t.Errorf("%s: an unserializable value = %d %q, want 500 InternalFailure", w.protocol, status, body)
		}
	}
}
