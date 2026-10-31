package apigateway

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The control plane of the tracer bullet (spec 6b ticket 2): enough of
// CreateRestApi, GetRestApi and CreateDeployment for a Lambda proxy on the
// root resource. A member these do not handle yet is refused with 501
// naming it, never ignored.

// createRestAPI creates a REST API with its root resource
// (apigateway-rest.md 3.1, 3.2; defaults recorded, 2.3).
func (s *Service) createRestAPI(_ context.Context, c *kernel.Call, in *apigatewayapi.CreateRestApiInput) (*apigatewayapi.RestApi, *awserr.Error) {
	if err := refuseUnimplemented("CreateRestApi",
		member{"cloneFrom", in.CloneFrom != nil},
		member{"binaryMediaTypes", in.BinaryMediaTypes != nil},
		member{"minimumCompressionSize", in.MinimumCompressionSize != nil},
		member{"apiKeySource", in.ApiKeySource != nil},
		member{"endpointConfiguration", in.EndpointConfiguration != nil},
		member{"policy", in.Policy != nil},
		member{"disableExecuteApiEndpoint", in.DisableExecuteApiEndpoint},
		member{"securityPolicy", in.SecurityPolicy != nil},
		member{"endpointAccessMode", in.EndpointAccessMode != nil},
	); err != nil {
		return nil, err
	}
	if in.Description != nil && *in.Description == "" {
		// Recorded (apigateway-rest.md 2.4).
		return nil, errBadRequest("Description cannot be an empty string")
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	scope := c.Scope()
	id := s.newAPIID(scope)
	api, err := kernel.Write(c, s.cells, scope, func(st *State, now clock.Instant, _ *kernel.Tx) (RestAPI, *awserr.Error) {
		root := string(s.ids.APIGatewayRootResourceID())
		a := RestAPI{
			ID: id, Seq: st.NextSeq, Name: in.Name, Description: clonePtr(in.Description), Version: clonePtr(in.Version), Created: now,
			RootResourceID: root, Resources: []Resource{{ID: root, Path: "/"}}, NextResourceSeq: 1,
			Models: seededModels(), Tags: maps.Clone(in.Tags),
		}
		st.NextSeq++
		st.APIs[id] = a
		// Set again in case a Reset dropped the reservation before the
		// write; the index lock is a leaf, never held while a cell lock is.
		s.index.set(id, scope)
		return a, nil
	})
	if err != nil {
		s.index.release(id)
		return nil, err
	}
	return new(api.output(APIStatusSwitch().Value.AfterCreate)), nil
}

// newAPIID draws a REST API id no account holds and reserves it for scope.
func (s *Service) newAPIID(scope aws.Scope) string {
	for {
		id := string(s.ids.APIGatewayAPIID())
		if s.index.reserve(id, scope) {
			return id
		}
	}
}

// getRestAPI answers one REST API of the caller's account and region.
func (s *Service) getRestAPI(_ context.Context, c *kernel.Call, in *apigatewayapi.GetRestApiInput) (*apigatewayapi.RestApi, *awserr.Error) {
	scope := c.Scope()
	out := readState(s, scope, func(st *State) *apigatewayapi.RestApi {
		a, ok := st.APIs[in.RestApiId]
		if !ok {
			return nil
		}
		// The status a REST API reports once created (switch
		// apigateway.restapi.api-status, APIGW1-03).
		return new(a.output(APIStatusSwitch().Value.AfterCreate))
	})
	if out == nil {
		return nil, errAPINotFound(scope, in.RestApiId)
	}
	return out, nil
}

// output is the API as CreateRestApi, GetRestApi and UpdateRestApi answer
// it, with the recorded defaults of the members it does not hold yet and
// no binaryMediaTypes when it has none (apigateway-rest.md 2.3).
func (a *RestAPI) output(status apigatewayapi.ApiStatus) apigatewayapi.RestApi {
	var binary []string
	if len(a.BinaryMediaTypes) > 0 {
		binary = slices.Clone(a.BinaryMediaTypes)
	}
	return apigatewayapi.RestApi{
		BinaryMediaTypes: binary,
		Id:               new(a.ID), Name: new(a.Name), Description: clonePtr(a.Description), Version: clonePtr(a.Version),
		CreatedDate:  new(a.Created.Wire()),
		ApiKeySource: new(apigatewayapi.ApiKeySourceTypeHeader),
		EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
			Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypeEdge}, IpAddressType: new(apigatewayapi.IpAddressTypeIpv4),
		},
		RootResourceId: new(a.RootResourceID),
		ApiStatus:      new(status),
		// Absent when the API has none (recorded, apigateway-rest.md 2.3).
		Tags: tagsOrNil(a.Tags),
	}
}

// lambdaInvokeURI is API Gateway's URI for invoking a Lambda function
// (apigateway-rest.md 3.4): the function ARN is the submatch.
var lambdaInvokeURI = regexp.MustCompile(`^arn:[a-z-]+:apigateway:[a-z0-9-]+:lambda:path/2015-03-31/functions/(.+)/invocations$`)

// lambdaFunction is the function a Lambda proxy URI names, its qualifier
// kept.
func lambdaFunction(uri string) (aws.ARN, bool) {
	m := lambdaInvokeURI.FindStringSubmatch(uri)
	if m == nil {
		return aws.ARN{}, false
	}
	fn, err := aws.ParseARN(m[1])
	if err != nil || fn.Service != "lambda" || fn.Region == "" || fn.Account == "" {
		return aws.ARN{}, false
	}
	name, qualifier, qualified := strings.Cut(strings.TrimPrefix(fn.Resource, "function:"), ":")
	if !strings.HasPrefix(fn.Resource, "function:") || name == "" || qualified && (qualifier == "" || strings.Contains(qualifier, ":")) {
		return aws.ARN{}, false
	}
	return fn, true
}
