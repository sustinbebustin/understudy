package lambda

import (
	"strconv"
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

// parseFunctionRef reads name in any of its accepted forms: a bare name,
// name:qualifier, a partial ARN (123456789012:function:name[:qualifier])
// or a full ARN. An ARN of another region answers "Functions from '<r>'
// are not reachable in this region"; one of another account
// AccessDeniedException. A qualifier in the name that differs from the
// Qualifier member is "The derived qualifier from the function name does
// not match the specified qualifier.".
func parseFunctionRef(c *kernel.Call, name string, qualifier *string) (functionRef, *awserr.Error) {
	scope := c.Scope()
	ref := functionRef{scope: scope}
	rest := name
	switch {
	case strings.HasPrefix(name, "arn:"):
		a, err := aws.ParseARN(name)
		fn, ok := strings.CutPrefix(a.Resource, "function:")
		if err != nil || a.Service != "lambda" || !ok {
			return functionRef{}, errFunctionNotFound(name)
		}
		if a.Region != scope.Region.String() {
			return functionRef{}, errNotReachable(a.Region, scope.Region.String())
		}
		if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: a, Mismatch: errOtherAccount()}); err != nil {
			return functionRef{}, err
		}
		rest = fn
	case isPartialARN(name):
		account, fn, _ := strings.Cut(name, ":function:")
		a := aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: account, Resource: "function:" + fn}
		if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: a, Mismatch: errOtherAccount()}); err != nil {
			return functionRef{}, err
		}
		rest = fn
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

// isPartialARN reports the 123456789012:function:name form.
func isPartialARN(s string) bool {
	account, _, ok := strings.Cut(s, ":function:")
	if !ok || len(account) != 12 {
		return false
	}
	_, err := strconv.ParseUint(account, 10, 64)
	return err == nil
}

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

// scopeOf reads the account and region of an ARN this service minted,
// as timer ids carry them.
func scopeOf(a aws.ARN) (aws.Scope, bool) {
	account, err := aws.ParseAccountID(a.Account)
	if err != nil {
		return aws.Scope{}, false
	}
	var region aws.Region
	if err := region.UnmarshalText([]byte(a.Region)); err != nil {
		return aws.Scope{}, false
	}
	return aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: region}, true
}

// lookup finds the function a reference names and the version its
// qualifier selects, following an alias to its version. The alias name is
// returned when the qualifier is one.
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
