package policycatalogue_test

import (
	"io/fs"
	"os"
	"slices"
	"testing"

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
