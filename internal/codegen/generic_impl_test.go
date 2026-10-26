package codegen_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// A descriptor carries its package's GenericErrors when the service speaks
// a family with a vocabulary, so the fault catalogue reads the service
// overlay's corrections (DynamoDB's 400 AccessDeniedException) instead of
// awserr.Catalogue; a service with no vocabulary carries none.
func TestGenericErrorsReachTheDescriptor(t *testing.T) {
	with, err := codegen.Generate(goldenInputs(t, overlay(`"dev.understudy.defaults#QueryFamily": {"type": "service",
		"errors": [{"target": "com.example.golden#KindsFault"}], "traits": {`+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	// gofmt aligns the descriptor's fields, so spacing is not compared.
	if src := strings.Join(strings.Fields(string(with["service_gen.go"])), " "); !strings.Contains(src, "GenericErrors: GenericErrors,") {
		t.Errorf("service_gen.go does not carry GenericErrors:\n%s", with["service_gen.go"])
	}
	without, err := codegen.Generate(goldenInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(without["service_gen.go"], []byte("GenericErrors")) {
		t.Errorf("golden has no vocabulary, yet service_gen.go names GenericErrors:\n%s", without["service_gen.go"])
	}
}
