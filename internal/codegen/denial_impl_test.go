package codegen_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// accessDenied is an error structure outside the golden namespace, which no
// operation lists, so only the trait naming it brings it into the closure.
const accessDenied = `"com.amazon.coral.service#AccessDenied": {"type": "structure",
	"members": {"message": {"target": "smithy.api#String"}},
	"traits": {"smithy.api#error": "client", "smithy.api#httpError": 403,
		"dev.understudy#jsonType": "AccessDeniedException", ` + evidence + `}}`

// The overlay's denialError becomes DenialError() and the descriptor's
// Denial, labelled with the evidence item covering the trait, not the
// entry's other items (iam-enforcement-surfaces.md 12.3).
func TestDenialErrorReachesTheDescriptor(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(accessDenied+`,
		"com.example.golden#Golden": {"type": "apply", "traits": {
			"dev.understudy#denialError": "com.amazon.coral.service#AccessDenied",
			"dev.understudy#emptyCollection": "omit",
			"dev.understudy#evidence": [
				{"label": "aws-doc", "source": "x", "covers": ["dev.understudy#denialError"]},
				{"label": "inferred", "source": "y", "covers": ["dev.understudy#emptyCollection"]}
			]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"errors_gen.go":  "func DenialError() awserr.Identity { return IdentityAccessDenied() }",
		"service_gen.go": `Denial: registry.Denial{Identity: IdentityAccessDenied(), Label: "aws-doc"},`,
	} {
		// gofmt aligns the descriptor's fields, so spacing is not compared.
		if !strings.Contains(strings.Join(strings.Fields(string(files[file])), " "), want) {
			t.Errorf("%s has no %s:\n%s", file, want, files[file])
		}
	}
	// The shape is generated although no operation lists it, with the
	// awsJson1_0 __type in its own namespace.
	const identity = `Shape: "com.amazon.coral.service#AccessDenied", JSONType: "com.amazon.coral.service#AccessDeniedException", ` +
		`QueryCode: "AccessDenied", Status: 403`
	if !bytes.Contains(files["errors_gen.go"], []byte(identity)) {
		t.Errorf("errors_gen.go has no identity %s:\n%s", identity, files["errors_gen.go"])
	}
}

// Without the trait a service has no DenialError and its descriptor's
// Denial stays zero, which the kernel answers with a 500 naming the
// missing trait rather than a borrowed identity (invariant 3).
func TestNoDenialErrorWithoutTheTrait(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(files["errors_gen.go"], []byte("DenialError")) || bytes.Contains(files["service_gen.go"], []byte("Denial:")) {
		t.Errorf("golden names no denialError, yet the generated package has one:\n%s\n%s", files["errors_gen.go"], files["service_gen.go"])
	}
}

func TestDenialErrorFailures(t *testing.T) {
	t.Run("a shape that is not an error", func(t *testing.T) {
		_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
			"dev.understudy#denialError": "com.example.golden#Nested", `+evidence+`}}`)))
		wantStageError(t, err, codegen.StageIR,
			"understudy-gen ir: com.example.golden#Golden: dev.understudy#denialError names com.example.golden#Nested, which is not an error structure")
	})
	t.Run("a trait no overlay evidence covers", func(t *testing.T) {
		// Only an overlay entry carries evidence, so a model that sets the
		// trait itself has nothing to label the descriptor with.
		in := goldenInputs(t)
		const at = `"aws.protocols#awsJson1_0": {},`
		if !bytes.Contains(in.Model.Content, []byte(at)) {
			t.Fatalf("the golden model no longer has %s", at)
		}
		in.Model.Content = bytes.Replace(in.Model.Content, []byte(at),
			[]byte(at+` "dev.understudy#denialError": "com.example.golden#KindsFault",`), 1)
		_, err := codegen.Generate(in)
		wantStageError(t, err, codegen.StageIR,
			"understudy-gen ir: com.example.golden#Golden: dev.understudy#denialError names com.example.golden#KindsFault, "+
				"but no overlay evidence covers it; apply the trait from an overlay entry whose dev.understudy#evidence covers it")
	})
	t.Run("a trait on an operation", func(t *testing.T) {
		_, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Ping": {"type": "apply", "traits": {
			"dev.understudy#denialError": "com.example.golden#KindsFault", `+evidence+`}}`)))
		wantStageError(t, err, codegen.StageOverlay,
			"understudy-gen overlay: com.example.golden#Ping: dev.understudy#denialError belongs on a service shape")
	})
}

func wantStageError(t *testing.T, err error, stage codegen.Stage, want string) {
	t.Helper()
	var gerr *codegen.Error
	if !errors.As(err, &gerr) {
		t.Fatalf("Generate = %v, want a *codegen.Error", err)
	}
	if gerr.Stage != stage || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("Generate failed at %s with\n%s\nwant %s starting\n%s", gerr.Stage, err, stage, want)
	}
}
