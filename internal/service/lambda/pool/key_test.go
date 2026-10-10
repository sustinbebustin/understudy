package pool_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
)

// specFieldChanges changes one Spec field each, by path. Paths under
// Code.ImageCode change a spec that already runs ImageCode{}.
func specFieldChanges(t *testing.T) map[string]func(*launcher.Spec) {
	t.Helper()
	account, err := aws.ParseAccountID("210987654321")
	if err != nil {
		t.Fatal(err)
	}
	region, err := aws.KnownRegions().Parse("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]func(*launcher.Spec){
		"Function.Name":                   func(s *launcher.Spec) { s.Function.Name = "other" },
		"Function.Version":                func(s *launcher.Spec) { s.Function.Version = "7" },
		"Function.Account":                func(s *launcher.Spec) { s.Function.Account = account },
		"Function.Region":                 func(s *launcher.Spec) { s.Function.Region = region },
		"Generation":                      func(s *launcher.Spec) { s.Generation++ },
		"Image":                           func(s *launcher.Spec) { s.Image = "my-function:dev" },
		"Architecture":                    func(s *launcher.Spec) { s.Architecture = launcher.ARM64 },
		"Code":                            func(s *launcher.Spec) { s.Code = launcher.ImageCode{} },
		"Code.ZipCode.Dir":                func(s *launcher.Spec) { s.Code = launcher.ZipCode{Dir: "/code/other", Handler: "bootstrap"} },
		"Code.ZipCode.Handler":            func(s *launcher.Spec) { s.Code = launcher.ZipCode{Dir: "/code/f", Handler: "main"} },
		"Code.ImageCode.EntryPoint":       func(s *launcher.Spec) { s.Code = launcher.ImageCode{EntryPoint: []string{"/app"}} },
		"Code.ImageCode.Command":          func(s *launcher.Spec) { s.Code = launcher.ImageCode{Command: []string{"handler"}} },
		"Code.ImageCode.WorkingDirectory": func(s *launcher.Spec) { s.Code = launcher.ImageCode{WorkingDirectory: "/app"} },
		"MemoryMB":                        func(s *launcher.Spec) { s.MemoryMB = 256 },
		"EphemeralStorageMB":              func(s *launcher.Spec) { s.EphemeralStorageMB = 1024 },
		"Environment":                     func(s *launcher.Spec) { s.Environment = map[string]string{"STAGE": "test"} },
		"Credentials.AccessKeyID":         func(s *launcher.Spec) { s.Credentials.AccessKeyID = "ASIAOTHER" },
		"Credentials.SecretAccessKey":     func(s *launcher.Spec) { s.Credentials.SecretAccessKey = "other" },
		"Credentials.SessionToken":        func(s *launcher.Spec) { s.Credentials.SessionToken = "other" },
		"LogGroup":                        func(s *launcher.Spec) { s.LogGroup = "/aws/lambda/other" },
	}
}

// fieldPaths lists the leaf fields of t, descending into the launcher's own
// structs; a Code path also lists the fields of each Code variant.
func fieldPaths(prefix string, t reflect.Type) []string {
	var paths []string
	for f := range t.Fields() {
		path := prefix + f.Name
		paths = append(paths, path)
		switch {
		case f.Type == reflect.TypeFor[launcher.Code]():
			for _, variant := range []reflect.Type{reflect.TypeFor[launcher.ZipCode](), reflect.TypeFor[launcher.ImageCode]()} {
				paths = append(paths, fieldPaths(path+"."+variant.Name()+".", variant)...)
			}
		case f.Type.Kind() == reflect.Struct && f.Type.PkgPath() == t.PkgPath():
			paths = append(paths[:len(paths)-1], fieldPaths(path+".", f.Type)...)
		}
	}
	return paths
}

// Two Specs that differ in any field start different containers, so they
// must never share a key. The test fails when Spec gains a field the key
// does not cover yet.
func TestNewKeyCoversEverySpecField(t *testing.T) {
	changes := specFieldChanges(t)
	paths := fieldPaths("", reflect.TypeFor[launcher.Spec]())
	for _, path := range paths {
		change, ok := changes[path]
		if !ok {
			t.Errorf("Spec field %s has no case here; cover it in pool.NewKey and add one", path)
			continue
		}
		base := fnSpec("f", 1)
		if strings.HasPrefix(path, "Code.ImageCode.") {
			base.Code = launcher.ImageCode{}
		}
		changed := base
		change(&changed)
		if pool.NewKey(changed) == pool.NewKey(base) {
			t.Errorf("changing %s left the key unchanged", path)
		}
	}
	for path := range changes {
		if !slices.Contains(paths, path) {
			t.Errorf("case %s names no Spec field", path)
		}
	}
}

// Specs that create the same container share a key: the environment's
// insertion order and nil against empty collections do not matter.
func TestNewKeyIgnoresWhatDoesNotShapeTheContainer(t *testing.T) {
	a, b := fnSpec("f", 1), fnSpec("f", 1)
	a.Environment = map[string]string{"A": "1", "B": "2"}
	b.Environment = map[string]string{"B": "2", "A": "1"}
	if pool.NewKey(a) != pool.NewKey(b) {
		t.Error("equal environments built in another order gave different keys")
	}
	a.Environment, b.Environment = nil, map[string]string{}
	a.Code, b.Code = launcher.ImageCode{}, launcher.ImageCode{EntryPoint: []string{}, Command: []string{}}
	if pool.NewKey(a) != pool.NewKey(b) {
		t.Error("nil and empty collections gave different keys; both create the same container")
	}
	// Moving a byte between adjacent values must not collide.
	a, b = fnSpec("f", 1), fnSpec("f", 1)
	a.Environment = map[string]string{"AB": "C"}
	b.Environment = map[string]string{"A": "BC"}
	if pool.NewKey(a) == pool.NewKey(b) {
		t.Error("environments AB=C and A=BC share a key")
	}
}
