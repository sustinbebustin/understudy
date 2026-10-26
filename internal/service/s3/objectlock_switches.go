package s3

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// The Object Lock switches of s3-ssec-object-lock.md 11.4 that the lock
// headers, the default retention and the delete paths follow, in that
// table's order after the SSE-C ones (ssecSwitches). None is settled by a
// recording yet: each names the recording-queue item of section 12 that
// would settle it.

// objectLockSwitches lists the switches in table order.
func objectLockSwitches() []fidelity.Entry {
	const headerValidation = "62-header-validation-third-party-recorded-all-400-invalidargument"
	return []fidelity.Entry{
		ssecSwitch("s3.object-lock.retain-until-format", "milliseconds", evidence.Inferred, "67-response-headers-on-get-and-head", "S3SOL-12",
			[]string{"s3.GetObject", "s3.HeadObject", "s3.GetObjectRetention", "s3.DeleteObject", "s3.DeleteObjects"}),
		ssecSwitch("s3.object-lock.default-years", "365-day", evidence.Inferred, "53-default-retention-dating", "S3SOL-13",
			[]string{"s3.PutObjectLockConfiguration", "s3.PutObject", "s3.CopyObject", "s3.CompleteMultipartUpload"}),
		ssecSwitch("s3.object-lock.checksum-scope", "PutObject-retention", evidence.Inferred, "63-the-content-md5-or-checksum-requirement", "S3SOL-14, S3SOL-15",
			[]string{"s3.PutObject", "s3.UploadPart"}),
		ssecSwitch("s3.object-lock.copy-default", "default-applied", evidence.Inferred, "68-copyobject-does-not-carry-lock-state", "S3SOL-16",
			[]string{"s3.CopyObject"}),
		ssecSwitch("s3.object-lock.headers-without-lock", "InvalidRequest", evidence.Inferred, headerValidation, "S3SOL-17",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload"}),
		ssecSwitch("s3.object-lock.header-texts", "InvalidArgument", evidence.Inferred, headerValidation, "S3SOL-18",
			[]string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.PutObjectRetention"}),
		ssecSwitch("s3.object-lock.bypass-without-lock", "refused", evidence.ThirdPartyRecorded, "73-the-bypass-header-on-a-bucket-without-object-lock", "S3SOL-23",
			[]string{"s3.DeleteObject", "s3.DeleteObjects"}),
	}
}
