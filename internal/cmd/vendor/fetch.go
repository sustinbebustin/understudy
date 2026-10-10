package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

// maxDownload caps one download; the largest model (S3) is about 3 MB.
const maxDownload = 64 << 20

// fetcher downloads from GitHub. The URLs are fields so tests can point them
// at an httptest server.
type fetcher struct {
	client *http.Client
	// treeURL is the git tree API URL of the pinned commit, recursive.
	treeURL string
	// rawURL returns the raw content URL of an upstream path.
	rawURL func(path string) string
	// today returns the UTC retrieval date.
	today func() string
}

func githubFetcher() fetcher {
	return fetcher{
		client:  &http.Client{Timeout: 2 * time.Minute},
		treeURL: "https://api.github.com/repos/" + upstreamOwner + "/git/trees/" + pinnedCommit + "?recursive=1",
		rawURL: func(path string) string {
			return "https://raw.githubusercontent.com/" + upstreamOwner + "/" + pinnedCommit + "/" + path
		},
		today: func() string { return time.Now().UTC().Format(time.DateOnly) },
	}
}

// tree is the part of the git tree API response the fetcher reads.
type tree struct {
	SHA       string      `json:"sha"`
	Truncated bool        `json:"truncated"`
	Tree      []treeEntry `json:"tree"`
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// fetch downloads the pinned files into dir, checks each against the tree's
// blob SHA-1 and writes UPSTREAM.json last, so a failed run leaves the old
// manifest in place and -verify reports the mismatch.
func (f fetcher) fetch(ctx context.Context, dir string) error {
	var t tree
	if err := f.getJSON(ctx, f.treeURL, &t); err != nil {
		return fmt.Errorf("read the tree of %s: %w", pinnedCommit, err)
	}
	if t.Truncated {
		return fmt.Errorf("the tree of %s came back truncated; GitHub's recursive tree listing cannot be trusted to hold every pinned path", pinnedCommit)
	}
	blobs := make(map[string]string, len(t.Tree))
	for _, e := range t.Tree {
		if e.Type == "blob" {
			blobs[e.Path] = e.SHA
		}
	}
	m := Manifest{Repository: upstreamRepo, Commit: pinnedCommit, Retrieved: f.today(), License: upstreamSPDX}
	for _, p := range legalPaths() {
		rec, err := f.download(ctx, dir, "", p, blobs)
		if err != nil {
			return err
		}
		m.Legal = append(m.Legal, rec)
	}
	for _, md := range pinnedModels() {
		rec, err := f.download(ctx, dir, md.Dir, md.Path, blobs)
		if err != nil {
			return err
		}
		m.Files = append(m.Files, rec)
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", manifestName, err)
	}
	return writeFileAtomic(filepath.Join(dir, manifestName), append(out, '\n'))
}

func (f fetcher) download(ctx context.Context, dir, modelDir, path string, blobs map[string]string) (File, error) {
	want, ok := blobs[path]
	if !ok {
		return File{}, fmt.Errorf("%s is not in the tree of %s; the pinned path list in upstream.go is wrong for this commit", path, pinnedCommit)
	}
	content, err := f.get(ctx, f.rawURL(path))
	if err != nil {
		return File{}, fmt.Errorf("download %s: %w", path, err)
	}
	rec := record(modelDir, path, content)
	if rec.GitBlobSHA1 != want {
		return File{}, fmt.Errorf("download %s: git blob SHA-1 is %s, the tree of %s says %s; the download was altered or truncated, nothing was written for it", path, rec.GitBlobSHA1, pinnedCommit, want)
	}
	if err := writeFileAtomic(filepath.Join(dir, filepath.FromSlash(path)), content); err != nil {
		return File{}, fmt.Errorf("write %s: %w", path, err)
	}
	return rec, nil
}

func (f fetcher) getJSON(ctx context.Context, url string, v any) error {
	body, err := f.get(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (f fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("GET %s: body exceeds %d bytes", url, maxDownload)
	}
	return body, nil
}
