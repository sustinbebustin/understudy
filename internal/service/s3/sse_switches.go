package s3

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The SSE-C and bucket-encryption switches of s3-ssec-object-lock.md 11.4,
// in that table's order. Each names the recording-queue item of section 12
// that settles it; recordedSwitch marks those the 2026-11-03 session
// recorded.

// ssecSwitches lists the switches in table order.
func ssecSwitches() []fidelity.Entry {
	// The operations that take the SSE-C trio, whose requests over plain
	// HTTP are refused (s3-ssec-object-lock.md 3.1, 3.9).
	ssecPlainHTTPOps := []string{
		"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy",
		"s3.CompleteMultipartUpload", "s3.GetObject", "s3.HeadObject", "s3.GetObjectAttributes",
	}
	return []fidelity.Entry{
		// ListParts checks the trio when sent, so it is refused over plain
		// HTTP the same way (switch s3.sse-c.complete-headers; 3.9).
		researchSwitch("s3.sse-c.plain-http", "refused", evidence.AWSDoc, "39-https", "S3SOL-02", append(slices.Clone(ssecPlainHTTPOps), "s3.ListParts")),
		recordedSwitch("s3.sse-c.blocked-write", "AccessDenied", "43-what-the-block-refuses", "S3SOL-01",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy"}),
		researchSwitch("s3.sse-c.missing-key-md5", "InvalidArgument", evidence.Inferred, "32-write-side-validation-third-party-recorded", "S3SOL-03", ssecPlainHTTPOps),
		researchSwitch("s3.sse-c.key-encoding", "write-side-texts", evidence.Inferred, "32-write-side-validation-third-party-recorded", "S3SOL-04", ssecPlainHTTPOps),
		researchSwitch("s3.sse-c.metadata-without-key", "InvalidRequest", evidence.Inferred, "33-reads-getobject-headobject-getobjectattributes", "S3SOL-05",
			[]string{"s3.HeadObject", "s3.GetObjectAttributes"}),
		researchSwitch("s3.sse-c.copy-source-not-applicable", "InvalidRequest", evidence.Inferred, "35-copyobject-and-uploadpartcopy", "S3SOL-06",
			[]string{"s3.CopyObject", "s3.UploadPartCopy"}),
		researchSwitch("s3.sse-c.copy-source-wrong-key", "AccessDenied", evidence.Inferred, "35-copyobject-and-uploadpartcopy", "S3SOL-07",
			[]string{"s3.CopyObject", "s3.UploadPartCopy"}),
		researchSwitch("s3.sse-c.complete-headers", "checked-when-sent", evidence.Inferred, "36-multipart-uploads", "S3SOL-08",
			[]string{"s3.CompleteMultipartUpload", "s3.ListParts"}),
		researchSwitch("s3.encryption.unblocked-read", "NONE-element", evidence.Inferred, "42-blocking-and-unblocking", "S3SOL-10",
			[]string{"s3.PutBucketEncryption", "s3.GetBucketEncryption"}),
		researchSwitch("s3.encryption.blocked-types-omitted", "kept", evidence.Inferred, "42-blocking-and-unblocking", "S3SOL-11",
			[]string{"s3.PutBucketEncryption", "s3.DeleteBucketEncryption"}),
	}
}

// recordedSwitch is a switch of s3-ssec-object-lock.md 11.4 whose section
// 12 item the 2026-11-03 session recorded (testdata/parity/s3/S3SOL):
// value is AWS's recorded behavior. No scenario settles it, as the
// replayer skips S3's REST requests (s3Unreplayed).
func recordedSwitch(name, value, anchor, record string, ops []string) fidelity.Entry {
	e := researchSwitch(name, value, evidence.AWSRecorded, anchor, record, ops)
	e.Settles = fidelity.NoScenario{Reason: s3Unreplayed(record + " is")}
	return e
}

// researchSwitch is one switch of s3-ssec-object-lock.md 11.4, SSE-C or
// Object Lock: anchor is its section's anchor in that note, record its
// section 12 queue item.
func researchSwitch(name, value string, label evidence.Label, anchor, record string, ops []string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   label,
		Ref:     "docs/research/s3-ssec-object-lock.md#" + anchor,
		Record:  record,
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: "unrecorded: " + record + " is queued in docs/research/s3-ssec-object-lock.md 12"},
	}.Entry()
}
