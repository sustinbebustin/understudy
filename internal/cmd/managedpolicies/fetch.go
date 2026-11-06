package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
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

// docsFetcher reads docs.aws.amazon.com with a bounded client, waiting on
// the real clock.
func docsFetcher() fetcher {
	return fetcher{
		client: &http.Client{Timeout: 2 * time.Minute},
		base:   docsBase,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		today: func() string { return time.Now().UTC().Format(time.DateOnly) },
	}
}

// fetch reads robots.txt, llms.txt and the single-file guide, one request
// each, waiting the robots.txt Crawl-delay before each request after the
// first (iam-evaluation.md 10.1 to 10.4). robots.txt must allow each path
// for productToken before it is requested. A failure before writing (a
// request, a parse, the catalogue's rules) ends the run with dir untouched.
// Then fetch writes the catalogue into dir and returns verify's verdict on
// it: a failure part way through writing leaves some files replaced beside
// the previous SOURCE.json, and a verify failure leaves every file written,
// so either way -verify reports what is wrong. No per-policy page is
// requested: one single-file guide holds them all.
func (f fetcher) fetch(ctx context.Context, dir string) error {
	robotsBody, robotsGot, err := f.get(ctx, robotsPath)
	if err != nil {
		return err
	}
	rules := parseRobots(robotsBody)
	if !rules.allowed(indexPath) {
		return &RobotsError{Path: indexPath}
	}
	delay, ok := rules.crawlDelay()
	if !ok {
		delay = defaultCrawlDelay
	}
	if err := f.wait(ctx, delay, indexPath); err != nil {
		return err
	}
	indexBody, indexGot, err := f.get(ctx, indexPath)
	if err != nil {
		return err
	}
	idx, err := parseIndex(indexBody)
	if err != nil {
		return fmt.Errorf("read %s: %w; nothing was written", docsBase+indexPath, err)
	}
	// parseIndex holds the link to the guide's own prefix, on docsBase.
	fullPath := strings.TrimPrefix(idx.full, docsBase)
	if !rules.allowed(fullPath) {
		return &RobotsError{Path: fullPath}
	}
	if err := f.wait(ctx, delay, fullPath); err != nil {
		return err
	}
	fullBody, fullGot, err := f.get(ctx, fullPath)
	if err != nil {
		return err
	}
	policies, err := parseFull(fullBody, idx)
	if err != nil {
		return fmt.Errorf("read %s: %w; nothing was written", idx.full, err)
	}
	cat, err := policycatalogue.New(policies)
	if err != nil {
		return fmt.Errorf("the guide's policies do not form a catalogue: %w; nothing was written", err)
	}
	src := Source{
		Retrieved:         f.today(),
		UserAgent:         userAgent,
		CrawlDelaySeconds: delay.Seconds(),
		Fetched:           []Fetched{robotsGot, indexGot, fullGot},
	}
	pages := make(map[string]string, len(idx.policies))
	for _, e := range idx.policies {
		pages[e.name] = e.page
	}
	for p := range cat.All() {
		src.Policies = append(src.Policies, PolicySource{ARN: p.ARN, Page: pages[p.Name], Anchor: p.Name, DocumentSHA256: sha256Hex([]byte(p.Document))})
	}
	if err := write(dir, cat, src); err != nil {
		return err
	}
	return verify(os.DirFS(dir), dir)
}

// wait waits d before the request for path, as robots.txt's Crawl-delay
// asks.
func (f fetcher) wait(ctx context.Context, d time.Duration, path string) error {
	if err := f.sleep(ctx, d); err != nil {
		return fmt.Errorf("wait %v before requesting %s: %w; nothing was written", d, path, err)
	}
	return nil
}

// get requests path from f.base with the generator's User-Agent and returns
// the body and the record of the request, whose URL is the public one.
func (f fetcher) get(ctx context.Context, path string) ([]byte, Fetched, error) {
	url := f.base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, Fetched{}, fmt.Errorf("GET %s: %w", url, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, Fetched{}, fmt.Errorf("GET %s: %w; nothing was written", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, Fetched{}, fmt.Errorf("GET %s: status %s, want 200 OK; nothing was written", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, Fetched{}, fmt.Errorf("GET %s: read the body: %w; nothing was written", url, err)
	}
	if len(body) > maxDownload {
		return nil, Fetched{}, fmt.Errorf("GET %s: the body exceeds %d bytes; nothing was written", url, maxDownload)
	}
	return body, Fetched{URL: docsBase + path, LastModified: resp.Header.Get("Last-Modified"), Bytes: len(body), SHA256: sha256Hex(body)}, nil
}

// RobotsError is a path robots.txt does not allow the generator to fetch.
type RobotsError struct{ Path string }

func (e *RobotsError) Error() string {
	return "robots.txt disallows " + e.Path + " for " + productToken + "; nothing further was fetched and nothing was written"
}
