package ssm

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// name is a parameter name as one request gave it.
type name struct {
	// canonical is the key in State.Parameters: a single-level name
	// without its leading slash, a hierarchical name as written
	// (ssm-parameters.md 3.2).
	canonical string
	// spelling is the request's form after trimming, which reads echo
	// (ssm-parameters.md 3.2). For a name given as an ARN it is the name
	// the ARN carries.
	spelling string
}

// asker is who a name is read for: the call's scope, and the caller and
// ssm: action that AWS names when it refuses a name as a denial
// (ssm-parameters.md 3.3, 3.4).
type asker struct {
	scope  aws.Scope
	caller identity.Principal
	action string
}

// askerOf is the asker of call c.
func askerOf(c *kernel.Call) asker {
	return asker{scope: c.Scope(), caller: c.Principal(), action: "ssm:" + c.OperationID().Name}
}

// denied is the AccessDeniedException AWS answers for a parameter ARN the
// caller may not reach whatever its policies say, in the IAM grammar with
// the resource named and reason appended when there is one: none for an
// ARN past the length limit, "because no resource-based policy allows the
// <action> action" for another account's parameter (ssm-parameters.md 3.3,
// 3.4; SSM-06 step 02, SSM-29 step 01).
func (a asker) denied(arn, reason string) *awserr.Error {
	return ssmapi.ErrAccessDeniedException("User: " + identity.Render(a.caller) + " is not authorized to perform: " +
		a.action + " on resource: " + arn + reason)
}

// Name limits (ssm-parameters.md 3.4).
const (
	// maxLevels is the deepest hierarchy, counting the name's own level:
	// 16 levels are accepted and 17 refused (SSM-05), the "depth of the
	// tree" of AWS's message being the levels above the name.
	maxLevels = 16
	// maxARNLength is the longest parameter ARN: the 1011 characters a
	// name may have "includes the characters in the ARN that precede the
	// name" (SSM-06: 1011 accepted, 1012 denied).
	maxARNLength = 1011
)

// AWS's texts for name defects (ssm-parameters.md 3.1; SSM-01 step 03,
// SSM-04).
const (
	// textPath answers a character outside the set, an empty level and a
	// hierarchical name whose first level begins with ssm.
	textPath = `Parameter name: can't be prefixed with "ssm" (case-insensitive). If formed as a path, ` +
		"it can consist of sub-paths divided by slash symbol; each sub-path can be formed as a mix of letters, numbers and the following 3 symbols .-_"
	textNotQualified  = "Parameter name must be a fully qualified name."
	textTrailingSlash = "Parameter name must not end with slash."
	textPrefixed      = `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`
	textDepth         = "Depth of the tree cannot exceed 15"
)

// parseName applies the naming rules of ssm-parameters.md 3.1 and 3.4 to a
// name, never an ARN, with AWS's code and text for each defect recorded in
// SSM-04, SSM-05 and SSM-06, in this order: outer spaces trimmed (stored
// and echoed trimmed), the character set, a slash without a leading slash
// (PutParameter's own text; the path text elsewhere),
// the reserved first level /aws/ (AccessDeniedException), an aws or ssm
// prefix of the first level, an empty level, a trailing slash, the depth
// (HierarchyLevelLimitExceededException) and the length of the parameter's
// ARN (AccessDeniedException). UNVERIFIED (SSM-04), ssm-parameters.md 3.1:
// the order when a name has two defects, a name empty once trimmed (whose
// text is understudy's own), and whether /AWS/ in another case is
// reserved too; understudy reserves it in any case.
func parseName(raw string, a asker) (name, *awserr.Error) {
	spelling := strings.Trim(raw, " ")
	if spelling == "" {
		return name{}, ssmapi.ErrValidationException("Parameter name is empty once its outer spaces are trimmed. " +
			"A parameter name can include only a-zA-Z0-9_.- and the slash that divides hierarchy levels.")
	}
	if strings.IndexFunc(spelling, func(r rune) bool { return !nameRune(r) }) >= 0 {
		return name{}, ssmapi.ErrValidationException(textPath)
	}
	rest, rooted := strings.CutPrefix(spelling, "/")
	first, _, hierarchical := strings.Cut(rest, "/")
	lower := strings.ToLower(first)
	switch {
	case !rooted && hierarchical && a.action == "ssm:PutParameter":
		return name{}, ssmapi.ErrValidationException(textNotQualified)
	case !rooted && hierarchical:
		// GetParameter and GetParameters answer the path text (3.1
		// [R-weak]). UNVERIFIED (SSM-04): the other operations' answer.
		return name{}, ssmapi.ErrValidationException(textPath)
	case hierarchical && lower == "aws":
		// UNVERIFIED (SSM-04), ssm-parameters.md 3.1: this text is
		// recorded for PutParameter only; GetParameter's answer under
		// /aws/service/ (SSM-02 step 05) carries the namespace text
		// `No access to "/aws/" namespace: ...`, so the other operations'
		// text may differ.
		return name{}, ssmapi.ErrAccessDeniedException("No access to reserved parameter name: " + rest + ".")
	case hierarchical && (strings.HasPrefix(lower, "aws") || strings.HasPrefix(lower, "ssm")):
		return name{}, ssmapi.ErrValidationException(textPath)
	case strings.HasPrefix(lower, "aws") || strings.HasPrefix(lower, "ssm"):
		return name{}, ssmapi.ErrValidationException(textPrefixed)
	case rest == "" || strings.Contains(rest, "//"):
		return name{}, ssmapi.ErrValidationException(textPath)
	case strings.HasSuffix(rest, "/"):
		return name{}, ssmapi.ErrValidationException(textTrailingSlash)
	case strings.Count(rest, "/") >= maxLevels: // the levels above the name's own
		return name{}, ssmapi.ErrHierarchyLevelLimitExceededException(textDepth)
	}
	canonical := spelling
	if !hierarchical {
		canonical = rest
	}
	if arn := parameterARN(a.scope, canonical).String(); len(arn) > maxARNLength {
		return name{}, a.denied(arn, "")
	}
	return name{canonical: canonical, spelling: spelling}, nil
}

