# API Gateway REST APIs (v1): primary-source research

Scope: the REST API (v1) subset that Terraform and SAM-style templates use (rest APIs, resources, methods, integrations, method and integration responses, deployments, stages, request validators, models, gateway responses, tags, account), its data plane (stage invocation, routing, Lambda proxy payload 1.0, MOCK and AWS service integrations, VTL mapping templates), and what the Terraform AWS provider v6.68.0 needs for `terraform apply` and an empty second plan. Sources: the Smithy models pinned at aws/api-models-aws `1cfdfb4bb9ea858e65623e66d73af04bec353201`, the API Gateway Developer Guide and API Reference (Markdown renderings), hashicorp/terraform-provider-aws v6.68.0, LocalStack's archived test suite at `8b9a79f05846835cf4dff63ab7eefdde9df83783` (Apache-2.0) whose snapshots were recorded against AWS, aws-samples/serverless-patterns at `deb84afac5425dfb4cabbc3c33f7a11fa4a71555` (MIT-0), the Smithy protocol tests and the Velocity 1.7 reference. Retrieved 2026-10-25. Nothing here was run against AWS.

## 0. How to read this

| Tag | Meaning |
|---|---|
| [S] | Smithy model at the pinned commit: [api-gateway-2015-07-09.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/api-gateway/service/2015-07-09/api-gateway-2015-07-09.json) (namespace `com.amazonaws.apigateway`, 124 operations) and [apigatewayv2-2018-11-29.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/apigatewayv2/service/2018-11-29/apigatewayv2-2018-11-29.json) (103 operations). Shapes are cited as smithy ids. |
| [D] | AWS documentation: Developer Guide pages under `https://docs.aws.amazon.com/apigateway/latest/developerguide/` and API Reference pages under `https://docs.aws.amazon.com/apigateway/latest/api/`. |
| [R] | Third-party-recorded: a LocalStack snapshot whose test id carries a `last_validated_date` in the matching `*.validation.json`. 225 test ids across 17 validation files under `tests/aws/services/apigateway/` carry one, validated between 2023-02-26 and 2026-02-25 (counted with jq at localstack@8b9a79f). The date given with a citation is the snapshot's `recorded-date`. |
| [R-assert] | An assertion inside an AWS-validated LocalStack test that has no snapshot (status codes, routing outcomes). Same provenance, weaker: only what the assertion checks is recorded. |
| [C] | Client or provider source: hashicorp/terraform-provider-aws v6.68.0, aws-sdk-go-v2. |
| [P] | aws-samples/serverless-patterns at `deb84af`: canonical user templates, evidence of what users write, not of AWS behavior. |

A claim with no AWS document or recording behind it is marked UNVERIFIED with the recording case that settles it (section 13). Code cites this note as `(apigateway-rest.md 5.2)`.

## 1. Findings that change the design

