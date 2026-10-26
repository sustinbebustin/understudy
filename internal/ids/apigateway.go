package ids

import (
	"crypto/sha256"
	"encoding/base64"
)

// API Gateway identifier types (apigateway-http.md 2.5). Their shapes are
// inferred from AWS's samples (UNVERIFIED, APIGW2-03).
type (
	// APIGatewayAPIID is an API Gateway API id: 10 lower-case letters and
	// digits, such as r3pmxmplak. HTTP and REST APIs share the namespace,
	// since an execute-api host names an API by its id alone.
	APIGatewayAPIID string
	// APIGatewayChildID is the id of a route, an integration or a
	// deployment of an API: lower-case letters and digits, 7 for a route
	// or an integration and 6 for a deployment.
	APIGatewayChildID string
	// APIGatewayRequestID is the apigw-requestid of an execute-api
	// request: 16 characters of the URL-safe base64 alphabet, the last
	// "=", such as LV7fzho-PHcEJPw=.
	APIGatewayRequestID string
)

const (
	// apigatewayAlphabet is the 36 characters of every API Gateway id.
	apigatewayAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	apigatewayAPIIDLen        = 10 // r3pmxmplak
	apigatewayRouteIDLen      = 7  // 72jz1wk
	apigatewayDeploymentIDLen = 6  // 53lz9l
	// apigatewayRequestIDBytes encode, padded, to 16 base64 characters
	// ending in one "=".
	apigatewayRequestIDBytes = 11
)

// APIGatewayAPIID mints an API id from KindAPIGatewayAPIID. The caller
// redraws one an account already holds.
func (s *Source) APIGatewayAPIID() APIGatewayAPIID {
	return APIGatewayAPIID(s.apigatewayChars(KindAPIGatewayAPIID, apigatewayAPIIDLen))
}

// APIGatewayRouteID mints a route id from KindAPIGatewayChildID.
func (s *Source) APIGatewayRouteID() APIGatewayChildID {
	return APIGatewayChildID(s.apigatewayChars(KindAPIGatewayChildID, apigatewayRouteIDLen))
}

// APIGatewayIntegrationID mints an integration id from
// KindAPIGatewayChildID, as long as a route id: no AWS sample shows an
// integration id's length (UNVERIFIED, APIGW2-03).
func (s *Source) APIGatewayIntegrationID() APIGatewayChildID {
	return APIGatewayChildID(s.apigatewayChars(KindAPIGatewayChildID, apigatewayRouteIDLen))
}

// APIGatewayDeploymentID mints a deployment id from KindAPIGatewayChildID.
func (s *Source) APIGatewayDeploymentID() APIGatewayChildID {
	return APIGatewayChildID(s.apigatewayChars(KindAPIGatewayChildID, apigatewayDeploymentIDLen))
}

// APIGatewayRequestIDFor derives the apigw-requestid of an execute-api
// call from the call's request id, drawing nothing from any stream, as a
// Lambda invocation's request id is the call's own. It is the padded
// URL-safe base64 of the first 11 bytes of the SHA-256 of the call id's
// text, so the header and the event's requestContext.requestId agree and
// distinct calls get distinct values.
func APIGatewayRequestIDFor(call RequestID) APIGatewayRequestID {
	sum := sha256.Sum256([]byte(call))
	return APIGatewayRequestID(base64.URLEncoding.EncodeToString(sum[:apigatewayRequestIDBytes]))
}

// apigatewayChars draws n characters uniformly from apigatewayAlphabet.
// chars cannot, since 36 is no power of two, so this samples by rejection:
// a stream byte of 252 (7 x 36) or more is skipped, and an accepted one is
// taken mod 36, at the cost of an occasional extra draw.
func (s *Source) apigatewayChars(k Kind, n uint) string {
	const limit = 256 - 256%len(apigatewayAlphabet)
	out := make([]byte, 0, n)
	for uint(len(out)) < n {
		for _, r := range s.Draw(k, n-uint(len(out))) {
			if int(r) < limit {
				out = append(out, apigatewayAlphabet[int(r)%len(apigatewayAlphabet)])
			}
		}
	}
	return string(out)
}
