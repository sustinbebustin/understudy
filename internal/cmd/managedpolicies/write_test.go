package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// sourceFor is a SOURCE.json for cat as a run on 2026-10-25 records it: the
// three requests in order with the Last-Modified headers
// iam-evaluation.md 10.2 and 10.4 recorded, and each policy's page, anchor
// and document hash.
func sourceFor(cat *policycatalogue.Catalogue) Source {
	src := Source{
		Retrieved:         "2026-10-25",
		UserAgent:         userAgent,
		CrawlDelaySeconds: 5,
		Fetched: []Fetched{
			{URL: docsBase + robotsPath, LastModified: "", Bytes: 6, SHA256: hexSHA256([]byte("robots"))},
			{URL: docsBase + indexPath, LastModified: "Sun, 25 Oct 2026 17:54:38 GMT", Bytes: 4, SHA256: hexSHA256([]byte("llms"))},
			{URL: guidePrefix + "full.md", LastModified: "Sun, 25 Oct 2026 17:54:17 GMT", Bytes: 4, SHA256: hexSHA256([]byte("full"))},
		},
	}
	for p := range cat.All() {
		src.Policies = append(src.Policies, PolicySource{ARN: p.ARN, Page: guidePrefix + p.Name + ".md", Anchor: p.Name, DocumentSHA256: hexSHA256([]byte(p.Document))})
	}
	return src
}

// written writes the fixture catalogue into a new directory.
func written(t *testing.T) (dir string, cat *policycatalogue.Catalogue, src Source) {
	t.Helper()
	dir = t.TempDir()
	cat = fixtureCatalogue(t)
	src = sourceFor(cat)
	if err := write(dir, cat, src); err != nil {
		t.Fatalf("write = %v", err)
	}
	return dir, cat, src
}

// dataFiles are the files of the catalogue directory.
var dataFiles = []string{awsmanagedpolicies.PoliciesFile, awsmanagedpolicies.PoliciesGzipFile, awsmanagedpolicies.SourceFile, awsmanagedpolicies.LicenseFile, awsmanagedpolicies.NoticeFile}

func asMapFS(t *testing.T, dir string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range dataFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fsys[name] = &fstest.MapFile{Data: b, Mode: 0o644}
	}
	return fsys
}

func mustEncodeSource(t *testing.T, src Source) []byte {
	t.Helper()
	b, err := encodeSource(src)
	if err != nil {
		t.Fatalf("encodeSource = %v", err)
	}
	return b
}

// SOURCE.json is written like policies.json: two-space indent, members in
// declaration order, one trailing newline.
func TestEncodeSourceIsCanonical(t *testing.T) {
	src := Source{
		Retrieved: "2026-10-25", UserAgent: userAgent, CrawlDelaySeconds: 2.5,
		Fetched:  []Fetched{{URL: docsBase + robotsPath, LastModified: "", Bytes: 3, SHA256: hexSHA256([]byte("abc"))}},
		Policies: []PolicySource{{ARN: "arn:aws:iam::aws:policy/ExampleA", Page: guidePrefix + "ExampleA.md", Anchor: "ExampleA", DocumentSHA256: hexSHA256([]byte("abc"))}},
	}
	want := `{
  "retrieved": "2026-10-25",
  "userAgent": "understudy-managedpolicies/1 (+https://github.com/sustinbebustin/understudy)",
  "crawlDelaySeconds": 2.5,
  "fetched": [
    {
      "url": "https://docs.aws.amazon.com/robots.txt",
      "lastModified": "",
      "bytes": 3,
      "sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    }
  ],
  "policies": [
    {
      "arn": "arn:aws:iam::aws:policy/ExampleA",
      "page": "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/ExampleA.md",
      "anchor": "ExampleA",
      "documentSha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    }
  ]
}
`
	if diff := cmp.Diff(want, string(mustEncodeSource(t, src))); diff != "" {
		t.Errorf("encodeSource (-want +got):\n%s", diff)
	}
}

