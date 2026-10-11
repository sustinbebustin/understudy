package s3

import (
	"context"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// headObject answers GetObject's head without the body (s3.md 5.8); it
// reads an archived object too, and the codec sends its errors without a
// body.
func (s *Service) headObject(ctx context.Context, c *kernel.Call, in *s3api.HeadObjectInput) (*s3api.HeadObjectOutput, *awserr.Error) {
	got, err := s.readObject(ctx, c, at(in.Bucket, in.ExpectedBucketOwner), readRequest{
		key: in.Key, versionID: in.VersionId, method: http.MethodHead, rangeSpec: in.Range, partNumber: in.PartNumber,
		conds: readConditions{ifMatch: in.IfMatch, ifNoneMatch: in.IfNoneMatch, ifModifiedSince: in.IfModifiedSince, ifUnmodifiedSince: in.IfUnmodifiedSince},
	})
	if err != nil {
		return nil, err
	}
	if customerKeyed(in.SSECustomerAlgorithm, in.SSECustomerKey, in.SSECustomerKeyMD5) {
		return nil, errSSECNotApplicable()
	}
	return headOf(got, in.ChecksumMode), nil
}
