package awserr_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Identity values come from generated constructors; a hand-written
// awserr.Identity{...} would let a handler invent an error AWS never sends
// (engineering review m11). Only generated code and internal/awserr (the
// generic catalogue) may build one. ADR 0003 names internal/api as the
// generated home; internal/protocoltest/gen is the same generator's output
// for the synthetic test services (ARCHITECTURE.md 5.6), so it is exempt too.
func TestNoIdentityLiteralsOutsideAPIAndAwserr(t *testing.T) {
	root, module := moduleRoot(t)
	found, err := identityLiterals(root, module)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		t.Errorf("%s: awserr.Identity composite literal; use the generated error constructor in internal/api/<svc>api instead", f)
	}
}

func TestIdentityLiteralCheckReportsSyntheticLiterals(t *testing.T) {
	root := t.TempDir()
	const module = "example.com/m"
	files := map[string]string{
		"internal/service/foo/errors.go": `package foo

import (
	"example.com/m/internal/awserr"
	ae "example.com/m/internal/awserr"
)

var plain = awserr.Identity{}

var aliased = &ae.Identity{Status: 400}

var elided = []awserr.Identity{{Status: 500}}

var keyed = map[string]*awserr.Identity{"x": {}}

type id = awserr.Identity

var alias = id{}

type ids []awserr.Identity

var named = ids{{}}

var nested = [][]awserr.Identity{{{}}}

var asKey = map[awserr.Identity]int{{}: 1}

var fine = awserr.Error{}

var fineNested = [][]int{{1}}
`,
		// Types declared in one file of a package and used in another that
		// does not import awserr.
		"internal/service/bar/types.go": `package bar

import ae "example.com/m/internal/awserr"

type ids []ae.Identity

type id = ae.Identity
`,
		"internal/service/bar/use.go": `package bar

var named = ids{{Status: 400}}

var aliased = &id{}

var fine = []int{1}
`,
		"internal/service/foo/errors_test.go": `package foo

import "example.com/m/internal/awserr"

var exempt = awserr.Identity{}
`,
		"internal/api/fooapi/errors_gen.go": `package fooapi

import "example.com/m/internal/awserr"

var generated = awserr.Identity{}
`,
		"internal/protocoltest/gen/jsonrpc10/errors_gen.go": `package jsonrpc10

import "example.com/m/internal/awserr"

var generated = awserr.Identity{}
`,
		"internal/awserr/catalogue.go": `package awserr

type Identity struct{ Status int }

var own = Identity{}
`,
		// The go tool ignores directories starting with . or _.
		"internal/service/foo/_scratch/x.go": `package x

import "example.com/m/internal/awserr"

var ignored = awserr.Identity{}
`,
		"internal/.cache/x.go": `package x

import "example.com/m/internal/awserr"

var ignored = awserr.Identity{}
`,
	}
	for name, src := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := identityLiterals(root, module)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/service/bar/use.go:3",
		"internal/service/bar/use.go:5",
		"internal/service/foo/errors.go:8",
		"internal/service/foo/errors.go:10",
		"internal/service/foo/errors.go:12",
		"internal/service/foo/errors.go:14",
		"internal/service/foo/errors.go:18",
		"internal/service/foo/errors.go:22",
		"internal/service/foo/errors.go:24",
		"internal/service/foo/errors.go:26",
	}
	if !slices.Equal(found, want) {
		t.Errorf("identityLiterals = %q, want %q", found, want)
	}
}

// exemptDirs hold generated code or the generic catalogue.
var exemptDirs = []string{"internal/api", "internal/awserr", "internal/protocoltest/gen", "internal/codegen/internal/goldenapi"}

// identityLiterals returns "path:line" for every awserr.Identity composite
// literal in a non-test Go file under root/internal, outside exemptDirs.
// Directories named testdata or starting with . or _ are skipped, as the go
// tool does. Paths are relative to root, slash-separated.
func identityLiterals(root, module string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if slices.Contains(exemptDirs, rel) || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return filepath.SkipDir
		}
		hits, err := packageLiterals(path, module+"/internal/awserr")
		for _, h := range hits {
			found = append(found, rel+"/"+h)
		}
		return err
	})
	return found, err
}

// sourceFile is a parsed non-test file and the names it imports awserr under.
type sourceFile struct {
	name   string
	ast    *ast.File
	awserr map[string]bool
}

