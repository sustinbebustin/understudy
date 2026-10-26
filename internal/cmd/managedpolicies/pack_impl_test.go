package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"pgregory.net/rapid"

	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// gunzip is b decompressed, with the header it carries.
func gunzip(t *testing.T, b []byte) ([]byte, gzip.Header) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader = %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress = %v", err)
	}
	return out, zr.Header
}

func readData(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// gzipPolicies is deterministic and lossless, and its header names no file,
// comment or time, so the bytes depend on the input alone.
func TestGzipPoliciesIsDeterministicAndLossless(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		in := rapid.SliceOf(rapid.Byte()).Draw(rt, "policies")
		a, err := gzipPolicies(in)
		if err != nil {
			rt.Fatalf("gzipPolicies = %v", err)
		}
		b, err := gzipPolicies(in)
		if err != nil {
			rt.Fatalf("gzipPolicies = %v", err)
		}
		if !bytes.Equal(a, b) {
			rt.Fatal("two calls on one input wrote different bytes")
		}
		zr, err := gzip.NewReader(bytes.NewReader(a))
		if err != nil {
			rt.Fatalf("gzip.NewReader = %v", err)
		}
		out, err := io.ReadAll(zr)
		if err != nil {
			rt.Fatalf("decompress = %v", err)
		}
		if !bytes.Equal(out, in) {
			rt.Fatalf("decompressed %d bytes, want the %d written", len(out), len(in))
		}
		if h := zr.Header; h.Name != "" || h.Comment != "" || !h.ModTime.IsZero() || h.Extra != nil {
			rt.Fatalf("header = %+v, want the zero header", h)
		}
	})
}

// write's policies.json.gz is policies.json as gzipPolicies compresses it.
func TestWriteWritesTheGzipCopy(t *testing.T) {
	dir, _, _ := written(t)
	policies := readData(t, dir, awsmanagedpolicies.PoliciesFile)
	compressed := readData(t, dir, awsmanagedpolicies.PoliciesGzipFile)
	if out, _ := gunzip(t, compressed); !bytes.Equal(out, policies) {
		t.Errorf("%s decompresses to %d bytes, not the %d of %s", awsmanagedpolicies.PoliciesGzipFile, len(out), len(policies), awsmanagedpolicies.PoliciesFile)
	}
	want, err := gzipPolicies(policies)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compressed, want) {
		t.Errorf("%s is not gzipPolicies(%s)", awsmanagedpolicies.PoliciesGzipFile, awsmanagedpolicies.PoliciesFile)
	}
}

