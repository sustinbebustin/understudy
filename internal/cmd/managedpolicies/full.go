package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// The fields of a policy page a ParseError of the single-file guide names.
const (
	fieldHeading       = "heading"
	fieldDescription   = "description"
	fieldType          = "type"
	fieldCreationTime  = "creation time"
	fieldEditedTime    = "edited time"
	fieldARN           = "arn"
	fieldPolicyVersion = "policy version"
	fieldDocument      = "document"
)

// The lines of a policy page (iam-evaluation.md 10.3), each colon where
// the guide puts it: outside the bold for Description, Type, Creation time
// and ARN, inside it for Edited time and Policy version.
const (
	descriptionPrefix  = "**Description**:"
	typePrefix         = "+ **Type**: "
	creationTimePrefix = "+ **Creation time**: "
	editedTimePrefix   = "+ **Edited time:** "
	arnLinePrefix      = "+ **ARN**: "
	versionPrefix      = "**Policy version:**"
	// guideTimeLayout is the guide's Creation and Edited time, always UTC.
	guideTimeLayout = "January 02, 2006, 15:04 UTC"
	// managedARNPrefix opens every ARN the guide publishes.
	managedARNPrefix = "arn:aws:iam::aws:policy/"
	fence            = "```"
)

var (
	// versionLineRE is the default version line; the id is the IAM model's
	// policyVersionIdType.
	versionLineRE = regexp.MustCompile(`^\*\*Policy version:\*\* (v[1-9]\d*(?:\.[A-Za-z0-9-]*)?) \(default\)$`)
	// anchorRE is a line holding only an anchor.
	anchorRE = regexp.MustCompile(`^<a name="([^"]*)"></a>$`)
)

// anchor is the anchor line for name.
func anchor(name string) string { return `<a name="` + name + `"></a>` }

// headingName is the text of a "## " heading line with its markdown
// backslash escapes removed, and false for any other line: the guide writes
// each underscore of a policy's heading as \_ (iam-evaluation.md 10.4).
func headingName(line string) (string, bool) {
	text, ok := strings.CutPrefix(trimEnd(line), "## ")
	if !ok {
		return "", false
	}
	return unescapeMarkdown(text), true
}

