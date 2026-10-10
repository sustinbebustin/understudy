package admin

import (
	"encoding/json"
	"net/http"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

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
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "principal", Message: text + " is a service principal, which no identity policy attaches to", Hint: "GET " + admin.Prefix + "/iam/usage?principal=" + text + " lists its deliveries"})
		return
	}
	doc, ok := h.o.Access.Policy(identity)
	if !ok {
		h.fail(w, r, &admin.Error{Code: admin.NotFound, Message: "no call by " + text + " is recorded, so there is no policy to write", Hint: "GET " + admin.Prefix + "/iam/usage lists the principals that made calls"})
		return
	}
	out := admin.Policy{Principal: identity.String(), Document: json.RawMessage(policydoc.Encode(doc))}
	reqs := h.o.Access.ResourceRequirements(access.Filter{})
	for i := range reqs {
		req := &reqs[i]
		out.ResourcePolicies = append(out.ResourcePolicies, admin.ResourcePolicy{
			Target: req.Target.String(), Principal: req.Principal.String(), Action: req.Action.String(), Source: req.Source.String(),
		})
	}
	h.ok(w, r, out)
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
	if on, ok := s.(access.OnResource); ok {
		return on.ARN.String()
	}
	return "*"
}
