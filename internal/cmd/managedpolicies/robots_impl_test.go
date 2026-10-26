package main

import (
	"testing"
	"time"
)

// A record before the first user-agent line belongs to no group (RFC 9309
// 2.1), and a Crawl-delay that does not fit a time.Duration is ignored like
// any other invalid value.
func TestRobotsEdges(t *testing.T) {
	r := parseRobots([]byte("Disallow: /\nCrawl-delay: 9\nUser-agent: *\nDisallow: /b\n"))
	if !r.allowed(indexPath) || r.allowed("/b") {
		t.Errorf("a rule before any group applied: allowed(%s) = %v, allowed(/b) = %v", indexPath, r.allowed(indexPath), r.allowed("/b"))
	}
	if d, ok := r.crawlDelay(); ok {
		t.Errorf("crawlDelay = %v from a line before any group, want none", d)
	}
	if d, ok := parseRobots([]byte("User-agent: *\nCrawl-delay: 99999999999999\nCrawl-delay: 2\n")).crawlDelay(); !ok || d != 2*time.Second {
		t.Errorf("crawlDelay = %v, %v; want the first value that fits, 2s", d, ok)
	}
}

// "*" and "$" combine: a middle "*" part is placed as early as it goes, and
// an anchored last part must end the path.
func TestRobotsPatternParts(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		match         bool
	}{
		{"/a*b*c$", "/axbxc", true},
		{"/a*b*c$", "/axbxcx", false},
		{"/a*b*c$", "/abcbc", true},
		{"/a*b*c", "/abxc/d", true},
		{"/a$", "/a", true},
		{"/a$", "/ab", false},
		{"*", "/anything", true},
	} {
		if got := patternMatches(c.pattern, c.path); got != c.match {
			t.Errorf("patternMatches(%q, %q) = %v, want %v", c.pattern, c.path, got, c.match)
		}
	}
}
