package normalize

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The masked identity values as the recordings hold them inside bodies:
// JSON nested in a JSON string, so quotes are escaped once or more
// (testdata/parity/sqs/R17/02-events-1.json, an S3 event in an SQS message
// body; testdata/parity/logs/R13/01-logged-events.json, a function URL event
// in a log line).
func TestExchangeTextRulesInNestedJSON(t *testing.T) {
	t.Parallel()
	cases := map[string]string{ //nolint:gosec // G101: masked placeholders and a fake token prefix, the values under test
		`{\"principalId\":\"AEXAMPLE000009\"}`:                                                       `{\"principalId\":\"<principal-id>\"}`,
		`{\\\"principalId\\\": \\\"AEXAMPLE000009\\\"}`:                                              `{\\\"principalId\\\": \\\"<principal-id>\\\"}`,
		`{\"requestParameters\":{\"sourceIPAddress\":\"198.51.100.100\"}}`:                           `{\"requestParameters\":{\"sourceIPAddress\":\"<source-ip>\"}}`,
		`\"x-forwarded-for\": \"198.51.100.100\", \"sourceIp\": \"198.51.100.100\"`:                  `\"x-forwarded-for\": \"<source-ip>\", \"sourceIp\": \"<source-ip>\"`,
		`{\"principalId\":\"AWS:AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003\"}`:       `{\"principalId\":\"AWS:<caller>\"}`,
		`{\"ownerIdentity\":{\"ID\":\"` + hex64 + `\"}}`:                                             `{\"ownerIdentity\":{\"ID\":\"<canonical-id>\"}}`,
		`x-amz-grant-read: id="` + hex64 + `"`:                                                       `x-amz-grant-read: id="<canonical-id>"`,
		"X-Forwarded-For: 203.0.113.9":                                                               "X-Forwarded-For: <source-ip>",
		"https://q.s3.amazonaws.com/k?X-Amz-Security-Token=IQoJb3JpZ2lu%2Fx&Signature=abc#frag":      "https://q.s3.amazonaws.com/k?X-Amz-Security-Token=<credential>&Signature=<credential>#frag",
		"https://sqs.us-east-2.amazonaws.com/123456789012":                                           "https://sqs.us-east-2.amazonaws.com/<account>",
		"https://s3.us-east-2.amazonaws.com/1234567890123/k":                                         "https://s3.us-east-2.amazonaws.com/1234567890123/k",
		"Mon, 26 Oct 2026 09:00:00 GMT and 2026-10-26T09:00:00.123456789Z":                           "<http-date> and <iso-time>",
		"arn:aws:sts::123456789012:assumed-role/Admin/00000000-0000-0000-0000-000000000001 and more": "<caller> and more",
		// An assumed role that is not the recording caller's session keeps
		// its name and only loses the account.
		"arn:aws:sts::123456789012:assumed-role/Admin/alice": "arn:aws:sts::<account>:assumed-role/Admin/alice",
		"arn:aws:iam::123456789012:rootless":                 "arn:aws:iam::<account>:rootless",
	}
	s := Exchange()
	for in, want := range cases {
		if got := s.Text(in); got != want {
			t.Errorf("Exchange().Text(%q) = %q; want %q", in, got, want)
		}
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Normalizing normalized text changes nothing: every mask is "<name>", which
// no rule of either set matches.
func TestTextIsIdempotent(t *testing.T) {
	t.Parallel()
	pieces := []string{
		"arn:aws:sqs:us-east-2:123456789012:q", "https://sqs.us-east-2.amazonaws.com/123456789012/q",
		"2026-10-14T15:21:12Z", "Wed, 14 Oct 2026 06:40:28 GMT", "arn:aws:iam::123456789012:root",
		"AROARZPUZDIKAAAAAAAHA", "AROARZPUZDIKAAAAAAACA:00000000-0000-0000-0000-000000000003",
		"ASIAREDACTED", `"principalId":"AEXAMPLE1234567"`, `"sourceIp":"198.51.100.7"`,
		"<ID>" + hex64 + "</ID>", "?X-Amz-Signature=abc", " ", "/", ":", "x",
		"(Service: AWSKMS; Status Code: 400; Error Code: DisabledException; Request ID: ", "(Service: Kms, Status Code: 400, Request ID: ",
		"720334c4-104a-4513-a125-c8e1ef700cea", "; Proxy: null)",
	}
	rapid.Check(t, func(t *rapid.T) {
		parts := rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 6).Draw(t, "parts")
		in := ""
		var inSb59 strings.Builder
		for _, p := range parts {
			inSb59.WriteString(p)
		}
		in += inSb59.String()
		for name, s := range map[string]Set{"exchange": Exchange(), "snapshot": Snapshot()} {
			once := s.Text(in)
			if twice := s.Text(once); twice != once {
				t.Fatalf("%s: Text(%q) = %q, but Text of that = %q", name, in, once, twice)
			}
		}
	})
}

// Longest Old first is over the whole text, not per position: a shorter Old
// that overlaps a longer one's occurrence loses to it even when it starts
// earlier.
func TestSubstituteLongestOldFirstOverTheWholeText(t *testing.T) {
	t.Parallel()
	rs := []Replacement{{Old: "ab", New: "X"}, {Old: "bcd", New: "Y"}}
	if got, want := Substitute(rs, "abcd"), "aY"; got != want {
		t.Errorf("Substitute(%v, %q) = %q; want %q", rs, "abcd", got, want)
	}
	if got, want := Substitute([]Replacement{{Old: "", New: "X"}}, "ab"), "ab"; got != want {
		t.Errorf("Substitute with an empty Old = %q; want %q, unchanged", got, want)
	}
}

// Names returns a copy: changing it leaves the set alone.
func TestSetNamesIsACopy(t *testing.T) {
	t.Parallel()
	s := Exchange()
	ns := s.Names()
	ns[0] = Credential
	if got := s.Names()[0]; got != Bound {
		t.Errorf("Exchange().Names()[0] = %q after changing a returned slice; want %q", got, Bound)
	}
}
