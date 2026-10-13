package sqs

import (
	"regexp"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// A queue URL understudy mints reads back as its account and name, on any
// host, with or without a trailing slash.
func TestQueueURLRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.StringMatching(`[a-zA-Z0-9_-]{1,80}`).Draw(rt, "name")
		account, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(rt, "account"))
		if err != nil {
			rt.Fatal(err)
		}
		endpoint := kernel.Endpoint{
			Scheme: rapid.SampledFrom([]string{"http", "https"}).Draw(rt, "scheme"),
			Host:   rapid.SampledFrom([]string{"localhost:4566", "127.0.0.1:1", "sqs.us-east-1.localhost", "[::1]:4566"}).Draw(rt, "host"),
		}
		raw := formatQueueURL(endpoint, account, name) + rapid.SampledFrom([]string{"", "/"}).Draw(rt, "slash")
		gotAccount, gotName, ok := parseQueueURL(raw)
		if !ok || gotAccount != account || gotName != name {
			rt.Fatalf("parseQueueURL(%q) = %s, %q, %v; want %s, %q", raw, gotAccount, gotName, ok, account, name)
		}
	})
}

// isQueueName takes exactly the names CreateQueue's pattern and 80-character
// limit admit.
func TestIsQueueNameMatchesThePattern(t *testing.T) {
	pattern := regexp.MustCompile(`^` + queueNamePattern + `$`)
	rapid.Check(t, func(rt *rapid.T) {
		s := rapid.OneOf(
			rapid.StringMatching(`[a-zA-Z0-9_-]{0,82}(\.fifo)?`),
			rapid.StringMatching(`[a-zA-Z0-9_./: -]{0,12}`),
			rapid.String(),
		).Draw(rt, "s")
		want := len(s) <= 80 && pattern.MatchString(s)
		if got := isQueueName(s); got != want {
			rt.Fatalf("isQueueName(%q) = %v, want %v", s, got, want)
		}
	})
}

func TestParseQueueURLRefusesOtherPaths(t *testing.T) {
	for _, raw := range []string{
		"", "http://localhost/", "http://localhost/000000000000", "http://localhost/000000000000/",
		"http://localhost/0000/q", "http://localhost/000000000000/a/b", "http://localhost/acct/q", "%zz",
	} {
		if _, _, ok := parseQueueURL(raw); ok {
			t.Errorf("parseQueueURL(%q) accepted a URL that names no queue", raw)
		}
	}
}
