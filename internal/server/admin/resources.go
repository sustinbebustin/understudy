package admin

import (
	"net/http"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// resources lists every resource the selected services hold in the
// selected accounts and regions. kernel.Stateful lists the resources of a
// scope it is given but names none, so each service names its scopes
// through Scoper.
func (h *Handler) resources(w http.ResponseWriter, r *http.Request, _ []string) {
	v, fields := r.URL.Query(), resourcesFields()
	selected, aerr := h.selectServices(v[fields.services], fields)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	filter, aerr := h.resetScope(v[fields.accounts], v[fields.regions], fields)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	out := admin.ResourceList{Resources: []admin.Resource{}}
	for _, s := range h.o.Services {
		if !slices.Contains(selected, s.Name()) {
			continue
		}
		for _, scope := range scopesOf(s) {
			if !filter.Covers(scope) {
				continue
			}
			for _, res := range s.Resources(scope) {
				out.Resources = append(out.Resources, admin.Resource{
					Service: string(s.Name()),
					Account: scope.Account.String(),
					Region:  scope.Region.String(),
					ARN:     res.ARN.String(),
					Summary: res.Summary,
				})
			}
		}
	}
	h.ok(w, r, out)
}

// scopesOf returns the scopes s holds state in, sorted by account and
// region.
func scopesOf(s Scoper) []aws.Scope {
	out := slices.Clone(s.Scopes())
	slices.SortFunc(out, func(a, b aws.Scope) int {
		if c := strings.Compare(a.Account.String(), b.Account.String()); c != 0 {
			return c
		}
		return strings.Compare(a.Region.String(), b.Region.String())
	})
	return slices.Compact(out)
}
