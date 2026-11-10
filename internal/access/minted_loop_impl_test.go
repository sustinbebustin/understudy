package access_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The resource types the iam-loop found minted beyond keys, secrets,
// subscriptions, event source mappings and log streams: a DynamoDB
// stream's label and
// API Gateway's API, route, integration, resource and deployment ids, also
// inside a tags resource. Formats: the Service Authorization Reference
// DynamoDB table (sarDynamo) and API Gateway's ARN reference
// (apigateway-http.md 8.1, whose policy example allows UpdateApi on
// /apis/??????????; apigateway-rest.md 3.10 for REST APIs).
func TestMintedPatternRendersStreamLabelsAndAPIGatewayIDs(t *testing.T) {
	const (
		ddb  = "arn:aws:dynamodb:us-east-1:111111111111:"
		apig = "arn:aws:apigateway:us-east-1::"
		// tagsOf is the tags resource of /apis/r3pmxmplak, encoded as
		// internal/service/apigatewayv2/arns.go tagsARN encodes it.
		tagsOf  = "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Fapis%2Fr3pmxmplak"
		tagsPat = "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Fapis%2F??????????"
	)
	tests := []struct {
		name, arn, pattern string
	}{
		{"dynamodb stream", ddb + "table/orders/stream/2026-10-31T00:32:25.347", ddb + "table/orders/stream/*"},
		{"dynamodb stream of a table named with ?", ddb + "table/a?b/stream/2026-10-31T00:32:25.347", ddb + "table/a${?}b/stream/*"},
		{"dynamodb table", ddb + "table/orders", ""},
		{"dynamodb index", ddb + "table/orders/index/byDate", ""},
		{"api", apig + "/apis/r3pmxmplak", apig + "/apis/??????????"},
		{"api cors", apig + "/apis/r3pmxmplak/cors", apig + "/apis/??????????/cors"},
		{"api routes collection", apig + "/apis/r3pmxmplak/routes", apig + "/apis/??????????/routes"},
		{"route", apig + "/apis/r3pmxmplak/routes/72jz1wk", apig + "/apis/??????????/routes/*"},
		{"integration", apig + "/apis/r3pmxmplak/integrations/a1b2c3d", apig + "/apis/??????????/integrations/*"},
		{"deployment", apig + "/apis/r3pmxmplak/deployments/53lz9l", apig + "/apis/??????????/deployments/*"},
		{"stage keeps its name", apig + "/apis/r3pmxmplak/stages/$default", apig + "/apis/??????????/stages/$default"},
		{"stage access log settings", apig + "/apis/r3pmxmplak/stages/prod/accesslogsettings", apig + "/apis/??????????/stages/prod/accesslogsettings"},
		{"api tags", apig + tagsOf, apig + tagsPat},
		{"stage tags", apig + tagsOf + "%2Fstages%2Fprod", apig + tagsPat + "%2Fstages%2Fprod"},
		{"apis collection", apig + "/apis", ""},
		{"id not of the API form", apig + "/apis/nope", ""},
		{"tags of another region's api", apig + "/tags/arn%3Aaws%3Aapigateway%3Aeu-west-1%3A%3A%2Fapis%2Fr3pmxmplak", ""},

		// REST APIs: the same ids under /restapis, with resource and
		// deployment ids as children (apigateway-rest.md 3.10).
		{"rest api", apig + "/restapis/fugvjdxtri", apig + "/restapis/??????????"},
		{"rest resources collection", apig + "/restapis/fugvjdxtri/resources", apig + "/restapis/??????????/resources"},
		{"rest root resource", apig + "/restapis/fugvjdxtri/resources/svzr2028x8", apig + "/restapis/??????????/resources/*"},
		{"rest method", apig + "/restapis/fugvjdxtri/resources/6sxz2j/methods/GET", apig + "/restapis/??????????/resources/*/methods/GET"},
		{
			"rest integration response",
			apig + "/restapis/fugvjdxtri/resources/6sxz2j/methods/POST/integration/responses/200",
			apig + "/restapis/??????????/resources/*/methods/POST/integration/responses/200",
		},
		{"rest deployments collection", apig + "/restapis/fugvjdxtri/deployments", apig + "/restapis/??????????/deployments"},
		{"rest deployment", apig + "/restapis/fugvjdxtri/deployments/vakw79", apig + "/restapis/??????????/deployments/*"},
		{"rest stage keeps its name", apig + "/restapis/fugvjdxtri/stages/prod", apig + "/restapis/??????????/stages/prod"},
		{
			"rest api tags",
			apig + "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Frestapis%2Ffugvjdxtri",
			apig + "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Frestapis%2F??????????",
		},
		{
			"rest stage tags",
			apig + "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Frestapis%2Ffugvjdxtri%2Fstages%2Fprod",
			apig + "/tags/arn%3Aaws%3Aapigateway%3Aus-east-1%3A%3A%2Frestapis%2F??????????%2Fstages%2Fprod",
		},
		{"restapis collection", apig + "/restapis", ""},
		{"rest id not of the API form", apig + "/restapis/nope", ""},
		{"routes are not rest children", apig + "/restapis/fugvjdxtri/routes/72jz1wk", apig + "/restapis/??????????/routes/72jz1wk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := arn(t, tc.arn)
			pattern, minted := access.MintedPattern(access.Direct{}, a)
			if pattern != tc.pattern || minted != (tc.pattern != "") {
				t.Fatalf("MintedPattern(%s) = %q, %t; want %q", tc.arn, pattern, minted, tc.pattern)
			}
			doc := policydoc.Document{Version: policydoc.Version20121017, Statements: []policydoc.Statement{{
				Effect: policydoc.Allow, Action: policydoc.Action{"x:Y"}, Resource: policydoc.Resource{pattern},
			}}}
			if _, ok := policydoc.Allows(doc, policydoc.Query{Action: "x:Y", Resource: a.String()}).(policydoc.Allowed); minted && !ok {
				t.Errorf("pattern %q does not match the ARN it renders, %s", pattern, tc.arn)
			}
		})
	}
}
