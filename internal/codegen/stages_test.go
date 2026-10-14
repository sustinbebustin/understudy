package codegen_test

import (
	"bytes"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/sustinbebustin/understudy/internal/codegen"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// evidence is the evidence trait every extra overlay entry carries.
const evidence = `"dev.understudy#evidence": {"label": "inferred", "source": "internal/codegen/stages_test.go"}`

// overlay wraps shape entries into an overlay document.
func overlay(entries string) string {
	return `{"smithy": "2.0", "shapes": {` + entries + `}}`
}

// TestStageFailures: each case feeds the fixture plus one extra overlay
// through Generate and expects the stage and the exact message, which names
// the input and the fix.
func TestStageFailures(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		stage codegen.Stage
		want  string
	}{
		{
			name: "unclassified trait",
			extra: overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
				"com.example.vendor#flavor": {}, ` + evidence + `}}`),
			stage: codegen.StageClassify,
			want: "understudy-gen classify: com.example.golden#Golden: trait com.example.vendor#flavor is unclassified; " +
				"add it to traitClasses in internal/codegen/trait.go as honor, generation-only or ignore (smithy-models.md 4.3)",
		},
		{
			name: "queryKeyAlias naming another key",
			extra: overlay(`"com.example.golden#PutKindsRequest$Name": {"type": "apply", "traits": {
				"dev.understudy#queryKeyAlias": {"key": "Other", "decode": "ignore"}, ` + evidence + `}}`),
			stage: codegen.StageEffective,
			want: "understudy-gen effective: com.example.golden#PutKindsRequest$Name: dev.understudy#queryKeyAlias key " +
				`"Other" is not the member's awsQuery key "Name"; the alias names the key the member already has`,
		},
		{
			name: "notConfigured naming a shape that is not an error",
			extra: overlay(`"com.example.golden#GetThing": {"type": "apply", "traits": {
				"dev.understudy#notConfigured": {"error": "com.example.golden#Nested"}, ` + evidence + `}}`),
			stage: codegen.StageIR,
			want: "understudy-gen ir: com.example.golden#GetThing: dev.understudy#notConfigured names com.example.golden#Nested, " +
				"which is not an error structure of golden",
		},
		{
			name: "overlay operation no service reaches",
			extra: overlay(`"com.example.golden#Rogue": {"type": "operation", "input": {"target": "smithy.api#Unit"}, "traits": {
				` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#Rogue: defines an operation without dev.understudy#internalOperation; " +
				"no service lists an overlay operation, so it would never be generated. Mark it internal, or leave modeled operations to the model",
		},
		{
			name: "overlay conflict with the model",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"smithy.api#httpError": 400, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#KindsFault: smithy.api#httpError conflicts with the value " +
				`already present: model 409, overlay 400; set "replace": true if the overlay must win`,
		},
		{
			name: "overlay duplicates the model",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"smithy.api#httpError": 409, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#KindsFault: smithy.api#httpError duplicates the value " +
				"already present (409); delete the overlay entry, the model now carries it",
		},
		{
			name: "member overlay conflicts with its target shape",
			extra: overlay(`"com.example.golden#PutKindsRequest$Name": {"type": "apply", "traits": {
				"smithy.api#length": {"min": 1, "max": 16}, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#PutKindsRequest$Name: smithy.api#length conflicts with the value " +
				`the member's target com.example.golden#Name carries: model {"max":8,"min":1}, overlay {"max":16,"min":1}; ` +
				`set "replace": true if the overlay must win`,
		},
		{
			name: "member overlay duplicates its target shape",
			extra: overlay(`"com.example.golden#PutKindsRequest$Name": {"type": "apply", "traits": {
				"smithy.api#pattern": "^[a-z]+$", ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#PutKindsRequest$Name: smithy.api#pattern duplicates the value " +
				`the member's target com.example.golden#Name carries ("^[a-z]+$"); delete the overlay entry, the model now carries it`,
		},
		{
			name: "overlay repeats a shape key",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#statusOverride": 422, ` + evidence + `}},
				"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#jsonType": "X", ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want:  `understudy-gen overlay: extra.json: shape "com.example.golden#KindsFault" appears twice; merge the two into one`,
		},
		{
			name: "overlay entry repeats a trait id",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#statusOverride": 422, "dev.understudy#statusOverride": 400, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: `understudy-gen overlay: extra.json: com.example.golden#KindsFault: trait "dev.understudy#statusOverride" ` +
				"appears twice; merge the two into one",
		},
		{
			name: "overlay shape member repeats a trait id",
			extra: overlay(`"com.example.golden#Extra": {"type": "structure", "members": {
				"x": {"target": "smithy.api#String", "traits": {"smithy.api#required": {}, "smithy.api#required": {}}}},
				"traits": {` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: `understudy-gen overlay: extra.json: com.example.golden#Extra: member "x": trait "smithy.api#required" ` +
				"appears twice; merge the two into one",
		},
		{
			name: "replace that replaces nothing",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "replace": true, "traits": {
				"dev.understudy#statusOverride": 400, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: `understudy-gen overlay: extra.json: com.example.golden#KindsFault: "replace" is set but no trait of the entry ` +
				`replaces one already present; drop "replace"`,
		},
		{
			name: "queryErrorEnvelope declaring a namespace the service lacks",
			extra: overlay(`"com.example.golden#Golden": {"type": "apply", "replace": true, "traits": {
				"dev.understudy#queryErrorEnvelope": {"namespace": true}, ` + evidence + `}}`),
			stage: codegen.StageIR,
			want: "understudy-gen ir: com.example.golden#Golden: dev.understudy#queryErrorEnvelope declares the namespace on " +
				"ErrorResponse, but the service has no smithy.api#xmlNamespace to declare",
		},
		{
			name: "queryErrorEnvelope that changes nothing",
			extra: overlay(`"com.example.golden#Golden": {"type": "apply", "replace": true, "traits": {
				"dev.understudy#queryErrorEnvelope": {"detail": false}, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: com.example.golden#Golden: dev.understudy#queryErrorEnvelope: " +
				`set "namespace" or "detail" to true; an envelope that follows Smithy's example needs no trait`,
		},
		{
			name: "unknown dev.understudy trait",
			extra: overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
				"dev.understudy#flavor": "vanilla", ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#Golden: dev.understudy#flavor is not a dev.understudy trait; " +
				"the closed set is [dev.understudy#alwaysEmit dev.understudy#emptyCollection dev.understudy#errorNamespace " +
				"dev.understudy#evidence dev.understudy#internalOperation dev.understudy#jsonType dev.understudy#malformedRequestError " +
				"dev.understudy#maxRequestBytes dev.understudy#messageByProtocol dev.understudy#notConfigured dev.understudy#patternEcho " +
				"dev.understudy#patternValidator dev.understudy#queryErrorEnvelope dev.understudy#queryKeyAlias " +
				"dev.understudy#statusOverride dev.understudy#successCode " +
				"dev.understudy#throttleError dev.understudy#unknownOperationError dev.understudy#validationError " +
				"dev.understudy#validationPathStyle]",
		},
		{
			name: "dev.understudy trait on the wrong site",
			extra: overlay(`"com.example.golden#PutKindsRequest": {"type": "apply", "traits": {
				"dev.understudy#jsonType": "Nope", ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want:  "understudy-gen overlay: com.example.golden#PutKindsRequest: dev.understudy#jsonType belongs on an error structure",
		},
		{
			name: "entry without evidence",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#statusOverride": 400}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#KindsFault: has no dev.understudy#evidence; " +
				"every overlay entry states its evidence {label, source, validated?}",
		},
		{
			name: "evidence with an unknown label",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#statusOverride": 400,
				"dev.understudy#evidence": {"label": "hearsay", "source": "a friend"}}}`),
			stage: codegen.StageOverlay,
			want: `understudy-gen overlay: extra.json: com.example.golden#KindsFault: dev.understudy#evidence: item 0: ` +
				`evidence label "hearsay" is unknown; use one of ["aws-recorded" "third-party-recorded" "aws-doc" "sdk-source" "inferred"]`,
		},
		{
			name: "evidence list that leaves a trait uncovered",
			extra: overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
				"dev.understudy#statusOverride": 400, "dev.understudy#jsonType": "Fault",
				"dev.understudy#evidence": [{"label": "inferred", "source": "x", "covers": ["dev.understudy#jsonType"]}]}}`),
			stage: codegen.StageOverlay,
			want: "understudy-gen overlay: extra.json: com.example.golden#KindsFault: dev.understudy#evidence: " +
				"no evidence item covers dev.understudy#statusOverride",
		},
		{
			name: "apply to a shape the model lacks",
			extra: overlay(`"com.example.golden#Missing": {"type": "apply", "traits": {
				"smithy.api#sensitive": {}, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want:  "understudy-gen overlay: extra.json: com.example.golden#Missing: applies to com.example.golden#Missing, which is not in the model",
		},
		{
			name: "apply to a member the shape lacks",
			extra: overlay(`"com.example.golden#Nested$Colour": {"type": "apply", "traits": {
				"smithy.api#sensitive": {}, ` + evidence + `}}`),
			stage: codegen.StageOverlay,
			want:  `understudy-gen overlay: extra.json: com.example.golden#Nested$Colour: com.example.golden#Nested has no member "Colour"`,
		},
		{
			name: "lookaround pattern without a validator",
			extra: overlay(`"com.example.golden#GetThingRequest$ThingId": {"type": "apply", "traits": {
				"smithy.api#pattern": "^(?!x).+$", ` + evidence + `}}`),
			stage: codegen.StageRegex,
			want: `understudy-gen regex: com.example.golden#GetThingRequest$ThingId: pattern "^(?!x).+$" has a lookaround at byte 1, ` +
				"which RE2 cannot compile; give the string shape dev.understudy#patternValidator {allOf, noneOf} with an RE2 rendering",
		},
		{
			name: "sensitive map key in an indexed validation path",
			extra: overlay(`"com.example.golden#SecretMap$key": {"type": "apply", "traits": {
				"smithy.api#sensitive": {}, ` + evidence + `}}`),
			stage: codegen.StageEmit,
			want: "understudy-gen emit: com.example.golden#SecretMap$key: the key is sensitive, but an indexed validation path " +
				"(<map>.<key>.member) would echo it; give the service dev.understudy#validationPathStyle rolledUp " +
				"or remove the constraints below this map",
		},
		{
			name: "validation code that is not an error",
			extra: overlay(`"com.example.golden#Nested$Code": {"type": "apply", "traits": {
				"dev.understudy#validationError": {"code": "com.example.golden#Nested"}, ` + evidence + `}}`),
			stage: codegen.StageIR,
			want: "understudy-gen ir: com.example.golden#Nested$Code: dev.understudy#validationError code com.example.golden#Nested " +
				"is not an error structure in the model or an overlay",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codegen.Generate(goldenInputs(t, tt.extra))
			var gerr *codegen.Error
			if !errors.As(err, &gerr) {
				t.Fatalf("Generate = %v, want a *codegen.Error", err)
			}
			if gerr.Stage != tt.stage || err.Error() != tt.want {
				t.Fatalf("Generate failed at %s with\n%s\nwant %s with\n%s", gerr.Stage, err, tt.stage, tt.want)
			}
		})
	}
}

// TestPatternPolicyHandlerDropsTheGeneratedCheck: a member-level
// validationError {patternPolicy: handler} leaves the member's pattern to
// the handler, so the generated validator does not check it and the
// service's own error answers (S3's "Invalid copy source object key").
func TestPatternPolicyHandlerDropsTheGeneratedCheck(t *testing.T) {
	const pattern = `"smithy.api#pattern": "^[a-z]+/[a-z]+$", `
	checked, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#GetThingRequest$ThingId": {"type": "apply", "traits": {
		`+pattern+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(checked["validate_gen.go"], []byte("[a-z]+/[a-z]+")) {
		t.Fatalf("without a policy the pattern is not checked:\n%s", checked["validate_gen.go"])
	}
	handled, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#GetThingRequest$ThingId": {"type": "apply", "traits": {
		`+pattern+`"dev.understudy#validationError": {"patternPolicy": "handler"}, `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(handled["validate_gen.go"], []byte("[a-z]+/[a-z]+")) {
		t.Fatalf("with patternPolicy handler the pattern is still checked:\n%s", handled["validate_gen.go"])
	}
}

// TestReplaceLetsTheOverlayWin: with "replace": true the overlay's value
// replaces the model's, so a status the model gets wrong is corrected.
func TestReplaceLetsTheOverlayWin(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#KindsFault": {"type": "apply", "replace": true, "traits": {
		"smithy.api#httpError": 400, `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(files["errors_gen.go"], []byte(`QueryCode: "Kinds.Fault", Status: 400, QueryModeStatus: 404`)) {
		t.Fatalf("KindsFault keeps the model's status:\n%s", files["errors_gen.go"])
	}
}

// TestQueryKeyAliasReachesTheSchema: the overlay's queryKeyAlias becomes the
// member's smithy.QueryKeyAlias, which the awsQuery decoder applies.
func TestQueryKeyAliasReachesTheSchema(t *testing.T) {
	for _, tt := range []struct{ decode, want string }{
		{"receive", `QueryKeyAlias: &smithy.QueryKeyAlias{Key: "Name", Decode: smithy.AliasReceive}`},
		{"ignore", `QueryKeyAlias: &smithy.QueryKeyAlias{Key: "Name", Decode: smithy.AliasIgnore}`},
	} {
		files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#PutKindsRequest$Name": {"type": "apply", "traits": {
			"dev.understudy#queryKeyAlias": {"key": "Name", "decode": "`+tt.decode+`"}, `+evidence+`}}`)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(files["schema_gen.go"], []byte(tt.want)) {
			t.Errorf("decode %q: schema_gen.go has no %s:\n%s", tt.decode, tt.want, files["schema_gen.go"])
		}
	}
}

// TestEntriesOnOneShapeMerge: two overlays may each apply traits to the
// same shape, each with its own evidence (overlays/common.json and a
// service overlay, or two entries of one research pass).
func TestEntriesOnOneShapeMerge(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#KindsFault": {"type": "apply", "traits": {
		"dev.understudy#statusOverride": 422,
		"dev.understudy#evidence": {"label": "aws-doc", "source": "https://example.com/kinds"}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	src := files["errors_gen.go"]
	if !bytes.Contains(src, []byte(`Status: 422`)) || !bytes.Contains(src, []byte(`Kinds failed for this wsdl version.`)) {
		t.Fatalf("the two entries on KindsFault did not both apply:\n%s", src)
	}
}

// TestFamilyVocabulary: the errors a family's defaults shape lists are the
// generic vocabulary of every service speaking that family, in that order.
func TestFamilyVocabulary(t *testing.T) {
	vocabulary := func(errs string) string {
		return overlay(`"dev.understudy.defaults#QueryFamily": {"type": "service", "errors": [` + errs + `], "traits": {` + evidence + `}}`)
	}
	files, err := codegen.Generate(goldenInputs(t, vocabulary(`{"target": "com.example.golden#KindsFault"}`)))
	if err != nil {
		t.Fatal(err)
	}
	const want = "\tcase awserr.QueryFamily:\n\t\treturn []awserr.Identity{IdentityKindsFault()}\n\tdefault:\n\t\treturn nil\n"
	if !bytes.Contains(files["errors_gen.go"], []byte(want)) {
		t.Fatalf("errors_gen.go lacks the Query vocabulary:\n%s", files["errors_gen.go"])
	}
	_, err = codegen.Generate(goldenInputs(t, vocabulary(`{"target": "com.example.golden#Nested"}`)))
	const failure = "understudy-gen ir: dev.understudy.defaults#QueryFamily: lists com.example.golden#Nested under errors, " +
		"but it has no error trait; the family vocabulary holds error structures only"
	var gerr *codegen.Error
	if !errors.As(err, &gerr) || gerr.Stage != codegen.StageIR || err.Error() != failure {
		t.Fatalf("Generate = %v\nwant %s", err, failure)
	}
}

// TestNotConfiguredEmitsTheAnswer: an S3-style Get* operation with
// notConfigured gets a NotConfigured<Op> constructor: the error, or the
// default output as a typed literal (wire-rest.md Implications 7).
func TestNotConfiguredEmitsTheAnswer(t *testing.T) {
	tests := []struct{ name, trait, want string }{
		{"error", `{"error": "com.example.golden#KindsFault"}`, "func NotConfiguredGetThing(message string) *awserr.Error { return ErrKindsFault(message) }"},
		{"default", `{"default": {"Name": "unset"}}`, `func NotConfiguredGetThing() *GetThingOutput { return &GetThingOutput{Name: new("unset")} }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#GetThing": {"type": "apply", "traits": {
				"dev.understudy#notConfigured": `+tt.trait+`, `+evidence+`}}`)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(files["ops_gen.go"], []byte(tt.want)) {
				t.Fatalf("ops_gen.go lacks\n%s\n\n%s", tt.want, files["ops_gen.go"])
			}
		})
	}
}

