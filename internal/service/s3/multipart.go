package s3

import "github.com/sustinbebustin/understudy/internal/api/s3api"

// bindMultipart binds the multipart upload operations. This build binds
// none, so each answers 501 NotImplemented in S3's error format and
// coverage lists it as unbound.
func (*Service) bindMultipart(s3api.Binder) {}

// Uploads is a bucket's multipart uploads in progress. No operation of
// this build starts one, so it holds none.
type Uploads struct{}
