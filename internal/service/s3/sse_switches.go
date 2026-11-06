package s3

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The SSE-C and bucket-encryption switches of s3-ssec-object-lock.md 11.4,
// in that table's order. Each names the recording-queue item of section 12
// that recorded it.

// ssecSwitches lists the switches in table order.
func ssecSwitches() []fidelity.Entry {
	// The operations that take the SSE-C trio, whose requests over plain
	// HTTP are refused (s3-ssec-object-lock.md 3.1, 3.9).
	ssecPlainHTTPOps := []string{
		"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy",
		"s3.CompleteMultipartUpload", "s3.GetObject", "s3.HeadObject", "s3.GetObjectAttributes",
	}
	return []fidelity.Entry{
		// S3SOL-02 recorded PutObject and GetObject; the other operations
		// are inferred. ListParts checks the trio when sent, so it is
		// refused over plain HTTP the same way (checkCustomerList; 3.9).
		recordedSwitch("s3.sse-c.plain-http", "refused", "39-https", "S3SOL-02", append(slices.Clone(ssecPlainHTTPOps), "s3.ListParts")),
		recordedSwitch("s3.sse-c.blocked-write", "AccessDenied", "43-what-the-block-refuses", "S3SOL-01",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy"}),
		// A read computes a missing key MD5 (S3SOL-03 03-get), so the
		// refusal governs the writes alone.
		recordedSwitch("s3.sse-c.missing-key-md5", "InvalidArgument", "32-write-side-validation-third-party-recorded", "S3SOL-03",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy", "s3.CompleteMultipartUpload"}),
		recordedSwitch("s3.sse-c.key-encoding", "write-side-texts", "32-write-side-validation-third-party-recorded", "S3SOL-04", ssecPlainHTTPOps),
		recordedSwitch("s3.sse-c.metadata-without-key", "InvalidRequest", "33-reads-getobject-headobject-getobjectattributes", "S3SOL-05",
			[]string{"s3.HeadObject", "s3.GetObjectAttributes"}),
		recordedSwitch("s3.sse-c.copy-source-not-applicable", "InvalidRequest", "35-copyobject-and-uploadpartcopy", "S3SOL-06",
			[]string{"s3.CopyObject", "s3.UploadPartCopy"}),
		recordedSwitch("s3.sse-c.copy-source-wrong-key", "InvalidRequest", "35-copyobject-and-uploadpartcopy", "S3SOL-07",
			[]string{"s3.CopyObject", "s3.UploadPartCopy"}),
		recordedSwitch("s3.sse-c.complete-headers", "required-with-checksum", "36-multipart-uploads", "S3SOL-08",
			[]string{"s3.CompleteMultipartUpload", "s3.ListParts"}),
		recordedSwitch("s3.encryption.unblocked-read", "NONE-element", "42-blocking-and-unblocking", "S3SOL-10",
			[]string{"s3.PutBucketEncryption", "s3.GetBucketEncryption"}),
		recordedSwitch("s3.encryption.blocked-types-omitted", "reset-to-new-bucket", "42-blocking-and-unblocking", "S3SOL-11",
			[]string{"s3.PutBucketEncryption", "s3.DeleteBucketEncryption"}),
	}
}

// recordedSwitch is one switch of s3-ssec-object-lock.md 11.4, SSE-C or
// Object Lock, whose section 12 item a recording session recorded
// (testdata/parity/s3/S3SOL): value is AWS's recorded behavior, anchor its
// section's anchor in that note, record the item. No scenario settles it,
// as the replayer skips S3's REST requests (s3Unreplayed).
func recordedSwitch(name, value, anchor, record string, ops []string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/s3-ssec-object-lock.md#" + anchor,
		Record:  record,
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: s3Unreplayed(record + " is")},
	}.Entry()
}
