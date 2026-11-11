package ref_test

import (
	"cmp"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Comments cite a research note as "<note>.md <section>" (CONTRIBUTING.md),
// so a note's file name must identify it, every cited document must exist and
// every cited section number must resolve in it. A number resolves when a
// heading carries it, or when everything but its last component is a heading
// and the last component is a numbered list item under that heading.

func TestResearchNoteNamesAreUnique(t *testing.T) {
	root, _ := moduleRoot(t)
	tr, err := scanTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range noteNameClashes(tr) {
		t.Error(c)
	}
}

func TestCommentCitationsResolve(t *testing.T) {
	root, _ := moduleRoot(t)
	tr, err := scanTree(root)
	if err != nil {
		t.Fatal(err)
	}
	broken, err := brokenCitations(root, tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range broken {
		t.Error(b)
	}
}

func TestCitationGuardReportsSyntheticViolations(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"README.md":                           "",
		"docs/ARCHITECTURE.md":                "# Architecture\n\n## 2. Layers\n",
		"docs/research/sns.md":                "# SNS\n\n## 3. Delivery\n\n### 3.1 Retries\n\n## 4. Order\n\n1. First.\n2. Second.\n\n```\n## 5. Not a heading\n```\n\n## Implications\n\n1. One.\n10. Ten.\n",
		"docs/research/s3-checksum.md":        "# Checksums\n\n## 2. Algorithms\n",
		"docs/research/sqs.md":                "",
		"docs/guide/sqs.md":                   "",
		"docs/configuration.md":               "",
		"docs/guide/configuration.md":         "",
		"docs/research/testdata/sns.md":       "",
		"internal/x/node_modules/y/broken.go": "package y\n\n// nosuch.md\n",
		"internal/x/testdata/broken.go":       "package x\n\n// nosuch.md\n",
		"conformance/go-sdk/broken_test.go":   "package gosdk\n\n// The DLQ follows lost.md 6.1.\n",
		"internal/x/.build/broken.go":         "package x\n\n// nosuch.md\n",
		"third_party/models/broken.go":        "package models\n\n// nosuch.md\n",
		"internal/x/.venv/broken.go":          "package x\n\n// nosuch.md\n",
		".git/broken.go":                      "package git\n\n// nosuch.md\n",
		"internal/x/README.md":                "",
		"internal/x/x.go": `// Package x follows sns.md 3, ARCHITECTURE.md 2 and README.md.
package x

// See docs/research/sns.md 4, docs/design/sns.md and s3-checksum.md 2.
// A longer hyphenated name is one citation, never its tail: x-nosuch.md.
// Paths outside docs are not citations: internal/x/README.md, https://example.com/a/gone.md.
// A path directly under docs counts: docs/configuration.md resolves, (docs/nosuch.md) does not.
const s = "nosuch.md"

/*
Spans lines:
missing.md 1.
*/
var v = s

// Sections resolve: sns.md 3.1, sns.md 4.2, sns.md 3 and sns.md 4.
// Sections miss: sns.md 3.2, sns.md 10 and sns.md 5.
`,
	}
	for name, src := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := scanTree(root)
	if err != nil {
		t.Fatal(err)
	}

	wantClashes := []string{
		"docs/guide/sqs.md and docs/research/sqs.md share the file name sqs.md; comments cite a research note by file name alone, so rename one of them",
	}
	if got := noteNameClashes(tr); !slices.Equal(got, wantClashes) {
		t.Errorf("noteNameClashes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantClashes, "\n"))
	}

	const why = "no such document under docs/ or the repository root; cite a note as <note>.md <section>"
	wantBroken := []string{
		"conformance/go-sdk/broken_test.go:3: lost.md: " + why,
		"internal/x/x.go:4: docs/design/sns.md: " + why,
		"internal/x/x.go:5: x-nosuch.md: " + why,
		"internal/x/x.go:7: docs/nosuch.md: " + why,
		"internal/x/x.go:12: missing.md: " + why,
		"internal/x/x.go:17: sns.md 3.2: docs/research/sns.md has no section 3.2; nearest sections: 3, 3.1",
		"internal/x/x.go:17: sns.md 10: docs/research/sns.md has no section 10; nearest sections: 3, 4",
		"internal/x/x.go:17: sns.md 5: docs/research/sns.md has no section 5; nearest sections: 3, 4",
	}
	got, err := brokenCitations(root, tr)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, wantBroken) {
		t.Errorf("brokenCitations =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantBroken, "\n"))
	}
}

// citation matches a document name in a comment: a bare "<name>.md" or a
// "docs/[<dir>/...]<name>.md" path. The leading class keeps it from
// starting inside a longer hyphenated name or a path that does not begin
// with docs/.
// An optional section number follows on the same line.
var citation = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./-])((?:docs/(?:[A-Za-z0-9_.-]+/)*)?[A-Za-z0-9][A-Za-z0-9_-]*\.md)\b(?: +(\d+(?:\.\d+)*))?`)

var (
	headingNumber = regexp.MustCompile(`^#{1,6}[ \t]+(\d+(?:\.\d+)*)\.?(?:[ \t]|$)`)
	heading       = regexp.MustCompile(`^#{1,6}[ \t]`)
	listItem      = regexp.MustCompile(`^(\d+)[.)][ \t]`)
)

