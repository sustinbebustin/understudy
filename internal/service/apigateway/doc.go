// Package apigateway is Amazon API Gateway's REST APIs (v1): the control
// plane for REST APIs, resources, methods and method responses,
// integrations and integration responses, deployments, stages and tags,
// and the execute-api data plane (InvokeRestApi) that selects a stage, a
// resource and a method over the stage's deployment snapshot and runs a
// Lambda proxy (payload format 1.0) or a MOCK integration
// (apigateway-rest.md 12). HTTP APIs are package apigatewayv2's; both sign
// as apigateway on disjoint route tables (ADR 0019).
package apigateway
