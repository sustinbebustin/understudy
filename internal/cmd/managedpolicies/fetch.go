package main

import (
	"context"
	"net/http"
	"time"
)

// Where and how the generator reads the guide (iam-evaluation.md 10.1 to
// 10.4).
const (
	docsBase     = "https://docs.aws.amazon.com"
	guidePrefix  = "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/"
	robotsPath   = "/robots.txt"
	indexPath    = "/aws-managed-policy/latest/reference/llms.txt"
	productToken = "understudy-managedpolicies"
	userAgent    = "understudy-managedpolicies/1 (+https://github.com/sustinbebustin/understudy)"
	// defaultCrawlDelay is the wait when robots.txt states none: the
	// Crawl-delay iam-evaluation.md 10.1 recorded.
	defaultCrawlDelay = 5 * time.Second
	// maxDownload caps one body; the single-file guide is about 8 MB
	// (iam-evaluation.md 10.4).
	maxDownload = 64 << 20
)

// fetcher reads the guide. base and sleep are fields so tests can serve it
// from httptest and record the waits.
type fetcher struct {
	client *http.Client
	// base is the scheme and host every request goes to.
	base string
	// sleep waits d, or returns ctx's error.
	sleep func(ctx context.Context, d time.Duration) error
	// today returns the UTC retrieval date, YYYY-MM-DD.
	today func() string
}

// docsFetcher reads docs.aws.amazon.com. Not implemented yet: it is the zero
// fetcher.
func docsFetcher() fetcher { return fetcher{} }

// fetch reads robots.txt, llms.txt and the single-file guide, one request
// each, writes the catalogue into dir and verifies it. Not implemented yet: it refuses.
func (fetcher) fetch(context.Context, string) error {
	return &notImplementedError{op: "fetching the guide"}
}

// RobotsError is a path robots.txt does not allow the generator to fetch.
type RobotsError struct{ Path string }

func (e *RobotsError) Error() string {
	return "robots.txt disallows " + e.Path + " for " + productToken + "; nothing further was fetched and nothing was written"
}
