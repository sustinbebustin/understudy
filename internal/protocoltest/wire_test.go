package protocoltest_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// wire is a member value in protocol-neutral form. The property tests
// build the body they expect from it, as each protocol writes the value:
// JSON for awsJson and restJson1, CBOR for rpcv2Cbor
// (smithy-protocol-tests.md 10.1, 13.2).
//
//sumtype:decl
type wire interface{ isWire() }

type (
	wString string
	wBool   bool
	wInt    int64
	// wFloat is a float; the body comparison takes floats by value,
	// whatever their width (internal/protocoltest/compare.go).
	wFloat float64
	// wTime is a timestamp in epoch seconds: a JSON number, CBOR tag 1
	// over a float64 (smithy-protocol-tests.md 10.1).
	wTime int64
	wBlob []byte
	wList []wire
	// wObject is a structure or map; member order does not matter to the
	// comparison.
	wObject []wEntry
	// wNull is JSON null or CBOR simple value 22 (smithy-protocol-tests.md
	// 13.2).
	wNull struct{}
	// wUndefined is CBOR simple value 23, which a server reads as null but
	// never writes, so only a request body carries it
	// (smithy-protocol-tests.md 13.1).
	wUndefined struct{}
	// wDoc is a document member, which only the JSON protocols carry.
	wDoc struct{ v document.Value }
)

type wEntry struct {
	key   string
	value wire
}

func (wString) isWire()    {}
func (wBool) isWire()      {}
func (wInt) isWire()       {}
func (wFloat) isWire()     {}
func (wTime) isWire()      {}
func (wBlob) isWire()      {}
func (wList) isWire()      {}
func (wObject) isWire()    {}
func (wNull) isWire()      {}
func (wUndefined) isWire() {}
func (wDoc) isWire()       {}

func jsonOf(t fataler, w wire) any {
	t.Helper()
	switch w := w.(type) {
	case wString:
		return string(w)
	case wBool:
		return bool(w)
	case wInt:
		return json.Number(strconv.FormatInt(int64(w), 10))
	case wFloat:
		return json.Number(strconv.FormatFloat(float64(w), 'g', -1, 64))
	case wTime:
		return json.Number(strconv.FormatInt(int64(w), 10))
	case wBlob:
		return base64.StdEncoding.EncodeToString(w)
	case wList:
		out := make([]any, len(w))
		for i, x := range w {
			out[i] = jsonOf(t, x)
		}
		return out
	case wObject:
		out := make(map[string]any, len(w))
		for _, e := range w {
			out[e.key] = jsonOf(t, e.value)
		}
		return out
	case wNull:
		return nil
	case wUndefined:
		t.Fatalf("JSON has no undefined")
	case wDoc:
		return jsonOfDocument(w.v)
	}
	return nil
}

func jsonOfDocument(v document.Value) any {
	switch v := v.(type) {
	case document.String:
		return string(v)
	case document.Bool:
		return bool(v)
	case document.Number:
		return json.Number(v)
	case document.List:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = jsonOfDocument(x)
		}
		return out
	case document.Map:
		out := make(map[string]any, len(v))
		for _, e := range v {
			out[e.Key] = jsonOfDocument(e.Value)
		}
		return out
	case document.Null:
		return nil
	}
	return nil
}

func cborOf(t fataler, w wire) cbor.Value {
	t.Helper()
	switch w := w.(type) {
	case wString:
		return cbor.Text(w)
	case wBool:
		return cbor.Bool(w)
	case wInt:
		if w >= 0 {
			return cbor.Uint(w)
		}
		return cbor.NegInt(-(w + 1)) //nolint:gosec // w < 0, so -1 - w is in 0 to 2^63-1
	case wFloat:
		return cbor.Float64(w)
	case wTime:
		return cbor.Tag{Number: 1, Content: cbor.Float64(float64(w))}
	case wBlob:
		return cbor.Bytes(w)
	case wList:
		out := make(cbor.Array, len(w))
		for i, x := range w {
			out[i] = cborOf(t, x)
		}
		return out
	case wObject:
		out := make(cbor.Map, len(w))
		for i, e := range w {
			out[i] = cbor.Entry{Key: e.key, Value: cborOf(t, e.value)}
		}
		return out
	case wNull:
		return cbor.Null{}
	case wUndefined:
		return cbor.Undefined{}
	case wDoc:
		// rpcv2Cbor does not support documents (smithy-protocol-tests.md
		// 10.1).
		t.Fatalf("a document member has no rpcv2Cbor form")
	}
	return nil
}

