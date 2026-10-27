package lambda_test

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// optOf is the /opt source of the last environment started for version of
// the function name.
func optOf(h *execHarness, name, version string) string {
	h.Helper()
	specs := h.started()
	last := -1
	for i := range specs {
		if specs[i].Function.Name == name && specs[i].Function.Version == version {
			last = i
		}
	}
	if last < 0 {
		h.Fatalf("no environment of %s:%s started", name, version)
	}
	code, ok := specs[last].Code.(launcher.ZipCode)
	if !ok {
		h.Fatalf("%s:%s started with %T, want zip code", name, version, specs[last].Code)
	}
	return code.Opt
}

// readOpt is the content of the file name in the merged tree dir, "" when
// it is missing.
func readOpt(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return ""
	}
	return string(b)
}

func createWithLayers(h *execHarness, name string, layers ...string) {
	h.Helper()
	in := zipFunction(h, name)
	in.Layers = layers
	call(h.harness, lambdaapi.OpCreateFunction, in)
}

// A function keeps running its layer's content after the layer version is
// deleted (lambda-destinations-layers.md 7.2, 11.5.4).
func TestInvokeRunsADeletedLayerVersionsContent(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.publish("a", layerArchive(t, map[string]string{"python/a.py": "A = 1\n"}))
		createWithLayers(h, "fn", layerPrefix+"a:1")
		call(h.harness, lambdaapi.OpDeleteLayerVersion, &lambdaapi.DeleteLayerVersionInput{LayerName: "a", VersionNumber: 1})
		h.invoke(invokeInput("fn", `{}`))
		if got := readOpt(t, optOf(h, "fn", "$LATEST"), "python/a.py"); got != "A = 1\n" {
			t.Errorf("python/a.py in /opt = %q, want the deleted version's content", got)
		}
	})
}

// A published version runs the layers it was published with, whatever
// $LATEST lists later (lambda-destinations-layers.md 7.2).
func TestEachVersionRunsItsOwnLayers(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.publish("a", layerArchive(t, map[string]string{"python/v.py": "V = 'a'\n"}))
		h.publish("b", layerArchive(t, map[string]string{"python/v.py": "V = 'b'\n"}))
		createWithLayers(h, "fn", layerPrefix+"a:1")
		call(h.harness, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		call(h.harness, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "b:1"}})
		h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn", Qualifier: new("1"), Payload: []byte(`{}`)})
		h.invoke(invokeInput("fn", `{}`))
		if got := readOpt(t, optOf(h, "fn", "1"), "python/v.py"); got != "V = 'a'\n" {
			t.Errorf("version 1's python/v.py = %q, want layer a's", got)
		}
		if got := readOpt(t, optOf(h, "fn", "$LATEST"), "python/v.py"); got != "V = 'b'\n" {
			t.Errorf("$LATEST's python/v.py = %q, want layer b's", got)
		}
	})
}

// The same layers in another order are another tree, in which the later
// layer's file wins a path both hold (switch lambda.layers.merge-conflict,
// lambda-destinations-layers.md 8.1).
func TestLayerOrderPicksTheTree(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.publish("a", layerArchive(t, map[string]string{"shared.txt": "a"}))
		h.publish("b", layerArchive(t, map[string]string{"shared.txt": "b"}))
		createWithLayers(h, "ab", layerPrefix+"a:1", layerPrefix+"b:1")
		createWithLayers(h, "ba", layerPrefix+"b:1", layerPrefix+"a:1")
		h.invoke(invokeInput("ab", `{}`))
		h.invoke(invokeInput("ba", `{}`))
		ab, ba := optOf(h, "ab", "$LATEST"), optOf(h, "ba", "$LATEST")
		if ab == ba {
			t.Fatalf("both orders got the tree %s", ab)
		}
		if got := readOpt(t, ab, "shared.txt"); got != "b" {
			t.Errorf("a then b: shared.txt = %q, want b's", got)
		}
		if got := readOpt(t, ba, "shared.txt"); got != "a" {
			t.Errorf("b then a: shared.txt = %q, want a's", got)
		}
	})
}