// -pack rewrites a missing or stale policies.json.gz from policies.json,
// offline, and the directory then verifies.
func TestPackThenVerify(t *testing.T) {
	for _, c := range []struct {
		name   string
		before func(t *testing.T, gz string)
	}{
		{"missing", func(t *testing.T, gz string) {
			t.Helper()
			if err := os.Remove(gz); err != nil {
				t.Fatal(err)
			}
		}},
		{"stale", func(t *testing.T, gz string) {
			t.Helper()
			stale, err := gzipPolicies([]byte("{\n  \"policies\": []\n}\n"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gz, stale, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, cat, _ := written(t)
			gz := filepath.Join(dir, awsmanagedpolicies.PoliciesGzipFile)
			c.before(t, gz)
			if err := verify(os.DirFS(dir), dir); err == nil {
				t.Fatal("verify before -pack = nil, want a problem with the gzip copy")
			}
			var stderr strings.Builder
			if err := run(t.Context(), []string{"-pack", "-dir", dir}, &stderr); err != nil {
				t.Fatalf("run -pack = %v", err)
			}
			if want := "managedpolicies: " + summary(cat) + "\n"; stderr.String() != want {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
			if err := verify(os.DirFS(dir), dir); err != nil {
				t.Errorf("verify after -pack = %v", err)
			}
			want, err := gzipPolicies(readData(t, dir, awsmanagedpolicies.PoliciesFile))
			if err != nil {
				t.Fatal(err)
			}
			if got := readData(t, dir, awsmanagedpolicies.PoliciesGzipFile); !bytes.Equal(got, want) {
				t.Errorf("-pack wrote %d bytes, want gzipPolicies(policies.json)'s %d", len(got), len(want))
			}
			if info, err := os.Stat(gz); err != nil || info.Mode().Perm() != 0o644 {
				t.Errorf("%s mode = %v, %v; want 0644", gz, info, err)
			}
		})
	}
}

// LICENSE names policies.json.gz, and says it is policies.json compressed.
func TestLicenseNamesTheGzipCopy(t *testing.T) {
	text := strings.Join(strings.Fields(licenseText), " ")
	for _, s := range []string{"(policies.json, policies.json.gz, SOURCE.json and NOTICE)", "policies.json.gz is policies.json compressed with gzip"} {
		if !strings.Contains(text, s) {
			t.Errorf("licenseText does not contain %q", s)
		}
	}
}

// -pack rewrites a stale LICENSE and a missing policies.json.gz to the
// bytes write gives them, so -fetch and -pack agree on the derived files.
func TestPackWritesTheDerivedFilesAsWriteDoes(t *testing.T) {
	dir, _, _ := written(t)
	fromWrite := map[string][]byte{}
	for _, name := range dataFiles {
		fromWrite[name] = readData(t, dir, name)
	}
	if err := os.WriteFile(filepath.Join(dir, awsmanagedpolicies.LicenseFile), []byte("an earlier licence text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, awsmanagedpolicies.PoliciesGzipFile)); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	if err := run(t.Context(), []string{"-pack", "-dir", dir}, &stderr); err != nil {
		t.Fatalf("run -pack = %v", err)
	}
	for _, name := range dataFiles {
		if got := readData(t, dir, name); !bytes.Equal(got, fromWrite[name]) {
			t.Errorf("%s after -pack differs from what write wrote", name)
		}
	}
	if got := string(readData(t, dir, awsmanagedpolicies.LicenseFile)); got != licenseText {
		t.Errorf("LICENSE after -pack is not licenseText")
	}
}

// -pack leaves the files only -fetch writes as they are, and -verify then
// reports them.
func TestPackLeavesTheFetchedFilesAlone(t *testing.T) {
	dir, _, _ := written(t)
	edited := map[string][]byte{}
	for _, name := range []string{awsmanagedpolicies.NoticeFile, awsmanagedpolicies.SourceFile} {
		edited[name] = append(readData(t, dir, name), '\n')
		if err := os.WriteFile(filepath.Join(dir, name), edited[name], 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stderr strings.Builder
	err := run(t.Context(), []string{"-pack", "-dir", dir}, &stderr)
	ve, ok := errors.AsType[*VerifyError](err)
	if !ok {
		t.Fatalf("run -pack = %v, want a *VerifyError", err)
	}
	for name, want := range edited {
		if got := readData(t, dir, name); !bytes.Equal(got, want) {
			t.Errorf("-pack rewrote %s, which only -fetch writes", name)
		}
		if !slices.ContainsFunc(ve.Problems, func(p Problem) bool { return p.Path == name }) {
			t.Errorf("problems = %+v, want one for %s", ve.Problems, name)
		}
	}
}

// -pack without a policies.json fails and writes nothing.
func TestPackWithoutPoliciesWritesNothing(t *testing.T) {
	dir := t.TempDir()
	var stderr strings.Builder
	err := run(t.Context(), []string{"-pack", "-dir", dir}, &stderr)
	if err == nil || !strings.Contains(err.Error(), awsmanagedpolicies.PoliciesFile) || !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("run -pack = %v, want an error naming %s and saying nothing was written", err, awsmanagedpolicies.PoliciesFile)
	}
	wantNothingWritten(t, dir)
}

// A policies.json.gz that is missing, is not gzip, or does not decompress
// to exactly policies.json is one problem of policies.json.gz alone, whose
// detail says the file is never edited by hand and how to rewrite it.
func TestVerifyRefusesAGzipCopyThatDiffers(t *testing.T) {
	recompress := func(edit func(policies []byte) []byte) func(*testing.T, fstest.MapFS) {
		return func(t *testing.T, fsys fstest.MapFS) {
			t.Helper()
			gz, err := gzipPolicies(edit(bytes.Clone(fsys[awsmanagedpolicies.PoliciesFile].Data)))
			if err != nil {
				t.Fatal(err)
			}
			fsys[awsmanagedpolicies.PoliciesGzipFile].Data = gz
		}
	}
	for _, c := range []struct {
		name   string
		change func(*testing.T, fstest.MapFS)
	}{
		{"missing", func(_ *testing.T, f fstest.MapFS) { delete(f, awsmanagedpolicies.PoliciesGzipFile) }},
		{"not gzip", func(_ *testing.T, f fstest.MapFS) {
			f[awsmanagedpolicies.PoliciesGzipFile].Data = f[awsmanagedpolicies.PoliciesFile].Data
		}},
		{"truncated gzip", func(_ *testing.T, f fstest.MapFS) {
			gz := f[awsmanagedpolicies.PoliciesGzipFile].Data
			f[awsmanagedpolicies.PoliciesGzipFile].Data = gz[:len(gz)-8]
		}},
		{"one byte changed", recompress(func(p []byte) []byte {
			i := bytes.Index(p, []byte(`"v3"`))
			p[i+2] = '4'
			return p
		})},
		{"shorter", recompress(func(p []byte) []byte { return p[:len(p)-2] })},
		{"longer", recompress(func(p []byte) []byte { return append(p, '\n') })},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _, _ := written(t)
			fsys := asMapFS(t, dir)
			c.change(t, fsys)
			err := verify(fsys, dir)
			ve, ok := errors.AsType[*VerifyError](err)
			if !ok {
				t.Fatalf("verify = %v, want a *VerifyError", err)
			}
			if len(ve.Problems) != 1 || ve.Problems[0].Path != awsmanagedpolicies.PoliciesGzipFile || ve.Problems[0].Policy != "" {
				t.Fatalf("problems = %+v, want one, of %s alone", ve.Problems, awsmanagedpolicies.PoliciesGzipFile)
			}
			detail := ve.Problems[0].Detail
			for _, s := range []string{"never edited by hand", "write step", "go run ./internal/cmd/managedpolicies -pack"} {
				if !strings.Contains(detail, s) {
					t.Errorf("detail = %q, want it to contain %q", detail, s)
				}
			}
			if msg := ve.Error(); !strings.Contains(msg, detail) {
				t.Errorf("Error() = %q does not list the problem", msg)
			}
		})
	}
}

// The usage names -pack, and -pack is a mode like the others: exactly one
// is given.
func TestRunPackIsOneOfTheModes(t *testing.T) {
	var help strings.Builder
	if err := run(t.Context(), []string{"-h"}, &help); err != nil {
		t.Fatalf("run -h = %v", err)
	}
	if !strings.Contains(help.String(), "-pack") {
		t.Errorf("run -h printed %q, want -pack in the usage", help.String())
	}
	for _, args := range [][]string{{"-pack", "-verify"}, {"-fetch", "-pack"}, {"-fetch", "-pack", "-verify"}, {"-pack", "extra"}} {
		var stderr strings.Builder
		if err := run(t.Context(), args, &stderr); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want errUsage", args, err)
		}
	}
	if !strings.Contains(errUsage.Error(), "-pack") {
		t.Errorf("errUsage = %q, want it to name -pack", errUsage)
	}
}
