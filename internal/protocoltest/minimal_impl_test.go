package protocoltest_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// OperationWithNestedStructure's only awsJson1_0 request case is skipped,
// so its response case 8 is sent the bench's own request. The input
// requires topLevel, and TopLevel requires dialog: the smallest body the
// generated validator accepts is {"topLevel":{"dialog":{}}}
// (smithy-protocol-tests.md 10.6, 14.6).
func TestMinimalRequestFillsRequiredStructuresWithEmptyOnes(t *testing.T) {
	op := jsonrpc10.OpOperationWithNestedStructure.Descriptor()
	got, err := minimalRequest(op)
	if err != nil {
		t.Fatal(err)
	}
	want := wireRequest{method: http.MethodPost, uri: "/", body: `{"topLevel":{"dialog":{}}}`, headers: map[string]string{
		"Content-Type": "application/x-amz-json-1.0",
		"X-Amz-Target": "JsonRpc10.OperationWithNestedStructure",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("minimalRequest = %#v\nwant %#v", got, want)
	}
	var in jsonrpc10.OperationWithNestedStructureInput
	awsJSON10().decode(t, op, []byte(got.body), &in)
	if vs := in.Validate(); len(vs) != 0 {
		t.Errorf("the minimal request's input violates the generated constraints: %v", vs)
	}
}

// requiredString is a required string member without a default: a value
// the bench cannot choose for the handler.
func requiredString(name string) *smithy.Member {
	return &smithy.Member{Name: name, Required: true, Target: &smithy.Schema{ID: "smithy.api#String", Type: smithy.TypeString}}
}

func structure(id smithy.ShapeID, members ...*smithy.Member) *smithy.Schema {
	return &smithy.Schema{ID: id, Type: smithy.TypeStructure, Members: members}
}

func syntheticOp(svc *registry.Service, input *smithy.Schema) *registry.Operation {
	return &registry.Operation{Service: svc, ID: ref.OperationID{Service: "synthetic", Name: "Put"}, Input: input}
}

func awsJSONService() *registry.Service {
	return &registry.Service{Protocols: []ref.ProtocolID{ref.AWSJSON10}, JSONTargetPrefix: "Synthetic"}
}

// A required member with a default never needs a value, since the server
// fills it in (smithy-protocol-tests.md 10.2), and an optional structure is
// left out, so neither changes the empty body.
func TestMinimalRequestLeavesOutDefaultedAndOptionalMembers(t *testing.T) {
	defaulted := requiredString("greeting")
	defaulted.Default = document.String("hi")
	optional := &smithy.Member{Name: "dialog", Target: structure("synthetic#Dialog", requiredString("language"))}
	got, err := minimalRequest(syntheticOp(awsJSONService(), structure("synthetic#PutInput", defaulted, optional)))
	if err != nil {
		t.Fatal(err)
	}
	if got.body != "{}" {
		t.Errorf("body = %s, want {}", got.body)
	}
}

// A required REST label is filled by the URI pattern, so it asks nothing
// of the body (smithy-protocol-tests.md 10.6).
func TestMinimalRequestLeavesRequiredLabelsToThePath(t *testing.T) {
	label := requiredString("id")
	label.HTTP = smithy.HTTPLabel{}
	op := syntheticOp(&registry.Service{Protocols: []ref.ProtocolID{ref.RESTJSON1}}, structure("synthetic#PutInput", label))
	op.HTTP = &registry.HTTPBinding{Method: http.MethodPut, Path: []registry.Segment{registry.Literal{Value: "things"}, registry.Label{Name: "id"}}}
	got, err := minimalRequest(op)
	if err != nil {
		t.Fatal(err)
	}
	if want := (wireRequest{method: http.MethodPut, uri: "/things/label"}); !reflect.DeepEqual(got, want) {
		t.Errorf("minimalRequest = %#v\nwant %#v", got, want)
	}
}

// The bench never invents a scalar for the handler, nor a body in a
// protocol it does not write: it fails, naming the operation and the
// member, so a corpus bump that needs more is seen rather than sent an
// input the server rejects (smithy-protocol-tests.md 14.6).
func TestMinimalRequestRefusesRequiredMembersItCannotFill(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   *registry.Operation
		want []string
	}{
		{
			name: "nested scalar",
			op: syntheticOp(awsJSONService(), structure("synthetic#PutInput",
				&smithy.Member{Name: "topLevel", Required: true, Target: structure("synthetic#TopLevel", requiredString("name"))})),
			want: []string{"synthetic.Put", "topLevel.name", "string"},
		},
		{
			name: "structure in awsQuery",
			op: syntheticOp(&registry.Service{Protocols: []ref.ProtocolID{ref.AWSQuery}, QueryVersion: "2020-01-08"}, structure("synthetic#PutInput",
				&smithy.Member{Name: "topLevel", Required: true, Target: structure("synthetic#TopLevel")})),
			want: []string{"synthetic.Put", "topLevel", "awsquery"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := minimalRequest(tc.op)
			if err == nil {
				t.Fatalf("minimalRequest = %#v, want an error", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
		})
	}
}
