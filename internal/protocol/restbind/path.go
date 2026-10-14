package restbind

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// PathError is a request path that does not have the shape of the
// operation's URI pattern. The edge routes by the same pattern, so it means
// a request reached the wrong operation: a bug, not a client mistake.
type PathError struct {
	Path    string
	Pattern string
}

func (e *PathError) Error() string {
	return fmt.Sprintf("restbind: path %q does not match the URI pattern %s; the request was routed to the wrong operation", e.Path, e.Pattern)
}

// pathLabels returns the label values of the escaped request path for
// pattern, splitting the path the way registry.RouteTable.Match does:
// segments are split before they are percent-decoded, once, so %2F stays
// inside its label and "." and ".." segments are kept verbatim; a greedy
// label joins its decoded segments with "/" and owns a trailing slash when
// it ends the pattern (the S3 key "dir/"). A segment with an invalid escape
// is a *BindError.
func pathLabels(pattern []registry.Segment, path string) (map[string]string, error) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	greedyAt, name := -1, ""
	for i, s := range pattern {
		if g, ok := s.(registry.Greedy); ok {
			greedyAt, name = i, g.Name
		}
	}
	after := len(pattern) - greedyAt - 1
	if (greedyAt < 0 || after > 0) && len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	mismatch := &PathError{Path: path, Pattern: patternString(pattern)}
	out := map[string]string{}
	if greedyAt < 0 {
		if len(segs) != len(pattern) {
			return nil, mismatch
		}
		return out, bindEach(out, pattern, segs)
	}
	if len(segs) < greedyAt+1+after {
		return nil, mismatch
	}
	if err := bindEach(out, pattern[:greedyAt], segs[:greedyAt]); err != nil {
		return nil, err
	}
	if err := bindEach(out, pattern[greedyAt+1:], segs[len(segs)-after:]); err != nil {
		return nil, err
	}
	parts := segs[greedyAt : len(segs)-after]
	decoded := make([]string, len(parts))
	for i, p := range parts {
		v, err := url.PathUnescape(p)
		if err != nil {
			return nil, &BindError{Location: InLabel, Name: name, Value: strings.Join(parts, "/"), Reason: "it has an invalid percent-escape"}
		}
		decoded[i] = v
	}
	out[name] = strings.Join(decoded, "/")
	return out, nil
}

// bindEach decodes into out the label segments of pattern, which holds no
// greedy label, from the segs at the same indices.
func bindEach(out map[string]string, pattern []registry.Segment, segs []string) error {
	for i, s := range pattern {
		l, ok := s.(registry.Label)
		if !ok {
			continue
		}
		v, err := url.PathUnescape(segs[i])
		if err != nil {
			return &BindError{Location: InLabel, Name: l.Name, Value: segs[i], Reason: "it has an invalid percent-escape"}
		}
		out[l.Name] = v
	}
	return nil
}

func patternString(pattern []registry.Segment) string {
	var b strings.Builder
	for _, s := range pattern {
		b.WriteByte('/')
		switch s := s.(type) {
		case registry.Literal:
			b.WriteString(s.Value)
		case registry.Label:
			b.WriteString("{" + s.Name + "}")
		case registry.Greedy:
			b.WriteString("{" + s.Name + "+}")
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// requestPath is the escaped path exactly as the client sent it: the
// request target up to its query, else URL.EscapedPath. It is never
// cleaned, so %2F, "." and ".." reach the labels unchanged
// (go-toolchain.md 5).
func requestPath(r *http.Request) string {
	if r.RequestURI == "" {
		return r.URL.EscapedPath()
	}
	target := r.RequestURI
	for _, scheme := range []string{"http://", "https://"} {
		if rest, ok := strings.CutPrefix(target, scheme); ok {
			target = rest[strings.IndexByte(rest+"/", '/'):]
		}
	}
	path, _, _ := strings.Cut(target, "?")
	return path
}
