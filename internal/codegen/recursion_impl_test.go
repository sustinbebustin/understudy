package codegen_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
)

// withNestedMember adds member to the fixture's Nested structure.
func withNestedMember(t *testing.T, member string) codegen.Inputs {
	t.Helper()
	in := goldenInputs(t)
	anchor := `"Weight": {"target": "com.example.golden#Double"}`
	if n := strings.Count(string(in.Model.Content), anchor); n != 1 {
		t.Fatalf("the fixture model holds %s %d times, want once; update the anchor", anchor, n)
	}
	in.Model.Content = []byte(strings.Replace(string(in.Model.Content), anchor, anchor+", "+member, 1))
	return in
}

// Schemas link statically, in their variables' literals, except a shape on
// a cycle of the schema graph: a variable initializer cannot reach its own
// variable, so a recursive shape's Members are assigned in init. A shape
// that targets itself and the shapes of a mutual recursion are recursive; a
// shape that only targets a recursive one is not.
func TestRecursiveShapesLinkInInit(t *testing.T) {
	const (
		nestedBare     = `var schemaNested = smithy.Schema{ID: "com.example.golden#Nested", Type: smithy.TypeStructure}`
		nestedListBare = `var schemaNestedList = smithy.Schema{ID: "com.example.golden#NestedList", Type: smithy.TypeList}`
		nestedListLit  = `var schemaNestedList = smithy.Schema{ID: "com.example.golden#NestedList", Type: smithy.TypeList, Members: []*smithy.Member{&memberNestedList_member}}`
		nestedInit     = "\tschemaNested.Members = []*smithy.Member{"
		nestedListInit = "\tschemaNestedList.Members = []*smithy.Member{&memberNestedList_member}"
		staticOutput   = `var schemaGetThingOutput = smithy.Schema{ID: "com.example.golden#GetThingResponse", Type: smithy.TypeStructure, Members: []*smithy.Member{`
	)
	for _, tc := range []struct {
		name, member   string
		want, wantNone []string
	}{
		{
			name:     "self",
			member:   `"Self": {"target": "com.example.golden#Nested"}`,
			want:     []string{nestedBare, nestedInit, nestedListLit, staticOutput, "var memberNested_Self = smithy.Member{Name: \"Self\", Index: 2, Target: &schemaNested}"},
			wantNone: []string{nestedListInit},
		},
		{
			name:   "mutual",
			member: `"Children": {"target": "com.example.golden#NestedList"}`,
			want:   []string{nestedBare, nestedListBare, nestedInit, nestedListInit, staticOutput},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, err := codegen.Generate(withNestedMember(t, tc.member))
			if err != nil {
				t.Fatal(err)
			}
			src := files["schema_gen.go"]
			mustContain(t, "schema_gen.go", src, tc.want...)
			for _, w := range tc.wantNone {
				if bytes.Contains(src, []byte(w)) {
					t.Errorf("schema_gen.go holds\n%s\nalthough the shape is not recursive\n\n%s", w, src)
				}
			}
		})
	}
}
