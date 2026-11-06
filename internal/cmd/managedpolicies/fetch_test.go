package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

const fullPath = "/aws-managed-policy/latest/reference/full.md"

// page is one file the fake guide serves.
type page struct {
	body         string
	lastModified string
	status       int
}

// guideLog records, in order, each request the fake guide served and each
// wait the fetcher asked for, and the User-Agent of every request.
type guideLog struct {
	mu     sync.Mutex
	events []string
	agents []string
}

func (l *guideLog) add(event, agent string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	if agent != "" {
		l.agents = append(l.agents, agent)
	}
}

func (l *guideLog) snapshot() (events, agents []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events), slices.Clone(l.agents)
}

// guidePages are the fixtures served at the guide's paths, with the
// Last-Modified headers iam-evaluation.md 10.2 and 10.4 recorded for
// llms.txt and the single-file guide; robots.txt is sent without one.
func guidePages(t *testing.T) map[string]page {
	t.Helper()
	return map[string]page{
		robotsPath: {body: readFixture(t, "robots.txt")},
		indexPath:  {body: readFixture(t, "llms.txt"), lastModified: "Sun, 25 Oct 2026 17:54:38 GMT"},
		fullPath:   {body: readFixture(t, "full.md"), lastModified: "Sun, 25 Oct 2026 17:54:17 GMT"},
	}
}

// fakeGuide serves pages from httptest, never docs.aws.amazon.com, and
// records requests and waits; sleep, when not nil, answers each wait.
func fakeGuide(t *testing.T, pages map[string]page, sleep func(time.Duration) error) (fetcher, *guideLog) {
	t.Helper()
	log := &guideLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method+" "+r.URL.Path, r.Header.Get("User-Agent"))
		p, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if p.lastModified != "" {
			w.Header().Set("Last-Modified", p.lastModified)
		}
		if p.status != 0 {
			w.WriteHeader(p.status)
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return fetcher{
		client: srv.Client(),
		base:   srv.URL,
		sleep: func(_ context.Context, d time.Duration) error {
			log.add("sleep "+d.String(), "")
			if sleep != nil {
				return sleep(d)
			}
			return nil
		},
		today: func() string { return "2026-10-25" },
	}, log
}

// wantNothingWritten fails the test when dir holds any file.
func wantNothingWritten(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a failed run wrote %v", names)
	}
}

