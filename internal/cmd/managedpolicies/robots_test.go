package main

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// The rules that apply to productToken, and their verdict on paths, follow
// RFC 9309 (https://www.rfc-editor.org/rfc/rfc9309): groups and the
// user-agent match (2.2.1), allow, disallow and the longest match (2.2.2),
// "*" and "$" (2.2.3), and other records ignored (2.2.4).
func TestRobotsAllowed(t *testing.T) {
	for _, c := range []struct {
		name                string
		robots              string
		allowed, disallowed []string
	}{
		{
			name:    "no groups: no rules apply (2.2.1)",
			robots:  "",
			allowed: []string{"/", indexPath},
		},
		{
			name:       "the * group applies when no group names the token (2.2.1)",
			robots:     "User-agent: *\nDisallow: /forms/\n",
			allowed:    []string{indexPath, "/form"},
			disallowed: []string{"/forms/", "/forms/a"},
		},
		{
			name:       "the token's group replaces the * group (2.2.1)",
			robots:     "User-agent: *\nDisallow: /\n\nUser-agent: understudy-managedpolicies\nDisallow: /forms/\n",
			allowed:    []string{indexPath},
			disallowed: []string{"/forms/a"},
		},
		{
			name:       "the token matches regardless of case (2.2.1)",
			robots:     "User-agent: *\nDisallow: /\n\nUser-agent: Understudy-ManagedPolicies\nDisallow: /a\n",
			allowed:    []string{"/b"},
			disallowed: []string{"/a"},
		},
		{
			name:       "the token's groups combine (2.2.1)",
			robots:     "User-agent: understudy-managedpolicies\nDisallow: /a\n\nUser-agent: *\nDisallow: /\n\nUser-agent: understudy-managedpolicies\nDisallow: /b\n",
			allowed:    []string{"/c"},
			disallowed: []string{"/a", "/b"},
		},
		{
			name:       "a group may name several agents (2.1)",
			robots:     "User-agent: other\nUser-agent: understudy-managedpolicies\nDisallow: /a\n\nUser-agent: *\nDisallow: /\n",
			allowed:    []string{"/b"},
			disallowed: []string{"/a"},
		},
		{
			name:    "only another agent's group: no rules apply (2.2.1)",
			robots:  "User-agent: other\nDisallow: /\n",
			allowed: []string{"/", indexPath},
		},
		{
			name:       "the longest match decides (2.2.2)",
			robots:     "User-agent: *\nDisallow: /aws-managed-policy/\nAllow: /aws-managed-policy/latest/\n",
			allowed:    []string{indexPath},
			disallowed: []string{"/aws-managed-policy/v1/x"},
		},
		{
			name:       "a longer disallow beats a shorter allow (2.2.2)",
			robots:     "User-agent: *\nAllow: /a/\nDisallow: /a/b/\n",
			allowed:    []string{"/a/c"},
			disallowed: []string{"/a/b/c"},
		},
		{
			name:    "allow wins a tie (2.2.2)",
			robots:  "User-agent: *\nDisallow: /a\nAllow: /a\n",
			allowed: []string{"/a", "/ab"},
		},
		{
			name:       "* matches any run of characters (2.2.3)",
			robots:     "User-agent: *\nDisallow: /*.txt\n",
			allowed:    []string{"/a.md", "/txt"},
			disallowed: []string{"/a.txt", "/x/y.txt", "/a.txt.bak"},
		},
		{
			name:       "$ anchors the end (2.2.3)",
			robots:     "User-agent: *\nDisallow: /*.txt$\n",
			allowed:    []string{"/a.txt.bak"},
			disallowed: []string{"/a.txt", "/x/y.txt"},
		},
		{
			name:    "an empty disallow matches nothing (2.2.2)",
			robots:  "User-agent: *\nDisallow:\n",
			allowed: []string{"/", "/a"},
		},
		{
			name:       "keys regardless of case, comments and white space (2.2)",
			robots:     "# robots for the fixture\nuser-AGENT: *   # every agent\n  DISALLOW:  /a  # a comment\n# Disallow: /b\n",
			allowed:    []string{"/b"},
			disallowed: []string{"/a", "/a/b"},
		},
		{
			name:       "other records are ignored (2.2.4)",
			robots:     "User-agent: *\nSitemap: https://docs.aws.amazon.com/sitemap_index.xml\nNoindex: /a\nCrawl-delay: 5\nDisallow: /b\n",
			allowed:    []string{"/a"},
			disallowed: []string{"/b"},
		},
		{
			name:       "paths match case-sensitively (2.2.2)",
			robots:     "User-agent: *\nDisallow: /Forms/\n",
			allowed:    []string{"/forms/"},
			disallowed: []string{"/Forms/a"},
		},
		{
			name:       "the recorded layout, CRLF line ends (iam-evaluation.md 10.1)",
			robots:     strings.ReplaceAll(readFixture(t, "robots.txt"), "\n", "\r\n"),
			allowed:    []string{indexPath, "/aws-managed-policy/latest/reference/full.md"},
			disallowed: []string{"/forms/x", "/search/"},
		},
	} {
		r := parseRobots([]byte(c.robots))
		for _, p := range c.allowed {
			if !r.allowed(p) {
				t.Errorf("%s: allowed(%q) = false, want true", c.name, p)
			}
		}
		for _, p := range c.disallowed {
			if r.allowed(p) {
				t.Errorf("%s: allowed(%q) = true, want false", c.name, p)
			}
		}
	}
}

