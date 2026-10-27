//go:build docker

package lambda_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// publishLayer publishes files as a version of the layer name and returns
// its version ARN.
func (s *dockerStack) publishLayer(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	out := mustDispatch(t, s, lambdaapi.OpPublishLayerVersion, &lambdaapi.PublishLayerVersionInput{
		LayerName: name, Content: lambdaapi.LayerVersionContentInput{ZipFile: zipFiles(t, files)},
	})
	return deref(out.LayerVersionArn)
}

// createWithLayers creates a function of runtime with the files as its
// package and the layers, in order.
func (s *dockerStack) createWithLayers(t *testing.T, name, runtime, handler string, files map[string]string, layers ...string) {
	t.Helper()
	mustDispatch(t, s, lambdaapi.OpCreateFunction, &lambdaapi.CreateFunctionInput{
		FunctionName: name,
		Role:         role,
		Runtime:      new(lambdaapi.Runtime(runtime)),
		Handler:      new(handler),
		Code:         lambdaapi.FunctionCode{ZipFile: zipFiles(t, files)},
		MemorySize:   new(int32(128)),
		Timeout:      new(int32(10)),
		PackageType:  new(lambdaapi.PackageTypeZip),
		Layers:       layers,
	})
}

// The Python runtime finds modules in a layer's python and
// python/lib/python3.x/site-packages directories, which Lambda extracts
// under /opt (lambda-destinations-layers.md 8.1, 8.2, from
// https://docs.aws.amazon.com/lambda/latest/dg/python-package.html,
// "Dependency search path"): a function returns what only the layer holds.
func TestDockerPythonImportsModulesFromALayer(t *testing.T) {
	s := newDockerStack(t)
	layer := s.publishLayer(t, "py-shared", map[string]string{
		"python/greet.py": "def hello():\n    return 'hello from the layer'\n",
		"python/lib/python3.13/site-packages/deep.py": "VALUE = 'from site-packages'\n",
	})
	s.createWithLayers(t, "py", "python3.13", "handler.handler", map[string]string{
		"handler.py": "import greet\nimport deep\n\n\ndef handler(event, context):\n    return {'greeting': greet.hello(), 'deep': deep.VALUE}\n",
	}, layer)
	got := result(t, s.invoke(t, "py", `{}`))
	if got["greeting"] != "hello from the layer" || got["deep"] != "from site-packages" {
		t.Errorf("payload = %v, want the layer's greeting and site-packages value", got)
	}
}

// The Node.js runtime finds packages in a layer's nodejs/node_modules
// (lambda-destinations-layers.md 8.2, from
// https://docs.aws.amazon.com/lambda/latest/dg/packaging-layers.html):
// a function requires a package only the layer holds.
func TestDockerNodeRequiresAPackageFromALayer(t *testing.T) {
	s := newDockerStack(t)
	layer := s.publishLayer(t, "node-shared", map[string]string{
		"nodejs/node_modules/greet/package.json": `{"name":"greet","version":"1.0.0","main":"index.js"}`,
		"nodejs/node_modules/greet/index.js":     "module.exports = () => 'hello from the layer';\n",
	})
	s.createWithLayers(t, "node", "nodejs22.x", "index.handler", map[string]string{
		"index.js": "const greet = require('greet');\nexports.handler = async () => ({ greeting: greet() });\n",
	}, layer)
	if got := result(t, s.invoke(t, "node", `{}`)); got["greeting"] != "hello from the layer" {
		t.Errorf("payload = %v, want the layer package's greeting", got)
	}
}

// A provided.al2023 function without a bootstrap of its own runs
// /opt/bootstrap, which a layer can supply (lambda-destinations-layers.md
// 8.3; containers.md 4.1).
func TestDockerALayerCanSupplyTheRuntime(t *testing.T) {
	s := newDockerStack(t)
	layer := s.publishLayer(t, "runtime", shellFiles(7))
	s.createWithLayers(t, "custom", "provided.al2023", "bootstrap", map[string]string{"README": "the runtime comes from a layer\n"}, layer)
	if got := result(t, s.invoke(t, "custom", `{}`)); got["version"] != 7.0 {
		t.Errorf("payload = %v, want version 7 from the layer's bootstrap", got)
	}
}

// Two layers holding one path merge in the order the function names them;
// which wins is unstated, and the switch lambda.layers.merge-conflict says
// (lambda-destinations-layers.md 8.1, 11.2, LDL-12).
func TestDockerLayersMergeInOrder(t *testing.T) {
	var value string
	run(t, lambda.Options{}, func(h *harness) { value = h.switchValue("lambda.layers.merge-conflict") })
	want := map[string]string{"later-overwrites": "second"}[value]
	if want == "" {
		t.Fatalf("lambda.layers.merge-conflict = %q, a value this test does not know", value)
	}
	s := newDockerStack(t)
	first := s.publishLayer(t, "first", map[string]string{"python/shared_value.py": "VALUE = 'first'\n", "python/only_first.py": "X = 1\n"})
	second := s.publishLayer(t, "second", map[string]string{"python/shared_value.py": "VALUE = 'second'\n"})
	s.createWithLayers(t, "py", "python3.13", "handler.handler", map[string]string{
		"handler.py": "import shared_value\nimport only_first\n\n\ndef handler(event, context):\n    return {'value': shared_value.VALUE, 'x': only_first.X}\n",
	}, first, second)
	got := result(t, s.invoke(t, "py", `{}`))
	if got["value"] != want || got["x"] != 1.0 {
		t.Errorf("payload = %v, want value %q and the first layer's other module", got, want)
	}
}
