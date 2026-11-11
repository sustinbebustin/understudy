package codegen_test

import (
	"bytes"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// An absolute dev.understudy#jsonType names the awsJson1_0 __type outright,
// namespace included, without renaming the shape or its Query code: SQS's
// InvalidParameterValue answers com.amazon.coral.service#InvalidParameterValueException
// (testdata/parity/sqs/R11/02-receive-max-0.json).
func TestAbsoluteJSONTypeNamesTheNamespace(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`
		"com.example.golden#KindsFault": {"type": "apply", "traits": {"dev.understudy#jsonType": "com.amazon.coral.service#KindsException", `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	want := `Shape: "com.example.golden#KindsFault", JSONType: "com.amazon.coral.service#KindsException", QueryCode: "Kinds.Fault"`
	if errs := files["errors_gen.go"]; !bytes.Contains(errs, []byte(want)) {
		t.Errorf("errors_gen.go has no %s:\n%s", want, errs)
	}
}

func TestJSONTypeRefusesAMalformedShapeID(t *testing.T) {
	_, err := codegen.Generate(goldenInputs(t, overlay(`
		"com.example.golden#KindsFault": {"type": "apply", "traits": {"dev.understudy#jsonType": "com.amazon.coral.service#", `+evidence+`}}`)))
	wantStageError(t, err, codegen.StageOverlay,
		`understudy-gen overlay: com.example.golden#KindsFault: dev.understudy#jsonType: "com.amazon.coral.service#" is not an absolute shape id: the form is namespace#Name, such as com.amazonaws.sqs#QueueDoesNotExist; or give a bare name such as InvalidParameterValueException`)
}
