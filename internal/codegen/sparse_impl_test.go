package codegen_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// withSparsePairs adds to the fixture model a sparse list of lists,
// SparsePairs, whose element Pair has length min 1, as member Pairs of
// PutKindsRequest and PutKindsResponse, so the fixture reaches it in both
// directions. edits are further old, new replacements of the model text.
func withSparsePairs(t *testing.T, edits ...string) codegen.Inputs {
	t.Helper()
	in := goldenInputs(t)
	model := string(in.Model.Content)
	edits = append([]string{
		`"Hints": {"target": "com.example.golden#SparseSecrets"}`,
		`"Hints": {"target": "com.example.golden#SparseSecrets"}, "Pairs": {"target": "com.example.golden#SparsePairs"}`,
		`"Items": {"target": "com.example.golden#NestedList"},`,
		`"Items": {"target": "com.example.golden#NestedList"}, "Pairs": {"target": "com.example.golden#SparsePairs"},`,
		`"com.example.golden#SparseSecrets": {`,
		`"com.example.golden#Pair": {"type": "list", "member": {"target": "com.example.golden#String"}, "traits": {"smithy.api#length": {"min": 1}}},
		"com.example.golden#SparsePairs": {"type": "list", "member": {"target": "com.example.golden#Pair"}, "traits": {"smithy.api#sparse": {}}},
		"com.example.golden#SparseSecrets": {`,
	}, edits...)
	for i := 0; i < len(edits); i += 2 {
		if n := strings.Count(model, edits[i]); n != 1 {
			t.Fatalf("the fixture model holds %s %d times, want once; update the anchor", edits[i], n)
		}
		model = strings.Replace(model, edits[i], edits[i+1], 1)
	}
	in.Model.Content = []byte(model)
	return in
}

// mustContain fails t unless src holds each of want.
func mustContain(t *testing.T, file string, src []byte, want ...string) {
	t.Helper()
	for _, w := range want {
		if !bytes.Contains(src, []byte(w)) {
			t.Errorf("%s lacks\n%s\n\n%s", file, w, src)
		}
	}
}

// A sparse list whose element is a list holds null as a nil element and an
// empty list as a non-nil one (smithy-protocol-tests.md 14.8): the
// serializer writes null for nil, the deserializer reads null before the
// element, validation skips null, and an example's null renders as nil.
func TestSparseListOfListsKeepsNullApart(t *testing.T) {
	files, err := codegen.Generate(withSparsePairs(t, `"Sparse": [1, null],`, `"Sparse": [1, null], "Pairs": [["a"], null, []],`))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "serde_gen.go", files["serde_gen.go"],
		"func serializeSparsePairs(s smithy.Serializer, m *smithy.Member, v [][]string) {\n"+
			"\ts.WriteList(m, len(v))\n"+
			"\tfor _, e := range v {\n"+
			"\t\tif e == nil {\n"+
			"\t\t\ts.WriteNull(&memberSparsePairs_member)\n"+
			"\t\t} else {\n"+
			"\t\t\tserializePair(s, &memberSparsePairs_member, e)\n"+
			"\t\t}\n",
		"\t\tnull, err := d.ReadNull(&memberSparsePairs_member)\n"+
			"\t\tif err != nil {\n"+
			"\t\t\treturn nil, err\n"+
			"\t\t}\n"+
			"\t\tif null {\n"+
			"\t\t\tout = append(out, nil)\n"+
			"\t\t\tcontinue\n"+
			"\t\t}\n"+
			"\t\tx, err := deserializePair(d, &memberSparsePairs_member)\n"+
			"\t\tif err != nil {\n"+
			"\t\t\treturn nil, err\n"+
			"\t\t}\n"+
			"\t\tout = append(out, x)\n",
	)
	// Pair's length min 1 holds for every element that is not null; a null
	// element is no value to check (smithy-protocol-tests.md 13.1).
	mustContain(t, "validate_gen.go", files["validate_gen.go"], "\t\t\tif e2 != nil {\n\t\t\t\tif len(e2) < 1 {\n")
	mustContain(t, "examples_gen_test.go", files["examples_gen_test.go"], `Pairs: [][]string{[]string{"a"}, nil, []string{}}`)
}

// Rolled up, a sparse element's constraints are checked only on the
// elements that are not null, and a null element renders as null in the
// echoed collection (error-matrix.md 4, style B; smithy-protocol-tests.md
// 13.1).
func TestRolledUpSparseListOfListsSkipsNull(t *testing.T) {
	in := withSparsePairs(t)
	in.Overlays = append(in.Overlays, codegen.Source{Name: "extra.json", Content: []byte(overlay(`"com.example.golden#Golden": {"type": "apply", "traits": {
		"dev.understudy#validationPathStyle": "rolledUp", ` + evidence + `}}`))})
	files, err := codegen.Generate(in)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "validate_gen.go", files["validate_gen.go"],
		"if e2 != nil && (len(e2) < 1) {",
		"func renderSparsePairs(v [][]string) string {",
		"\t\tif e == nil {\n\t\t\tparts = append(parts, \"null\")\n\t\t\tcontinue\n\t\t}\n\t\tparts = append(parts, renderPair(e))",
	)
}

// A null element the Go type cannot hold stops generation rather than
// render a value the case or example does not hold: a list literal would
// turn a null into an empty list (smithy-protocol-tests.md 13.1, 14.14).
// That covers a collection that is not @sparse, and a sparse one whose
// element type does not keep null apart (14.8).
func TestUnheldNullElementStopsGeneration(t *testing.T) {
	const prefix = `understudy-gen emit: PutKinds example "To put one of every kind": `
	tests := []struct {
		name  string
		edits []string
		want  string
	}{
		{
			name:  "dense list",
			edits: []string{`"Sparse": [1, null],`, `"Sparse": [1, null], "Pairs": [[null]],`},
			want: prefix + "com.example.golden#Pair holds a null element, but it is not @sparse, so no element of it can be null " +
				"(smithy-protocol-tests.md 13.1); a literal would render the null as an empty value",
		},
		{
			name: "sparse list of blobs",
			edits: []string{
				`"com.example.golden#SparseSecrets": {`,
				`"com.example.golden#SparseBlobs": {"type": "list", "member": {"target": "com.example.golden#Data"}, "traits": {"smithy.api#sparse": {}}},
				"com.example.golden#SparseSecrets": {`,
				`"Hints": {"target": "com.example.golden#SparseSecrets"}`,
				`"Hints": {"target": "com.example.golden#SparseSecrets"}, "Blobs": {"target": "com.example.golden#SparseBlobs"}`,
				`"Sparse": [1, null],`, `"Sparse": [1, null], "Blobs": [null],`,
			},
			want: prefix + "com.example.golden#SparseBlobs holds a null blob element; this generator keeps null apart only for elements " +
				"that are scalars, structures, lists or maps (smithy-protocol-tests.md 14.8)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codegen.Generate(withSparsePairs(t, tt.edits...))
			var gerr *codegen.Error
			if !errors.As(err, &gerr) || gerr.Stage != codegen.StageEmit || err.Error() != tt.want {
				t.Fatalf("Generate = %v\nwant %s", err, tt.want)
			}
		})
	}
}