// Crawl-delay is no RFC 9309 record; it is read, in seconds, from the
// groups that apply, the first valid value winning.
func TestRobotsCrawlDelay(t *testing.T) {
	for _, c := range []struct {
		name   string
		robots string
		want   time.Duration
		stated bool
	}{
		{"none", "User-agent: *\nDisallow: /forms/\n", 0, false},
		{"the recorded value (iam-evaluation.md 10.1)", "User-agent: *\nCrawl-delay: 5\n", 5 * time.Second, true},
		{"the fixture", readFixture(t, "robots.txt"), 3 * time.Second, true},
		{"decimal seconds", "User-agent: *\nCrawl-delay: 2.5\n", 2500 * time.Millisecond, true},
		{"key regardless of case", "User-agent: *\ncrawl-DELAY: 4\n", 4 * time.Second, true},
		{"the first valid value", "User-agent: *\nCrawl-delay: soon\nCrawl-delay: 4\nCrawl-delay: 7\n", 4 * time.Second, true},
		{"only invalid values", "User-agent: *\nCrawl-delay: soon\n", 0, false},
		{"the token's group over *", "User-agent: *\nCrawl-delay: 10\n\nUser-agent: understudy-managedpolicies\nCrawl-delay: 1\n", time.Second, true},
		{"another agent's group", "User-agent: other\nCrawl-delay: 9\n", 0, false},
	} {
		got, ok := parseRobots([]byte(c.robots)).crawlDelay()
		if got != c.want || ok != c.stated {
			t.Errorf("%s: crawlDelay = %v, %v; want %v, %v", c.name, got, ok, c.want, c.stated)
		}
	}
}

// For rules of plain path prefixes in the * group, allowed agrees with a
// reference reading of RFC 9309 2.2.2: the longest matching rule decides,
// allow winning a tie, and a path no rule matches is allowed.
func TestRobotsLongestMatchProperty(t *testing.T) {
	type rule struct {
		allow   bool
		pattern string
	}
	rapid.Check(t, func(t *rapid.T) {
		rules := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) rule {
			return rule{rapid.Bool().Draw(t, "allow"), rapid.StringMatching(`/[ab/]{0,4}`).Draw(t, "pattern")}
		}), 0, 6).Draw(t, "rules")
		path := rapid.StringMatching(`/[ab/]{0,6}`).Draw(t, "path")

		var b strings.Builder
		b.WriteString("User-agent: *\n")
		for _, r := range rules {
			if r.allow {
				b.WriteString("Allow: " + r.pattern + "\n")
			} else {
				b.WriteString("Disallow: " + r.pattern + "\n")
			}
		}
		want, longest := true, -1
		for _, r := range rules {
			if !strings.HasPrefix(path, r.pattern) {
				continue
			}
			if n := len(r.pattern); n > longest || (n == longest && r.allow) {
				want, longest = r.allow, n
			}
		}
		if got := parseRobots([]byte(b.String())).allowed(path); got != want {
			t.Fatalf("allowed(%q) under\n%s= %v, want %v", path, b.String(), got, want)
		}
	})
}
