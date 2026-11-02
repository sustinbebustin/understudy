package codegen_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// TestServiceConfigBoundOperations: boundOperations is optional, and when
// present is a sorted list of distinct operation names, so a newly bound
// operation is a one-line diff in a predictable place.
func TestServiceConfigBoundOperations(t *testing.T) {
	const head = `{"service": "com.amazonaws.sqs#AmazonSQS", "package": "sqsapi", "model": "sqs", "iamPrefix": "sqs"`
	const prefix = "understudy-gen load: codegen/services/sqs.json: boundOperations: "
	tests := []struct {
		name, field string
		want        codegen.Wiring
		err         string
	}{
		{name: "absent", want: codegen.WireAll{}},
		{name: "empty", field: `[]`, want: codegen.WireBound{Operations: []string{}}},
		{name: "sorted", field: `["CreateQueue", "DeleteQueue"]`, want: codegen.WireBound{Operations: []string{"CreateQueue", "DeleteQueue"}}},
		{name: "unsorted", field: `["DeleteQueue", "CreateQueue"]`, err: prefix + "CreateQueue comes after DeleteQueue; keep the list sorted"},
		{name: "twice", field: `["CreateQueue", "CreateQueue"]`, err: prefix + "CreateQueue is listed twice; remove one"},
		{name: "not a name", field: `["createQueue"]`, err: prefix + `"createQueue" is not an operation name such as CreateQueue`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := head + "}"
			if tt.field != "" {
				doc = head + `, "boundOperations": ` + tt.field + "}"
			}
			cfg, err := codegen.ParseServiceConfig(strings.NewReader(doc), "sqs")
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("ParseServiceConfig = %v\nwant %s", err, tt.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(cfg.Wiring, tt.want) {
				t.Fatalf("ParseServiceConfig = %#v, %v; want Wiring %#v", cfg.Wiring, err, tt.want)
			}
		})
	}
}

// TestWireBoundPrunesUnboundClosures: under WireBound, the bound operations'
// inputs and outputs and every error shape carry their members, with what
// they reach; any other shape keeps its ID and traits but no members, while
// its member variables stay declared, with Targets, for the serde code. An
// unbound input with a @streaming member keeps its own members, because
// signature verification reads them before the 501, but not its targets'.
func TestWireBoundPrunesUnboundClosures(t *testing.T) {
	in := goldenInputs(t)
	model := string(in.Model.Content)
	for _, e := range []struct{ old, new string }{
		{
			"\"NextToken\": {\"target\": \"com.example.golden#String\"}\n      }\n    },\n    \"com.example.golden#ListPartsResponse\": {",
			"\"NextToken\": {\"target\": \"com.example.golden#String\"}, \"Upload\": {\"target\": \"com.example.golden#Upload\"}\n      }\n    },\n" +
				"    \"com.example.golden#Upload\": {\"type\": \"blob\", \"traits\": {\"smithy.api#streaming\": {}}},\n    \"com.example.golden#ListPartsResponse\": {",
		},
	} {
		if n := strings.Count(model, e.old); n != 1 {
			t.Fatalf("the fixture model holds %s %d times, want once; update the anchor", e.old, n)
		}
		model = strings.Replace(model, e.old, e.new, 1)
	}
	in.Model.Content = []byte(model)
	in.Config.Wiring = codegen.WireBound{Operations: []string{"GetThing"}}
	files, err := codegen.Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	src := files["schema_gen.go"]
	mustContain(t, "schema_gen.go", src,
		// the bound operation's closure
		`var schemaGetThingInput = smithy.Schema{ID: "com.example.golden#GetThingRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{&memberGetThingInput_ThingId}}`,
		`var schemaGetThingOutput = smithy.Schema{ID: "com.example.golden#GetThingResponse", Type: smithy.TypeStructure, Members: []*smithy.Member{`,
		// an error shape, although only unbound PutKinds names it
		`var schemaKindsFault = smithy.Schema{ID: "com.example.golden#KindsFault", Type: smithy.TypeStructure, Members: []*smithy.Member{&memberKindsFault_Message}, Error: `,
		// unbound closures: stubs, their members still declared
		`var schemaPutKindsInput = smithy.Schema{ID: "com.example.golden#PutKindsRequest", Type: smithy.TypeStructure}`,
		`var memberPutKindsInput_Tags = smithy.Member{Name: "Tags", Index: 16, Target: &schemaTagList}`,
		`var schemaTagList = smithy.Schema{ID: "com.example.golden#TagList", Type: smithy.TypeList}`,
		`var schemaSweepInput = smithy.Schema{ID: "com.example.golden#SweepRequest", Type: smithy.TypeStructure}`,
		// the streaming input keeps its members, its output does not
		`var schemaListPartsInput = smithy.Schema{ID: "com.example.golden#ListPartsRequest", Type: smithy.TypeStructure, Members: []*smithy.Member{&memberListPartsInput_ThingId, &memberListPartsInput_NextToken, &memberListPartsInput_Upload}}`,
		`var schemaListPartsOutput = smithy.Schema{ID: "com.example.golden#ListPartsResponse", Type: smithy.TypeStructure}`,
		`var schemaNestedList = smithy.Schema{ID: "com.example.golden#NestedList", Type: smithy.TypeList}`,
		`var schemaUpload = smithy.Schema{ID: "com.example.golden#Upload", Type: smithy.TypeBlob, Streaming: true}`,
	)
}

// TestBoundOperationsNameTypedOperations: each bound operation is a modeled
// or internal operation of the model; a raw one has no schemas to wire.
func TestBoundOperationsNameTypedOperations(t *testing.T) {
	const prefix = "understudy-gen ir: codegen/services/golden.json: boundOperations names "
	tests := []struct{ name, op, err string }{
		{name: "unknown", op: "PutKind", err: prefix + "PutKind, which the model has no operation for; correct the name or remove it"},
		{name: "raw", op: "Upload", err: prefix + "Upload, a raw operation, which has no schemas to wire; remove it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := goldenInputs(t)
			in.Config.RawOperations = []codegen.RawOperation{{Name: "Upload"}}
			in.Config.Wiring = codegen.WireBound{Operations: []string{tt.op}}
			_, err := codegen.Generate(in)
			var gerr *codegen.Error
			if !errors.As(err, &gerr) || gerr.Stage != codegen.StageIR || err.Error() != tt.err {
				t.Fatalf("Generate = %v\nwant %s", err, tt.err)
			}
		})
	}
}
