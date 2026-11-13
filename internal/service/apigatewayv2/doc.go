// Package apigatewayv2 is Amazon API Gateway's HTTP APIs: the v2 control
// plane for APIs, routes, integrations, stages, deployments and tags, and
// the execute-api data plane (InvokeApi) that selects a stage and a route
// and runs a Lambda proxy integration with payload format 1.0 or 2.0
// (apigateway-http.md 13). REST APIs are package apigateway's; both sign
// as apigateway on disjoint route tables (ADR 0019).
//
// The service's fidelity switches are the five Switches lists; route
// selection is package route's.
package apigatewayv2