// The merged tree goes with the last environment that mounts it, and the
// layers' own extractions go once it is built: it is a copy. A function
// whose layers are removed gets no /opt.
func TestLayerTreesGoWithTheirEnvironments(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		archive := layerArchive(t, map[string]string{"python/a.py": "A = 1\n"})
		h.publish("a", archive)
		h.publish("b", layerArchive(t, map[string]string{"python/b.py": "B = 2\n"}))
		createWithLayers(h, "fn", layerPrefix+"a:1")
		h.invoke(invokeInput("fn", `{}`))
		first := optOf(h, "fn", "$LATEST")
		synctest.Wait()
		if _, err := h.code.Dir(codestore.Digest(sha256.Sum256(archive))); err == nil {
			t.Error("layer a's own extracted tree outlived the merge")
		}

		call(h.harness, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "b:1"}})
		h.invoke(invokeInput("fn", `{}`))
		second := optOf(h, "fn", "$LATEST")
		synctest.Wait()
		if _, err := os.Stat(first); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the tree of layer a after no environment mounts it: %v, want it removed", err)
		}
		if got := readOpt(t, second, "python/b.py"); got != "B = 2\n" {
			t.Errorf("python/b.py in the new /opt = %q, want layer b's", got)
		}

		call(h.harness, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{}})
		h.invoke(invokeInput("fn", `{}`))
		if opt := optOf(h, "fn", "$LATEST"); opt != "" {
			t.Errorf("/opt of a function without layers = %q, want none", opt)
		}
		synctest.Wait()
		if _, err := os.Stat(second); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the tree of layer b after its layers were removed: %v, want it removed", err)
		}
	})
}

// Which layer wins when one has a file where another has a directory is
// unrecorded (lambda-destinations-layers.md 8.1, LDL-12), so understudy
// runs no function with such layers and says which path and layer; the
// function runs again once the layers no longer conflict.
func TestLayersThatCannotMergeRefuseToRun(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.publish("dir", layerArchive(t, map[string]string{"python/x.py": "X = 1\n"}))
		h.publish("file", layerArchive(t, map[string]string{"python": "not a directory"}))
		createWithLayers(h, "fn", layerPrefix+"dir:1", layerPrefix+"file:1")
		err := h.invokeFails(invokeInput("fn", `{}`))
		if err.ID.Shape.Name() != "ServiceException" {
			t.Errorf("Invoke = %s, want ServiceException", err.ID.Shape.Name())
		}
		for _, want := range []string{"at python", layerPrefix + "file:1", "unrecorded"} {
			if !strings.Contains(err.Message, want) {
				t.Errorf("Invoke message = %q, want it to name %q", err.Message, want)
			}
		}
		call(h.harness, lambdaapi.OpUpdateFunctionConfiguration, &lambdaapi.UpdateFunctionConfigurationInput{FunctionName: "fn", Layers: []string{layerPrefix + "dir:1"}})
		h.invoke(invokeInput("fn", `{}`))
	})
}

// A hot-reload overlay replaces the code at /var/task and leaves the
// layers at /opt.
func TestHotReloadKeepsTheLayers(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.publish("a", layerArchive(t, map[string]string{"python/a.py": "A = 1\n"}))
		createWithLayers(h, "fn", layerPrefix+"a:1")
		dir := buildDir(t)
		if err := h.x.HotReload(h.Context(), h.Scope, "fn", dir); err != nil {
			t.Fatalf("HotReload: %s %s", err.ID.Shape.Name(), err.Message)
		}
		h.invoke(invokeInput("fn", `{}`))
		specs := h.started()
		last := specs[len(specs)-1]
		if codeDir(t, &last) != dir {
			t.Errorf("/var/task = %s, want the overlay %s", codeDir(t, &last), dir)
		}
		if got := readOpt(t, optOf(h, "fn", "$LATEST"), "python/a.py"); got != "A = 1\n" {
			t.Errorf("python/a.py in /opt under an overlay = %q, want layer a's", got)
		}
	})
}
