package s3

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/checksum"
)

// shape adapts a checksumFields constructor to build its own zero shape, so
// the test can inspect the shape and the fields it was given.
func shape[T any](f func(*T) checksumFields) func() (reflect.Value, checksumFields) {
	return func() (reflect.Value, checksumFields) {
		v := new(T)
		return reflect.ValueOf(v).Elem(), f(v)
	}
}

// fieldConstructors lists every checksumFields constructor; the test below
// fails when checksum_fields.go declares one this table misses.
var fieldConstructors = map[string]func() (reflect.Value, checksumFields){
	"putObjectInputFields":                shape(putObjectInputFields),
	"putObjectOutputFields":               shape(putObjectOutputFields),
	"uploadPartInputFields":               shape(uploadPartInputFields),
	"uploadPartOutputFields":              shape(uploadPartOutputFields),
	"partFields":                          shape(partFields),
	"completeMultipartUploadInputFields":  shape(completeMultipartUploadInputFields),
	"completeMultipartUploadOutputFields": shape(completeMultipartUploadOutputFields),
	"completedPartFields":                 shape(completedPartFields),
	"headObjectOutputFields":              shape(headObjectOutputFields),
	"getObjectOutputFields":               shape(getObjectOutputFields),
	"copyObjectResultFields":              shape(copyObjectResultFields),
	"copyPartResultFields":                shape(copyPartResultFields),
	"checksumShapeFields":                 shape(checksumShapeFields),
	"objectPartFields":                    shape(objectPartFields),
}

// TestChecksumFieldsTableIsComplete keeps fieldConstructors in step with the
// constructors checksum_fields.go declares.
func TestChecksumFieldsTableIsComplete(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "checksum_fields.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse checksum_fields.go: %v", err)
	}
	var declared []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "checksumFields" {
			declared = append(declared, fn.Name.Name)
		}
	}
	listed := make([]string, 0, len(fieldConstructors))
	for name := range fieldConstructors {
		listed = append(listed, name)
	}
	slices.Sort(declared)
	slices.Sort(listed)
	if !slices.Equal(declared, listed) {
		t.Errorf("checksum_fields.go declares %v, fieldConstructors lists %v; add the missing constructor to the table", declared, listed)
	}
}

// TestChecksumFieldsFillEverySlot checks that each constructor points every
// algorithm's slot at that algorithm's Checksum<ALGORITHM> field, so a
// missed or swapped field fails here rather than as a nil dereference or a
// checksum under the wrong header.
func TestChecksumFieldsFillEverySlot(t *testing.T) {
	if got := len(checksum.Algorithms()); got != checksum.Count {
		t.Fatalf("len(checksum.Algorithms()) = %d, want checksum.Count %d", got, checksum.Count)
	}
	for name, build := range fieldConstructors {
		t.Run(name, func(t *testing.T) {
			v, fs := build()
			if fs[0] != nil {
				t.Errorf("slot 0, the zero Algorithm, is set")
			}
			for _, a := range checksum.Algorithms() {
				field := v.FieldByName("Checksum" + a.String())
				if !field.IsValid() {
					t.Fatalf("%s has no field Checksum%s", v.Type(), a)
				}
				want, ok := reflect.TypeAssert[**string](field.Addr())
				if !ok {
					t.Fatalf("%s.Checksum%s is %s, want *string", v.Type(), a, field.Type())
				}
				if fs[a] != want {
					t.Errorf("slot %s does not point at %s.Checksum%s", a, v.Type(), a)
				}
			}
		})
	}
}