// A run makes three requests, robots.txt, llms.txt and reference/full.md,
// each with the generator's User-Agent and each after the robots.txt
// Crawl-delay, then writes the catalogue and its provenance
// (iam-evaluation.md 10.1 to 10.4).
func TestFetchReadsTheGuideOnceAndWritesTheCatalogue(t *testing.T) {
	pages := guidePages(t)
	f, log := fakeGuide(t, pages, nil)
	dir := t.TempDir()
	if err := f.fetch(t.Context(), dir); err != nil {
		t.Fatalf("fetch = %v", err)
	}
	events, agents := log.snapshot()
	wantEvents := []string{"GET " + robotsPath, "sleep 3s", "GET " + indexPath, "sleep 3s", "GET " + fullPath}
	if !slices.Equal(events, wantEvents) {
		t.Errorf("requests and waits = %q, want %q", events, wantEvents)
	}
	for _, a := range agents {
		if a != userAgent {
			t.Errorf("User-Agent = %q, want %q", a, userAgent)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, awsmanagedpolicies.PoliciesFile))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := policycatalogue.Parse(data)
	if err != nil {
		t.Fatalf("policies.json does not parse: %v", err)
	}
	if diff := cmp.Diff(fixturePolicies(), slices.Collect(cat.All())); diff != "" {
		t.Errorf("policies.json (-fixture +written):\n%s", diff)
	}

	want := Source{Retrieved: "2026-10-25", UserAgent: userAgent, CrawlDelaySeconds: 3}
	for _, fetched := range []struct{ url, path string }{
		{docsBase + robotsPath, robotsPath},
		{docsBase + indexPath, indexPath},
		{"https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md", fullPath},
	} {
		p := pages[fetched.path]
		want.Fetched = append(want.Fetched, Fetched{URL: fetched.url, LastModified: p.lastModified, Bytes: len(p.body), SHA256: hexSHA256([]byte(p.body))})
	}
	for _, p := range fixturePolicies() {
		want.Policies = append(want.Policies, PolicySource{
			ARN: p.ARN, Page: "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/" + p.Name + ".md", Anchor: p.Name, DocumentSHA256: hexSHA256([]byte(p.Document)),
		})
	}
	raw, err := os.ReadFile(filepath.Join(dir, awsmanagedpolicies.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	var got Source
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("SOURCE.json does not decode: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("SOURCE.json (-want +got):\n%s", diff)
	}
	if !bytes.Equal(raw, mustEncodeSource(t, want)) {
		t.Errorf("SOURCE.json is not encodeSource's form:\n%s", raw)
	}
	for name, content := range map[string]string{awsmanagedpolicies.LicenseFile: licenseText, awsmanagedpolicies.NoticeFile: string(notice(want))} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != content {
			t.Errorf("%s = %q, %v; want %q", name, b, err, content)
		}
	}
}

// With no Crawl-delay in robots.txt the run waits the 5 seconds
// docs.aws.amazon.com states (iam-evaluation.md 10.1).
func TestFetchWaitsTheRecordedDelayWhenRobotsStatesNone(t *testing.T) {
	pages := guidePages(t)
	pages[robotsPath] = page{body: "User-agent: *\nDisallow: /forms/\n"}
	f, log := fakeGuide(t, pages, nil)
	dir := t.TempDir()
	if err := f.fetch(t.Context(), dir); err != nil {
		t.Fatalf("fetch = %v", err)
	}
	events, _ := log.snapshot()
	if want := []string{"GET " + robotsPath, "sleep 5s", "GET " + indexPath, "sleep 5s", "GET " + fullPath}; !slices.Equal(events, want) {
		t.Errorf("requests and waits = %q, want %q", events, want)
	}
	if defaultCrawlDelay != 5*time.Second {
		t.Errorf("defaultCrawlDelay = %v, want the recorded 5s", defaultCrawlDelay)
	}
	raw, err := os.ReadFile(filepath.Join(dir, awsmanagedpolicies.SourceFile))
	if err != nil {
		t.Fatal(err)
	}
	var got Source
	if err := json.Unmarshal(raw, &got); err != nil || got.CrawlDelaySeconds != 5 {
		t.Errorf("SOURCE.json crawlDelaySeconds = %v, %v; want 5", got.CrawlDelaySeconds, err)
	}
}

// robots.txt decides before each further request: a path it disallows for
// the generator's token stops the run there, with nothing written.
func TestFetchObeysRobots(t *testing.T) {
	for _, c := range []struct {
		name     string
		robots   string
		path     string
		requests []string
	}{
		{
			name:     "the guide disallowed for every agent",
			robots:   "User-agent: *\nDisallow: /aws-managed-policy/\n",
			path:     indexPath,
			requests: []string{"GET " + robotsPath},
		},
		{
			name:     "the guide disallowed for the generator only",
			robots:   "User-agent: *\nAllow: /\n\nUser-agent: understudy-managedpolicies\nDisallow: /aws-managed-policy/latest/reference/llms.txt\n",
			path:     indexPath,
			requests: []string{"GET " + robotsPath},
		},
		{
			name:     "full.md disallowed",
			robots:   "User-agent: *\nDisallow: /aws-managed-policy/latest/reference/full.md\n",
			path:     fullPath,
			requests: []string{"GET " + robotsPath, "sleep 5s", "GET " + indexPath},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pages := guidePages(t)
			pages[robotsPath] = page{body: c.robots}
			f, log := fakeGuide(t, pages, nil)
			dir := t.TempDir()
			err := f.fetch(t.Context(), dir)
			var re *RobotsError
			if !errors.As(err, &re) || re.Path != c.path {
				t.Fatalf("fetch = %v, want a *RobotsError for %s", err, c.path)
			}
			if msg := re.Error(); !strings.Contains(msg, c.path) || !strings.Contains(msg, productToken) {
				t.Errorf("RobotsError.Error() = %q, want the path and %s named", msg, productToken)
			}
			if events, _ := log.snapshot(); !slices.Equal(events, c.requests) {
				t.Errorf("requests and waits = %q, want %q", events, c.requests)
			}
			wantNothingWritten(t, dir)
		})
	}
}

