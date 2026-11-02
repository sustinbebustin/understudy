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
