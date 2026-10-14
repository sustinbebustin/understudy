package codegen

import (
	"path/filepath"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Destination says where a service's generated package goes and what it
// holds.
//
//sumtype:decl
type Destination interface{ isDestination() }

// APIPackage is a real service's package: internal/api/<svc>api.
type APIPackage struct{}

// CorpusPackage is a synthetic service of the protocol compliance corpus:
// internal/protocoltest/gen/<pkg>, which also holds the service's case
// tables (ARCHITECTURE.md 5.6).
type CorpusPackage struct{}

func (APIPackage) isDestination()    {}
func (CorpusPackage) isDestination() {}

// corpusOrigin names the vendored corpus in generated headers.
const corpusOrigin = "smithy-protocol-tests@1.74.0"

// CorpusServices lists the synthetic services of the corpus the protocol
// layer is proven against (ARCHITECTURE.md 5.6): one per protocol the
// emulated services speak, the awsJson1_0 query-compatible variant, the
// validation and namespace variants, the S3 slice and the rpcv2Cbor
// query-compatible pair (wire-cbor.md 6). ec2Query, rpcv2Json and the API
// Gateway, Glacier and Machine Learning slices serve no emulated service.
func CorpusServices() []ServiceConfig {
	svc := func(id smithy.ShapeID, pkg string) ServiceConfig {
		return ServiceConfig{ID: ref.ServiceID(pkg), Service: id, Package: pkg, Destination: CorpusPackage{}}
	}
	return []ServiceConfig{
		svc("aws.protocoltests.json10#JsonRpc10", "jsonrpc10"),
		svc("aws.protocoltests.json10#QueryCompatibleJsonRpc10", "querycompatiblejsonrpc10"),
		svc("aws.protocoltests.json#JsonProtocol", "jsonprotocol"),
		svc("aws.protocoltests.query#AwsQuery", "awsquery"),
		svc("aws.protocoltests.restjson#RestJson", "restjson"),
		svc("aws.protocoltests.restjson.validation#RestJsonValidation", "restjsonvalidation"),
		svc("aws.protocoltests.restxml#RestXml", "restxml"),
		svc("aws.protocoltests.restxml.xmlns#RestXmlWithNamespace", "restxmlwithnamespace"),
		svc("com.amazonaws.s3#AmazonS3", "s3"),
		svc("smithy.protocoltests.rpcv2Cbor#RpcV2Protocol", "rpcv2protocol"),
		svc("aws.protocoltests.rpcv2cbor#QueryCompatibleRpcV2Protocol", "querycompatiblerpcv2protocol"),
		svc("aws.protocoltests.rpcv2cbor#NonQueryCompatibleRpcV2Protocol", "nonquerycompatiblerpcv2protocol"),
	}
}

// CorpusInputs reads the inputs of one corpus service: the vendored
// flattened AST, overlays/common.json and overlays/protocoltests.json. No IAM
// policy names a synthetic service.
func (r Repo) CorpusInputs(cfg ServiceConfig) (Inputs, error) {
	in := Inputs{Config: cfg, Origin: corpusOrigin, IAM: NoActions{}}
	var err error
	if in.Model, err = r.read("third_party/smithy-protocol-tests/model.json"); err != nil {
		return Inputs{}, err
	}
	for _, name := range []string{"codegen/overlays/common.json", "codegen/overlays/protocoltests.json"} {
		src, err := r.read(name)
		if err != nil {
			return Inputs{}, err
		}
		in.Overlays = append(in.Overlays, src)
	}
	return in, nil
}

// corpusOutputDir is where corpus package pkg is written.
func (r Repo) corpusOutputDir(pkg string) string {
	return filepath.Join(r.Root, "internal", "protocoltest", "gen", pkg)
}
