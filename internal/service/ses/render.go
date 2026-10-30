package ses

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// TemplateSend is the template a templated send used (ses.md 11.2): v1
// SendTemplatedEmail's stored template, or v2 Template content, stored or
// inline. Its rendered parts are the message's Subject, Text and HTML; a
// part with a gap is left unrendered (ses.md 11.8).
type TemplateSend struct {
	// Name is the stored template's name; "" for v2 inline content.
	Name string `json:"name,omitempty"`
	// Subject, Text and HTML are the template's parts as used, nil when
	// the template has none.
	Subject *string `json:"subject,omitempty"`
	Text    *string `json:"text,omitempty"`
	HTML    *string `json:"html,omitempty"`
	// Data is TemplateData as given.
	Data string `json:"data"`
	// Gaps are what understudy did not render, in part then source order.
	Gaps []RenderGap `json:"gaps,omitempty"`
}

// RenderGap is a template tag understudy does not render (ses.md 11.6 item
// 6): SES renders stored templates with Handlebars, of which understudy
// implements only {{name}} and dotted paths, and guesses at nothing else.
type RenderGap struct {
	// Part is "subject", "text" or "html"; "" when the gap is TemplateData
	// itself, which leaves every part unrendered.
	Part string `json:"part,omitempty"`
	// Tag is the tag as written, from "{{" to "}}", or to the part's end
	// when unclosed; "" for a TemplateData gap.
	Tag string `json:"tag,omitempty"`
	// Reason says why it was not rendered.
	Reason string `json:"reason"`
}

// clone is t sharing no memory with it.
func (t *TemplateSend) clone() *TemplateSend {
	if t == nil {
		return nil
	}
	c := *t
	c.Gaps = append([]RenderGap(nil), t.Gaps...)
	return &c
}

// Reasons a tag is a render gap.
const (
	gapConstruct = "a Handlebars construct other than {{name}} or a dotted path; understudy does not render it"
	gapUnclosed  = "the tag has no closing }}"
	gapMissing   = "TemplateData has no value at this path; what SES renders for it is unrecorded"
	gapEscaped   = "the value holds a character Handlebars escapes (& < > \" ' ` =); whether SES escapes it is unrecorded"
	gapValue     = "the value is not a string, boolean or integer; how SES formats it is unrecorded"
)

// render renders t's parts from its Data, recording a gap for each tag it
// does not render; a part with a gap is nil. TemplateData that is not one
// JSON object renders nothing; absent (v2's is optional) it is no values.
func (t *TemplateSend) render(absent bool) (subject, text, html *Content) {
	data := map[string]any{}
	if !absent {
		var err error
		if data, err = templateData(t.Data); err != nil {
			t.Gaps = append(t.Gaps, RenderGap{Reason: "TemplateData is not a JSON object (" + err.Error() + "); no part was rendered"})
			return nil, nil, nil
		}
	}
	part := func(name string, src *string) *Content {
		if src == nil {
			return nil
		}
		out, gaps := renderPart(name, *src, data)
		t.Gaps = append(t.Gaps, gaps...)
		if len(gaps) > 0 {
			return nil
		}
		return &Content{Data: out}
	}
	return part("subject", t.Subject), part("text", t.Text), part("html", t.HTML)
}

// templateData parses TemplateData as one JSON object, numbers kept as
// written.
func templateData(s string) (map[string]any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("null")
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("text after the object")
	}
	return m, nil
}

// renderPart substitutes each {{path}} of src from data. Every other tag is
// a gap, as are paths data lacks and values whose rendering is unrecorded.
func renderPart(part, src string, data map[string]any) (string, []RenderGap) {
	var out bytes.Buffer
	var gaps []RenderGap
	for {
		i := strings.Index(src, "{{")
		if i < 0 {
			out.WriteString(src)
			return out.String(), gaps
		}
		out.WriteString(src[:i])
		j := strings.Index(src[i+2:], "}}")
		if j < 0 {
			return "", append(gaps, RenderGap{Part: part, Tag: src[i:], Reason: gapUnclosed})
		}
		end := i + 2 + j + 2
		// A triple stash's third brace belongs to its tag.
		if strings.HasPrefix(src[end:], "}") && strings.HasPrefix(src[i+2:], "{") {
			end++
		}
		tag := src[i:end]
		escaped := i > 0 && src[i-1] == '\\'
		v, reason := substitute(tag, data)
		if escaped {
			reason = gapConstruct
		}
		if reason != "" {
			gaps = append(gaps, RenderGap{Part: part, Tag: tag, Reason: reason})
		}
		out.WriteString(v)
		src = src[end:]
	}
}

// substitute is the value of tag, "{{" path "}}", or the reason it is a gap.
func substitute(tag string, data map[string]any) (value, gap string) {
	path := strings.TrimSpace(tag[2 : len(tag)-2])
	segments := strings.Split(path, ".")
	for _, s := range segments {
		if !isIdentifier(s) {
			return "", gapConstruct
		}
	}
	var v any = data
	for _, s := range segments {
		m, ok := v.(map[string]any)
		if !ok {
			return "", gapMissing
		}
		if v, ok = m[s]; !ok {
			return "", gapMissing
		}
	}
	switch v := v.(type) {
	case string:
		if strings.ContainsAny(v, "&<>\"'`=") {
			return "", gapEscaped
		}
		return v, ""
	case bool:
		return strconv.FormatBool(v), ""
	case json.Number:
		// Only an integer JavaScript prints as written is certain.
		if n, err := strconv.ParseInt(v.String(), 10, 64); err == nil && strconv.FormatInt(n, 10) == v.String() && n > -1<<53 && n < 1<<53 {
			return v.String(), ""
		}
	}
	return "", gapValue
}

// isIdentifier reports whether s is one path segment understudy renders:
// an ASCII letter or "_", then letters, digits, "_" or "-", and not
// Handlebars' "this".
func isIdentifier(s string) bool {
	if s == "" || s == "this" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r == '-' || r >= '0' && r <= '9'):
		default:
			return false
		}
	}
	return true
}
