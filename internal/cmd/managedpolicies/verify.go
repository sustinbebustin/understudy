package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// Problem is one way the catalogue directory differs from what the
// generator writes.
type Problem struct {
	// Path is "policies.json", "SOURCE.json", "LICENSE" or "NOTICE".
	Path string
	// Policy is the policy's ARN, "" when the problem is not one policy's.
	Policy string
	Detail string
}

// VerifyError lists every problem -verify found, so one run reports them
// all.
type VerifyError struct {
	Dir      string
	Problems []Problem
}

func (e *VerifyError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s is not the catalogue the generator writes (%d problems):", e.Dir, len(e.Problems))
	for _, p := range e.Problems {
		if p.Policy != "" {
			fmt.Fprintf(&b, "\n  %s: %s: %s", p.Path, p.Policy, p.Detail)
		} else {
			fmt.Fprintf(&b, "\n  %s: %s", p.Path, p.Detail)
		}
	}
	b.WriteString("\nThe files are generator output and are never edited by hand; restore them with git checkout, or re-run 'go run ./internal/cmd/managedpolicies -fetch' to refresh them on purpose.")
	return b.String()
}

// sha256RE is a sha256 as SOURCE.json records it.
var sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sha256Hex is the lowercase hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verify checks the catalogue directory fsys, named dir in messages,
// offline: policies.json parses and is canonical, every document parses as
// an identity policy (see document), SOURCE.json is canonical and matches
// the catalogue and its hashes, and LICENSE and NOTICE are the generator's.
// It returns nil or a *VerifyError holding every problem found.
func verify(fsys fs.FS, dir string) error {
	v := verifier{fsys: fsys}
	cat := v.catalogue()
	src, srcOK := v.source()
	if cat != nil && srcOK {
		v.provenance(cat, src)
	}
	v.legal(src, srcOK)
	if len(v.problems) > 0 {
		return &VerifyError{Dir: dir, Problems: v.problems}
	}
	return nil
}

// verifier collects the problems verify finds in fsys.
type verifier struct {
	fsys     fs.FS
	problems []Problem
}

func (v *verifier) add(path, policy, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Policy: policy, Detail: fmt.Sprintf(format, args...)})
}

// read reads a data file, reporting it when it cannot be read.
func (v *verifier) read(name string) ([]byte, bool) {
	b, err := fs.ReadFile(v.fsys, name)
	if err != nil {
		v.add(name, "", "cannot be read (%v); -fetch writes it", err)
		return nil, false
	}
	return b, true
}

// catalogue checks policies.json and returns its catalogue, or nil when it
// does not parse.
func (v *verifier) catalogue() *policycatalogue.Catalogue {
	const name = awsmanagedpolicies.PoliciesFile
	data, ok := v.read(name)
	if !ok {
		return nil
	}
	cat, err := policycatalogue.Parse(data)
	if de, ok := errors.AsType[*policycatalogue.DecodeError](err); ok {
		v.add(name, "", "%v", de)
		return nil
	}
	if err != nil {
		arns := arnsByIndex(data)
		for _, e := range unjoin(err) {
			var ie *policycatalogue.InvalidError
			if errors.As(e, &ie) && ie.Index < len(arns) {
				v.add(name, arns[ie.Index], "%v", ie)
			} else {
				v.add(name, "", "%v", e)
			}
		}
		return nil
	}
	if encoded, err := cat.Encode(); err != nil {
		v.add(name, "", "%v", err)
	} else if !bytes.Equal(encoded, data) {
		v.add(name, "", "is not in the generator's canonical form (sorted by name regardless of case, two-space indent, one trailing newline); it first differs on line %d", firstDifferentLine(data, encoded))
	}
	for p := range cat.All() {
		v.document(p)
	}
	return cat
}

// document checks that p's document parses and is an identity policy
// (policydoc.ValidateIdentityForm). The rest of policydoc.ValidateIdentity
// is not applied: it ports moto's CreatePolicy checks, which IAM applies to
// a customer's upload, and AWS publishes its managed policies without that
// call. Some of AWS's published documents break those checks (resources
// naming IAM role templates, which the IAM resource path rule refuses), and
// AWS is the arbiter of what its own documents hold (iam-evaluation.md
// 10.6).
func (v *verifier) document(p policycatalogue.Policy) {
	const name = awsmanagedpolicies.PoliciesFile
	doc, perr := policydoc.Parse([]byte(p.Document))
	if perr != nil {
		v.add(name, p.ARN, "the document does not parse as a policy: %v; this is a finding about policydoc or the data for the maintainer, never patched in the data", perr)
		return
	}
	if verr := policydoc.ValidateIdentityForm(doc); verr != nil {
		v.add(name, p.ARN, "the document is not an identity policy: %v; this is a finding about policydoc or the data for the maintainer, never patched in the data", verr)
	}
}