// Anything but the three expected answers ends the run with nothing
// written: a single-file guide link off the site, a status other than 200, a
// wait cut short, a guide that does not parse, and policies New refuses.
func TestFetchWritesNothingOnFailure(t *testing.T) {
	llms := readFixture(t, "llms.txt")
	full := readFixture(t, "full.md")
	for _, c := range []struct {
		name     string
		edit     func(pages map[string]page)
		sleep    func(time.Duration) error
		check    func(t *testing.T, err error)
		requests []string
	}{
		{
			name: "full.md link outside the guide",
			edit: func(p map[string]page) {
				p[indexPath] = page{body: strings.Replace(llms, "https://docs.aws.amazon.com/aws-managed-policy/latest/reference/full.md", "https://example.com/aws-managed-policy/latest/reference/full.md", 1)}
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if _, ok := errors.AsType[*ParseError](err); !ok {
					t.Errorf("fetch = %v, want a *ParseError", err)
				}
			},
			requests: []string{"GET " + robotsPath, "sleep 3s", "GET " + indexPath},
		},
		{
			name: "full.md not found",
			edit: func(p map[string]page) { p[fullPath] = page{body: "gone", status: http.StatusNotFound} },
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), fullPath) {
					t.Errorf("fetch = %v, want an error naming %s", err, fullPath)
				}
			},
			requests: []string{"GET " + robotsPath, "sleep 3s", "GET " + indexPath, "sleep 3s", "GET " + fullPath},
		},
		{
			name: "robots.txt unavailable",
			edit: func(p map[string]page) { p[robotsPath] = page{body: "busy", status: http.StatusServiceUnavailable} },
			check: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), robotsPath) {
					t.Errorf("fetch = %v, want an error naming %s", err, robotsPath)
				}
			},
			requests: []string{"GET " + robotsPath},
		},
		{
			name:  "wait cancelled",
			sleep: func(time.Duration) error { return context.Canceled },
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, context.Canceled) {
					t.Errorf("fetch = %v, want the wait's context.Canceled", err)
				}
			},
			requests: []string{"GET " + robotsPath, "sleep 3s"},
		},
		{
			name: "a policy that does not parse",
			edit: func(p map[string]page) {
				p[fullPath] = page{body: strings.Replace(full, "+ **Type**: Service role policy", "+ **Type**: Role policy", 1)}
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				var pe *ParseError
				if !errors.As(err, &pe) || pe.Policy != "ExampleQueueReader" {
					t.Errorf("fetch = %v, want ExampleQueueReader's *ParseError", err)
				}
			},
			requests: []string{"GET " + robotsPath, "sleep 3s", "GET " + indexPath, "sleep 3s", "GET " + fullPath},
		},
		{
			name: "names equal regardless of case",
			edit: func(p map[string]page) {
				p[indexPath] = page{body: strings.ReplaceAll(llms, "ExampleQueueReader", "EXAMPLEADMINACCESS")}
				p[fullPath] = page{body: strings.ReplaceAll(full, "ExampleQueueReader", "EXAMPLEADMINACCESS")}
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				// The index or the catalogue may be the one to refuse it.
				var ie *policycatalogue.InvalidError
				var pe *ParseError
				if (!errors.As(err, &ie) || ie.Field != policycatalogue.FieldPolicyName) && !errors.As(err, &pe) {
					t.Errorf("fetch = %v, want policycatalogue's policyName *InvalidError or a *ParseError", err)
				}
			},
			requests: []string{"GET " + robotsPath, "sleep 3s", "GET " + indexPath, "sleep 3s", "GET " + fullPath},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pages := guidePages(t)
			if c.edit != nil {
				c.edit(pages)
			}
			f, log := fakeGuide(t, pages, c.sleep)
			dir := t.TempDir()
			c.check(t, f.fetch(t.Context(), dir))
			if events, _ := log.snapshot(); !slices.Equal(events, c.requests) {
				t.Errorf("requests and waits = %q, want %q", events, c.requests)
			}
			wantNothingWritten(t, dir)
		})
	}
}
