package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// corpusFixture is a corpus directory whose PIN.json records model.json and
// the six jars smithy ast reads.
func corpusFixture(t *testing.T, model []byte) (fstest.MapFS, Pin) {
	t.Helper()
	p := Pin{
		Smithy: smithyVersion, License: "Apache-2.0", Generated: "2026-10-10",
		Command: "smithy ast --no-config --flatten smithy-aws-protocol-tests-1.74.0.jar smithy-protocol-tests-1.74.0.jar " +
			"smithy-aws-traits-1.74.0.jar smithy-protocol-test-traits-1.74.0.jar smithy-protocol-traits-1.74.0.jar " +
			"smithy-validation-model-1.74.0.jar > model.json",
	}
	p.CLI.URL = "https://github.com/smithy-lang/smithy/releases/download/1.74.0/smithy-cli-linux-x86_64.zip"
	p.CLI.SHA256 = strings.Repeat("a", 64)
	for _, a := range corpusArtifacts() {
		name := strings.Split(a, ":")[1]
		url := "https://repo1.maven.org/maven2/software/amazon/smithy/" + name + "/1.74.0/" + name + "-1.74.0.jar"
		p.Jars = append(p.Jars, PinJar{Artifact: a, URL: url, Bytes: 1, SHA1: strings.Repeat("b", 40), SHA256: strings.Repeat("c", 64)})
	}
	p.Output.Path = "model.json"
	p.Output.Bytes = int64(len(model))
	p.Output.SHA256 = sha256Hex(model)
	return writePin(t, fstest.MapFS{"model.json": &fstest.MapFile{Data: model}}, p), p
}

func writePin(t *testing.T, fsys fstest.MapFS, p Pin) fstest.MapFS {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	fsys[corpusPin] = &fstest.MapFile{Data: raw}
	return fsys
}

func TestVerifyCorpusAcceptsThePinnedAST(t *testing.T) {
	fsys, _ := corpusFixture(t, []byte(`{"smithy":"2.0","shapes":{}}`))
	if err := verifyCorpus(fsys, "fixture"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCorpusReportsEveryDeparture(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(fstest.MapFS, *Pin)
		want   string
	}{
		{
			name: "an edited AST",
			mutate: func(f fstest.MapFS, _ *Pin) {
				f["model.json"] = &fstest.MapFile{Data: []byte(`{"smithy":"2.0","shapes":{"x":{}}}`)}
			},
			want: "model.json: has ",
		},
		{
			name:   "another Smithy version",
			mutate: func(_ fstest.MapFS, p *Pin) { p.Smithy = "1.75.0" },
			want:   "records Smithy 1.75.0, but the pin is 1.74.0",
		},
		{
			name:   "a jar missing",
			mutate: func(_ fstest.MapFS, p *Pin) { p.Jars = p.Jars[:5] },
			want:   "records jars",
		},
		{
			name:   "a CLI hash that is not one",
			mutate: func(_ fstest.MapFS, p *Pin) { p.CLI.SHA256 = "latest" },
			want:   `the CLI sha256 "latest" is not 64 lowercase hex digits`,
		},
		{
			name:   "another CLI url",
			mutate: func(_ fstest.MapFS, p *Pin) { p.CLI.URL = "https://example.com/smithy-cli.zip" },
			want:   `records the CLI url "https://example.com/smithy-cli.zip", but the Smithy 1.74.0 CLI is https://github.com/`,
		},
		{
			name:   "a jar from another host",
			mutate: func(_ fstest.MapFS, p *Pin) { p.Jars[2].URL = "https://example.com/smithy-aws-traits-1.74.0.jar" },
			want:   "records jar urls",
		},
		{
			name:   "a sha1 that is not one",
			mutate: func(_ fstest.MapFS, p *Pin) { p.Jars[0].SHA1 = strings.Repeat("B", 40) },
			want:   "want 64 and 40 lowercase hex digits",
		},
		{
			name:   "another command",
			mutate: func(_ fstest.MapFS, p *Pin) { p.Command = "smithy ast --flatten *.jar > model.json" },
			want:   `records the command "smithy ast --flatten *.jar > model.json"`,
		},
		{
			name: "another output path",
			mutate: func(f fstest.MapFS, p *Pin) {
				f["ast.json"] = f["model.json"]
				p.Output.Path = "ast.json"
			},
			want: `records the output "ast.json", but the command writes model.json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, p := corpusFixture(t, []byte(`{"smithy":"2.0","shapes":{}}`))
			tt.mutate(fsys, &p)
			err := verifyCorpus(writePin(t, fsys, p), "fixture")
			if ve, ok := errors.AsType[*VerifyError](err); !ok || len(ve.Problems) == 0 || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("verifyCorpus = %v, want a *VerifyError naming %q", err, tt.want)
			}
		})
	}
}

// TestVendoredInputsVerify is the -verify run over the checked-in models,
// corpus and actions table.
func TestVendoredInputsVerify(t *testing.T) {
	root := filepath.Join("..", "..", "..", "third_party")
	d := dirs{
		models:    filepath.Join(root, "api-models-aws"),
		corpus:    filepath.Join(root, "smithy-protocol-tests"),
		reference: filepath.Join(root, "aws-service-reference"),
	}
	if err := verifyAll(d); err != nil {
		t.Fatal(err)
	}
}
