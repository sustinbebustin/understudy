package apigateway_test

import (
	"net/http"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
)

// setting is one data-plane setting of the spec's scope list: build makes
// an API whose root GET reaches it, deployed to stage s, with a function
// policy that would allow the invoke; the request must answer 501 naming
// it and never run the function.
type setting struct {
	name      string
	spellings []string
	build     func(h *harness) (apiID string)
}

// proxyRoot builds the root GET with an AWS_PROXY integration adjusted by
// adjust, a permission for every route, and returns the API id
// (undeployed).
func (h *harness) proxyRoot(create *apigatewayapi.CreateRestApiInput, adjust func(*apigatewayapi.PutIntegrationInput)) string {
	h.Helper()
	fnARN := h.createFunction("fn")
	h.fnARN = fnARN
	out := must(h, apigatewayapi.OpCreateRestApi, create)
	id, root := *out.Id, *out.RootResourceId
	h.method(id, root, http.MethodGet)
	in := proxyInput(id, root, http.MethodGet, fnARN)
	if adjust != nil {
		adjust(in)
	}
	must(h, apigatewayapi.OpPutIntegration, in)
	h.allow("apigateway", h.executionARN(id)+"/*/*")
	return id
}

// settings are the scope list's "501 at invoke while set" entries (spec 6b
// Scope; enforce matrix rows "Integration credentials" and "Stage-variable
// uri, STREAM, contentHandling, binaryMediaTypes, disableExecuteApiEndpoint,
// PRIVATE endpoint"; apigateway-rest.md 3.4, 3.5, 5.1, 5.2, 5.4).
func settings() []setting {
	plain := func() *apigatewayapi.CreateRestApiInput { return &apigatewayapi.CreateRestApiInput{Name: "t"} }
	deployed := func(h *harness, id string) string {
		h.deploy(id)
		return id
	}
	return []setting{
		{"credentials", nil, func(h *harness) string {
			return deployed(h, h.proxyRoot(plain(), func(in *apigatewayapi.PutIntegrationInput) {
				in.Credentials = new("arn:aws:iam::" + account + ":role/apigw")
			}))
		}},
		{"stage variable", []string{"stageVariables"}, func(h *harness) string {
			id := h.proxyRoot(plain(), func(in *apigatewayapi.PutIntegrationInput) {
				in.Uri = new("arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/arn:aws:lambda:" + region + ":" + account + ":function:${stageVariables.fn}/invocations")
			})
			must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: id, StageName: new("s"), Variables: map[string]string{"fn": "fn"}})
			return id
		}},
		{"STREAM", []string{"responseTransferMode"}, func(h *harness) string {
			return deployed(h, h.proxyRoot(plain(), func(in *apigatewayapi.PutIntegrationInput) {
				in.ResponseTransferMode = new(apigatewayapi.ResponseTransferModeStream)
			}))
		}},
		{"contentHandling", nil, func(h *harness) string {
			return deployed(h, h.proxyRoot(plain(), func(in *apigatewayapi.PutIntegrationInput) {
				in.ContentHandling = new(apigatewayapi.ContentHandlingStrategyConvertToText)
			}))
		}},
		{"binaryMediaTypes", nil, func(h *harness) string {
			return deployed(h, h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", BinaryMediaTypes: []string{"image/png"}}, nil))
		}},
		{"disableExecuteApiEndpoint", nil, func(h *harness) string {
			return deployed(h, h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", DisableExecuteApiEndpoint: true}, nil))
		}},
		{"PRIVATE", nil, func(h *harness) string {
			return deployed(h, h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", EndpointConfiguration: &apigatewayapi.EndpointConfiguration{
				Types: []apigatewayapi.EndpointType{apigatewayapi.EndpointTypePrivate},
			}}, nil))
		}},
	}
}

// Every data-plane setting of the scope list answers 501 at invoke when
// set, naming it, journaled, without running the function, under off,
// advise and enforce (spec 6b acceptance: "test per setting").
func TestSettingsAnswer501InEveryMode(t *testing.T) {
	for mode, m := range modes {
		for _, s := range settings() {
			t.Run(mode+"/"+s.name, func(t *testing.T) {
				runMode(t, m, func(h *harness) {
					id := s.build(h)
					want501(t, h.get(id, "/s/"), s.name, s.name, s.spellings...)
					h.wantJournaled501(s.name)
					h.notInvoked(s.name)
				})
			})
		}
	}
}

// credentials answer 501 before any function policy check: with no
// permission at all the answer is still 501, never the permission denial
// (spec 6b enforce matrix: "before any function policy check").
func TestCredentialsAnswer501BeforeThePermissionCheck(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		id, root := h.api("t")
		h.method(id, root, http.MethodGet)
		in := proxyInput(id, root, http.MethodGet, fnARN)
		in.Credentials = new("arn:aws:iam::" + account + ":role/apigw")
		must(h, apigatewayapi.OpPutIntegration, in)
		h.deploy(id)
		want501(t, h.get(id, "/s/"), "credentials", "credentials without a function policy")
		h.notInvoked("credentials")
	})
}

