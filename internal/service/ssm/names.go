package ssm

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
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

// Name limits (ssm-parameters.md 3.4).
const (
	// maxLevels is the deepest hierarchy, counting the name's own level.
	// A 16-level name is refused too. UNVERIFIED (SSM-05),
	// ssm-parameters.md 3.4: the docs show only 15 accepted and 17 refused.
	maxLevels = 15
	// maxARNLength is the longest parameter ARN: the 1011 characters a
	// name may have "includes the characters in the ARN that precede the
	// name". UNVERIFIED (SSM-06), ssm-parameters.md 3.4.
	maxARNLength = 1011
)

// pathRule is the text AWS answers for a path defect, as both emulators
// render it (ssm-parameters.md 3.1 [E]; the R-weak assertion requires
// "sub-paths divided by slash symbol").
const pathRule = "If formed as a path, it can consist of sub-paths divided by slash symbol; " +
	"each sub-path can be formed as a mix of letters, numbers and the following 3 symbols .-_"

// parseName applies the naming rules of ssm-parameters.md 3.1 and 3.4 to a
// name, never an ARN, in this order: outer spaces trimmed, the character
// set, the aws/ssm prefix of the first level, the hierarchy's form, its
// depth, and the length of the parameter's ARN in scope. Every refusal is
// SSM's own ValidationException (spec conflict 1), except the depth's
// HierarchyLevelLimitExceededException. The message texts are understudy's
// own. UNVERIFIED (SSM-04), ssm-parameters.md 3.1: AWS's code and text for
// each defect, and that the trimmed form is stored and echoed.
func parseName(raw string, scope aws.Scope) (name, *awserr.Error) {
	spelling := strings.Trim(raw, " ")
	if spelling == "" {
		return name{}, ssmapi.ErrValidationException("Parameter name is empty once its outer spaces are trimmed. " +
			"A parameter name can include only a-zA-Z0-9_.- and the slash that divides hierarchy levels.")
	}
	if i := strings.IndexFunc(spelling, func(r rune) bool { return !nameRune(r) }); i >= 0 {
		bad, _ := utf8.DecodeRuneInString(spelling[i:])
		why := fmt.Sprintf("it contains %q", bad)
		if bad == ' ' {
			why = "it contains a space between characters"
		}
		return name{}, ssmapi.ErrValidationException(fmt.Sprintf("Parameter name %q is not valid: %s. "+
			"A parameter name can include only a-zA-Z0-9_.- and the slash that divides hierarchy levels.", spelling, why))
	}
	rest, rooted := strings.CutPrefix(spelling, "/")
	first, _, _ := strings.Cut(rest, "/")
	if l := strings.ToLower(first); strings.HasPrefix(l, "aws") || strings.HasPrefix(l, "ssm") {
		// The prefix test on the first level is read literally from the
		// docs, so awsome is refused too. UNVERIFIED (SSM-04),
		// ssm-parameters.md 3.1.
		return name{}, ssmapi.ErrValidationException(fmt.Sprintf(
			"Parameter name %q is not valid: its first level %q begins with \"aws\" or \"ssm\" (case-insensitive), which Systems Manager reserves.",
			spelling, first))
	}
	switch {
	case !rooted && strings.Contains(rest, "/"):
		return name{}, errPath(spelling, "a name with more than one level must begin with a slash")
	case rest == "" || strings.Contains(rest, "//"):
		return name{}, errPath(spelling, "it has an empty level")
	case strings.HasSuffix(rest, "/"):
		// A trailing slash is refused as an empty last level.
		// UNVERIFIED (SSM-04), ssm-parameters.md 3.1.
		return name{}, errPath(spelling, "it ends with a slash")
	}
	if levels := strings.Count(rest, "/") + 1; levels > maxLevels {
		return name{}, ssmapi.ErrHierarchyLevelLimitExceededException(fmt.Sprintf(
			"A hierarchy can have a maximum of %d levels. Parameter name %q has %d.", maxLevels, spelling, levels))
	}
	canonical := spelling
	if !strings.Contains(rest, "/") {
		canonical = rest
	}
	if n := len(parameterARN(scope, canonical).String()); n > maxARNLength {
		return name{}, ssmapi.ErrValidationException(fmt.Sprintf(
			"Parameter name %q is too long: its ARN would be %d characters, and a parameter ARN can have at most %d.",
			spelling, n, maxARNLength))
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

func errPath(spelling, why string) *awserr.Error {
	return ssmapi.ErrValidationException(fmt.Sprintf("Parameter name %q is not valid: %s. %s", spelling, why, pathRule))
}

// parseNameOrARN is parseName for the operations that also take a
// parameter ARN (ssm-parameters.md 3.1): GetParameter, GetParameters and
// GetParameterHistory. Of arn:<partition>:ssm:<region>:<account>:parameter/<rest>
// the name is /<rest> when rest holds another slash, else rest
// (ssm-parameters.md 3.3, Terraform's rule), and the reads echo that name,
// not the ARN (localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json:L2-L15, L42-L56).
func parseNameOrARN(raw string, scope aws.Scope) (name, *awserr.Error) {
	trimmed := strings.Trim(raw, " ")
	if !strings.HasPrefix(trimmed, "arn:") {
		return parseName(raw, scope)
	}
	a, err := aws.ParseARN(trimmed)
	rest, isParameter := strings.CutPrefix(a.Resource, "parameter/")
	if err != nil || a.Service != "ssm" || !isParameter {
		// UNVERIFIED (SSM-29), ssm-parameters.md 3.3: AWS's answer to an
		// ARN of another SSM resource type or a malformed one.
		return name{}, ssmapi.ErrValidationException(fmt.Sprintf(
			"%q is not a parameter ARN: the form is arn:<partition>:ssm:<region>:<account>:parameter/<name>.", trimmed))
	}
	base := rest
	if strings.Contains(rest, "/") {
		base = "/" + rest
	}
	n, aerr := parseName(base, scope)
	if aerr != nil {
		return name{}, aerr
	}
	if a.Partition != scope.Partition.String() || a.Region != scope.Region.String() || a.Account != scope.Account.String() {
		// An ARN of another account or Region names no parameter this
		// call can see. UNVERIFIED (SSM-29), ssm-parameters.md 3.3.
		return name{}, errNotFound()
	}
	return n, nil
}

// canonicalOf is the canonical form of a name, valid or not, for comparing
// filter values with stored names (ssm-parameters.md 3.2).
func canonicalOf(raw string) string {
	spelling := strings.Trim(raw, " ")
	if rest, ok := strings.CutPrefix(spelling, "/"); ok && !strings.Contains(rest, "/") {
		return rest
	}
	return spelling
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

// reservedNamespaces are the namespaces understudy does not serve (spec
// "Scope"; ssm-parameters.md 8.1, 8.2, 12.1): AWS public parameters and
// Secrets Manager references.
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