// arnsByIndex is the arn member of each policy of policies.json, read
// leniently, so a policy New refuses can be named by its ARN.
func arnsByIndex(data []byte) []string {
	var f struct {
		Policies []struct {
			ARN string `json:"arn"`
		} `json:"policies"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	arns := make([]string, len(f.Policies))
	for i, p := range f.Policies {
		arns[i] = p.ARN
	}
	return arns
}

// unjoin is the errors err joins, or err alone.
func unjoin(err error) []error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return joined.Unwrap()
	}
	return []error{err}
}

// firstDifferentLine is the 1-based line on which a and b first differ.
func firstDifferentLine(a, b []byte) int {
	la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	for i := range min(len(la), len(lb)) {
		if !bytes.Equal(la[i], lb[i]) {
			return i + 1
		}
	}
	return min(len(la), len(lb)) + 1
}

// source checks SOURCE.json on its own and returns it, and false when it
// does not decode.
func (v *verifier) source() (Source, bool) {
	const name = awsmanagedpolicies.SourceFile
	raw, ok := v.read(name)
	if !ok {
		return Source{}, false
	}
	var src Source
	if err := json.Unmarshal(raw, &src, json.RejectUnknownMembers(true)); err != nil {
		v.add(name, "", "does not decode as SOURCE.json: %v", err)
		return Source{}, false
	}
	if encoded, err := encodeSource(src); err != nil {
		v.add(name, "", "%v", err)
	} else if !bytes.Equal(encoded, raw) {
		v.add(name, "", "is not in the generator's canonical form (two-space indent, members in order, one trailing newline); it first differs on line %d", firstDifferentLine(raw, encoded))
	}
	if d, err := time.Parse(time.DateOnly, src.Retrieved); err != nil || d.Format(time.DateOnly) != src.Retrieved {
		v.add(name, "", "retrieved is %q, not a YYYY-MM-DD date", src.Retrieved)
	}
	v.requests(src.Fetched)
	return src, true
}

// requests checks SOURCE.json's record of a run's three requests.
func (v *verifier) requests(fetched []Fetched) {
	const name = awsmanagedpolicies.SourceFile
	if len(fetched) != 3 {
		v.add(name, "", "records %d requests, not the three of a run: robots.txt, llms.txt and the single-file guide", len(fetched))
	} else {
		for i, want := range []string{docsBase + robotsPath, docsBase + indexPath} {
			if fetched[i].URL != want {
				v.add(name, "", "request %d is %s, not %s", i+1, fetched[i].URL, want)
			}
		}
		if u := fetched[2].URL; !strings.HasPrefix(u, guidePrefix) || !strings.HasSuffix(u, "/full.md") {
			v.add(name, "", "request 3 is %s, not a full.md under %s", u, guidePrefix)
		}
	}
	for _, f := range fetched {
		if !sha256RE.MatchString(f.SHA256) {
			v.add(name, "", "the sha256 of %s is %q, not 64 lowercase hex digits", f.URL, f.SHA256)
		}
	}
}

// provenance checks that SOURCE.json has one entry per policy of cat, in
// its order, each with the policy's anchor, page and document hash.
func (v *verifier) provenance(cat *policycatalogue.Catalogue, src Source) {
	const name = awsmanagedpolicies.SourceFile
	if len(src.Policies) != cat.Len() {
		v.add(name, "", "has %d policy entries, but policies.json holds %d policies", len(src.Policies), cat.Len())
	}
	i := 0
	for p := range cat.All() {
		if i < len(src.Policies) && src.Policies[i].ARN != p.ARN {
			v.add(name, "", "entry %d is for %s, but policy %d of policies.json is %s; the entries follow policies.json's order", i, src.Policies[i].ARN, i, p.ARN)
			break
		}
		i++
	}
	for _, e := range src.Policies {
		p, ok := cat.Lookup(e.ARN)
		if !ok {
			v.add(name, "", "has an entry for %s, which policies.json does not hold", e.ARN)
			continue
		}
		if e.Anchor != p.Name {
			v.add(name, p.ARN, "anchor is %q, not the policy's name %q", e.Anchor, p.Name)
		}
		if want := guidePrefix + p.Name + ".md"; e.Page != want {
			v.add(name, p.ARN, "page is %s, not %s", e.Page, want)
		}
		if got := sha256Hex([]byte(p.Document)); e.DocumentSHA256 != got {
			v.add(name, p.ARN, "documentSha256 is %s, but the document in policies.json hashes to %s", e.DocumentSHA256, got)
		}
	}
}

// legal checks LICENSE, and NOTICE against src when SOURCE.json decoded.
func (v *verifier) legal(src Source, srcOK bool) {
	if b, ok := v.read(awsmanagedpolicies.LicenseFile); ok && string(b) != licenseText {
		v.add(awsmanagedpolicies.LicenseFile, "", "differs from the licence text the generator writes; it first differs on line %d", firstDifferentLine(b, []byte(licenseText)))
	}
	if b, ok := v.read(awsmanagedpolicies.NoticeFile); ok && srcOK {
		if want := notice(src); !bytes.Equal(b, want) {
			v.add(awsmanagedpolicies.NoticeFile, "", "differs from the notice the generator writes for SOURCE.json; it first differs on line %d", firstDifferentLine(b, want))
		}
	}
}
