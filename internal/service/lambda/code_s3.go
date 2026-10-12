package lambda

import (
	"context"
	"fmt"
	"io"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// fetchPackage reads the package src names from S3 for c's caller, as
// Lambda reads it with the caller's s3:GetObject permission. A failed read
// is InvalidParameterValueException naming S3's error code and message, as
// AWS answers it (lambda-api.md 4.3); an object larger than a package can
// unzip to is refused unread.
func (s *Service) fetchPackage(ctx context.Context, c *kernel.Call, src s3Source) ([]byte, *awserr.Error) {
	t := kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.LambdaCode}
	out, err := kernel.Invoke(ctx, s.k, c, s3api.OpGetObject, t, &s3api.GetObjectInput{Bucket: src.bucket, Key: src.key, VersionId: src.version})
	if err != nil {
		return nil, errS3Package(err)
	}
	size := out.Body.Size()
	if size > maxUnzipped {
		return nil, errUnzippedTooLarge()
	}
	rc, rerr := out.Body.Range(0, size)
	if rerr != nil {
		return nil, awserr.Internalf(fmt.Errorf("reading the package s3://%s/%s: %w", src.bucket, src.key, rerr))
	}
	defer rc.Close()
	b, rerr := io.ReadAll(rc)
	if rerr != nil {
		return nil, awserr.Internalf(fmt.Errorf("reading the package s3://%s/%s: %w", src.bucket, src.key, rerr))
	}
	return b, nil
}
