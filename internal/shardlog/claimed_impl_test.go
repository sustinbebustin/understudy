package shardlog_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// ClaimedStream reads the stream a token names from its prefix, the form
// "<stream ARN>|1|<token>" (dynamodb-streams.md 4.4), without the key:
// what IAM authorizes a GetRecords call against before the handler checks
// the token. It agrees with Decode on every token Encode issues, names the
// prefix's stream even when the rest does not verify, and reports false for
// a token without a parseable prefix.
func TestClaimedStreamReadsThePrefix(t *testing.T) {
	t.Parallel()
	c := shardlog.NewCodec(testKey, shardlog.ARNPrefixed{})
	rapid.Check(t, func(t *rapid.T) {
		it := iteratorGen().Draw(t, "it")
		b := c.Encode(it)
		got, ok := shardlog.ClaimedStream(b)
		if !ok || got != it.Stream {
			t.Fatalf("ClaimedStream(%q) = %v, %v; want %v", b, got, ok, it.Stream)
		}
		forged := append([]byte(it.Stream.String()+"|1|"), "bm90LWEtdG9rZW4="...)
		if got, ok := shardlog.ClaimedStream(forged); !ok || got != it.Stream {
			t.Fatalf("ClaimedStream(%q) = %v, %v; want the prefix's %v, unverified", forged, got, ok, it.Stream)
		}
	})
	for _, token := range []string{"", "no-prefix", "not-an-arn|1|AAAA"} {
		if got, ok := shardlog.ClaimedStream([]byte(token)); ok {
			t.Errorf("ClaimedStream(%q) = %v, true; want false", token, got)
		}
	}
}