// LICENSE covers the documents with MIT No Attribution, the licence AWS
// grants for code in its documentation, and every other member with
// CC BY-SA 4.0, the documentation's licence (ADR 0016 amendment "managed
// policies"; https://github.com/aws/mit-0 for the MIT-0 text and its
// copyright line).
func TestLicenseText(t *testing.T) {
	for _, s := range []string{
		"MIT No Attribution",
		"Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.",
		"CC BY-SA 4.0",
		"https://creativecommons.org/licenses/by-sa/4.0/legalcode",
	} {
		if !strings.Contains(licenseText, s) {
			t.Errorf("licenseText does not contain %q", s)
		}
	}
}

// NOTICE keeps the CC BY-SA 4.0 attribution the ADR 0016 amendment lists:
// the source and its URLs, the retrieval date, the copyright holder and
// that the data was reformatted to JSON with no other changes. It is a
// function of SOURCE.json alone.
func TestNoticeAttributesTheGuide(t *testing.T) {
	src := sourceFor(fixtureCatalogue(t))
	got := string(notice(src))
	for _, s := range []string{
		"AWS Managed Policy Reference Guide",
		"https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md",
		"https://docs.aws.amazon.com/aws-managed-policy/latest/reference/llms.txt",
		"Retrieved: 2026-10-25",
		"Amazon Web Services, Inc. or its affiliates",
		"CC BY-SA 4.0",
		"reformatted to JSON with no other changes",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("notice does not contain %q:\n%s", s, got)
		}
	}
	if again := string(notice(src)); again != got {
		t.Error("notice differs between two calls on one Source")
	}
	later := src
	later.Retrieved = "2027-01-02"
	if l := string(notice(later)); !strings.Contains(l, "Retrieved: 2027-01-02") || strings.Contains(l, "2026-10-25") {
		t.Errorf("notice for a later run = %q, want its own date", l)
	}
}

// write leaves exactly the five data files, each mode 0644, holding the
// generator's forms of cat and src, and the directory verifies.
func TestWriteThenVerify(t *testing.T) {
	dir, cat, src := written(t)
	encoded, err := cat.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		awsmanagedpolicies.PoliciesFile: string(encoded),
		awsmanagedpolicies.SourceFile:   string(mustEncodeSource(t, src)),
		awsmanagedpolicies.LicenseFile:  licenseText,
		awsmanagedpolicies.NoticeFile:   string(notice(src)),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, slices.Sorted(slices.Values(dataFiles))) {
		t.Errorf("directory holds %v, want only %v", names, dataFiles)
	}
	for name, content := range want {
		path := filepath.Join(dir, name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %v, %v; want 0644", name, info, err)
		}
	}
	if err := verify(os.DirFS(dir), dir); err != nil {
		t.Errorf("verify of what write wrote = %v", err)
	}
}

// The repository's data directory before the maintainer's run: the empty
// catalogue and no other file. -verify reports the three missing files and
// nothing about policies.json.
func TestVerifyEmptyCatalogueWithoutTheOtherFiles(t *testing.T) {
	fsys := fstest.MapFS{awsmanagedpolicies.PoliciesFile: {Data: []byte("{\n  \"policies\": []\n}\n")}}
	err := verify(fsys, "third_party/aws-managed-policies")
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("verify = %v, want a *VerifyError", err)
	}
	paths := make([]string, 0, len(ve.Problems))
	for _, p := range ve.Problems {
		paths = append(paths, p.Path)
	}
	for _, want := range []string{awsmanagedpolicies.SourceFile, awsmanagedpolicies.LicenseFile, awsmanagedpolicies.NoticeFile} {
		if !slices.Contains(paths, want) {
			t.Errorf("problems %v name no %s", paths, want)
		}
	}
	if slices.Contains(paths, awsmanagedpolicies.PoliciesFile) {
		t.Errorf("problems %+v include policies.json, which is the canonical empty catalogue", ve.Problems)
	}
	if ve.Dir != "third_party/aws-managed-policies" {
		t.Errorf("VerifyError.Dir = %q", ve.Dir)
	}
}

