package codegen_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// withModelEdit is the fixture with the model text old, which it holds
// once, replaced by replacement.
func withModelEdit(t *testing.T, old, replacement string) codegen.Inputs {
	t.Helper()
	in := goldenInputs(t)
	model := string(in.Model.Content)
	if n := strings.Count(model, old); n != 1 {
		t.Fatalf("the fixture model holds %s %d times, want once; update the anchor", old, n)
	}
	in.Model.Content = []byte(strings.Replace(model, old, replacement, 1))
	return in
}

// outputItems anchors an edit to PutKindsResponse, the fixture's output.
const outputItems = `"Items": {"target": "com.example.golden#NestedList"},`

// An output member whose default is not its Go zero value is a pointer
// the serializer writes as the default when nil; a zero default stays a
// plain value written as it is; a defaulted list is always written, since
// a nil one writes its only possible default, []
// (smithy-protocol-tests.md 10.4, 14.2, 14.3).
func TestUnsetOutputDefaultsAreWrittenAsModeled(t *testing.T) {
	files, err := codegen.Generate(withModelEdit(t, outputItems, outputItems+`
        "Retries": {"target": "com.example.golden#Integer", "traits": {"smithy.api#default": 3}},
        "Total": {"target": "com.example.golden#Long", "traits": {"smithy.api#default": 0}},
        "Since": {"target": "com.example.golden#Timestamp", "traits": {"smithy.api#default": 0}},
        "Color": {"target": "com.example.golden#Color", "traits": {"smithy.api#default": "red"}},
        "Tags": {"target": "com.example.golden#TagList", "traits": {"smithy.api#default": []}},`))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "types_gen.go", files["types_gen.go"],
		"\tRetries *int32\n", "\tTotal   int64\n", "\tSince   *time.Time\n", "\tColor   *Color\n", "\tTags    []string\n")
	mustContain(t, "serde_gen.go", files["serde_gen.go"],
		"\tif v.Retries != nil {\n"+
			"\t\ts.WriteInt32(&memberPutKindsOutput_Retries, *v.Retries)\n"+
			"\t} else {\n"+
			"\t\ts.WriteInt32(&memberPutKindsOutput_Retries, int32(3))\n"+
			"\t}\n"+
			"\ts.WriteInt64(&memberPutKindsOutput_Total, v.Total)\n"+
			"\tif v.Since != nil {\n"+
			"\t\ts.WriteTimestamp(&memberPutKindsOutput_Since, *v.Since)\n"+
			"\t} else {\n"+
			// @default(0) on a timestamp is the epoch (smithy-protocol-tests.md 10.2).
			"\t\ts.WriteTimestamp(&memberPutKindsOutput_Since, time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC))\n"+
			"\t}\n"+
			"\tif v.Color != nil {\n"+
			"\t\ts.WriteString(&memberPutKindsOutput_Color, string(*v.Color))\n"+
			"\t} else {\n"+
			"\t\ts.WriteString(&memberPutKindsOutput_Color, string(Color(\"red\")))\n"+
			"\t}\n"+
			"\tserializeTagList(s, &memberPutKindsOutput_Tags, v.Tags)\n")
}

// A required member with a default always has a value, so it is not
// tracked for presence and never reported missing; being required does
// not make a non-zero default a plain value (smithy-protocol-tests.md
// 10.2, 14.2). Decoding stores a pointer to the default before reading
// (14.4).
func TestRequiredNonZeroDefaultIsAnUntrackedPointer(t *testing.T) {
	const retries = `"Retries": {"target": "com.example.golden#Integer", "traits": {"smithy.api#default": 3}},`
	files, err := codegen.Generate(withModelEdit(t, retries,
		`"Retries": {"target": "com.example.golden#Integer", "traits": {"smithy.api#default": 3, "smithy.api#required": {}}},`))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "types_gen.go", files["types_gen.go"], "\tRetries *int32\n")
	// Name and Nested are the two tracked members, as without the edit.
	mustContain(t, "serde_gen.go", files["serde_gen.go"], "\tv.missing = 1<<2 - 1\n\tv.Retries = new(int32(3))\n")
	if v := files["validate_gen.go"]; bytes.Contains(v, []byte(`"retries"), Value: awserr.RenderedNull{}`)) {
		t.Errorf("validate_gen.go reports a missing retries, which its default always fills:\n%s", v)
	}
}

// A default the target's type cannot take, or that is not of the target's
// type, fails generation rather than being dropped.
func TestUnrenderableDefaultsFailGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement, want string
	}{
		{
			name:        "structure",
			old:         `"Nested": {"target": "com.example.golden#Nested", "traits": {"smithy.api#required": {}}},`,
			replacement: `"Nested": {"target": "com.example.golden#Nested", "traits": {"smithy.api#required": {}, "smithy.api#default": {}}},`,
			want:        "a structure takes no default",
		},
		{
			name:        "boolean",
			old:         `"Flag": {"target": "com.example.golden#Boolean"},`,
			replacement: `"Flag": {"target": "com.example.golden#Boolean", "traits": {"smithy.api#default": "yes"}},`,
			want:        "is not true or false",
		},
		{
			name:        "number",
			old:         `"Tiny": {"target": "com.example.golden#Byte"},`,
			replacement: `"Tiny": {"target": "com.example.golden#Byte", "traits": {"smithy.api#default": "one"}},`,
			want:        "is not a number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := codegen.Generate(withModelEdit(t, tc.old, tc.replacement))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate = %v, want an error saying %q", err, tc.want)
			}
		})
	}
}
