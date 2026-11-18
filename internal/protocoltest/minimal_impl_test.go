package protocoltest_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
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