// TestRolledUpPathsReportAtTheCollection: with validationPathStyle rolledUp
// (Lambda), a list element's own constraints are one Composite violation at
// the list with the whole list as the value (error-matrix.md 4, style B).
func TestRolledUpPathsReportAtTheCollection(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
		"dev.understudy#validationPathStyle": "rolledUp", `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	src := files["validate_gen.go"]
	for _, want := range []string{
		`Constraint: awserr.Composite{Scope: awserr.ElementScope, Of: []awserr.MemberConstraint{`,
		`Path: at(path, "tags"), Value: awserr.RenderedText{Text: renderTagList(v.Tags)}`,
		// A collection holding sensitive values is never echoed.
		`Path: at(path, "vault"), Value: awserr.RenderedOmitted{}, Constraint: awserr.Composite{Scope: awserr.MapValueScope`,
		`Path: at(path, "hints"), Value: awserr.RenderedOmitted{}, Constraint: awserr.Composite{Scope: awserr.ElementScope`,
	} {
		if !bytes.Contains(src, []byte(want)) {
			t.Fatalf("validate_gen.go lacks %s:\n%s", want, src)
		}
	}
	if bytes.Contains(src, []byte(`at(path, "tags"), strconv.Itoa(`)) {
		t.Fatalf("rolled-up validators still index the tags elements:\n%s", src)
	}
}

// TestASensitiveMapKeyHidesTheWholeMap: no copy of a map can keep its keys
// once they are sensitive, so Redact clears the map and a rolled-up
// violation omits it.
func TestASensitiveMapKeyHidesTheWholeMap(t *testing.T) {
	files, err := codegen.Generate(goldenInputs(t, overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
		"dev.understudy#validationPathStyle": "rolledUp", `+evidence+`}},
		"com.example.golden#LabelMap$key": {"type": "apply", "traits": {
		"smithy.api#sensitive": {}, "smithy.api#length": {"max": 3}, `+evidence+`}}`)))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\tv.Labels = nil\n"; !bytes.Contains(files["redact_gen.go"], []byte(want)) {
		t.Fatalf("redact_gen.go lacks %q:\n%s", want, files["redact_gen.go"])
	}
	want := `Path: at(path, "labels"), Value: awserr.RenderedOmitted{}, Constraint: awserr.Composite{Scope: awserr.MapKeysScope`
	if !bytes.Contains(files["validate_gen.go"], []byte(want)) {
		t.Fatalf("validate_gen.go lacks %s:\n%s", want, files["validate_gen.go"])
	}
}

// TestErrorKeepsItsCause: a failure caused by an I/O error keeps that error
// in the chain, so a caller can tell a missing or unreadable input apart.
func TestErrorKeepsItsCause(t *testing.T) {
	_, err := codegen.ParseOverlay(iotest.ErrReader(fs.ErrPermission), "denied.json")
	var ce *codegen.Error
	if !errors.As(err, &ce) || ce.Stage != codegen.StageOverlay || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("ParseOverlay = %v, want an overlay-stage *codegen.Error wrapping fs.ErrPermission", err)
	}
	if want := "understudy-gen overlay: denied.json: read: permission denied"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// TestLoadRejectsWhatTheModelsDoNotUse: the loader refuses AST features no
// vendored model uses rather than guessing (smithy-models.md 2).
func TestLoadRejectsWhatTheModelsDoNotUse(t *testing.T) {
	const prefix = "understudy-gen load: m.json: a.b#S: "
	tests := []struct {
		name, shapes, want string
	}{
		{
			"mixins", `"a.b#S": {"type": "structure", "members": {}, "mixins": [{"target": "a.b#M"}]}`,
			"uses mixins, which no published model uses; the generator does not flatten them",
		},
		{
			"apply", `"a.b#S": {"type": "apply", "traits": {}}`,
			"apply is not allowed in a vendored model (smithy-models.md 2); corrections belong in codegen/overlays",
		},
		{
			"set", `"a.b#S": {"type": "set", "member": {"target": "smithy.api#String"}}`,
			"set is a Smithy 1.0 shape type, not allowed in a Smithy 2.0 model (smithy-models.md 2)",
		},
		{
			"unknown shape type", `"a.b#S": {"type": "tuple"}`,
			`shape type "tuple" has no runtime schema kind: service, resource, operation, apply and set shapes never reach a codec, ` +
				`and smithy.api#Unit is a structure with @unitType rather than type "unit"; check the model's spelling`,
		},
		{"unknown property", `"a.b#S": {"type": "string", "flavor": "x"}`, `json: unknown field "flavor"`},
		{"replace outside an overlay", `"a.b#S": {"type": "string", "replace": true}`, `"replace" is an overlay property`},
		{
			"members on a simple shape", `"a.b#S": {"type": "string", "members": {"x": {"target": "smithy.api#String"}}}`,
			"a string shape has no members",
		},
		{
			"duplicate member", `"a.b#S": {"type": "structure", "members": {"x": {"target": "smithy.api#String"}, "x": {"target": "smithy.api#String"}}}`,
			`member "x" appears twice; merge the two into one`,
		},
		{
			"duplicate trait", `"a.b#S": {"type": "string", "traits": {"smithy.api#sensitive": {}, "smithy.api#sensitive": {}}}`,
			`trait "smithy.api#sensitive" appears twice; merge the two into one`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codegen.Load(strings.NewReader(`{"smithy": "2.0", "shapes": {`+tt.shapes+`}}`), "m.json")
			var gerr *codegen.Error
			if !errors.As(err, &gerr) || gerr.Stage != codegen.StageLoad || err.Error() != prefix+tt.want {
				t.Fatalf("Load = %v\nwant %s", err, prefix+tt.want)
			}
		})
	}
}

