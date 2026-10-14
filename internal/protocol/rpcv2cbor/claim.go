package rpcv2cbor

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Claim is the codec's answer to whether a request is an rpcv2Cbor request,
// and for which operation.
//
//sumtype:decl
type Claim interface{ isClaim() }

// Claimed names the modeled operation the request calls.
type Claimed struct {
	Op *registry.Operation
}

// UnknownOperation is a request whose path names a service that speaks
// rpcv2Cbor but no modeled operation of it (an internal or raw operation
// counts as unknown, since the edge never claims those). The service's
// unknownOperationError answers it.
type UnknownOperation struct {
	Service *registry.Service
	// Operation is the operation segment of the path.
	Operation string
}

// Rejected is a request that is rpcv2Cbor by its Smithy-Protocol header
// and path but breaks a rule of the protocol: it carries X-Amz-Target or
// X-Amzn-Target, is not a POST, or has a Content-Type other than
// application/cbor. The codec serves it no operation. Err is the answer
// the protocol calls for, the generic UnknownOperationException, the
// identity the edge answers an unroutable request with, carrying a message
// that names the rule. Whether a later claim rule may still take the
// request, and whether Err is what the client receives, is the edge's
// routing decision; see the package doc for how the composition root maps it.
type Rejected struct {
	Err *awserr.Error
}

// Declined is a request this codec does not serve; Reason says why, for
// the journal's Unclaimed event and for the claim rules after this one.
type Declined struct {
	Reason string
}

func (Claimed) isClaim()          {}
func (UnknownOperation) isClaim() {}
func (Rejected) isClaim()         {}
func (Declined) isClaim()         {}

// The protocol's fixed strings (wire-cbor.md 3.1 to 3.3).
const (
	protocolHeader = "Smithy-Protocol"
	protocolName   = "rpc-v2-cbor"
	mediaType      = "application/cbor"
)

// Claim decides whether r is an rpcv2Cbor request (ARCHITECTURE.md 4.1
// rule 5, wire-cbor.md 3.1): header Smithy-Protocol: rpc-v2-cbor and a
// path whose last four segments are service/{Service}/operation/{Op},
// any prefix before them ignored. {Service} is the service shape's name
// (GraniteServiceVersion20100801) or its absolute id with dots for #
// (com.amazonaws.cloudwatch.GraniteServiceVersion20100801) of a service in
// reg that speaks rpcv2Cbor; {Op} is an operation name, case
// sensitive, never an absolute id. Such a request is Rejected when it
// also carries X-Amz-Target or X-Amzn-Target, which the protocol forbids,
// is not a POST, or sends a Content-Type other than application/cbor,
// whatever {Service} names. Only a request that passes those checks is
// Declined for a {Service} that names no rpcv2Cbor service.
// Accept is not checked: servers must not require it. Cross-checking the
// service against the SigV4 signing name is the edge's job.
func (Codec) Claim(r *http.Request, reg *registry.Registry) Claim {
	if v := r.Header.Get(protocolHeader); !strings.EqualFold(v, protocolName) {
		if v == "" {
			return Declined{Reason: "the request has no Smithy-Protocol header"}
		}
		return Declined{Reason: fmt.Sprintf("Smithy-Protocol %q is not %s", v, protocolName)}
	}
	path := r.URL.EscapedPath()
	service, operation, ok := routeSegments(path)
	if !ok {
		return Declined{Reason: fmt.Sprintf("Smithy-Protocol is %s but path %q does not end in /service/<name>/operation/<name>", protocolName, path)}
	}
	if reason, bad := violation(r); bad {
		return Rejected{Err: &awserr.Error{ID: awserr.UnknownOperationException(), Message: reason, Origin: awserr.Generic{}}}
	}
	svc, ok := serviceNamed(reg, service)
	if !ok {
		return Declined{Reason: fmt.Sprintf("path segment %q names no service that speaks %s", service, ref.RPCv2CBOR)}
	}
	op, ok := svc.Operation(operation)
	if !ok || op.Kind != registry.Modeled {
		return UnknownOperation{Service: svc, Operation: operation}
	}
	return Claimed{Op: op}
}

// routeSegments returns the service and operation segments of an
// rpcv2Cbor path: its last four segments are service/{s}/operation/{o}.
func routeSegments(path string) (service, operation string, ok bool) {
	segs := strings.Split(path, "/")
	if len(segs) < 5 {
		return "", "", false
	}
	tail := segs[len(segs)-4:]
	if tail[0] != "service" || tail[2] != "operation" || tail[1] == "" || tail[3] == "" {
		return "", "", false
	}
	return tail[1], tail[3], true
}

// violation reports the first rule of the protocol r breaks, as the text
// of the answer.
func violation(r *http.Request) (string, bool) {
	for _, h := range []string{"X-Amz-Target", "X-Amzn-Target"} {
		if r.Header.Get(h) != "" {
			return fmt.Sprintf("The request carries both Smithy-Protocol: %s and %s; rpcv2Cbor requests must not carry %s, so send the request in one protocol only.", protocolName, h, h), true
		}
	}
	if r.Method != http.MethodPost {
		return fmt.Sprintf("rpcv2Cbor requests are POST; this request is %s.", r.Method), true
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !isCBOR(ct) {
		return fmt.Sprintf("rpcv2Cbor requests send Content-Type: %s; this request sends %q.", mediaType, ct), true
	}
	return "", false
}

// isCBOR reports whether the Content-Type essence is application/cbor,
// parameters ignored. A malformed parameter does not change the essence,
// so mime.ErrInvalidMediaParameter still matches on the media type.
func isCBOR(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return (err == nil || errors.Is(err, mime.ErrInvalidMediaParameter)) && mt == mediaType
}

// serviceNamed finds the service of reg that speaks rpcv2Cbor and whose
// shape is named name, or whose absolute shape id is name with a dot in
// place of #.
func serviceNamed(reg *registry.Registry, name string) (*registry.Service, bool) {
	for _, svc := range reg.Services() {
		if !slices.Contains(svc.Protocols, ref.RPCv2CBOR) {
			continue
		}
		if name == svc.Shape.Name() || name == svc.Shape.Namespace()+"."+svc.Shape.Name() {
			return svc, true
		}
	}
	return nil, false
}