// unescapeMarkdown removes CommonMark's backslash escapes: a backslash
// before ASCII punctuation stands for that character. A policy name holds
// no backslash, so this loses nothing a heading can name.
func unescapeMarkdown(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.IndexByte(asciiPunctuation, s[i+1]) >= 0 {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// asciiPunctuation is what CommonMark lets a backslash escape.
const asciiPunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// parseFull reads every policy idx lists out of the single-file guide, in
// idx order: each policy page with its headings demoted one level, after a
// preamble that opens no policy (iam-evaluation.md 10.3, 10.4). Every
// problem is a *ParseError, all of them joined.
//
// A policy's section opens at "## <Name>", its underscores escaped as \_,
// immediately followed by its anchor and runs to the next line starting
// "## ". Fences are not tracked
// for that: the only fenced block of a page is its JSON document
// (iam-evaluation.md 10.3), and no line of a JSON text starts with "#", so
// a fence never hides a heading, and one page's unclosed fence cannot
// swallow the pages after it.
func parseFull(body []byte, idx index) ([]policycatalogue.Policy, error) {
	ls := lines(body)
	listed := make(map[string]bool, len(idx.policies))
	for _, e := range idx.policies {
		listed[e.name] = true
	}
	errs := unlistedSections(ls, listed)
	opens := map[string][]int{}
	for i := 0; i+1 < len(ls); i++ {
		if name, ok := headingName(ls[i]); ok && trimEnd(ls[i+1]) == anchor(name) {
			opens[name] = append(opens[name], i)
		}
	}
	policies := make([]policycatalogue.Policy, 0, len(idx.policies))
	for _, e := range idx.policies {
		r := pageReader{name: e.name, ls: ls}
		switch at := opens[e.name]; len(at) {
		case 0:
			r.bad(fieldHeading, 0, "no section: no line %q followed by %s", "## "+e.name, anchor(e.name))
		case 1:
			if p, ok := r.page(at[0]); ok {
				policies = append(policies, p)
			}
		default:
			r.bad(fieldHeading, at[1]+1, "a second section for the policy; line %d opens the first", at[0]+1)
		}
		errs = append(errs, r.errs...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return policies, nil
}

// unlistedSections reports each policy whose details anchor the guide has
// but llms.txt does not list: the guide and its index disagree.
func unlistedSections(ls []string, listed map[string]bool) []error {
	var errs []error
	for i, l := range ls {
		m := anchorRE.FindStringSubmatch(trimEnd(l))
		if len(m) < 2 {
			continue
		}
		name, ok := strings.CutSuffix(m[1], "-details")
		if !ok || listed[name] {
			continue
		}
		// The anchor opening the section of a policy named <X>-details is
		// no details anchor.
		if i > 0 {
			if heading, ok := headingName(ls[i-1]); ok && heading == m[1] {
				continue
			}
		}
		errs = append(errs, &ParseError{
			Source: fullSource, Policy: name, Field: fieldHeading, Line: i + 1,
			Problem: "the guide has a details section for this policy, which llms.txt does not list; the guide and its index disagree",
		})
	}
	return errs
}

// pageReader reads one policy's section of the guide's lines ls, keeping
// every problem found.
type pageReader struct {
	name string
	ls   []string
	errs []error
}

func (r *pageReader) bad(field string, line int, format string, args ...any) {
	r.errs = append(r.errs, &ParseError{Source: fullSource, Policy: r.name, Field: field, Line: line, Problem: fmt.Sprintf(format, args...)})
}

// page reads the section whose heading is line start (0-based), and false
// when any field is wrong.
func (r *pageReader) page(start int) (policycatalogue.Policy, bool) {
	end := start + 2
	for end < len(r.ls) && !strings.HasPrefix(r.ls[end], "## ") {
		end++
	}
	intro := span{from: start + 2, to: r.nextSubheading(start+2, end), where: "before the page's first ### heading", found: true}
	p := policycatalogue.Policy{Name: r.name, Description: r.description(intro)}
	details := r.region(start, end, "-details")
	if rest, n, ok := r.once(details, typePrefix, fieldType); ok {
		var known bool
		if p.Type, known = policycatalogue.ParseType(rest); !known {
			r.bad(fieldType, n, "%q is none of the four types the guide publishes", rest)
		}
	}
	p.Created = r.guideTime(details, creationTimePrefix, fieldCreationTime)
	p.Updated = r.guideTime(details, editedTimePrefix, fieldEditedTime)
	p.ARN, p.Path = r.arn(details)
	p.VersionID = r.version(r.region(start, end, "-version"))
	p.Document = r.document(r.region(start, end, "-json"))
	return p, len(r.errs) == 0
}

// span is the lines [from, to) a field is looked for in, where says where
// they are for messages, and found is false when the anchor that opens
// them is missing.
type span struct {
	from, to int
	where    string
	found    bool
}

// nextSubheading is the first line in [from, to) starting "### ", or to.
func (r *pageReader) nextSubheading(from, to int) int {
	for i := from; i < to; i++ {
		if strings.HasPrefix(r.ls[i], "### ") {
			return i
		}
	}
	return to
}

// region is the lines after the anchor <name><suffix> in the section
// [start, end) up to the next "### " heading; it is empty, and not found,
// when the section lacks the anchor.
func (r *pageReader) region(start, end int, suffix string) span {
	want := anchor(r.name + suffix)
	for i := start; i < end; i++ {
		if trimEnd(r.ls[i]) == want {
			return span{from: i + 1, to: r.nextSubheading(i+1, end), where: "after " + want, found: true}
		}
	}
	return span{from: end, to: end, where: "where the guide puts it: the page has no " + want + " anchor"}
}

// once finds the one line of s starting with prefix and returns the rest
// of it and its 1-based number; it reports a line that is missing or
// repeated.
func (r *pageReader) once(s span, prefix, field string) (rest string, line int, ok bool) {
	for i := s.from; i < s.to; i++ {
		l := trimEnd(r.ls[i])
		if !strings.HasPrefix(l, prefix) {
			continue
		}
		if line > 0 {
			r.bad(field, i+1, "a second %q line; line %d is the first", prefix, line)
			return "", 0, false
		}
		rest, line = strings.TrimPrefix(l, prefix), i+1
	}
	if line == 0 {
		r.bad(field, 0, "no line starting %q %s", prefix, s.where)
		return "", 0, false
	}
	return rest, line, true
}

// description is the page's Description text, before its first "### "
// heading, with markdown's backslash escapes removed: the guide writes some
// descriptions' underscores as \_ and once an asterisk as \*
// (iam-evaluation.md 10.4), characters the text itself holds. Other markdown
// is kept as published.
func (r *pageReader) description(s span) string {
	rest, _, ok := r.once(s, descriptionPrefix, fieldDescription)
	if !ok {
		return ""
	}
	return unescapeMarkdown(strings.TrimPrefix(rest, " "))
}

// guideTime reads a Creation or Edited time, in UTC.
func (r *pageReader) guideTime(s span, prefix, field string) time.Time {
	rest, n, ok := r.once(s, prefix, field)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse(guideTimeLayout, rest)
	if err != nil {
		r.bad(field, n, "%q is not a time of the form %q", rest, guideTimeLayout)
		return time.Time{}
	}
	return t
}

// arn reads the ARN line, a backtick-quoted ARN of the aws account whose
// last path element is the policy's name, and returns the ARN and its
// path.
func (r *pageReader) arn(s span) (arn, path string) {
	rest, n, ok := r.once(s, arnLinePrefix, fieldARN)
	if !ok {
		return "", ""
	}
	inner, quoted := strings.CutPrefix(rest, "`"+managedARNPrefix)
	inner, closed := strings.CutSuffix(inner, "`")
	if !quoted || !closed {
		r.bad(fieldARN, n, "%s is not `%s<path><Name>` in backticks", rest, managedARNPrefix)
		return "", ""
	}
	path, name := aws.SplitIAMPath(inner)
	if name != r.name {
		r.bad(fieldARN, n, "the ARN names policy %q, not the section's %q", name, r.name)
		return "", ""
	}
	return managedARNPrefix + inner, path
}

// version reads the default version line of s, the version subsection.
func (r *pageReader) version(s span) string {
	rest, n, ok := r.once(s, versionPrefix, fieldPolicyVersion)
	if !ok {
		return ""
	}
	m := versionLineRE.FindStringSubmatch(versionPrefix + rest)
	if len(m) < 2 {
		r.bad(fieldPolicyVersion, n, "%q is not %q followed by v<N> and \" (default)\"", versionPrefix+rest, versionPrefix+" ")
		return ""
	}
	return m[1]
}

// document reads the JSON document of s, the JSON subsection: the lines
// between its first bare fence and the next, joined by "\n", byte for byte
// as published.
func (r *pageReader) document(s span) string {
	if !s.found {
		r.bad(fieldDocument, 0, "no %s anchor", anchor(r.name+"-json"))
		return ""
	}
	open := -1
	for i := s.from; i < s.to; i++ {
		if trimEnd(r.ls[i]) != fence {
			continue
		}
		if open < 0 {
			open = i
			continue
		}
		doc := strings.Join(r.ls[open+1:i], "\n")
		if strings.TrimSpace(doc) == "" {
			r.bad(fieldDocument, open+1, "the fenced document is empty")
			return ""
		}
		return doc
	}
	if open < 0 {
		r.bad(fieldDocument, 0, "no bare %s fence after %s", fence, anchor(r.name+"-json"))
	} else {
		r.bad(fieldDocument, open+1, "the fence opened here is not closed before the next heading")
	}
	return ""
}
