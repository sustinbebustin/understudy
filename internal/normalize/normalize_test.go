package normalize

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// allNames is the closed set in its declared order: the snapshot set first
// (snapshot diff --normalize, ARCHITECTURE.md 12), then the names added for
// recorded HTTP exchanges (ARCHITECTURE.md 19).
var allNames = []Name{
	RequestID, ReceiptHandle, WallTime, Duration, Host, Account, AccessKey, EventID,
	Bound, HostID, ISOTime, EpochTime, HTTPDate, Caller, UniqueID, CanonicalID, PrincipalID, SourceIP, Credential,
}

func TestNamesListsTheClosedSetInDeclaredOrder(t *testing.T) {
	t.Parallel()
	if got := Names(); !slices.Equal(got, allNames) {
		t.Errorf("Names() = %v; want %v", got, allNames)
	}
}

// Every normalizer writes one placeholder, its name in angle brackets, so a
// masked value says which rule hid it.
func TestMaskIsTheNameInAngleBrackets(t *testing.T) {
	t.Parallel()
	for _, n := range allNames {
		if got, want := n.Mask(), "<"+string(n)+">"; got != want {
			t.Errorf("%s.Mask() = %q; want %q", n, got, want)
		}
	}
}

func TestParseNameReadsEveryName(t *testing.T) {
	t.Parallel()
	for _, n := range allNames {
		got, err := ParseName(string(n))
		if err != nil || got != n {
			t.Errorf("ParseName(%q) = %q, %v; want %q, nil", n, got, err, n)
		}
	}
}

func TestParseNameRefusesUnknownSpellings(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "nope", "Request-ID", "request_id", " request-id", "<request-id>"} {
		_, err := ParseName(s)
		var ne *NameError
		if !errors.As(err, &ne) {
			t.Errorf("ParseName(%q) error = %v; want a *NameError", s, err)
			continue
		}
		if ne.Text != s {
			t.Errorf("ParseName(%q): NameError.Text = %q; want the input", s, ne.Text)
		}
		msg := ne.Error()
		if want := `normalizer "` + s + `" is unknown; use one of [`; !strings.HasPrefix(msg, want) || !strings.HasSuffix(msg, "]") {
			t.Errorf("ParseName(%q) error = %q; want %q...]", s, msg, want)
		}
		for _, n := range allNames {
			if !strings.Contains(msg, string(n)) {
				t.Errorf("ParseName(%q) error = %q; want it to list %q", s, msg, n)
			}
		}
	}
}

func TestSetsHoldTheirNamesInRuleOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		set  Set
		want []Name
	}{
		{"snapshot", Snapshot(), []Name{RequestID, ReceiptHandle, WallTime, Duration, Host, Account, AccessKey, EventID}},
		// Bound first, so a bound value is replaced before any rule can mask
		// part of it; Caller before Account, so the replay caller's root ARN
		// is one placeholder rather than an ARN with a masked account.
		{"exchange", Exchange(), []Name{Bound, RequestID, HostID, ISOTime, EpochTime, HTTPDate, Caller, UniqueID, CanonicalID, PrincipalID, SourceIP, Credential, Account, ReceiptHandle}},
	}
	for _, c := range cases {
		if got := c.set.Names(); !slices.Equal(got, c.want) {
			t.Errorf("%s: Names() = %v; want %v", c.name, got, c.want)
		}
		for _, n := range allNames {
			if got, want := c.set.Has(n), slices.Contains(c.want, n); got != want {
				t.Errorf("%s: Has(%s) = %v; want %v", c.name, n, got, want)
			}
		}
	}
}

type memberCase struct {
	name  string
	kind  Kind
	value string
	want  string // "" when no member rule matches
}

func checkMember(t *testing.T, set string, s Set, cases []memberCase) {
	t.Helper()
	for _, c := range cases {
		got, ok := s.Member(c.name, c.kind, c.value)
		switch {
		case c.want == "" && ok:
			t.Errorf("%s.Member(%q, %d, %q) = %q; want no rule to match", set, c.name, c.kind, c.value, got)
		case c.want != "" && (!ok || got != c.want):
			t.Errorf("%s.Member(%q, %d, %q) = %q, %v; want %q, true", set, c.name, c.kind, c.value, got, ok, c.want)
		}
	}
}