// sections holds the section numbers a document carries: those of its
// numbered headings, and those of numbered list items directly under one.
type sections struct {
	headings map[string]bool
	items    map[string]bool
}

func parseSections(src string) sections {
	s := sections{headings: map[string]bool{}, items: map[string]bool{}}
	cur, fenced := "", false
	for line := range strings.SplitSeq(src, "\n") {
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if heading.MatchString(line) {
			cur = ""
			if m := headingNumber.FindStringSubmatch(line); m != nil {
				cur = m[1]
				s.headings[cur] = true
			}
		} else if m := listItem.FindStringSubmatch(line); m != nil && cur != "" {
			s.items[cur+"."+m[1]] = true
		}
	}
	return s
}

func (s sections) has(num string) bool { return s.headings[num] || s.items[num] }

// parent drops the last component of a section number.
func parent(num string) string {
	before, _, ok := strings.CutLast(num, ".")
	if !ok {
		return ""
	}
	return before
}

func compareNums(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		// Components are digit strings without leading zeros: longer is larger.
		if c := cmp.Or(len(as[i])-len(bs[i]), strings.Compare(as[i], bs[i])); c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

// nearest returns up to six heading numbers closest to num: the nearest
// ancestor level that has headings, ordered, windowed around where num would
// sit.
func (s sections) nearest(num string) []string {
	var cands []string
	for p := parent(num); ; p = parent(p) {
		for h := range s.headings {
			if p == "" && !strings.Contains(h, ".") || p != "" && (h == p || strings.HasPrefix(h, p+".")) {
				cands = append(cands, h)
			}
		}
		if len(cands) > 0 || p == "" {
			break
		}
	}
	slices.SortFunc(cands, compareNums)
	at, _ := slices.BinarySearchFunc(cands, num, compareNums)
	lo := max(0, min(at-3, len(cands)-6))
	return cands[lo:min(len(cands), lo+6)]
}

// skippedDirs are directory names whose contents are vendored, generated by
// tools, or fixtures rather than the repository's own documents and code.
var skippedDirs = map[string]bool{
	"node_modules": true, ".venv": true, ".build": true, "third_party": true, "testdata": true, ".git": true,
}

// tree lists the files the citation guard reads, as slash-separated paths
// relative to the repository root.
type tree struct {
	docs   []string // *.md under docs/
	rootMD []string // *.md directly in the repository root
	goSrc  []string // every .go file, in any module
}

func scanTree(root string) (tree, error) {
	var tr tree
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(rel, ".go"):
			tr.goSrc = append(tr.goSrc, rel)
		case !strings.HasSuffix(rel, ".md"):
		case strings.HasPrefix(rel, "docs/"):
			tr.docs = append(tr.docs, rel)
		case !strings.Contains(rel, "/"):
			tr.rootMD = append(tr.rootMD, rel)
		}
		return nil
	})
	return tr, err
}

