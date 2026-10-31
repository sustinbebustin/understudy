package apigateway

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigateway/patch"
)

// updateRestAPI applies UpdateRestApi's patch operations to the API, all
// or none (apigateway-rest.md 3.8, 12.4). The generated validator has
// already refused an op outside the enum.
func (s *Service) updateRestAPI(_ context.Context, c *kernel.Call, in *apigatewayapi.UpdateRestApiInput) (*apigatewayapi.RestApi, *awserr.Error) {
	scope := c.Scope()
	ops := patch.FromModel(in.PatchOperations)
	api, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (RestAPI, *awserr.Error) {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return RestAPI{}, errAPINotFound(scope, in.RestApiId)
		}
		a, err := restAPIPatch().Apply(a, ops)
		if err != nil {
			return RestAPI{}, err
		}
		if err := a.Endpoint.check(); err != nil {
			return RestAPI{}, err
		}
		if a.Policy != nil {
			a.Policy = new(policyStored(scope, a.ID, *a.Policy))
		}
		st.APIs[a.ID] = a
		return a, nil
	})
	if err != nil {
		return nil, err
	}
	out := api.output(APIStatusSwitch().Value.AfterUpdate)
	// UpdateRestApi answers "tags": {} when the API has none (recorded,
	// apigateway-rest.md 2.3).
	out.Tags = tagsOut(api.Tags)
	return &out, nil
}

// restAPIPatch is UpdateRestApi's path table (apigateway-rest.md 3.8). An
// unknown path answers "Invalid patch path <path>" (recorded, finding 5);
// an op other than replace on a replace-only path answers the recorded
// OpsOneOf(replace) text (recorded for /minimumCompressionSize, inferred
// for the others). The documented paths whose effect understudy does not
// hold yet are known paths that answer NotImplemented, so their op
// refusals stay AWS's.
func restAPIPatch() patch.Table[RestAPI] {
	add, remove, replace := apigatewayapi.OpAdd, apigatewayapi.OpRemove, apigatewayapi.OpReplace
	addRemoveReplace := []apigatewayapi.Op{add, remove, replace}
	rules := []patch.Rule[RestAPI]{
		{Path: "/name", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			if op.Value == nil || *op.Value == "" {
				// name is required on create (apigateway-rest.md 3.1); the
				// text is inferred.
				return errBadRequest("Invalid patch value for /name: a REST API name cannot be empty")
			}
			a.Name = *op.Value
			return nil
		}},
		{Path: "/description", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			if op.Op == remove {
				a.Description = nil
				return nil
			}
			a.Description = clonePtr(op.Value)
			return nil
		}},
		{Path: "/binaryMediaTypes/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: patchBinaryMediaType},
		{Path: "/endpointConfiguration/types/*", Ops: addRemoveReplace, OpText: patch.OpsAddRemoveReplace, Apply: patchEndpointType},
		{Path: "/endpointConfiguration/vpcEndpointIds", Ops: []apigatewayapi.Op{add, remove}, OpText: patch.OpsOneOf(add, remove), Apply: patchVPCEndpointID},
		{Path: "/endpointConfiguration/ipAddressType", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			v, err := enumValue[apigatewayapi.IpAddressType](op)
			a.Endpoint.IPAddressType = v
			return err
		}},
		{Path: "/apiKeySource", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			v, err := enumValue[apigatewayapi.ApiKeySourceType](op)
			a.APIKeySource = v
			return err
		}},
		{Path: "/securityPolicy", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			v, err := enumValue[apigatewayapi.SecurityPolicy](op)
			a.SecurityPolicy = &v
			return err
		}},
		{Path: "/endpointAccessMode", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			v, err := enumValue[apigatewayapi.EndpointAccessMode](op)
			a.EndpointAccessMode = &v
			return err
		}},
		{Path: "/disableExecuteApiEndpoint", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			v, err := strconv.ParseBool(deref(op.Value, ""))
			if err != nil {
				// The text is inferred.
				return errBadRequest("Invalid patch value for %s: %q is not true or false", op.Path, deref(op.Value, ""))
			}
			a.DisableExecuteAPIEndpoint = v
			return nil
		}},
		{Path: "/minimumCompressionSize", Ops: []apigatewayapi.Op{replace}, Apply: patchMinimumCompressionSize},
		// The policy itself is rewritten by updateRestAPI, which knows the
		// API's ARN; "" removes it, as Terraform's delete sends
		// (apigateway-rest.md 9.3).
		{Path: "/policy", Ops: []apigatewayapi.Op{replace}, Apply: func(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
			if deref(op.Value, "") == "" {
				a.Policy = nil
				return nil
			}
			a.Policy = clonePtr(op.Value)
			return nil
		}},
	}
	return patch.Table[RestAPI]{
		Rules:       rules,
		UnknownPath: patch.InvalidPath,
		OpText:      patch.OpsOneOf(replace),
		Clone:       cloneRestAPIForPatch,
	}
}

