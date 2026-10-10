package codegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ServiceConfig is codegen/services/<svc>.json (ARCHITECTURE.md 5.3). Model
// names the UPSTREAM.json directory of the service's model file, so the
// generator reads one model instead of scanning all twelve. IAMPrefix is the
// service's IAM action prefix and its key in the Service Reference table,
// stated rather than derived: the model's arnNamespace is wrong for
// CloudWatch (monitoring, where IAM says cloudwatch; smithy-models.md 3.1).
type ServiceConfig struct {
	ID             ref.ServiceID
	Service        smithy.ShapeID
	Package        string
	Model          string
	IAMPrefix      string
	ExtraProtocols []ref.ProtocolID
	// RawBinding adds a <Op>Raw binder method per operation (DynamoDB's
	// passthrough, ADR 0014).
	RawBinding bool
	// RawOperations are synthetic operations outside the model whose
	// handlers own the HTTP exchange (ADR 0003).
	RawOperations []RawOperation
	// RouteOverrides replace the route row the model derives for an
	// operation (wire-rest.md 3.2).
	RouteOverrides []RouteOverride
	// IAMPrimary names the primary action of an operation whose authorized
	// actions leave it ambiguous (smithy-models.md 8).
	IAMPrimary []IAMPrimary
	// Destination is APIPackage for a codegen/services file and
	// CorpusPackage for a synthetic compliance service.
	Destination Destination
}

// RawOperation is one rawOperations entry: a Kind Raw descriptor with its IAM
// actions, claimed by an edge prefix or host rule rather than a route.
type RawOperation struct {
	Name string
	IAM  []registry.IAMAction
}

// RouteOverride is one routeOverrides entry: the operation's whole route row,
// method included, with the reason the model's row does not serve.
type RouteOverride struct {
	Operation string
	Method    string
	URI       string
	Headers   []string
	Reason    string
}

// IAMPrimary is one iamPrimary entry: the action an operation's descriptor
// lists first, chosen from the operation's authorized actions, with the
// reason no rule picks it.
type IAMPrimary struct {
	Operation string
	Action    registry.IAMAction
	Reason    string
}

// ParseServiceConfig reads a service config; id is the file's base name.
func ParseServiceConfig(r io.Reader, id ref.ServiceID) (ServiceConfig, error) {
	subject := "codegen/services/" + string(id) + ".json"
	raw, err := io.ReadAll(r)
	if err != nil {
		return ServiceConfig{}, failf(StageLoad, subject, "read: %w", err)
	}
	var v struct {
		Service        string            `json:"service"`
		Package        string            `json:"package"`
		Model          string            `json:"model"`
		IAMPrefix      string            `json:"iamPrefix"`
		ExtraProtocols []string          `json:"extraProtocols"`
		RawBinding     bool              `json:"rawBinding"`
		RawOperations  []json.RawMessage `json:"rawOperations"`
		RouteOverrides []json.RawMessage `json:"routeOverrides"`
		IAMPrimary     []json.RawMessage `json:"iamPrimary"`
	}
	if err := strictUnmarshal(raw, &v); err != nil {
		return ServiceConfig{}, failf(StageLoad, subject, "%w", err)
	}
	svc, err := smithy.Parse(v.Service)
	if err != nil {
		return ServiceConfig{}, failf(StageLoad, subject, "service: %w", err)
	}
	if !token.IsIdentifier(v.Package) || v.Package != string(id)+"api" {
		return ServiceConfig{}, failf(StageLoad, subject, "package is %q; it must be %q, the service id plus api", v.Package, string(id)+"api")
	}
	if v.Model == "" {
		return ServiceConfig{}, failf(StageLoad, subject, `"model" names the model's directory in third_party/api-models-aws/UPSTREAM.json`)
	}
	if !isIAMPrefix(v.IAMPrefix) {
		return ServiceConfig{}, failf(StageLoad, subject, `"iamPrefix" is %q; give the IAM action prefix the Service Reference lists, such as "sqs" or "cloudwatch" (not the arnNamespace "monitoring")`, v.IAMPrefix)
	}
	cfg := ServiceConfig{
		ID: id, Service: svc, Package: v.Package, Model: v.Model, IAMPrefix: v.IAMPrefix,
		RawBinding: v.RawBinding, Destination: APIPackage{},
	}
	for _, p := range v.ExtraProtocols {
		pid, ok := protocolTraits()[p]
		if !ok {
			return ServiceConfig{}, failf(StageLoad, subject, "extraProtocols: %q is not a protocol trait id such as aws.protocols#awsQuery", p)
		}
		cfg.ExtraProtocols = append(cfg.ExtraProtocols, pid)
	}
	if cfg.RawOperations, err = parseEntries(subject, "rawOperations", v.RawOperations, parseRawOperation, func(o RawOperation) string { return o.Name }); err != nil {
		return ServiceConfig{}, err
	}
	if cfg.RouteOverrides, err = parseEntries(subject, "routeOverrides", v.RouteOverrides, parseRouteOverride, func(o RouteOverride) string { return o.Operation }); err != nil {
		return ServiceConfig{}, err
	}
	if cfg.IAMPrimary, err = parseEntries(subject, "iamPrimary", v.IAMPrimary, parseIAMPrimary, func(p IAMPrimary) string { return p.Operation }); err != nil {
		return ServiceConfig{}, err
	}
	return cfg, nil
}

