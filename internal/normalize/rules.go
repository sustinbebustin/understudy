package normalize

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/store"
)

// accountLen is the length of an AWS account id.
const accountLen = 12

// The text rules of the Exchange set. The masked identity values follow the
// patterns scripts/distill-parity masks them by (testdata/README.md, "Parity
// fixture format"): UNIQUE_ID, SESSION_UUIDS, CANONICAL_IDS, OWNER_PRINCIPAL
// and CALLER_IP. The role session id is masked to
// 00000000-0000-0000-0000-00000000000N, so a session of the recording caller
// is recognized by that prefix.
var (
	isoTimePattern  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})`)
	httpDatePattern = regexp.MustCompile(`(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), \d{2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{4} \d{2}:\d{2}:\d{2} GMT`)
	// The recording caller, an assumed-role session, as an ARN and as the
	// role id and session pair SQS writes in SenderId; the replay caller,
	// the masked account's root (ADR 0012).
	callerSessionARN = regexp.MustCompile(`\barn:aws:sts::\d{12}:assumed-role/[\w+=,.@-]+/00000000-0000-0000-0000-[0-9a-f]{12}\b`)
	callerSessionID  = regexp.MustCompile(`\bA(?:ROA|IDA)[A-Z2-7]{16,17}:00000000-0000-0000-0000-[0-9a-f]{12}\b`)
	callerRoot       = regexp.MustCompile(`\barn:aws:iam::\d{12}:root\b`)
	uniqueIDPattern  = regexp.MustCompile(`\bA(?:IDA|ROA|IPA|NPA|NVA|GPA|PKA|SCA|CCA)[A-Z2-7]{16,17}\b`)
	// S3 canonical user ids: in XML, in JSON (quotes at any escaping depth)
	// and in a grant header's id=.
	canonicalXML    = regexp.MustCompile(`(<ID>)[0-9a-f]{64}(</ID>)`)
	canonicalJSON   = regexp.MustCompile(`(\\*"ID\\*"\s*:\s*\\*")[0-9a-f]{64}(\\*")`)
	canonicalGrant  = regexp.MustCompile(`(\bid="?)[0-9a-f]{64}\b`)
	ownerPrincipal  = regexp.MustCompile(`(\\*"principalId\\*"\s*:\s*\\*")A[0-9A-Z]{9,20}(\\*")`)
	callerIP        = regexp.MustCompile(`((?:sourceIPAddress|sourceIp|SourceIp|[Xx]-[Ff]orwarded-[Ff]or)(?:\\*"\s*:\s*\\*"|'?\s*:\s*b?'|:\s*))\d{1,3}(?:\.\d{1,3}){3}\b`)
	credentialParam = regexp.MustCompile(`([?&](?:X-Amz-Signature|X-Amz-Credential|X-Amz-Security-Token|Signature)=)[^&#\s"'<>]+`)
	accessKeyID     = regexp.MustCompile(`\b(?:AKIA|ASIA)(?:[A-Z0-9]{16}|REDACTED)\b`)
)

// masksMember reports whether n's member rule masks a scalar of kind holding
// value under the folded member name key.
func (n Name) masksMember(key string, kind Kind, value string) bool {
	switch n {
	case RequestID:
		return strings.HasSuffix(key, "requestid")
	case WallTime:
		return isWallName(key)
	case Account:
		return isAccountName(key) && kind == String && isAccount(value)
	case AccessKey:
		return strings.HasSuffix(key, "accesskeyid") && kind == String && encodesAccount(value)
	case EventID:
		return strings.HasSuffix(key, "eventid") || key == "journalnext"
	case HostID:
		return key == "hostid" && kind == String
	case EpochTime:
		return isTimeName(key) && isEpoch(kind, value)
	case Caller:
		// A root sender is the replay's caller (ADR 0012).
		return key == "senderid" && kind == String && isAccount(value)
	case Credential:
		return (key == "secretaccesskey" || key == "sessiontoken" || key == "signature") && kind == String
	case ReceiptHandle, Duration, Host, Bound, ISOTime, HTTPDate, UniqueID, CanonicalID, PrincipalID, SourceIP:
		return false
	}
	return false
}

// maskText applies n's text rule to s.
func (n Name) maskText(s string) string {
	switch n {
	case RequestID:
		return maskLogRequestID(s)
	case ReceiptHandle:
		if isReceiptHandle(s) {
			return n.Mask()
		}
		return s
	case Duration:
		return maskReportDurations(s)
	case Host:
		return maskURLs(s, true)
	case Account:
		return maskAccounts(s)
	case ISOTime:
		return isoTimePattern.ReplaceAllLiteralString(s, n.Mask())
	case HTTPDate:
		return httpDatePattern.ReplaceAllLiteralString(s, n.Mask())
	case Caller:
		s = callerSessionARN.ReplaceAllLiteralString(s, n.Mask())
		s = callerSessionID.ReplaceAllLiteralString(s, n.Mask())
		return callerRoot.ReplaceAllLiteralString(s, n.Mask())
	case UniqueID:
		return uniqueIDPattern.ReplaceAllLiteralString(s, n.Mask())
	case CanonicalID:
		s = canonicalXML.ReplaceAllString(s, "${1}"+n.Mask()+"${2}")
		s = canonicalJSON.ReplaceAllString(s, "${1}"+n.Mask()+"${2}")
		return canonicalGrant.ReplaceAllString(s, "${1}"+n.Mask())
	case PrincipalID:
		return ownerPrincipal.ReplaceAllString(s, "${1}"+n.Mask()+"${2}")
	case SourceIP:
		return callerIP.ReplaceAllString(s, "${1}"+n.Mask())
	case Credential:
		s = credentialParam.ReplaceAllString(s, "${1}"+n.Mask())
		return accessKeyID.ReplaceAllLiteralString(s, n.Mask())
	case WallTime, AccessKey, EventID, Bound, HostID, EpochTime:
		return s
	}
	return s
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

// isAccountName lists the members whose 12-digit values are accounts: names
// ending in Account or AccountId, and an SQS message's senderId, which is
// the account of a root sender.
func isAccountName(key string) bool {
	return strings.HasSuffix(key, "account") || strings.HasSuffix(key, "accountid") || key == "senderid"
}

// isTimeName lists the members whose values may be epoch timestamps: names
// ending in Date, Time or Timestamp (SQS's SentTimestamp, KMS's
// CreationDate).
func isTimeName(key string) bool {
	return strings.HasSuffix(key, "date") || strings.HasSuffix(key, "time") || strings.HasSuffix(key, "timestamp")
}

// isEpoch reports whether value is an epoch reading in seconds or
// milliseconds: a number in [1e9, 1e10) or [1e12, 1e13) in any notation, or
// a string of exactly 10 or 13 digits. An ISO 8601 string is not, so a
// format difference still shows.
func isEpoch(kind Kind, value string) bool {
	switch kind {
	case Number:
		v, err := strconv.ParseFloat(value, 64)
		return err == nil && (1e9 <= v && v < 1e10 || 1e12 <= v && v < 1e13)
	case String:
		return (len(value) == 10 || len(value) == 13) && allDigits(value)
	}
	return false
}

// encodesAccount reports whether s is an AKIA or ASIA access key id with an
// account in its bits, as every key ids mints is: its account differs
// between runs under different accounts even when the seed is the same.
func encodesAccount(s string) bool {
	_, ok := ids.DecodeAccount(s)
	return ok
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

// logPrefixes start the Lambda log lines whose request id is masked.
const (
	startPrefix  = "START RequestId: "
	endPrefix    = "END RequestId: "
	reportPrefix = "REPORT RequestId: "
)

// maskLogRequestID masks the request id of a Lambda START, END or REPORT
// line.
func maskLogRequestID(s string) string {
	for _, prefix := range [...]string{startPrefix, endPrefix, reportPrefix} {
		rest, ok := strings.CutPrefix(s, prefix)
		if !ok {
			continue
		}
		end := strings.IndexAny(rest, " \t\n")
		if end < 0 {
			end = len(rest)
		}
		return prefix + RequestID.Mask() + rest[end:]
	}
	return s
}

// maskReportDurations masks every "Duration: <ms>" of a Lambda REPORT line.
func maskReportDurations(s string) string {
	if !strings.HasPrefix(s, reportPrefix) {
		return s
	}
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
			b.WriteString(Duration.Mask())
		}
		s = s[n:]
	}
}

// maskAccounts masks the account of a whole store.ScopeKeys value
// ("<account>/<region>"), an account that starts a URL path (an SQS queue
// URL), and the account field of every ARN.
func maskAccounts(s string) string {
	if scope, err := store.ScopeKeys().Decode(s); err == nil {
		return Account.Mask() + "/" + scope.Region.String()
	}
	return maskARNAccounts(maskURLs(s, false))
}

// maskURLs masks an account id that is the first path segment of an http or
// https URL, as in an SQS queue URL, and, when host is set, the URL's host
// and port.
func maskURLs(s string, host bool) string {
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
		if host {
			b.WriteString(Host.Mask())
		} else {
			b.WriteString(s[:n])
		}
		s = s[n:]
		if rest, ok := strings.CutPrefix(s, "/"); ok && len(rest) >= accountLen && isAccount(rest[:accountLen]) &&
			(len(rest) == accountLen || strings.IndexByte("/?# \t\n\"", rest[accountLen]) >= 0) {
			b.WriteString("/" + Account.Mask())
			s = rest[accountLen:]
		}
	}
}

// hostLen is the length of the URL host and port s starts with: the run of
// letters, digits and ".-:[]" that names, IPv4 and IPv6 literals and ports
// are made of, or the host mask itself, so normalizing is idempotent.
func hostLen(s string) int {
	if strings.HasPrefix(s, Host.Mask()) {
		return len(Host.Mask())
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

// maskARNAccounts masks the account field of every ARN in s.
func maskARNAccounts(s string) string {
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
		b.WriteString(Account.Mask())
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

func isAccount(s string) bool { return len(s) == accountLen && allDigits(s) }

func allDigits(s string) bool {
	for i := range len(s) {
		if !isDigit(s[i]) {
			return false
		}
	}
	return s != ""
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}
