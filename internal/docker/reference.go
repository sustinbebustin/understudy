package docker

import (
	"fmt"
	"regexp"
)

// nameMaxLength is the longest repository name, registry included, that the
// Engine accepts.
const nameMaxLength = 255

// referencePattern is the image reference grammar of the Engine
// (distribution/reference): [registry[:port]/]path[:tag][@digest]. An image
// ID matches it too, as a bare hex name or as sha256:<hex>. No segment of a
// matching reference is "." or "..", so it is safe to place in a request
// path. Submatch 1 is the name.
var referencePattern = regexp.MustCompile(`^(` + refName + `)(?::` + refTag + `)?(?:@` + refDigest + `)?$`)

// The parts of referencePattern.
const (
	refAlphanumeric    = `[a-z0-9]+`
	refSeparator       = `(?:[._]|__|-+)`
	refPathComponent   = refAlphanumeric + `(?:` + refSeparator + refAlphanumeric + `)*`
	refDomainComponent = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
	refHost            = `(?:` + refDomainComponent + `(?:\.` + refDomainComponent + `)*|\[[a-fA-F0-9:]+\])`
	refDomain          = refHost + `(?::[0-9]+)?`
	refName            = `(?:` + refDomain + `/)?` + refPathComponent + `(?:/` + refPathComponent + `)*`
	refTag             = `[\w][\w.-]{0,127}`
	refDigest          = `[A-Za-z][A-Za-z0-9]*(?:[-_+.][A-Za-z][A-Za-z0-9]*)*:[a-fA-F0-9]{32,}`
)

// ValidateReference reports whether ref is an image reference or ID the
// Engine accepts, returning an *InvalidReferenceError when it is not. Image
// calls check their reference with it before anything is sent, so a
// caller-supplied reference can never steer the request path.
func ValidateReference(ref string) error {
	m := referencePattern.FindStringSubmatch(ref)
	if m == nil {
		return &InvalidReferenceError{Ref: ref, Reason: "it does not match [registry[:port]/]repository[:tag][@digest] with a lowercase repository"}
	}
	if len(m[1]) > nameMaxLength {
		return &InvalidReferenceError{Ref: ref, Reason: fmt.Sprintf("its repository name is %d characters, over the limit of %d", len(m[1]), nameMaxLength)}
	}
	return nil
}
