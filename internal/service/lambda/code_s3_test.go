package lambda_test

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
)

// openedBody is a GetObject body opened before the answer, as S3's is: it
// reports its size and hands over a reader that records its Close.
type openedBody struct {
	size   int64
	closed atomic.Bool
}

func (b *openedBody) Size() int64 { return b.size }

func (b *openedBody) Range(int64, int64) (io.ReadCloser, error) {
	return closeRecorder{Reader: strings.NewReader(""), closed: &b.closed}, nil
}

type closeRecorder struct {
	io.Reader
	closed *atomic.Bool
}

func (r closeRecorder) Close() error {
	r.closed.Store(true)
	return nil
}

// An S3 package larger than a package can unzip to is refused unread, and
// the body GetObject opened for it is closed rather than leaked.
func TestOversizedS3PackageClosesTheObjectBody(t *testing.T) {
	body := &openedBody{size: codestore.DefaultMaxUnzipped + 1}
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory("", 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		s3api.NewBinder(k.Deps(s3api.Service()).Binder).GetObject(func(context.Context, *kernel.Call, *s3api.GetObjectInput) (*s3api.GetObjectOutput, *awserr.Error) {
			return &s3api.GetObjectOutput{Body: body}, nil
		}, registry.Full{})
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		h := &harness{K: k, svc: lambda.New(deps, lambda.Options{})}
		in := zipFunction(t, "fn")
		in.Code = lambdaapi.FunctionCode{S3Bucket: new("code"), S3Key: new("fn.zip")}
		errorIs(t, fail(h, lambdaapi.OpCreateFunction, in), "InvalidParameterValueException",
			"Unzipped size must be smaller than "+strconv.FormatInt(codestore.DefaultMaxUnzipped, 10)+" bytes")
		if !body.closed.Load() {
			t.Error("the oversized package's GetObject body was left open")
		}
	}, kerneltest.Services(lambdaapi.Service(), s3api.Service()))
}
