package lambda_test

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
)

// role is an execution role ARN in the test account.
const role = "arn:aws:iam::000000000000:role/lambda-role"

// harness is a kernel with the Lambda and SQS services.
type harness struct {
	*kerneltest.K
	svc *lambda.Service
}

// run runs fn against the services in a kernel built with kopts.
func run(t *testing.T, opts lambda.Options, fn func(h *harness), kopts ...kerneltest.Option) {
	t.Helper()
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
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		sqs.New(k.Deps(sqsapi.Service()), sqs.Options{})
		fn(&harness{K: k, svc: lambda.New(deps, opts)})
	}, append(kopts, kerneltest.Services(lambdaapi.Service(), sqsapi.Service()))...)
}

// call runs op as a client request and fails the test on an error.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err != nil {
		h.Fatalf("%s: %s %s", op.Descriptor().ID.Name, err.ID.JSONType, err.Message)
	}
	return out
}

// fail runs op as a client request that must fail, and returns the error.
func fail[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *awserr.Error {
	h.Helper()
	_, err := h.Invoke(h.Context(), op, in)
	if err == nil {
		h.Fatalf("%s succeeded, want an error", op.Descriptor().ID.Name)
	}
	return err
}

// zipOf builds a zip package holding a bootstrap file.
func zipOf(tb testing.TB, body string) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("bootstrap")
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// zipFunction is the CreateFunction input Terraform sends for a zip
// function (tf-compute.md 2.1): Description, MemorySize, Timeout,
// PackageType and Publish always.
func zipFunction(tb testing.TB, name string) *lambdaapi.CreateFunctionInput {
	tb.Helper()
	return &lambdaapi.CreateFunctionInput{
		FunctionName: name,
		Role:         role,
		Runtime:      new(lambdaapi.RuntimeProvidedal2023),
		Handler:      new("bootstrap"),
		Code:         lambdaapi.FunctionCode{ZipFile: zipOf(tb, "#!/bin/sh\n")},
		Description:  new(""),
		MemorySize:   new(int32(128)),
		Timeout:      new(int32(3)),
		PackageType:  new(lambdaapi.PackageTypeZip),
	}
}

func (h *harness) createFunction(name string) *lambdaapi.FunctionConfiguration {
	h.Helper()
	return call(h, lambdaapi.OpCreateFunction, zipFunction(h, name))
}

// settle waits until the kernel has no pending work.
func (h *harness) settle() {
	h.Helper()
	if _, err := h.Kernel.Settle(h.Context(), time.Minute); err != nil {
		h.Fatal(err)
	}
}

// errorIs checks an error's identity and message.
func errorIs(tb testing.TB, err *awserr.Error, shape, message string) {
	tb.Helper()
	if err == nil {
		tb.Fatalf("no error, want %s %q", shape, message)
	}
	if got := err.ID.Shape.Name(); got != shape || err.Message != message {
		tb.Fatalf("error = %s %q\nwant    %s %q", got, err.Message, shape, message)
	}
}
