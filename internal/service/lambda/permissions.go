package lambda

import (
	"context"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// maxPolicyBytes is the resource policy size limit (lambda-api.md 7.3).
const maxPolicyBytes = 20_480

func (s *Service) bindPermissions(b lambdaapi.Binder) {
	b.AddPermission(s.addPermission, stored("statements are stored and returned by GetPolicy, not enforced"))
	b.GetPolicy(s.getPolicy, full())
	b.RemovePermission(s.removePermission, full())
}

func (s *Service) addPermission(_ context.Context, c *kernel.Call, in *lambdaapi.AddPermissionInput) (*lambdaapi.AddPermissionOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	if ref.qualifier == latest {
		return nil, errLatestPolicy()
	}
	principal, err := parsePrincipal(ref.scope, in.Principal)
	if err != nil {
		return nil, err
	}
	statement := policydoc.Statement{
		Sid:       in.StatementId,
		Effect:    policydoc.Allow,
		Principal: principal,
		Action:    policydoc.Action{in.Action},
		Resource:  policydoc.Resource{ref.qualifiedARN()},
		Condition: conditions(in),
	}
	text := statementJSON(statement)
	out, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (string, *awserr.Error) {
		f, err := st.policyTarget(ref)
		if err != nil {
			return "", err
		}
		if err := checkPolicyRevision(f, ref.qualifier, in.RevisionId); err != nil {
			return "", err
		}
		p := f.Policies[ref.qualifier]
		if slices.ContainsFunc(p.Statements, func(s Statement) bool { return s.Sid == in.StatementId }) {
			return "", errStatementExists(in.StatementId)
		}
		p.Statements = append(slices.Clone(p.Statements), Statement{Sid: in.StatementId, JSON: text})
		if size := len(p.document()); size > maxPolicyBytes {
			return "", errPolicyTooLarge(size)
		}
		f = f.withRevision(ref.qualifier, st.revision(ref.scope))
		if f.Policies == nil {
			f.Policies = map[string]Policy{}
		}
		f.Policies[ref.qualifier] = p
		st.Functions[ref.name] = f
		return text, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &lambdaapi.AddPermissionOutput{Statement: new(out)}, nil
}

// checkPolicyRevision is AddPermission's and RemovePermission's RevisionId
// precondition: rev, when given, must be the RevisionId of the policy's
// target, $LATEST for the unqualified function, the version or the alias
// (revisionOf).
func checkPolicyRevision(f Function, qualifier string, rev *string) *awserr.Error {
	if rev == nil {
		return nil
	}
	if current, ok := f.revisionOf(qualifier); !ok || *rev != current {
		return errPolicyRevisionMismatch()
	}
	return nil
}

// policyTarget finds the function AddPermission names, as lookup does,
// except that an alias the function lacks is "Cannot find alias arn: <arn>"
// (test_permission_exceptions).
func (st *State) policyTarget(ref functionRef) (Function, *awserr.Error) {
	if f, ok := st.Functions[ref.name]; ok {
		if err := f.missingAlias(ref); err != nil {
			return Function{}, err
		}
	}
	f, _, err := st.lookup(ref)
	return f, err
}

// missingAlias is "Cannot find alias arn: <arn>" when ref's qualifier
// names an alias f does not have; nil otherwise. A qualifier that cannot
// be an alias name is left to lookup, as AWS answers it "Function not
// found" (test_permission_exceptions).
func (f Function) missingAlias(ref functionRef) *awserr.Error {
	if _, ok := f.Aliases[ref.qualifier]; isAliasName(ref.qualifier) && !ok {
		return errAliasNotFound(ref.aliasARN(ref.qualifier))
	}
	return nil
}

// parsePrincipal renders AddPermission's Principal as Lambda stores it
// (lambda-api.md 7.3): "*" as is, a service principal under Service, an
// account id as its root ARN and an IAM ARN under AWS.
func parsePrincipal(scope aws.Scope, p string) (policydoc.PrincipalElement, *awserr.Error) {
	switch {
	case p == "*":
		return policydoc.Principal{Who: policydoc.Anyone{}}, nil
	case len(p) == 12 && isVersionNumber(p):
		return policydoc.Principal{Who: policydoc.Named{AWS: []string{aws.RootARN(scope.Partition, p).String()}}}, nil
	case strings.HasPrefix(p, "arn:"):
		if a, err := aws.ParseARN(p); err == nil && (a.Service == "iam" || a.Service == "sts") {
			return policydoc.Principal{Who: policydoc.Named{AWS: []string{p}}}, nil
		}
	case isServicePrincipal(p):
		return policydoc.Principal{Who: policydoc.Named{Service: []string{p}}}, nil
	}
	return nil, errInvalidPrincipal()
}

// isServicePrincipal reports p as a DNS name under an AWS service domain:
// amazonaws.com (amazonaws.com.cn for some China services) or amazon.com
// for the Alexa principals Lambda documents. Any other dotted name, such
// as "invalid.nonaws.com", is a malformed principal
// (test_permission_exceptions).
func isServicePrincipal(p string) bool {
	if strings.ContainsAny(p, ":/") {
		return false
	}
	for _, d := range []string{".amazonaws.com", ".amazonaws.com.cn", ".amazon.com"} {
		if name, ok := strings.CutSuffix(p, d); ok && name != "" {
			return true
		}
	}
	return false
}

// conditions are the condition clauses the optional members ask for; a
// statement carries none the request did not (lambda-api.md 7.3).
func conditions(in *lambdaapi.AddPermissionInput) policydoc.Condition {
	var c policydoc.Condition
	add := func(test policydoc.Test, key string, value *string) {
		if value != nil {
			c = append(c, policydoc.Clause{Operator: policydoc.Operator{Test: test}, Key: key, Values: []policydoc.Value{policydoc.StringValue(*value)}})
		}
	}
	add(policydoc.StringEquals, "AWS:SourceAccount", in.SourceAccount)
	add(policydoc.ArnLike, "AWS:SourceArn", in.SourceArn)
	add(policydoc.StringEquals, "aws:PrincipalOrgID", in.PrincipalOrgID)
	add(policydoc.StringEquals, "lambda:EventSourceToken", in.EventSourceToken)
	if in.FunctionUrlAuthType != nil {
		add(policydoc.StringEquals, "lambda:FunctionUrlAuthType", new(string(*in.FunctionUrlAuthType)))
	}
	if in.InvokedViaFunctionUrl != nil && *in.InvokedViaFunctionUrl {
		add(policydoc.Bool, "lambda:InvokedViaFunctionUrl", new("true"))
	}
	return c
}

// statementJSON is one statement as Lambda writes it: policydoc's
// canonical element order, with one-element lists as strings.
func statementJSON(st policydoc.Statement) string {
	doc := string(policydoc.Encode(policydoc.Document{Statements: []policydoc.Statement{st}}))
	return strings.TrimSuffix(strings.TrimPrefix(doc, `{"Statement":[`), `]}`)
}

// document is the policy GetPolicy returns, with its statements in
// insertion order.
func (p Policy) document() string {
	parts := make([]string, len(p.Statements))
	for i, st := range p.Statements {
		parts[i] = st.JSON
	}
	return `{"Version":"2012-10-17","Id":"default","Statement":[` + strings.Join(parts, ",") + `]}`
}

func (s *Service) getPolicy(_ context.Context, c *kernel.Call, in *lambdaapi.GetPolicyInput) (*lambdaapi.GetPolicyOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	// A missing function, version or alias reads as a missing policy
	// (test_permission_exceptions).
	return view(s, ref.scope, func(st *State) (*lambdaapi.GetPolicyOutput, *awserr.Error) {
		f, _, err := st.lookup(ref)
		if err != nil {
			return nil, errResourceNotFound()
		}
		p, ok := f.Policies[ref.qualifier]
		if !ok || len(p.Statements) == 0 {
			return nil, errResourceNotFound()
		}
		rev, _ := f.revisionOf(ref.qualifier)
		return &lambdaapi.GetPolicyOutput{Policy: new(p.document()), RevisionId: new(rev)}, nil
	})
}

func (s *Service) removePermission(_ context.Context, c *kernel.Call, in *lambdaapi.RemovePermissionInput) (*lambdaapi.RemovePermissionOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, in.Qualifier)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		f, ok := st.Functions[ref.name]
		if !ok {
			return struct{}{}, errNoPolicyFound(ref.qualifiedARN())
		}
		if err := f.missingAlias(ref); err != nil {
			return struct{}{}, err
		}
		p, ok := f.Policies[ref.qualifier]
		if !ok || len(p.Statements) == 0 {
			return struct{}{}, errNoPolicy()
		}
		i := slices.IndexFunc(p.Statements, func(s Statement) bool { return s.Sid == in.StatementId })
		if i < 0 {
			return struct{}{}, errStatementNotFound(in.StatementId)
		}
		if err := checkPolicyRevision(f, ref.qualifier, in.RevisionId); err != nil {
			return struct{}{}, err
		}
		f = f.withRevision(ref.qualifier, st.revision(ref.scope))
		p.Statements = slices.Delete(slices.Clone(p.Statements), i, i+1)
		if len(p.Statements) == 0 {
			delete(f.Policies, ref.qualifier)
		} else {
			f.Policies[ref.qualifier] = p
		}
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.qualifiedARN())
	return &lambdaapi.RemovePermissionOutput{}, nil
}