// nameRune reports a character a parameter name may hold: a-zA-Z0-9_.-
// and the slash (ssm-parameters.md 3.1).
func nameRune(r rune) bool {
	switch {
	case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		return true
	}
	return strings.ContainsRune("_.-/", r)
}

// parseNameOrARN is parseName for the operations that also take a
// parameter ARN (ssm-parameters.md 3.1): GetParameter, GetParameters,
// GetParameterHistory and, as ResourceId, the tag operations. Of
// arn:<partition>:ssm:<region>:<account>:parameter/<rest> the name is
// /<rest> when rest holds another slash, else rest (ssm-parameters.md 3.3,
// Terraform's rule), and the reads echo that name, not the ARN
// (localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json:L2-L15,
// L42-L56). An ARN of another resource type or Region answers
// ValidationException naming it, and one of another account
// AccessDeniedException, whatever the caller's policies (SSM-29).
// UNVERIFIED (SSM-29), ssm-parameters.md 3.3: the order of those checks, an
// ARN of another partition (refused as of another Region), and the answer
// to an ARN of another service or a malformed one (understudy's own text).
func parseNameOrARN(raw string, a asker) (name, *awserr.Error) {
	trimmed := strings.Trim(raw, " ")
	if !strings.HasPrefix(trimmed, "arn:") {
		return parseName(raw, a)
	}
	arn, err := aws.ParseARN(trimmed)
	if err != nil || arn.Service != "ssm" {
		return name{}, ssmapi.ErrValidationException(fmt.Sprintf(
			"%q is not a parameter ARN: the form is arn:<partition>:ssm:<region>:<account>:parameter/<name>.", trimmed))
	}
	kind, rest, _ := strings.Cut(arn.Resource, "/")
	scope := a.scope
	switch {
	case kind != "parameter":
		return name{}, ssmapi.ErrValidationException("Incorrect resource type " + kind + " in: " + trimmed)
	case arn.Partition != scope.Partition.String() || arn.Region != scope.Region.String():
		return name{}, ssmapi.ErrValidationException("Incorrect region in: " + trimmed)
	case arn.Account != scope.Account.String():
		return name{}, a.denied(trimmed, " because no resource-based policy allows the "+a.action+" action")
	}
	base := rest
	if strings.Contains(rest, "/") {
		base = "/" + rest
	}
	return parseName(base, a)
}

// parameterARN is the ARN of the parameter canonical in scope:
// parameter/<canonical name without its leading slash> (ssm-parameters.md
// 3.3).
func parameterARN(scope aws.Scope, canonical string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "ssm", Region: scope.Region.String(),
		Account: scope.Account.String(), Resource: "parameter/" + strings.TrimPrefix(canonical, "/"),
	}
}

// reservedNamespaces are the namespaces understudy does not serve
// (ssm-parameters.md 8.1, 8.2, 12.1): AWS public parameters and Secrets
// Manager references.
func reservedNamespaces() [2]struct{ prefix, feature string } {
	return [2]struct{ prefix, feature string }{
		{"/aws/service/", "AWS public parameters"},
		{"/aws/reference/secretsmanager/", "Secrets Manager references"},
	}
}

// checkReserved is the one boundary check every Parameter Store operation
// runs before any other hand-coded rule: a name, path, ARN or filter value
// under a reserved namespace makes the whole call answer 501 naming it,
// with nothing read or changed (ssm-parameters.md 12.1, 12.6: never
// ParameterNotFound, never a faked value). A value matches after trimming
// outer spaces and dropping a selector, with or without its leading slash,
// and as the resource of an ARN.
func checkReserved(op string, raws ...string) *awserr.Error {
	for _, raw := range raws {
		v := strings.Trim(raw, " ")
		if _, after, ok := strings.Cut(v, ":parameter/"); ok && strings.HasPrefix(v, "arn:") {
			v = after
		}
		v, _, _ = strings.Cut(v, ":")
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		for _, ns := range reservedNamespaces() {
			if strings.HasPrefix(v, ns.prefix) || v == strings.TrimSuffix(ns.prefix, "/") {
				return ssmapi.ErrNotImplemented(fmt.Sprintf(
					"understudy does not implement %s (names under %s) in ssm %s; nothing was read or changed. "+
						"Run 'understudy coverage --service ssm' for what is supported; to ask for it, see %s?q=ssm",
					ns.feature, ns.prefix, op, issuesURL))
			}
		}
	}
	return nil
}

// issuesURL is where users ask for what understudy does not implement.
const issuesURL = "https://github.com/sustinbebustin/understudy/issues"
