package s3

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// Object limits (s3.md 5.1, 5.2, 5.4).
const (
	maxKeyBytes = 1024
	// maxUserMetadata is the user metadata budget: the UTF-8 bytes of every
	// key and value together.
	maxUserMetadata = 2048
	// maxSinglePut is the largest object one PutObject writes and the
	// largest source one CopyObject copies: 5 GiB (s3.md 5.2, 7).
	maxSinglePut = 5 << 30
	// defaultContentType is S3's, not application/octet-stream (s3.md 5.4).
	defaultContentType = "binary/octet-stream"
	// awsChunked is the Content-Encoding coding of a chunk-framed upload,
	// which S3 removes from what it stores (s3.md 5.4).
	awsChunked = "aws-chunked"
)

// systemHeaders is the system metadata a write sends.
type systemHeaders struct {
	contentType, cacheControl, contentDisposition, contentEncoding, contentLanguage, expires, websiteRedirect *string
}

// metadata reads the system metadata of a write: Content-Type defaults to
// binary/octet-stream, and aws-chunked is dropped from Content-Encoding,
// which an aws-chunked-only value leaves unset.
func (h systemHeaders) metadata() SystemMetadata {
	m := SystemMetadata{
		ContentType:        defaultContentType,
		CacheControl:       h.cacheControl,
		ContentDisposition: h.contentDisposition,
		ContentLanguage:    h.contentLanguage,
		Expires:            h.expires,
		WebsiteRedirect:    h.websiteRedirect,
	}
	if h.contentType != nil {
		m.ContentType = *h.contentType
	}
	if h.contentEncoding != nil {
		var kept []string
		for coding := range strings.SplitSeq(*h.contentEncoding, ",") {
			if coding = strings.TrimSpace(coding); coding != "" && !strings.EqualFold(coding, awsChunked) {
				kept = append(kept, coding)
			}
		}
		if len(kept) > 0 {
			m.ContentEncoding = new(strings.Join(kept, ","))
		}
	}
	return m
}

// chunked reports a Content-Encoding that frames the body as aws-chunked.
func chunked(contentEncoding *string) bool {
	if contentEncoding == nil {
		return false
	}
	for coding := range strings.SplitSeq(*contentEncoding, ",") {
		if strings.EqualFold(strings.TrimSpace(coding), awsChunked) {
			return true
		}
	}
	return false
}

// checkKey refuses a key longer than 1,024 bytes. The context elements are
// UNVERIFIED (s3.md 5.1).
func checkKey(key string) *awserr.Error {
	if len(key) <= maxKeyBytes {
		return nil
	}
	e := withField(s3api.ErrKeyTooLongError("Your key is too long"), "MaxSizeAllowed", strconv.Itoa(maxKeyBytes))
	return withField(e, "Size", strconv.Itoa(len(key)))
}

// checkUserMetadata enforces the 2 KB user metadata budget (s3.md 5.4).
// The restbind decoder has already lower-cased the keys.
func checkUserMetadata(m map[string]string) *awserr.Error {
	n := 0
	for k, v := range m {
		n += len(k) + len(v)
	}
	if n > maxUserMetadata {
		return s3api.ErrMetadataTooLarge("Your metadata headers exceed the maximum allowed metadata size.")
	}
	return nil
}

// storageClass reads x-amz-storage-class: STANDARD when absent, and only
// the classes a general purpose bucket accepts (s3.md 5.6).
func storageClass(v *s3api.StorageClass) (s3api.StorageClass, *awserr.Error) {
	if v == nil {
		return s3api.StorageClassStandard, nil
	}
	switch *v {
	case s3api.StorageClassStandard, s3api.StorageClassReducedRedundancy, s3api.StorageClassStandardIa,
		s3api.StorageClassOnezoneIa, s3api.StorageClassIntelligentTiering, s3api.StorageClassGlacier,
		s3api.StorageClassDeepArchive, s3api.StorageClassGlacierIr:
		return *v, nil
	case s3api.StorageClassOutposts, s3api.StorageClassSnow, s3api.StorageClassExpressOnezone, s3api.StorageClassFsxOpenzfs,
		s3api.StorageClassFsxOntap, s3api.StorageClassAwsBackupWarm, s3api.StorageClassAwsBackupLowCostWarm:
	}
	return "", withField(s3api.ErrInvalidStorageClass("The storage class you specified is not valid"), "StorageClassRequested", string(*v))
}

// archived reports a class GetObject cannot read without a restore.
func archived(c s3api.StorageClass) bool {
	return c == s3api.StorageClassGlacier || c == s3api.StorageClassDeepArchive
}

// reportedClass is the x-amz-storage-class HEAD and GET report: omitted for
// STANDARD (s3.md 5.6).
func reportedClass(c s3api.StorageClass) *s3api.StorageClass {
	if c == s3api.StorageClassStandard {
		return nil
	}
	return new(c)
}
