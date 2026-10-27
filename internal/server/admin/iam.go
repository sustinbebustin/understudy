package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/enforce"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// IAMEvaluator decides (action, resource) pairs for a principal as
// iam.enforce would: the enforce.Authorizer, which answers under every mode.
type IAMEvaluator interface {
	Evaluate(ctx context.Context, r enforce.EvaluateRequest) ([]enforce.Evaluation, error)
}

// iamUsage lists the recorded uses, by principal and IAM action prefix.
func (h *Handler) iamUsage(w http.ResponseWriter, r *http.Request, _ []string) {
	v := r.URL.Query()
	var f access.Filter
	if text := v.Get("principal"); text != "" {
		p, err := access.ParsePrincipal(text)
		if err != nil {
			h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: err.Error()})
			return
		}
		f.Principal = p
	}
	f.Service = v.Get("service")
	uses := h.o.Access.Usage(f)
	out := admin.UsageList{Usage: make([]admin.Usage, 0, len(uses))}
	for i := range uses {
		u := &uses[i]
		out.Usage = append(out.Usage, admin.Usage{
			Principal: u.Principal.String(),
			Mode:      modeText(u.Mode),
			Action:    u.Action.String(),
			Resource:  resourceText(u.Resource),
			Calls:     u.Calls,
			Failures:  u.Failures,
			First:     u.First.UTC(),
			Last:      u.Last.UTC(),
		})
	}
	h.ok(w, r, out)
}

// iamPolicy renders the least-privilege identity policy of a principal
// with the resource-policy statements the recorded deliveries need.
func (h *Handler) iamPolicy(w http.ResponseWriter, r *http.Request, _ []string) {
	text := r.URL.Query().Get("principal")
	p, err := access.ParsePrincipal(text)
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: err.Error(), Hint: "pass ?principal= with an IAM ARN, such as arn:aws:iam::000000000000:role/worker"})
		return
	}
	identity, ok := p.(access.Identity)
	if !ok {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: text + " is a service principal, which no identity policy attaches to", Hint: "'understudy iam usage --principal " + text + "' (GET " + admin.Prefix + "/iam/usage?principal=" + text + ") lists its deliveries"})
		return
	}
	doc, ok := h.o.Access.Policy(identity)
	if !ok {
		h.fail(w, r, noPolicy(text, len(h.o.Access.Usage(access.Filter{Principal: identity})) > 0))
		return
	}
	out := admin.Policy{Principal: identity.String(), Document: json.RawMessage(policydoc.Encode(doc))}
	reqs := h.o.Access.ResourceRequirements(access.Filter{})
	for i := range reqs {
		req := &reqs[i]
		out.ResourcePolicies = append(out.ResourcePolicies, admin.ResourcePolicy{
			Target: req.Target.String(), Principal: req.Principal.String(), Action: req.Action.String(), Source: req.Source.String(),
			// A zero account renders "", which the field omits.
			SourceAccount: req.SourceAccount.String(),
		})
	}
	h.ok(w, r, out)
}

// noPolicy is GET /iam/policy's 404 for principal, whose calls, when
// recorded is true, all need no permission: Policy leaves out
// sts:GetCallerIdentity (iam-enforcement-surfaces.md 5.1, 12.6), which
// /iam/usage still lists.
func noPolicy(principal string, recorded bool) *admin.Error {
	if recorded {
		return &admin.Error{
			Code:    admin.NotFound,
			Message: "the calls recorded for " + principal + " need no permission (sts:GetCallerIdentity), so there is no policy to write",
			Hint:    "'understudy iam usage --principal " + principal + "' (GET " + admin.Prefix + "/iam/usage?principal=" + principal + ") lists them",
		}
	}
	return &admin.Error{
		Code:    admin.NotFound,
		Message: "no call by " + principal + " is recorded, so there is no policy to write",
		Hint:    "'understudy iam usage' (GET " + admin.Prefix + "/iam/usage) lists the principals that made calls",
	}
}

func modeText(m access.Mode) string {
	switch m.(type) {
	case access.Direct:
		return "direct"
	case access.AsCaller:
		return "caller"
	case access.AsExecutionRole:
		return "execution-role"
	case access.AsService:
		return "service"
	default:
		return "unknown"
	}
}

func resourceText(s access.Scope) string {
	switch s := s.(type) {
	case access.OnResource:
		return s.ARN.String()
	case access.BucketObjects:
		return s.Bucket.String() + "/*"
	case access.AnyResource:
	}
	return "*"
}
