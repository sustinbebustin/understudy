package kernel

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Handler implements one AWS operation. Dispatch maps (nil, nil) and
// (out, err) to Internal, naming the operation; unit outputs are the empty
// struct, so nil is never legitimate.
type Handler[In, Out any] func(ctx context.Context, c *Call, in *In) (*Out, *awserr.Error)

// RawHandler is the passthrough form (DynamoDB proxy only, ADR 0014): the
// typed input is decoded and validated; w says whether a client request
// exists to forward.
type RawHandler[In any] func(ctx context.Context, c *Call, in *In, w Wire) (RawResponse, *awserr.Error)

// RawHTTPHandler serves a raw operation (codegen/services/<svc>.json
// "rawOperations"): the function URL data plane and the SNS links. It runs
// the full pipeline and owns the HTTP body in both directions.
type RawHTTPHandler func(ctx context.Context, c *Call, r *http.Request, w http.ResponseWriter) *awserr.Error

// Wire says whether a client request exists to forward.
//
//sumtype:decl
type Wire interface{ isWire() }

// FromClient carries the client's original request.
type FromClient struct{ Request RawRequest }

// Synthesized is an internal caller's input; the service encodes the typed
// input itself.
type Synthesized struct{}

func (FromClient) isWire()  {}
func (Synthesized) isWire() {}

// RawRequest is a client request as received: method, escaped path and
// query, headers and the body bytes.
type RawRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// RawResponse is what a RawHandler answers, written to the client as is.
type RawResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// RawOutput is the output of an operation bound with BindRaw: the
// RawResponse its handler answered, which the edge writes as is and an
// internal caller reads back through Invoke.
type RawOutput struct{ Response RawResponse }

// RawExchange is the input Dispatch hands a raw HTTP operation: the
// request and the writer its handler answers on.
type RawExchange struct {
	Request *http.Request
	Writer  http.ResponseWriter
}

// RawWritten is the output of a raw HTTP operation: its handler has
// written the response itself.
type RawWritten struct{}

// Op is a typed, immutable handle on an operation. Bindings never live on
// the descriptor: each kernel holds its own table indexed by
// Operation.Ordinal, so several kernels coexist without a data race.
type Op[In, Out any] struct{ desc *registry.Operation }

// NewOp returns the handle on the operation d describes. The generated
// packages declare one per operation.
func NewOp[In, Out any](d *registry.Operation) Op[In, Out] {
	return Op[In, Out]{desc: d}
}

// Descriptor returns the operation's descriptor.
func (o Op[In, Out]) Descriptor() *registry.Operation { return o.desc }

// NoValidationPolicyError reports an input type without the generated
// ValidationError method, so a violation cannot be rendered in AWS's
// words.
type NoValidationPolicyError struct {
	Type string
}

func (e *NoValidationPolicyError) Error() string {
	return fmt.Sprintf("kernel: input type %s has no generated ValidationError method, so its violations have no AWS rendering; bind a generated input type", e.Type)
}

// Validate renders violations of an In as the operation answers them: the
// overlay's code and text per member path and constraint kind, then per
// operation, then per service, reporting the first violation in member
// order when they span two codes. The policy is generated onto *In; an In
// without one is an Internal error.
func Validate[In any](vs []awserr.Violation) *awserr.Error {
	if len(vs) == 0 {
		return nil
	}
	if p, ok := any((*In)(nil)).(interface {
		ValidationError(vs []awserr.Violation) *awserr.Error
	}); ok {
		return p.ValidationError(vs)
	}
	return awserr.Internalf(&NoValidationPolicyError{Type: fmt.Sprintf("%T", (*In)(nil))})
}

// NotImplemented is the answer for an operation in the model that no
// handler is bound to: 501 in the protocol's native envelope (ADR 0003).
func NotImplemented(op *registry.Operation) *awserr.Error {
	return &awserr.Error{
		ID: awserr.NotImplemented(),
		Message: fmt.Sprintf("understudy does not implement %s. Run 'understudy coverage --service %s' for what is supported.",
			op.ID, op.ID.Service),
		Origin: awserr.Unimplemented{Op: op.ID},
	}
}
