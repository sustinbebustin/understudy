package snapshot

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Placeholders the normalizers write in place of what they mask.
const (
	maskRequestID     = "<request-id>"
	maskReceiptHandle = "<receipt-handle>"
	maskWallTime      = "<wall-time>"
	maskDuration      = "<duration>"
	maskHost          = "<host>"
	maskAccount       = "<account>"
	maskAccessKey     = "<access-key>"
	maskEventID       = "<event-id>"
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
	switch foldName(name) {
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
		if mask, ok := maskByName(foldName(name), t, in); ok {
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
// wall-domain value. State names these members by the convention of the
// package documentation, with SQS's senderId named outright. A member of a
// user-keyed map is never masked by name.
func maskByName(key string, t tree, in scope) (string, bool) {
	switch {
	case in == userScope:
		return "", false
	case strings.HasSuffix(key, "requestid"):
		return maskRequestID, true
	case isEventIDName(key) || in == causeScope && (key == "parent" || key == "trace"):
		return maskEventID, true
	case isWallName(key):
		return maskWallTime, true
	case isAccountName(key) && t.kind == jsonx.String && isAccount(t.text):
		return maskAccount, true
	case strings.HasSuffix(key, "accesskeyid") && t.kind == jsonx.String && encodesAccount(t.text):
		return maskAccessKey, true
	}
	return "", false
}

// encodesAccount reports whether s is an AKIA or ASIA access key id with an
// account in its bits, as every key ids mints is: its account differs
// between runs under different accounts even when the seed is the same.
func encodesAccount(s string) bool {
	_, ok := ids.DecodeAccount(s)
	return ok
}

// isEventIDName lists the members whose values are journal event ids: names
// ending in EventId, and the manifest's JournalNext.
func isEventIDName(key string) bool {
	return strings.HasSuffix(key, "eventid") || key == "journalnext"
}

// isAccountName lists the members whose 12-digit values are accounts: names
// ending in Account or AccountId, and an SQS message's senderId, which is
// the account of a root sender.
func isAccountName(key string) bool {
	return strings.HasSuffix(key, "account") || strings.HasSuffix(key, "accountid") || key == "senderid"
}

// isWallName lists the members whose values are wall-clock readings: STS
// credential expiry (Expiration on the wire, Expiry in identity's export)
// and DynamoDB Local's own timestamps and stream labels (ADR 0018).
func isWallName(key string) bool {
	switch key {
	case "expiration", "expiry", "creationdatetime", "lastupdatetopayperrequestdatetime",
		"approximatecreationdatetime", "lateststreamlabel", "streamlabel":
		return true
	}
	return false
}

// foldName lowercases a member name and drops '_' and '-', so requestId,
// RequestId and request_id compare alike.
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

// normalizeText masks receipt handles, the account of a scope key
// ("<account>/<region>", store.ScopeKeys), the request ids and durations of
// Lambda START, END and REPORT log lines, URL hosts with the account a queue
// URL path starts with, and ARN accounts.
func normalizeText(s string) string {
	if isReceiptHandle(s) {
		return maskReceiptHandle
	}
	if scope, err := store.ScopeKeys().Decode(s); err == nil {
		return maskAccount + "/" + scope.Region.String()
	}
	s = normalizeLogLine(s)
	s = normalizeURLs(s)
	return normalizeARNs(s)
}

// isReceiptHandle matches the receipt handles ids mints and real SQS
// handles: AQEB and at least 32 base64 characters.
func isReceiptHandle(s string) bool {
	rest, ok := strings.CutPrefix(s, "AQEB")
	if !ok || len(rest) < 32 {
		return false
	}
	for i := range len(rest) {
		c := rest[i]
		if !isAlnum(c) && c != '+' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}

// normalizeLogLine masks the request id of a Lambda START, END or REPORT
// line and every "Duration: <ms>" of a REPORT line.
func normalizeLogLine(s string) string {
	for _, prefix := range [...]string{"START RequestId: ", "END RequestId: ", "REPORT RequestId: "} {
		rest, ok := strings.CutPrefix(s, prefix)
		if !ok {
			continue
		}
		end := strings.IndexAny(rest, " \t\n")
		if end < 0 {
			end = len(rest)
		}
		s = prefix + maskRequestID + rest[end:]
		if prefix == "REPORT RequestId: " {
			s = maskDurations(s)
		}
		return s
	}
	return s
}

func maskDurations(s string) string {
	const label = "Duration: "
	var b strings.Builder
	for {
		i := strings.Index(s, label)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i+len(label)])
		s = s[i+len(label):]
		n := 0
		for n < len(s) && (isDigit(s[n]) || s[n] == '.') {
			n++
		}
		if n > 0 {
			b.WriteString(maskDuration)
		}
		s = s[n:]
	}
}

// normalizeURLs masks the host and port of every http and https URL, and an
// account id that is the URL path's first segment, as in an SQS queue URL.
func normalizeURLs(s string) string {
	var b strings.Builder
	for {
		i, scheme := nextURL(s)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		start := i + len(scheme)
		b.WriteString(s[:start])
		s = s[start:]
		n := hostLen(s)
		if n == 0 {
			continue
		}
		b.WriteString(maskHost)
		s = s[n:]
		if rest, ok := strings.CutPrefix(s, "/"); ok && len(rest) >= accountLen && isAccount(rest[:accountLen]) &&
			(len(rest) == accountLen || strings.IndexByte("/?# \t\n\"", rest[accountLen]) >= 0) {
			b.WriteString("/" + maskAccount)
			s = rest[accountLen:]
		}
	}
}

// hostLen is the length of the URL host and port s starts with: the run of
// letters, digits and ".-:[]" that names, IPv4 and IPv6 literals and ports
// are made of, or the <host> mask itself, so normalizing is idempotent.
func hostLen(s string) int {
	if strings.HasPrefix(s, maskHost) {
		return len(maskHost)
	}
	n := 0
	for n < len(s) && (isAlnum(s[n]) || strings.IndexByte(".-:[]", s[n]) >= 0) {
		n++
	}
	return n
}

// nextURL returns the index of the first "http://" or "https://" in s and
// which it is, or -1.
func nextURL(s string) (at int, scheme string) {
	h, hs := strings.Index(s, "http://"), strings.Index(s, "https://")
	switch {
	case h < 0 && hs < 0:
		return -1, ""
	case hs < 0 || (h >= 0 && h < hs):
		return h, "http://"
	default:
		return hs, "https://"
	}
}

// normalizeARNs masks the account field of every ARN in s.
func normalizeARNs(s string) string {
	const prefix = "arn:"
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i+len(prefix)])
		rest := s[i+len(prefix):]
		at := -1
		if i == 0 || !isAlnum(s[i-1]) {
			at = arnAccount(rest)
		}
		if at < 0 {
			s = rest
			continue
		}
		b.WriteString(rest[:at])
		b.WriteString(maskAccount)
		s = rest[at+accountLen:]
	}
}

// arnAccount returns the offset of the account in rest, the part of an ARN
// after "arn:", or -1 when rest does not continue as
// "<partition>:<service>:<region>:<12 digits>:". The region may be empty.
func arnAccount(rest string) int {
	off := 0
	for field := range 3 {
		j := strings.IndexByte(rest[off:], ':')
		if j < 0 || strings.ContainsAny(rest[off:off+j], " \t\n\"/") || (j == 0 && field < 2) {
			return -1
		}
		off += j + 1
	}
	if len(rest) <= off+accountLen || !isAccount(rest[off:off+accountLen]) || rest[off+accountLen] != ':' {
		return -1
	}
	return off
}

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
	return servicesDir + svc + "/" + maskAccount + key[accountLen:]
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

func isAlnum(c byte) bool {
	return isDigit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