// wireProtocol is one protocol a property test runs through: the codec the
// server registers for it, the media type its bodies are compared as, and
// the headers a client request of it carries.
type wireProtocol struct {
	name      string
	codec     edge.Codec
	mediaType string
	header    map[string]string
	cbor      bool
}

func awsJSON10() wireProtocol {
	return wireProtocol{
		name: "awsJson1_0", codec: server.Codecs()[ref.AWSJSON10], mediaType: "application/x-amz-json-1.0",
		header: map[string]string{"Content-Type": "application/x-amz-json-1.0"},
	}
}

func restJSON1() wireProtocol {
	return wireProtocol{
		name: "restJson1", codec: server.Codecs()[ref.RESTJSON1], mediaType: "application/json",
		header: map[string]string{"Content-Type": "application/json"},
	}
}

// rpcv2CBOR carries the headers of the corpus's rpcv2Cbor requests
// (smithy-protocol-tests.md 13.2 case 14).
func rpcv2CBOR() wireProtocol {
	return wireProtocol{
		name: "rpcv2Cbor", codec: server.Codecs()[ref.RPCv2CBOR], mediaType: "application/cbor",
		header: map[string]string{"Smithy-Protocol": "rpc-v2-cbor", "Content-Type": "application/cbor", "Accept": "application/cbor"},
		cbor:   true,
	}
}

// body renders v as the protocol's body, in the form CompareBody takes:
// base64 for CBOR, as the corpus writes binary bodies.
func (p wireProtocol) body(t fataler, v wire) []byte {
	t.Helper()
	if p.cbor {
		b, err := cbor.Encode(cborOf(t, v))
		if err != nil {
			t.Fatalf("encoding the expected CBOR: %v", err)
		}
		return []byte(base64.StdEncoding.EncodeToString(b))
	}
	b, err := json.Marshal(jsonOf(t, v))
	if err != nil {
		t.Fatalf("encoding the expected JSON: %v", err)
	}
	return b
}

// uriOf is the path the corpus's request cases of op are sent to, which a
// REST codec matches against op's URI pattern; "/" when the corpus has no
// request case for op.
func uriOf(op *registry.Operation) string {
	for _, cs := range corpusServices() {
		requests := cs.suite().Requests
		for i := range requests {
			if requests[i].Operation == op {
				return requests[i].URI
			}
		}
	}
	return "/"
}

func (p wireProtocol) request(op *registry.Operation, body []byte) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://localhost"+uriOf(op), bytes.NewReader(body))
	for k, v := range p.header {
		r.Header.Set(k, v)
	}
	return r
}

// encode serializes out as op's response body.
func (p wireProtocol) encode(t fataler, op *registry.Operation, out smithy.Serializable) []byte {
	t.Helper()
	resp, err := p.codec.Encode(p.request(op, nil), op, "rid", out, http.StatusOK)
	if err != nil {
		t.Fatalf("%s: Encode(%T) = %s", p.name, out, err.Message)
	}
	b, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("%s: reading the encoded body: %v", p.name, rerr)
	}
	return b
}

// decode reads body, as a client request of op sends it, into in.
func (p wireProtocol) decode(t fataler, op *registry.Operation, body []byte, in smithy.Deserializable) {
	t.Helper()
	r := p.request(op, body)
	if err := p.codec.DecodeBody(r, op, gen.TextBody(body), in); err != nil {
		t.Fatalf("%s: DecodeBody(%q) = %s (%v)", p.name, body, err.Message, err.AsError())
	}
}

// compare compares a body the codec wrote with the expected one.
func (p wireProtocol) compare(t fataler, want, got []byte) {
	t.Helper()
	if err := protocoltest.CompareBody(p.mediaType, nil, want, got); err != nil {
		shown := got
		if p.cbor {
			shown = []byte(base64.StdEncoding.EncodeToString(got))
		}
		t.Fatalf("%s body: %v\ngot  %s\nwant %s", p.name, err, shown, want)
	}
}