1. **v1 and v2 share one signing name and one endpoint prefix.** Both service shapes carry `aws.auth#sigv4 {"name":"apigateway"}` and `aws.api#service.endpointPrefix: "apigateway"` ([S] `com.amazonaws.apigateway#BackplaneControlService`, `com.amazonaws.apigatewayv2#ApiGatewayV2`). ADR 0019 (accepted 2026-10-26) registers them as two services, `apigateway` and `apigatewayv2`, on the one signing name; the registry accepts the pair because their route tables are disjoint: v2 paths all start `/v2/` and v1 paths never do (section 2.1).
2. **Terraform 6.68.0 polls `apiStatus` and fails on anything else.** `aws_api_gateway_rest_api` waits after create and update for `apiStatus` in `{PENDING, UPDATING}` to become `AVAILABLE`; a missing `apiStatus` is an unexpected state `''` and the apply fails (hashicorp/terraform-provider-aws v6.68.0 internal/service/apigateway/rest_api.go:L870-L889, internal/retry/state.go:L141-L151). No LocalStack snapshot contains `apiStatus` (all predate it), so `GetRestApi` must emit `"apiStatus":"AVAILABLE"` on inferred grounds (section 3.1, APIGW1-03).
3. **Unknown routes answer 403, not 404.** An unmatched resource or method returns `{"message":"Missing Authentication Token"}` with `x-amzn-ErrorType: MissingAuthenticationTokenException` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L1113-L1132, 2024-07-23) and default status 403 ([D] [supported-gateway-response-types](https://docs.aws.amazon.com/apigateway/latest/developerguide/supported-gateway-response-types.html); the routing test does not record the status, but the default `MISSING_AUTHENTICATION_TOKEN` gateway response is recorded as 403, [R] test_apigateway_api.snapshot.json:L2265-L2277, 2024-04-15).
4. **Routing backtracks.** With `/part1` (GET), `/part1/hardcoded-value` (GET) and `/{param+}` (GET), `GET /part1/random-value` is served by the root greedy resource; and when a literal resource lacks the request's method, a greedy sibling that has it serves the request ([R-assert] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.py:L1599-L1748, validated 2024-07-23). A naive "longest literal prefix, then stop" matcher is wrong (section 4.3).
5. **Patch operations are not RFC 6902.** `add /binaryMediaTypes/image~1png` with no `value` appends `image/png`; `add /binaryMediaTypes/-` with value `image/png` appends the literal `-`; `add /binaryMediaTypes` is refused with `Invalid patch path /binaryMediaTypes` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L303-L377 and L478-L537, 2025-07-02). Each resource type has its own path table (section 3.8).
6. **Empty-string templates come back as JSON `null`.** `PutIntegrationResponse` with `responseTemplates {"application/json": ""}` answers `"responseTemplates":{"application/json":null}` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L5326-L5348, 2026-02-12). aws-sdk-go-v2 apigateway v1.52.1 (2026-10-09) fixed a deserialization failure on exactly this, and Terraform maps nil back to `""` (internal/service/apigateway/integration_response.go:L155-L163).
7. **The VTL dialect renders nulls as empty and Java maps as `{k=v}`.** `$input.path("$.json")` on `{"foo":"bar"}` renders `{foo=bar}` while a list renders `[{"foo":"bar"}]`; a null reference renders nothing (not its own source text), and `null == ""` is true ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L1380-L1397, 2025-06-18). This is not stock Velocity output; the engine has to be written to these recordings (section 7.2).
8. **`$input.body` of an empty request is `{}`.** An empty POST rendered `Action=SendMessage&MessageBody={}` and SQS received body `{}` (MD5 `99914b932bd37a50b983c5e7c90ae93b`) ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L1398-L1432, 2025-03-19; test_apigateway_sqs.snapshot.json:L39-L64, 2025-03-19).
9. **AWS integrations with Query services ask for JSON.** An SQS `SendMessage` integration's response template reads `$input.path('$.SendMessageResponse.ResponseMetadata.RequestId')` and works; overriding `integration.request.header.Accept` to `'application/xml'` returns XML ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_sqs.snapshot.json:L2-L10, 2024-07-12; test code test_apigateway_sqs.py:L176-L260). The SNS tutorial shows the same JSON rendering of `ListTopicsResponse` ([D] [getting-started-aws-proxy](https://docs.aws.amazon.com/apigateway/latest/developerguide/getting-started-aws-proxy.html)). understudy's Query codecs must render JSON for `Accept: application/json` before SQS and SNS integrations work (section 6.4).
10. **Lambda proxy errors are 502, configuration errors 500.** A function error or a malformed proxy result returns 502 `{"message":"Internal server error"}`; an AWS_PROXY integration with `integrationHttpMethod GET` returns 500 with the same body, although `INTEGRATION_FAILURE` defaults to 504 in `GetGatewayResponses` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1516-L1523 and L1669-L1797; status codes from the test asserts, test_apigateway_lambda.py:L455 and L1128-L1186).
11. **Terraform's `invoke_url` and `execution_arn` are computed client-side** from the region and the DNS suffix (`https://<id>.execute-api.<region>.amazonaws.com/<stage>`), never read from the API (internal/conns/awsclient.go:L306-L308, internal/service/apigateway/stage.go:L286). understudy cannot make `invoke_url` point at itself; it must serve a host-routed execute-api name and document how to build the local URL (section 4.1).
12. **Deployments are snapshots and deleting a live one fails.** "Every time you update an API, you must redeploy" ([D] [how-to-deploy-api](https://docs.aws.amazon.com/apigateway/latest/developerguide/how-to-deploy-api.html)); `DeleteDeployment` while a stage points at it answers 400 `Active stages pointing to this deployment must be moved or deleted` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L633-L643, 2023-09-07). Terraform's `create_before_destroy` redeployment pattern depends on this ordering (section 9.4).
13. **Control-plane validation answers `ValidationException`, a shape the model does not have.** Enum and pattern failures answer 400 `ValidationException` with the Coral text `1 validation error detected: Value 'wrong' at 'statusCode' failed to satisfy constraint: Member must satisfy regular expression pattern: [1-5]\d\d`; type errors answer `SerializationException` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L4062-L4086 and L3822-L3869). The model declares only `BadRequestException`, `ConflictException`, `LimitExceededException`, `NotFoundException`, `ServiceUnavailableException`, `TooManyRequestsException` and `UnauthorizedException` ([S]).
14. **`body` (OpenAPI import) is a common Terraform pattern.** Two of the four Terraform patterns read here build the whole API from `body` with `x-amazon-apigateway-integration` ([P] apigw-sqs-terraform/main.tf:L68-L128, apigw-dynamodb-terraform/main.tf:L98-L192), which runs `PutRestApi` (internal/service/apigateway/rest_api.go:L273-L327). SAM templates reach API Gateway only through CloudFormation, which understudy does not emulate, and through the same OpenAPI document. `PutRestApi` is the second slice of this item, not the first (section 9.6).

## 2. Control-plane wire protocol

### 2.1 Service identity and the shared signing name

- Protocol `aws.protocols#restJson1`, signing name `apigateway`, endpoint prefix `apigateway`, sdkId `API Gateway`, version `2015-07-09` ([S] `com.amazonaws.apigateway#BackplaneControlService`). The endpoint rule set resolves `https://apigateway.<region>.amazonaws.com` ([S] `smithy.rules#endpointTests`). No operation carries `smithy.api#endpoint` or `hostPrefix` ([S], both models).
- apigatewayv2: restJson1, signing name `apigateway`, endpoint prefix `apigateway`, `smithy.api#auth ["aws.auth#sigv4"]` ([S] `com.amazonaws.apigatewayv2#ApiGatewayV2`). All 103 v2 URIs start with `/v2/` (`/v2/apis`, `/v2/domainnames`, `/v2/tags`, `/v2/vpclinks`, ...); v1 URIs start with `/restapis` (76 operations), `/apikeys`, `/account`, `/tags/{resourceArn}`, `/domainnames`, `/usageplans`, `/vpclinks`, `/clientcertificates`, `/sdktypes`, `/domainnameaccessassociations` and `/rejectdomainnameaccessassociations` ([S] `smithy.api#http` of every operation). The two sets do not overlap, so the two services' route tables are disjoint on signing name `apigateway` (ADR 0019).
- SDK customization: "API Gateway requires that this Accept header is set on all requests", `Accept: application/json` (Smithy protocol test `ApiGatewayAccept`, [smithy-aws-protocol-tests apigateway.smithy L28-L39](https://github.com/smithy-lang/smithy/blob/2d6a0d84dd470ee7c4371d6a575cf8e05ab64fea/smithy-aws-protocol-tests/model/restJson1/services/apigateway.smithy#L28-L39)). understudy lists that case as ungenerated today (internal/protocoltest/ungenerated.go). A server should accept the header and not require it.

### 2.2 Operations in the subset and their HTTP bindings

From `smithy.api#http` of each operation ([S]). Status is the success code.

| Operation | Method and URI | Status |
|---|---|---|
| CreateRestApi | `POST /restapis` | 201 |
| ImportRestApi | `POST /restapis?mode=import` | 201 |
| GetRestApis / GetRestApi | `GET /restapis`, `GET /restapis/{restApiId}` | 200 |
| UpdateRestApi | `PATCH /restapis/{restApiId}` | 200 |
| PutRestApi | `PUT /restapis/{restApiId}` | 200 |
| DeleteRestApi | `DELETE /restapis/{restApiId}` | 202 |
| GetResources / GetResource | `GET /restapis/{restApiId}/resources`, `.../resources/{resourceId}` | 200 |
| CreateResource | `POST /restapis/{restApiId}/resources/{parentId}` | 201 |
| UpdateResource / DeleteResource | `PATCH` / `DELETE .../resources/{resourceId}` | 200 / 202 |
| PutMethod / GetMethod / UpdateMethod / DeleteMethod | `PUT`, `GET`, `PATCH`, `DELETE .../resources/{resourceId}/methods/{httpMethod}` | 201 / 200 / 200 / 204 |
| PutMethodResponse / Get / Update / Delete | `.../methods/{httpMethod}/responses/{statusCode}` | 201 / 200 / **201** / 204 |
| PutIntegration / Get / Update / Delete | `.../methods/{httpMethod}/integration` | 201 / 200 / 200 / 204 |
| PutIntegrationResponse / Get / Update / Delete | `.../integration/responses/{statusCode}` | 201 / 200 / 200 / 204 |
| CreateDeployment / GetDeployments | `POST`, `GET /restapis/{restApiId}/deployments` | 201 / 200 |
| GetDeployment / UpdateDeployment / DeleteDeployment | `.../deployments/{deploymentId}` | 200 / 200 / 202 |
| CreateStage / GetStages | `POST`, `GET /restapis/{restApiId}/stages` (`GetStages` takes `?deploymentId=`) | 201 / 200 |
| GetStage / UpdateStage / DeleteStage | `.../stages/{stageName}` | 200 / 200 / 202 |
| CreateRequestValidator / Get(s) / Update / Delete | `/restapis/{restApiId}/requestvalidators[/{requestValidatorId}]` | 201 / 200 / 200 / 202 |
| CreateModel / Get(s) / Update / Delete | `/restapis/{restApiId}/models[/{modelName}]` | 201 / 200 / 200 / 202 |
| PutGatewayResponse / Get(s) / Update / Delete | `/restapis/{restApiId}/gatewayresponses[/{responseType}]` | 201 / 200 / 200 / 202 |
| TagResource / UntagResource / GetTags | `PUT` / `DELETE` (`?tagKeys=`) / `GET /tags/{resourceArn}` | 204 / 204 / 200 |
| GetAccount / UpdateAccount | `GET`, `PATCH /account` | 200 |

The 201 on `UpdateMethodResponse` and the 202 on most deletes but 204 on method-level deletes are what the model says and what the recordings show (`del-base-method-response` 204, `delete-rest-api` 202; [R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L1125-L1130 and L81-L86).

### 2.3 JSON shapes the codec must get right

- **List members are named `item` on the wire.** `RestApis.items`, `Resources.items`, `Deployments.items`, `Models.items` and `RequestValidators.items` carry `smithy.api#jsonName: "item"`; `Stages` has a member literally named `item` ([S] `com.amazonaws.apigateway#RestApis`, `#Resources`, `#Stages`). LocalStack snapshots show `items` because botocore renames to the member name.
- **`PutIntegrationRequest` swaps names.** The label member `httpMethod` has `jsonName: "requestHttpMethod"` (inert, it is a label) and the body member `integrationHttpMethod` has `jsonName: "httpMethod"` ([S] `com.amazonaws.apigateway#PutIntegrationRequest`). The `Integration` output names it `httpMethod` too ([S] `com.amazonaws.apigateway#Integration$httpMethod`).
- **Timestamps** (`createdDate`, `lastUpdatedDate`) are `smithy.api#Timestamp` with no format trait, so epoch seconds under restJson1 ([S] `com.amazonaws.apigateway#Timestamp`).
- **Defaults returned on create** (recorded): `CreateRestApi` returns `apiKeySource: "HEADER"`, `disableExecuteApiEndpoint: false`, `endpointConfiguration: {"ipAddressType":"ipv4","types":["EDGE"]}` when no endpoint configuration is given (`{"ipAddressType":"dualstack","types":["PRIVATE"]}` for PRIVATE), and omits `description`, `binaryMediaTypes`, `policy` and `tags` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L4618-L4692, 2025-07-04). Recordings from 2024 lack `ipAddressType` (L112-L131), so it was added server-side in between. `UpdateRestApi` responses include `"tags": {}` (L324, L348, L370).
- **Integration defaults** (recorded): every `PutIntegration` answer has `cacheKeyParameters: []`, `cacheNamespace` (the resource id per [D] [API_PutIntegration](https://docs.aws.amazon.com/apigateway/latest/api/API_PutIntegration.html)), `passthroughBehavior: "WHEN_NO_MATCH"` and `timeoutInMillis: 29000`; `HTTP` and `HTTP_PROXY` add `connectionType: "INTERNET"`, while `AWS`, `AWS_PROXY` and `MOCK` omit `connectionType`; `MOCK` omits `httpMethod`, both when none was sent ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L268-L380, 2024-06-06) and when `integrationHttpMethod POST` was sent (L1062-L1083, 2024-11-29; request at test_apigateway_integrations.py:L586-L600); it also omits `uri`, which that first request sent as `http://example.com` (test_apigateway_integrations.py, `test_put_integration_validation`).
- **Stage defaults** (recorded): `cacheClusterEnabled: false`, `cacheClusterStatus: "NOT_AVAILABLE"`, `methodSettings: {}` (an empty object, present), `tracingEnabled: false`, `createdDate`, `lastUpdatedDate`, and no `variables` key when none were set ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L374-L389 and L604-L630, 2024-03-05 and 2023-09-07).
- **Method defaults** (recorded): `PutMethod` answers `apiKeyRequired: false`, `authorizationType`, `httpMethod`; `GetMethod` adds `methodIntegration` (with `integrationResponses` keyed by status code) and `methodResponses` keyed by status code ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L131-L160, 2023-05-26).
- **Null map values**: section 1 finding 6.
- **HAL links**: the API Reference examples show a `_links` object in responses ([D] [API_PutIntegration example](https://docs.aws.amazon.com/apigateway/latest/api/API_PutIntegration.html), [API_CreateRestApi example](https://docs.aws.amazon.com/apigateway/latest/api/API_CreateRestApi.html)). It is outside the model and no recording shows it; SDKs ignore unknown keys, so understudy omits it.
- **The resource policy is returned escaped.** `GetRestApi.policy` is a string whose content is the policy with its quotes backslash-escaped, `{\"Version\":\"2012-10-17\",...}`: Terraform wraps it in quotes and unquotes it (internal/service/apigateway/rest_api.go:L990-L1008), and the AWS-validated LocalStack test decodes it with `unicode_escape` before parsing (test_apigateway_integrations.py:L1055-L1058). A plain JSON string breaks Terraform's read. AWS also rewrites the policy: `"Resource": ["execute-api:/*"]` came back as the string `"arn:aws:execute-api:<region>:<account>:<api-id>/*"` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L45-L77, validated 2025-07-30).

### 2.4 Errors

Modeled error shapes ([S], every one with a single `message` member except where noted):

| Shape | `httpError` | Notes |
|---|---|---|
| `BadRequestException` | 400 | |
| `UnauthorizedException` | 401 | |
| `NotFoundException` | 404 | |
| `ConflictException` | 409 | |
| `LimitExceededException` | 429 | `retryAfterSeconds` bound to header `Retry-After` |
| `TooManyRequestsException` | 429 | `retryAfterSeconds` bound to header `Retry-After` |
| `ServiceUnavailableException` | 503 (server) | `retryAfterSeconds` bound to header `Retry-After` |

Unmodeled codes seen on the wire: `ValidationException` 400 and `SerializationException` 400 ([R], section 1 finding 13). The restJson1 wire form is the `X-Amzn-Errortype` header plus a body with `message` ([wire-rest.md 4.2](wire-rest.md)); for the modeled shapes botocore surfaces both a parsed `Error.Message` and a top-level `message` member, which shows their body key is lower-case `message`. The `ValidationException` and `SerializationException` recordings carry only `Error.Message` (test_apigateway_api.snapshot.json:L4065-L4074, L3848-L3857), so the case of their body key is not shown; UNVERIFIED (APIGW1-01). Whether AWS appends a `:<namespace URL>` suffix to the header value is UNVERIFIED (APIGW1-01); clients strip it either way.

Recorded error texts that clients and Terraform meet (all [R], localstack@8b9a79f tests/aws/services/apigateway/, file and line):

| Case | Code, status | Message | Source |
|---|---|---|---|
| Unknown REST API id | `NotFoundException` 404 | `Invalid API identifier specified <account>:<id>` | test_apigateway_api.snapshot.json:L133-L144, L539-L554 |
| Unknown resource, or a method call on an unknown API | `NotFoundException` 404 | `Invalid Resource identifier specified` | test_apigateway_api.snapshot.json:L1339-L1362 |
| Unknown method | `NotFoundException` 404 | `Invalid Method identifier specified` | test_apigateway_api.snapshot.json:L1104-L1153 |
| Unknown integration | `NotFoundException` 404 | `Invalid Integration identifier specified` | test_apigateway_api.snapshot.json:L4113-L4128 |
| Unknown method or integration response | `NotFoundException` 404 | `Invalid Response status code specified` | test_apigateway_api.snapshot.json:L4129-L4144 |
| Unknown request validator (get) | `NotFoundException` 404 | `Invalid Request Validator identifier specified` | test_apigateway_api.snapshot.json:L2098-L2124 |
| Unknown request validator on PutMethod | `BadRequestException` 400 | `Invalid Request Validator identifier specified` | test_apigateway_api.snapshot.json:L1385-L1394 |
| Unknown model on PutMethod | `BadRequestException` 400 | `Invalid model identifier specified: petModel` | test_apigateway_api.snapshot.json:L1396-L1405 |
| Unknown model (get) | `NotFoundException` 404 | `Invalid model name specified: mySchema` | test_apigateway_api.snapshot.json:L1884-L1895 |
| Bad HTTP method | `BadRequestException` 400 | `Invalid HttpMethod specified. Valid options are GET,PUT,POST,DELETE,PATCH,OPTIONS,HEAD,ANY` | test_apigateway_api.snapshot.json:L1364-L1373 |
| CUSTOM or COGNITO without authorizer | `BadRequestException` 400 | `Invalid authorizer ID specified. Setting the authorization type to CUSTOM or COGNITO_USER_POOLS requires a valid authorizer.` | test_apigateway_api.snapshot.json:L1374-L1383 |
| Same parameter name in two locations | `BadRequestException` 400 | `Parameter names must be unique across querystring, header and path` | test_apigateway_api.snapshot.json:L1188-L1197 |
| Duplicate sibling path part | `ConflictException` 409 | `Another resource with the same parent already has this name: pets` | test_apigateway_api.snapshot.json:L728-L736 |
| Second variable sibling | `BadRequestException` 400 | `A sibling ({proxy+}) of this resource already has a variable path part -- only one is allowed` | test_apigateway_api.snapshot.json:L1032-L1042 |
| Child of a greedy resource | `BadRequestException` 400 | `Cannot create a child of a resource with a greedy path variable: {proxy+}` | test_apigateway_api.snapshot.json:L1043-L1064 |
| Cycle via parentId | `BadRequestException` 400 | `Resources cannot be cyclical.` | test_apigateway_api.snapshot.json:L739-L748 |
| Root update | `BadRequestException` 400 | `Root resource cannot update its pathPart.` / `... its parentId.` | test_apigateway_api.snapshot.json:L4909-L4935 |
| Integration type with no integration method | `BadRequestException` 400 | `Enumeration value for HttpMethod must be non-empty` | test_apigateway_integrations.snapshot.json:L271-L303 |
| AWS_PROXY to a non-Lambda target | `BadRequestException` 400 | `Integrations of type 'AWS_PROXY' currently only supports Lambda function and Firehose stream invocations.` | test_apigateway_integrations.snapshot.json:L383-L393 |
| HTTP URI that is not a URL | `BadRequestException` 400 | `Invalid HTTP endpoint specified for URI` | test_apigateway_integrations.snapshot.json:L394-L415 |
| AWS URI that is not an ARN | `BadRequestException` 400 | `Invalid ARN specified in the request` | test_apigateway_integrations.snapshot.json:L416-L426 |
| AWS URI ARN without `path` or `action` | `BadRequestException` 400 | `AWS ARN for integration must contain path or action` | test_apigateway_integrations.snapshot.json:L427-L437 |
| Bad integration response status code | `ValidationException` 400 | `1 validation error detected: Value 'wrong' at 'statusCode' failed to satisfy constraint: Member must satisfy regular expression pattern: [1-5]\d\d` | test_apigateway_api.snapshot.json:L4062-L4086 |
| Bad integration type | `ValidationException` 400 | `1 validation error detected: Value 'HTTPS_PROXY' at 'putIntegrationInput.type' failed to satisfy constraint: Member must satisfy enum value set: [HTTP, MOCK, AWS_PROXY, HTTP_PROXY, AWS]` | test_apigateway_api.snapshot.json:L3084-L3098 |
| Boolean where a string is expected | `SerializationException` 400 | `class java.lang.Boolean can not be converted to an String` | test_apigateway_api.snapshot.json:L3822-L3869 |
| Empty description on CreateRestApi | `BadRequestException` 400 | `Description cannot be an empty string` | test_apigateway_api.snapshot.json:L146-L227 |
| Compression size out of range | `BadRequestException` 400 | `Invalid minimum compression size, must be between 0 and 10485760` | test_apigateway_api.snapshot.json:L378-L477 |
| Several endpoint types | `BadRequestException` 400 | `Cannot create an api with multiple Endpoint Types.` | test_apigateway_api.snapshot.json:L4457-L4580 |
| PRIVATE with ipv4 | `BadRequestException` 400 | `Only dualstack ipAddressType is supported for Private APIs.` | test_apigateway_api.snapshot.json:L4581-L4617 |
| Deleting a deployment a stage uses | `BadRequestException` 400 | `Active stages pointing to this deployment must be moved or deleted` | test_apigateway_common.snapshot.json:L633-L643 |
| Bad UpdateStage path | `BadRequestException` 400 | `Invalid method setting path: /tags/tag1. Must be one of: [/deploymentId, /description, ...]` (full list in the source) | test_apigateway_common.snapshot.json:L393-L410 |
| `CreateAuthorizer` on an unknown REST API id (no concurrent edit took place) | `ConflictException` 409 | `Unable to complete operation due to concurrent modification. Please try again later.` | test_apigateway_api.snapshot.json:L1077-L1090 (`test_authorizer_crud_no_api`) |

The account id inside `Invalid API identifier specified` is the caller's (LocalStack masks it as `111111111111`). REST API ids are case-sensitive: `GetRestApi` with the id upper-cased answers that 404 (test_apigateway_api.snapshot.json:L112-L144, 2024-04-15).

### 2.5 Pagination

`GetRestApis`, `GetResources`, `GetDeployments` and `GetModels` are `@paginated(inputToken: "position", outputToken: "position", items: "items", pageSize: "limit")` ([S]); `GetStages` is not paginated. "The default value is 25 and the maximum value is 500" for `limit` ([D] [API_GetResources](https://docs.aws.amazon.com/apigateway/latest/api/API_GetResources.html), [API_GetRestApis](https://docs.aws.amazon.com/apigateway/latest/api/API_GetRestApis.html)). `GetResources?embed=methods` embeds full `Method` objects: "the request supports only retrieval of the embedded Method resources this way. The query parameter value must be a single-valued list and contain the `"methods"` string" ([D] API_GetResources); without it `resourceMethods` maps each method to `{}` ([D] [api-gateway-method-settings-method-request](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-method-settings-method-request.html) example). The order of `GetResources` items is not recorded: the LocalStack resource tests sort `items` by path before snapshotting (test_apigateway_api.py:L698). UNVERIFIED (APIGW1-04).

### 2.6 Quotas and fixed limits

From [D] [api-gateway-execution-service-limits-table](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-execution-service-limits-table.html) and [limits](https://docs.aws.amazon.com/apigateway/latest/developerguide/limits.html):

| Limit | Value |
|---|---|
| Regional / private APIs per Region; edge-optimized | 600; 120 |
| Resources per API | 300 (adjustable) |
| Stages per API | 10 (adjustable) |
| Stage variables per stage; key length; value length | 100; 64; 512 |
| Resource policy length | 8192 characters |
| Integration timeout | 50 ms to 29 s (Regional and private may raise it) |
| Payload size | 10 MB; the default `REQUEST_TOO_LARGE` text is `HTTP content length exceeded 10485760 bytes` ([D] supported-gateway-response-types) |
| Header size | 20480 bytes in total |
| Mapping template size; `#foreach` iterations | 300 KB; 1000 |
| Model size per API | 400 KB |
| Method ARN length | 1600 bytes, else `414 Request URI too long` |
| URL length, Regional | 10240 characters |
| Control-plane rates (not adjustable) | CreateRestApi 1 per 3 s (Regional), DeleteRestApi 1 per 30 s, CreateDeployment 1 per 5 s, CreateResource and DeleteResource 5 per s, GetResources 5 per 2 s, other calls 10 per s with burst 40 |

The control-plane rates matter only for the recording queue; understudy should not throttle by default.

## 3. Control-plane semantics

### 3.1 RestApi

- `CreateRestApiRequest`: `name` is required; optional `description`, `version`, `cloneFrom`, `binaryMediaTypes`, `minimumCompressionSize`, `apiKeySource`, `endpointConfiguration`, `policy`, `tags`, `disableExecuteApiEndpoint` (default false), `securityPolicy`, `endpointAccessMode` ([S] `com.amazonaws.apigateway#CreateRestApiRequest`). `RestApi` adds `id`, `createdDate`, `warnings`, `rootResourceId`, `apiStatus` (`UPDATING`, `AVAILABLE`, `PENDING`, `FAILED`) and `apiStatusMessage` ("When the status message is `UPDATING` you can still invoke it") ([S] `com.amazonaws.apigateway#RestApi`, `#ApiStatus`).
- Creating an API creates its root resource `/` with no `parentId` and no `pathPart` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L558-L566).
- Defaults returned: section 2.3. With no `endpointConfiguration` the type is `EDGE` (the `create-with-empty-default` case, L4675-L4692).
- `apiStatus` after `CreateRestApi` and `UpdateRestApi`: Terraform 6.58.0 added the wait "preventing intermittent `BadRequestException: There is already an update in progress` errors" ([hashicorp/terraform-provider-aws CHANGELOG 6.58.0](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/CHANGELOG.md)), so AWS likely passes through `PENDING` or `UPDATING` (inferred: the entry names the error, not the states). How long, and whether `GetRestApis` items carry it, is UNVERIFIED (APIGW1-03). After `DeleteRestApi`, Terraform polls until `GetRestApi` answers `NotFoundException` (rest_api.go:L891-L910).
- Names are not keys. Whether a second `CreateRestApi` with the same name succeeds is UNVERIFIED (APIGW1-14); the plausible answer is yes, since nothing in the API reference ties names to identity and every get is by id.
- `cloneFrom`, `securityPolicy`, `endpointAccessMode`, `minimumCompressionSize` and `policy` have no data-plane effect understudy can honour without IAM, TLS and compression work.

### 3.2 Resources

- `CreateResource` takes `parentId` (label) and `pathPart` ([S] `com.amazonaws.apigateway#CreateResourceRequest`); the answer carries `id`, `parentId`, `path` (the full path) and `pathPart` (201).
- A greedy path variable is `{name+}`; "You can use any string for the greedy path parameter name" ([D] api-gateway-method-settings-method-request). `/parent/{child+}` is accepted ([R] test_apigateway_api.snapshot.json:L1065-L1074). A greedy resource cannot have children, and a resource can have only one variable child (`{x}` or `{x+}`) beside any number of literal children (section 2.4 texts; [D] same page: "A proxy resource cannot have any child resource").
- Path segment characters for imports: "alphanumeric characters, underscores, hyphens, periods, commas, colons, and curly braces. Path parameters must be separate path segments" ([D] [api-gateway-known-issues](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)). Whether `CreateResource` enforces the same set is not recorded; enforce it and keep the text in the validation table (section 12.4).
- `DeleteResource` deletes the subtree: after deleting `/pets`, `GetResources` lists only `/` although `/pets/subpets` existed, and a later `DeleteResource` on the child answers `Invalid Resource identifier specified` ([R] test_apigateway_api.snapshot.json:L555-L670 and L813-L846, 2024-04-15).
- `UpdateResource` supports `replace` on `/pathPart` and `/parentId` only ([D] [patch-operations](https://docs.aws.amazon.com/apigateway/latest/api/patch-operations.html)); moving changes `path` of the whole subtree; recorded errors in section 2.4 and test_apigateway_api.snapshot.json:L672-L812.

### 3.3 Methods

- `PutMethodRequest`: required `authorizationType`; optional `authorizerId`, `apiKeyRequired` (default false), `operationName`, `requestParameters` (map of `method.request.{querystring|path|header}.{name}` to required-flag booleans), `requestModels` (content type to model name, `$default` for any), `requestValidatorId`, `authorizationScopes` ([S] `com.amazonaws.apigateway#PutMethodRequest`; [D] api-gateway-method-settings-method-request).
- `authorizationType` values used in practice: `NONE`, `AWS_IAM`, `CUSTOM`, `COGNITO_USER_POOLS` ([D] same page). The model types it as a plain string, so an unknown value is not a `ValidationException`; what AWS answers for, say, `FOO` is not recorded and not needed for Terraform.
- HTTP methods: `DELETE`, `GET`, `HEAD`, `OPTIONS`, `PATCH`, `POST`, `PUT` and `ANY` (recorded error text, section 2.4).
- `PutMethod` on an existing method replaces it? The recordings only create; Terraform never re-puts a method (it patches, method.go:L175-L295). Treat `PutMethod` as create-or-replace, which is what `PUT` suggests (inferred).
- `DeleteMethod` removes its integration and responses (inferred from the model: they are sub-resources of the method URI).

### 3.4 Integrations

- `PutIntegrationRequest` members: required `type`; optional `integrationHttpMethod` (wire `httpMethod`), `uri`, `connectionType`, `connectionId`, `credentials`, `requestParameters`, `requestTemplates`, `passthroughBehavior` (string), `cacheNamespace`, `cacheKeyParameters`, `contentHandling` (`CONVERT_TO_BINARY`, `CONVERT_TO_TEXT`), `timeoutInMillis`, `tlsConfig`, `responseTransferMode` (`BUFFERED`, `STREAM`), `integrationTarget` ([S] `com.amazonaws.apigateway#PutIntegrationRequest`).
- Types `HTTP`, `AWS`, `MOCK`, `HTTP_PROXY`, `AWS_PROXY` ([S] `com.amazonaws.apigateway#IntegrationType`).
- `integrationHttpMethod` is required for `HTTP`, `HTTP_PROXY` and `AWS`, optional for `MOCK` (recorded text in section 2.4); "For Lambda integrations, you must set the integration method to `POST`" ([D] API_PutIntegration). The control plane accepts `GET` for AWS_PROXY; the invocation then fails with 500 (section 1 finding 10).
- `uri` for `AWS` and `AWS_PROXY`: `arn:aws:apigateway:{region}:{subdomain.service|service}:path|action/{service_api}`. "action can be used for an AWS service action-based API, using an `Action={name}&{p1}={v1}&p2={v2}...` query string ... Alternatively, path can be used for an AWS service path-based API" ([D] API_PutIntegration). Lambda: `arn:aws:apigateway:{region}:lambda:path/2015-03-31/functions/{function-arn}/invocations` (the form used in every recorded Lambda test, e.g. test_apigateway_lambda.py:L175). Stage variables may appear in the URI, for example `function:${stageVariables.lambdaFunction}` (test_apigateway_basic.py:L1380).
- URI validation is recorded (section 2.4): a bare Lambda ARN or a `lambda:test/...` ARN gets `AWS ARN for integration must contain path or action`; `bad-arn:...` gets `Invalid ARN specified in the request`; a `firehose:path/2015-03-31/functions/...` or `lambda:path/random/value/...` URI with AWS_PROXY gets the AWS_PROXY text ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1799-L1857, 2025-03-03).
- `credentials`: "To specify an IAM Role for API Gateway to assume, use the role's Amazon Resource Name (ARN). To require that the caller's identity be passed through from the request, specify the string `arn:aws:iam::\*:user/\*`. To use resource-based permissions on supported AWS services, specify null" ([D] API_PutIntegration).
- `timeoutInMillis`: "Custom timeout between 50 and 29,000 milliseconds. The default value is 29,000" ([D] API_PutIntegration). Terraform allows up to 300000 for `BUFFERED` and 900000 for `STREAM` (internal/service/apigateway/integration.go:L171-L198); section 11.
- `requestParameters` keys are `integration.request.{location}.{name}`; values are `method.request.{location}.{name}` or a static value "enclosed within single quotes" ([D] API_PutIntegration). A boolean value is a `SerializationException` (section 2.4).

### 3.5 Method responses and integration responses

- `MethodResponse`: `statusCode` (pattern `^[1-5]\d\d$`), `responseParameters` (`method.response.header.{name}` to booleans), `responseModels` ([S] `com.amazonaws.apigateway#MethodResponse`, `#StatusCode`).
- `IntegrationResponse`: `statusCode`, `selectionPattern`, `responseParameters`, `responseTemplates`, `contentHandling`. "The status code must map to an existing MethodResponse" ([D] [API_IntegrationResponse](https://docs.aws.amazon.com/apigateway/latest/api/API_IntegrationResponse.html)); "Make sure that you also set up the corresponding error code (`400`) on the method response. Otherwise, API Gateway throws an invalid configuration error response at runtime" ([D] [handle-errors-in-lambda-integration](https://docs.aws.amazon.com/apigateway/latest/developerguide/handle-errors-in-lambda-integration.html)). Whether `PutIntegrationResponse` itself refuses a status with no method response is not recorded.
- `responseParameters` values: "an integration response header value, a static value enclosed within a pair of single quotes, or a JSON expression from the integration response body ... `integration.response.header.{name}` or `integration.response.body.{JSON-expression}` ... without the `$` prefix" ([D] API_IntegrationResponse).
- `PutIntegrationResponse` echoes `selectionPattern` (including `""`) and `responseTemplates` (`{}` when sent empty, values `null` when sent `""`) ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L113-L130; test_apigateway_api.snapshot.json:L5326-L5358).
- Whether `PutIntegration` and `GetIntegration` also return an empty-string `requestTemplates` value as `null` is UNVERIFIED (APIGW1-13); the recordings cover response templates only.
- Runtime selection: section 6.7.

### 3.6 Deployments

- `CreateDeploymentRequest`: `stageName`, `stageDescription`, `description`, `cacheClusterEnabled`, `cacheClusterSize`, `variables`, `canarySettings`, `tracingEnabled` ([S] `com.amazonaws.apigateway#CreateDeploymentRequest`). With `stageName` the stage is created or repointed; without it only the deployment is created (both paths recorded, test_apigateway_common.py:L1493-L1541).
- `Deployment` output: `id`, `description`, `createdDate`, `apiSummary` ([S] `com.amazonaws.apigateway#Deployment`); the recorded `GetDeployment` has only `createdDate` and `id` (test_apigateway_common.snapshot.json:L606-L612).
- A deployment freezes the API: later edits reach callers only after a new deployment ([D] how-to-deploy-api, quoted in section 1).
- Deleting a deployment a stage points at fails (section 2.4). Terraform retries `DeleteDeployment` once when the message contains `Active stages with canary settings pointing to this deployment must be moved or deleted` (internal/service/apigateway/deployment.go:L165-L173).
- `CreateDeployment` on an API with no methods, or with a method that has no integration: UNVERIFIED texts (APIGW1-05). Plausible: `BadRequestException` `The REST API doesn't contain any methods` and `No integration defined for method`.

### 3.7 Stages and stage variables

- Stage names "can only contain alphanumeric characters, hyphens, and underscores. Maximum length is 128 characters" ([D] [API_CreateStage](https://docs.aws.amazon.com/apigateway/latest/api/API_CreateStage.html); [D] api-gateway-known-issues).
- Stage variables: "Variable names can have alphanumeric and underscore characters, and the values must match `[A-Za-z0-9-._~:/?#&=,]+`" ([D] API_CreateStage, API_CreateDeployment). Limits in section 2.6.
- `CreateStage` with an unknown deployment id: `BadRequestException` `Deployment id does not exist` is recorded for the canary path ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_canary.snapshot.json:L299-L308); the plain path, duplicate stage names and `GetStage` on a missing stage are UNVERIFIED (APIGW1-06).
- `UpdateStage` paths (supported ops per [D] patch-operations): `replace` on `/deploymentId` (and `copy` from `/canarySettings/deploymentId`), `/description`, `/cacheClusterEnabled`, `/cacheClusterSize`, `/clientCertificateId`, `/documentationVersion`, `/tracingEnabled`, `/variables/{name}` (`replace` creates or updates, `remove` deletes), `/accessLogSettings/destinationArn` and `/format`, and `/{resourcePath}/{httpMethod}/{setting}` with `*/*` for all methods. "Forward slashes (`/`) in the resource path might be plain or encoded as `~1`" ([D] patch-operations, UpdateStage). The recorded update echoes `methodSettings` keyed `*/*` and `test/GET` with every `MethodSetting` field filled with defaults (`cacheTtlInSeconds: 300`, `throttlingRateLimit: 10000.0`, `unauthorizedCacheControlHeaderStrategy: "SUCCEED_WITH_RESPONSE_HEADER"`, ...) ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L412-L460).

### 3.8 Patch operations

- `PatchOperation`: `op` (`add`, `remove`, `replace`, `move`, `copy`, `test`), `path`, `value`, `from` ([S] `com.amazonaws.apigateway#PatchOperation`, `#Op`). The path is "a JSON Pointer value ... Any slash ("/") character appearing in path names must be escaped with "~1"" ([S] documentation of `PatchOperation$path`). Operations apply "in the order specified in the list" ([S] `ListOfPatchOperation`).
- An op outside the enum is a `ValidationException` that counts every bad entry (`2 validation errors detected: Value 'wrong' at 'updateRestApiInput.patchOperations.1.member.op' ...; Value 'wrong' at 'updateRestApiInput.patchOperations.2.member.op' ...`) ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L4803-L4837). The member index is 1-based.
- Semantics are per resource and per path, from the tables in [D] patch-operations. The subset Terraform sends:

| Operation | Paths and ops |
|---|---|
| UpdateRestApi | `replace /name`, `/description` (also `add`, `remove`), `/apiKeySource`, `/disableExecuteApiEndpoint`, `/minimumCompressionSize`, `/policy`, `/endpointConfiguration/ipAddressType`, `/endpointAccessMode`, `/securityPolicy`; `add` and `remove /binaryMediaTypes/{escaped-type}`; `replace /endpointConfiguration/types/{type}` (Terraform sends `/types/0`, rest_api.go:L486-L497); `add` and `remove /endpointConfiguration/vpcEndpointIds` with the id as `value` |
| UpdateResource | `replace /pathPart`, `/parentId` |
| UpdateMethod | `replace /authorizationType`, `/authorizerId`, `/apiKeyRequired`, `/operationName`, `/requestValidatorId`; `add`, `replace`, `remove /requestParameters/{key}` and `/requestModels/{escaped-content-type}`; `add`, `remove /authorizationScopes`. Terraform also sends `replace /resourceId` (method.go:L180-L186), which the table does not list |
| UpdateIntegration | `replace /uri`, `/httpMethod` (not for MOCK), `/passthroughBehavior`, `/contentHandling`, `/timeoutInMillis`, `/cacheNamespace`, `/connectionType`, `/connectionId`, `/tlsConfig/insecureSkipVerification`, `/responseTransferMode`, `/integrationTarget`; `add`, `replace`, `remove /requestParameters/{key}`, `/requestTemplates/{escaped-content-type}`, `/cacheKeyParameters/{key}`; `/type` is not patchable |
| UpdateMethodResponse | `add`, `replace`, `remove /responseModels/{escaped}`, `/responseParameters/{key}` |
| UpdateIntegrationResponse | `replace /selectionPattern`, `/contentHandling`; `add`, `replace`, `remove /responseParameters/{key}`, `/responseTemplates/{escaped}` |
| UpdateDeployment | `replace /description` |
| UpdateStage | section 3.7 |
| UpdateAccount | `replace /cloudwatchRoleArn` |

- Recorded list-as-object semantics for `binaryMediaTypes`: section 1 finding 5. `replace /binaryMediaTypes/image~1png` with value `image/gif` replaced the element in place order-preserving? The recording shows `["image/png","image/jpeg"]` becoming `["image/jpeg","image/gif"]`: the replaced element moved to the end (test_apigateway_api.snapshot.json:L306-L353). Implement remove-then-append.
- Recorded error texts for unsupported paths come in three shapes depending on the resource: `Invalid patch path /binaryMediaTypes`, `Invalid patch path  '/invalid' specified for op 'replace'. Must be one of: [/parentId, /pathPart]` (two spaces after `path`), and `Invalid patch path  '/pathPart' specified for op 'remove'. Please choose supported operations` (section 2.4 sources; test_apigateway_api.snapshot.json:L672-L812). Unsupported ops on some resources: `Invalid patch operation specified. Must be one of: [replace]` and `Invalid patch operation specified. Must be 'add'|'remove'|'replace'`.

### 3.9 Request validators, models, gateway responses, tags and account

- **Request validators**: `name`, `validateRequestBody`, `validateRequestParameters` (both default false) ([S] `com.amazonaws.apigateway#CreateRequestValidatorRequest`); "Request Validator name cannot be blank" ([R] test_apigateway_api.snapshot.json:L2184-L2261). Runtime behavior in section 4.6.
- **Models**: `name` (required), `contentType` (required), `schema` (JSON Schema draft 4), `description` ([S] `com.amazonaws.apigateway#CreateModelRequest`; [D] api-gateway-known-issues: "API Gateway models are defined using JSON schema draft 4"). Model names "can only contain alphanumeric characters" (same page). Recorded texts: `Model name already exists for this REST API` (409), `Model name must be non-empty`, `Model schema must have at least 1 property or array items defined`, and `DeleteModel` on a referenced model: `Cannot delete model '<name>', is referenced in method request: //ANY` (409) ([R] test_apigateway_api.snapshot.json:L1636-L1883, L1240-L1300). Every new API carries the models `Empty` and `Error`: the console docs reference `Empty` ([D] api-gateway-method-settings-method-request) and recorded tests use `responseModels {"application/json": "Empty"}` without creating it (test_apigateway_sqs.py:L172). That `Error` exists too is UNVERIFIED, folded into APIGW1-02.
- **Gateway responses**: `GetGatewayResponses` lists 21 response types, each a default (`defaultResponse: true`, `responseParameters: {}`, `responseTemplates: {"application/json": "{\"message\":$context.error.messageString}"}` and the default status; `DEFAULT_4XX` and `DEFAULT_5XX` have no `statusCode`) until the API overrides it ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L2262-L2534, 2024-04-15). In that recording 20 entries are defaults; the test had first overridden `MISSING_AUTHENTICATION_TOKEN` (`defaultResponse: false`, 404, L2278-L2295), and its default, `GetGatewayResponse` before the override, is 403 with the same template (L2265-L2277). The listing is sorted by the test (test_apigateway_api.py:L2358), so its order is not recorded. Templates are "not processed by the Velocity Template Language engine" and support only simple substitution of `$context`, `$stageVariables` and `method.request.*` ([D] [api-gateway-gatewayResponse-definition](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-gatewayResponse-definition.html), [api-gateway-mapping-template-reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-mapping-template-reference.html)).
- **Tags**: `TagResource` takes the ARN as an (escaped) label; REST API ARN `arn:aws:apigateway:<region>::/restapis/<id>`, stage ARN `.../restapis/<id>/stages/<stage>` ([D] [arn-format-reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html); Terraform builds the same, rest_api.go:L1010-L1012, stage.go:L665-L667). Terraform reads REST API and stage tags from `GetRestApi.tags` and `GetStage.tags` (rest_api.go:L390, stage.go:L293) and writes them with `TagResource`/`UntagResource` (tags_gen.go:L86-L121). 50 tags per stage ([D] quotas).
- **Account**: `UpdateAccount` with `replace /cloudwatchRoleArn`; Terraform retries for IAM propagation on `The role ARN does not have required permissions` and `API Gateway could not successfully write to CloudWatch Logs using the ARN specified` (internal/service/apigateway/account.go:L102-L114). Two of the serverless-patterns Terraform stacks create `aws_api_gateway_account` ([P] apigw-sqs-terraform/main.tf:L187-L189, apigw-dynamodb-terraform/main.tf:L251).

### 3.10 Identifiers and ARNs

- Documentation examples: REST API ids `fugvjdxtri`, `vaz7da96z6`, `7zw9uyk9kl`, `z0vprf0mdh` (10 lower-case alphanumerics); root resource ids `svzr2028x8` and `3kzxbg5sa2` (10, both with path `/`); child resource ids `6sxz2j`, `rjkmth`, `xdsvhp`, `x3o5ih` (6); deployment id `vakw79` (6); request validator `jgpyy6` (6) ([D] API_PutIntegration, [API_GetResources](https://docs.aws.amazon.com/apigateway/latest/api/API_GetResources.html) example, API_CreateStage example, api-gateway-method-settings-method-request, handle-errors-in-lambda-integration). No example shows a model id. The exact alphabet and length rules are UNVERIFIED (APIGW1-02); LocalStack masks every id.
- execute-api ARNs for permissions: `arn:aws:execute-api:{region}:{account-id}:{api-id}/{stage}/{http-method}/{resource-path}` ([D] arn-format-reference). Terraform's `execution_arn` is `arn:aws:execute-api:<region>:<account>:<api-id>` and the stage's is `.../<api-id>/<stage>` (rest_api.go:L1014-L1016, stage.go:L669-L671); `aws_lambda_permission.source_arn` is usually `${execution_arn}/*/*`.

Control-plane IAM resources. As for HTTP APIs (`apigateway-http.md` 8.1), the action of a call is `apigateway:<the method of its HTTP binding>` (2.2): "The API-managing `Action` expression has the format `apigateway:{action}`, where {action} is one of the following API Gateway actions: GET, POST, PUT, DELETE, PATCH (to update resources), or *", and "API Gateway resources have the following ARN format: `arn:aws:apigateway:{region}::{resource-path-specifier}`" ([D] [How Amazon API Gateway works with IAM](https://docs.aws.amazon.com/apigateway/latest/developerguide/security_iam_service-with-iam.html), Actions and Resources). `third_party/aws-service-reference/actions.json` (retrieved 2026-10-26) lists `apigateway:<method>` first for every operation below, plus `apigateway:PUT` and `apigateway:UpdateRestApiPolicy` on CreateRestApi, `apigateway:UpdateRestApiPolicy` on UpdateRestApi, `apigateway:PUT` on CreateStage, `iam:PassRole` on PutIntegration and UpdateIntegration, and `apigateway:POST`, `apigateway:PUT` and `apigateway:PATCH` beside TagResource's and UntagResource's methods; the method is the primary action (`iamPrimary`, as in `codegen/services/apigatewayv2.json`). The resource of a call is the ARN whose path is the operation's URI (2.2): every row below names a row of the ARN reference's "REST API resources" table ([D] [ARN reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html); read in the archived Markdown mirror awsdocs/amazon-api-gateway-developer-guide at `fbdb3eb6ae6e2bd89c59513e5ea414b002594a24`, doc_source/arn-format-reference.md, because the live page renders only in a browser). Form: collection for creates and lists, item for one resource, tags for the tag operations. Grounds: "path" where the ARN reference row whose path is the URI is the only source; open ones are UNVERIFIED with APIGW1-22.

| Operations | Action | Form | Resource (after `arn:aws:apigateway:<region>::`) | ARN reference row | Grounds |
|---|---|---|---|---|---|
| CreateRestApi / GetRestApis | POST / GET | collection | `/restapis` | RestApis | path |
| GetRestApi, UpdateRestApi, DeleteRestApi | GET, PATCH, DELETE | item | `/restapis/<api-id>` | RestApi | path |
| GetResources | GET | collection | `/restapis/<api-id>/resources` | Resources | path |
| CreateResource | POST | item (of the parent) | `/restapis/<api-id>/resources/<parent-id>` | Resource | path: the URI names the parent, so the parent's item row; whether AWS evaluates the parent's Resource or the Resources collection is UNVERIFIED (APIGW1-22) |
| GetResource, UpdateResource, DeleteResource | GET, PATCH, DELETE | item | `/restapis/<api-id>/resources/<resource-id>` | Resource | path |
| PutMethod, GetMethod, UpdateMethod, DeleteMethod | PUT, GET, PATCH, DELETE | item | `/restapis/<api-id>/resources/<resource-id>/methods/<http-method>` | Method | path |
| PutMethodResponse, GetMethodResponse, UpdateMethodResponse, DeleteMethodResponse | PUT, GET, PATCH, DELETE | item | `/restapis/<api-id>/resources/<resource-id>/methods/<http-method>/responses/<status-code>` | MethodResponse | path |
| PutIntegration, GetIntegration, UpdateIntegration, DeleteIntegration | PUT, GET, PATCH, DELETE | item | `/restapis/<api-id>/resources/<resource-id>/methods/<http-method>/integration` | Integration | path |
| PutIntegrationResponse, GetIntegrationResponse, UpdateIntegrationResponse, DeleteIntegrationResponse | PUT, GET, PATCH, DELETE | item | `/restapis/<api-id>/resources/<resource-id>/methods/<http-method>/integration/responses/<status-code>` | IntegrationResponse | path |
| CreateDeployment, GetDeployments | POST, GET | collection | `/restapis/<api-id>/deployments` | Deployments | path; whether a CreateDeployment with `stageName` also needs a permission on the stage it creates or repoints is UNVERIFIED (APIGW1-22) |
| GetDeployment, UpdateDeployment, DeleteDeployment | GET, PATCH, DELETE | item | `/restapis/<api-id>/deployments/<deployment-id>` | Deployment | path |
| CreateStage, GetStages | POST, GET | collection | `/restapis/<api-id>/stages` | Stages | path |
| GetStage, UpdateStage, DeleteStage | GET, PATCH, DELETE | item | `/restapis/<api-id>/stages/<stage-name>` | Stage | path |
| TagResource, GetTags, UntagResource | PUT, GET, DELETE | tags | `/tags/<url-encoded resource ARN>` | Tags (not in the archived mirror; `apigateway-http.md` 8.1 reads it in the live page's REST table) | UNVERIFIED (APIGW1-22): the form, which bytes are encoded, and a second pair on the tagged resource itself: "To get the tags for a resource, the user must have `GET` permissions for that resource. To update the tags for a resource, the user must have `PATCH` permissions for that resource" ([D] [tags and IAM](https://docs.aws.amazon.com/apigateway/latest/developerguide/apigateway-tagging-iam-policy.html), "Allow tagging operations") |
| `InvokeRestApi` (raw) | none | | | | 12.2 |

## 4. Data plane: invoking a deployed stage

### 4.1 Invoke URL and what understudy can serve

- "The base URL for REST APIs is in the following format: `https://{api-id}.execute-api.{region}.amazonaws.com/{stage}/`" ([D] [how-to-call-api](https://docs.aws.amazon.com/apigateway/latest/developerguide/how-to-call-api.html)). The first path segment is the stage name; the rest is the resource path.
- Invocation is not a modeled operation: there is no execute-api Smithy model. IAM controls it with `execute-api:Invoke` on the execute-api ARN ([D] [api-gateway-control-access-using-iam-policies-to-invoke-api](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-control-access-using-iam-policies-to-invoke-api.html)).
- Terraform computes `invoke_url` itself (section 1 finding 11). understudy has to accept a host `{api-id}.execute-api.{region}.<base>` like the function URL host (ARCHITECTURE.md 4.1 rule 2) plus a path fallback; the names are decisions in section 12.6.

### 4.2 Path normalization (recorded)

From the recorded Lambda proxy events for a `/{proxy+}` resource on stage `stage` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json, 2024-08-02; request paths from test_apigateway_lambda.py:L205-L297):

| Request path after the host | `path` | `pathParameters.proxy` | `requestContext.path` | Lines |
|---|---|---|---|---|
| `/stage/proxy-value` | `/proxy-value` | `proxy-value` | `/stage/proxy-value` | L5-L121 |
| `/stage/proxy-value/` | `/proxy-value/` | `proxy-value` (trailing slash dropped) | `/stage/proxy-value/` | L123-L239 |
| `/stage/proxy-value//double-slash` | `/proxy-value//double-slash` | `proxy-value//double-slash` | same plus stage | L241-L357 |
| `//stage/proxy-value` | `/proxy-value` | `proxy-value` | `/stage/proxy-value` | L359-L475 |
| `/stage//proxy-value` | `/proxy-value` | `proxy-value` | `/stage//proxy-value` | L477-L593 |
| `/stage//hardcoded` (literal resource `/hardcoded`) | `/hardcoded` | `pathParameters: null` | `/stage//hardcoded` | L1238-L1355 |
| `/stage/proxy-value/api/user/test%2Balias@gmail.com/plus/test+alias@gmail.com` | the same, not decoded | the same, not decoded | the same | L843-L959 |

Leading duplicate slashes before the stage or before the resource path are dropped for matching and for `path`; interior ones are kept; a trailing slash stays in `path` and `requestContext.path` but not in the greedy parameter. The path is passed to Lambda percent-encoded as received. (The known-issues page says "REST APIs decode URL-encoded request parameters before passing them to backend integrations", which the query string below obeys and the path does not; section 11.)

### 4.3 Resource matching

- "a method request against a specific resource takes precedence over a method request against a generic resource at the same level of the resource hierarchy" ([D] api-gateway-method-settings-method-request), with the documented table: for `ANY /{proxy+}`, `GET /pets/{proxy+}`, `GET /pets/dog`, the request `GET /pets/dog` selects `GET /pets/dog`, `GET /pets/cats` selects `GET /pets/{proxy+}`, `GET /animals` selects `GET /{proxy+}`.
- Backtracking and method-aware fallback are recorded (section 1 finding 4). In the first recorded API (`/test` POST, `/test/any` ANY, `/{proxy+}` OPTIONS): `OPTIONS /test` answered from `/{proxy+}`, `POST /test` from `/test`, `OPTIONS` and `GET /test/any` from `ANY /test/any` (test_apigateway_common.py:L1599-L1676).
- `{param}` matches exactly one non-empty segment; `{proxy+}` matches one or more segments, and a request with a trailing slash still matches it, with the slash dropped from the parameter (section 4.2). Whether `{proxy+}` at `/` also serves the bare stage root `/stage` is not recorded; the docs list `/produce` but not `/` as examples ([D] set-up-lambda-proxy-integrations). UNVERIFIED (APIGW1-07): which resource serves `/stage` and `/stage/`; the plausible rule is the root resource only.
- Nothing matched: 403 `Missing Authentication Token` (section 1 finding 3). That covers a method missing on a matched resource with no fallback (`POST /existing` where only GET exists, test_apigateway_common.py:L1785-L1790).

### 4.4 Method matching and ANY

`ANY` "represents any HTTP method that is to be supplied at run time ... `DELETE`, `GET`, `HEAD`, `OPTIONS`, `PATCH`, `POST`, and `PUT`" ([D] api-gateway-method-settings-method-request). When a resource has both an explicit method and `ANY`, which wins is UNVERIFIED (APIGW1-08); the plausible rule is explicit first. `HEAD` against a resource with only `GET` is also UNVERIFIED (APIGW1-08). `$context.httpMethod` and the proxy `httpMethod` carry the real verb, not `ANY` (the recorded `ANY` proxy events show `GET` and `POST`).

### 4.5 Parameter decoding

From the recorded events ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L961-L1236; inputs at test_apigateway_lambda.py:L253-L297):

- Query values are form-decoded: `email=test%2Balias@gmail.com` gives `test+alias@gmail.com`, `plus=test+alias@gmail.com` gives `test alias@gmail.com`, `dateTimeOffset=...123456+00:00` gives `...123456 00:00` (L1064-L1069). (`zhash=abort/#` arriving as `abort/` is the client dropping the fragment, not API Gateway.)
- Repeated keys: `queryStringParameters` keeps the last value (`category: books`, `price: 30`) and `multiValueQueryStringParameters` keeps all (L1186-L1203). The documented example agrees: `parameter2=value1&parameter2=value2` gives `"parameter2": "value2"` ([D] [set-up-lambda-proxy-integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html)).
- No query string: both maps are `null` (L83, L87).
- Header names keep their case (`tEsT-HEADeR`, L26), except that a request header sent as `aUThorization` arrived as `Authorization` (L1112); recorded on an EDGE API, so CloudFront may be the cause. "Header names and query parameters are processed in a case-sensitive way" ([D] api-gateway-known-issues).
- `$input.params(x)` searches "the path, query string, or header value (searched in that order)" ([D] api-gateway-mapping-template-reference).
- Query strings must URL-encode `|`, `{`, `}`; a `;` splits the data; values cannot contain `%%` ([D] api-gateway-known-issues, how-to-call-api).

### 4.6 Method request validation

With a validator enabled, missing required parameters answer `{"message": "Missing required request parameters: [x-header-param, issuer, qs1]"}` (names in header, path, query order as recorded) and a body failing the model answers `{"message": "Invalid request body"}` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L54-L105, 2024-01-10). The statuses come from the gateway response defaults: `BAD_REQUEST_PARAMETERS` 400 and `BAD_REQUEST_BODY` 400 ([D] supported-gateway-response-types). `maxItems` and `minItems` "are not included in simple request validation" for imported models ([D] api-gateway-known-issues).

### 4.7 Gateway responses and error bodies

Every error API Gateway generates itself is a gateway response: status from the type table, body from the type's template, default `{"message":$context.error.messageString}` (section 3.9), plus `x-amzn-ErrorType`.

| Type | Default status | Recorded body and `x-amzn-ErrorType` |
|---|---|---|
| `MISSING_AUTHENTICATION_TOKEN` (unknown route or method) | 403 | `{"message":"Missing Authentication Token"}`, `MissingAuthenticationTokenException` (section 1 finding 3) |
| Lambda proxy failure | 502 | `{"message": "Internal server error"}` (section 5.3) |
| Configuration failure (`API_CONFIGURATION_ERROR`, invalid endpoint) | 500 | `{"message":"Internal server error"}`, `InternalServerErrorException`, `Content-Length: 36` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L554-L582; test_apigateway_api.snapshot.json:L4936-L4971, 2025-09-29) |
| `BAD_REQUEST_PARAMETERS`, `BAD_REQUEST_BODY` | 400 | section 4.6 |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | UNVERIFIED message (APIGW1-09) |
| `INTEGRATION_TIMEOUT` | 504 | UNVERIFIED message (APIGW1-10) |
| `REQUEST_TOO_LARGE` | 413 | `HTTP content length exceeded 10485760 bytes` ([D]); "Customizing the default gateway response for `413 REQUEST_TOO_LARGE` isn't currently supported" ([D] api-gateway-known-issues) |
| Unknown stage | | UNVERIFIED (APIGW1-07) |

`DEFAULT_4XX` and `DEFAULT_5XX` are the fallbacks; changing their status "changes the status codes of all other `4XX` responses to the new value" ([D] supported-gateway-response-types).

### 4.8 Response headers API Gateway adds

Recorded on successful and failed invocations: `x-amzn-RequestId`, `x-amz-apigw-id`, `X-Amzn-Trace-Id` (successful proxy responses), `Content-Type: application/json` when the backend sets none, `Date`, `Connection` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1675-L1708; test_apigateway_integrations.snapshot.json:L904-L913). "API Gateway includes a `Content-Type` header for all integration responses. By default, the content type is `application/json`" ([D] api-gateway-known-issues). The same page lists the headers that are dropped or remapped to `X-Amzn-Remapped-<name>` (for example `WWW-Authenticate`, `Server`, `Date` on responses); the remapping is recorded for HTTP integrations (`x-amzn-Remapped-WWW-Authenticate`, test_apigateway_integrations.snapshot.json:L550).

## 5. Lambda proxy integration (AWS_PROXY, payload format 1.0)

### 5.1 Event shape

Top-level keys, as recorded for a GET on `ANY /{proxy+}` with authorization `NONE` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L5-L121, 2024-08-02):

```
{
  "body": null,
  "headers": {"Accept": "*/*", "Host": "...", "User-Agent": "...", "X-Amzn-Trace-Id": "...", "X-Forwarded-For": "...", "X-Forwarded-Port": "...", "X-Forwarded-Proto": "...", ...},
  "httpMethod": "GET",
  "isBase64Encoded": false,
  "multiValueHeaders": {"Accept": ["*/*"], ...},
  "multiValueQueryStringParameters": null,
  "path": "/proxy-value",
  "pathParameters": {"proxy": "proxy-value"},
  "queryStringParameters": null,
  "requestContext": {
    "accountId": "<owner account>", "apiId": "<api-id>", "deploymentId": "<deployment-id>",
    "domainName": "<host>", "domainPrefix": "<api-id>", "extendedRequestId": "...", "httpMethod": "GET",
    "identity": {"accessKey": null, "accountId": null, "caller": null, "cognitoAuthenticationProvider": null,
                 "cognitoAuthenticationType": null, "cognitoIdentityId": null, "cognitoIdentityPoolId": null,
                 "principalOrgId": null, "sourceIp": "...", "user": null, "userAgent": "...", "userArn": null},
    "path": "/stage/proxy-value", "protocol": "HTTP/1.1", "requestId": "<uuid>",
    "requestTime": "...", "requestTimeEpoch": ..., "resourceId": "<resource-id>", "resourcePath": "/{proxy+}", "stage": "stage"
  },
  "resource": "/{proxy+}",
  "stageVariables": null
}
```

- The recorded EDGE event also carries `CloudFront-*`, `Via` and `X-Amz-Cf-Id` headers; a REGIONAL API does not pass through CloudFront (inferred from the endpoint types), so understudy omits them.
- `requestContext` has no `authorizer` key for `NONE`; the documented example's `"authorizer": {"claims": null, "scopes": null}` and `identity.clientCert` belong to other configurations ([D] set-up-lambda-proxy-integrations). "When the authorization type is `AWS_IAM`, the authorized user information includes `$context.identity.*` properties" (same page).
- `requestTime` is CLF `dd/MMM/yyyy:HH:mm:ss +-hhmm`, `requestTimeEpoch` milliseconds ([D] api-gateway-mapping-template-reference); both are timestamps AWS assigns, so they come from the emulator clock.
- `body` is `null` for a GET with no body. A POST JSON body arrives as the raw string (the recorded echo parses it, L1105-L1108). A POST with an empty body, and which content types are base64 (`isBase64Encoded: true` when the content type matches `binaryMediaTypes`, [D] [api-gateway-payload-encodings](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-payload-encodings.html)), are UNVERIFIED in detail (APIGW1-16).
- `stageVariables` is `null` when the stage has none.

### 5.2 Response mapping

The function must return `{"isBase64Encoded", "statusCode", "headers", "multiValueHeaders", "body"}`; "If the function output is of a different format, API Gateway returns a `502 Bad Gateway` error response" ([D] set-up-lambda-proxy-integrations). Recorded outcomes for a function that returns the request body verbatim ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1669-L1797, 2024-11-15; inputs and expected statuses at test_apigateway_lambda.py:L1113-L1186):

| Function result | Status | Body or headers |
|---|---|---|
| `{"statusCode": 200}` | 200 | empty body, `Content-Type: application/json` |
| `{"statusCode": 200, "headers": {"test-header": "value", "header-bool": true}}` | 200 | `header-bool: true` (non-string values stringified) |
| `{"statusCode": 200, "headers": null}` | 200 | no extra headers |
| `{"statusCode": 200, "wrongValue": "value"}` (an extra key) | 502 | `{"message": "Internal server error"}` |
| `{"statusCode": 200, "body": "dGVzdC1kYXRh", "isBase64Encoded": true}` with no binary media types | 200 | `dGVzdC1kYXRh` (not decoded) |
| `{"statusCode": 200, "multiValueHeaders": {"test-multi": ["value1", "value2"]}}` | 200 | `test-multi: value1, value2` |
| `multiValueHeaders {"test-multi": ["value-multi"]}` plus `headers {"test-multi": "value-solo"}` | 200 | `test-multi: value-multi, value-solo` |
| the same with the multi-value key spelled `tesT-Multi` | 200 | `tesT-Multi: value-multi, value-solo` (merged case-insensitively, first spelling kept) |
| `{"statusCode": 200, "multiValueHeaders": {"test-multi-invalid": "value1"}}` (not a list) | 502 | `{"message": "Internal server error"}` |
| `{"statusCode": "test"}` | 502 | same |
| `{"statusCode": "201"}` (a string) | 201 | empty body |
| `"justAString"` | 502 | same |
| `{"headers": {"test-header": "value"}}` (no `statusCode`) | 200 | empty body |

The documented merge rule covers an identical key-value pair in both maps: "If the same key-value pair is specified in both, only the values from `multiValueHeaders` will appear in the merged list" ([D] set-up-lambda-proxy-integrations). The recording covers the same key with different values, and both values are kept, with the `multiValueHeaders` values first. The two do not conflict; an identical pair is deduplicated per the docs, not recorded (section 11).

### 5.3 Failures

- A function error (unhandled exception) answers 502 `{"message": "Internal server error"}` (the `invoke-api-empty-response` case is a `TypeError` in the function, L1714-L1718).
- `integrationHttpMethod GET` on AWS_PROXY answers 500 with the same body (section 1 finding 10). That test had first put an `INTEGRATION_FAILURE` gateway response with no status code; its comment reads "when the user does not update the status code, it returns 500, unlike what the documentation and APIGW returns when calling `GetGatewayResponse`" (test_apigateway_lambda.py:L427-L437).
- Integration timeout and a function without invoke permission: UNVERIFIED (APIGW1-10, APIGW1-15). Plausible: 504 `{"message": "Endpoint request timed out"}`, and 500 `Internal server error` with execution-log text "Invalid permissions on Lambda function".

### 5.4 How API Gateway invokes the function

- Synchronous Lambda `Invoke` (the 29 s integration timeout bounds it, section 2.6). The function is named by the ARN inside the URI; a qualifier or alias ARN selects the version (inferred: the URI path is Lambda's `Invoke` URI, and no recording here uses a qualified ARN).
- Permission: either `credentials` names a role that may `lambda:InvokeFunction`, or the function's resource policy allows principal `apigateway.amazonaws.com` with a `SourceArn` such as `arn:aws:execute-api:<region>:<account>:<api-id>/*/HTTP_METHOD/resource` ([D] [stage-variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/stage-variables.html) `add-permission` example; [D] [set-up-lambda-proxy-integration-using-cli](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integration-using-cli.html): "Instead of supplying an IAM role for `credentials`, you can use the add-permission command ... This is what the API Gateway console does").

## 6. Non-proxy integrations

### 6.1 Integration request pipeline

For `AWS`, `HTTP` and `MOCK` (inferred order from the docs cited in each step):

1. Match the route (section 4.3) and validate (section 4.6).
2. Map `requestParameters` into integration headers, path and query (section 3.4).
3. Pick the request template by the request `Content-Type`; with no `Content-Type` "API Gateway defaults to `application/json`" ([D] [integration-passthrough-behaviors](https://docs.aws.amazon.com/apigateway/latest/developerguide/integration-passthrough-behaviors.html)). Recorded: a request with no `Content-Type` used the `application/json` template under `passthroughBehavior NEVER` (test_apigateway_sqs.py:L56-L111, payload `None` case). Whether media-type parameters (`; charset=utf-8`) and case are ignored when matching is UNVERIFIED (folded into APIGW1-09).
4. Apply passthrough behavior (section 6.2), render the template (section 7), apply `$context.requestOverride` (section 6.8).
5. Call the backend (sections 6.3 to 6.6), then select and map the integration response (section 6.7).

### 6.2 Passthrough behavior and 415

From [D] integration-passthrough-behaviors (three tables, nine cases each):

| Templates defined | Request content type | `WHEN_NO_MATCH` | `WHEN_NO_TEMPLATES` | `NEVER` |
|---|---|---|---|---|
| one, for `application/json` | none (defaults to json) or `application/json` | template | template | template |
| one, for `application/json` | `application/xml` | pass through | 415 | 415 |
| one, for `application/xml` | none or `application/json` | pass through | 415 | 415 |
| none | any | pass through | pass through | 415 |

`WHEN_NO_MATCH` is the default (recorded on every `PutIntegration`, section 2.3). The recorded MOCK runs with each of the three behaviors and a JSON request all answered 200 (localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_basic.snapshot.json:L154-L262, 2024-05-30). The 415 body text is UNVERIFIED (APIGW1-09).

### 6.3 MOCK

- The integration response is selected by the `statusCode` the request template renders: request template `{"statusCode":200}` selects the 200 integration response ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_basic.snapshot.json:L154-L161; templates at test_apigateway_basic.py:L1697-L1724). The parse is lenient: `{statusCode: 200}` (unquoted key, emitted by a CDK CORS construct) is accepted and works ([R] test_apigateway_integrations.snapshot.json:L1062-L1083, 2024-11-29; test code test_apigateway_integrations.py:L558-L629). `selectionPattern` `2\d{2}` matched that status.
- With no response template the body is empty (`b''`) and the status is the selected one (test_apigateway_integrations.py:L620-L628; snapshot L1097-L1105).
- Response templates on MOCK see the method request through `$input.params()`: `{"statusCode": 200, "id": $input.params().path.id}` rendered `{"id": 42, "statusCode": 200}` for `/42` (test_apigateway_basic.snapshot.json:L154-L161).
- The common CORS pattern (an `OPTIONS` MOCK whose integration response sets `method.response.header.Access-Control-Allow-Origin` to `'*'`) uses only static `responseParameters` (section 3.5).

### 6.4 AWS service integrations

- `path` URIs address a service resource path: SQS `arn:aws:apigateway:<region>:sqs:path/<account>/<queue>` with `integration.request.header.Content-Type = 'application/x-www-form-urlencoded'` and template `Action=SendMessage&MessageBody=$input.body` (section 8.1); SNS `arn:aws:apigateway:<region>:sns:path//` with a form template (section 8.2); S3 `s3:path/{bucket}/{key}` ([D] API_PutIntegration).
- `action` URIs name an action: `dynamodb:action/PutItem`, `states:action/StartExecution`, `events:action/PutEvents`, `kinesis:action/ListStreams` ([D] API_PutIntegration example; [R] sections 8.3 to 8.5). For DynamoDB and Step Functions the recordings sent a JSON template with no header mapping and the service answered the action (so API Gateway supplies the JSON protocol target and content type itself); the EventBridge recording and pattern set `X-Amz-Target` and `Content-Type` through `$context.requestOverride.header` explicitly. Whether EventBridge works without them, and the exact request API Gateway builds for each protocol, is UNVERIFIED (APIGW1-20).
- The backend's response becomes the integration response: status code, headers and body. Query services answer JSON because API Gateway asks for it (section 1 finding 9); for SQS `SendMessage` the JSON has `SendMessageResponse.SendMessageResult.MessageId` and `SendMessageResponse.ResponseMetadata.RequestId` (test_apigateway_sqs.py:L180-L189).
- A backend 4xx with only a default integration response: UNVERIFIED (APIGW1-19). The docs say for Lambda custom integrations that unmapped errors "are returned as `200 OK` responses by default" ([D] handle-errors-in-lambda-integration); the same likely holds for AWS service errors (the default integration response matches anything no pattern claims).
- `credentials` is the role API Gateway assumes for the call (section 3.4); every recorded AWS integration passes one.

### 6.5 Lambda custom (non-proxy) integration

- Type `AWS` with the Lambda invocation URI and `integrationHttpMethod POST` ([R] test_apigateway_lambda.snapshot.json:L1356-L1400, 2023-05-31). The rendered template (or the passthrough body) is the invocation payload; the function's result is the integration response body.
- A function error is matched by `selectionPattern` against `errorMessage`: "API Gateway matches the Lambda error's `errorMessage` against the pattern of the regular expression on the `selectionPattern` property. If there is a match, API Gateway returns the Lambda error as an HTTP response of the corresponding HTTP status code. If there is no match, API Gateway returns the error as a default response or throws an invalid configuration exception if no default response is configured" ([D] handle-errors-in-lambda-integration).
- Recorded ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1475-L1496, 2025-05-05; setup at test_apigateway_lambda.py:L770-L893): integration responses `200` (pattern `""`), `405` (`.*four hundred.*`), `502` (`.+`), `504` (`200`). Success answered 200 with body `"Pass"`; an exception `Error: Raising four hundred from within the Lambda function` answered **405**, although `.+` matches it too; `...five hundred...` answered 502. The body was the Lambda error object `{"errorMessage", "errorType", "requestId", "stackTrace"}` passed through. The `200` pattern on 504 did not capture the successful call, so successes are not matched against status codes for Lambda.

### 6.6 HTTP and HTTP_PROXY

`HTTP_PROXY` "simply passes the entire request and response between the frontend and the backend, subject to certain restrictions" ([D] [api-gateway-set-up-simple-proxy](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-set-up-simple-proxy.html)); `HTTP` goes through templates and integration responses like `AWS`. For HTTP and AWS backends "the HTTP status code is matched" by `selectionPattern` ([D] API_IntegrationResponse). Header handling follows the known-issues table (section 4.8). The recorded status passthrough tests exist (test_apigateway_http.validation.json); they need a reachable HTTP backend, which understudy supplies only for loopback or user-run services.

### 6.7 Integration response selection and mapping

- "API Gateway uses Java pattern-style regexes for response mapping" ([D] handle-errors-in-lambda-integration). "Setting the `selectionPattern` value to `.*` ... amounts to resetting this response as the default response ... such a selection pattern will match all error messages, including null"; `.+` "might not match a response containing a newline" (same page). An integration response with no `selectionPattern` (or `""`, recorded as the default in every test) is the default.
- Match target: `errorMessage` for Lambda errors; the status code for HTTP and AWS ([D] API_IntegrationResponse); the template's `statusCode` for MOCK (section 6.3).
- Anchoring (full match like Java `String.matches`, or find) and the order in which several matching patterns are tried are UNVERIFIED (APIGW1-12). The recorded 405-over-502 outcome (section 6.5) fits "ascending status code", "creation order" and "most specific" alike.
- After selection, `responseParameters` set method response headers and the response template is chosen. Which template is chosen when several exist (by the request `Accept`, by the integration response `Content-Type`, or the first) is UNVERIFIED (APIGW1-11). With no template the body passes through ([D] [models-mappings](https://docs.aws.amazon.com/apigateway/latest/developerguide/models-mappings.html): "If you don't define a mapping template for the Content-Type, by default, API Gateway passes the body through the integration response to the method response"). Response template that does not match and no default: `API_CONFIGURATION_ERROR` covers "integration response mapping can't match any template and no default template is configured" ([D] supported-gateway-response-types).

### 6.8 Request and response overrides

- `$context.requestOverride.header|path|querystring.{name}` in a request template replaces integration parameters; `$context.responseOverride.status` and `.header.{name}` in a template replace the method response status and headers ([D] api-gateway-mapping-template-reference; [apigateway-override-request-response-parameters](https://docs.aws.amazon.com/apigateway/latest/developerguide/apigateway-override-request-response-parameters.html)). "Overrides are final. An override may only be applied to each parameter once. If you try to override the same parameter multiple times, API Gateway returns a `5XX` response" (same page).
- Recorded: a MOCK request template that sets `$context.responseOverride.status = 444` and `.header.foo = "bar"` makes the response 444 with header `foo: bar`, and the response template can read those values back, along with an arbitrary key `$context.responseOverride.custom` ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_integrations.snapshot.json:L1084-L1105, 2025-05-16). A request template can stash data in `$context.requestOverride.path.body` that the MOCK response template then reads (L1048-L1061, 2024-11-06). Map assignment forms `#set($context.requestOverride.querystring[$paramName] = "bar")`, `.put($k, $v)` and `#set($context["requestOverride"].querystring["nestedfoo"] = ...)` all work (L1106-L1118, 2025-05-29; template at test_apigateway_integrations.py:L814-L860).

## 7. Mapping templates (VTL)

### 7.1 Dialect

- Templates are "a script expressed in Velocity Template Language (VTL)" ([D] models-mappings), linking the Velocity devel reference. The directives seen in the AWS examples, recordings and patterns are `#set`, `#if`/`#elseif`/`#else`/`#end`, `#foreach` with `$foreach.hasNext` ([D] [api-gateway-mapping-variable-examples](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-mapping-variable-examples.html) example 2; [R] section 6.8), and comments are not seen.
- Identifiers may contain hyphens: `$context.requestOverride.header.X-Amz-Target` works on AWS ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_eventbridge.snapshot.json:L2-L14, template at test_apigateway_eventbridge.py:L58-L72). Velocity 1.7 allows `-` after the first letter (`[a..z, A..Z] [a..z, A..Z, 0..9, -, _]`, [Velocity 1.7 VTL reference](https://velocity.apache.org/engine/1.7/vtl-reference-guide.html)); Velocity 2.x does not by default. So the AWS engine behaves like 1.7 here (inferred).
- Method calls on values are Java methods: `.size()` on a map from `$input.path` ([D] example 5), `.toString()`, `.contains("error")` ([D] override example), `.replaceAll("\\'","'")` ([D] mapping template reference), `.keySet()`, `.get($k)`, `.put($k, $v)` ([R] section 6.8; test_apigateway_sqs.py:L424-L428). String concatenation with `+` inside `#set` is used by a pattern (`#set($inputRoot='{"data" :'+$body+',"httpMethod" :"'+$context.httpMethod+'"}')`, [P] apigw-sfn-crud-terraform/main.tf:L121-L124).
- In Velocity 1.7, "If the RHS evaluates to null, it is not assigned to the LHS" ([Velocity 1.7 VTL reference](https://velocity.apache.org/engine/1.7/vtl-reference-guide.html), `#set`); that is consistent with the recordings in 7.2 but not separately recorded.

### 7.2 Rendering of values (recorded)

From an HTTP integration whose response template renders `$input.path(...)` results ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_common.snapshot.json:L1380-L1397, 2025-06-18; templates and inputs at test_apigateway_common.py:L796-L901). Against AWS the integration URI is a public httpbin-style echo whose response carries the parsed request body under `json` (localstack@8b9a79f localstack-core/localstack/testing/pytest/fixtures.py:L2319-L2352), so `$.json` below is the request body.

| Template (response) | Request body (echoed as `json`) | Rendered |
|---|---|---|
| `#set($result = $input.path("$.json"))$result` | `{"foo":"bar"}` | `{foo=bar}` |
| same | `[{"foo":"bar"}]` | `[{"foo":"bar"}]` |
| `...$result.nested` | `{"nested": {"foo": "bar"}}` | `{foo=bar}` |
| `...$result.nested` | `{"nested": [{"foo": "bar"}]}` | `[{"foo":"bar"}]` |
| `...$result[0]` | `[{"foo":"bar"}]` | `{foo=bar}` |
| `...$result[0]` | `[[{"foo":"bar"}]]` | `[{"foo":"bar"}]` |
| `$result` | `{"foo": [{"nested": "bar"}]}` | `{foo=[{"nested":"bar"}]}` |
| `$result` | `{"bigger": "dict", "to": "test", "with": "separators"}` | `{bigger=dict, to=test, with=separators}` |
| `$result.toString()` | `{"list": [{"foo": "bar"}]}` | `{list=[{"foo":"bar"}]}` |
| `$input.path("$.nonExisting")` used as `{"body": $result, "nested": $result.nested, "isNull": #if( $result == $null )"true"#else"false"#end, "isEmptyString": #if( $result == "" )"true"#else"false"#end}` | empty | `{"body": , "nested": , "isNull": "true", "isEmptyString": "true"}` |
| same on `$.json.listValue` | `{"listValue": []}` | `{"body": [], "nested": , "isNull": "false", "isEmptyString": "false"}` |
| same | `{"listValue": null}` | `{"body": , "nested": , "isNull": "true", "isEmptyString": "true"}` |

Rules this fixes: an object renders as Java `Map.toString()` (`{k=v, k2=v2}`, insertion order, `, ` separator) and a list as compact JSON in which nested objects are JSON too; a missing or null value renders as nothing; `null == ""` is true; an empty list renders `[]` and is neither null nor `""`.

### 7.3 $input

| Function | Behavior and source |
|---|---|
| `$input.body` | "Returns the raw request payload as a string. You can use `$input.body` to preserve entire floating point numbers, such as `10.00`" ([D] mapping template reference). An empty payload renders `{}` (section 1 finding 8); the raw bytes are kept exactly (`{"foo": "bar"}` with its spaces reached SQS, MD5 `94232c5b8fc9272f6f73a1e36eb68fcf`, [R] test_apigateway_sqs.snapshot.json:L52). A request template `#set($result = $input.body.testAccess)$result` (property access on the string) made the echo return `{}` for every input, which fits the request template rendering empty and the response template's `$input.body` of an empty integration body rendering `{}` again (inferred from [R] test_apigateway_common.snapshot.json:L1421-L1425 and the echo at test_apigateway_common.py:L908-L915). |
| `$input.json(x)` | "evaluates a JSONPath expression and returns the results as a JSON string" ([D]). It re-serializes compactly: input `{ "Price" : "249.99", "Age": "6" }` gives `{"Price":"249.99","Age":"6"}` for `$`, and `"6"` (quoted) for `$.Age` ([D] mapping variable examples 3 and 4). A missing path and number formatting are UNVERIFIED (APIGW1-18). |
| `$input.path(x)` | "returns a JSON object representation of the result" for use in VTL; `$input.path('$.pets').size()` returns `3` for a three-element list ([D]); rendering per section 7.2. |
| `$input.params()` | a map with keys `path`, `querystring`, `header`, each a map of names to values ([D] example 2). |
| `$input.params(x)` | path, then query, then header (section 4.5). |

### 7.4 $context

The documented variables are listed in [D] api-gateway-mapping-template-reference (context-variable-reference): `accountId`, `apiId`, `authorizer.*`, `awsEndpointRequestId`, `deploymentId`, `domainName`, `domainPrefix`, `error.message`, `error.messageString`, `error.responseType`, `error.validationErrorString`, `extendedRequestId`, `httpMethod`, `identity.*` (`accountId`, `apiKey`, `apiKeyId`, `caller`, `cognito*`, `principalOrgId`, `sourceIp`, `clientCert.*`, `vpcId`, `vpceId`, `user`, `userAgent`, `userArn`), `isCanaryRequest`, `path` (`/{stage}/root/child`), `protocol`, `requestId`, `requestOverride.*`, `responseOverride.*`, `requestTime`, `requestTimeEpoch`, `resourceId`, `resourcePath` (`/root/child`), `stage`, `wafResponseCode`, `webaclArn`.

Recorded `$context.keySet()` for a POST with authorization `NONE` on an AWS integration: `resourceId, resourcePath, httpMethod, extendedRequestId, requestTime, path, accountId, protocol, requestOverride, stage, domainPrefix, requestTimeEpoch, requestId, identity, domainName, deploymentId, responseOverride, apiId` (that order, a hash order); `$context.identity.keySet()`: `cognitoIdentityPoolId, accountId, cognitoIdentityId, caller, sourceIp, principalOrgId, accessKey, cognitoAuthenticationType, cognitoAuthenticationProvider, userArn, userAgent, user`, unset ones rendering empty; `requestOverride` has `path`, `header`, `querystring`; `responseOverride` has `header`; `$context.authorizer.keySet()` is empty ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_sqs.snapshot.json:L66-L121, 2025-05-20; template at test_apigateway_sqs.py:L424-L428). `$context.get("identity")` inside a string rendered as an empty string in that recording, not as a map.

### 7.5 $stageVariables

`$stageVariables.<name>`, `$stageVariables['<name>']` or `${stageVariables['<name>']}` ([D] mapping template reference); in URIs `${stageVariables.<name>}` (section 3.4). Recorded with a Lambda URI and a template `{ "version": "$stageVariables.version" }` (test_apigateway_basic.py:L1354-L1430, validated 2024-07-12).

### 7.6 $util

| Function | Behavior and source |
|---|---|
| `$util.escapeJavaScript(s)` | "Escapes the characters in a string using JavaScript string rules. This function will turn any regular single quotes (`'`) into escaped ones (`\'`). However, the escaped single quotes are not valid in JSON", hence the idiom `.replaceAll("\\'","'")` ([D] mapping template reference). Double quotes become `\"` ([D] example 5 output `"{\"1\":{},\"2\":{},\"3\":{}}"`). The handling of `/`, control characters and non-ASCII is UNVERIFIED (APIGW1-17). |
| `$util.parseJson(s)` | "Takes 'stringified' JSON and returns an object representation of the result" ([D]). |
| `$util.urlEncode(s)` / `urlDecode(s)` | "Converts a string into application/x-www-form-urlencoded format" ([D]). Recorded: `{"some": "value"}` encodes to `%7B%22some%22%3A+%22value%22%7D` and `some raw data` to `some+raw+data` (space as `+`, Java `URLEncoder` style); `urlEncode` of an empty reference is empty ([R] test_apigateway_common.snapshot.json:L1426-L1430). |
| `$util.base64Encode(s)` / `base64Decode(s)` | standard base64 ([D]); `base64Decode` of a path parameter is used in a recorded template (test_apigateway_integrations.py:L657-L661). |

"Unless otherwise specified, the default character set is UTF-8" ([D]).

### 7.7 JSONPath subset

The docs point at Goessner's JSONPath and Jayway's Java implementation ([D] mapping template reference). Expressions seen in docs, recordings and patterns: `$`, `$.a`, `$.a.b.c`, `$.a[0]`, and `$input.path('$')` followed by VTL navigation. Wildcards, recursive descent, filters, functions and bracket-quoted names are UNVERIFIED (APIGW1-21).

### 7.8 Limits

Template size 300 KB and 1000 `#foreach` iterations (section 2.6). What happens at 1001 iterations is not documented.

## 8. Canonical templates and expected outputs

### 8.1 SQS SendMessage (path URI, form body)

```
type = AWS, integrationHttpMethod = POST
uri = arn:aws:apigateway:<region>:sqs:path/<account>/<queue>
credentials = <role allowed sqs:SendMessage>
requestParameters = {"integration.request.header.Content-Type": "'application/x-www-form-urlencoded'"}
requestTemplates = {"application/json": "Action=SendMessage&MessageBody=$input.body"}
passthroughBehavior = NEVER
```

Sources: [R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_sqs.py:L56-L69 (snapshot test_apigateway_sqs.snapshot.json:L39-L64, 2025-03-19); [P] apigw-sqs-terraform/main.tf:L98-L115 (OpenAPI form, `passthroughBehavior: "never"`). Expected: a POST with body `{"foo": "bar"}` delivers a message whose body is exactly `{"foo": "bar"}` (MD5 `94232c5b8fc9272f6f73a1e36eb68fcf`); a POST with no body delivers `{}` (MD5 `99914b932bd37a50b983c5e7c90ae93b`); the client receives the integration response template output, `{"message": "great success!"}` in the recording. The body is not URL-encoded by the template, so a `+` or `&` in the payload reaches SQS form-decoded (inferred from form semantics).

### 8.2 SNS Publish (path URI, form body)

```
type = aws, httpMethod = POST, uri = arn:aws:apigateway:${AWS::Region}:sns:path//
requestParameters: integration.request.header.Content-Type: "'application/x-www-form-urlencoded'"
requestTemplates:
  application/json: "#set($inputRoot = $input.path('$'))\nAction=Publish&PhoneNumber=$util.urlEncode($util.parseJson($input.json('$.PhoneNumber')))&Message=$util.parseJson($input.json('$.Message'))&AWS.SNS.SMS.SMSType={'DataType:'String','StringValue': 'Transactional'}"
passthroughBehavior: when_no_templates
```

Source: [P] apigw-sns-sms/api.yaml:L20-L32 (SAM, OpenAPI 3). Expected (inferred): `$util.parseJson($input.json('$.PhoneNumber'))` turns the JSON string `"+15555550100"` back into `+15555550100`, `urlEncode` makes it `%2B15555550100`, SNS answers a JSON `PublishResponse` because API Gateway asks for JSON (section 6.4). For a topic, the same shape with `TopicArn=$util.urlEncode(...)` instead of `PhoneNumber`. No AWS recording of SNS integrations exists in the LocalStack suite read here.

### 8.3 EventBridge PutEvents (action URI, JSON body with overridden headers)

```
type = AWS, integration_http_method = POST, uri = arn:aws:apigateway:<region>:events:action/PutEvents
passthrough_behavior = WHEN_NO_TEMPLATES
request_templates = {"application/json" = <<EOT
  #set($context.requestOverride.header.X-Amz-Target = "AWSEvents.PutEvents")
  #set($context.requestOverride.header.Content-Type = "application/x-amz-json-1.1")
  #set($inputRoot = $input.path('$'))
  { "Entries": [
    #foreach($elem in $inputRoot.items)
    { "Detail": "$util.escapeJavaScript($elem.Detail).replaceAll("\\'","'")",
      "DetailType": "$elem.DetailType", "EventBusName": "<bus>", "Source":"$elem.Source" }#if($foreach.hasNext),#end
    #end ] }
EOT}
```

Sources: [P] apigw-rest-api-eventbridge-terraform/main.tf:L71-L117; [R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_eventbridge.py:L46-L95 (same template without `EventBusName`), snapshot test_apigateway_eventbridge.snapshot.json:L2-L14 (recorded 2024-07-12). Expected: input `{"items":[{"Detail":"{\"data\":\"Order is created\"}","DetailType":"Test","Source":"order"}]}` produces one event whose `Detail` is the object `{"data":"Order is created"}`, and the response template `#set($inputRoot = $input.json('$'))\n$inputRoot` returns `{"Entries":[{"EventId":"<uuid>"}],"FailedEntryCount":0}`.

### 8.4 DynamoDB PutItem and Query (action URI, JSON body)

```
uri = arn:aws:apigateway:<region>:dynamodb:action/PutItem
requestTemplates = {"application/json": "{\"TableName\": \"<table>\", \"Item\": {\"id\": {\"S\": \"$input.params('id')\"}}}"}
uri = arn:aws:apigateway:<region>:dynamodb:action/Query
requestTemplates = {"application/json": "{\"TableName\": \"<table>\", \"KeyConditionExpression\": \"id = :id\", \"ExpressionAttributeValues\": {\":id\": {\"S\": \"$input.params('id')\"}}}"}
responseTemplates = {"application/json": "$input.json('$')"}
```

Sources: [R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_dynamodb.py:L54-L80 (helper conftest.py:L80-L165), snapshot test_apigateway_dynamodb.snapshot.json:L18-L195 (2024-07-19); [P] apigw-dynamodb-terraform/main.tf:L121-L178 (a `#foreach` response template over `$inputRoot.Items`). Expected: `POST /test?id=test-new` stores `{"id":{"S":"test-new"}}` and answers 200 `{}` with `content-type: application/json`; `POST /test?id=test%203` (space) queries `test 3` and answers `{"Count":1,"Items":[{"id":{"S":"test 3"}}],"ScannedCount":1}`.

### 8.5 Step Functions StartExecution (action URI, escaped input)

```
uri = arn:aws:apigateway:<region>:states:action/StartExecution
requestTemplates = {"application/json": "{\"input\": \"$util.escapeJavaScript($input.json('$'))\", \"stateMachineArn\": \"<arn>\"}"}
```

Sources: [R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_stepfunctions.py:L101-L157, snapshot test_apigateway_stepfunctions.snapshot.json:L2-L16 (2024-07-12); [P] apigw-sfn-crud-terraform/main.tf:L112-L140 (adds `$context.httpMethod` and a `replaceAll("\\'","'")`, and a response template `#set($inputRoot = $input.path('$')) $inputRoot.output` for synchronous express executions). Expected: body `{"test": "test-value"}` (sent with no `Content-Type`) starts an execution with input `{"test":"test-value"}` and the passthrough response is the `StartExecution` JSON `{"executionArn": "...", "startDate": <epoch>}`; `DeleteStateMachine` with an empty template map passes the request body through and answers `{}`.

## 9. Terraform AWS provider v6.68.0

### 9.1 Calls per resource

All paths are hashicorp/terraform-provider-aws v6.68.0 internal/service/apigateway/.

| Resource | Create | Read (refresh and plan) | Update | Delete |
|---|---|---|---|---|
| `aws_api_gateway_rest_api` | `CreateRestApi`, wait `GetRestApi` until `AVAILABLE`; with `body`: `PutRestApi` (mode `overwrite`), wait, `UpdateRestApi` to re-apply configured attributes, wait (rest_api.go:L208-L327) | `GetRestApi`, then `GetResources` paged until the item with path `/` (rest_api.go:L330-L413) | one `UpdateRestApi` with all changed paths, wait; `PutRestApi` when `body` changes (rest_api.go:L415-L659) | `DeleteRestApi` (NotFound ignored), poll `GetRestApi` until NotFound (rest_api.go:L661-L685) |
| `aws_api_gateway_rest_api_policy` | `UpdateRestApi replace /policy` (normalized JSON) | `GetRestApi` | same as create | `UpdateRestApi replace /policy ""` (rest_api_policy.go:L62-L151) |
| `aws_api_gateway_resource` | `CreateResource`, `GetResource` | `GetResource` | `UpdateResource replace /pathPart`, `/parentId` | `DeleteResource` (resource.go:L74-L181) |
| `aws_api_gateway_method` | `PutMethod` (no read in create) | `GetMethod` | `UpdateMethod`, then `GetIntegration` (and rarely `UpdateIntegration`) (method.go:L175-L375) | `DeleteMethod` |
| `aws_api_gateway_integration` | `PutIntegration`, `GetIntegration` | `GetIntegration` | `UpdateIntegration`, sleep 3 s, `GetIntegration`, maybe a second `UpdateIntegration` for cache keys (integration.go:L330-L659) | `DeleteIntegration` |
| `aws_api_gateway_method_response` | `PutMethodResponse` under a provider-wide mutex, retried on `ConflictException` for 2 min (method_response.go:L85-L122) | `GetMethodResponse` | `UpdateMethodResponse` | `DeleteMethodResponse` |
| `aws_api_gateway_integration_response` | `PutIntegrationResponse`, `GetIntegrationResponse` (update re-puts) (integration_response.go:L94-L132) | `GetIntegrationResponse` | `PutIntegrationResponse` | `DeleteIntegrationResponse` |
| `aws_api_gateway_deployment` | `CreateDeployment` with `description` always sent (as `""` when unset) and `variables`, `GetDeployment` (deployment.go:L76-L95) | `GetDeployment` | `UpdateDeployment replace /description` | `DeleteDeployment`, retried once on the canary text (deployment.go:L154-L184) |
| `aws_api_gateway_stage` | `CreateStage` (tags in the request), cache wait only when a cache was asked for and the status is not `NOT_AVAILABLE`, then `UpdateStage` if `client_certificate_id` or `access_log_settings` is set, else `GetStage` (stage.go:L174-L241) | `GetStage` | `UpdateStage` (stage.go:L298-L413) | `DeleteStage` |
| `aws_api_gateway_account` | `UpdateAccount replace /cloudwatchRoleArn`, retried for IAM propagation (account.go:L73-L125) | `GetAccount` | same | `UpdateAccount` with a null value (account.go:L218-L253) |

Tags on `aws_api_gateway_rest_api` and `aws_api_gateway_stage` go through `TagResource`/`UntagResource` keyed by the ARN (section 3.9).

### 9.2 Computed attributes

- `root_resource_id`: the id of the `GetResources` item whose `path` is `/` (rest_api.go:L395-L413); `RestApi.rootResourceId` is not used.
- `arn`: `arn:aws:apigateway:<region>::/restapis/<id>`; `execution_arn`: `arn:aws:execute-api:<region>:<account>:<id>` (rest_api.go:L1010-L1016).
- `created_date`: `createdDate` formatted RFC 3339 (rest_api.go:L362, deployment.go:L119-L122).
- `aws_api_gateway_resource.path`: `GetResource.path`, recomputed when `path_part` changes (resource.go:L67-L69).
- `aws_api_gateway_stage.invoke_url`: `https://<id>.execute-api.<region>.<dns-suffix>/<stage>`, client-side (section 1 finding 11); `execution_arn`: `.../<id>/<stage>`; `arn`: `arn:aws:apigateway:<region>::/restapis/<id>/stages/<stage>`; id `ags-<api-id>-<stage>` (stage.go:L225, L284-L286, L665-L671).

### 9.3 What must round-trip for an empty second plan

1. `GetRestApi`: `apiStatus: "AVAILABLE"`; `endpointConfiguration.types` and `ipAddressType` (Optional+Computed; an absent `ipAddressType` after the user configured none is fine, a changed one is not); `apiKeySource` (Computed, so `HEADER` must come back every time); `disableExecuteApiEndpoint`; `binaryMediaTypes` exactly as sent, order kept (a list, rest_api.go:L84-L89); `description` absent or equal; `minimumCompressionSize` absent when unset (`""` vs `-1` suppression, rest_api.go:L157-L166); `policy` escaped (section 2.3) and semantically equal; `tags`.
2. `GetResources` contains `/`.
3. `GetMethod`: `apiKeyRequired` (default false), `authorizationType`, `authorizerId`, `authorizationScopes`, `operationName`, `requestModels`, `requestParameters` (booleans), `requestValidatorId` exactly as sent (method.go:L377-L386).
4. `GetIntegration`: `httpMethod` equal to `integration_http_method` (ForceNew: a missing or changed value replaces the integration on every plan), except for `MOCK`, where AWS drops it (2.3), so a configuration that sets it on a MOCK integration replans on AWS too and understudy must drop it the same way; `credentials` (ForceNew) verbatim; `passthroughBehavior` (Computed, `WHEN_NO_MATCH` default, ForceNew); `timeoutInMillis` 29000 by default (Terraform's default, integration.go:L137-L141); `cacheNamespace` (Computed) stable; `cacheKeyParameters` as a set; `requestParameters` and `requestTemplates` byte-for-byte (heredoc templates keep their leading spaces and final newline); `connectionType` absent or `INTERNET` (Terraform maps absent to `INTERNET`, integration.go:L308-L311); `responseTransferMode` absent or `BUFFERED` (L319-L323); `uri` verbatim; `type`.
5. `GetMethodResponse`: `responseModels`, `responseParameters` as sent.
6. `GetIntegrationResponse`: `selectionPattern` as sent (absent when unset), `responseTemplates` with `null` for empty strings, `responseParameters`, `contentHandling`.
7. `GetDeployment`: `createdDate`, `description` (`""` or absent when Terraform sent `""`).
8. `GetStage`: `deploymentId`, `description`, `documentationVersion`, `variables`, `tracingEnabled`, `cacheClusterEnabled` false and `cacheClusterStatus` `NOT_AVAILABLE`, `clientCertificateId`, `accessLogSettings`, `canarySettings` absent, `webAclArn` absent, `tags`.
9. `GetRestApi` after `DeleteRestApi` answers `NotFoundException` (Terraform's delete wait).

### 9.4 Redeployment patterns

- Since 6.0.0 the deployment resource has no `stage_name`, `stage_description`, `canary_settings`, `invoke_url` or `execution_arn` ("Instead, use the `aws_api_gateway_stage` resource to manage stages", [hashicorp/terraform-provider-aws CHANGELOG 6.0.0](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/CHANGELOG.md)). The schema confirms it (deployment.go:L37-L74).
- `triggers` and `variables` are ForceNew (deployment.go:L59-L71); the documented pattern hashes the API's resources into `triggers.redeployment` and sets `create_before_destroy` ([P] apigw-rest-api-eventbridge-terraform/main.tf:L124-L145). On a change Terraform creates the new deployment, updates the stage with `replace /deploymentId`, then deletes the old deployment, which succeeds only because no stage points at it any more (section 3.6).
- A deployment created in the same apply as the methods it should contain has to be ordered after them with `depends_on` ([P] apigw-sfn-crud-terraform/main.tf:L259-L262); understudy snapshots at `CreateDeployment` time, like AWS, so a deployment created too early serves the API as it was.

### 9.5 Wall-clock costs

`aws_api_gateway_integration` updates sleep 3 s of wall time between their two stages (integration.go:L578, L689). The `apiStatus` and delete waits poll; with `AVAILABLE` and NotFound answered at once they cost one round trip each.

### 9.6 `body` (OpenAPI) and SAM

`PutRestApi` with mode `overwrite` "will delete existing literal properties if they are not explicitly set in the OpenAPI definition" (rest_api.go:L273-L286); Terraform then re-applies configured attributes with `UpdateRestApi`. The OpenAPI vendor extension `x-amazon-apigateway-integration` carries the same fields as `PutIntegration` (`type`, `httpMethod`, `uri`, `credentials`, `requestParameters`, `requestTemplates`, `passthroughBehavior` in lower case, `responses` keyed by a selection pattern or `default`, `timeoutInMillis`, ...) ([D] [api-gateway-swagger-extensions-integration](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-swagger-extensions-integration.html), [api-gateway-swagger-extensions-integration-responses](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-swagger-extensions-integration-responses.html)). SAM's `AWS::Serverless::Api` with `DefinitionBody` is deployed through CloudFormation ([P] apigw-sns-sms/template.yaml:L5-L17). LocalStack's import suite (20 validated cases, test_apigateway_import.validation.json) is the evidence base when this slice is built; it is not mapped further here.

## 10. LocalStack's AWS-validated snapshots: what they record

All under localstack@8b9a79f tests/aws/services/apigateway/. Counts are test ids with a `last_validated_date`.

| File | Validated | Behaviors recorded that this note uses |
|---|---|---|
| test_apigateway_api | 82 | control-plane defaults and every error text in section 2.4; patch semantics (3.8); null templates (3.5); gateway response defaults (3.9) |
| test_apigateway_common | 18 | 403 Missing Authentication Token (1.3); routing backtracking, asserts only (4.3); request validation messages (4.6); stage and deployment shapes and deletion rule (3.6, 3.7); VTL rendering and `$input.body` (7.2, 7.3) |
| test_apigateway_lambda | 12 | proxy event 1.0 (5.1), path and query decoding (4.2, 4.5), proxy response mapping (5.2), selection patterns (6.5), AWS_PROXY URI validation (3.4), non-POST failure (5.3) |
| test_apigateway_integrations | 13 | integration defaults and validation (2.3, 2.4), MOCK lenient parse and overrides (6.3, 6.8), header remapping and 500 bodies (4.7, 4.8), policy rewrite (2.3) |
| test_apigateway_basic | 23 | MOCK under each passthrough behavior (6.2), stage variables in URIs (7.5) |
| test_apigateway_sqs | 7 | form and JSON-protocol SQS integrations, raw body MD5s, JSON rendering of Query responses, `$context` key sets (8.1, 6.4, 7.4) |
| test_apigateway_eventbridge, _dynamodb, _stepfunctions, _kinesis, _ssm | 1, 4, 2, 2, 2 | action URIs end to end (8.3 to 8.5) |
| test_apigateway_http, _s3 | 7, 10 | HTTP status passthrough, S3 path integrations and binary handling (not mapped in detail) |
| test_apigateway_import, _extended, _canary, _lambda_cfn | 20, 13, 8, 1 | OpenAPI import, API keys and usage plans, export, canaries: outside the first slice |

## 11. Conflicts between sources

| Topic | Source A | Source B | Resolution |
|---|---|---|---|
| Duplicate keys in proxy `headers` and `multiValueHeaders` | Docs: "If the same key-value pair is specified in both, only the values from `multiValueHeaders` will appear in the merged list" ([D] set-up-lambda-proxy-integrations) | Recorded: same key, different values, `value-multi, value-solo`, both kept (5.2) | Not a conflict: the docs cover an identical pair, the recording different values. Keep both values, `multiValueHeaders` first, and drop a value already present |
| `INTEGRATION_FAILURE` status | `GetGatewayResponses` default 504 ([R] api.snapshot L2378-L2385) | AWS_PROXY failure answered 500 (5.3) and a function error 502 (5.2) | Follow the recordings per failure kind; keep 504 in the gateway response listing |
| Integration timeout ceiling | Docs: 50 to 29000 ms default, raisable for Regional ([D] API_PutIntegration) | Terraform validates up to 300000 (BUFFERED) and 900000 (STREAM) (integration.go:L171-L198) | Accept up to 300000/900000 on the control plane; time out on the configured value |
| URL decoding | Docs: "REST APIs decode URL-encoded request parameters" ([D] api-gateway-known-issues) | Recorded: the path and `pathParameters` stay encoded (4.2) | Decode query values, not the path |
| `escapeJavaScript` example | Docs example 3 shows `"body": {"Price":"249.99","Age":"6"}` unescaped after `escapeJavaScript` ([D] mapping variable examples) | Docs example 5 shows escaped quotes | Example 3 is a documentation error; escape quotes |
| Example 2 output | Docs show `{"params":{"path":{"example2":"myparamm"},...` for a parameter `myparam` | | Documentation typo; not a behavior |
| Proxy event example | The documented event has `stage: "$default"` and `resource: "/my/path"`, an HTTP API shape ([D] set-up-lambda-proxy-integrations) | Recorded REST events have the real stage and the resource template (5.1) | Follow the recording |

## 12. Implications for understudy

### 12.1 Edge and codec

1. Generate `internal/api/apigatewayapi` from the pinned v1 model like the other restJson1 services and register it as the service `apigateway` under signing name `apigateway`, beside `apigatewayv2` (roadmap item 6 does v2 first; [apigateway-http.md](apigateway-http.md) covers it). ADR 0019 (accepted 2026-10-26) makes them two registry services on one signing name: the registry accepts the pair because the `/v2/` prefix keeps their route tables disjoint (2.1), refuses an overlap at boot with `OverlappingRouteTables`, and tries the REST services of the name in registration order, so a request neither table matches is the first service's unknown operation. A single merged service was rejected there because the models share operation names (`CreateDeployment`, `TagResource`).
2. Wire names from the model: `item` for list members, `httpMethod` for `integrationHttpMethod` (2.3); `createdDate` and `lastUpdatedDate` as epoch seconds; `PutIntegrationResponse`/`GetIntegrationResponse` and `GetIntegration` serialize empty-string template values as JSON `null` (coverage-note item `templates.empty-value`, 12.3); the policy string escaped (2.3).
3. Errors: `X-Amzn-Errortype: <ShapeName>` plus `{"message": "..."}` with the modeled status (2.4). Emit `ValidationException` (400) for enum and pattern failures with the Coral message format and 1-based member indexes (`patchOperations.1.member.op`, `putIntegrationInput.type`) and `SerializationException` (400) for type mismatches, both unmodeled codes registered as protocol-level errors. Accept `Accept: application/json` and do not require it (2.1).

### 12.2 Operation coverage

The marks and notes after the first paragraph below are the target. The first slice (roadmap item 6, REST APIs) binds the roadmap's minimum, and the operations it binds are at least Partial, each note starting with the base note `the X-Amzn-Errortype value and generated ids are inferred (UNVERIFIED, APIGW1-01, APIGW1-02)` (2.4, 3.10), so none is Full until those recordings land.

First slice. Settings that change what the data plane does get exactly one treatment each, never silent storage:

| Setting | First-slice treatment | Why |
|---|---|---|
| Integration `credentials` | stored and returned; an invocation that reaches the integration answers 501 naming `credentials`, journaled, before any function-policy check | AWS assumes the role instead of checking the function policy (3.4, 5.4); roles are not assumed yet |
| `${stageVariables.*}` in an integration `uri` | stored and returned; an invocation that reaches it answers 501 naming the stage-variable URI | the target is resolved per stage (3.4, localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_basic.py:L1380) |
| `responseTransferMode` `STREAM`, `contentHandling` on an integration or integration response | stored and returned; an invocation that reaches it (or selects the response) answers 501 naming the setting | streaming and binary conversion are not emulated (3.4, 3.5) |
| non-empty `binaryMediaTypes` in the stage's deployment snapshot | stored and returned; an invocation of the stage answers 501 naming `binaryMediaTypes` | they decide the event's `isBase64Encoded` and whether a response body is decoded (5.1, 5.2); the details are APIGW1-16 |
| `disableExecuteApiEndpoint` true on the live API | stored and returned; every invocation answers 501 naming it, from the call that sets it | no REST source records AWS's answer or when it takes effect, so understudy refuses rather than serve |
| `endpointConfiguration` type `PRIVATE` | stored and returned; every invocation answers 501 naming the PRIVATE endpoint | AWS serves private APIs only through VPC endpoints, never on a public host |
| `policy` (execute-api resource policy) | stored and returned escaped (2.3), not evaluated; under `iam.enforce=advise` each invocation of an API with a policy adds an Advisory, under `enforce` it answers 501 naming the resource policy | never served open while unevaluated |
| method `authorizationType` `AWS_IAM`; `CUSTOM` and `COGNITO_USER_POOLS` | `AWS_IAM` stored, an invocation answers 501; `CUSTOM` and `COGNITO_USER_POOLS` fail PutMethod with the recorded `Invalid authorizer ID specified...` text (2.4), since no authorizer can be created | authorizers unbound, SigV4 on execute-api not checked yet |
| method `apiKeyRequired` true | stored; an invocation answers 403, since no API key can be created | 4.7, 12.9 |
| integration types `HTTP`, `HTTP_PROXY`, `AWS` | stored and returned; an invocation that reaches them answers 501 naming the type | follow-ups: AWS integrations to understudy's own services, HTTP through `internal/egress` |
| request and response templates | a template containing none of `$`, `#` or `\` is literal text and renders unchanged; any other answers 501 naming the template, journaled | VTL deferred (12.7) |
| `vpcEndpointIds`; EDGE endpoint type | stored without effect; an EDGE API is served as a REGIONAL one, so the event has no `CloudFront-*`, `Via` or `X-Amz-Cf-Id` headers (5.1, inferred) | `vpcEndpointIds` matter only for PRIVATE |

First-slice coverage notes, after the base note. Base note only: GetRestApi, GetRestApis, DeleteRestApi, CreateResource, GetResource, UpdateResource, DeleteResource, GetMethod, DeleteMethod, PutMethodResponse, GetMethodResponse, UpdateMethodResponse, DeleteMethodResponse, DeleteIntegration, GetIntegrationResponse, DeleteIntegrationResponse, GetDeployment, GetDeployments, UpdateDeployment, DeleteDeployment, GetStages, TagResource, UntagResource, GetTags.

| Operation | First-slice note |
|---|---|
| CreateRestApi, UpdateRestApi | `cloneFrom answers NotImplemented; policy is stored and returned escaped, and under iam.enforce=enforce an invocation of an API with a policy answers 501; binaryMediaTypes, disableExecuteApiEndpoint and endpointConfiguration PRIVATE are stored and returned, and an invocation of an API that has them answers 501; vpcEndpointIds, securityPolicy, endpointAccessMode and minimumCompressionSize are stored without effect; an EDGE API is served as a REGIONAL one (no CloudFront headers in the event); a second API with the same name is allowed (inferred, APIGW1-14)` |
| PutMethod, UpdateMethod | `AWS_IAM, CUSTOM and COGNITO_USER_POOLS methods answer 501 at invoke; apiKeyRequired methods answer 403 because API keys cannot be created; requestValidatorId and requestModels name only the seeded Empty and Error models until validators and models are bound` |
| PutIntegration, UpdateIntegration, GetIntegration | `AWS_PROXY to Lambda and MOCK run; HTTP, HTTP_PROXY and AWS are stored and answer 501 at invoke; credentials, a stage variable in the uri, responseTransferMode STREAM and contentHandling are stored and returned, and an invocation that reaches them answers 501; connectionType VPC_LINK, tlsConfig and integrationTarget answer NotImplemented; an empty-string request template is returned as null (inferred, APIGW1-13)` (GetIntegration carries the last clause only) |
| PutIntegrationResponse, UpdateIntegrationResponse | `selectionPattern runs as Go RE2; a Java-only construct (lookaround, backreference, possessive quantifier) answers 501 at invoke naming the pattern; only static responseParameters and literal templates run, anything else answers 501; contentHandling is stored and an invocation that selects the response answers 501` |
| GetResources | `items in creation order, root first (inferred, APIGW1-04)` |
| CreateDeployment | `canarySettings answers NotImplemented; cacheClusterEnabled, cacheClusterSize and tracingEnabled are stored without effect; the texts for deploying an API with no methods or a method with no integration are inferred (APIGW1-05)` |
| CreateStage, UpdateStage, GetStage, DeleteStage | `cache settings, methodSettings, accessLogSettings, clientCertificateId, tracingEnabled and webAclArn are stored and echoed without effect; canarySettings answers NotImplemented; the duplicate-stage and missing-stage texts are inferred (APIGW1-06)` (GetStage and DeleteStage carry the last clause only) |
| `InvokeRestApi` (raw) | `AWS_PROXY (payload 1.0) and MOCK with literal templates; mapping templates with VTL, request validation, authorizers, API keys, caching and throttling are not applied (501 where AWS would act); integration credentials, stage-variable URIs, STREAM, contentHandling, binaryMediaTypes, a disabled default endpoint and PRIVATE APIs answer 501; inferred, UNVERIFIED: unknown stage, ANY against explicit methods, HEAD (APIGW1-07, -08), the 415 text (APIGW1-09), timeout (APIGW1-10), empty-body event (APIGW1-16)` |
| request validators, models, gateway responses, GetAccount, UpdateAccount, PutRestApi, ImportRestApi, and the unbound list at the end of this section | unbound (501) |

Target, beyond the first slice. Full (no note): GetRestApi, GetRestApis, DeleteRestApi, CreateResource, GetResource, GetResources, UpdateResource, DeleteResource, GetMethod, DeleteMethod, PutMethodResponse, GetMethodResponse, UpdateMethodResponse, DeleteMethodResponse, GetIntegration, DeleteIntegration, GetIntegrationResponse, DeleteIntegrationResponse, GetDeployment, GetDeployments, UpdateDeployment, DeleteDeployment, GetStage, GetStages, DeleteStage, CreateRequestValidator, GetRequestValidator, GetRequestValidators, UpdateRequestValidator, DeleteRequestValidator, GetModel, GetModels, UpdateModel, DeleteModel, PutGatewayResponse, GetGatewayResponse, GetGatewayResponses, UpdateGatewayResponse, DeleteGatewayResponse, TagResource, UntagResource, GetTags.

Partial, with these coverage notes (rows beyond the first slice say so). Two rules shape them. A valid member understudy does not implement answers NotImplemented (501) naming the member and saying nothing was changed, never `BadRequestException`, which would claim that AWS refuses it (the repository's convention, `internal/service/lambda/errors.go` `checkUnimplementedConfig`). An invocation that needs something understudy cannot do (an unsupported template, selection pattern or construct, Docker missing, an unemulated target) answers 501 in API Gateway's JSON message shape, `{"message":"understudy does not implement ...; nothing was changed. ..."}`, journaled, never an AWS-shaped 500, which would be indistinguishable from a real configuration error (CLAUDE.md invariant 3; `apigateway-http.md` 13.3 item 2 and 13.7).

| Operation | Coverage note |
|---|---|
| CreateRestApi | `cloneFrom` answers NotImplemented; policy, securityPolicy, endpointAccessMode and minimumCompressionSize are stored without effect |
| UpdateRestApi | the UpdateRestApi paths of the patch-operations reference; policy, securityPolicy, endpointAccessMode and minimumCompressionSize are stored without effect |
| PutMethod, UpdateMethod | first slice: AWS_IAM answers 501 at invoke (table above); target: AWS_IAM checks only that the invoke request carries a SigV4 Authorization header for execute-api; CUSTOM and COGNITO_USER_POOLS need an authorizer, which cannot be created; apiKeyRequired methods answer 403 because API keys cannot be created |
| PutIntegration, UpdateIntegration | first slice: HTTP, HTTP_PROXY and AWS stored, answering 501 at invoke (table above); target: HTTP and HTTP_PROXY reach the integration URI from the host and refuse AWS endpoints; connectionType VPC_LINK, tlsConfig and integrationTarget answer NotImplemented; AWS integrations reach understudy's own services only, and an integration naming a service understudy does not emulate answers 501 at invoke |
| PutIntegrationResponse, UpdateIntegrationResponse | selectionPattern is compiled as Go RE2: a Java-only construct (lookaround, backreference, possessive quantifier) makes the invocation answer 501 naming the pattern, journaled |
| CreateDeployment | canarySettings answers NotImplemented; cacheClusterEnabled, cacheClusterSize and tracingEnabled are stored without effect |
| CreateStage, UpdateStage | cache settings, methodSettings, accessLogSettings, clientCertificateId, tracingEnabled and webAclArn are stored and echoed without effect; canarySettings answers NotImplemented |
| CreateModel | first slice: unbound (501); target: schemas are JSON Schema draft 4 limited to type, properties, required, items, enum, minimum, maximum, minLength, maxLength, pattern and $ref to models of the same API; another keyword answers NotImplemented naming it |

Stored only: GetAccount, UpdateAccount (first slice: unbound (501); target: `cloudwatchRoleArn is stored and echoed; no execution or access logs are written`).

Raw operation (not in the model): `apigateway.InvokeRestApi` serving the execute-api data plane, journaled and faultable like `lambda.InvokeFunctionUrl` (ARCHITECTURE.md 14.7). It declares no static IAM action, as v2's `InvokeApi` (`apigateway-http.md` 8.1): AWS evaluates no IAM on a `NONE` method, and an `AWS_IAM` method (501 in the first slice) would be authorized as `execute-api:Invoke` on its execute ARN when the request arrives (4.1, 3.10).

Unbound (501) in the first slice: PutRestApi and ImportRestApi (the second slice, 9.6), authorizers, API keys, usage plans and keys, domain names, base path mappings, domain name access associations, VPC links, client certificates, documentation parts and versions, SDK and export, GetModelTemplate, TestInvokeMethod, TestInvokeAuthorizer, FlushStageCache, FlushStageAuthorizersCache, routing rules and portals.

### 12.3 Fidelity switches

Five switches, one per recording case or group of related cases, each struct-valued; every other unknown is a code comment plus a Partial coverage note naming its case (12.2). Each is a `fidelity.Switch` (`internal/fidelity/switch.go`), listed by the service's `Switches()`; `Ref` points at the first section of its grounds, `Record` at the cases. The last column names the switches an earlier draft of this section listed one per unknown (21 in all; the per-unit cap of five comes from the roadmap plan's "Process per unit", which settles the count conflict with this note).

| Name | Value | Label | Grounds | Record | Replaces |
|---|---|---|---|---|---|
| `apigateway.restapi.api-status` | `apiStatus` is `AVAILABLE` at once after CreateRestApi and UpdateRestApi | inferred | 3.1, finding 2 | APIGW1-03 | (unchanged) |
| `apigateway.invoke.routing` | an unknown stage answers `403` `{"message":"Forbidden"}` with `x-amzn-ErrorType: ForbiddenException`; an explicit method is served before `ANY` on the same resource; `HEAD` is served only by a `HEAD` or `ANY` method | inferred | 4.7, 4.4 | APIGW1-07, APIGW1-08 | `invoke.unknown-stage`, `invoke.method-precedence` |
| `apigateway.lambda.permission` | on under every `iam.enforce` mode: an AWS_PROXY invoke without `credentials` runs only when the function's resource policy allows `apigateway.amazonaws.com` with the source ARN `<api-id>/<stage>/<METHOD>/<resource path template without its leading slash>` after `arn:aws:execute-api:<region>:<account>:` (3.10; the documented example ends `/HTTP_METHOD/resource`, 5.4), with `ANY` as written for an `ANY` method and an empty last segment for the root resource (both inferred); otherwise it answers `500` `{"message": "Internal server error"}` and the function does not run | inferred | 5.4, 3.10 | APIGW1-15 | `lambda.permission-check` |
| `apigateway.mock.selection` | an unmatched request media type under `NEVER` or `WHEN_NO_TEMPLATES` answers `415` `{"message":"Unsupported Media Type"}`, media types matched by essence, case-insensitively; the response template is chosen by the first `Accept` media type, else `application/json`, else the only template; a `selectionPattern` must match the whole status string, candidates tried in ascending status-code order, the default response last | inferred (selection consistent with third-party-recorded 6.5) | 6.2, 6.7 | APIGW1-09, APIGW1-11, APIGW1-12 | `passthrough.unsupported-media-type`, `response-template.selection`, `selection-pattern.match` |
| `apigateway.proxy.unrecorded` | an integration timeout answers `504` `{"message": "Endpoint request timed out"}`; a request with no body (GET recorded, POST inferred) gives the event `body: null` and `isBase64Encoded: false` | inferred | 5.3, 5.1 | APIGW1-10, APIGW1-16 | `integration.timeout-response`, `proxy.empty-body` |

Coverage-note items, no switch (each a clause of the 12.2 note it names; tests assert them only by code, status, presence and uniqueness, never as a literal):

- `error.type-header` (the bare shape name in `X-Amzn-Errortype`; 2.4, APIGW1-01) and `ids.format` (API and root resource ids 10 lower-case alphanumerics, other ids 6; 3.10, APIGW1-02): the base note of every bound control-plane operation.
- `resources.list-order` (creation order, root first; 2.5, APIGW1-04): the GetResources note.
- `deployment.incomplete-api-errors` (`BadRequestException` `The REST API doesn't contain any methods` / `No integration defined for method`; 3.6, APIGW1-05): the CreateDeployment note.
- `stage.error-texts` (duplicate `ConflictException` `Stage already exists`, missing `NotFoundException` `Invalid stage identifier specified`; 3.7, APIGW1-06): the CreateStage, UpdateStage, GetStage and DeleteStage notes.
- `templates.empty-value` (`""` serialized as `null`; recorded for response templates, inferred for request templates; 3.5, APIGW1-13): the PutIntegration, UpdateIntegration and GetIntegration note.
- `restapi.duplicate-names` (allowed; 3.1, APIGW1-14): the CreateRestApi note.

Out of scope while the feature answers 501 (VTL deferred, 12.7; AWS integrations stored and answering 501 at invoke, 12.2): `vtl.escape-javascript` (7.6, APIGW1-17), `vtl.input-json` (7.3, APIGW1-18), `aws-integration.error-status` (6.4, APIGW1-19), `aws-integration.action-translation` (6.4, APIGW1-20) and `vtl.jsonpath` (7.7, APIGW1-21). Each becomes a switch, or a recorded value, with the follow-up that builds its feature.

`apigateway.lambda.permission` is on under every `iam.enforce` mode, through the same Lambda-invoke check the HTTP API front end and function URLs use (`apigateway-http.md` finding 9 and 13.3 item 1, ARCHITECTURE.md 14.7): the refusal is observable without any IAM enforcement, the docs make the policy statement the way API Gateway gets permission (5.4, "Instead of supplying an IAM role for `credentials`, you can use the add-permission command"), and invoking a function its policy does not allow would be a silent success.

### 12.4 Validation and limits

- Enforce, with the recorded texts of section 2.4: id existence per level, HTTP method enum, authorizer presence for CUSTOM and COGNITO, unique parameter names, sibling and greedy rules, cycles, root immutability, integration method presence, AWS_PROXY targets (Lambda and Firehose; Firehose itself answers 501 at invoke), URI shapes, status-code pattern, `minimumCompressionSize` 0..10485760, single endpoint type, PRIVATE with dualstack, empty description on CreateRestApi, model name and schema rules, validator name, deployment deletion rule.
- Enforce documented limits (2.6): resources per API 300, stages per API 10, stage variables 100 with key 64 and value 512 and the value pattern, stage name pattern and length 128, resource policy 8192, template 300 KB, `#foreach` 1000, payload 10 MB (413 `HTTP content length exceeded 10485760 bytes`), integration timeout 50 ms minimum. The texts for exceeding the count limits are not recorded; use `LimitExceededException` with a message naming the limit and record the gap in the coverage note.
- Patch: implement per-resource path tables (3.8), the list-as-object `binaryMediaTypes` semantics, the three recorded error shapes, ops applied in order, and refuse the whole request on the first invalid op without applying any (inferred: the API reference names no partial-success form).

### 12.5 State model

- One cell per (account, region) holds REST APIs; each API owns a resource tree (root created with the API), methods, integrations, method and integration responses, models (with `Empty` and `Error` seeded), validators, gateway response overrides, deployments and stages.
- A deployment is an immutable deep copy of the executable configuration (resources, methods, integrations, responses, models, validators, gateway responses, binary media types) taken at `CreateDeployment`; the data plane serves `stage -> deployment snapshot`, never live configuration (1.12). Snapshots and persistence carry deployments as values.
- `createdDate`, `lastUpdatedDate`, `requestTime` and `requestTimeEpoch` come from the emulator clock (CLAUDE.md invariant 5); integration timeouts are wall-clock deadlines like Lambda's function timeout (lambda-api.md 12).

### 12.6 Data plane and integrations

1. Claim the data plane by host before auth parsing, beside the function URL rule: `<api-id>.execute-api.<region>.<base>` (ARCHITECTURE.md 4.1 rule 2), plus a path fallback under the admin prefix, `/_understudy/execute-api/<api-id>/<stage>/...`, for clients that cannot set a host. Both are decisions; document the local `invoke_url` in `docs/guide/` because Terraform's points at amazonaws.com (9.2).
2. Route with the backtracking, method-aware matcher of 4.3 over the deployment snapshot; property-test it with rapid against a reference implementation that enumerates candidate resources in precedence order.
3. AWS_PROXY: build the 1.0 event exactly (5.1, 4.2, 4.5), invoke the function synchronously through the Lambda execution layer, map the result per the 5.2 table, answer 502 or 500 per 5.3. Reuse nothing from the function URL v2.0 builder beyond header handling; the formats differ (lambda-events.md 7).
4. AWS integrations: dispatch in-process to understudy's own service handlers, building the request from the URI (`path` or `action`), the mapped parameters and the rendered body, with `Accept: application/json` by default; record the `credentials` role as the calling principal in the journal and link the invocation and the service call causally. These are synchronous request and response calls, not `delivery.Target` outbox deliveries (ARCHITECTURE.md 16 lists API Gateway there, which fits only asynchronous fan-out). A URI naming a service understudy does not emulate answers 501 in API Gateway's JSON message shape naming the service, and journals why (12.2).
5. Prerequisite outside this package: the awsquery codecs must render the JSON form of a Query response when the request sends `Accept: application/json` (1.9; sqs.md 2.3 records it for `ListQueues`). A search of `internal/protocol` found no `Accept` handling in the Query codec today.
6. MOCK: status from the rendered template's `statusCode` with a lenient parse (6.3); no backend call.
7. HTTP and HTTP_PROXY: outbound HTTP from the host with the integration timeout, refusing AWS endpoint hosts (CLAUDE.md invariant 1); journal the call.
8. Gateway responses: render the API's (snapshot's) gateway response templates with simple substitution only (3.9); the 403, 415, 500, 502 and 504 bodies of 4.7.

### 12.7 VTL engine

**First slice: no VTL, literal templates only.** The roadmap plan makes the engine depend on a census of what users write. Census (2026-10-30): method, a read-only clone of aws-samples/serverless-patterns at `deb84afac5425dfb4cabbc3c33f7a11fa4a71555`, every `*.tf` file searched with `rg 'resource\s+"aws_api_gateway_'` (v1 only; v2 resources are `aws_apigatewayv2_*`), then each match read for `request_templates`, `response_templates`, integration `type` and `body`. 18 patterns have v1 resources, matching the state report's count ([P]):

| Pattern | Integrations | `request_templates` | `response_templates` | In the first slice? |
|---|---|---|---|---|
| apigw-bedrock-nova-canvas | AWS_PROXY | none | none | yes (calls Bedrock from the function) |
| apigw-data-validation-tf | AWS_PROXY | none | none | no: request validator and model (stretch) |
| apigw-dynamodb-terraform | from `body` | in the OpenAPI document | in the OpenAPI document | no: `body` (PutRestApi, 9.6) |
| apigw-lambda-bedrock-nova-terraform | AWS_PROXY | none | none | yes (calls Bedrock from the function) |
| apigw-lambda-durable-tenant-isolation-callback | AWS, AWS_PROXY | `$input.body` on the AWS integration (VTL) | `{"message": "Workflow started"}` (literal) | no: AWS integration |
| apigw-lambda-rekognition | AWS_PROXY | none | none | yes |
| apigw-lambda-s3-terraform-java | AWS_PROXY | none | none | yes (runtime `java25`) |
| apigw-lambda-snapstart-terraform | AWS_PROXY | none | none | yes (runtime `java25`, SnapStart) |
| apigw-lambda-transcribe | AWS_PROXY | none | none | yes |
| apigw-openapi-lambda-dynamodb-xray-terraform | from `body` | in the OpenAPI document | in the OpenAPI document | no: `body` |
| apigw-rest-api-eventbridge-terraform | AWS | VTL (`#set`, `#foreach`) | VTL (`#set`) | no: AWS integration |
| apigw-rest-api-http-integration-terraform | HTTP_PROXY | none | none | no: HTTP_PROXY answers 501 |
| apigw-rest-vpclink-pvt-alb-terraform | HTTP_PROXY over VPC_LINK (CLI in a `local-exec`) | none | none | no: VPC_LINK |
| apigw-sfn-crud-terraform | AWS (two) | VTL (`#set`, `$util.escapeJavaScript`) | VTL (`#set`) | no: AWS integration |
| apigw-sqs-terraform | from `body` | in the OpenAPI document | in the OpenAPI document | no: `body` |
| lambda-ddb-tenant-isolation-terraform | AWS_PROXY, MOCK (two OPTIONS) | `{"statusCode": 200}` on both MOCKs (literal) | `""` on both (literal) | no: request validator and model (stretch), integration `request_parameters` on AWS_PROXY, Lambda `tenancy_config` |
| lambda-df-slack | AWS_PROXY | none | none | yes (ECR image, Secrets Manager, `null_resource`) |
| public-alb-private-api-terraform | AWS_PROXY | none | none | no: PRIVATE endpoint |

Result: four patterns set `request_templates` in HCL; the three VTL ones are on `AWS` integrations, outside the first slice, and the fourth (lambda-ddb-tenant-isolation-terraform) is literal on MOCK. No pattern inside the first slice needs a non-literal template, so VTL stays deferred. The three `body` patterns keep their templates in an OpenAPI document that PutRestApi would import; it stays unbound.

The literal-only rule: a request or response template containing none of `$`, `#` or `\` is literal text and renders unchanged, since Velocity interprets only `$` references, `#` directives and `\` escapes ([Velocity 1.7 VTL reference](https://velocity.apache.org/engine/1.7/vtl-reference-guide.html), the dialect 7.1 infers for AWS). The recording supports the request side: a literal MOCK request template selects its status ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_basic.py:L1697-L1724, snapshot L154-L161). Any other template answers 501 naming the template, journaled (12.2); that test's response template uses `$input.params()` and is the refusal check.

The census pattern the first slice runs end to end is **apigw-lambda-rekognition**: one `POST /generate-presigned-url` AWS_PROXY Lambda method (`NONE`, no template, no parameter mapping, `source_arn` `${execution_arn}/*/*`) on a stage `dev`. Its documented check is a POST with `{"object_name": ..., "content_type": ...}` that answers 200 with `{"presigned_url": ...}`, then a PUT of an object to that URL, which triggers a function that calls Rekognition. The function code builds its S3 client with `endpoint_url='https://s3.<region>.amazonaws.com'` (`generate_presigned_url.zip`), so the URL it signs names AWS and the PUT would leave the machine; the code is not changed (the suite shape allows no code edits), so the check run is the POST and its 200 `presigned_url` answer, and the PUT and the Rekognition step are reported not run with that reason. Adjustments: runtime `python3.14` to `python3.13`. apigw-lambda-transcribe has the same shape and the same hard-coded endpoint, and is the fallback. No first-slice pattern uses `{proxy+}`; the Terraform conformance stack covers it.

Target (beyond the first slice). ADR 0016 allows no runtime dependency, and AWS behavior is never delegated to a library, so write a VTL subset interpreter and a JSONPath subset in Go:

- Grammar: text, `$ref`, `$!ref`, `${ref}`, property access with hyphenated identifiers, `[index]` and `["key"]`, method calls with arguments, string, number, boolean, list and map literals, `+` on strings and numbers, comparison and logical operators, `#set`, `#if`/`#elseif`/`#else`/`#end`, `#foreach` with `$foreach.hasNext`, `$foreach.count`, `$foreach.index`, and `##` and `#* *#` comments (comments are standard VTL, not seen in the corpus).
- Values: JSON objects as insertion-ordered maps rendering `{k=v, ...}`, lists rendering compact JSON, null rendering empty, `null == ""` true (7.2). Java methods on strings (`replaceAll` with Java regex semantics translated to RE2 where possible, `contains`, `toString`, `length`, `substring`, `indexOf`, `toLowerCase`, `toUpperCase`, `trim`, `split`, `equals`), maps (`get`, `put`, `keySet`, `size`, `containsKey`, `isEmpty`), lists (`size`, `get`, `isEmpty`). A call outside the supported set fails the invocation with 501 naming the method, journaled (12.2), never a silent empty string.
- Golden tests from every recorded output in sections 7.2, 7.3, 7.6 and 8, plus rapid properties: rendering a template with no directives returns it unchanged; `$util.urlDecode($util.urlEncode(s)) == s`; `$util.base64Decode($util.base64Encode(s)) == s`; `$util.parseJson($input.json(p))` equals `$input.path(p)` for every path the subset accepts.

### 12.8 Terraform

Meet the nine round-trip rules of 9.3, answer the Terraform waits at once (9.5), keep `PutMethodResponse` free of spurious `ConflictException` (Terraform retries them for two minutes, 9.1), and add a conformance stack per pattern in section 8 (SQS, EventBridge, DynamoDB, Step Functions, Lambda proxy with `{proxy+}`) whose second `terraform plan` is empty and whose invocations return the section 8 outputs.

### 12.9 What must never be faked

Authorizers, API keys and usage plans, caching, throttling, WAF, mutual TLS, VPC links, canaries, execution and access logs and X-Ray: the operations stay unbound or the settings are refused or marked stored without effect (12.2). A method whose authorization or key requirement cannot be honoured is refused at invoke, never served open. A selection pattern, VTL construct or JSONPath understudy cannot evaluate fails loudly (501 naming it, journaled, 12.2), never renders a guess.

## 13. Recording queue

Agents never run these. A maintainer runs them in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1 and 2: `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`) with `jq`, `zip` and `curl` installed. Every case writes under `$REC/APIGW1/`. Costs: REST API requests and Lambda invocations in these cases number in the hundreds, so each case rounds to USD 0.00 (estimate; under USD 0.01 for the whole queue). Times include the 30 s waits after deployments (propagation) and before `delete-rest-api` (1 call per 30 s per account, section 2.6).

### 13.1 Shared setup and teardown

Setup (about 5 minutes). It needs R03's role `$P-role` (trusted by `lambda.amazonaws.com`); if R03 has not run, create it first with the two `iam` lines marked `# only without R03`.

```sh
mkdir -p "$REC/APIGW1" && cd "$REC/APIGW1"
G=https://apigateway.$R.amazonaws.com
# only without R03:
aws iam create-role --role-name "$P-role" --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam attach-role-policy --role-name "$P-role" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole   # only without R03

# role API Gateway assumes for AWS integrations
aws iam create-role --role-name "$P-apigw1" --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"apigateway.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-apigw1" --policy-name send --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","events:PutEvents","lambda:InvokeFunction"],"Resource":"*"}]}'
CRED=arn:aws:iam::$A:role/$P-apigw1

# echo function: proxy results carry the event; non-proxy results are the event itself
cat >echo.py <<'EOF'
import json, time
def handler(event, context):
    if str(event.get("path", "")).endswith("/slow"):
        time.sleep(3)
    if "httpMethod" in event:
        return {"statusCode": 200, "body": json.dumps(event)}
    return event
EOF
zip -q echo.zip echo.py
aws lambda create-function --function-name "$P-apigw1-echo" --runtime python3.13 --handler echo.handler \
  --role "arn:aws:iam::$A:role/$P-role" --zip-file fileb://echo.zip --timeout 10 >/dev/null
aws lambda wait function-active-v2 --function-name "$P-apigw1-echo"
FN=arn:aws:lambda:$R:$A:function:$P-apigw1-echo
LURI=arn:aws:apigateway:$R:lambda:path/2015-03-31/functions/$FN/invocations

API=$(aws apigateway create-rest-api --name "$P-apigw1" --endpoint-configuration types=REGIONAL --query id --output text)
ROOT=$(aws apigateway get-resources --rest-api-id "$API" --query 'items[?path==`/`].id' --output text)
aws lambda add-permission --function-name "$P-apigw1-echo" --statement-id apigw1 --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:$R:$A:$API/*" >/dev/null
U=https://$API.execute-api.$R.amazonaws.com

res() {  # res <path-part>: create a child of / and print its id
  aws apigateway create-resource --rest-api-id "$API" --parent-id "$ROOT" --path-part "$1" --query id --output text
}
mock() {  # mock <resource-id> <http-method> <marker>: MOCK answering 200 {"marker": "<marker>"}
  aws apigateway put-method --rest-api-id "$API" --resource-id "$1" --http-method "$2" --authorization-type NONE >/dev/null
  aws apigateway put-integration --rest-api-id "$API" --resource-id "$1" --http-method "$2" --type MOCK \
    --request-templates '{"application/json":"{\"statusCode\": 200}"}' >/dev/null
  aws apigateway put-method-response --rest-api-id "$API" --resource-id "$1" --http-method "$2" --status-code 200 >/dev/null
  aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$1" --http-method "$2" --status-code 200 \
    --selection-pattern "" --response-templates "$(jq -nc --arg m "$3" '{"application/json": ("{\"marker\": \"" + $m + "\"}")}')" >/dev/null
}
deploy() {  # redeploy stage s1 and wait for propagation
  aws apigateway create-deployment --rest-api-id "$API" --stage-name s1 >/dev/null; sleep 30
}
inv() {  # inv <step> <curl args...>: unsigned data-plane request; status line, headers and body kept
  curl -sS -i --http1.1 "${@:2}" >"$REC/APIGW1/$1.http"
}

ITEMS=$(res items); mock "$ITEMS" GET get
deploy
```

Teardown (after all cases, about 2 minutes):

```sh
aws apigateway delete-rest-api --rest-api-id "$API"
aws lambda delete-function --function-name "$P-apigw1-echo"
aws iam delete-role-policy --role-name "$P-apigw1" --policy-name send
aws iam delete-role --role-name "$P-apigw1"
aws sqs delete-queue --queue-url "$Q/$P-apigw1" 2>/dev/null || true
```

`$P-role` stays for R13, R16 and R17; recording-kit section 5 removes it.

### 13.2 APIGW1-01: control-plane error wire form

Settles the base note's `error.type-header` item (12.3, 2.4): the exact `x-amzn-ErrorType` value (bare or with a `:http://...` suffix), whether the body has `message`, `Message` or `__type`, for a modeled 404, an unmodeled `ValidationException` and a `SerializationException`.

```sh
SVC=apigateway recurl APIGW1/01-notfound -H 'Accept: application/json' "$G/restapis/$P-missing"
SVC=apigateway recurl APIGW1/01-validation -X POST -H 'Accept: application/json' -H 'Content-Type: application/json' \
  -d '{"name":"'"$P"'-bad","endpointConfiguration":{"types":["WRONG"]}}' "$G/restapis"
SVC=apigateway recurl APIGW1/01-serialization -X PUT -H 'Accept: application/json' -H 'Content-Type: application/json' \
  -d '{"authorizationType":true}' "$G/restapis/$API/resources/$ITEMS/methods/POST"
```

Expected: three `.http` files with status 404, 400, 400. Cost USD 0.00, 2 minutes. Teardown: none.

### 13.3 APIGW1-02: identifier formats and seeded models

Settles the base note's `ids.format` item (12.3, 3.10) and whether `Empty` and `Error` models exist on a new API (3.9).

```sh
rec APIGW1/02-resources aws apigateway get-resources --rest-api-id "$API"
for i in 1 2 3 4 5 6 7 8; do res "id$i"; done >"$REC/APIGW1/02-resource-ids.txt"
rec APIGW1/02-validator aws apigateway create-request-validator --rest-api-id "$API" --name v1 --validate-request-body
rec APIGW1/02-model aws apigateway create-model --rest-api-id "$API" --name M1 --content-type application/json \
  --schema '{"type":"object","properties":{"a":{"type":"string"}}}'
rec APIGW1/02-models aws apigateway get-models --rest-api-id "$API"
rec APIGW1/02-deployments aws apigateway get-deployments --rest-api-id "$API"
echo "$API $ROOT" >"$REC/APIGW1/02-api-and-root.txt"
```

Expected: lengths and alphabets of each id kind; the model list. Cost USD 0.00, 3 minutes. Teardown: none (the shared API delete removes them).

### 13.4 APIGW1-03: apiStatus after create, update and delete

Settles `apigateway.restapi.api-status` (3.1) and the post-delete 404 Terraform waits for.

```sh
X=$(aws apigateway create-rest-api --name "$P-apigw1-status" --endpoint-configuration types=REGIONAL --query id --output text)
for i in 1 2 3 4 5; do rec "APIGW1/03-get-$i" aws apigateway get-rest-api --rest-api-id "$X"; sleep 1; done
rec APIGW1/03-list aws apigateway get-rest-apis
rec APIGW1/03-update aws apigateway update-rest-api --rest-api-id "$X" --patch-operations op=replace,path=/description,value=d
for i in 1 2 3; do rec "APIGW1/03-get-after-update-$i" aws apigateway get-rest-api --rest-api-id "$X"; sleep 1; done
sleep 30
rec APIGW1/03-delete aws apigateway delete-rest-api --rest-api-id "$X"
for i in 1 2 3; do rec "APIGW1/03-get-after-delete-$i" aws apigateway get-rest-api --rest-api-id "$X"; sleep 2; done
```

Expected: the `apiStatus` sequence; how soon `get-rest-api` answers `NotFoundException`. Cost USD 0.00, 3 minutes. Teardown: included (if the delete was throttled, rerun it after 30 s).

### 13.5 APIGW1-04: GetResources order and paging

Settles the GetResources note's `resources.list-order` item (12.3, 2.5) and the raw `item` and `position` keys.

```sh
for p in zeta alpha mid; do res "$p"; done >/dev/null
SVC=apigateway recurl APIGW1/04-all -H 'Accept: application/json' "$G/restapis/$API/resources"
SVC=apigateway recurl APIGW1/04-page1 -H 'Accept: application/json' "$G/restapis/$API/resources?limit=2"
POS=$(sed -n 's/.*"position":"\([^"]*\)".*/\1/p' "$REC/APIGW1/04-page1.http")
SVC=apigateway recurl APIGW1/04-page2 -G --data-urlencode "position=$POS" --data-urlencode limit=2 \
  -H 'Accept: application/json' "$G/restapis/$API/resources"
```

Expected: the order of `item` entries against creation order (`items`, then 13.3's `id1`..`id8` if run, then `zeta`, `alpha`, `mid`); the token format. Cost USD 0.00, 2 minutes. Teardown: none.

### 13.6 APIGW1-05: deploying an incomplete API

Settles the CreateDeployment note's `deployment.incomplete-api-errors` item (12.3, 3.6).

```sh
Y=$(aws apigateway create-rest-api --name "$P-apigw1-empty" --endpoint-configuration types=REGIONAL --query id --output text)
rec APIGW1/05-no-methods aws apigateway create-deployment --rest-api-id "$Y"
YR=$(aws apigateway get-resources --rest-api-id "$Y" --query 'items[0].id' --output text)
aws apigateway put-method --rest-api-id "$Y" --resource-id "$YR" --http-method GET --authorization-type NONE >/dev/null
rec APIGW1/05-no-integration aws apigateway create-deployment --rest-api-id "$Y"
```

Expected: two error codes and messages. Cost USD 0.00, 2 minutes. Teardown: `sleep 30; aws apigateway delete-rest-api --rest-api-id "$Y"`.

### 13.7 APIGW1-06: stage error texts

Settles the stage notes' `stage.error-texts` item (12.3, 3.7).

```sh
D1=$(aws apigateway get-deployments --rest-api-id "$API" --query 'items[0].id' --output text)
rec APIGW1/06-duplicate aws apigateway create-stage --rest-api-id "$API" --stage-name s1 --deployment-id "$D1"
rec APIGW1/06-get-missing aws apigateway get-stage --rest-api-id "$API" --stage-name nosuch
rec APIGW1/06-delete-missing aws apigateway delete-stage --rest-api-id "$API" --stage-name nosuch
rec APIGW1/06-bad-deployment aws apigateway create-stage --rest-api-id "$API" --stage-name s2 --deployment-id nosuch
rec APIGW1/06-bad-name aws apigateway create-stage --rest-api-id "$API" --stage-name bad.name --deployment-id "$D1"
rec APIGW1/06-long-name aws apigateway create-stage --rest-api-id "$API" --stage-name "$(printf 'a%.0s' $(seq 129))" --deployment-id "$D1"
```

Expected: six errors with codes and messages. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-stage --rest-api-id "$API" --stage-name s2 2>/dev/null; aws apigateway delete-stage --rest-api-id "$API" --stage-name bad.name 2>/dev/null; true`.

### 13.8 APIGW1-07: unknown stage, stage root and unknown route

Settles switch `apigateway.invoke.routing`, its unknown-stage answer (4.7), and the root matching rule of 4.3.

```sh
inv 07-unknown-stage "$U/nosuch/items"
inv 07-no-stage "$U/"
inv 07-stage-root "$U/s1"
inv 07-stage-root-slash "$U/s1/"
inv 07-unknown-route "$U/s1/nosuch"
inv 07-known-route "$U/s1/items"
```

Expected: status, body and `x-amzn-ErrorType` of each; `07-unknown-route` should reproduce the recorded 403. Cost USD 0.00, 1 minute. Teardown: none.

### 13.9 APIGW1-08: explicit method against ANY, and HEAD

Settles switch `apigateway.invoke.routing`, its method precedence (4.4).

```sh
mock "$ITEMS" ANY any
OG=$(res onlyget); mock "$OG" GET get
deploy
inv 08-get-with-any "$U/s1/items"
inv 08-post-with-any "$U/s1/items" -X POST -H 'Content-Type: application/json' -d '{}'
inv 08-head-with-any "$U/s1/items" -I
inv 08-head-onlyget "$U/s1/onlyget" -I
inv 08-options-onlyget "$U/s1/onlyget" -X OPTIONS
```

Expected: which marker answers each (`get` or `any`), and the statuses for `HEAD` and `OPTIONS` on a GET-only resource. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-method --rest-api-id "$API" --resource-id "$ITEMS" --http-method ANY; aws apigateway delete-resource --rest-api-id "$API" --resource-id "$OG"`.

### 13.10 APIGW1-09: 415 text and media-type matching

Settles switch `apigateway.mock.selection`, its 415 answer and media-type matching (6.1, 6.2).

```sh
PT=$(res pt); mock "$PT" POST pt
aws apigateway update-integration --rest-api-id "$API" --resource-id "$PT" --http-method POST \
  --patch-operations op=replace,path=/passthroughBehavior,value=NEVER >/dev/null
deploy
inv 09-xml "$U/s1/pt" -X POST -H 'Content-Type: application/xml' -d '<a/>'
inv 09-json "$U/s1/pt" -X POST -H 'Content-Type: application/json' -d '{}'
inv 09-json-charset "$U/s1/pt" -X POST -H 'Content-Type: application/json; charset=utf-8' -d '{}'
inv 09-json-upper "$U/s1/pt" -X POST -H 'Content-Type: Application/JSON' -d '{}'
inv 09-none "$U/s1/pt" -X POST -H 'Content-Type:' -d '{}'
```

Expected: the 415 status, body and `x-amzn-ErrorType`; whether parameters and case are ignored. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$PT"`.

### 13.11 APIGW1-10: integration timeout

Settles switch `apigateway.proxy.unrecorded`, its timeout answer (5.3).

```sh
SL=$(res slow)
aws apigateway put-method --rest-api-id "$API" --resource-id "$SL" --http-method GET --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$SL" --http-method GET --type AWS_PROXY \
  --integration-http-method POST --uri "$LURI" --timeout-in-millis 1000 >/dev/null
deploy
inv 10-timeout "$U/s1/slow"
```

Expected: status (504?), body and `x-amzn-ErrorType` after about 1 s. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$SL"`.

### 13.12 APIGW1-11: response template selection

Settles switch `apigateway.mock.selection`, its response-template choice (6.7).

```sh
RT=$(res rt)
aws apigateway put-method --rest-api-id "$API" --resource-id "$RT" --http-method GET --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$RT" --http-method GET --type MOCK \
  --request-templates '{"application/json":"{\"statusCode\": 200}"}' >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$RT" --http-method GET --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$RT" --http-method GET --status-code 200 \
  --selection-pattern "" --response-templates '{"application/xml":"<r>xml</r>","text/plain":"plain"}' >/dev/null
deploy
inv 11-accept-xml "$U/s1/rt" -H 'Accept: application/xml'
inv 11-accept-plain "$U/s1/rt" -H 'Accept: text/plain'
inv 11-accept-json "$U/s1/rt" -H 'Accept: application/json'
inv 11-accept-none "$U/s1/rt" -H 'Accept:'
inv 11-accept-two "$U/s1/rt" -H 'Accept: text/html, text/plain'
```

Expected: which template renders and the response `Content-Type` for each. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$RT"`.

### 13.13 APIGW1-12: selectionPattern anchoring and ties

Settles switch `apigateway.mock.selection`, its selectionPattern anchoring and order (6.7). A MOCK renders status 404; integration responses compete for it.

```sh
sel() {  # sel <path-part> <method-statuses> then pairs "<status>=<pattern>" in creation order
  local r; r=$(res "$1"); local codes=$2; shift 2
  aws apigateway put-method --rest-api-id "$API" --resource-id "$r" --http-method GET --authorization-type NONE >/dev/null
  aws apigateway put-integration --rest-api-id "$API" --resource-id "$r" --http-method GET --type MOCK \
    --request-templates '{"application/json":"{\"statusCode\": 404}"}' >/dev/null
  for c in $codes; do aws apigateway put-method-response --rest-api-id "$API" --resource-id "$r" --http-method GET --status-code "$c" >/dev/null; done
  for pair in "$@"; do
    aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$r" --http-method GET \
      --status-code "${pair%%=*}" --selection-pattern "${pair#*=}" \
      --response-templates "$(jq -nc --arg m "${pair%%=*}" '{"application/json": ("{\"picked\": \"" + $m + "\"}")}')" >/dev/null
  done
}
sel anchor "200 404" "200=" "404=40"
sel tie-a "200 400 404" "200=" "400=4\\d{2}" "404=404"
sel tie-b "200 400 404" "200=" "404=404" "400=4\\d{2}"
sel tie-c "200 401 499" "200=" "499=4\\d{2}" "401=40\\d"
deploy
for p in anchor tie-a tie-b tie-c; do inv "12-$p" "$U/s1/$p"; done
```

Expected: `anchor` answering 200 means full-match semantics; the `tie-*` results show whether order is by status code, creation or specificity. Cost USD 0.00, 4 minutes. Teardown: `for p in anchor tie-a tie-b tie-c; do aws apigateway delete-resource --rest-api-id "$API" --resource-id "$(aws apigateway get-resources --rest-api-id "$API" --query "items[?pathPart=='$p'].id" --output text)"; done`.

### 13.14 APIGW1-13: empty-string request templates on the wire

Settles the integration notes' `templates.empty-value` item for request templates (12.3, 3.5).

```sh
TM=$(res tmpl)
aws apigateway put-method --rest-api-id "$API" --resource-id "$TM" --http-method GET --authorization-type NONE >/dev/null
SVC=apigateway recurl APIGW1/13-put -X PUT -H 'Accept: application/json' -H 'Content-Type: application/json' \
  -d '{"type":"MOCK","requestTemplates":{"application/json":"","text/plain":"x"}}' \
  "$G/restapis/$API/resources/$TM/methods/GET/integration"
SVC=apigateway recurl APIGW1/13-get -H 'Accept: application/json' "$G/restapis/$API/resources/$TM/methods/GET/integration"
```

Expected: `"application/json": null` or `""` in both. Cost USD 0.00, 1 minute. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$TM"`.

### 13.15 APIGW1-14: duplicate REST API names

Settles the CreateRestApi note's `restapi.duplicate-names` item (12.3, 3.1).

```sh
rec APIGW1/14-duplicate aws apigateway create-rest-api --name "$P-apigw1" --endpoint-configuration types=REGIONAL
```

Expected: success with a new id, or an error. Cost USD 0.00, 1 minute. Teardown: `sleep 30; aws apigateway delete-rest-api --rest-api-id "$(sed -n 's/^ *"id": "\([^"]*\)".*/\1/p' "$REC/APIGW1/14-duplicate.out")"` (only if it succeeded).

### 13.16 APIGW1-15: Lambda proxy without invoke permission

Settles what switch `apigateway.lambda.permission` must produce (5.3, 5.4).

```sh
PX=$(res px)
aws apigateway put-method --rest-api-id "$API" --resource-id "$PX" --http-method ANY --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$PX" --http-method ANY --type AWS_PROXY \
  --integration-http-method POST --uri "$LURI" >/dev/null
deploy
inv 15-with-permission "$U/s1/px"
aws lambda remove-permission --function-name "$P-apigw1-echo" --statement-id apigw1
sleep 60
inv 15-without-permission "$U/s1/px"
aws lambda add-permission --function-name "$P-apigw1-echo" --statement-id apigw1 --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:$R:$A:$API/*" >/dev/null
sleep 60
```

Expected: status, body and `x-amzn-ErrorType` without permission. Cost USD 0.00, 4 minutes. Teardown: none (13.17 reuses `/px`; the shared teardown removes it).

### 13.17 APIGW1-16: proxy event edges

Settles switch `apigateway.proxy.unrecorded`, its empty-body event, and the REGIONAL event shape (5.1, 4.5). Run after 13.16.

```sh
aws apigateway update-rest-api --rest-api-id "$API" --patch-operations op=add,path=/binaryMediaTypes/application~1octet-stream >/dev/null
deploy
inv 16-get "$U/s1/px"
inv 16-post-empty "$U/s1/px" -X POST
inv 16-post-empty-json "$U/s1/px" -X POST -H 'Content-Type: application/json'
inv 16-post-text "$U/s1/px" -X POST -H 'Content-Type: text/plain' -d 'hello'
inv 16-post-binary "$U/s1/px" -X POST -H 'Content-Type: application/octet-stream' --data-binary @echo.zip
inv 16-query "$U/s1/px?a=1&a=2&b=&c&d=x%2By+z"
inv 16-headers "$U/s1/px" -H 'aUThorization: x' -H 'X-Multi: 1' -H 'X-Multi: 2'
```

Expected: the logged events in the response bodies: `body` and `isBase64Encoded` per case, `queryStringParameters` for `b=` and `c`, header casing on a REGIONAL endpoint. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway update-rest-api --rest-api-id "$API" --patch-operations op=remove,path=/binaryMediaTypes/application~1octet-stream`.

### 13.18 APIGW1-17: escapeJavaScript

Settles 12.3's out-of-scope `vtl.escape-javascript` item (7.6). A Lambda custom integration receives `{"e": "<escaped body>"}`; the body has no single quote, so the payload stays valid JSON.

```sh
NP=$(res np17)
aws apigateway put-method --rest-api-id "$API" --resource-id "$NP" --http-method POST --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$NP" --http-method POST --type AWS \
  --integration-http-method POST --uri "$LURI" \
  --request-templates "$(jq -nc '{"application/json": "{\"e\": \"$util.escapeJavaScript($input.body)\", \"u\": \"$util.urlEncode($input.body)\", \"b\": \"$util.base64Encode($input.body)\"}"}')" >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$NP" --http-method POST --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$NP" --http-method POST --status-code 200 --selection-pattern "" >/dev/null
deploy
printf '"/\\<>&\t\xc3\xa9\xe2\x82\xac~' >escape-body.txt
inv 17-escape "$U/s1/np17" -X POST -H 'Content-Type: application/json' --data-binary @escape-body.txt
```

Expected: the echoed `e`, `u`, `b` values (after the Lambda's own JSON decoding, so compare with the raw `.http` body). Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$NP"`.

### 13.19 APIGW1-18: $input.json on missing paths and numbers

Settles 12.3's out-of-scope `vtl.input-json` item (7.3).

```sh
# the template goes through a file: its '...' JSONPath literals cannot sit inside a single-quoted shell argument
cat >t18.vtl <<'EOF'
{"missing": "$util.escapeJavaScript($input.json('$.missing'))", "n": "$util.escapeJavaScript($input.json('$.n'))", "f": "$util.escapeJavaScript($input.json('$.f'))", "big": "$util.escapeJavaScript($input.json('$.big'))", "all": "$util.escapeJavaScript($input.json('$'))", "pathmissing": "$input.path('$.missing')"}
EOF
NJ=$(res np18)
aws apigateway put-method --rest-api-id "$API" --resource-id "$NJ" --http-method POST --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$NJ" --http-method POST --type AWS \
  --integration-http-method POST --uri "$LURI" \
  --request-templates "$(jq -nc --rawfile t t18.vtl '{"application/json": $t}')" >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$NJ" --http-method POST --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$NJ" --http-method POST --status-code 200 --selection-pattern "" >/dev/null
deploy
inv 18-numbers "$U/s1/np18" -X POST -H 'Content-Type: application/json' -d '{"n": 10.00, "f": 1e3, "big": 12345678901234567890, "s": "x"}'
inv 18-not-json "$U/s1/np18" -X POST -H 'Content-Type: application/json' -d 'not json'
```

Expected: each field's rendering; what a non-JSON body does to `$input.json('$')`. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$NJ"`.

### 13.20 APIGW1-19: AWS integration backend error with only a default response

Settles 12.3's out-of-scope `aws-integration.error-status` item (6.4) and records the JSON rendering of `SendMessageResponse` and of an SQS error.

```sh
aws sqs create-queue --queue-name "$P-apigw1" >/dev/null
cat >rp.json <<'EOF'
{"integration.request.header.Content-Type":"'application/x-www-form-urlencoded'"}
EOF
cat >rt.json <<'EOF'
{"application/json":"Action=SendMessage&MessageBody=$input.body"}
EOF
sqsres() {  # sqsres <path-part> <queue-name>
  local r; r=$(res "$1")
  aws apigateway put-method --rest-api-id "$API" --resource-id "$r" --http-method POST --authorization-type NONE >/dev/null
  aws apigateway put-integration --rest-api-id "$API" --resource-id "$r" --http-method POST --type AWS \
    --integration-http-method POST --uri "arn:aws:apigateway:$R:sqs:path/$A/$2" --credentials "$CRED" \
    --request-parameters file://rp.json --request-templates file://rt.json --passthrough-behavior NEVER >/dev/null
  aws apigateway put-method-response --rest-api-id "$API" --resource-id "$r" --http-method POST --status-code 200 >/dev/null
  aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$r" --http-method POST --status-code 200 --selection-pattern "" >/dev/null
}
sqsres sqs "$P-apigw1"
sqsres sqsmissing "$P-missing"
deploy
inv 19-ok "$U/s1/sqs" -X POST -H 'Content-Type: application/json' -d '{"a": 1}'
inv 19-missing-queue "$U/s1/sqsmissing" -X POST -H 'Content-Type: application/json' -d '{"a": 1}'
```

Expected: 19-ok body is the JSON `SendMessageResponse`; 19-missing-queue status (200 with the SQS error JSON, or the SQS 400) and body. Cost USD 0.00, 3 minutes. Teardown: `for p in sqs sqsmissing; do aws apigateway delete-resource --rest-api-id "$API" --resource-id "$(aws apigateway get-resources --rest-api-id "$API" --query "items[?pathPart=='$p'].id" --output text)"; done` (the queue goes in the shared teardown).

### 13.21 APIGW1-20: action URIs without header overrides

Settles 12.3's out-of-scope `aws-integration.action-translation` item (6.4).

```sh
cat >eb.json <<'EOF'
{"application/json":"{\"Entries\":[{\"Source\":\"apigw1\",\"DetailType\":\"t\",\"Detail\":\"{}\"}]}"}
EOF
EB=$(res eb)
aws apigateway put-method --rest-api-id "$API" --resource-id "$EB" --http-method POST --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$EB" --http-method POST --type AWS \
  --integration-http-method POST --uri "arn:aws:apigateway:$R:events:action/PutEvents" --credentials "$CRED" \
  --request-templates file://eb.json >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$EB" --http-method POST --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$EB" --http-method POST --status-code 200 \
  --selection-pattern "" --response-templates '{"application/json":"$input.json('"'"'$'"'"')"}' >/dev/null
cat >sqsa.json <<EOF
{"application/json":"QueueUrl=$Q/$P-apigw1&MessageBody=action-uri"}
EOF
SA=$(res sqsaction)
aws apigateway put-method --rest-api-id "$API" --resource-id "$SA" --http-method POST --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$SA" --http-method POST --type AWS \
  --integration-http-method POST --uri "arn:aws:apigateway:$R:sqs:action/SendMessage" --credentials "$CRED" \
  --request-templates file://sqsa.json >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$SA" --http-method POST --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$SA" --http-method POST --status-code 200 --selection-pattern "" >/dev/null
deploy
inv 20-putevents "$U/s1/eb" -X POST -H 'Content-Type: application/json' -d '{}'
inv 20-sqs-action "$U/s1/sqsaction" -X POST -H 'Content-Type: application/json' -d '{}'
```

Expected: whether PutEvents succeeds (`Entries` with an `EventId`) with no `X-Amz-Target` override, and whether an action URI to a Query service accepts a form body. Cost USD 0.00 (one custom event, one message), 3 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$EB"; aws apigateway delete-resource --rest-api-id "$API" --resource-id "$SA"`.

### 13.22 APIGW1-21: JSONPath beyond simple paths

Settles 12.3's out-of-scope `vtl.jsonpath` item (7.7).

```sh
# the template goes through a file: its '...' JSONPath literals cannot sit inside a single-quoted shell argument
cat >t21.vtl <<'EOF'
{"wild": "$util.escapeJavaScript($input.json('$.a[*].b'))", "deep": "$util.escapeJavaScript($input.json('$..b'))", "filter": "$util.escapeJavaScript($input.json('$.a[?(@.b > 1)]'))", "quoted": "$util.escapeJavaScript($input.json('$["k e"]'))", "len": "$util.escapeJavaScript($input.json('$.a.length()'))", "size": "$input.path('$.a').size()"}
EOF
NX=$(res np21)
aws apigateway put-method --rest-api-id "$API" --resource-id "$NX" --http-method POST --authorization-type NONE >/dev/null
aws apigateway put-integration --rest-api-id "$API" --resource-id "$NX" --http-method POST --type AWS \
  --integration-http-method POST --uri "$LURI" \
  --request-templates "$(jq -nc --rawfile t t21.vtl '{"application/json": $t}')" >/dev/null
aws apigateway put-method-response --rest-api-id "$API" --resource-id "$NX" --http-method POST --status-code 200 >/dev/null
aws apigateway put-integration-response --rest-api-id "$API" --resource-id "$NX" --http-method POST --status-code 200 --selection-pattern "" >/dev/null
deploy
inv 21-jsonpath "$U/s1/np21" -X POST -H 'Content-Type: application/json' -d '{"a": [{"b": 1}, {"b": 2}], "k e": "v"}'
```

Expected: each field's rendering, or a 500 if a construct breaks the template. Cost USD 0.00, 2 minutes. Teardown: `aws apigateway delete-resource --rest-api-id "$API" --resource-id "$NX"`.

### 13.23 APIGW1-22: control-plane IAM denials and resources

Settles: the identity of an IAM denial on a v1 control-plane call (status, `x-amzn-ErrorType`, message key and text; the v1 model has no `AccessDeniedException` shape, 2.4, `iam-enforcement-surfaces.md` 12.3); where the message names the resource, the resource of each call (3.10): the collection and item forms, CreateResource's parent item, the method, integration and response items, and the tags form with its encoding; and, from steps 14 to 16, whether a tag call also needs a permission on the tagged API (3.10). Step 01 repeats IAM-ENF-33 (`iam-enforcement-surfaces.md` 13.35); either recording settles the identity. In `notes.md`, write per step the status line, `x-amzn-ErrorType`, the body, and the action and resource the message names, if it names one. Five minutes, USD 0.

The role `$P-apigw1-caller` has no policy, so every call below should be denied and change nothing until the policy before step 14 allows the tag resource.

```sh
aws iam create-role --role-name "$P-apigw1-caller" --assume-role-policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::$A:root\"},\"Action\":\"sts:AssumeRole\"}]}" >/dev/null
sleep 15                                   # the new role must propagate
read -r AK SK ST <<<"$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-apigw1-caller" --role-session-name gw22 \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
deny() {  # deny <step> <apigateway args...>: one call as the $P-apigw1-caller session, kept as rec keeps it
  ( unset AWS_PROFILE; export AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" AWS_SESSION_TOKEN="$ST"
    rec "APIGW1/22-$1" aws apigateway "${@:2}" )
}
TAGGED="arn:aws:apigateway:$R::/restapis/$API"
deny 01-get-rest-apis get-rest-apis
deny 02-create-rest-api create-rest-api --name "$P-apigw1-denied"
deny 03-get-rest-api get-rest-api --rest-api-id "$API"
deny 04-update-rest-api update-rest-api --rest-api-id "$API" --patch-operations op=replace,path=/description,value=x
deny 05-get-resources get-resources --rest-api-id "$API"
deny 06-create-resource create-resource --rest-api-id "$API" --parent-id "$ROOT" --path-part denied
deny 07-get-method get-method --rest-api-id "$API" --resource-id "$ITEMS" --http-method GET
deny 08-get-method-response get-method-response --rest-api-id "$API" --resource-id "$ITEMS" --http-method GET --status-code 200
deny 09-get-integration get-integration --rest-api-id "$API" --resource-id "$ITEMS" --http-method GET
deny 10-get-integration-response get-integration-response --rest-api-id "$API" --resource-id "$ITEMS" --http-method GET --status-code 200
deny 11-create-deployment create-deployment --rest-api-id "$API" --stage-name s1
deny 12-get-stage get-stage --rest-api-id "$API" --stage-name s1
deny 13-get-tags get-tags --resource-arn "$TAGGED"
aws iam put-role-policy --role-name "$P-apigw1-caller" --policy-name tags --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"apigateway:*\",\"Resource\":\"arn:aws:apigateway:$R::/tags/*\"}]}"
sleep 15                                   # the new role policy must propagate
deny 14-get-tags-tags-only get-tags --resource-arn "$TAGGED"
deny 15-tag-resource-tags-only tag-resource --resource-arn "$TAGGED" --tags k=v
deny 16-untag-resource-tags-only untag-resource --resource-arn "$TAGGED" --tag-keys k
aws iam delete-role-policy --role-name "$P-apigw1-caller" --policy-name tags
unset AK SK ST
```

A success in steps 14 to 16 means the `/tags/...` resource alone authorizes the call; a denial naming `$TAGGED` means the tagged API needs its own permission. If step 02 is not denied, delete its API with the shared teardown's `delete-rest-api` (after the 30 s wait); if step 06 or 11 is not denied, note it, since it changed the shared API. Teardown: `aws iam delete-role --role-name "$P-apigw1-caller"`.