// TestLoadRejectsARepeatedShapeKey: encoding/json keeps the last of two
// equal keys, so a model that repeats one would silently lose a shape.
func TestLoadRejectsARepeatedShapeKey(t *testing.T) {
	_, err := codegen.Load(strings.NewReader(`{"smithy": "2.0", "shapes": {"a.b#S": {"type": "string"}, "a.b#S": {"type": "blob"}}}`), "m.json")
	const want = `understudy-gen load: m.json: shape "a.b#S" appears twice; merge the two into one`
	var gerr *codegen.Error
	if !errors.As(err, &gerr) || gerr.Stage != codegen.StageLoad || err.Error() != want {
		t.Fatalf("Load = %v\nwant %s", err, want)
	}
}

// TestIAMPrimaryAction: a descriptor lists the primary action first, never
// the Service Reference's alphabetical first (smithy-models.md 8). The
// fixture's PutKinds authorizes golden:TagThing, golden:PutKinds and
// iam:PassRole, and the service lists no Actions[]; each case swaps in
// another list, Actions[], operation name or iamPrimary entry.
func TestIAMPrimaryAction(t *testing.T) {
	const fixture = `["golden:TagThing", "golden:PutKinds", "iam:PassRole"]`
	const noActions = `"actions": []`
	const listsPutKinds = `[{"name": "PutKinds"}]`
	const config = "codegen/services/golden.json"
	const putKinds = "understudy-gen ir: com.example.golden#PutKinds: "
	mapTo := func(op, action string) []codegen.IAMPrimary {
		prefix, name, _ := strings.Cut(action, ":")
		return []codegen.IAMPrimary{{Operation: op, Action: registry.IAMAction{Prefix: prefix, Name: name}, Reason: "test"}}
	}
	tests := []struct {
		name       string
		authorized string
		actions    string // the service's Actions[], when not empty
		op         string // the name PutKinds is renamed to, when renamed
		primary    []codegen.IAMPrimary
		first      string // the first action of the operation, when generation succeeds
		err        string
	}{
		{name: "same name", authorized: fixture, first: `{Prefix: "golden", Name: "PutKinds"}`},
		{
			name: "same-name action listed under Actions only", authorized: `["golden:TagThing", "iam:PassRole"]`, actions: listsPutKinds,
			first: `{Prefix: "golden", Name: "PutKinds"}, {Prefix: "golden", Name: "TagThing"}, {Prefix: "iam", Name: "PassRole"}}`,
		},
		{
			name: "same-name action listed under Actions, none authorized", authorized: `[]`, actions: listsPutKinds,
			first: `{Prefix: "golden", Name: "PutKinds"}}`,
		},
		{
			name: "iamPrimary the Actions same-name action makes redundant", authorized: `["golden:TagThing", "golden:WriteKinds"]`,
			actions: listsPutKinds, primary: mapTo("PutKinds", "golden:WriteKinds"),
			err: putKinds + "the rules already make golden:PutKinds the primary action; remove its iamPrimary entry from " + config,
		},
		{
			name: "a Batch operation's single-item action", op: "PutKindsBatch",
			authorized: `["golden:TagThing", "golden:PutKinds", "golden:WriteKinds"]`,
			first:      `{Prefix: "golden", Name: "PutKinds"}, {Prefix: "golden", Name: "TagThing"}, {Prefix: "golden", Name: "WriteKinds"}}`,
		},
		{
			name: "iamPrimary for an operation that authorizes nothing", authorized: fixture, primary: mapTo("Ping", "golden:Ping"),
			err: "understudy-gen ir: com.example.golden#Ping: the Service Reference lists no authorized action; remove its iamPrimary entry from " + config,
		},
		{
			name: "the one action under the service's prefix outranks iam:PassRole", authorized: `["iam:PassRole", "golden:ReplaceKinds"]`,
			first: `{Prefix: "golden", Name: "ReplaceKinds"}`,
		},
		{
			name: "iamPrimary picks among several", authorized: `["golden:TagThing", "golden:WriteKinds"]`,
			primary: mapTo("PutKinds", "golden:WriteKinds"), first: `{Prefix: "golden", Name: "WriteKinds"}`,
		},
		{
			name: "several and no iamPrimary", authorized: `["golden:TagThing", "golden:WriteKinds", "iam:PassRole"]`,
			err: putKinds + "no rule picks the primary action among [golden:TagThing golden:WriteKinds iam:PassRole] (2 under golden); " +
				"add an iamPrimary entry {operation, action, reason} to " + config,
		},
		{
			name: "iamPrimary the rules make redundant", authorized: fixture, primary: mapTo("PutKinds", "golden:TagThing"),
			err: putKinds + "the rules already make golden:PutKinds the primary action; remove its iamPrimary entry from " + config,
		},
		{
			name: "iamPrimary naming an action not authorized", authorized: `["golden:TagThing", "golden:WriteKinds"]`,
			primary: mapTo("PutKinds", "golden:DeleteKinds"),
			err: putKinds + "the iamPrimary entry in " + config + " names golden:DeleteKinds, which is not among the authorized actions " +
				"[golden:TagThing golden:WriteKinds]",
		},
		{
			name: "iamPrimary naming no modeled operation", authorized: fixture, primary: mapTo("PutKind", "golden:TagThing"),
			err: "understudy-gen ir: " + config + ": iamPrimary names PutKind, which is not a modeled operation of com.example.golden#Golden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := goldenInputs(t)
			table, ok := in.IAM.(codegen.ActionsTable)
			if !ok {
				t.Fatalf("goldenInputs gives IAM %T, want an ActionsTable", in.IAM)
			}
			table.Source.Content = bytes.Replace(table.Source.Content, []byte(fixture), []byte(tt.authorized), 1)
			if tt.actions != "" {
				table.Source.Content = bytes.Replace(table.Source.Content, []byte(noActions), []byte(`"actions": `+tt.actions), 1)
			}
			if tt.op != "" {
				table.Source.Content = bytes.Replace(table.Source.Content, []byte(`{"name": "PutKinds"`), []byte(`{"name": "`+tt.op+`"`), 1)
				rename := func(src []byte) []byte {
					return bytes.ReplaceAll(src, []byte("golden#PutKinds"), []byte("golden#"+tt.op))
				}
				in.Model.Content = rename(in.Model.Content)
				for i := range in.Overlays {
					in.Overlays[i].Content = rename(in.Overlays[i].Content)
				}
			}
			in.IAM = table
			in.Config.IAMPrimary = tt.primary
			files, err := codegen.Generate(in)
			if tt.err != "" {
				var gerr *codegen.Error
				if !errors.As(err, &gerr) || gerr.Stage != codegen.StageIR || err.Error() != tt.err {
					t.Fatalf("Generate = %v\nwant %s", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := "[]registry.IAMAction{" + tt.first; !bytes.Contains(files["ops_gen.go"], []byte(want)) {
				t.Fatalf("PutKinds does not list %s first:\n%s", tt.first, files["ops_gen.go"])
			}
		})
	}
}

// TestServiceConfigIAMPrimaryEntries: an iamPrimary entry states its reason,
// and one operation has one entry.
func TestServiceConfigIAMPrimaryEntries(t *testing.T) {
	const head = `{"service": "com.amazonaws.s3#AmazonS3", "package": "s3api", "model": "s3", "iamPrefix": "s3", "iamPrimary": [`
	const entry = `{"operation": "ListObjects", "action": "s3:ListBucket", "reason": "smithy-models.md 8"}`
	tests := []struct{ name, entries, want string }{
		{
			"no reason", `{"operation": "ListObjects", "action": "s3:ListBucket"}`,
			`understudy-gen load: codegen/services/s3.json: iamPrimary[0]: an entry needs "operation", "action" and the "reason" that action is the primary one`,
		},
		{
			"two entries for one operation", entry + ", " + entry,
			"understudy-gen load: codegen/services/s3.json: iamPrimary[1]: ListObjects has two entries; merge them into one",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codegen.ParseServiceConfig(strings.NewReader(head+tt.entries+"]}"), "s3")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("ParseServiceConfig = %v\nwant %s", err, tt.want)
			}
		})
	}
	cfg, err := codegen.ParseServiceConfig(strings.NewReader(head+entry+"]}"), "s3")
	want := []codegen.IAMPrimary{{Operation: "ListObjects", Action: registry.IAMAction{Prefix: "s3", Name: "ListBucket"}, Reason: "smithy-models.md 8"}}
	if err != nil || !slices.Equal(cfg.IAMPrimary, want) {
		t.Fatalf("ParseServiceConfig = %+v, %v; want iamPrimary %+v", cfg.IAMPrimary, err, want)
	}
}