func TestExchangeMemberRules(t *testing.T) {
	t.Parallel()
	checkMember(t, "Exchange()", Exchange(), []memberCase{
		// testdata/parity/sts/R15/06-assume-ok.json: <RequestId> in
		// ResponseMetadata; member names fold case, "_" and "-".
		{"RequestId", String, "eb14de1e-89b2-447d-8836-9ac7a692f6e9", "<request-id>"},
		{"request_id", String, "x", "<request-id>"},
		{"x-amzn-RequestId", String, "x", "<request-id>"},
		{"RequestId", Number, "17", "<request-id>"},
		// The S3 <HostId> of an error body.
		{"HostId", String, "aGVsbG8=", "<host-id>"},
		// Epoch timestamps: a number in seconds or milliseconds in any
		// notation, or a 10- or 13-digit string, under a *date, *time or
		// *timestamp member (testdata/parity/sqs/R01/02-recv1.json SentTimestamp).
		{"SentTimestamp", String, "1791959411396", "<epoch-time>"},
		{"ApproximateFirstReceiveTimestamp", String, "1791959411907", "<epoch-time>"},
		{"CreationDate", Number, "1791987671.668", "<epoch-time>"},
		{"CreationDate", Number, "1.791987671668E9", "<epoch-time>"},
		{"LastModifiedTime", Number, "1791987671668", "<epoch-time>"},
		{"CreationDate", String, "1791987671", "<epoch-time>"},
		{"CreationDate", Number, "5", ""},
		{"CreationDate", Number, "1791987671668000", ""},
		{"CreationDate", String, "17919876716", ""},
		// An ISO date under a *date member is free text, not an epoch value:
		// the member rule leaves it to Text, so ISO against epoch still differs.
		{"CreationDate", String, "2026-10-14T06:40:28Z", ""},
		{"Size", Number, "1791987671", ""},
		// testdata/parity/sts/R15/06-get-access-key-info-deleted.json <Account>.
		{"Account", String, "123456789012", "<account>"},
		{"OwnerAccountId", String, "123456789012", "<account>"},
		{"Account", String, "12345678901", ""},
		{"Account", Number, "123456789012", ""},
		// A root sender is the replay's caller (ADR 0012), so in the Exchange
		// set SenderId is the caller placeholder, Caller preceding Account.
		{"SenderId", String, "123456789012", "<caller>"},
		// testdata/parity/sts/R15/06-assume-ok.json: credentials are redacted
		// in the recording and minted by understudy; the whole value is masked.
		{"SecretAccessKey", String, "REDACTED", "<credential>"},
		{"SessionToken", String, "IQoJb3JpZ2luX2VjEJr//////////wEaCXVz", "<credential>"},
		{"Signature", String, "abc", "<credential>"},
		// Wall-time and access-key are snapshot rules only.
		{"Expiration", String, "2026-10-14T15:21:12Z", ""},
		{"AccessKeyId", String, "ASIAREDACTED", ""},
		{"Body", String, "m1", ""},
	})
}

func TestSnapshotMemberRules(t *testing.T) {
	t.Parallel()
	checkMember(t, "Snapshot()", Snapshot(), []memberCase{
		{"RequestId", String, "x", "<request-id>"},
		{"Expiration", String, "2026-10-14T15:21:12Z", "<wall-time>"},
		{"expiry", String, "2026-10-14T15:21:12Z", "<wall-time>"},
		{"LatestStreamLabel", String, "2026-10-14T06:40:28.123", "<wall-time>"},
		{"Account", String, "123456789012", "<account>"},
		{"SenderId", String, "123456789012", "<account>"},
		// internal/snapshot/diff_test.go: enqueuedEventId and journalNext.
		{"enqueuedEventId", Number, "17", "<event-id>"},
		{"journalNext", Number, "18", "<event-id>"},
		// Not snapshot rules.
		{"HostId", String, "x", ""},
		{"SecretAccessKey", String, "x", ""},
		{"SentTimestamp", String, "1791959411396", ""},
	})
}

