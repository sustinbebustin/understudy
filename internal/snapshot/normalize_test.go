package snapshot

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

func TestNormalizeTextMasksTheNamedValues(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"AQEBzZ0aAbC1dEf2gHi3jKl4mNo5pQr6sTu7":                                                  "<receipt-handle>",
		"AQEBshort":                                                                             "AQEBshort",
		"arn:aws:sqs:us-east-1:123456789012:orders":                                             "arn:aws:sqs:us-east-1:<account>:orders",
		"arn:aws:s3:::bucket/123456789012":                                                      "arn:aws:s3:::bucket/123456789012",
		"arn:aws:iam::123456789012:role/r":                                                      "arn:aws:iam::<account>:role/r",
		"learn:aws:sqs:us-east-1:123456789012:x":                                                "learn:aws:sqs:us-east-1:123456789012:x",
		"http://sqs.us-east-1.localhost:4566/123456789012/orders":                               "http://<host>/<account>/orders",
		"https://127.0.0.1:4566/bucket/key?x=1":                                                 "https://<host>/bucket/key?x=1",
		"see http://a:1/ and https://b:2/1234567890123":                                         "see http://<host>/ and https://<host>/1234567890123",
		"START RequestId: 6f1c2a3b-0000-4000-8000-000000000001 Version: $LATEST":                "START RequestId: <request-id> Version: $LATEST",
		"END RequestId: 6f1c2a3b-0000-4000-8000-000000000001":                                   "END RequestId: <request-id>",
		"REPORT RequestId: x\tDuration: 1.5 ms\tBilled Duration: 2 ms\tInit Duration: 30.25 ms": "REPORT RequestId: <request-id>\tDuration: <duration> ms\tBilled Duration: <duration> ms\tInit Duration: <duration> ms",
		"an order body mentioning 123456789012":                                                 "an order body mentioning 123456789012",
		"(http://a:1)":                                                                          "(http://<host>)",
		"'https://b.example:443', <http://[::1]:80>":                                            "'https://<host>', <http://<host>>",
		"http:// and https://":                                                                  "http:// and https://",
		"123456789012/us-east-1":                                                                "<account>/us-east-1",
		"123456789012/us-east-1/x":                                                              "123456789012/us-east-1/x",
		"123456789012/US-EAST-1":                                                                "123456789012/US-EAST-1",
	}
	for in, want := range cases {
		if got := normalizeText(in); got != want {
			t.Errorf("normalizeText(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNormalizeEntryMasksAccountSegments(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"services/sqs/123456789012/us-east-1.json": "services/sqs/<account>/us-east-1.json",
		"services/iam/123456789012.json":           "services/iam/<account>.json",
		"services/s3/buckets/1234567890123.json":   "services/s3/buckets/1234567890123.json",
		"services/s3/buckets/123456789012.json":    "services/s3/buckets/123456789012.json",
		"services/s3/123456789012x.json":           "services/s3/123456789012x.json",
		"blobs/sha256/123456789012":                "blobs/sha256/123456789012",
		"manifest.json":                            "manifest.json",
	}
	for in, want := range cases {
		if got := normalizeEntry(in); got != want {
			t.Errorf("normalizeEntry(%q) = %q; want %q", in, got, want)
		}
	}
}

// A minted key encodes its account, so it is masked under an AccessKeyId
// member; AWS's documentation key encodes none and is kept, as is a minted
// key under any other name.
func TestNormalizeMasksAccessKeysThatEncodeAnAccount(t *testing.T) {
	t.Parallel()
	key, err := ids.NewSeeded(1).TemporaryAccessKeyID("111111111111")
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseTree([]byte(`{"accessKeyId":"` + string(key) + `","AccessKeyId":"AKIAI44QH8DHBEXAMPLE","body":"` + string(key) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"AccessKeyId":"AKIAI44QH8DHBEXAMPLE","accessKeyId":"<access-key>","body":"` + string(key) + `"}`
	if got := normalize(in, "", plainScope).json(); got != want {
		t.Errorf("normalize = %s; want %s", got, want)
	}
}

// fragment draws text the normalizers look at, mixed with plain text.
func fragment() *rapid.Generator[string] {
	return rapid.OneOf(
		rapid.StringMatching(`AQEB[A-Za-z0-9+/=]{28,40}`),
		rapid.StringMatching(`arn:aws:[a-z0-9]{0,4}:[a-z0-9-]{0,9}:[0-9]{11,13}:[a-z/:]{0,6}`),
		rapid.StringMatching(`https?://[a-z0-9.:<>]{0,12}(/[0-9]{11,13})?[/?#a-z]{0,6}`),
		rapid.StringMatching(`(START|END|REPORT) RequestId: [a-z0-9-]{0,8}( |\t)?`),
		rapid.StringMatching(`(Billed )?Duration: [0-9.]{0,5} ms`),
		rapid.StringMatching(`[a-z :/<>"\t]{0,8}`),
		rapid.StringMatching(`[0-9]{12}`),
		rapid.StringMatching(`(AKIA|ASIA)[A-Z2-7]{16}`),
		rapid.StringMatching(`[0-9]{11,13}/[a-z0-9-]{0,12}`),
	)
}

func TestNormalizeTextIsIdempotent(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := strings.Join(rapid.SliceOfN(fragment(), 0, 6).Draw(t, "fragments"), "")
		once := normalizeText(s)
		if twice := normalizeText(once); twice != once {
			t.Fatalf("normalizeText(%q) = %q, but normalizing that gives %q", s, once, twice)
		}
	})
}

func TestNormalizeTreeIsIdempotent(t *testing.T) {
	t.Parallel()
	names := rapid.SampledFrom([]string{"requestId", "lastRequestId", "eventId", "cause", "parent", "trace", "Expiration", "expiry", "AccountId", "accessKeyId", "enqueuedEventId", "senderId", "body", "url", "tags", "Variables", "metadata"})
	rapid.Check(t, func(t *rapid.T) {
		var gen func(depth int) tree
		gen = func(depth int) tree {
			if depth == 0 || rapid.Bool().Draw(t, "leaf") {
				if rapid.Bool().Draw(t, "number") {
					return tree{kind: jsonx.Number, text: rapid.StringMatching(`[0-9]{1,13}`).Draw(t, "n")}
				}
				return str(rapid.Custom(func(t *rapid.T) string {
					return strings.Join(rapid.SliceOfN(fragment(), 0, 3).Draw(t, "f"), "")
				}).Draw(t, "s"))
			}
			out := tree{kind: jsonx.Object}
			for range rapid.IntRange(0, 4).Draw(t, "members") {
				name := names.Draw(t, "name")
				if rapid.Bool().Draw(t, "fragment name") {
					name = fragment().Draw(t, "member")
				}
				out.members = append(out.members, member{name: name, val: gen(depth - 1)})
			}
			sortMembers(out.members)
			return out
		}
		in := gen(3)
		once := normalize(in, "", plainScope)
		if twice := normalize(once, "", plainScope); twice.json() != once.json() {
			t.Fatalf("normalize(%s) = %s, but normalizing that gives %s", in.json(), once.json(), twice.json())
		}
	})
}
