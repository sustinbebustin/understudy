package server

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// A codegen/services file that lists boundOperations generates members only
// for those operations' closures (ADR 0001 amendment 2026-11-02), so the list
// must be exactly the operations the wired kernel binds: a bound operation
// left out would decode into a schema without members. Independently of the
// lists, every bound operation's closure must be wired.
func TestBoundOperationsMatchTheKernel(t *testing.T) {
	srv, err := New(Config{Docker: DockerDisabled{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
	})
	k := srv.wired.kernel
	paths, err := filepath.Glob("../../codegen/services/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("listing codegen/services = %v, %v; want the service configs", paths, err)
	}
	for _, path := range paths {
		id := ref.ServiceID(strings.TrimSuffix(filepath.Base(path), ".json"))
		t.Run(string(id), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			cfg, err := codegen.ParseServiceConfig(f, id)
			if err != nil {
				t.Fatal(err)
			}
			svc, ok := k.Registry().Service(id)
			if !ok {
				t.Fatalf("codegen/services/%s.json has no service in the wired registry", id)
			}
			var bound []string
			for _, op := range svc.Operations {
				if _, ok := k.Bindings().Status(op).(kernel.Bound); ok && op.Kind != registry.Raw {
					bound = append(bound, op.ID.Name)
					checkWired(t, k, op)
				}
			}
			if listed, ok := cfg.Wiring.(codegen.WireBound); ok {
				compareBound(t, id, bound, listed.Operations)
			}
		})
	}
}

// compareBound reports each difference between the operations the kernel
// binds and the ones codegen/services/<id>.json lists, with the edit that
// fixes it.
func compareBound(t *testing.T, id ref.ServiceID, bound, listed []string) {
	t.Helper()
	config := "codegen/services/" + string(id) + ".json"
	for _, name := range bound {
		if !slices.Contains(listed, name) {
			t.Errorf(`%s: %s is bound in the kernel but %s "boundOperations" does not list it, so its input and output schemas are generated without members and its requests cannot decode; add "%s" to boundOperations, keeping the list sorted, and run just generate`,
				id, name, config, name)
		}
	}
	for _, name := range listed {
		if !slices.Contains(bound, name) {
			t.Errorf(`%s: %s "boundOperations" lists %s, which nothing binds; remove it and run just generate, so its closure is pruned`, id, config, name)
		}
	}
}

// checkWired fails t when a schema op reads is a pruned stub: an input
// whose Go type has fields but whose schema has no members, or a list, map
// or union without members anywhere in the input's or output's closure.
func checkWired(t *testing.T, k *kernel.Kernel, op *registry.Operation) {
	t.Helper()
	if in, ok := k.NewInput(op); ok && hasFields(in) && len(op.Input.Members) == 0 {
		t.Errorf("%s: the input %s of bound operation %s has no members although its Go type has fields; its schema was pruned (codegen/services boundOperations)", op.Service.ID, op.Input.ID, op.ID.Name)
	}
	seen := map[*smithy.Schema]bool{}
	var walk func(s *smithy.Schema)
	walk = func(s *smithy.Schema) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		alwaysMembers := s.Type == smithy.TypeList || s.Type == smithy.TypeMap || s.Type == smithy.TypeUnion
		if alwaysMembers && len(s.Members) == 0 {
			t.Errorf("%s: %s, reached from bound operation %s, has no members; its schema was pruned (codegen/services boundOperations)", op.Service.ID, s.ID, op.ID.Name)
		}
		for _, m := range s.Members {
			walk(m.Target)
		}
	}
	walk(op.Input)
	walk(op.Output)
}

// hasFields reports whether v, a pointer to a generated input structure,
// has an exported field.
func hasFields(v any) bool {
	typ := reflect.TypeOf(v)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return false
	}
	for field := range typ.Fields() {
		if field.IsExported() {
			return true
		}
	}
	return false
}