// typeDecl is a package-level type's definition and the awserr names of the
// file that declares it, which qualify the definition.
type typeDecl struct {
	def    ast.Expr
	awserr map[string]bool
}

// packageLiterals returns "file:line" for every awserr.Identity composite
// literal in dir's non-test Go files. A type declared in one file of the
// package (type ids []awserr.Identity) is followed when another file uses
// it, even a file that does not import awserr itself.
func packageLiterals(dir, awserrPath string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var files []sourceFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		names, err := awserrNames(f, awserrPath)
		if err != nil {
			return nil, err
		}
		files = append(files, sourceFile{name: name, ast: f, awserr: names})
	}
	// Scopes are ignored, so a shadowing name can only over-report.
	local := map[string]typeDecl{}
	for _, f := range files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				local[ts.Name.Name] = typeDecl{def: ts.Type, awserr: f.awserr}
			}
			return true
		})
	}
	var found []string
	for _, f := range files {
		for _, l := range literalLines(fset, f, local) {
			found = append(found, f.name+":"+strconv.Itoa(l))
		}
	}
	return found, nil
}

// awserrNames returns the names f imports awserrPath under, "." included.
func awserrNames(f *ast.File, awserrPath string) (map[string]bool, error) {
	names := map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if p == awserrPath {
			if imp.Name != nil {
				names[imp.Name.Name] = true
			} else {
				names["awserr"] = true
			}
		}
	}
	return names, nil
}

func literalLines(fset *token.FileSet, f sourceFile, local map[string]typeDecl) []int {
	// resolve strips pointers and follows package-level type names, switching
	// to the awserr names of the file that declares each one; seen stops a
	// cyclic declaration.
	resolve := func(e ast.Expr, names map[string]bool) (ast.Expr, map[string]bool) {
		seen := map[string]bool{}
		for {
			switch t := e.(type) {
			case *ast.StarExpr:
				e = t.X
			case *ast.ParenExpr:
				e = t.X
			case *ast.Ident:
				decl, ok := local[t.Name]
				if !ok || seen[t.Name] {
					return e, names
				}
				seen[t.Name] = true
				e, names = decl.def, decl.awserr
			default:
				return e, names
			}
		}
	}
	isIdentity := func(e ast.Expr, names map[string]bool) bool {
		r, names := resolve(e, names)
		switch r := r.(type) {
		case *ast.SelectorExpr:
			x, ok := r.X.(*ast.Ident)
			return ok && names[x.Name] && r.Sel.Name == "Identity"
		case *ast.Ident:
			return names["."] && r.Name == "Identity"
		default:
			return false
		}
	}
	var lines []int
	report := func(n ast.Node) { lines = append(lines, fset.Position(n.Pos()).Line) }
	// elided reports the type-elided literals among lit's keys and elements
	// whose implied type is Identity, descending through nested arrays and
	// maps ([][]awserr.Identity{{{}}}). Literals that spell their type are
	// left to the walk below.
	var elided func(lit *ast.CompositeLit, typ ast.Expr, names map[string]bool)
	elided = func(lit *ast.CompositeLit, typ ast.Expr, names map[string]bool) {
		var key, elem ast.Expr
		r, names := resolve(typ, names)
		switch t := r.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			key, elem = t.Key, t.Value
		default:
			return
		}
		check := func(e, implied ast.Expr) {
			inner, ok := e.(*ast.CompositeLit)
			if !ok || inner.Type != nil {
				return
			}
			if isIdentity(implied, names) {
				report(inner)
				return
			}
			elided(inner, implied, names)
		}
		for _, e := range lit.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if key != nil {
					check(kv.Key, key)
				}
				e = kv.Value
			}
			check(e, elem)
		}
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || lit.Type == nil {
			return true
		}
		if isIdentity(lit.Type, f.awserr) {
			report(lit)
			return true
		}
		elided(lit, lit.Type, f.awserr)
		return true
	})
	slices.Sort(lines)
	return lines
}

// moduleRoot walks up from the test's directory to go.mod and returns that
// directory and the module path.
func moduleRoot(t *testing.T) (root, module string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for line := range strings.Lines(string(data)) {
				if module, ok := strings.CutPrefix(line, "module "); ok {
					return dir, strings.TrimSpace(module)
				}
			}
			t.Fatalf("%s/go.mod has no module line", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