// noteNameClashes returns one message, sorted, per file name that a note
// under docs/research/ shares with another document under docs/.
func noteNameClashes(tr tree) []string {
	byName := map[string][]string{}
	for _, p := range tr.docs {
		byName[path.Base(p)] = append(byName[path.Base(p)], p)
	}
	var clashes []string
	for name, paths := range byName {
		if len(paths) < 2 || !slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, "docs/research/") }) {
			continue
		}
		slices.Sort(paths)
		clashes = append(clashes, fmt.Sprintf("%s share the file name %s; comments cite a research note by file name alone, so rename one of them",
			strings.Join(paths, " and "), name))
	}
	slices.Sort(clashes)
	return clashes
}

// brokenCitations parses the comments of every Go file in tr and returns
// one message, in file and position order, per citation that names no
// existing document. A docs/ path must name a document under docs/; a bare
// name must be the file name of one, or of a document in the repository
// root. Files parse concurrently: the tree holds megabytes of generated Go.
func brokenCitations(root string, tr tree) ([]string, error) {
	byPath := map[string]sections{}
	byName := map[string][]string{} // file name -> paths, research notes first
	for _, p := range slices.Concat(tr.docs, tr.rootMD) {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return nil, err
		}
		byPath[p] = parseSections(string(src))
		byName[path.Base(p)] = append(byName[path.Base(p)], p)
	}
	for _, ps := range byName {
		slices.SortStableFunc(ps, func(a, b string) int {
			rank := func(p string) int {
				if strings.HasPrefix(p, "docs/research/") {
					return 0
				}
				return 1
			}
			return rank(a) - rank(b)
		})
	}
	resolves := func(cited, num string) (string, string) {
		ps := byName[cited]
		if strings.HasPrefix(cited, "docs/") {
			ps = nil
			if _, ok := byPath[cited]; ok {
				ps = []string{cited}
			}
		}
		switch {
		case len(ps) == 0:
			return cited, "no such document under docs/ or the repository root; cite a note as <note>.md <section>"
		case num == "" || slices.ContainsFunc(ps, func(p string) bool { return byPath[p].has(num) }):
			return "", ""
		}
		msg := fmt.Sprintf("%s has no section %s", ps[0], num)
		if near := byPath[ps[0]].nearest(num); len(near) > 0 {
			msg += "; nearest sections: " + strings.Join(near, ", ")
		}
		return cited + " " + num, msg
	}
	broken := make([][]string, len(tr.goSrc))
	errs := make([]error, len(tr.goSrc))
	slots := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, rel := range tr.goSrc {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			broken[i], errs[i] = brokenInFile(root, rel, resolves)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return slices.Concat(broken...), nil
}

// brokenInFile returns the citations in the comments of one Go file that
// resolves rejects, as "file:line: citation: reason". For a document and
// section number it rejects, resolves returns the citation to print and the
// reason; for one it accepts, an empty reason.
func brokenInFile(root, rel string, resolves func(cited, num string) (name, why string)) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var broken []string
	for _, group := range f.Comments {
		for _, c := range group.List {
			line := fset.Position(c.Pos()).Line
			for _, m := range citation.FindAllStringSubmatchIndex(c.Text, -1) {
				cited, num := c.Text[m[2]:m[3]], ""
				if m[4] >= 0 {
					num = c.Text[m[4]:m[5]]
				}
				if name, why := resolves(cited, num); why != "" {
					at := line + strings.Count(c.Text[:m[2]], "\n")
					broken = append(broken, fmt.Sprintf("%s:%d: %s: %s", rel, at, name, why))
				}
			}
		}
	}
	return broken, nil
}
