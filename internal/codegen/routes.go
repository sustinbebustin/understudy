package codegen

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// RouteIR is one row of a REST dispatch table (ARCHITECTURE.md 5.5).
type RouteIR struct {
	Op      *OperationIR
	Binding registry.HTTPBinding
	// Headers are header names the request must carry.
	Headers []string
	// Override is the codegen/services reason when the row replaces the
	// model's, else empty.
	Override string
}

// clientQueryKey is the query literal Smithy-generated clients add to keep
// identical S3 uri templates unique in the model; botocore never sends it,
// so no row may require it (wire-rest.md 3.2).
const clientQueryKey = "x-id"

// routes builds the dispatch table of a REST service: one row per modeled
// operation from its http trait, with the x-id literal dropped. Rows that
// still match the same requests are told apart by the required query and
// header members of their inputs (S3 UploadPart's partNumber and uploadId,
// CopyObject's x-amz-copy-source; wire-rest.md Implications 2). A
// codegen/services routeOverrides entry replaces an operation's row. The
// table is checked by registry.New, the check the server runs at boot.
func (b *builder) routes() error {
	if !slices.Contains(b.ir.Protocols, ref.RESTJSON1) && !slices.Contains(b.ir.Protocols, ref.RESTXML) {
		if len(b.cfg.RouteOverrides) > 0 {
			return failf(StageIR, b.configName(), "routeOverrides apply to REST services; %s speaks %v", b.ir.ID, b.ir.Protocols)
		}
		return nil
	}
	overrides := map[string]RouteOverride{}
	for _, o := range b.cfg.RouteOverrides {
		overrides[o.Operation] = o
	}
	var rows []RouteIR
	for _, o := range b.ir.Operations {
		if o.Kind != registry.Modeled {
			continue
		}
		if ov, ok := overrides[o.Name]; ok {
			delete(overrides, o.Name)
			hb, err := registry.ParseHTTPBinding(ov.Method, ov.URI)
			if err != nil {
				return failf(StageIR, b.configName(), "routeOverrides %s: %w", o.Name, err)
			}
			rows = append(rows, RouteIR{Op: o, Binding: hb, Headers: ov.Headers, Override: ov.Reason})
			continue
		}
		if o.HTTP == nil {
			return failf(StageIR, string(o.ID), "has no http trait, but %s is a REST service", b.ir.ID)
		}
		hb := *o.HTTP
		hb.Query = slices.DeleteFunc(slices.Clone(hb.Query), func(q registry.QueryMatch) bool { return queryKeyOf(q) == clientQueryKey })
		rows = append(rows, RouteIR{Op: o, Binding: hb})
	}
	if len(overrides) > 0 {
		return failf(StageIR, b.configName(), "routeOverrides name %v, which are not modeled operations of %s", slices.Sorted(maps.Keys(overrides)), b.ir.ID)
	}
	disambiguate(rows)
	if groups := conflicts(rows); len(groups) > 0 {
		var names []string
		for _, i := range groups[0] {
			names = append(names, routeSummary(rows[i]))
		}
		return failf(StageIR, b.configName(), "routes %s match the same requests even with their required query and header members; add a routeOverrides entry that tells them apart", strings.Join(names, " and "))
	}
	slices.SortStableFunc(rows, func(x, y RouteIR) int {
		return cmp.Or(-registry.CompareSpecificity(x.route(), y.route()), cmp.Compare(x.Op.Name, y.Op.Name))
	})
	b.ir.Routes = rows
	return b.checkRoutes()
}

func (b *builder) configName() string { return "codegen/services/" + string(b.cfg.ID) + ".json" }

// disambiguate adds, to every row of a group whose rows match alike, the
// presence of each required httpQuery and httpHeader input member. A row an
// override set is left as written.
func disambiguate(rows []RouteIR) {
	for _, group := range conflicts(rows) {
		for _, i := range group {
			if rows[i].Override == "" {
				requireMembers(&rows[i])
			}
		}
	}
}