// -verify finds each way the directory can differ from what the generator
// writes, naming the file and, when the problem is one policy's, its ARN;
// its message lists every problem and how to recover.
func TestVerifyReportsEachProblem(t *testing.T) {
	const admin = "arn:aws:iam::aws:policy/ExampleAdminAccess"
	const queue = "arn:aws:iam::aws:policy/service-role/ExampleQueueReader"
	type change func(t *testing.T, fsys fstest.MapFS, src Source)
	setSource := func(edit func(src *Source)) change {
		return func(t *testing.T, fsys fstest.MapFS, src Source) {
			t.Helper()
			src.Fetched = slices.Clone(src.Fetched)
			src.Policies = slices.Clone(src.Policies)
			edit(&src)
			fsys[awsmanagedpolicies.SourceFile].Data = mustEncodeSource(t, src)
		}
	}
	editFile := func(name, old, by string) change {
		return func(t *testing.T, fsys fstest.MapFS, _ Source) {
			t.Helper()
			fsys[name].Data = []byte(replaceOnce(t, string(fsys[name].Data), old, by))
		}
	}
	for _, c := range []struct {
		name   string
		change change
		want   Problem
	}{
		{
			name: "policies.json not canonical",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) {
				f["policies.json"].Data = []byte(strings.ReplaceAll(string(f["policies.json"].Data), "\n  ", "\n    "))
			},
			want: Problem{Path: "policies.json"},
		},
		{
			name:   "policies.json breaking a rule",
			change: editFile("policies.json", `"defaultVersionId": "v3"`, `"defaultVersionId": "v0"`),
			want:   Problem{Path: "policies.json", Policy: admin},
		},
		{
			name:   "policies.json not the format",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) { f["policies.json"].Data = []byte("{") },
			want:   Problem{Path: "policies.json"},
		},
		{
			name:   "policies.json missing",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) { delete(f, "policies.json") },
			want:   Problem{Path: "policies.json"},
		},
		{
			name:   "a document hash differs",
			change: setSource(func(s *Source) { s.Policies[2].DocumentSHA256 = hexSHA256([]byte("other")) }),
			want:   Problem{Path: "SOURCE.json", Policy: queue},
		},
		{
			name:   "an anchor that is not the name",
			change: setSource(func(s *Source) { s.Policies[2].Anchor = "ExampleQueueReader-json" }),
			want:   Problem{Path: "SOURCE.json", Policy: queue},
		},
		{
			name:   "a page that is not the policy's",
			change: setSource(func(s *Source) { s.Policies[2].Page = guidePrefix + "ExampleOther.md" }),
			want:   Problem{Path: "SOURCE.json", Policy: queue},
		},
		{
			name:   "a policy without a source entry",
			change: setSource(func(s *Source) { s.Policies = s.Policies[:3] }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "source entries out of order",
			change: setSource(func(s *Source) { s.Policies[0], s.Policies[1] = s.Policies[1], s.Policies[0] }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "two requests recorded",
			change: setSource(func(s *Source) { s.Fetched = s.Fetched[:2] }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "robots.txt from another site",
			change: setSource(func(s *Source) { s.Fetched[0].URL = "https://example.com/robots.txt" }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "a guide outside the reference",
			change: setSource(func(s *Source) { s.Fetched[2].URL = "https://docs.aws.amazon.com/other/full.md" }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "an upper-case hash",
			change: setSource(func(s *Source) { s.Fetched[1].SHA256 = strings.ToUpper(s.Fetched[1].SHA256) }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "a short hash",
			change: setSource(func(s *Source) { s.Fetched[1].SHA256 = s.Fetched[1].SHA256[:63] }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "a retrieval date in another layout",
			change: setSource(func(s *Source) { s.Retrieved = "25 Oct 2026" }),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "SOURCE.json not canonical",
			change: editFile("SOURCE.json", "\n  \"userAgent\"", " \"userAgent\""),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "SOURCE.json with an unknown member",
			change: editFile("SOURCE.json", "\n  \"userAgent\"", "\n  \"etag\": \"x\",\n  \"userAgent\""),
			want:   Problem{Path: "SOURCE.json"},
		},
		{
			name:   "LICENSE edited",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) { f["LICENSE"].Data = append(f["LICENSE"].Data, '\n') },
			want:   Problem{Path: "LICENSE"},
		},
		{
			name:   "LICENSE missing",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) { delete(f, "LICENSE") },
			want:   Problem{Path: "LICENSE"},
		},
		{
			name: "NOTICE edited",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) {
				f["NOTICE"].Data = []byte(strings.ToUpper(string(f["NOTICE"].Data)))
			},
			want: Problem{Path: "NOTICE"},
		},
		{
			name:   "NOTICE missing",
			change: func(_ *testing.T, f fstest.MapFS, _ Source) { delete(f, "NOTICE") },
			want:   Problem{Path: "NOTICE"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _, src := written(t)
			fsys := asMapFS(t, dir)
			c.change(t, fsys, src)
			err := verify(fsys, dir)
			var ve *VerifyError
			if !errors.As(err, &ve) {
				t.Fatalf("verify = %v, want a *VerifyError", err)
			}
			if !slices.ContainsFunc(ve.Problems, func(p Problem) bool {
				return p.Path == c.want.Path && p.Policy == c.want.Policy && p.Detail != ""
			}) {
				t.Errorf("problems = %+v, want one for %s (policy %q)", ve.Problems, c.want.Path, c.want.Policy)
			}
			msg := ve.Error()
			for _, p := range ve.Problems {
				if !strings.Contains(msg, p.Path) || !strings.Contains(msg, p.Detail) {
					t.Errorf("Error() = %q does not list problem %+v", msg, p)
				}
			}
			if !strings.Contains(msg, "git checkout") || !strings.Contains(msg, "-fetch") {
				t.Errorf("Error() = %q, want the recovery: git checkout, or re-run -fetch", msg)
			}
		})
	}
}

// A document IAM would refuse is a -verify finding about the data or
// policydoc, carrying policydoc's message and the policy's ARN.
func TestVerifyChecksDocumentsWithPolicydoc(t *testing.T) {
	ps := fixturePolicies()
	ps[2].Document = "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Principal\" : \"*\",\n      \"Action\" : \"sqs:ReceiveMessage\",\n      \"Resource\" : \"*\"\n    }\n  ]\n}"
	ps[3].Document = "{\n  \"Version\" : \"2008-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : \"logs:PutLogEvents\",\n      \"Resource\" : \"*\"\n    }\n  ]\n}"
	cat, err := policycatalogue.New(ps)
	if err != nil {
		t.Fatalf("policycatalogue.New = %v", err)
	}
	dir := t.TempDir()
	src := sourceFor(cat)
	if err := write(dir, cat, src); err != nil {
		t.Fatalf("write = %v", err)
	}
	err = verify(os.DirFS(dir), dir)
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("verify = %v, want a *VerifyError", err)
	}
	for _, p := range ps[2:] {
		doc, perr := policydoc.Parse([]byte(p.Document))
		if perr != nil {
			t.Fatalf("policydoc.Parse(%s) = %v", p.Name, perr)
		}
		verr := policydoc.ValidateIdentity(doc)
		if verr == nil {
			t.Fatalf("policydoc.ValidateIdentity accepts %s's document", p.Name)
		}
		if !slices.ContainsFunc(ve.Problems, func(pr Problem) bool {
			return pr.Path == "policies.json" && pr.Policy == p.ARN && strings.Contains(pr.Detail, verr.Error())
		}) {
			t.Errorf("problems = %+v, want one for %s carrying %q", ve.Problems, p.ARN, verr.Error())
		}
	}
}
