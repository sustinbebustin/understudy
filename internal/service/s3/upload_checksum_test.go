package s3

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
)

// TestParseUploadChecksumRefusesUnknownAlgorithm: an algorithm S3 does not
// know is InvalidRequest listing the valid ones in S3's order
// (testdata/parity/s3/R08/08-create-alg-foo.json). Over the wire the
// generated enum validation answers first, so this checks the text here.
func TestParseUploadChecksumRefusesUnknownAlgorithm(t *testing.T) {
	_, err := parseUploadChecksum(new(s3api.ChecksumAlgorithm("FOO")), nil)
	const want = "Checksum algorithm provided is unsupported. Please try again with any of the valid types: [CRC32, CRC32C, CRC64NVME, MD5, SHA1, SHA256, SHA512, XXHASH128, XXHASH3, XXHASH64]"
	if err == nil || err.ID.QueryCode != "InvalidRequest" || err.Message != want {
		t.Fatalf("parseUploadChecksum(FOO) = %v, want InvalidRequest %q", err, want)
	}
}
