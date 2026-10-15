package identity_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// redacting are the methods through which slog, encoding/json and fmt reach
// a value; a type with a redacted view must answer every one of them, on a
// value receiver so that a copy redacts as the original does.
var redacting = []string{"LogValue", "MarshalJSON", "String", "GoString"}

// Every type in the package with a view() method implements each redacting
// method, so a new credential type that forgets one fails here rather than
// leaking through the one path TestCredentialsNeverReachLogsOrFormatting
// does not render it by.
func TestEveryViewedTypeRedactsEveryPath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	viewed, missing := redactionGaps(files)
	if !slices.Contains(viewed, "Credentials") {
		t.Fatalf("types with a view method = %v, want Credentials among them", viewed)
	}
	for _, m := range missing {
		t.Errorf("%s; add it beside the other redacted types in redact.go", m)
	}
}

func TestRedactionGapsReportsAMissingMethod(t *testing.T) {
	src := `package p

type A struct{}

func (A) view() int                     { return 0 }
func (A) LogValue() int                 { return 0 }
func (A) MarshalJSON() ([]byte, error)  { return nil, nil }
func (A) String() string                { return "" }
func (A) GoString() string              { return "" }

type B struct{}

func (B) view() int                     { return 0 }
func (*B) LogValue() int                { return 0 }
func (B) String() string                { return "" }
func (B) GoString() string              { return "" }

type C struct{}

func (C) String() string                { return "" }
`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	viewed, missing := redactionGaps([]*ast.File{f})
	if want := []string{"A", "B"}; !slices.Equal(viewed, want) {
		t.Errorf("viewed = %v, want %v", viewed, want)
	}
	want := []string{"B lacks LogValue on a value receiver", "B lacks MarshalJSON on a value receiver"}
	if !slices.Equal(missing, want) {
		t.Errorf("missing = %v, want %v", missing, want)
	}
}

// redactionGaps returns, sorted, the types declaring a view method and a
// line for each redacting method one of them lacks on a value receiver.
func redactionGaps(files []*ast.File) (viewed, missing []string) {
	methods := map[string]map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv, ok := fn.Recv.List[0].Type.(*ast.Ident)
			if !ok {
				continue // a pointer or generic receiver does not serve a value
			}
			if methods[recv.Name] == nil {
				methods[recv.Name] = map[string]bool{}
			}
			methods[recv.Name][fn.Name.Name] = true
		}
	}
	for typ, has := range methods {
		if has["view"] {
			viewed = append(viewed, typ)
		}
	}
	slices.Sort(viewed)
	for _, typ := range viewed {
		for _, m := range redacting {
			if !methods[typ][m] {
				missing = append(missing, typ+" lacks "+m+" on a value receiver")
			}
		}
	}
	return viewed, missing
}
