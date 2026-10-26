package main

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// robots is the part of a robots.txt that applies to productToken (RFC
// 9309, https://www.rfc-editor.org/rfc/rfc9309): its allow and disallow
// rules and its Crawl-delay values, in file order.
type robots struct {
	rules []robotsRule
	// delays are the applicable Crawl-delay values as written.
	delays []string
}

// robotsRule is one allow or disallow line of an applicable group.
type robotsRule struct {
	allow   bool
	pattern string
}

// robotsGroup is one group: its user-agent lines and the records under
// them.
type robotsGroup struct {
	agents []string
	rules  []robotsRule
	delays []string
}

// parseRobots reads a robots.txt; it never fails. Lines are "key: value",
// keys regardless of case, "#" starting a comment; a group is one or more
// user-agent lines and the records after them (RFC 9309 2.1, 2.2). The
// groups naming productToken apply, regardless of case; when none does,
// the "*" groups; when there are none, no rule (2.2.1). Records other than
// allow, disallow and the non-standard crawl-delay are ignored (2.2.4).
func parseRobots(body []byte) robots {
	groups := robotsGroups(body)
	for _, agent := range []string{productToken, "*"} {
		var r robots
		found := false
		for _, g := range groups {
			if slices.ContainsFunc(g.agents, func(a string) bool { return strings.EqualFold(a, agent) }) {
				found = true
				r.rules = append(r.rules, g.rules...)
				r.delays = append(r.delays, g.delays...)
			}
		}
		if found {
			return r
		}
	}
	return robots{}
}

// robotsGroups splits a robots.txt into its groups, in file order. A
// record before the first user-agent line belongs to no group.
func robotsGroups(body []byte) []*robotsGroup {
	var groups []*robotsGroup
	var cur *robotsGroup
	inAgents := false
	for line := range strings.SplitSeq(string(body), "\n") {
		line, _, _ = strings.Cut(line, "#")
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if !inAgents {
				cur = &robotsGroup{}
				groups = append(groups, cur)
				inAgents = true
			}
			cur.agents = append(cur.agents, value)
		case "allow", "disallow":
			inAgents = false
			if cur != nil {
				cur.rules = append(cur.rules, robotsRule{allow: key == "allow", pattern: value})
			}
		case "crawl-delay":
			inAgents = false
			if cur != nil {
				cur.delays = append(cur.delays, value)
			}
		}
	}
	return groups
}

// allowed reports whether the rules let productToken fetch path: the
// longest matching pattern, in bytes, decides, allow winning a tie, and a
// path no rule matches is allowed (RFC 9309 2.2.2).
func (r robots) allowed(path string) bool {
	verdict, longest := true, -1
	for _, rule := range r.rules {
		if !patternMatches(rule.pattern, path) {
			continue
		}
		if n := len(rule.pattern); n > longest || (n == longest && rule.allow) {
			verdict, longest = rule.allow, n
		}
	}
	return verdict
}

// patternMatches reports whether a rule's pattern matches path from its
// start: "*" matches any run of bytes and a final "$" anchors the end (RFC
// 9309 2.2.3); paths compare case-sensitively. An empty pattern matches
// nothing (2.2.2).
func patternMatches(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	rest := path[len(parts[0]):]
	if len(parts) == 1 {
		return !anchored || rest == ""
	}
	middle, last := parts[1:len(parts)-1], parts[len(parts)-1]
	for _, p := range middle {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	// Placing each middle part as early as it goes leaves the most room
	// for the last one.
	if anchored {
		return strings.HasSuffix(rest, last)
	}
	return strings.Contains(rest, last)
}

// crawlDelayRE is a Crawl-delay value understood as seconds: an integer or
// a decimal.
var crawlDelayRE = regexp.MustCompile(`^\d+(\.\d+)?$`)

// crawlDelay is the first valid applicable Crawl-delay, and false when
// robots.txt states none. Crawl-delay is no RFC 9309 record; a value that
// is not seconds, or does not fit a time.Duration, is ignored.
func (r robots) crawlDelay() (time.Duration, bool) {
	for _, v := range r.delays {
		if !crawlDelayRE.MatchString(v) {
			continue
		}
		secs, err := strconv.ParseFloat(v, 64)
		if err != nil || secs*float64(time.Second) > math.MaxInt64 {
			continue
		}
		return time.Duration(secs * float64(time.Second)), true
	}
	return 0, false
}
