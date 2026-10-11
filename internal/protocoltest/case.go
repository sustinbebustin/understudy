package protocoltest

import (
	"cmp"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Kind is the trait a compliance case comes from.
type Kind string

// The three kinds of server-runnable case (smithy-protocol-tests.md 2).
const (
	// Request is an httpRequestTests case: the server decodes the request
	// into the case's params.
	Request Kind = "request"
	// Response is an httpResponseTests case: the server encodes the case's
	// params into the case's response.
	Response Kind = "response"
	// Malformed is an expanded httpMalformedRequestTests case: the server
	// rejects the request before its handler runs.
	Malformed Kind = "malformed"
)

// Kinds returns every kind in report order.
func Kinds() []Kind { return []Kind{Request, Response, Malformed} }

// parseKind returns the kind spelled s.
func parseKind(s string) (Kind, bool) {
	for _, k := range Kinds() {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// Key names one case. Smithy ids are unique within a trait, not across
// the request and response traits, so the kind is part of the name.
type Key struct {
	Protocol ref.ProtocolID
	Kind     Kind
	ID       string
}

func (k Key) String() string {
	return fmt.Sprintf("%s %s %s", k.Protocol, k.Kind, k.ID)
}

// compareKeys orders keys by protocol, kind and id.
func compareKeys(a, b Key) int {
	return cmp.Or(cmp.Compare(a.Protocol, b.Protocol), cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)), cmp.Compare(a.ID, b.ID))
}

func kindRank(k Kind) int {
	for i, x := range Kinds() {
		if x == k {
			return i
		}
	}
	return len(Kinds())
}

// Result is the outcome of running one case: Failure is nil when it
// passed.
type Result struct {
	Key     Key
	Failure error
}
