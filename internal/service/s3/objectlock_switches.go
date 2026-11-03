package s3

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The Object Lock switches of s3-ssec-object-lock.md 11.4 that the lock
// headers, the default retention, the retention and legal-hold operations
// and the delete paths follow, in that table's order after the SSE-C ones
// (ssecSwitches). None is settled by a recording yet: each names the
// recording-queue item of section 12 that would settle it.

// objectLockSwitches lists the switches in table order.
func objectLockSwitches() []fidelity.Entry {
	const (
		headerValidation = "62-header-validation-third-party-recorded-all-400-invalidargument"
		retentionOps     = "64-putobjectretention-and-getobjectretention"
	)
	return []fidelity.Entry{
		recordedSwitch("s3.object-lock.retain-until-format", "milliseconds-header-trimmed", "67-response-headers-on-get-and-head", "S3SOL-12",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.PutObjectRetention", "s3.GetObject", "s3.HeadObject", "s3.GetObjectRetention", "s3.DeleteObject", "s3.DeleteObjects"}),
		recordedSwitch("s3.object-lock.default-years", "calendar-years", "53-default-retention-dating", "S3SOL-13",
			[]string{"s3.PutObjectLockConfiguration", "s3.PutObject", "s3.CopyObject", "s3.CompleteMultipartUpload"}),
		recordedSwitch("s3.object-lock.checksum-scope", "any-lock-parameter-and-parts", "63-the-content-md5-or-checksum-requirement", "S3SOL-14, S3SOL-15",
			[]string{"s3.PutObject", "s3.UploadPart"}),
		researchSwitch("s3.object-lock.copy-default", "default-applied", evidence.Inferred, "68-copyobject-does-not-carry-lock-state", "S3SOL-16",
			[]string{"s3.CopyObject"}),
		researchSwitch("s3.object-lock.headers-without-lock", "InvalidRequest", evidence.Inferred, headerValidation, "S3SOL-17",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload"}),
		researchSwitch("s3.object-lock.header-texts", "InvalidArgument", evidence.Inferred, headerValidation, "S3SOL-18",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.PutObjectRetention"}),
		researchSwitch("s3.object-lock.mode-upgrade", "refused", evidence.Inferred, retentionOps, "S3SOL-19",
			[]string{"s3.PutObjectRetention"}),
		researchSwitch("s3.object-lock.date-without-mode", "MalformedXML", evidence.Inferred, retentionOps, "S3SOL-20",
			[]string{"s3.PutObjectRetention"}),
		researchSwitch("s3.object-lock.cleared-reads", "404-and-OFF", evidence.Inferred, retentionOps, "S3SOL-21",
			[]string{"s3.GetObjectRetention", "s3.GetObjectLegalHold", "s3.GetObject", "s3.HeadObject"}),
		researchSwitch("s3.object-lock.check-order", "lock-state-first", evidence.Inferred, "65-putobjectlegalhold-and-getobjectlegalhold", "S3SOL-22",
			[]string{"s3.PutObjectRetention", "s3.GetObjectRetention", "s3.PutObjectLegalHold", "s3.GetObjectLegalHold"}),
		researchSwitch("s3.object-lock.bypass-without-lock", "refused", evidence.ThirdPartyRecorded, "73-the-bypass-header-on-a-bucket-without-object-lock", "S3SOL-23",
			[]string{"s3.DeleteObject", "s3.DeleteObjects"}),
	}
}
