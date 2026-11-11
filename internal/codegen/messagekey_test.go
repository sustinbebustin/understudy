package codegen_test

import (
	"bytes"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// dev.understudy#messageKey sets the body key of an error's message when
// the wire's spelling is not the model member's: SSM's ValidationException
// declares Message but answers message, and its member-less InvalidFilterKey
// answers Message (ssm-parameters.md 2.2, SSM-01).
func TestMessageKeyOverridesTheMemberSpelling(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`
		"com.example.golden#KindsFault": {"type": "apply", "traits": {"dev.understudy#messageKey": "message", `+evidence+`}},
		"com.example.golden#Throttled": {"type": "apply", "traits": {"dev.understudy#messageKey": "Message", `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	errs := files["errors_gen.go"]
	for _, want := range []string{
		`Shape: "com.example.golden#KindsFault", JSONType: "com.example.golden.v1#KindsFault", QueryCode: "Kinds.Fault", Status: 409, QueryModeStatus: 404, QueryErrorWhen: awserr.QueryErrorInQueryMode, Fault: awserr.Sender, MessageKey: awserr.MessageLower}`,
		`MessageKey: awserr.MessageUpper, Retryable: true`,
		"func ErrThrottled(message string) *awserr.Error {",
	} {
		if !bytes.Contains(errs, []byte(want)) {
			t.Errorf("errors_gen.go has no %s:\n%s", want, errs)
		}
	}
}

func TestMessageKeyFailures(t *testing.T) {
	t.Run("a spelling other than message or Message", func(t *testing.T) {
		_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
			"dev.understudy#messageKey": "msg", `+evidence+`}}`)))
		wantStageError(t, err, codegen.StageOverlay,
			`understudy-gen overlay: com.example.golden#KindsFault: dev.understudy#messageKey: messageKey is "message" or "Message", not "msg"`)
	})
	t.Run("the member's own spelling", func(t *testing.T) {
		_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
			"dev.understudy#messageKey": "Message", `+evidence+`}}`)))
		wantStageError(t, err, codegen.StageIR,
			`understudy-gen ir: com.example.golden#KindsFault: dev.understudy#messageKey is "Message", the member's own spelling; drop the trait`)
	})
}
