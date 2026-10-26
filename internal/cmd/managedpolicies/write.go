package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

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

// licenseText is LICENSE: MIT-0 for the policy documents, the licence AWS
// grants for code in its documentation, with the copyright line of
// https://github.com/aws/mit-0, and CC BY-SA 4.0 for the fields derived
// from the documentation (ADR 0016, amendment "managed policies").
const licenseText = `The data files in this directory (policies.json, policies.json.gz,
SOURCE.json and NOTICE) hold the AWS managed policies as the AWS Managed
Policy Reference Guide publishes them, under two licences. They are
licensed separately from the rest of the repository, which is Apache-2.0;
embed.go is part of that code. policies.json.gz is policies.json
compressed with gzip, the copy the binary embeds; everything below that
applies to policies.json applies to it too.

1. The policy documents, the "document" member of each policy in
   policies.json, are code in AWS documentation, licensed under the MIT No
   Attribution licence:

MIT No Attribution

Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.

Permission is hereby granted, free of charge, to any person obtaining a copy of this
software and associated documentation files (the "Software"), to deal in the Software
without restriction, including without limitation the rights to use, copy, modify,
merge, publish, distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED,
INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A
PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

2. Every other member of each policy in policies.json (policyName, arn,
   path, type, description, defaultVersionId, createDate and updateDate)
   is AWS documentation, licensed under the Creative Commons
   Attribution-ShareAlike 4.0 International licence (CC BY-SA 4.0):

   https://creativecommons.org/licenses/by-sa/4.0/legalcode

   NOTICE carries the attribution that licence requires.
`

// encodeSource writes src as canonical SOURCE.json: two-space indent,
// members in declaration order, one trailing newline, as policies.json.
func encodeSource(src Source) ([]byte, error) {
	b, err := json.Marshal(src, jsontext.WithIndent("  "))
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", awsmanagedpolicies.SourceFile, err)
	}
	return append(b, '\n'), nil
}

// notice is NOTICE for src: the CC BY-SA 4.0 attribution ADR 0016's
// amendment "managed policies" lists, the source and its URLs, the
// retrieval date, the copyright holder, the licence and that the data was
// reformatted with no other change. It depends on src alone.
func notice(src Source) []byte {
	full := guidePrefix + "full.md"
	if len(src.Fetched) == 3 {
		full = src.Fetched[2].URL
	}
	return fmt.Appendf(nil, `AWS managed policies, from the AWS Managed Policy Reference Guide
Copyright Amazon Web Services, Inc. or its affiliates. All rights reserved.

The policies in policies.json are the AWS Managed Policy Reference Guide's,
by Amazon Web Services, Inc. or its affiliates, licensed under CC BY-SA 4.0
(https://creativecommons.org/licenses/by-sa/4.0/legalcode), each policy
document under MIT No Attribution; see LICENSE. The material is provided
as is, without warranties of any kind (section 5 of CC BY-SA 4.0).

Source: %s
Index: %s
Retrieved: %s

internal/cmd/managedpolicies read each policy's fields from the guide and
reformatted to JSON with no other changes: each document is byte for byte
as published, and SOURCE.json records the sha256 of each and the request
that read the guide.
`, full, docsBase+indexPath, src.Retrieved)
}

// write writes NOTICE, policies.json, the derived files (LICENSE and
// policies.json.gz, as -pack writes them) and, last, SOURCE.json into dir,
// each atomically, so a run that fails part way leaves the previous
// SOURCE.json and -verify reports the mismatch. Nothing is written when cat
// or src does not encode.
func write(dir string, cat *policycatalogue.Catalogue, src Source) error {
	policies, err := cat.Encode()
	if err != nil {
		return err
	}
	derived, err := derivedFiles(policies)
	if err != nil {
		return err
	}
	source, err := encodeSource(src)
	if err != nil {
		return err
	}
	return writeFiles(dir, slices.Concat(
		[]dataFile{{awsmanagedpolicies.NoticeFile, notice(src)}, {awsmanagedpolicies.PoliciesFile, policies}},
		derived,
		[]dataFile{{awsmanagedpolicies.SourceFile, source}},
	))
}

// writeFiles writes each file into dir atomically, in order, and stops at
// the first that fails.
func writeFiles(dir string, files []dataFile) error {
	for _, f := range files {
		if err := writeFileAtomic(filepath.Join(dir, f.name), f.content); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	return nil
}

// writeFileAtomic writes content to a temp file beside name and renames it
// into place, so an interrupted run never leaves a half-written file. It is
// internal/cmd/vendor's, which another main package cannot import.
func writeFileAtomic(name string, content []byte) error {
	dir, base := filepath.Dir(name), filepath.Base(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+base+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// CreateTemp makes the file 0600; the data files are ordinary repo
	// files that other users (a Docker build context) must read.
	_, werr := tmp.Write(content)
	merr := tmp.Chmod(0o644)
	cerr := tmp.Close()
	if err := errors.Join(werr, merr, cerr); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, name); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	return nil
}
