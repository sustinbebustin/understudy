package admin

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/enforce"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// iamEvaluate decides (action, resource) pairs for a principal as
// iam.enforce would, under every mode: the stored identity-based policies
// or the request's replacements, the stored permissions boundary and
// resource policies. Nothing is journaled.
func (h *Handler) iamEvaluate(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.IAM == nil {
		h.fail(w, r, &admin.Error{Code: admin.Unavailable, Message: "IAM evaluation is not wired into this server, so no pair can be decided"})
		return
	}
	var body admin.EvaluateRequest
	if aerr := readJSON(w, r, &body, false); aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	req, aerr := evaluateRequest(body)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	evaluations, err := h.o.IAM.Evaluate(r.Context(), req)
	if unknown, ok := errors.AsType[*enforce.UnknownPrincipalError](err); ok {
		h.fail(w, r, &admin.Error{Code: admin.NotFound, Message: unknown.ARN + " does not exist", Hint: "'understudy iam usage' lists the principals that made calls"})
		return
	}
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.Internal, Message: err.Error()})
		return
	}
	out := admin.Evaluation{Results: make([]admin.EvaluationResult, len(evaluations))}
	for i, e := range evaluations {
		out.Results[i] = evaluationResult(body.Requests[i], e)
	}
	h.ok(w, r, out)
}

// evaluateRequest reads a POST /iam/evaluate body, refusing each field
// that does not read by name.
func evaluateRequest(body admin.EvaluateRequest) (enforce.EvaluateRequest, *admin.Error) {
	var out enforce.EvaluateRequest
	p, err := access.ParsePrincipal(body.Principal)
	if err != nil {
		return out, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: err.Error()}
	}
	who, ok := p.(access.Identity)
	if !ok {
		return out, &admin.Error{
			Code: admin.InvalidRequest, Field: "principal", Message: body.Principal + " is a service principal, which has no identity-based policy to evaluate",
			Hint: "pass an account root, user or role ARN, such as arn:aws:iam::000000000000:user/app",
		}
	}
	if out.Principal, err = aws.ParseARN(who.String()); err != nil {
		return out, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: err.Error()}
	}
	out.Identity = enforce.StoredIdentity{}
	if body.IdentityPolicies != nil {
		replaced := enforce.ReplacedIdentity{Policies: make([]authz.Policy, 0, len(*body.IdentityPolicies))}
		for i, raw := range *body.IdentityPolicies {
			field := fmt.Sprintf("identityPolicies[%d]", i)
			doc, err := policydoc.Parse(raw)
			if err != nil {
				return out, &admin.Error{Code: admin.InvalidRequest, Field: field, Message: err.Error()}
			}
			replaced.Policies = append(replaced.Policies, authz.Policy{Name: field, Document: doc})
		}
		out.Identity = replaced
	}
	if len(body.Requests) == 0 {
		return out, &admin.Error{
			Code: admin.InvalidRequest, Field: "requests", Message: "the request names no (action, resource) pair to evaluate",
			Hint: `send "requests": [{"action": "sqs:SendMessage", "resource": "*"}]`,
		}
	}
	for i, item := range body.Requests {
		pair, aerr := evaluatePair(i, item)
		if aerr != nil {
			return out, aerr
		}
		out.Pairs = append(out.Pairs, pair)
	}
	return out, nil
}

// evaluatePair reads requests[i]: an action <prefix>:<name> and a resource
// ARN or "*".
func evaluatePair(i int, item admin.EvaluateItem) (kernel.Pair, *admin.Error) {
	prefix, name, ok := strings.Cut(item.Action, ":")
	if !ok || prefix == "" || name == "" || strings.ContainsAny(item.Action, " \t\r\n") {
		return kernel.Pair{}, &admin.Error{
			Code: admin.InvalidRequest, Field: fmt.Sprintf("requests[%d].action", i),
			Message: fmt.Sprintf("%q is not an IAM action <prefix>:<name>", item.Action), Hint: "write it as a policy does, such as sqs:SendMessage",
		}
	}
	pair := kernel.Pair{Action: registry.IAMAction{Prefix: prefix, Name: name}, Resource: kernel.AnyResource{}}
	if item.Resource == "*" {
		return pair, nil
	}
	arn, err := aws.ParseARN(item.Resource)
	if err != nil {
		return kernel.Pair{}, &admin.Error{
			Code: admin.InvalidRequest, Field: fmt.Sprintf("requests[%d].resource", i),
			Message: fmt.Sprintf("%q is neither * nor an ARN: %v", item.Resource, err),
		}
	}
	pair.Resource = kernel.ResourceARN{ARN: arn}
	return pair, nil
}

// evaluationResult renders one evaluation for the pair item asked about,
// with the decision in the IAM simulator's EvalDecision spellings
// (com.amazonaws.iam#PolicyEvaluationDecisionType).
func evaluationResult(item admin.EvaluateItem, e enforce.Evaluation) admin.EvaluationResult {
	out := admin.EvaluationResult{Action: item.Action, Resource: item.Resource, Message: e.Message}
	switch d := e.Outcome.Decision.(type) {
	case authz.Allow:
		out.Decision = "allowed"
	case authz.ImplicitDeny:
		out.Decision, out.PolicyType = "implicitDeny", d.PolicyType.String()
	case authz.ExplicitDeny:
		out.Decision, out.PolicyType, out.PolicyARN = "explicitDeny", d.PolicyType.String(), d.PolicyARN
	}
	for _, u := range e.Outcome.Unsupplied {
		if !slices.Contains(out.UnsuppliedKeys, u.Key) {
			out.UnsuppliedKeys = append(out.UnsuppliedKeys, u.Key)
		}
	}
	return out
}