// parseEntries parses the entries of a per-operation list field, refusing a
// second entry for one operation.
func parseEntries[T any](subject, field string, raws []json.RawMessage, parse func(json.RawMessage) (T, error), operation func(T) string) ([]T, error) {
	var out []T
	for i, r := range raws {
		e, err := parse(r)
		if err != nil {
			return nil, failf(StageLoad, subject, "%s[%d]: %w", field, i, err)
		}
		if slices.ContainsFunc(out, func(x T) bool { return operation(x) == operation(e) }) {
			return nil, failf(StageLoad, subject, "%s[%d]: %s has two entries; merge them into one", field, i, operation(e))
		}
		out = append(out, e)
	}
	return out, nil
}

func parseIAMPrimary(raw json.RawMessage) (IAMPrimary, error) {
	var v struct {
		Operation string `json:"operation"`
		Action    string `json:"action"`
		Reason    string `json:"reason"`
	}
	if err := strictUnmarshal(raw, &v); err != nil {
		return IAMPrimary{}, err
	}
	if !token.IsExported(v.Operation) || v.Reason == "" {
		return IAMPrimary{}, errors.New(`an entry needs "operation", "action" and the "reason" that action is the primary one`)
	}
	act, err := parseIAMAction(v.Action)
	if err != nil {
		return IAMPrimary{}, err
	}
	return IAMPrimary{Operation: v.Operation, Action: act, Reason: v.Reason}, nil
}

func parseRawOperation(raw json.RawMessage) (RawOperation, error) {
	var v struct {
		Name string   `json:"name"`
		IAM  []string `json:"iam"`
	}
	if err := strictUnmarshal(raw, &v); err != nil {
		return RawOperation{}, err
	}
	if !token.IsExported(v.Name) {
		return RawOperation{}, fmt.Errorf("name %q must be an exported operation name such as InvokeFunctionUrl", v.Name)
	}
	op := RawOperation{Name: v.Name}
	for _, a := range v.IAM {
		act, err := parseIAMAction(a)
		if err != nil {
			return RawOperation{}, err
		}
		op.IAM = append(op.IAM, act)
	}
	return op, nil
}

// parseIAMAction reads prefix:Action.
func parseIAMAction(s string) (registry.IAMAction, error) {
	prefix, name, ok := strings.Cut(s, ":")
	if !ok || !isIAMPrefix(prefix) || !token.IsExported(name) {
		return registry.IAMAction{}, fmt.Errorf("%q is not an IAM action prefix:Action, such as lambda:InvokeFunctionUrl", s)
	}
	return registry.IAMAction{Prefix: prefix, Name: name}, nil
}

func parseRouteOverride(raw json.RawMessage) (RouteOverride, error) {
	var v RouteOverride
	var w struct {
		Operation string   `json:"operation"`
		Method    string   `json:"method"`
		URI       string   `json:"uri"`
		Headers   []string `json:"headers"`
		Reason    string   `json:"reason"`
	}
	if err := strictUnmarshal(raw, &w); err != nil {
		return v, err
	}
	v = RouteOverride(w)
	if v.Operation == "" || v.Method == "" || v.URI == "" || v.Reason == "" {
		return v, errors.New(`an override needs "operation", "method", "uri" and the "reason" the model's row does not serve`)
	}
	if _, err := registry.ParseHTTPBinding(v.Method, v.URI); err != nil {
		return v, err
	}
	return v, nil
}

// isIAMPrefix reports whether s has the shape of an IAM service prefix:
// lowercase letters, digits and hyphens, starting with a letter.
func isIAMPrefix(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// protocolTraits maps protocol trait ids onto protocol ids.
func protocolTraits() map[string]ref.ProtocolID {
	return map[string]ref.ProtocolID{
		traitAWSJSON10: ref.AWSJSON10,
		traitAWSJSON11: ref.AWSJSON11,
		traitAWSQuery:  ref.AWSQuery,
		traitRESTJSON1: ref.RESTJSON1,
		traitRESTXML:   ref.RESTXML,
		traitRPCv2CBOR: ref.RPCv2CBOR,
	}
}