func TestExchangeTextRules(t *testing.T) {
	t.Parallel()
	const root = "arn:aws:iam::123456789012:root"
	cases := map[string]string{ //nolint:gosec // G101: redacted and documentation credentials, the masked values under test
		// testdata/parity/sts/R15/06-assume-ok.json <Expiration>.
		"2026-10-14T15:21:12Z":                 "<iso-time>",
		"at 2026-10-14T15:21:12.123+02:00 now": "at <iso-time> now",
		"2026-10-14":                           "2026-10-14",
		// testdata/parity/kms/KMS-alias/01-duplicate.json Date header.
		"Wed, 14 Oct 2026 06:40:28 GMT": "<http-date>",
		// testdata/parity/kms/KMS-alias/01-duplicate.json message: the
		// account field of an ARN.
		"An alias with the name arn:aws:kms:us-east-2:123456789012:alias/usr-rec-20261014-dup already exists": "An alias with the name arn:aws:kms:us-east-2:<account>:alias/usr-rec-20261014-dup already exists",
		"123456789012/us-east-2": "<account>/us-east-2",
		// The Exchange set masks the account of a URL path but keeps the
		// host, which a binding settles (testdata/parity/sqs/R01 QueueUrl).
		"https://sqs.us-east-2.amazonaws.com/123456789012/usr-rec-20261014-std": "https://sqs.us-east-2.amazonaws.com/<account>/usr-rec-20261014-std",
		// testdata/parity/sts/R15/06-assume-denied.json message: the
		// recording caller, and the replay caller, the masked account's root
		// (ADR 0012), are one placeholder; the resource's account is masked.
		"User: arn:aws:sts::123456789012:assumed-role/AccountFullAccessRole/00000000-0000-0000-0000-000000000003 is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::123456789012:role/usr-rec-20261014-deny": "User: <caller> is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::<account>:role/usr-rec-20261014-deny",
		"User: " + root + " is not authorized": "User: <caller> is not authorized",
		root:                                   "<caller>",
		// testdata/parity/sqs/R01/02-recv1.json SenderId: a role session.
		"AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003": "<caller>",
		// testdata/parity/sts/R15/06-assume-ok.json <AssumedRoleId>: masked
		// IAM unique ids (testdata/README.md).
		"AROARZPUZDIKAAAAAAAHA:rec": "<unique-id>:rec",
		"AIDARZPUZDIKAAAAAAAB":      "<unique-id>",
		"AROA1234":                  "AROA1234",
		// S3 canonical ids and owner principal ids (testdata/README.md).
		"<ID>" + strings.Repeat("ab", 32) + "</ID>": "<ID><canonical-id></ID>",
		`"ID":"` + strings.Repeat("0f", 32) + `"`:   `"ID":"<canonical-id>"`,
		"id=" + strings.Repeat("9c", 32):            "id=<canonical-id>",
		`"principalId":"AEXAMPLE1234567"`:           `"principalId":"<principal-id>"`,
		`{\"principalId\":\"AEXAMPLE1234567\"}`:     `{\"principalId\":\"<principal-id>\"}`,
		`"sourceIPAddress":"192.0.2.10"`:            `"sourceIPAddress":"<source-ip>"`,
		`"sourceIp":"198.51.100.7"`:                 `"sourceIp":"<source-ip>"`,
		// testdata/parity/sts/R15/06-assume-ok.json <AccessKeyId>, and a
		// minted one; credential query parameters of a presigned URL.
		"ASIAREDACTED":         "<credential>",
		"AKIAREDACTED":         "<credential>",
		"AKIAIOSFODNN7EXAMPLE": "<credential>",
		"https://b.s3.amazonaws.com/k?X-Amz-Signature=0123abcd&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20261014%2Fus-east-2%2Fs3%2Faws4_request": "https://b.s3.amazonaws.com/k?X-Amz-Signature=<credential>&X-Amz-Credential=<credential>",
		// The whole value of a receipt handle (testdata/parity/sqs/R01).
		"AQEBMpo34lIChKeT2KqKUxah/cRpUTa4nC4C1LGJM2QE8fL+VoE4==": "<receipt-handle>",
		"AQEBshort": "AQEBshort",
		// Free text with none of the masked values is kept.
		"m1":                         "m1",
		"application/x-amz-json-1.1": "application/x-amz-json-1.1",
	}
	s := Exchange()
	for in, want := range cases {
		if got := s.Text(in); got != want {
			t.Errorf("Exchange().Text(%q) = %q; want %q", in, got, want)
		}
	}
}

