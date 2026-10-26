package main

import "github.com/sustinbebustin/understudy/internal/policycatalogue"

// Source is SOURCE.json: when and how the catalogue was retrieved, and where
// each policy came from.
type Source struct {
	// Retrieved is the UTC date of the run, YYYY-MM-DD.
	Retrieved string `json:"retrieved"`
	UserAgent string `json:"userAgent"`
	// CrawlDelaySeconds is the wait between requests.
	CrawlDelaySeconds float64 `json:"crawlDelaySeconds"`
	// Fetched are robots.txt, llms.txt and the single-file guide, in
	// request order.
	Fetched []Fetched `json:"fetched"`
	// Policies has one entry per policies.json entry, in the same order.
	Policies []PolicySource `json:"policies"`
}

// Fetched is one request of the run.
type Fetched struct {
	// URL is the public URL, also when a test serves it.
	URL string `json:"url"`
	// LastModified is the Last-Modified header verbatim, "" when absent.
	LastModified string `json:"lastModified"`
	Bytes        int    `json:"bytes"`
	// SHA256 is the lowercase hex sha256 of the body as received.
	SHA256 string `json:"sha256"`
}

// PolicySource is where one policy came from.
type PolicySource struct {
	ARN string `json:"arn"`
	// Page is the policy's page as llms.txt links it.
	Page string `json:"page"`
	// Anchor is the <a name> that opens the policy's section in the
	// single-file guide.
	Anchor string `json:"anchor"`
	// DocumentSHA256 is the lowercase hex sha256 of the document as
	// published.
	DocumentSHA256 string `json:"documentSha256"`
}

// licenseText is LICENSE: MIT-0 for the policy documents, CC BY-SA 4.0 for
// the fields derived from the documentation (ADR 0016). Not implemented
// yet: it is empty.
const licenseText = ""

// encodeSource writes src as canonical SOURCE.json. Not implemented yet: it
// refuses.
func encodeSource(Source) ([]byte, error) {
	return nil, &notImplementedError{op: "encoding SOURCE.json"}
}

// notice is NOTICE for src: the attribution ADR 0016 requires. Not
// implemented yet: it is empty.
func notice(Source) []byte { return nil }

// write writes LICENSE, NOTICE, policies.json and, last, SOURCE.json into
// dir, each atomically. Not implemented yet: it refuses.
func write(string, *policycatalogue.Catalogue, Source) error {
	return &notImplementedError{op: "writing the catalogue"}
}
