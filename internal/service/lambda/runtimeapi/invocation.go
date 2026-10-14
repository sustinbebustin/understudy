package runtimeapi

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// Invocation is one event handed to a runtime. The wall-clock deadline is
// not part of it: the runtime API computes Lambda-Runtime-Deadline-Ms at
// dispatch, after any cold start, as wall time plus what is left of Timeout
// (lambda-runtime-contract.md 1).
type Invocation struct {
	// RequestID is Lambda-Runtime-Aws-Request-Id, the X-Amzn-RequestId of
	// the Invoke call; retries of one async event share it.
	RequestID ids.LambdaRequestID
	// InvocationID is Lambda-Runtime-Invocation-Id, unique per attempt. A
	// runtime that echoes a different value is refused with 400
	// InvalidInvocationId.
	InvocationID string
	// FunctionARN is Lambda-Runtime-Invoked-Function-Arn: the ARN the
	// caller invoked, qualified when it named a version or alias.
	FunctionARN aws.ARN
	// TraceID is Lambda-Runtime-Trace-Id, an X-Ray trace header such as
	// Root=1-5bef4de7-ad49b0e87f6ef6c87fc2e700;Parent=9a9197af755a6419;Sampled=0.
	TraceID string
	// ClientContext and CognitoIdentity are JSON documents; each header is
	// sent only when its value is set, as the RIE does, and aws-lambda-go
	// and the Python RIC treat an absent header as "none" (lambda-api.md
	// 9.1).
	ClientContext   string
	CognitoIdentity string
	// Payload is the event, sent as the /next body.
	Payload []byte
	// Timeout is the function's configured timeout. It bounds the invoke
	// phase on a monotonic timer started at dispatch.
	Timeout time.Duration
}

// Validate reports, as an *InvocationError, a field the runtime protocol
// requires but inv lacks. Environment.Invoke runs it before anything else.
func (inv Invocation) Validate() error {
	switch {
	case inv.RequestID == "":
		return &InvocationError{Field: "RequestID", Reason: "is empty"}
	case inv.InvocationID == "":
		return &InvocationError{Field: "InvocationID", Reason: "is empty"}
	case inv.FunctionARN == (aws.ARN{}):
		return &InvocationError{Field: "FunctionARN", Reason: "is unset"}
	case inv.TraceID == "":
		return &InvocationError{Field: "TraceID", Reason: "is empty"}
	case inv.Timeout <= 0:
		return &InvocationError{Field: "Timeout", Reason: "must be positive, got " + inv.Timeout.String()}
	}
	return nil
}
