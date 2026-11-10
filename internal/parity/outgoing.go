package parity

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// dropped reports whether a replay leaves out the recorded request header
// name: the transport's signature replaces Authorization, X-Amz-Date and
// the token, and the transport sets the host and the length of the
// rewritten body.
func dropped(name string) bool {
	switch strings.ToLower(name) {
	case "host", "authorization", "x-amz-security-token", "x-amz-date", "content-length":
		return true
	}
	return false
}

// outgoing rewrites the recorded request req for understudy: each bound
// AWS value replaced by understudy's within the
// decoded URL path segments and query values, header values, form values,
// JSON string values, or else the raw body text; then re-encoded where a
// value changed. It signs in region for the signing name of the recorded
// host's first DNS label.
func outgoing(req Request, rs []normalize.Replacement, region string) (Outgoing, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return Outgoing{}, fmt.Errorf("the recorded URL %q does not parse: %w", req.URL, err)
	}
	label, _, _ := strings.Cut(u.Hostname(), ".")
	out := Outgoing{Method: req.Method, Target: substitutePath(u.EscapedPath(), rs), SigningName: signingName(label), Region: region}
	if u.RawQuery != "" || u.ForceQuery {
		out.Target += "?" + substituteForm(u.RawQuery, rs)
	}
	for _, h := range req.Headers {
		if !dropped(h.Name) {
			out.Header = append(out.Header, Header{Name: h.Name, Value: normalize.Substitute(rs, h.Value)})
		}
	}
	switch b := req.Body.(type) {
	case TextBody:
		out.Body = []byte(substituteText(firstContentType(req.Headers), b.Text, rs))
	case BinaryBody:
		out.Body = b.Bytes
	case NoBody, OmittedBody, UnrecordedBody, nil:
	}
	return out, nil
}

// signingName is the SigV4 signing name of a recorded host's first DNS
// label: the label itself, except SES's endpoint prefix "email", which
// signs as "ses" (ses.md 2.1). The capture drops Authorization, so the
// recorded credential scope cannot say.
func signingName(label string) string {
	if label == "email" {
		return "ses"
	}
	return label
}

// substitutePath substitutes within each decoded segment of an escaped
// URL path, which keeps its escaping where nothing changed.
func substitutePath(escaped string, rs []normalize.Replacement) string {
	if escaped == "" {
		return "/"
	}
	segs := strings.Split(escaped, "/")
	for i, seg := range segs {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			continue
		}
		if s := normalize.Substitute(rs, dec); s != dec {
			segs[i] = url.PathEscape(s)
		}
	}
	return strings.Join(segs, "/")
}

// substituteForm substitutes within each decoded value of a query string
// or form body, which keeps its encoding where nothing changed.
func substituteForm(raw string, rs []normalize.Replacement) string {
	pairs := strings.Split(raw, "&")
	for i, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		dec, err := url.QueryUnescape(v)
		if !ok || err != nil {
			continue
		}
		if s := normalize.Substitute(rs, dec); s != dec {
			pairs[i] = k + "=" + url.QueryEscape(s)
		}
	}
	return strings.Join(pairs, "&")
}

// substituteText substitutes within a request body by its Content-Type: a
// form's values, a JSON document's string values, else the text.
func substituteText(contentType, text string, rs []normalize.Replacement) string {
	if len(rs) == 0 {
		return text
	}
	if media, _, err := mime.ParseMediaType(contentType); err == nil && media == "application/x-www-form-urlencoded" {
		return substituteForm(text, rs)
	}
	return substituteJSON(contentType, text, rs)
}

// substituteJSON substitutes within the string values of text when it is
// JSON, else within the text itself; text that nothing changes is kept as
// recorded.
func substituteJSON(contentType, text string, rs []normalize.Replacement) string {
	d, ok := parseDocument(contentType, text).(jsonDocument)
	if !ok {
		return normalize.Substitute(rs, text)
	}
	v, changed := d.root.substituted(rs)
	if !changed {
		return text
	}
	return v.render()
}

// substituted is v with every string value substituted; changed reports
// whether one changed.
func (v jsonNode) substituted(rs []normalize.Replacement) (out jsonNode, changed bool) {
	switch v.kind {
	case jsonString:
		s := normalize.Substitute(rs, v.text)
		changed, v.text = s != v.text, s
	case jsonObject:
		ms := slices.Clone(v.members)
		for i := range ms {
			var c bool
			ms[i].value, c = ms[i].value.substituted(rs)
			changed = changed || c
		}
		v.members = ms
	case jsonArray:
		es := slices.Clone(v.elems)
		for i := range es {
			var c bool
			es[i], c = es[i].substituted(rs)
			changed = changed || c
		}
		v.elems = es
	case jsonNull, jsonBool, jsonNumber:
	}
	return v, changed
}

// routable reports whether this replayer routes a recorded request: awsJson
// by its X-Amz-Target header, awsQuery by the Action of its form body or
// query. Anything else only REST routing could place.
func routable(req Request) bool {
	if _, ok := firstHeader(req.Headers, "X-Amz-Target"); ok {
		return true
	}
	if u, err := url.Parse(req.URL); err == nil && u.Query().Has("Action") {
		return true
	}
	b, ok := req.Body.(TextBody)
	if !ok || req.Method != http.MethodPost {
		return false
	}
	form, err := url.ParseQuery(b.Text)
	return err == nil && form.Has("Action")
}