// patchBinaryMediaType applies add, remove or replace
// /binaryMediaTypes/<escaped type>: the list is addressed as an object
// whose key is the type, add needs no value, and replace removes the type
// and appends the value (recorded, apigateway-rest.md finding 5, 3.8).
// Adding a type already listed and removing one not listed change nothing
// (inferred).
func patchBinaryMediaType(a *RestAPI, op patch.Operation, wild []string) *awserr.Error {
	key := wild[0]
	if op.Op == apigatewayapi.OpAdd {
		if !slices.Contains(a.BinaryMediaTypes, key) {
			a.BinaryMediaTypes = append(slices.Clone(a.BinaryMediaTypes), key)
		}
		return nil
	}
	types := slices.DeleteFunc(slices.Clone(a.BinaryMediaTypes), func(t string) bool { return t == key })
	if op.Op == apigatewayapi.OpReplace {
		if op.Value == nil || *op.Value == "" {
			// The text is inferred.
			return errBadRequest("Invalid patch value for %s: replace needs the new media type as its value", op.Path)
		}
		if !slices.Contains(types, *op.Value) {
			types = append(types, *op.Value)
		}
	}
	a.BinaryMediaTypes = types
	return nil
}

// patchEndpointType applies add, remove or replace
// /endpointConfiguration/types/<type or index>. Terraform sends replace
// /endpointConfiguration/types/0 with the new type (apigateway-rest.md
// 3.8), which sets the API's one type. An API has exactly one type: adding
// another answers the recorded CreateRestApi text, and removing the last
// one is refused (both inferred for UpdateRestApi).
func patchEndpointType(a *RestAPI, op patch.Operation, wild []string) *awserr.Error {
	switch op.Op {
	case apigatewayapi.OpReplace:
		v, err := enumValue[apigatewayapi.EndpointType](op)
		if err != nil {
			return err
		}
		a.Endpoint.Type = v
		return nil
	case apigatewayapi.OpAdd:
		named := apigatewayapi.EndpointType(deref(op.Value, wild[0]))
		if named == a.Endpoint.Type {
			return nil
		}
		return errBadRequest("Cannot create an api with multiple Endpoint Types.")
	case apigatewayapi.OpRemove:
		return errBadRequest("Invalid patch operation for %s: a REST API must keep exactly one endpoint type; replace it instead", op.Path)
	case apigatewayapi.OpMove, apigatewayapi.OpCopy, apigatewayapi.OpTest:
		// The table's Ops admit only add, remove and replace.
	}
	return errBadRequest("Invalid patch operation specified. Must be 'add'|'remove'|'replace'")
}

// patchVPCEndpointID applies add or remove /endpointConfiguration/
// vpcEndpointIds with the id as the value (apigateway-rest.md 3.8). The
// ids are stored without effect.
func patchVPCEndpointID(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
	id := deref(op.Value, "")
	if id == "" {
		// The text is inferred.
		return errBadRequest("Invalid patch value for %s: the VPC endpoint id is the value", op.Path)
	}
	ids := slices.DeleteFunc(slices.Clone(a.Endpoint.VPCEndpointIDs), func(v string) bool { return v == id })
	if op.Op == apigatewayapi.OpAdd {
		ids = append(ids, id)
	}
	a.Endpoint.VPCEndpointIDs = ids
	return nil
}

