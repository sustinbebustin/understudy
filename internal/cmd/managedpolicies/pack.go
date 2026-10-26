package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	awsmanagedpolicies "github.com/sustinbebustin/understudy/third_party/aws-managed-policies"
)

// dataFile is one file the generator writes, with its content.
type dataFile struct {
	name    string
	content []byte
}

// derivedFiles are the data files that follow from policies.json and the
// generator's code alone, so -pack rewrites them offline: LICENSE, from
// licenseText, and policies.json.gz, policies.json compressed. write writes
// the same two, so -fetch and -pack never disagree on them.
func derivedFiles(policies []byte) ([]dataFile, error) {
	compressed, err := gzipPolicies(policies)
	if err != nil {
		return nil, err
	}
	return []dataFile{
		{awsmanagedpolicies.LicenseFile, []byte(licenseText)},
		{awsmanagedpolicies.PoliciesGzipFile, compressed},
	}, nil
}

// gzipOSUnknown is the OS byte compress/gzip writes by default, 255
// (unknown, RFC 1952 2.3.1), and so the one policies.json.gz carries.
const gzipOSUnknown = 255

// gzipPolicies is policies.json compressed as the binary embeds it,
// policies.json.gz: compress/gzip at BestCompression with its default
// Header (no name, comment, extra field or modification time, and OS
// gzipOSUnknown), so one policies.json and Go release always give the same
// bytes.
func gzipPolicies(policies []byte) ([]byte, error) {
	var b bytes.Buffer
	zw, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("compress %s: %w", awsmanagedpolicies.PoliciesFile, err)
	}
	_, werr := zw.Write(policies)
	if err := errors.Join(werr, zw.Close()); err != nil {
		return nil, fmt.Errorf("compress %s: %w", awsmanagedpolicies.PoliciesFile, err)
	}
	return b.Bytes(), nil
}

// pack is the -pack mode: it rewrites the derived files in dir from the
// policies.json there, each atomically and offline, and returns verify's
// verdict on the directory. policies.json is compressed as it is, so
// -verify still reports any problem it has.
func pack(dir string) error {
	policies, err := os.ReadFile(filepath.Join(dir, awsmanagedpolicies.PoliciesFile))
	if err != nil {
		return fmt.Errorf("-pack compresses %s, which cannot be read: %w; nothing was written", awsmanagedpolicies.PoliciesFile, err)
	}
	files, err := derivedFiles(policies)
	if err != nil {
		return fmt.Errorf("%w; nothing was written", err)
	}
	if err := writeFiles(dir, files); err != nil {
		return fmt.Errorf("%w; that file is unchanged", err)
	}
	return verify(os.DirFS(dir), dir)
}
