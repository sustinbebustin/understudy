package lambda

import (
	"regexp"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// functionRef is a FunctionName member read from the caller's point of
// view (lambda-api.md 3): the scope it names, the bare function name and
// the qualifier, from the name or the Qualifier member.
type functionRef struct {
	scope aws.Scope
	name  string
	// qualifier is "$LATEST", a version number, an alias name, or "" for
	// the unqualified function.
	qualifier string
}

// parseFunctionRef reads name in any of its accepted forms: a full ARN, or
// the model's partial grammar [region:][account:][function:]name[:qualifier]
// (lambda-api.md 3), such as 123456789012:function:name. A name of another
// region answers "Functions from '<r>' are not reachable in this region";
// one of another account AccessDeniedException. A qualifier in the name
// that differs from the Qualifier member is "The derived qualifier from the
// function name does not match the specified qualifier.".
func parseFunctionRef(c *kernel.Call, name string, qualifier *string) (functionRef, *awserr.Error) {
	scope := c.Scope()
	ref := functionRef{scope: scope}
	a, rest, err := splitFunctionName(scope, name)
	if err != nil {
		return functionRef{}, err
	}
	if a.Region != scope.Region.String() {
		return functionRef{}, errNotReachable(a.Region, scope.Region.String())
	}
	if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: a, Mismatch: errOtherAccount()}); err != nil {
		return functionRef{}, err
	}
	ref.name, ref.qualifier, _ = strings.Cut(rest, ":")
	if qualifier != nil {
		if ref.qualifier != "" && ref.qualifier != *qualifier {
			return functionRef{}, errQualifierMismatch()
		}
		ref.qualifier = *qualifier
	}
	return ref, nil
}

// splitFunctionName returns the function ARN name points at, its region
// and account defaulting to scope's, and the name[:qualifier] it ends
// with. The prefixes of a partial name are optional each on its own, so
// a region or account segment is read only before at least two more
// segments: "fn:1" stays name:qualifier, while "function:" is a prefix
// wherever it leads, as in the model's left-to-right grammar.
func splitFunctionName(scope aws.Scope, name string) (aws.ARN, string, *awserr.Error) {
	if strings.HasPrefix(name, "arn:") {
		a, err := aws.ParseARN(name)
		fn, ok := strings.CutPrefix(a.Resource, "function:")
		if err != nil || a.Service != "lambda" || !ok {
			return aws.ARN{}, "", errFunctionNotFound(name)
		}
		return a, fn, nil
	}
	a := aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: scope.Account.String()}
	segments := strings.Split(name, ":")
	if len(segments) > 2 && regionPrefix.MatchString(segments[0]) {
		a.Region, segments = segments[0], segments[1:]
	}
	if _, err := aws.ParseAccountID(segments[0]); err == nil && len(segments) > 2 {
		a.Account, segments = segments[0], segments[1:]
	}
	if len(segments) > 1 && segments[0] == "function" {
		segments = segments[1:]
	}
	rest := strings.Join(segments, ":")
	a.Resource = "function:" + rest
	return a, rest, nil
}

// passedRole is the execution role a configuration write passes, which
// iam:PassRole acts on; nothing when it passes none or the value is not an
// IAM ARN, which validation answers.
func passedRole(role *string) []aws.ARN {
	if role == nil {
		return nil
	}
	a, err := aws.ParseARN(*role)
	if err != nil || a.Service != "iam" {
		return nil
	}
	return []aws.ARN{a}
}

// regionPrefix is the region segment of the FunctionName grammar
// (lambda-api.md 3).
var regionPrefix = regexp.MustCompile(`^(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d$`)

// unqualified refuses a qualified reference for an operation that applies
// to the function as a whole.
func (r functionRef) unqualified(refusal *awserr.Error) *awserr.Error {
	if r.qualifier != "" && r.qualifier != latest {
		return refusal
	}
	return nil
}

// arn is the unqualified function ARN.
func (r functionRef) arn() string { return functionARN(r.scope, r.name) }

// qualifiedARN is the ARN as the reference names it: with its qualifier,
// when it has one.
func (r functionRef) qualifiedARN() string {
	if r.qualifier == "" {
		return r.arn()
	}
	return r.arn() + ":" + r.qualifier
}

// notFound is the error for a reference naming no function or no such
// version or alias: "Function not found: <arn>", qualified as given.
func (r functionRef) notFound() *awserr.Error { return errFunctionNotFound(r.qualifiedARN()) }

// functionARN is the unqualified ARN of the function name in scope.
func functionARN(scope aws.Scope, name string) string {
	return aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "function:" + name}.String()
}

// mappingARN is the ARN of the event source mapping uuid in scope.
func mappingARN(scope aws.Scope, uuid string) string {
	return aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "event-source-mapping:" + uuid}.String()
}

// lookup finds the function a reference names and the version its
// qualifier selects, following an alias to its version.
func (st *State) lookup(r functionRef) (Function, Version, *awserr.Error) {
	f, ok := st.Functions[r.name]
	if !ok {
		return Function{}, Version{}, r.notFound()
	}
	switch q := r.qualifier; {
	case q == "":
		return f, f.Latest, nil
	case isVersionNumber(q) || q == latest:
		v, ok := f.version(q)
		if !ok {
			return Function{}, Version{}, r.notFound()
		}
		return f, v, nil
	default:
		a, ok := f.Aliases[q]
		if !ok {
			return Function{}, Version{}, r.notFound()
		}
		v, ok := f.version(a.Version)
		if !ok {
			return Function{}, Version{}, r.notFound()
		}
		return f, v, nil
	}
}

// function finds the function a reference names, ignoring its qualifier.
func (st *State) function(r functionRef) (Function, *awserr.Error) {
	f, ok := st.Functions[r.name]
	if !ok {
		return Function{}, errFunctionNotFound(r.arn())
	}
	return f, nil
}

// isAliasName reports a qualifier of an alias name's form: letters,
// digits, '-' and '_', not all digits.
func isAliasName(q string) bool {
	if q == "" || isVersionNumber(q) {
		return false
	}
	for _, r := range q {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// isVersionNumber reports a published version number: decimal digits.
func isVersionNumber(q string) bool {
	if q == "" {
		return false
	}
	for _, r := range q {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
