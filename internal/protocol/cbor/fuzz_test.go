package cbor_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
)

// corpusModel is the vendored Smithy protocol compliance corpus, read for
// its rpcv2Cbor bodies (third_party/smithy-protocol-tests/README.md).
const corpusModel = "../../../third_party/smithy-protocol-tests/model.json"

// rpcv2CborBodies returns the decoded body of every rpcv2Cbor request and
// response compliance case that has one: the bytes Smithy clients and
// servers put on the wire, as fuzz seeds.
func rpcv2CborBodies(f *testing.F) [][]byte {
	f.Helper()
	raw, err := os.ReadFile(corpusModel)
	if err != nil {
		f.Fatalf("reading the protocol corpus: %v", err)
	}
	var model struct {
		Shapes map[string]struct {
			Traits map[string]json.RawMessage `json:"traits"`
		} `json:"shapes"`
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		f.Fatalf("parsing %s: %v", corpusModel, err)
	}
	var bodies [][]byte
	// Sorted, so a seed keeps its number from run to run.
	for _, name := range slices.Sorted(maps.Keys(model.Shapes)) {
		shape := model.Shapes[name]
		for _, trait := range []string{"smithy.test#httpRequestTests", "smithy.test#httpResponseTests"} {
			cases, ok := shape.Traits[trait]
			if !ok {
				continue
			}
			var list []struct {
				ID       string `json:"id"`
				Protocol string `json:"protocol"`
				Body     string `json:"body"`
			}
			if err := json.Unmarshal(cases, &list); err != nil {
				f.Fatalf("parsing %s on %s: %v", trait, name, err)
			}
			for _, c := range list {
				if c.Protocol != "smithy.protocols#rpcv2Cbor" || c.Body == "" {
					continue
				}
				body, err := base64.StdEncoding.DecodeString(c.Body)
				if err != nil {
					f.Fatalf("case %s: body is not base64: %v", c.ID, err)
				}
				bodies = append(bodies, body)
			}
		}
	}
	if len(bodies) == 0 {
		f.Fatalf("%s holds no rpcv2Cbor case with a body; the corpus layout changed", corpusModel)
	}
	return bodies
}

// FuzzDecode checks the two properties malformed-input handling rests on
// (wire-cbor.md section 7): Decode never panics, failing only with a
// *MalformedError that names its problem, and a tree it accepts encodes
// canonically and decodes back to itself. Seeds are the rpcv2Cbor
// compliance bodies plus every regression input under testdata/fuzz.
func FuzzDecode(f *testing.F) {
	for _, body := range rpcv2CborBodies(f) {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		v, err := cbor.Decode(body)
		if err != nil {
			if malformed, ok := errors.AsType[*cbor.MalformedError](err); !ok || malformed.Problem == 0 {
				t.Fatalf("Decode(%x) error = %v, want a *MalformedError naming its problem", body, err)
			}
			return
		}
		canonical, err := cbor.Encode(v)
		if err != nil {
			t.Fatalf("Encode of the tree decoded from %x: %v", body, err)
		}
		again, err := cbor.Decode(canonical)
		if err != nil {
			t.Fatalf("accepted %x but rejected its canonical form %x: %v", body, canonical, err)
		}
		if !sameValue(again, v) {
			t.Fatalf("accepted %x as\n%s\nbut its canonical form %x decodes to\n%s", body, diag(v), canonical, diag(again))
		}
		if twice, err := cbor.Encode(again); err != nil || !bytes.Equal(twice, canonical) {
			t.Fatalf("re-encoding is not canonical: %x then %x (%v)", canonical, twice, err)
		}
	})
}
