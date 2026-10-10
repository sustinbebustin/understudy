package awsjson

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// Claim is the codec's answer to whether a request is an awsJson request of
// its version, and for which operation.
//
//sumtype:decl
type Claim interface{ isClaim() }

// Claimed names the modeled operation the request calls.
type Claimed struct {
	Op *registry.Operation
}

// UnknownOperation is a request whose X-Amz-Target names a service that
// speaks this version but no modeled operation of it (an internal or raw
// operation counts as unknown, since the edge never claims those). The
// service's unknownOperationError answers it.
type UnknownOperation struct {
	Service *registry.Service
	// Operation is the operation name the target carried, possibly empty.
	Operation string
}

// Declined is a request this codec does not serve; Reason says why, for the
// journal's Unclaimed event and for the claim rules after this one.
type Declined struct {
	Reason string
}

func (Claimed) isClaim()          {}
func (UnknownOperation) isClaim() {}
func (Declined) isClaim()         {}

// Claim decides whether r is an awsJson request of c's version
// (ARCHITECTURE.md 4.1 rule 6): a POST whose X-Amz-Target is
// <prefix>.<Operation>, whose prefix names a service in reg that speaks
// this version, and whose Content-Type essence is this version's media
// type or application/json, parameters such as charset ignored. The path
// is not checked: SDKs with a base path in the endpoint, and the PHP SDK
// posting JSON to a queue URL, send other paths, and AWS routes on the
// target (sqs.md 2.1). Cross-checking the service against the SigV4
// signing name is the edge's job.
func (c Codec) Claim(r *http.Request, reg *registry.Registry) Claim {
	target := r.Header.Get("X-Amz-Target")
	if target == "" {
		return Declined{Reason: "the request has no X-Amz-Target header"}
	}
	if r.Method != http.MethodPost {
		return Declined{Reason: fmt.Sprintf("X-Amz-Target is set but the method is %s; awsJson requests are POST", r.Method)}
	}
	if !c.acceptsContentType(r.Header.Get("Content-Type")) {
		return Declined{Reason: fmt.Sprintf("X-Amz-Target is set but Content-Type %q is neither %s nor %s", r.Header.Get("Content-Type"), c.Version.mediaType(), plainJSON)}
	}
	// Service target prefixes never contain a dot, so a dotted remainder is
	// an unknown operation of the service, as DynamoDB answers it
	// (error-matrix.md 5.5 T5).
	prefix, name, _ := strings.Cut(target, ".")
	svc, ok := reg.ByJSONTarget(prefix)
	if !ok {
		return Declined{Reason: fmt.Sprintf("X-Amz-Target prefix %q names no service", prefix)}
	}
	if !slices.Contains(svc.Protocols, c.Protocol()) {
		return Declined{Reason: fmt.Sprintf("service %s does not speak %s", svc.ID, c.Protocol())}
	}
	op, ok := svc.Operation(name)
	if !ok || op.Kind != registry.Modeled {
		return UnknownOperation{Service: svc, Operation: name}
	}
	return Claimed{Op: op}
}

// plainJSON is the generic JSON media type, which AWS's JSON front end
// takes as well as the version's own (error-matrix.md 5.5 T6).
const plainJSON = "application/json"

// acceptsContentType reports whether the Content-Type essence is the
// version's media type or application/json. A malformed parameter does not
// change the essence, so mime.ErrInvalidMediaParameter still matches on
// the media type ParseMediaType returns with it.
func (c Codec) acceptsContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return false
	}
	own := c.Version.mediaType()
	return own != "" && (mediaType == own || mediaType == plainJSON)
}