// TestServiceConfigStatesTheIAMPrefix: the prefix is stated, not derived
// from arnNamespace, which says monitoring for CloudWatch.
func TestServiceConfigStatesTheIAMPrefix(t *testing.T) {
	const want = `understudy-gen load: codegen/services/cloudwatch.json: "iamPrefix" is "Monitoring"; give the IAM action prefix ` +
		`the Service Reference lists, such as "sqs" or "cloudwatch" (not the arnNamespace "monitoring")`
	doc := `{"service": "com.amazonaws.cloudwatch#GraniteServiceVersion20100801", "package": "cloudwatchapi", "model": "cloudwatch", "iamPrefix": "Monitoring"}`
	_, err := codegen.ParseServiceConfig(strings.NewReader(doc), "cloudwatch")
	if err == nil || err.Error() != want {
		t.Fatalf("ParseServiceConfig = %v\nwant %s", err, want)
	}
	cfg, err := codegen.ParseServiceConfig(strings.NewReader(strings.Replace(doc, "Monitoring", "cloudwatch", 1)), "cloudwatch")
	if err != nil || cfg.IAMPrefix != "cloudwatch" {
		t.Fatalf("ParseServiceConfig = %+v, %v", cfg, err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		id    string
		class codegen.Class
		known bool
	}{
		{"smithy.api#required", codegen.Honor, true},
		{"aws.protocols#awsQueryError", codegen.Honor, true},
		{"smithy.api#documentation", codegen.GenerationOnly, true},
		{"smithy.rules#endpointTests", codegen.Ignore, true},
		{"smithy.api#clientOptional", codegen.Ignore, true},
		{"dev.understudy#flavor", 0, false},
		{"com.example#custom", 0, false},
	}
	for _, tt := range tests {
		c, ok := codegen.Classify(tt.id)
		if c != tt.class || ok != tt.known {
			t.Errorf("Classify(%s) = %v, %v; want %v, %v", tt.id, c, ok, tt.class, tt.known)
		}
	}
}

// TestClassifyOverlayTraits pins the class of every dev.understudy trait by
// its effect (smithy-models.md 4.3): evidence only labels overlay entries,
// and every other overlay trait changes what the server puts on the wire.
func TestClassifyOverlayTraits(t *testing.T) {
	want := map[string]codegen.Class{
		"dev.understudy#evidence":              codegen.GenerationOnly,
		"dev.understudy#errorNamespace":        codegen.Honor,
		"dev.understudy#jsonType":              codegen.Honor,
		"dev.understudy#messageByProtocol":     codegen.Honor,
		"dev.understudy#validationError":       codegen.Honor,
		"dev.understudy#validationPathStyle":   codegen.Honor,
		"dev.understudy#successCode":           codegen.Honor,
		"dev.understudy#queryKeyAlias":         codegen.Honor,
		"dev.understudy#emptyCollection":       codegen.Honor,
		"dev.understudy#internalOperation":     codegen.Honor,
		"dev.understudy#patternEcho":           codegen.Honor,
		"dev.understudy#patternValidator":      codegen.Honor,
		"dev.understudy#alwaysEmit":            codegen.Honor,
		"dev.understudy#maxRequestBytes":       codegen.Honor,
		"dev.understudy#throttleError":         codegen.Honor,
		"dev.understudy#notConfigured":         codegen.Honor,
		"dev.understudy#malformedRequestError": codegen.Honor,
		"dev.understudy#statusOverride":        codegen.Honor,
		"dev.understudy#unknownOperationError": codegen.Honor,
		"dev.understudy#queryErrorEnvelope":    codegen.Honor,
	}
	for id, class := range want {
		if c, ok := codegen.Classify(id); c != class || !ok {
			t.Errorf("Classify(%s) = %v, %v; want %v, true", id, c, ok, class)
		}
	}
}
