package ids

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

// APIGatewayAPIID mints an API id from KindAPIGatewayAPIID. It is not
// implemented yet and returns "".
func (s *Source) APIGatewayAPIID() APIGatewayAPIID { return "" }

// APIGatewayRouteID mints a route id from KindAPIGatewayChildID. It is not
// implemented yet and returns "".
func (s *Source) APIGatewayRouteID() APIGatewayChildID { return "" }

// APIGatewayIntegrationID mints an integration id from
// KindAPIGatewayChildID. It is not implemented yet and returns "".
func (s *Source) APIGatewayIntegrationID() APIGatewayChildID { return "" }

// APIGatewayDeploymentID mints a deployment id from KindAPIGatewayChildID.
// It is not implemented yet and returns "".
func (s *Source) APIGatewayDeploymentID() APIGatewayChildID { return "" }

// APIGatewayRequestIDFor derives the apigw-requestid of an execute-api
// call from the call's request id, drawing nothing from any stream, as a
// Lambda invocation's request id is the call's own. It is not implemented
// yet and returns "".
func APIGatewayRequestIDFor(_ RequestID) APIGatewayRequestID { return "" }
