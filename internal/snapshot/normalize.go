package snapshot

import (
	"slices"
	"strconv"
	"strings"

	// Named apart from normalize, this package's tree walk.
	normalizer "github.com/sustinbebustin/understudy/internal/normalize"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// accountLen is the length of an AWS account id.
const accountLen = 12

// scope is the kind of object a member belongs to, which decides the
// member-name rules its value is subject to.
type scope int

const (
	// plainScope: every member-name rule applies.
	plainScope scope = iota
	// causeScope: a Cause object's members, whose Parent and Trace are
	// event ids as well.
	causeScope
	// userScope: a user-keyed map's members (Tags, Variables, Metadata),
	// whose names are the user's and mark nothing, so no member-name rule
	// applies.
	userScope
)

// scopeOf is the scope of the members of obj, an object that is the value of
// a member called name. A user map holds strings only, so an object named
// like one that holds anything else is a record keyed by a user-chosen name
// (a queue called "tags", an S3 object keyed "metadata"), and every rule
// applies to it.
func scopeOf(name string, obj tree) scope {
	switch ruleKey(name) {
	case "cause":
		return causeScope
	case "tags", "variables", "metadata":
		if allStrings(obj) {
			return userScope
		}
	}
	return plainScope
}

// allStrings reports whether every member of obj has a string value.
func allStrings(obj tree) bool {
	for _, m := range obj.members {
		if m.val.kind != jsonx.String {
			return false
		}
	}
	return true
}

// normalize applies the closed normalizer set (package documentation) to t,
// the value of a member called name in an object of scope in.
func normalize(t tree, name string, in scope) tree {
	switch t.kind {
	case jsonx.Object:
		inner := scopeOf(name, t)
		ms := make([]member, len(t.members))
		for i, m := range t.members {
			ms[i] = member{name: m.name, val: normalize(m.val, m.name, inner)}
		}
		return tree{kind: jsonx.Object, members: disambiguate(ms, normalizeText)}
	case jsonx.Array:
		es := make([]tree, len(t.elems))
		for i, e := range t.elems {
			es[i] = normalize(e, name, in)
		}
		return tree{kind: jsonx.Array, elems: es}
	case jsonx.String, jsonx.Number:
		if mask, ok := maskByName(ruleKey(name), t, in); ok {
			return str(mask)
		}
		if t.kind == jsonx.String {
			return str(normalizeText(t.text))
		}
		return t
	case jsonx.Null, jsonx.Bool:
		return t
	}
	return t
}

// maskByName masks a scalar whose member name marks it as an id or a
// wall-domain value: the member rules of normalize.Snapshot, plus the scope
// rules of state. State names these members by the convention of the
// package documentation, with SQS's senderId named outright. A member of a
// user-keyed map is never masked by name, and a Cause object's Parent and
// Trace are event ids as well.
func maskByName(key string, t tree, in scope) (string, bool) {
	switch {
	case in == userScope:
		return "", false
	case in == causeScope && (key == "parent" || key == "trace"):
		return normalizer.EventID.Mask(), true
	}
	kind := normalizer.String
	if t.kind == jsonx.Number {
		kind = normalizer.Number
	}
	return normalizer.Snapshot().Member(key, kind, t.text)
}

// ruleKey is the member name the member-name rules read: name without the
// "#n" suffixes disambiguate numbers members with, folded. A second pass
// over normalized output then applies the rules the first pass applied, so
// "metadata#1" stays a user map and normalizing is idempotent. A masked name
// keeps no text a rule could newly match: every mask ends in '>', and the
// text after the last mask is the original name's.
func ruleKey(name string) string {
	for {
		i := strings.LastIndexByte(name, '#')
		if i < 0 || i == len(name)-1 || strings.TrimLeft(name[i+1:], "0123456789") != "" {
			return foldName(name)
		}
		name = name[:i]
	}
}

// foldName lowercases a member name and drops '_' and '-', so requestId,
// RequestId and request_id compare alike, as normalize.Set.Member folds it.
func foldName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '_' || r == '-':
			return -1
		case 'A' <= r && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, name)
}

// disambiguate renames the members of ms, whose values are normalized, with
// rename. It then numbers "name#1", "name#2", ... every group of members
// that share a new name, in the order of their values, when the group has
// several members or rename changed its name. Maps keyed by masked ids then
// line up by content rather than by the ids, and a lone masked key lines up
// with the first of several. It reuses ms.
func disambiguate(ms []member, rename func(string) string) []member {
	masked := make(map[string]bool, len(ms))
	for i, m := range ms {
		ms[i].name = rename(m.name)
		if ms[i].name != m.name {
			masked[ms[i].name] = true
		}
	}
	slices.SortStableFunc(ms, func(a, b member) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.val.json(), b.val.json())
	})
	for i := 0; i < len(ms); {
		j := i + 1
		for j < len(ms) && ms[j].name == ms[i].name {
			j++
		}
		if j-i > 1 || masked[ms[i].name] {
			for k := i; k < j; k++ {
				ms[k].name += "#" + strconv.Itoa(k-i+1)
			}
		}
		i = j
	}
	sortMembers(ms)
	return ms
}

// normalizeText applies the text rules of normalize.Snapshot: receipt
// handles, the account of a scope key ("<account>/<region>",
// store.ScopeKeys), the request ids and durations of Lambda START, END and
// REPORT log lines, URL hosts with the account a queue URL path starts with,
// and ARN accounts.
func normalizeText(s string) string { return normalizer.Snapshot().Text(s) }

// normalizeEntry masks the account of a section entry name: the segment after
// services/<svc>/ when it is an account, alone or before an extension, as
// store.ScopeKeys and store.AccountKeys lay sections out in
// services/sqs/<account>/us-east-1.json and services/iam/<account>.json.
// Other segments are names, such as an S3 bucket named with 12 digits, and
// are kept.
func normalizeEntry(name string) string {
	rest, ok := strings.CutPrefix(name, servicesDir)
	if !ok {
		return name
	}
	svc, key, ok := strings.Cut(rest, "/")
	if !ok || len(key) < accountLen || !isAccount(key[:accountLen]) ||
		len(key) > accountLen && key[accountLen] != '/' && key[accountLen] != '.' {
		return name
	}
	return servicesDir + svc + "/" + normalizer.Account.Mask() + key[accountLen:]
}

func isAccount(s string) bool {
	if len(s) != accountLen {
		return false
	}
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }
