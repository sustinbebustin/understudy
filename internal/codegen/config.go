package codegen

import (
	"encoding/json"
	"go/token"
	"io"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// ServiceConfig is codegen/services/<svc>.json (ARCHITECTURE.md 5.3). Model
// names the UPSTREAM.json directory of the service's model file, so the
// generator reads one model instead of scanning all twelve. IAMPrefix is the
// IAM action prefix, stated rather than derived: the model's arnNamespace
// is wrong for CloudWatch (monitoring, where IAM says cloudwatch;
// smithy-models.md 3.1) until the Service Reference table lands (T19).
type ServiceConfig struct {
	ID             ref.ServiceID
	Service        smithy.ShapeID
	Package        string
	Model          string
	IAMPrefix      string
	ExtraProtocols []ref.ProtocolID
}

// ParseServiceConfig reads a service config; id is the file's base name.
// rawBinding, rawOperations and routeOverrides are accepted only empty: their
// emission lands with the all-models generator (PHASE1-PLAN T19).
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
	if v.RawBinding || len(v.RawOperations) > 0 || len(v.RouteOverrides) > 0 {
		return ServiceConfig{}, failf(StageLoad, subject, "rawBinding, rawOperations and routeOverrides are not emitted by this generator yet (they land with the all-models generator); leave them unset")
	}
	cfg := ServiceConfig{ID: id, Service: svc, Package: v.Package, Model: v.Model, IAMPrefix: v.IAMPrefix}
	for _, p := range v.ExtraProtocols {
		pid, ok := protocolTraits()[p]
		if !ok {
			return ServiceConfig{}, failf(StageLoad, subject, "extraProtocols: %q is not a protocol trait id such as aws.protocols#awsQuery", p)
		}
		cfg.ExtraProtocols = append(cfg.ExtraProtocols, pid)
	}
	return cfg, nil
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
