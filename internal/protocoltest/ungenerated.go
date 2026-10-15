package protocoltest

import "github.com/sustinbebustin/understudy/internal/ref"

// Ungenerated is a server-runnable corpus case that no generated table
// holds, and why: the counts of ARCHITECTURE.md 6.4 include it, the
// baseline cannot.
type Ungenerated struct {
	Key    Key
	Reason string
}

// UngeneratedCases returns the server-runnable cases the generator leaves
// out, sorted by key: customizations of services understudy does not
// generate, and an operation no service of the corpus binds.
func UngeneratedCases() []Ungenerated {
	const misc = "Its operation, aws.protocoltests.misc#AcceptHeaderStarService, belongs to no service of the corpus, so no synthetic service carries it."
	return []Ungenerated{
		{
			Key:    Key{Protocol: ref.AWSJSON11, Kind: Request, ID: "MachinelearningPredictEndpoint"},
			Reason: "A customization of Amazon Machine Learning (com.amazonaws.machinelearning), which understudy does not generate: a client sends Predict to the host the request names.",
		},
		{Key: Key{Protocol: ref.RESTJSON1, Kind: Request, ID: "AcceptHeaderStarRequestTest"}, Reason: misc},
		{Key: Key{Protocol: ref.RESTJSON1, Kind: Request, ID: "AcceptHeaderStarStarRequestTest"}, Reason: misc},
		{
			Key:    Key{Protocol: ref.RESTJSON1, Kind: Request, ID: "ApiGatewayAccept"},
			Reason: "A customization of API Gateway (com.amazonaws.apigateway), which understudy does not generate: the Accept header API Gateway requires on every request.",
		},
		{
			Key:    Key{Protocol: ref.RESTJSON1, Kind: Request, ID: "GlacierVersionHeader"},
			Reason: "A customization of Glacier (com.amazonaws.glacier), which understudy does not generate: the X-Amz-Glacier-Version header Glacier requires on every request.",
		},
	}
}
