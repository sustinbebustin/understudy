package registry

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// family is a group of protocols one claim rule of ARCHITECTURE.md 4.1
// reads, and so the request field that tells two services of one signing
// name apart within it (ADR 0019).
type family uint8

const (
	// cborFamily is rpcv2Cbor (rule 5): the service shape in the path.
	cborFamily family = iota + 1
	// jsonFamily is awsJson 1.0 and 1.1 (rule 6): the X-Amz-Target prefix.
	jsonFamily
	// queryFamily is awsQuery (rule 7): the Version parameter.
	queryFamily
	// restFamily is restJson1 and restXml (rule 8): the route table.
	restFamily
)

// familyOf is the family protocol p belongs to; ref.Raw belongs to none,
// since raw operations are claimed by prefix and host rules alone.
func familyOf(p ref.ProtocolID) (family, bool) {
	switch p {
	case ref.RPCv2CBOR:
		return cborFamily, true
	case ref.AWSJSON10, ref.AWSJSON11:
		return jsonFamily, true
	case ref.AWSQuery:
		return queryFamily, true
	case ref.RESTJSON1, ref.RESTXML:
		return restFamily, true
	case ref.Raw:
	}
	return 0, false
}

// speaks reports whether s speaks a protocol of family f.
func (s *Service) speaks(f family) bool {
	for _, p := range s.Protocols {
		if got, ok := familyOf(p); ok && got == f {
			return true
		}
	}
	return false
}

// checkSigning refuses s when it shares its signing name with an earlier
// service and no claim rule tells their requests apart (ADR 0019). It runs
// after the JSON target and query indexes, whose own refusals of a shared
// prefix or (Action, Version) come first.
func (r *Registry) checkSigning(s *Service) error {
	if s.SigningName == "" {
		return nil
	}
	for _, other := range r.bySigning[s.SigningName] {
		if err := apart(s, other); err != nil {
			return err
		}
	}
	return nil
}

// apart checks that every protocol family both s and other speak tells
// their requests apart; a pair with no family in common is told apart by
// the request's shape, which picks the rule.
func apart(s, other *Service) error {
	same := func(why string) error {
		return &DescriptorError{
			Problem: DuplicateSigningName, Service: s.ID,
			Detail: fmt.Sprintf("signing name %q is also service %q's, and no claim rule tells their requests apart: %s", s.SigningName, other.ID, why),
		}
	}
	if len(s.Protocols) == 0 || len(other.Protocols) == 0 {
		return same("one of them declares no protocol")
	}
	for _, f := range []family{cborFamily, jsonFamily, queryFamily, restFamily} {
		if !s.speaks(f) || !other.speaks(f) {
			continue
		}
		switch f {
		case cborFamily:
			if s.Shape == other.Shape {
				return same(fmt.Sprintf("both speak rpcv2Cbor as shape %s", s.Shape))
			}
		case jsonFamily:
			// Equal non-empty prefixes were refused as DuplicateJSONTarget.
			if s.JSONTargetPrefix == other.JSONTargetPrefix {
				return same(fmt.Sprintf("both speak awsJson with X-Amz-Target prefix %q", s.JSONTargetPrefix))
			}
		case queryFamily:
			if s.QueryVersion == other.QueryVersion {
				return same(fmt.Sprintf("both speak awsQuery at Version %q", s.QueryVersion))
			}
		case restFamily:
			if err := restApart(s, other); err != nil {
				return err
			}
		}
	}
	return nil
}

// restApart checks that no request can match a route of both REST services'
// tables: rule 8 tries the candidates in registration order, so an overlap
// would leave a later service's operation unreachable. The check reads first
// path segments only, never methods (ADR 0019): each pair of routes, one
// from each table, must start with literals of different values, or one of
// them must be / and the other start with a literal. So `/v2/...` against
// `/restapis/...` is apart, and a table with a route starting with a label
// overlaps every other table that has a route.
func restApart(s, other *Service) error {
	for _, svc := range []*Service{s, other} {
		if svc.Routes == nil {
			return &DescriptorError{
				Problem: DuplicateSigningName, Service: s.ID,
				Detail: fmt.Sprintf("signing name %q is also service %q's, and no claim rule tells their requests apart: service %q speaks REST without a route table", s.SigningName, other.ID, svc.ID),
			}
		}
	}
	for i, a := range s.Routes.Routes {
		for j, b := range other.Routes.Routes {
			if !firstSegmentsMeet(a.Path, b.Path) {
				continue
			}
			return &DescriptorError{
				Problem: OverlappingRouteTables, Service: s.ID,
				Detail: fmt.Sprintf("signing name %q is also service %q's, and route %d (%s %s) of one can match a request that route %d (%s %s) of the other matches; give each table its own first literal path segment",
					s.SigningName, other.ID, i, a.Method, pathPattern(a.Path), j, b.Method, pathPattern(b.Path)),
			}
		}
	}
	return nil
}

// firstSegmentsMeet reports whether the first segments of two path patterns
// could match one request segment: either is a label or a greedy label, the
// two are equal literals, or neither pattern has a segment (both match /).
// The pattern / against one starting with a literal never meets.
func firstSegmentsMeet(a, b []Segment) bool {
	fa, fb := firstSegment(a), firstSegment(b)
	la, aLiteral := fa.(Literal)
	lb, bLiteral := fb.(Literal)
	switch {
	case fa == nil && fb == nil:
		return true
	case fa != nil && !aLiteral, fb != nil && !bLiteral:
		return true
	case aLiteral && bLiteral:
		return la.Value == lb.Value
	}
	return false
}

// firstSegment is a path pattern's first segment, nil for the pattern /.
func firstSegment(path []Segment) Segment {
	if len(path) == 0 {
		return nil
	}
	return path[0]
}

// pathPattern renders a path pattern as the http trait writes it, literals
// decoded: /restapis/{restapi_id}/deployments.
func pathPattern(path []Segment) string {
	if len(path) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, s := range path {
		b.WriteByte('/')
		switch s := s.(type) {
		case Literal:
			b.WriteString(s.Value)
		case Label:
			b.WriteString("{" + s.Name + "}")
		case Greedy:
			b.WriteString("{" + s.Name + "+}")
		}
	}
	return b.String()
}
