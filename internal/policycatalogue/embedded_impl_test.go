package policycatalogue_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// dataDir is the data package's directory, relative to this package's.
const dataDir = "../../third_party/aws-managed-policies"

// The binary embeds the data directory whole except policies.json, which
// it carries compressed as policies.json.gz (ADR 0016, amendment "managed
// policies"): every other generator output beside embed.go is in FS, and FS
// holds nothing else. A data commit that adds a file but leaves it off the
// embed directive of embed.go, or embeds the uncompressed policies.json,
// fails here.
func TestEmbeddedFilesAreTheDataDirectory(t *testing.T) {
	dataFiles := []string{awsmanagedpolicies.PoliciesFile, awsmanagedpolicies.PoliciesGzipFile, awsmanagedpolicies.SourceFile, awsmanagedpolicies.LicenseFile, awsmanagedpolicies.NoticeFile}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read the data directory: %v", err)
	}
	var onDisk []string
	for _, e := range entries {
		switch name := e.Name(); {
		case name == "embed.go":
		case e.IsDir() || !slices.Contains(dataFiles, name):
			t.Errorf("%s holds %s, which is neither embed.go nor a file the generator writes %v", dataDir, name, dataFiles)
		default:
			onDisk = append(onDisk, name)
		}
	}
	for _, name := range []string{awsmanagedpolicies.PoliciesFile, awsmanagedpolicies.PoliciesGzipFile} {
		if !slices.Contains(onDisk, name) {
			t.Errorf("%s has no %s, which the generator writes and -verify checks; restore it with git checkout", dataDir, name)
		}
	}

	var embedded []string
	err = fs.WalkDir(awsmanagedpolicies.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			embedded = append(embedded, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk FS: %v", err)
	}
	want := slices.DeleteFunc(slices.Clone(onDisk), func(name string) bool { return name == awsmanagedpolicies.PoliciesFile })
	slices.Sort(want)
	slices.Sort(embedded)
	if !slices.Equal(embedded, want) {
		t.Errorf("FS holds %v, want %v: every data file but %s, which the binary carries as %s; fix the go:embed line of embed.go", embedded, want, awsmanagedpolicies.PoliciesFile, awsmanagedpolicies.PoliciesGzipFile)
	}
}

// The first Embedded call decompresses the catalogue into one buffer sized
// from the gzip trailer, so it allocates the text once rather than growing
// a buffer through a series of copies, each garbage the moment the next is
// made (ARCHITECTURE.md 5.7, the RSS budgets): one decompression allocates
// the text's size and a little more for the readers, not the several times
// over that io.ReadAll's growth costs. Not parallel: TotalAlloc is
// process-wide.
func TestEmbeddedDecompressesIntoOneBuffer(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join(dataDir, awsmanagedpolicies.PoliciesFile))
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	data, err := policycatalogue.GunzipEmbedded()
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("decompress the embedded catalogue: %v", err)
	}
	if !bytes.Equal(data, onDisk) {
		t.Fatalf("the embedded %s decompresses to %d bytes, not the %d of %s", awsmanagedpolicies.PoliciesGzipFile, len(data), len(onDisk), awsmanagedpolicies.PoliciesFile)
	}
	if grew, bound := after.TotalAlloc-before.TotalAlloc, uint64(len(data)+len(data)/4); grew >= bound {
		t.Errorf("decompressing %d bytes allocated %d bytes, want under %d: the buffer is regrown", len(data), grew, bound)
	}
}
