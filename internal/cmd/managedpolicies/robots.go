package main

import "time"

// robots is the part of a robots.txt that applies to productToken (RFC
// 9309): its allow and disallow rules and its Crawl-delay. Not implemented
// yet: it holds nothing.
type robots struct{}

// parseRobots reads a robots.txt; it never fails. Not implemented yet: it
// reads nothing.
func parseRobots([]byte) robots { return robots{} }

// allowed reports whether the rules let productToken fetch path. Not
// implemented yet: it allows nothing.
func (robots) allowed(string) bool { return false }

// crawlDelay is the applicable Crawl-delay, and false when robots.txt
// states none. Not implemented yet: it states none.
func (robots) crawlDelay() (time.Duration, bool) { return 0, false }