// requireMembers adds the presence of each required httpQuery and
// httpHeader input member to row r.
func requireMembers(r *RouteIR) {
	for _, m := range r.Op.Input.Members {
		if !m.Required {
			continue
		}
		switch l := m.HTTP.(type) {
		case smithy.HTTPQuery:
			if !slices.ContainsFunc(r.Binding.Query, func(q registry.QueryMatch) bool { return queryKeyOf(q) == l.Name }) {
				r.Binding.Query = append(r.Binding.Query, registry.QueryPresent{Key: l.Name})
			}
		case smithy.HTTPHeader:
			r.Headers = append(r.Headers, l.Name)
		case smithy.HTTPLabel, smithy.HTTPPayload, smithy.HTTPPrefixHeaders, smithy.HTTPQueryParams, smithy.HTTPResponseCode:
			// A label is in the URI pattern already; the others are never
			// required to tell routes apart.
		}
	}
}

// conflicts returns the index groups of rows no request tells apart.
func conflicts(rows []RouteIR) [][]int {
	bySig := map[string][]int{}
	for i, r := range rows {
		s := signature(r)
		bySig[s] = append(bySig[s], i)
	}
	var out [][]int
	for _, s := range slices.Sorted(maps.Keys(bySig)) {
		if len(bySig[s]) > 1 {
			out = append(out, bySig[s])
		}
	}
	return out
}

// route is the registry row r becomes, without its operation.
func (r RouteIR) route() *registry.Route {
	return &registry.Route{HTTPBinding: r.Binding, Headers: r.Headers}
}

// signature is equal for two rows exactly when registry.RouteTable.Match
// cannot tell them apart.
func signature(r RouteIR) string { return r.route().Signature() }

func queryKeyOf(q registry.QueryMatch) string {
	switch q := q.(type) {
	case registry.QueryPresent:
		return q.Key
	case registry.QueryEquals:
		return q.Key
	}
	return ""
}

// checkRoutes runs registry.New over a skeleton of the descriptors the
// package will hold, so a table the server would refuse at boot fails
// generation instead.
func (b *builder) checkRoutes() error {
	svc := &registry.Service{ID: b.ir.ID, Routes: &registry.RouteTable{}}
	ops := map[*OperationIR]*registry.Operation{}
	for _, o := range b.ir.Operations {
		d := &registry.Operation{Service: svc, ID: ref.OperationID{Service: b.ir.ID, Name: o.Name}, Ordinal: o.Ordinal, Kind: o.Kind}
		ops[o] = d
		svc.Operations = append(svc.Operations, d)
	}
	for _, r := range b.ir.Routes {
		svc.Routes.Routes = append(svc.Routes.Routes, &registry.Route{HTTPBinding: r.Binding, Headers: r.Headers, Operation: ops[r.Op]})
	}
	if _, err := registry.New(svc); err != nil {
		return failf(StageIR, string(b.ir.Shape), "the generated route table does not register: %w", err)
	}
	return nil
}

// emitRoutes writes routes_gen.go: the REST dispatch table, linked to the
// service at init because each row points at an operation descriptor.
func emitRoutes(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "routes_gen.go")
	f.use("registry")
	f.p("var routes = registry.RouteTable{Routes: []*registry.Route{")
	for _, r := range ir.Routes {
		if r.Override != "" {
			f.p("\t// %s: %s", r.Op.Name, r.Override)
		}
		headers := ""
		if len(r.Headers) > 0 {
			quoted := make([]string, len(r.Headers))
			for i, h := range r.Headers {
				quoted[i] = goString(h)
			}
			headers = ", Headers: []string{" + strings.Join(quoted, ", ") + "}"
		}
		f.p("\t{HTTPBinding: %s%s, Operation: &desc%s},", strings.TrimPrefix(httpBindingLit(&r.Binding), "&"), headers, r.Op.Name)
	}
	f.p("}}\n")
	f.p("// The rows point at the operation descriptors, which point at the")
	f.p("// service, so the table is linked once at init.")
	f.p("func init() { service.Routes = &routes }")
	return f.bytes()
}

// routeSummary renders a row for messages.
func routeSummary(r RouteIR) string {
	return fmt.Sprintf("%s %s", r.Op.Name, signature(r))
}