// The snapshot set reproduces snapshot diff --normalize; these cases are
// internal/snapshot/normalize_test.go's, which must stay green (ARCHITECTURE.md 12).
func TestSnapshotTextRules(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7":                                                  "<receipt-handle>",
		"arn:aws:sqs:us-east-1:123456789012:orders":                                             "arn:aws:sqs:us-east-1:<account>:orders",
		"arn:aws:iam::123456789012:root":                                                        "arn:aws:iam::<account>:root",
		"http://sqs.us-east-1.localhost:4566/123456789012/orders":                               "http://<host>/<account>/orders",
		"START RequestId: 6f1c2a3b-0000-4000-8000-000000000001 Version: $LATEST":                "START RequestId: <request-id> Version: $LATEST",
		"REPORT RequestId: x\tDuration: 1.5 ms\tBilled Duration: 2 ms\tInit Duration: 30.25 ms": "REPORT RequestId: <request-id>\tDuration: <duration> ms\tBilled Duration: <duration> ms\tInit Duration: <duration> ms",
		"123456789012/us-east-1":                                                                "<account>/us-east-1",
		// Exchange-only text rules do not apply.
		"2026-10-14T15:21:12Z":          "2026-10-14T15:21:12Z",
		"Wed, 14 Oct 2026 06:40:28 GMT": "Wed, 14 Oct 2026 06:40:28 GMT",
		"ASIAREDACTED":                  "ASIAREDACTED",
	}
	s := Snapshot()
	for in, want := range cases {
		if got := s.Text(in); got != want {
			t.Errorf("Snapshot().Text(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestSubstitute(t *testing.T) {
	t.Parallel()
	const (
		aws  = "157cb509-e842-4095-b0b6-e97188e0a507" // testdata/parity/kms/KMS-alias/01-duplicate.json TargetKeyId
		ours = "0f0e0d0c-0b0a-4909-8807-060504030201"
	)
	cases := []struct {
		name string
		rs   []Replacement
		in   string
		want string
	}{
		{"none", nil, "abc", "abc"},
		{"every occurrence", []Replacement{{Old: aws, New: ours}}, `{"KeyId":"` + aws + `","Arn":"arn:aws:kms:us-east-2:123456789012:key/` + aws + `"}`, `{"KeyId":"` + ours + `","Arn":"arn:aws:kms:us-east-2:123456789012:key/` + ours + `"}`},
		{"absent", []Replacement{{Old: aws, New: ours}}, "no id here", "no id here"},
		// The longest Old wins where two start at the same place.
		{"longest first", []Replacement{{Old: "ab", New: "X"}, {Old: "abc", New: "Y"}}, "abcab", "YX"},
		// A replaced span is never rescanned: a New holding another Old,
		// or itself, stays as written.
		{"no rescan of another", []Replacement{{Old: "x", New: "y"}, {Old: "y", New: "z"}}, "x", "y"},
		{"no rescan of itself", []Replacement{{Old: "a", New: "aa"}}, "a", "aa"},
		{"chain", []Replacement{{Old: "a", New: "b"}, {Old: "b", New: "c"}}, "ab", "bc"},
	}
	for _, c := range cases {
		if got := Substitute(c.rs, c.in); got != c.want {
			t.Errorf("%s: Substitute(%v, %q) = %q; want %q", c.name, c.rs, c.in, got, c.want)
		}
	}
}

// Substituting values that a text does not hold leaves it unchanged, and
// swapping a value for itself is the identity.
func TestSubstituteProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringMatching(`[a-z ]{0,40}`).Draw(t, "s")
		olds := rapid.SliceOf(rapid.StringMatching(`[A-Z0-9]{1,6}`)).Draw(t, "olds")
		absent := make([]Replacement, 0, len(olds))
		for _, o := range olds {
			absent = append(absent, Replacement{Old: o, New: rapid.String().Draw(t, "new")})
		}
		selves := rapid.SliceOf(rapid.StringMatching(`[a-z]{1,4}`)).Draw(t, "selves")
		self := make([]Replacement, 0, len(selves))
		for _, o := range selves {
			self = append(self, Replacement{Old: o, New: o})
		}
		if got := Substitute(absent, s); got != s {
			t.Fatalf("Substitute(%v, %q) = %q; want it unchanged: no Old occurs", absent, s, got)
		}
		if got := Substitute(self, s); got != s {
			t.Fatalf("Substitute(%v, %q) = %q; want it unchanged: each Old maps to itself", self, s, got)
		}
		// Every occurrence of a lone Old is replaced.
		old := rapid.StringMatching(`[A-Z]{2,5}`).Draw(t, "old")
		parts := rapid.SliceOfN(rapid.StringMatching(`[a-z ]{0,5}`), 1, 5).Draw(t, "parts")
		in := strings.Join(parts, old)
		want := strings.Join(parts, "<v>")
		if got := Substitute([]Replacement{{Old: old, New: "<v>"}}, in); got != want {
			t.Fatalf("Substitute(%q -> <v>, %q) = %q; want %q", old, in, got, want)
		}
	})
}
