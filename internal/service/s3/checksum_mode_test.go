package s3_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
)

// checksumHeaders is the x-amz-checksum-* headers of an encoded response.
func checksumHeaders(header http.Header) map[string]string {
	got := map[string]string{}
	for name, values := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-checksum-") {
			got[strings.ToLower(name)] = strings.Join(values, ",")
		}
	}
	return got
}

// TestChecksumModeIsMatchedInAnyCase: GetObject and HeadObject with
// x-amz-checksum-mode enabled, as the CRT S3 client behind the AWS CLI's
// s3 commands sends it, or Enabled, answer the object's checksum headers
// exactly as ENABLED does: S3 accepts the value in lower case, and mixed
// case is inferred to count the same (s3.md 5.8).
func TestChecksumModeIsMatchedInAnyCase(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("modes")
		h.put("modes", "k", "hi\n")
		get := func(mode s3api.ChecksumMode) map[string]string {
			out := ok(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "modes", Key: "k", ChecksumMode: new(mode)})
			return checksumHeaders(encode(h.T, s3api.OpGetObject.Descriptor(), out).header)
		}
		head := func(mode s3api.ChecksumMode) map[string]string {
			out := ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "modes", Key: "k", ChecksumMode: new(mode)})
			return checksumHeaders(encode(h.T, s3api.OpHeadObject.Descriptor(), out).header)
		}
		// "hi\n" is CRC64NVME JYwwIoi0PuE=, which S3 computes when the PUT
		// sends no checksum (s3.md 6.2).
		want := map[string]string{"x-amz-checksum-crc64nvme": "JYwwIoi0PuE=", "x-amz-checksum-type": "FULL_OBJECT"}
		for op, read := range map[string]func(s3api.ChecksumMode) map[string]string{"GetObject": get, "HeadObject": head} {
			if got := read(s3api.ChecksumModeEnabled); !maps.Equal(got, want) {
				t.Fatalf("%s with ENABLED: checksum headers %v, want %v", op, got, want)
			}
			for _, mode := range []s3api.ChecksumMode{"enabled", "Enabled"} {
				if got := read(mode); !maps.Equal(got, want) {
					t.Errorf("%s with %s: checksum headers %v, want %v, as with ENABLED", op, mode, got, want)
				}
			}
		}
	})
}

// TestChecksumModeOtherValuesAreRefused: a value that is no case variant
// of ENABLED is refused with 400 InvalidRequest "Value for
// x-amz-checksum-mode header is invalid.", with no ArgumentName or
// ArgumentValue (s3.md 5.8; S3-01, recorded 2026-11-03). That the refusal
// comes before the bucket is looked up is inferred: S3-01 read an existing
// object.
func TestChecksumModeOtherValuesAreRefused(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("modes")
		h.put("modes", "k", "hi\n")
		const message = "Value for x-amz-checksum-mode header is invalid."
		junk := new(s3api.ChecksumMode("junk"))
		for _, bucket := range []string{"modes", "missing"} {
			_, err := invoke(h, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: bucket, Key: "k", ChecksumMode: junk})
			wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
			if err.Message != message {
				t.Errorf("GetObject from %s with junk: message %q, want %q", bucket, err.Message, message)
			}
			if len(err.Fields) != 0 {
				t.Errorf("GetObject from %s with junk: elements %v, want none", bucket, err.Fields)
			}
			_, err = invoke(h, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: bucket, Key: "k", ChecksumMode: junk})
			wantCode(t, err, "InvalidRequest", http.StatusBadRequest)
		}
	})
}