// patchMinimumCompressionSize applies replace /minimumCompressionSize: a
// number in range, or "" to unset it, as Terraform sends for -1
// (apigateway-rest.md 9.3); out of range answers the recorded text (2.4).
func patchMinimumCompressionSize(a *RestAPI, op patch.Operation, _ []string) *awserr.Error {
	v := deref(op.Value, "")
	if v == "" {
		a.MinimumCompressionSize = nil
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || checkCompressionSize(int32(n)) != nil {
		return errBadRequest(compressionSizeText)
	}
	a.MinimumCompressionSize = new(int32(n))
	return nil
}

// enumValue reads a replace value of an enum member; an unknown value
// answers BadRequestException (text inferred).
func enumValue[E interface {
	~string
	Known() bool
}](op patch.Operation) (E, *awserr.Error) {
	v := E(deref(op.Value, ""))
	if !v.Known() {
		return v, errBadRequest("Invalid patch value for %s: %q is not a valid value", op.Path, string(v))
	}
	return v, nil
}

// cloneRestAPIForPatch copies the members restAPIPatch writes, so a refused
// request leaves the stored API as it was. The other members are shared:
// the table never writes them.
func cloneRestAPIForPatch(a RestAPI) RestAPI {
	c := a
	c.Description = clonePtr(a.Description)
	c.BinaryMediaTypes = slices.Clone(a.BinaryMediaTypes)
	c.Endpoint.VPCEndpointIDs = slices.Clone(a.Endpoint.VPCEndpointIDs)
	c.Policy, c.MinimumCompressionSize = clonePtr(a.Policy), clonePtr(a.MinimumCompressionSize)
	c.SecurityPolicy, c.EndpointAccessMode = clonePtr(a.SecurityPolicy), clonePtr(a.EndpointAccessMode)
	return c
}

// compressionSizeText is the recorded refusal of a minimumCompressionSize
// out of range (apigateway-rest.md 2.4).
const compressionSizeText = "Invalid minimum compression size, must be between 0 and 10485760"

// checkCompressionSize refuses a minimumCompressionSize outside
// 0..10485760 (recorded, apigateway-rest.md 2.4).
func checkCompressionSize(n int32) *awserr.Error {
	if n < 0 || n > 10_485_760 {
		return errBadRequest(compressionSizeText)
	}
	return nil
}

// endpointFrom reads CreateRestApi's endpointConfiguration with the
// recorded defaults: EDGE and ipv4 when absent, dualstack for PRIVATE
// (apigateway-rest.md 2.3). No types at all is EDGE (inferred).
func endpointFrom(ec *apigatewayapi.EndpointConfiguration) (Endpoint, *awserr.Error) {
	e := Endpoint{Type: apigatewayapi.EndpointTypeEdge, IPAddressType: apigatewayapi.IpAddressTypeIpv4}
	if ec == nil {
		return e, nil
	}
	switch len(ec.Types) {
	case 0:
	case 1:
		e.Type = ec.Types[0]
	default:
		// Recorded (apigateway-rest.md 2.4).
		return Endpoint{}, errBadRequest("Cannot create an api with multiple Endpoint Types.")
	}
	if e.Type == apigatewayapi.EndpointTypePrivate {
		e.IPAddressType = apigatewayapi.IpAddressTypeDualstack
	}
	if ec.IpAddressType != nil {
		e.IPAddressType = *ec.IpAddressType
	}
	e.VPCEndpointIDs = slices.Clone(ec.VpcEndpointIds)
	if err := e.check(); err != nil {
		return Endpoint{}, err
	}
	return e, nil
}

// check refuses a PRIVATE endpoint on ipv4 (recorded on CreateRestApi,
// apigateway-rest.md 2.4; applied to UpdateRestApi, inferred).
func (e *Endpoint) check() *awserr.Error {
	if e.Type == apigatewayapi.EndpointTypePrivate && e.IPAddressType == apigatewayapi.IpAddressTypeIpv4 {
		return errBadRequest("Only dualstack ipAddressType is supported for Private APIs.")
	}
	return nil
}

// policyStored is a resource policy as AWS stores it: a resource
// execute-api:/<path> becomes the API's execute-api ARN followed by
// /<path> (recorded for execute-api:/*, apigateway-rest.md 2.3). The
// recording also shows a one-element Resource list returned as a string;
// understudy keeps the policy's shape (inferred, harmless to Terraform,
// which compares policies semantically).
func policyStored(scope aws.Scope, apiID, policy string) string {
	arn := aws.ARN{Partition: scope.Partition.String(), Service: "execute-api", Region: scope.Region.String(), Account: scope.Account.String(), Resource: apiID}
	return strings.ReplaceAll(policy, `"execute-api:/`, `"`+arn.String()+`/`)
}

// policyOut is a stored policy as GetRestApi returns it: a string whose
// backslashes and quotes are backslash-escaped, which Terraform unquotes
// (recorded, apigateway-rest.md 2.3).
func policyOut(policy *string) *string {
	if policy == nil {
		return nil
	}
	return new(strings.ReplaceAll(strings.ReplaceAll(*policy, `\`, `\\`), `"`, `\"`))
}

// getRestAPIs pages the caller's REST APIs in creation order (the order is
// inferred; apigateway-rest.md 2.5).
func (s *Service) getRestAPIs(_ context.Context, c *kernel.Call, in *apigatewayapi.GetRestApisInput) (*apigatewayapi.GetRestApisOutput, *awserr.Error) {
	type found struct {
		out apigatewayapi.GetRestApisOutput
		err *awserr.Error
	}
	got := readState(s, c.Scope(), func(st *State) found {
		apis := slices.SortedFunc(maps.Values(st.APIs), func(a, b RestAPI) int { return cmp.Compare(a.Seq, b.Seq) })
		items, next, err := page(apis, func(a RestAPI) uint64 { return a.Seq }, in.Limit, in.Position)
		if err != nil {
			return found{err: err}
		}
		out := apigatewayapi.GetRestApisOutput{Items: make([]apigatewayapi.RestApi, 0, len(items)), Position: next}
		for i := range items {
			out.Items = append(out.Items, items[i].output(APIStatusSwitch().Value.AfterCreate))
		}
		return found{out: out}
	})
	if got.err != nil {
		return nil, got.err
	}
	return &got.out, nil
}

// deleteRestAPI removes a REST API with everything it holds, and its id
// from the index, so GetRestApi then answers NotFoundException
// (apigateway-rest.md 3.1).
func (s *Service) deleteRestAPI(_ context.Context, c *kernel.Call, in *apigatewayapi.DeleteRestApiInput) (*apigatewayapi.DeleteRestApiOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.APIs[in.RestApiId]; !ok {
			return struct{}{}, errAPINotFound(scope, in.RestApiId)
		}
		delete(st.APIs, in.RestApiId)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	s.index.release(in.RestApiId)
	return &apigatewayapi.DeleteRestApiOutput{}, nil
}
