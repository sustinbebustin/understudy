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
	Origin  RouteOrigin
}

// RouteOrigin says where a route row comes from.
//
//sumtype:decl
type RouteOrigin interface{ isRouteOrigin() }

// ModeledRoute is a row derived from the operation's http trait.
type ModeledRoute struct{}

// OverrideRoute is a codegen/services routeOverrides row, which replaces the
// model's, with its reason.
type OverrideRoute struct{ Reason string }

// AddedRoute is a codegen/services additionalRoutes row, served beside the
// model's, with its reason.
type AddedRoute struct{ Reason string }

func (ModeledRoute) isRouteOrigin()  {}
func (OverrideRoute) isRouteOrigin() {}
func (AddedRoute) isRouteOrigin()    {}

// clientQueryKey is the query literal Smithy-generated clients add to keep
// identical S3 uri templates unique in the model; botocore never sends it,
// so no row may require it (wire-rest.md 3.2).
const clientQueryKey = "x-id"

// routes builds the dispatch table of a REST service: one row per modeled
// operation from its http trait, with the x-id literal dropped. Rows that
// still match the same requests are told apart by the required query and
// header members of their inputs (S3 UploadPart's partNumber and uploadId,
// CopyObject's x-amz-copy-source; wire-rest.md Implications 2). A
// codegen/services routeOverrides entry replaces an operation's row; an
// additionalRoutes entry adds one beside it, which must match requests no
// other row matches. The table is checked by registry.New, the check the
// server runs at boot.
func (b *builder) routes() error {
	if !slices.Contains(b.ir.Protocols, ref.RESTJSON1) && !slices.Contains(b.ir.Protocols, ref.RESTXML) {
		if len(b.cfg.RouteOverrides) > 0 {
			return failf(StageIR, b.configName(), "routeOverrides apply to REST services; %s speaks %v", b.ir.ID, b.ir.Protocols)
		}
		if len(b.cfg.AdditionalRoutes) > 0 {
			return failf(StageIR, b.configName(), "additionalRoutes apply to REST services; %s speaks %v", b.ir.Shape, b.ir.Protocols)
		}
		return nil
	}
	overrides := map[string]*RouteOverride{}
	for i := range b.cfg.RouteOverrides {
		overrides[b.cfg.RouteOverrides[i].Operation] = &b.cfg.RouteOverrides[i]
	}
	added := map[string]*AdditionalRoute{}
	for i := range b.cfg.AdditionalRoutes {
		added[b.cfg.AdditionalRoutes[i].Operation] = &b.cfg.AdditionalRoutes[i]
	}
	var rows []RouteIR
	for _, o := range b.ir.Operations {
		if o.Kind != registry.Modeled {
			continue
		}
		own, err := b.operationRows(o, overrides[o.Name], added[o.Name])
		if err != nil {
			return err
		}
		delete(overrides, o.Name)
		delete(added, o.Name)
		rows = append(rows, own...)
	}
	if len(overrides) > 0 {
		return failf(StageIR, b.configName(), "routeOverrides name %v, which are not modeled operations of %s", slices.Sorted(maps.Keys(overrides)), b.ir.ID)
	}
	if len(added) > 0 {
		return failf(StageIR, b.configName(), "additionalRoutes name %v, which are not modeled operations of %s", slices.Sorted(maps.Keys(added)), b.ir.Shape)
	}
	disambiguate(rows)
	if groups := conflicts(rows); len(groups) > 0 {
		if err := b.addedConflict(rows, groups[0]); err != nil {
			return err
		}
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

// operationRows are modeled operation o's rows: its override, else its http
// trait's row, plus its added row; ov and add are nil when the service config
// has no entry for o.
func (b *builder) operationRows(o *OperationIR, ov *RouteOverride, add *AdditionalRoute) ([]RouteIR, error) {
	var rows []RouteIR
	if add != nil {
		row, err := b.addedRow(o, add)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if ov != nil {
		hb, err := registry.ParseHTTPBinding(ov.Method, ov.URI)
		if err != nil {
			return nil, failf(StageIR, b.configName(), "routeOverrides %s: %w", o.Name, err)
		}
		return append(rows, RouteIR{Op: o, Binding: hb, Headers: ov.Headers, Origin: OverrideRoute{Reason: ov.Reason}}), nil
	}
	if o.HTTP == nil {
		return nil, failf(StageIR, string(o.ID), "has no http trait, but %s is a REST service", b.ir.ID)
	}
	hb := *o.HTTP
	hb.Query = slices.DeleteFunc(slices.Clone(hb.Query), func(q registry.QueryMatch) bool { return queryKeyOf(q) == clientQueryKey })
	return append(rows, RouteIR{Op: o, Binding: hb, Origin: ModeledRoute{}}), nil
}

// disambiguate adds, to every row of a group whose rows match alike, the
// presence of each required httpQuery and httpHeader input member. A row from
// codegen/services is left as written.
func disambiguate(rows []RouteIR) {
	for _, group := range conflicts(rows) {
		for _, i := range group {
			if _, modeled := rows[i].Origin.(ModeledRoute); modeled {
				requireMembers(&rows[i])
			}
		}
	}
}

// addedRow is the row of additionalRoutes entry a for operation o. Each query
// binding names a member of o's input the modeled route reads from the body
// document, so that route and this one bind it in different places but never
// both in the head. The binding is also recorded on o, whose descriptor
// tells the REST binder how a request that arrived by the row binds.
func (b *builder) addedRow(o *OperationIR, a *AdditionalRoute) (RouteIR, error) {
	hb, err := registry.ParseHTTPBinding(a.Method, a.URI)
	if err != nil {
		return RouteIR{}, failf(StageIR, b.configName(), "additionalRoutes %s: %w", o.Name, err)
	}
	route := registry.AdditionalRoute{Binding: hb}
	for _, q := range a.Query {
		i := slices.IndexFunc(o.Input.Members, func(m *MemberIR) bool { return m.Name == q.Member })
		if i < 0 {
			names := make([]string, len(o.Input.Members))
			for j, m := range o.Input.Members {
				names[j] = m.Name
			}
			return RouteIR{}, failf(StageIR, b.configName(), "additionalRoutes %s binds %s, which %s does not have; its members are %s",
				o.Name, q.Member, o.Input.ID, strings.Join(names, ", "))
		}
		if o.Input.Members[i].HTTP != nil {
			return RouteIR{}, failf(StageIR, b.configName(), "additionalRoutes %s binds %s, which the modeled route already binds with an HTTP trait; "+
				"bind only members it reads from the body document", o.Name, q.Member)
		}
		route.QueryMembers = append(route.QueryMembers, registry.QueryMember{Param: q.Param, Member: q.Member})
	}
	o.AdditionalRoutes = append(o.AdditionalRoutes, route)
	return RouteIR{Op: o, Binding: hb, Origin: AddedRoute{Reason: a.Reason}}, nil
}

// addedConflict refuses a conflict group holding an additionalRoutes row:
// unlike an override, an added row may not take requests another row
// serves, its own operation's modeled row included. It returns nil for a
// group of other rows.
func (b *builder) addedConflict(rows []RouteIR, group []int) error {
	at := slices.IndexFunc(group, func(i int) bool {
		_, ok := rows[i].Origin.(AddedRoute)
		return ok
	})
	if at < 0 {
		return nil
	}
	var others []string
	for _, i := range group {
		if i != group[at] {
			others = append(others, routeText(rows[i]))
		}
	}
	return failf(StageIR, b.configName(), "additionalRoutes %s matches the same requests as %s; an added route must reach requests no other row matches",
		routeText(rows[group[at]]), strings.Join(others, " and "))
}

// routeText renders a row as its operation, method and URI pattern, such as
// "ListThings GET /things?list".
func routeText(r RouteIR) string {
	var b strings.Builder
	b.WriteString(r.Op.Name + " " + r.Binding.Method + " ")
	if len(r.Binding.Path) == 0 {
		b.WriteByte('/')
	}
	for _, s := range r.Binding.Path {
		b.WriteByte('/')
		switch s := s.(type) {
		case registry.Literal:
			b.WriteString(s.Value)
		case registry.Label:
			b.WriteString("{" + s.Name + "}")
		case registry.Greedy:
			b.WriteString("{" + s.Name + "+}")
		}
	}
	sep := "?"
	for _, q := range r.Binding.Query {
		b.WriteString(sep)
		sep = "&"
		switch q := q.(type) {
		case registry.QueryPresent:
			b.WriteString(q.Key)
		case registry.QueryEquals:
			b.WriteString(q.Key + "=" + q.Value)
		}
	}
	return b.String()
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
		switch o := r.Origin.(type) {
		case OverrideRoute:
			f.p("\t// %s: %s", r.Op.Name, o.Reason)
		case AddedRoute:
			f.p("\t// %s, beside its modeled route: %s", r.Op.Name, o.Reason)
		case ModeledRoute:
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