// binaryMediaTypes are read from the stage's deployment snapshot, while
// disableExecuteApiEndpoint is read live and takes effect at once (spec
// 6b scope list; contract 7.1).
func TestSnapshotAndLiveSettings(t *testing.T) {
	run(t, func(h *harness) {
		id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t"}, nil)
		h.deploy(id)
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: id, PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpAdd, "/binaryMediaTypes/image~1png")}})
		if rec := h.get(id, "/s/"); rec.Code != http.StatusOK {
			t.Errorf("binaryMediaTypes added after the deployment: GET = %d %s, want the deployed snapshot served", rec.Code, rec.Body)
		}
		h.deploy(id)
		want501(t, h.get(id, "/s/"), "binaryMediaTypes", "binaryMediaTypes in the new deployment")
		other := h.proxyRootNamed("u")
		h.deploy(other)
		must(h, apigatewayapi.OpUpdateRestApi, &apigatewayapi.UpdateRestApiInput{RestApiId: other, PatchOperations: []apigatewayapi.PatchOperation{op(apigatewayapi.OpReplace, "/disableExecuteApiEndpoint", "true")}})
		want501(t, h.get(other, "/s/"), "disableExecuteApiEndpoint", "disableExecuteApiEndpoint set live")
	})
}

// proxyRootNamed is proxyRoot for a second API in the same test: the
// function and its permission exist already.
func (h *harness) proxyRootNamed(name string) string {
	h.Helper()
	out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: name})
	h.proxy(*out.Id, *out.RootResourceId, http.MethodGet, h.fnARN)
	h.allow("apigateway-"+name, h.executionARN(*out.Id)+"/*/*")
	return *out.Id
}

// Method authorization (spec 6b enforce matrix, last row): AWS_IAM
// answers 501 naming it, apiKeyRequired 403 (status only: inferred), in
// every mode; neither runs the function.
func TestMethodAuthorization(t *testing.T) {
	for mode, m := range modes {
		t.Run(mode, func(t *testing.T) {
			runMode(t, m, func(h *harness) {
				fnARN := h.createFunction("fn")
				id, root := h.api("t")
				iam := h.resource(id, root, "iam")
				key := h.resource(id, root, "key")
				must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: iam, HttpMethod: "GET", AuthorizationType: "AWS_IAM"})
				must(h, apigatewayapi.OpPutIntegration, proxyInput(id, iam, "GET", fnARN))
				must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: id, ResourceId: key, HttpMethod: "GET", AuthorizationType: "NONE", ApiKeyRequired: true})
				must(h, apigatewayapi.OpPutIntegration, proxyInput(id, key, "GET", fnARN))
				h.allow("apigateway", h.executionARN(id)+"/*/*")
				h.deploy(id)
				want501(t, h.get(id, "/s/iam"), "AWS_IAM", "an AWS_IAM method")
				h.wantJournaled501("an AWS_IAM method")
				if rec := h.get(id, "/s/key"); rec.Code != http.StatusForbidden {
					t.Errorf("an apiKeyRequired method = %d %s, want 403", rec.Code, rec.Body)
				}
				h.notInvoked("method authorization")
			})
		})
	}
}

// HTTP, HTTP_PROXY and AWS integrations are stored and answer 501 at
// invoke naming the type (spec 6b scope).
func TestUnemulatedIntegrationTypes(t *testing.T) {
	run(t, func(h *harness) {
		id, root := h.api("t")
		for part, in := range map[string]*apigatewayapi.PutIntegrationInput{
			"http":  {Type: apigatewayapi.IntegrationTypeHttp, IntegrationHttpMethod: new("GET"), Uri: new("http://example.com")},
			"proxy": {Type: apigatewayapi.IntegrationTypeHttpProxy, IntegrationHttpMethod: new("GET"), Uri: new("http://example.com")},
			"aws":   {Type: apigatewayapi.IntegrationTypeAws, IntegrationHttpMethod: new("POST"), Uri: new("arn:aws:apigateway:" + region + ":sqs:path/" + account + "/q")},
		} {
			res := h.resource(id, root, part)
			h.method(id, res, "GET")
			in.RestApiId, in.ResourceId, in.HttpMethod = id, res, "GET"
			must(h, apigatewayapi.OpPutIntegration, in)
		}
		h.deploy(id)
		want501(t, h.get(id, "/s/http"), "HTTP", "an HTTP integration")
		want501(t, h.get(id, "/s/proxy"), "HTTP_PROXY", "an HTTP_PROXY integration")
		want501(t, h.get(id, "/s/aws"), "AWS", "an AWS integration")
	})
}

// The RestApi policy row of the enforce matrix: off serves without
// evaluating it; advise serves and records an access-unevaluated Advisory
// naming the API; enforce answers 501 naming the resource policy and never
// serves open (spec 6b enforce matrix; contract 7.7 step 6).
func TestResourcePolicyByMode(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"execute-api:Invoke","Resource":"execute-api:/*"}]}`
	for mode, m := range modes {
		t.Run(mode, func(t *testing.T) {
			runMode(t, m, func(h *harness) {
				id := h.proxyRoot(&apigatewayapi.CreateRestApiInput{Name: "t", Policy: new(policy)}, nil)
				h.deploy(id)
				rec := h.get(id, "/s/")
				if m == apigateway.EnforceEnforce {
					want501(t, rec, "policy", "a resource policy under enforce")
					h.notInvoked("a resource policy under enforce")
					return
				}
				if rec.Code != http.StatusOK {
					t.Errorf("a resource policy under %s = %d %s, want the function's 200", mode, rec.Code, rec.Body)
				}
				var advised bool
				for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 1000}).Events {
					if a, ok := e.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryAccessUnevaluated {
						advised = true
					}
				}
				if advised != (m == apigateway.EnforceAdvise) {
					t.Errorf("under %s an access-unevaluated advisory was recorded: %v, want %v", mode, advised, m == apigateway.EnforceAdvise)
				}
			})
		})
	}
}
