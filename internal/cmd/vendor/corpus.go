package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// The Smithy protocol compliance corpus (smithy-protocol-tests.md 5): one
// flattened JSON AST that a human produces with the self-contained Smithy CLI
// over the six jars, recorded with every input hash in PIN.json. Producing it
// needs the CLI's bundled runtime, so the vendor tool only verifies it.
const (
	smithyVersion = "1.74.0"
	corpusPin     = "PIN.json"
	corpusOutput  = "model.json"
	smithyGroup   = "software.amazon.smithy"
)

// corpusJars are the six artifacts of smithyGroup that smithy ast reads, in
// command-line order.
func corpusJars() []string {
	return []string{
		"smithy-aws-protocol-tests",
		"smithy-protocol-tests",
		"smithy-aws-traits",
		"smithy-protocol-test-traits",
		"smithy-protocol-traits",
		"smithy-validation-model",
	}
}

// corpusArtifacts are the Maven coordinates of corpusJars at smithyVersion.
func corpusArtifacts() []string {
	out := make([]string, 0, len(corpusJars()))
	for _, j := range corpusJars() {
		out = append(out, smithyGroup+":"+j+":"+smithyVersion)
	}
	return out
}

// corpusJarURLs are the Maven Central URLs of corpusJars at smithyVersion.
func corpusJarURLs() []string {
	out := make([]string, 0, len(corpusJars()))
	for _, j := range corpusJars() {
		out = append(out, "https://repo1.maven.org/maven2/"+strings.ReplaceAll(smithyGroup, ".", "/")+"/"+j+"/"+smithyVersion+"/"+j+"-"+smithyVersion+".jar")
	}
	return out
}

// corpusCLIURL is the release asset of the self-contained Smithy CLI.
func corpusCLIURL() string {
	return "https://github.com/smithy-lang/smithy/releases/download/" + smithyVersion + "/smithy-cli-linux-x86_64.zip"
}

// corpusCommand is the command that produces corpusOutput from the jars.
func corpusCommand() string {
	files := make([]string, 0, len(corpusJars()))
	for _, j := range corpusJars() {
		files = append(files, j+"-"+smithyVersion+".jar")
	}
	return "smithy ast --no-config --flatten " + strings.Join(files, " ") + " > " + corpusOutput
}

// Pin is PIN.json.
type Pin struct {
	Smithy    string `json:"smithy"`
	License   string `json:"license"`
	Generated string `json:"generated"`
	CLI       struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"cli"`
	Command string   `json:"command"`
	Jars    []PinJar `json:"jars"`
	Output  struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"output"`
}

// PinJar records one input jar.
type PinJar struct {
	Artifact string `json:"artifact"`
	URL      string `json:"url"`
	Bytes    int64  `json:"bytes"`
	SHA1     string `json:"sha1"`
	SHA256   string `json:"sha256"`
}

// verifyCorpus checks PIN.json against the pinned Smithy version and its six
// artifacts, and model.json against the recorded size and hash, offline.
func verifyCorpus(root fs.FS, dir string) error {
	raw, err := fs.ReadFile(root, corpusPin)
	if err != nil {
		return fmt.Errorf("verify %s: read %s: %w; produce the corpus with the recipe its README gives", dir, corpusPin, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p Pin
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("verify %s: parse %s: %w", dir, corpusPin, err)
	}
	var problems []Problem
	add := func(path, format string, args ...any) {
		problems = append(problems, Problem{Path: path, Detail: fmt.Sprintf(format, args...)})
	}
	if p.Smithy != smithyVersion {
		add(corpusPin, "records Smithy %s, but the pin is %s", p.Smithy, smithyVersion)
	}
	if p.CLI.URL != corpusCLIURL() {
		add(corpusPin, "records the CLI url %q, but the Smithy %s CLI is %s", p.CLI.URL, smithyVersion, corpusCLIURL())
	}
	if !hexPattern.MatchString(p.CLI.SHA256) {
		add(corpusPin, "the CLI sha256 %q is not 64 lowercase hex digits", p.CLI.SHA256)
	}
	if p.Command != corpusCommand() {
		add(corpusPin, "records the command %q, but the corpus is produced by %q", p.Command, corpusCommand())
	}
	artifacts := make([]string, len(p.Jars))
	urls := make([]string, len(p.Jars))
	for i, j := range p.Jars {
		artifacts[i], urls[i] = j.Artifact, j.URL
		if !hexPattern.MatchString(j.SHA256) || !sha1Pattern.MatchString(j.SHA1) || j.Bytes <= 0 {
			add(corpusPin, "jar %s records sha256 %q, sha1 %q and %d bytes; want 64 and 40 lowercase hex digits and a positive size", j.Artifact, j.SHA256, j.SHA1, j.Bytes)
		}
	}
	if !slices.Equal(artifacts, corpusArtifacts()) {
		add(corpusPin, "records jars %v, but smithy ast reads %v", artifacts, corpusArtifacts())
	} else if !slices.Equal(urls, corpusJarURLs()) {
		add(corpusPin, "records jar urls %v, but Maven Central serves them at %v", urls, corpusJarURLs())
	}
	if p.Output.Path != corpusOutput {
		add(corpusPin, "records the output %q, but the command writes %s", p.Output.Path, corpusOutput)
	}
	content, err := fs.ReadFile(root, p.Output.Path)
	switch {
	case err != nil:
		add(p.Output.Path, "cannot be read: %v", err)
	case int64(len(content)) != p.Output.Bytes:
		add(p.Output.Path, "has %d bytes, %s records %d", len(content), corpusPin, p.Output.Bytes)
	case sha256Hex(content) != p.Output.SHA256:
		add(p.Output.Path, "has SHA-256 %s, %s records %s", sha256Hex(content), corpusPin, p.Output.SHA256)
	}
	if len(problems) > 0 {
		return &VerifyError{Dir: dir, Problems: problems}
	}
	return nil
}
