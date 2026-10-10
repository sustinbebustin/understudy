package runtimeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/ids"
)

// MaxResponseBytes is Lambda's synchronous response limit; a runtime that
// posts more makes the invocation a Function.ResponseSizeTooLarge error
// (lambda-api.md 8.2).
const MaxResponseBytes = 6_291_556

// Outcome is how one invocation ended, as the Invoke caller sees it. Every
// outcome but Success is a function error: Invoke answers 200 with
// X-Amz-Function-Error: Unhandled and the outcome's Response as the body
// (lambda-api.md 8.3).
//
//sumtype:decl
type Outcome interface {
	// Response is the Invoke response payload for requestID.
	Response(requestID ids.LambdaRequestID) []byte
	isOutcome()
}

// Success is a payload the runtime posted to /response.
type Success struct {
	Payload []byte
}

// FunctionError is an error the runtime posted to /error, or an error
// trailer on /response. Payload is forwarded unmodified, as AWS does; Type
// is the normalized Lambda-Runtime-Function-Error-Type, Runtime.Unknown when
// the runtime sent none (aws-lambda-go and the Python RIC often do not).
type FunctionError struct {
	Type    string
	Payload []byte
}

// Timeout is an invocation still running when its timeout passed. The
// environment cannot be reused: its runtime is still inside the handler.
type Timeout struct {
	After time.Duration
}

// ExitError is a runtime process that exited before answering, with the
// container's exit code.
type ExitError struct {
	Code int
}

// OOM is a container killed for exceeding its memory limit.
type OOM struct{}

// InitError is an initialization failure the runtime posted to /init/error
// before it ever asked for an invocation.
type InitError struct {
	Type    string
	Payload []byte
}

// ArchMismatch is a container that could not execute the function's code
// because it was built for another architecture (exec format error). AWS
// reports this as Runtime.InvalidEntrypoint. Declared equals Host when the
// container ran natively and the code itself is built for another
// architecture than the function declares.
type ArchMismatch struct {
	Declared, Host string
}

// Response returns the posted payload.
func (o Success) Response(ids.LambdaRequestID) []byte { return o.Payload }

// Response returns the posted error unmodified.
func (o FunctionError) Response(ids.LambdaRequestID) []byte { return o.Payload }

// Response is the Sandbox.Timedout payload, for example "RequestId: <id>
// Error: Task timed out after 3.00 seconds".
func (o Timeout) Response(requestID ids.LambdaRequestID) []byte {
	return errorPayload("Sandbox.Timedout", fmt.Sprintf("RequestId: %s Error: Task timed out after %.2f seconds", requestID, o.After.Seconds()))
}

// Response is the Runtime.ExitError payload: "Runtime exited without
// providing a reason" for exit code 0, else "Runtime exited with error:
// exit status <n>". A signal arrives as the container's 128+n code; the
// package doc says why a segfault is not reported as a timeout.
func (o ExitError) Response(requestID ids.LambdaRequestID) []byte {
	if o.Code == 0 {
		return errorPayload("Runtime.ExitError", fmt.Sprintf("RequestId: %s Error: Runtime exited without providing a reason", requestID))
	}
	return errorPayload("Runtime.ExitError", fmt.Sprintf("RequestId: %s Error: Runtime exited with error: exit status %d", requestID, o.Code))
}

// Response is the Runtime.OutOfMemory payload.
func (OOM) Response(requestID ids.LambdaRequestID) []byte {
	return errorPayload("Runtime.OutOfMemory", fmt.Sprintf("RequestId: %s Error: Runtime exited with error: signal: killed", requestID))
}

// Response returns the posted initialization error, or a payload naming
// only its type when the runtime posted an empty body.
func (o InitError) Response(requestID ids.LambdaRequestID) []byte {
	if len(bytes.TrimSpace(o.Payload)) == 0 {
		return errorPayload(o.Type, fmt.Sprintf("RequestId: %s Error: the runtime reported an initialization failure without details", requestID))
	}
	return o.Payload
}

// Response is the Runtime.InvalidEntrypoint payload. When the declared
// architecture is not the host's, it names both and the two remedies;
// when they are the same, the code is what is built for another
// architecture, and the remedy is rebuilding it or declaring its own.
func (o ArchMismatch) Response(requestID ids.LambdaRequestID) []byte {
	if o.Declared == o.Host {
		return errorPayload("Runtime.InvalidEntrypoint", fmt.Sprintf(
			"RequestId: %s Error: exec format error: the function declares %s, which the container host runs, but its code is built for another architecture; rebuild it for %s (a Go bootstrap with GOOS=linux GOARCH=%s) or set Architectures to the architecture it is built for",
			requestID, o.Declared, o.Declared, goarch(o.Declared)))
	}
	return errorPayload("Runtime.InvalidEntrypoint", fmt.Sprintf(
		"RequestId: %s Error: exec format error: the function is built for %s but the container host runs %s; set Architectures to [%q] or install binfmt/QEMU emulation for %s on the Docker host",
		requestID, o.Declared, o.Host, o.Host, o.Declared))
}

// goarch is the GOARCH that builds for a Lambda architecture.
func goarch(lambdaArch string) string {
	if lambdaArch == "x86_64" {
		return "amd64"
	}
	return lambdaArch
}

func (Success) isOutcome()       {}
func (FunctionError) isOutcome() {}
func (Timeout) isOutcome()       {}
func (ExitError) isOutcome()     {}
func (OOM) isOutcome()           {}
func (InitError) isOutcome()     {}
func (ArchMismatch) isOutcome()  {}

// FunctionErrorHeader returns the X-Amz-Function-Error value for o:
// "Unhandled" for every function error and "" for Success. AWS no longer
// emits "Handled" (lambda-api.md 8.3).
func FunctionErrorHeader(o Outcome) string {
	if _, ok := o.(Success); ok {
		return ""
	}
	return "Unhandled"
}

// Lambda's own error payloads order their two fields as AWS recorded each
// one: errorType first for Sandbox.Timedout and Runtime.ExitError
// (lambda-api.md 8.3), errorMessage first for Function.ResponseSizeTooLarge
// (8.2). Payloads with no recording put errorType first.

// errorPayload is an error payload with errorType first.
func errorPayload(errorType, message string) []byte {
	return compactJSON(struct {
		ErrorType    string `json:"errorType"`
		ErrorMessage string `json:"errorMessage"`
	}{errorType, message})
}

// messageFirstErrorPayload is an error payload with errorMessage first.
func messageFirstErrorPayload(errorType, message string) []byte {
	return compactJSON(struct {
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorType"`
	}{message, errorType})
}

// compactJSON encodes a struct of strings without HTML escaping or a
// trailing newline.
func compactJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) //nolint:errcheck,errchkjson // a struct of strings always encodes, and a bytes.Buffer never fails a write
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// validErrorType is the pattern Lambda keeps an error type by. It is
// unanchored, exactly as in the RIE's GetValidRuntimeOrFunctionErrorType
// (fatalerror.go, lambda-api.md 8.3): a type merely containing
// Runtime.Xy or Function.Xy is kept verbatim.
var validErrorType = regexp.MustCompile(`(Runtime|Function)\.[A-Z][a-zA-Z]+`)

// normalizeErrorType keeps an error type validErrorType matches and maps
// anything else to Function.Unknown when it starts with "Function.", else
// Runtime.Unknown, as Lambda does.
func normalizeErrorType(t string) string {
	switch {
	case validErrorType.MatchString(t):
		return t
	case strings.HasPrefix(t, "Function."):
		return "Function.Unknown"
	default:
		return "Runtime.Unknown"
	}
}
