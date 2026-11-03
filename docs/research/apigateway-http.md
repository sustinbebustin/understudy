# API Gateway HTTP APIs (v2): primary-source research

Scope: roadmap item 6, the API Gateway v2 control plane for HTTP APIs (APIs, routes, integrations, stages, deployments, tags, CORS, authorizers), the `execute-api` data plane (stage and route selection, Lambda proxy payload formats 1.0 and 2.0, the responses API Gateway writes itself), AWS service integrations (integration subtypes), HTTP proxy integrations, IAM, what Terraform AWS provider v6.68.0 sends and reads, and how a local emulator is addressed. Sources: the Smithy model `aws/api-models-aws` at `1cfdfb4bb9ea858e65623e66d73af04bec353201`, the API Gateway Developer Guide, the API Gateway v2 API Reference, the Lambda Developer Guide and the AWS CLI reference on docs.aws.amazon.com, AWS-published samples on GitHub, the Terraform provider source and acceptance tests, aws-lambda-go, and, as hints only, LocalStack and AWS's .NET Lambda test tool. All retrieved 2026-10-25. WebSocket APIs, REST APIs (v1), custom domain names, VPC links and the developer portal are out of scope except where they constrain HTTP APIs.

## 0. How to read this

| Tag | Meaning | Pin |
|---|---|---|
| [S] | Smithy model `com.amazonaws.apigatewayv2#ApiGatewayV2`, service version 2018-11-29 | [apigatewayv2-2018-11-29.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/apigatewayv2/service/2018-11-29/apigatewayv2-2018-11-29.json); vendored at the same commit in `third_party/api-models-aws/` on 2026-10-26 |
| [D] | AWS documentation on docs.aws.amazon.com, fetched 2026-10-25 | URL on each claim |
| [A] | AWS-published samples: the Lambda Developer Guide's sample apps (real captures with `xmpl` substituted into identifiers), the AWS CLI's example outputs | [awsdocs/aws-lambda-developer-guide @ 0d45f4a](https://github.com/awsdocs/aws-lambda-developer-guide/tree/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050), [aws/aws-cli @ e9993cf](https://github.com/aws/aws-cli/tree/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2) |
| [TF] | hashicorp/terraform-provider-aws v6.68.0 (commit `c9b83d81`). A bare file name is under `internal/service/apigatewayv2/`; other paths are from the repository root. Acceptance tests (`*_test.go`) assert values read back from real AWS | [v6.68.0](https://github.com/hashicorp/terraform-provider-aws/tree/v6.68.0) |
| [C] | Client source: aws-lambda-go, hashicorp/aws-sdk-go-base | [aws-lambda-go @ 4111853](https://github.com/aws/aws-lambda-go/tree/41118539f90fa1989458560031d82a16b3686722/events), [aws-sdk-go-base v2.0.0-beta.73](https://github.com/hashicorp/aws-sdk-go-base/tree/v2.0.0-beta.73) |
| [R] | Third-party-recorded: LocalStack snapshots whose test has a `last_validated_date`. LocalStack's archived repository has **no HTTP API (v2) tests**, so every [R] item here is a REST API (v1) analogue | localstack@8b9a79f |
| [E] | Another emulator, hints only: AWS's own `LambdaTestTool-v2` (an emulator written by AWS, not the service), LocalStack's provider code | [aws/aws-lambda-dotnet @ f43a9f0](https://github.com/aws/aws-lambda-dotnet/tree/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2) |

The section 14 cases APIGW2-01..15 were recorded on 2026-11-03 (us-east-2, `testdata/parity/apigatewayv2/`); a claim they settle cites the case as `(APIGW2-NN, recorded 2026-11-03)`, and "differs" marks where AWS contradicted this note's earlier reading. "UNVERIFIED:" marks a behavior no source fixes, with the reason it stays open.

## 1. Findings that change the design

1. **There is no SNS integration subtype.** HTTP APIs support exactly ten subtypes: `EventBridge-PutEvents`, `SQS-SendMessage`, `SQS-ReceiveMessage`, `SQS-DeleteMessage`, `SQS-PurgeQueue`, `AppConfig-GetConfiguration`, `Kinesis-PutRecord`, `StepFunctions-StartExecution`, `StepFunctions-StartSyncExecution`, `StepFunctions-StopExecution` ([D subtype reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services-reference.html)). The roadmap's "SQS, SNS and EventBridge direct integrations" (`docs/ROADMAP.md` item 6) must drop SNS (section 6.1).
2. **Integrations are synchronous request and response, with no retries.** API Gateway invokes Lambda "synchronously" and "does not retry any Lambda invocations" ([D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html), [D services-apigateway-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway-errors.html)); response parameters "transform the HTTP response from a backend integration before returning the response to clients", configured "for each status code that your integration returns" ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)), so a service integration answers the client from the service's response. APIGW2-14 (recorded 2026-11-03) saw no sign of a retry: failed service calls answered no slower than a successful one (0.15 to 0.17 s against 0.22 s). They are `kernel.Invoke` calls made while the client waits, not outbox `delivery.Target` variants with retry policies (ARCHITECTURE.md 16). The roadmap's "new `delivery.Target` variants" does not fit (section 13.8).
3. **The control plane signs as `apigateway`, the same signing name as REST APIs.** v2 is `aws.auth#sigv4` name `apigateway`, endpoint prefix `apigateway`, every URI under `/v2/` ([S] service shape); REST APIs (`com.amazonaws.apigateway#BackplaneControlService`, [model @ 1cfdfb4](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/api-gateway/service/2015-07-09/api-gateway-2015-07-09.json)) also sign as `apigateway` on the same host, under `/restapis`, `/tags`, `/vpclinks` and others. understudy's registry refused two services with one signing name (`DuplicateSigningName`, `internal/registry/registry.go`). ADR 0019 (accepted 2026-10-26) settles it: `apigateway` (v1) and `apigatewayv2` are two registry services on the one signing name, which the registry accepts because their route tables are disjoint (section 13.1).
4. **The model carries almost no constraints for HTTP API shapes.** Shapes named `StringWithLengthBetween1And128`, `UriWithLengthBetween1And2048` and the like have empty traits; the only `@range` traits that touch HTTP APIs are `IntegerWithLengthBetween50And30000` (TimeoutInMillis), `IntegerWithLengthBetweenMinus1And86400` (Cors MaxAge) and `IntegerWithLengthBetween0And3600` (authorizer TTL) ([S], jq scan of `smithy.api#length|range|pattern`). Stage names, stage variables, route keys and quotas come from the docs (sections 2.4, 3.9) and are hand-coded.
5. **Terraform builds `invoke_url` itself, always on `amazonaws.com`.** `aws_apigatewayv2_stage.invoke_url` is `https://<api-id>.execute-api.<region>.amazonaws.com/<stage>` from the partition's DNS suffix, ignoring endpoint configuration ([TF] `internal/conns/awsclient.go:L313-L323`, `L362-L369`); `api_endpoint` is the server's `ApiEndpoint` ([TF] `api.go:L270`); `execution_arn` is built from the provider's account id ([TF] `api.go:L547-L549`). Tests against understudy must build URLs from `api_endpoint`, never `invoke_url` (section 9.7).
6. **Terraform's default payload format is 1.0.** `payload_format_version` defaults to `"1.0"` ([TF] `integration.go:L131-L139`), so a Terraform user who does not set it gets 1.0 events. Both formats are needed in the first slice (sections 5.1, 5.2).
7. **A deployment is a snapshot.** "You must deploy an API for changes to take effect. If you enable automatic deployments, changes to an API are automatically released for you" ([D stages](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.html)). A stage without `autoDeploy` serves its deployment's configuration, not the live one. Terraform's deployment resource waits for `DeploymentStatus` `DEPLOYED` and fails on any other final value ([TF] `deployment.go:L226-L246`).
8. **A named stage stays in the path the function sees.** In AWS's sample event the stage `default` appears in `rawPath` and `requestContext.http.path` (`/default/nodejs-apig-function-...`) while `routeKey` is `ANY /nodejs-apig-function-...` ([A event-v2.json:L3-L4, L34, L41](https://github.com/awsdocs/aws-lambda-developer-guide/blob/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050/sample-apps/nodejs-apig/event-v2.json)). The `$default` stage serves from the URL root ([D stages](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.html)).
9. **A Lambda integration without permission fails with `{"message":"Internal Server Error"}`.** The troubleshooting guide names the missing `lambda:InvokeFunction` permission as a cause of that body ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)). understudy records access and never enforces it (`internal/access/doc.go`), but the function URL front end already evaluates resource policies because the refusal is observable (`internal/service/lambda/furl/doc.go`, ARCHITECTURE.md 14.7). The HTTP API data plane must do the same (section 13.3).
10. **`HTTP_PROXY` is outbound network.** An HTTP proxy integration calls "a publicly routable HTTP endpoint" ([D HTTP proxy](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-http.html)); ADR 0015 already treats outbound HTTP from the emulator as an SSRF vector and gates SNS HTTP delivery behind an empty-by-default allow-list. The same gate applies (section 7.2).
11. **understudy's IAM action table carries `apigateway` and no `execute-api`.** When this note was written `third_party/aws-service-reference/actions.json` had neither prefix; the bump of 2026-10-26 added `apigateway`, which lists `apigateway:<the operation's HTTP method>` for every v2 operation except the portal and routing-rule ones (those have named actions such as `apigateway:GetPortal`), and `codegen/services/apigatewayv2.json` takes that method as each operation's primary action (`iamPrimary`). `execute-api` still has no entry, and the raw data-plane operation `InvokeApi` declares no action: AWS evaluates no IAM on a `NONE` route (sections 8.1 to 8.3).
12. **The gateway's own errors are small JSON documents with an `apigw-requestid` header**: `{"message":"Not Found"}` for no stage or route ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)), `{"message":"Internal Server Error"}` for Lambda integration failures ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)), `401 {"message":"Unauthorized"}` from a JWT authorizer with header `apigw-requestid: Mc7UVioPPHcEKPA=` ([D JWT troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-jwt.html)). REST APIs answer differently (`{"message": "Internal server error"}`, section 12), so nothing can be borrowed from REST snapshots.

## 2. Control plane wire contract

### 2.1 Service shape, protocol and signing

- Protocol `aws.protocols#restJson1`; auth `aws.auth#sigv4` with name `apigateway`; `aws.api#service` `{sdkId: "ApiGatewayV2", arnNamespace: "apigateway", endpointPrefix: "apigateway", cloudTrailEventSource: "apigatewayv2.amazonaws.com"}` ([S] `com.amazonaws.apigatewayv2#ApiGatewayV2`).
- Endpoint rule tests: `https://apigateway.us-east-1.amazonaws.com`; FIPS `apigateway-fips.<region>.amazonaws.com`; dual-stack `apigateway.<region>.api.aws`; a custom endpoint is used as given ([S] `smithy.rules#endpointTests`). No `smithy.api#endpoint` or `hostLabel` traits: no operation prefixes the host.
- Members carry `smithy.api#jsonName` in camelCase, so the wire body is `{"name":"x","protocolType":"HTTP"}` and responses are `{"apiId":...,"apiEndpoint":...}` ([S] e.g. `com.amazonaws.apigatewayv2#CreateApiRequest$Name` has `jsonName` `name`). The CLI prints PascalCase because botocore maps member names. understudy's restJson codec already honors `jsonName` (`internal/protocol/restjson/encode.go`, `internal/protocol/restjson/decode.go`).
- Timestamps target `__timestampIso8601` with `timestampFormat: date-time` (RFC 3339 strings) ([S]). The CLI shows `"2020-04-06T23:38:08Z"` ([D create-deployment](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-deployment.html)). No fractional seconds: `2026-11-03T16:43:26Z` (APIGW2-03, recorded 2026-11-03).

### 2.2 Operations and HTTP bindings

The model has 103 operations ([S]). The HTTP-API-relevant ones (method, URI, success status, modeled errors; B = BadRequestException, C = ConflictException, N = NotFoundException, T = TooManyRequestsException):

| Operation | Method and URI | Success | Errors |
|---|---|---|---|
| CreateApi | `POST /v2/apis` | 201 | B C N T |
| GetApis | `GET /v2/apis?maxResults=&nextToken=` | 200 | B N T |
| GetApi | `GET /v2/apis/{ApiId}` | 200 | N T |
| UpdateApi | `PATCH /v2/apis/{ApiId}` | 200 | B C N T |
| DeleteApi | `DELETE /v2/apis/{ApiId}` | 204 | N T |
| DeleteCorsConfiguration | `DELETE /v2/apis/{ApiId}/cors` | 204 | N T |
| CreateRoute / GetRoutes | `POST`, `GET /v2/apis/{ApiId}/routes` | 201 / 200 | B C N T / B N T |
| GetRoute / UpdateRoute / DeleteRoute | `GET`, `PATCH`, `DELETE /v2/apis/{ApiId}/routes/{RouteId}` | 200 / 200 / 204 | N T / B C N T / N T |
| CreateIntegration / GetIntegrations | `POST`, `GET /v2/apis/{ApiId}/integrations` | 201 / 200 | B C N T / B N T |
| GetIntegration / UpdateIntegration / DeleteIntegration | `.../integrations/{IntegrationId}` | 200 / 200 / 204 | as routes |
| CreateStage / GetStages | `POST`, `GET /v2/apis/{ApiId}/stages` | 201 / 200 | B C N T / B N T |
| GetStage / UpdateStage / DeleteStage | `.../stages/{StageName}` | 200 / 200 / 204 | as routes |
| DeleteAccessLogSettings | `DELETE .../stages/{StageName}/accesslogsettings` | 204 | N T |
| DeleteRouteSettings | `DELETE .../stages/{StageName}/routesettings/{RouteKey}` | 204 | N T |
| CreateDeployment / GetDeployments | `POST`, `GET /v2/apis/{ApiId}/deployments` | 201 / 200 | B C N T / B N T |
| GetDeployment / UpdateDeployment / DeleteDeployment | `.../deployments/{DeploymentId}` | 200 / 200 / 204 | as routes |
| CreateAuthorizer / GetAuthorizers | `POST`, `GET /v2/apis/{ApiId}/authorizers` | 201 / 200 | B C N T / B N T |
| GetAuthorizer / UpdateAuthorizer / DeleteAuthorizer | `.../authorizers/{AuthorizerId}` | 200 / 200 / 204 | as routes |
| ResetAuthorizersCache | `DELETE .../stages/{StageName}/cache/authorizers` | 204 | N T |
| TagResource / GetTags / UntagResource | `POST`, `GET`, `DELETE /v2/tags/{ResourceArn}` (`tagKeys` query list on DELETE) | 201 / 200 / 204 | B C N T |
| ImportApi / ReimportApi / ExportApi | `PUT /v2/apis`, `PUT /v2/apis/{ApiId}`, `GET .../exports/{Specification}` | 201 / 201 / 200 | B C N T / B C N T / B N T |
| DeleteRouteRequestParameter | `DELETE .../routes/{RouteId}/requestparameters/{RequestParameterKey}` | 204 | N T |

Source: [S] each operation's `smithy.api#http` and `errors`. The rest (domain names, API mappings, VPC links, models, route and integration responses, portals, routing rules) is outside HTTP API scope. `{ResourceArn}` is a plain label, so clients percent-encode the ARN's `/` as `%2F`; ARCHITECTURE.md 4.1 already requires dispatch on the escaped path. Only `ListRoutingRules` carries `@paginated` ([S]); the `Get*` lists page through `maxResults` (a `__string` query member) and `nextToken` without the trait.

### 2.3 Errors

| Shape | `httpError` | Members (wire names) |
|---|---|---|
| `com.amazonaws.apigatewayv2#BadRequestException` | 400 | `message` |
| `com.amazonaws.apigatewayv2#ConflictException` | 409 | `message` |
| `com.amazonaws.apigatewayv2#NotFoundException` | 404 | `message`, `resourceType` |
| `com.amazonaws.apigatewayv2#TooManyRequestsException` | 429 | `message`, `limitType` |
| `com.amazonaws.apigatewayv2#AccessDeniedException` | 403 | `message`; only on portal and domain-name operations |

Source: [S]. An IAM denial of another operation sends `AccessDeniedException` 403 with key `message`, though the model does not list it for those operations (APIGW2-15, recorded 2026-11-03).

Identity of a constraint failure: the API Reference describes `BadRequestException`, the only 400 model it lists, as "One of the parameters in the request is invalid" ([D apis-apiid](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid.html), the ReimportApi and UpdateApi responses). No v2 recording shows a model constraint failing, and REST APIs on the same signing name and host answer model enum and pattern failures with an unmodeled `ValidationException` (section 12 item 11). APIGW2-01 (recorded 2026-11-03) settles it: a range failure is `BadRequestException` 400 `Timeout should be between 50 ms and 30000 ms` (case 17), not REST's `ValidationException`; an unknown enum value (`HTTPS_PROXY`, case 17b) differs from both readings: AWS drops it and answers `BadRequestException` `IntegrationType is a required parameter for creating integration.`

The API Reference names the 429 response model `LimitExceededException` with the same members ([D apis-apiid](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid.html)); which name AWS sends in `x-amzn-ErrorType` is UNVERIFIED: APIGW2-02 (recorded 2026-11-03) sent three CreateDeployment calls within 2 s and all succeeded, so no 429 was seen and the documented "1 request every 5 seconds" was not enforced at that rate. Clients resolve the type from `x-amzn-ErrorType`, body `__type` or `code`, taking the text before the first `:` (`wire-rest.md 4.2`), so a coral-suffixed header such as REST's `...:http://internal.amazon.com/coral/com.amazonaws.backplane.controlplane/` parses either way. v2 sends the bare name with no suffix (APIGW2-01, recorded 2026-11-03). Bodies carry `message` and never `resourceType`; their formatting depends on the operation: GetApi, GetStage and CreateStage pretty-print (`{\n  "message" : "..."\n}`), DeleteApi, routes and integrations send compact JSON, and GetTags sends `{"message": "..."}` with a space.

Messages:

- REST APIs answer a missing API with `NotFoundException` `Invalid API identifier specified 111111111111:<upper-id>`, the id echoed as sent (the test upper-cases an existing id) ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L136, `test_get_api_case_insensitive`, third-party-recorded, validated 2024-04-15 per test_apigateway_api.validation.json:L185-L186). v2 sends the same text for GetApi and DeleteApi (APIGW2-01, recorded 2026-11-03); the other missing-resource texts are listed below.
- Terraform retries a `ConflictException` whose message contains `try again later`, and any error containing `Unable to complete operation due to concurrent modification. Please try again later.` ([TF] `service_package.go:L24-L37`). The provider's comment says the text was observed; when AWS sends it is not documented, and understudy emits it only by fault injection.
- `BadRequestException: Warnings found during import` appears in an import test ([TF] `api_test.go:L536`); import is out of scope.
- Recorded texts (APIGW2-01, 2026-11-03): missing route `Invalid route identifier specified`, integration `Invalid Integration identifier specified`, stage `Invalid stage identifier specified`, deployment `Invalid Deployment identifier specified`, authorizer `Invalid Authorizer identifier specified` (all 404); duplicate stage 409 `Stage already exists`; duplicate route key 409 `Route with key GET /any already exists for this API`; a bad method or a path without `/` 400 `The provided route key is not formatted properly for HTTP protocol. Format should be "[HTTP METHOD] /[RESOURCE PATH]" or "$default"`; greedy not last `Greedy variables may only be in last position of route key`; a part mixing literal and variable `One or more path parts appear to be invalid. Parts are validated against this regular expression: ^(\{[\w.:-]+\+?\}|[a-zA-Z0-9.:_-]+)$`; an empty part (trailing slash) `Part of the given route key path is empty`; a target naming a missing integration `Unable to find route target of type integrations with ID <id>`; bad stage name `Stage name only allows a-zA-Z0-9_`; timeout `Timeout should be between 50 ms and 30000 ms`; `Unsupported PayloadFormatVersion: 3.0`; no version `PayloadFormatVersion is a required parameter for integration`; missing subtype parameters `Operation: SQS-SendMessage requires enabling passthrough, or defining all of the following parameters in the parameter mapping: [QueueUrl, MessageBody]`; `Operation: SNS-Publish is not supported.`; a WebSocket integration type `Currently, an API with a protocol type of HTTP may only be associated with proxy integrations (AWS_PROXY, HTTP_PROXY)`. A stage variable value with a space was accepted (differs: the documented value pattern is not enforced).

### 2.4 Constraints: what the model carries and what it does not

- Model `@range`: `TimeoutInMillis` 50..30000, `Cors.MaxAge` -1..86400, `AuthorizerResultTtlInSeconds` 0..3600 ([S]). The member docs agree: "between 50 and 30,000 milliseconds for HTTP APIs. The default timeout is ... 30 seconds for HTTP APIs" ([S] `CreateIntegrationRequest$TimeoutInMillis`).
- Model `@required`: CreateApi `Name`, `ProtocolType`; CreateRoute `RouteKey`; CreateIntegration `IntegrationType`; CreateStage `StageName`; CreateAuthorizer `AuthorizerType`, `IdentitySource`, `Name`; path labels ([S]).
- Enums ([S]): `ProtocolType` `WEBSOCKET|HTTP`; `IntegrationType` `AWS|HTTP|MOCK|HTTP_PROXY|AWS_PROXY` (HTTP APIs use `AWS_PROXY` and `HTTP_PROXY`; `AWS`, `HTTP`, `MOCK` are "Supported only for WebSocket APIs", [S] member doc); `ConnectionType` `INTERNET|VPC_LINK`; `AuthorizationType` `NONE|AWS_IAM|CUSTOM|JWT`; `AuthorizerType` `REQUEST|JWT`; `IpAddressType` `ipv4|dualstack`; `DeploymentStatus` `PENDING|FAILED|DEPLOYED`; `LoggingLevel` `ERROR|INFO|OFF`; `PassthroughBehavior` and `ContentHandlingStrategy` (WebSocket only).
- From the docs, not the model: stage names "can contain only alphanumeric characters, hyphens, and underscores, or be `$default`. Maximum length is 128 characters"; stage variable "names can have alphanumeric and underscore characters, and the values must match `[A-Za-z0-9-._~:/?#&=,]+`"; an access log format "must include at least $context.requestId" ([D stage reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-stages-stagename.html)); `routeSelectionExpression` "must be `${request.method} ${request.path}`" for HTTP APIs ([D apis-apiid](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid.html)); `payloadFormatVersion` is "Required for HTTP APIs", `1.0` or `2.0` for Lambda, `1.0` for everything else ([D](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-integrations.html)); route keys are `$default` or "a combination of an HTTP method and resource path, for example, `GET /pets`" ([D routes reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-routes.html)); a greedy variable "must be at the end of the resource path" ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)).
- From Terraform's validators (client side, not AWS): name 1..128, description 0..1024, version 1..64, operation name 1..64, route target 1..128, subtype 1..128, integration methods `ANY DELETE GET HEAD OPTIONS PATCH POST PUT` ([TF] `api.go:L142-L146`, `L119-L123`, `L170-L174`, `route.go:L82-L86`, `L117-L121`, `integration.go:L102-L107`, `validate.go:L11-L22`).

### 2.5 Identifiers, timestamps and ARNs

- API id: 10 lower-case alphanumerics. AWS's redacted samples use `r3pmxmplak`, `ktyvxmpls1`, `mnh1xmpli7` ([A event-v2.json:L29](https://github.com/awsdocs/aws-lambda-developer-guide/blob/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050/sample-apps/nodejs-apig/event-v2.json), [D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html)); aws-lambda-go's fixture uses `aaaaaaaaaa` ([C apigw-v2-request-no-authorizer.json:L18](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/events/testdata/apigw-v2-request-no-authorizer.json)). The CLI examples' `a1b2c3d4` is a placeholder.
- Route id: 7 characters (`72jz1wk`, `go65gqi`) ([A get-routes](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/get-routes.html)). Deployment id: 6 characters (`53lz9l`, `x1zwyv`, `dty748`, `shktxb`) ([A get-stages.rst:L19](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-stages.rst), [D create-deployment](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-deployment.html)). Integration id: the CLI shows both `0abcdef` and `a1b2c3`, both placeholders; AWS sent 7 characters (`b5bzc0o`), and an authorizer id of 6 (`hguhl4`); API ids are 10 (APIGW2-03, recorded 2026-11-03).
- Request id (`apigw-requestid` header, `requestContext.requestId`): 16 characters ending in `=`, with `-` in the alphabet: `JKJaXmPLvHcESHA=`, `LV7fzho-PHcEJPw=`, `Mc7UVioPPHcEKPA=` ([A event-v2.json:L39](https://github.com/awsdocs/aws-lambda-developer-guide/blob/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050/sample-apps/nodejs-apig/event-v2.json), [C fixture L40](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/events/testdata/apigw-v2-request-no-authorizer.json), [D JWT troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-jwt.html)). Read as base64 of 11 bytes in a URL-safe alphabet; that reading is inferred. The event's `requestId` equals the `apigw-requestid` response header (APIGW2-07, recorded 2026-11-03).
- `ApiEndpoint`: "The URI of the API, of the form {api-id}.execute-api.{region}.amazonaws.com" ([S] `Api$ApiEndpoint`); the CLI shows `https://a1b2c3d4.execute-api.us-west-2.amazonaws.com`, scheme and no trailing slash ([A get-api.rst:L11](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-api.rst)).
- Resource ARNs have an empty account field: `arn:<partition>:apigateway:<region>::/apis/<api-id>`, `.../apis/<api-id>/stages/<stage-name>`, `.../routes/<id>`, `.../integrations/<integration-id>`, `.../deployments/<id>`, `.../authorizers/<id>` ([D ARN reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html)); Terraform builds `arn` the same way with `RegionalARNNoAccount` ([TF] `api.go:L543-L545`, `stage.go:L617-L619`).
- Execute ARNs: `arn:<partition>:execute-api:<region>:<account-id>:<api-id>/<stage>/<http-method>/<resource-path>`; "The ARN for the `$default` route endpoint for HTTP APIs is `arn:...:<api-id>/*/$default`" ([D ARN reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html)).

## 3. Control plane resources

### 3.1 Api and quick create

- Create defaults seen in AWS output: `ApiKeySelectionExpression` `$request.header.x-api-key`, `RouteSelectionExpression` `$request.method $request.path`, `ApiEndpoint`, `CreatedDate`, `Name`, `ProtocolType` ([A create-api.rst:L13-L19](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/create-api.rst)); `ip_address_type` reads back `ipv4` and `disable_execute_api_endpoint` `false` when unset ([TF] `api_test.go:L87-L92`, acceptance test against AWS). Both are present in the JSON of CreateApi and GetApi (`ipAddressType: "ipv4"`, `disableExecuteApiEndpoint: false`); `description` and `corsConfiguration` are absent when unset (APIGW2-03, recorded 2026-11-03). A braced `routeSelectionExpression` (`${request.method} ${request.path}`) is stored and echoed as sent. GetApis pages with `nextToken` the URL-encoded base64 of `id=S<api-id>`.
- `GetApi` returns `Tags` ([A get-api.rst:L18](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-api.rst)).
- Quick create: with `Target` (a Lambda ARN gives `AWS_PROXY`, a URL gives `HTTP_PROXY`), an optional `RouteKey` (default `$default`) and `CredentialsArn`, CreateApi "produces an API with an integration, a default catch-all route, and a default stage which is configured to automatically deploy changes" ([S] `CreateApiRequest$Target`, `$RouteKey`). The quick-created route, integration and `$default` stage carry `ApiGatewayManaged: true` ([A get-routes, get-integrations](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/get-integrations.html), [A get-stages.rst:L13-L25](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-stages.rst)). "You can't modify the `$default` stage" ([D stage reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-stages-stagename.html)), "You can't modify the `$default` route key" ([D routes reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-routes.html)), "You can update a managed integration, but you can't delete it" ([D integrations reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-integrations.html)). A quick-created Lambda integration is `PayloadFormatVersion` `2.0` ([A get-integrations](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/get-integrations.html)); a quick-created HTTP one is `HTTP_PROXY` with the target URL as its URI ([TF] `api_test.go:L685`). APIGW2-03 (recorded 2026-11-03) confirms the quick-created shape (`$default` route, AWS_PROXY integration with the function ARN as given and payload 2.0, `$default` stage with `autoDeploy: true`, all `apiGatewayManaged: true`) but differs on the refusals: deleting the managed route answered 204, updating the managed stage's description answered 200, and deleting the managed integration answered 409 `Cannot delete Integration because it is referenced by the following Routes with Ids: [<route id>]`, naming the route just deleted. There is no managed-resource error.
- UpdateApi's `Target` "updates the integration URI and integration type. You can update a quick-created target, but you can't remove it" ([D apis-apiid](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid.html)).
- `DisableExecuteApiEndpoint` stops the default endpoint answering ([S] member doc); a request on a disabled endpoint answers 404 `{"message":"Not Found"}` with `Content-Type: application/json` (APIGW2-06, recorded 2026-11-03, 60 s after disabling).

### 3.2 Routes

- `RouteKey` is `$default` or `<METHOD> <path>`; `Target` is `integrations/<integration-id>` ([A get-routes](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/get-routes.html), [D CloudFormation example](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services.html)). A route may exist without a target (`create-route` output has none, [D create-route](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-route.html)); such a route takes no part in routing: a request falls to the next matching route (APIGW2-05, recorded 2026-11-03).
- Create echoes `ApiKeyRequired: false` and `AuthorizationType: "NONE"` when unset ([D create-route](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-route.html)). `ApiKeyRequired`, `ModelSelectionExpression`, `RequestModels`, `RequestParameters`, `RouteResponseSelectionExpression` are WebSocket-only ([D routes reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-routes.html)).
- `AuthorizationType` for HTTP APIs: `NONE`, `JWT`, `AWS_IAM`, `CUSTOM`; `AuthorizationScopes` apply to JWT ([D routes reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-routes.html)).
- Path variables are `{name}` segments; greedy `{name+}` only at the end ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)). `{param}` may not share a segment with literal text: `/a{b}` is refused, each part being a whole variable or literal of `[a-zA-Z0-9.:_-]`, and an empty part (a trailing slash) is refused too (APIGW2-01, APIGW2-05, recorded 2026-11-03).

### 3.3 Integrations

- HTTP API integrations are Lambda proxy, AWS service and HTTP proxy, plus private integrations over VPC links ([D integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations.html)).
- Lambda: `IntegrationType` `AWS_PROXY`, `IntegrationUri` a function ARN (CLI example) or the invoke URI `arn:aws:apigateway:<region>:lambda:path/2015-03-31/functions/<function-arn>/invocations`, which AWS stores verbatim ([TF] `integration_test.go:L383` pairs `integration_uri` with `invoke_arn`; `internal/service/lambda/function.go:L1698-L1700` builds it). AWS fills `IntegrationMethod: "POST"`, `ConnectionType: "INTERNET"`, `TimeoutInMillis: 30000` ([A create-integration](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-integration.html), [TF] `integration_test.go:L375-L390`).
- Service integration: `AWS_PROXY` with `IntegrationSubtype`, `CredentialsArn`, `RequestParameters`, `PayloadFormatVersion` `1.0`; AWS returns no `IntegrationMethod` and no `IntegrationUri` ([TF] `integration_test.go:L550-L568`).
- HTTP proxy: `HTTP_PROXY`, `IntegrationMethod` as sent (`GET`, `ANY`), `IntegrationUri` the URL, `PayloadFormatVersion` `1.0` ([TF] `integration_test.go:L83-L100`, `L161-L190`).
- For HTTP APIs AWS returns no `PassthroughBehavior` and no `IntegrationResponseSelectionExpression` even though Terraform always sends `PassthroughBehavior: WHEN_NO_MATCH` ([TF] `integration.go:L118-L130`, `L240-L242`; `integration_test.go:L89`, `L93`). APIGW2-03 (recorded 2026-11-03) confirms no `passthroughBehavior` comes back on create or get after one was sent; whether AWS stores it hidden cannot be observed and stays UNVERIFIED.
- `RequestParameters` values are returned exactly, including the two-character value `''` for a removal ([TF] `integration_test.go:L174-L175`). Removing a mapping on update is sending it with value `""`; removing a response mapping is sending its status key with `{}` ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html), [TF] `integration.go:L377-L421`).
- `IntegrationUri` and `CredentialsArn` may reference stage variables, `${stageVariables.<name>}`; "you must configure the permissions on the Lambda function manually" for such URIs ([D stage variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.stage-variables.html)).

### 3.4 Stages

- `$default` is "served from the base of your API's URL"; other stages are addressed as `/<stage>` ([D stages](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-stages.html), [D publish](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-publish.html)).
- Create returns `DefaultRouteSettings: {"DetailedMetricsEnabled": false}`, `RouteSettings: {}`, `StageVariables: {}`, `Tags: {}`, `CreatedDate`, `LastUpdatedDate`, no `DeploymentId` until there is one ([D create-stage](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-stage.html), [TF] `stage_test.go:L95-L101`). `AutoDeploy` appears only when true in the CLI examples ([A get-stages.rst:L14](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-stages.rst)); `false` is omitted, and the other create defaults above are as listed (APIGW2-03, recorded 2026-11-03).
- With `autoDeploy`, `DeploymentId` "Can't be updated" ([D stage reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-stages-stagename.html)); `LastDeploymentStatusMessage` reads `Successfully deployed stage with deployment ID 'dty748'` ([A get-stages.rst:L20](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-stages.rst)).
- `DataTraceEnabled`, `LoggingLevel` and `ClientCertificateId` are WebSocket-only ([D stage reference](https://docs.aws.amazon.com/apigatewayv2/latest/api-reference/apis-apiid-stages-stagename.html)); Terraform sends them only for WebSocket APIs ([TF] `stage.go:L537`, `L543`). Route settings are keyed by route key (`"GET /pets": {"ThrottlingBurstLimit": 100, "ThrottlingRateLimit": 2000.0}`, [A update-stage](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/update-stage.html)).
- Access logs: `AccessLogSettings{DestinationArn, Format}`, destination a CloudWatch Logs log group ARN `arn:aws:logs:<region>:<account-id>:log-group:<name>`; CLF, JSON, XML and CSV example formats; variables `$context.*` ([D logging](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging.html), [D logging variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html)). Stage variable removal on update is the variable with value `""` ([TF] `stage.go:L374-L384`).

### 3.5 Deployments and automatic deployment

- `CreateDeployment{Description, StageName}` returns `{AutoDeployed: false, CreatedDate, DeploymentId, DeploymentStatus: "DEPLOYED"}` ([D create-deployment](https://docs.aws.amazon.com/cli/latest/reference/apigatewayv2/create-deployment.html)). Automatic deployments read `AutoDeployed: true`, `Description: "Automatic deployment triggered by changes to the Api configuration"` ([A get-deployments.rst:L13-L17](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/get-deployments.rst)).
- APIGW2-04 (recorded 2026-11-03): an automatic deployment read Successfully after 2 s and served after 1 s; deploying an API with no routes is refused, differing from the earlier "succeeds" reading: 400 `Unable to deploy API because no valid routes exist in this API`; an explicit `DeploymentId` on an auto-deploy stage answers 400 `Deployment ID cannot be set on this stage <account>:<api-id> because AutoDeploy is enabled.`; deleting a deployment a stage uses answers 400 `Active stages pointing to this deployment must be moved or deleted`; a stage with no deployment answers 404 `{"message":"Not Found"}`. An automatic deployment's description is `Automatic deployment triggered by changes to the Api configuration`.

### 3.6 Tags

Tags ride on CreateApi and CreateStage and are read back from GetApi and GetStage; `TagResource`/`UntagResource`/`GetTags` take the resource ARN (section 2.5) in the path ([S], [TF] `tags_gen.go:L25-L37`, `L95-L112`). "Tags per stage: 50" ([D quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)); tag keys are 1 to 128 characters and values 0 to 256, refused with `BadRequestException` and the Coral text `1 validation error detected: Value '{k=v}' at 'addedTags' failed to satisfy constraint: Map keys must satisfy constraint: [...]` (APIGW2-01 cases 24 and 25, recorded 2026-11-03). GetTags on a missing API answers 404 `Resource was not found`.

### 3.7 CORS configuration

`Cors{AllowCredentials, AllowHeaders, AllowMethods, AllowOrigins, ExposeHeaders, MaxAge}`, HTTP APIs only ([S] `com.amazonaws.apigatewayv2#Cors`). Origins may be exact, `*`, `https://*` or `http://*` ([D CORS](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-cors.html)). `DeleteCorsConfiguration` removes it. Terraform compares every CORS list case-insensitively ([TF] `api.go:L83-L105`, `L535-L538`), because AWS normalizes case: `AllowOrigins`, `AllowHeaders` and `ExposeHeaders` are stored lower-cased and `AllowMethods` upper-cased (APIGW2-03, recorded 2026-11-03). `AllowOrigins: ["*"]` with `AllowCredentials: true` is refused with `BadRequestException` `allow-credentials is not supported if 'allow-origin' is *`; UpdateApi merges a new `CorsConfiguration` into the stored one before that check, so a MaxAge out of range was not recorded (APIGW2-13, recorded 2026-11-03).

### 3.8 Authorizers

JWT authorizers (issuer, audiences; JWKS fetched with a 1500 ms timeout and a 150000-byte response limit) and Lambda `REQUEST` authorizers (10000 ms timeout) ([S] `CreateAuthorizerRequest`, [D quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)). A JWT authorizer needs outbound HTTPS to the issuer's JWKS endpoint, which an offline emulator cannot make; a Lambda authorizer is implementable but is its own payload contract. Both are out of the first slice (section 13.2).

### 3.9 Quotas

Per API or stage ([D HTTP API quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)): routes 300, integrations 300, stages 10, authorizers 10, scopes per route 10, audiences per authorizer 50, tags per stage 50, stage variables per stage 100, stage variable key 64 characters, value 512 characters, access log template 3 KB, maximum integration timeout 30 seconds, payload 10 MB, request line plus header values 10240 bytes. Account: 10,000 requests per second with a 5,000 burst across API types ([D quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/limits.html)). Control plane: `CreateDeployment` "1 request every 5 seconds per account", `CreateVpcLink` 1 per 15 s, `DeleteVpcLink` 1 per 30 s, "Total operations: 10 requests per second with a burst quota of 40" (same page). The stage variable value limit of 512 conflicts with the model's shape name `StringWithLengthBetween0And2048` (section 12).

## 4. Data plane: from request to integration

### 4.1 Endpoint and stage in the URL

The default endpoint is `https://<api-id>.execute-api.<region>.amazonaws.com`; a named stage is `https://<api-id>.execute-api.<region>.amazonaws.com/<stage>` and `$default` is the root ([D publish](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-publish.html), [D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html): "For named stages, you must include the stage name at the beginning of the path"). The `execute-api.<region>.amazonaws.com` domain is on the Public Suffix List ([D publish](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-publish.html)). Clients connect over HTTPS; "API Gateway APIs can accept HTTP/2 requests, but API Gateway sends requests to backend integrations using HTTP/1.1" ([D logging variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html), `$context.protocol`).

### 4.2 Stage selection

"API Gateway first determines which stage to route the request to. If the request explicitly matches a stage, API Gateway sends the request to that stage. If no stage fully matches the request, API Gateway sends the request to the `$default` stage. If there's no `$default` stage, then the API returns `{"message":"Not Found"}` and does not generate CloudWatch logs" ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)). Read: a first path segment equal to a stage name selects that stage and is consumed for routing; a route under `$default` whose path starts with a stage's name is shadowed. Inferred from the text; the parameter mapping source `$request.path` is "The request path, without the stage name" ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)), which fits the consuming half. APIGW2-06 (recorded 2026-11-03) confirms the reading: `/prod/x` reaches stage `prod` route `GET /x` (with `/prod/x` as `rawPath`), `/prod/prod/x` reaches `GET /prod/x` on `prod`, and `GET /prod/x` on `$default` is shadowed. The stage segment is case-sensitive (`/PROD/x` falls to `$default`), and a bare `/<stage>` answers 404 `{"message":"Not Found"}`.

### 4.3 Route selection

"API Gateway selects the route with the most-specific match, using the following priorities: 1. Full match for a route and method. 2. Match for a route and method with a greedy path variable (`{proxy+}`). 3. The `$default` route. If no routes match a request, API Gateway returns `{"message":"Not Found"}`" ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)). The documented table, with routes `GET /pets/dog/1`, `GET /pets/dog/{id}`, `GET /pets/{proxy+}`, `ANY /{proxy+}`, `$default`:

| Request | Selected route |
|---|---|
| `GET /pets/dog/1` | `GET /pets/dog/1` (static beats variable) |
| `GET /pets/dog/2` | `GET /pets/dog/{id}` |
| `GET /pets/cat/1` | `GET /pets/{proxy+}` |
| `POST /test/5` | `ANY /{proxy+}` ("The `ANY` method matches all methods that you haven't defined for a route") |

Consequences: a full match with path variables beats any greedy match; a specific method beats `ANY` on the same path; greedy beats `$default`. APIGW2-05 (recorded 2026-11-03): a full match on `ANY` (`ANY /pets/bird/1`) beats a greedy match on the request's method (`GET /pets/{proxy+}`), so path specificity comes before method specificity; `/{proxy+}` matches `/` and `/pets/{proxy+}` matches `/pets/`. The order between two greedy routes of different depth and the same method stays UNVERIFIED (not tried). AWS's .NET tool orders by literal-segment count, then segments before the greedy variable, then fewer greedy variables ([E ApiGatewayRouteConfigService.cs](https://github.com/aws/aws-lambda-dotnet/blob/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2/src/Amazon.Lambda.TestTool/Services/ApiGatewayRouteConfigService.cs)); a hint, not AWS.

### 4.4 Methods, HEAD, trailing slashes, case and decoding

- Methods: `$context.httpMethod` takes `DELETE`, `GET`, `HEAD`, `OPTIONS`, `PATCH`, `POST`, `PUT` ([D logging variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html)); route keys add `ANY` ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)). The route-key method set is not enumerated; the answer to another method is APIGW2-01.
- APIGW2-05 (recorded 2026-11-03): literal segments match case-sensitively (`/case` and `/PETS/dog/1` missed `GET /Case` and `GET /pets/dog/1`), against AWS's .NET tool, which compares them with `OrdinalIgnoreCase` ([E ApiGatewayRouteConfigService.cs:L420](https://github.com/aws/aws-lambda-dotnet/blob/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2/src/Amazon.Lambda.TestTool/Services/ApiGatewayRouteConfigService.cs)). A trailing slash is significant: `/pets/dog/1/` does not match `GET /pets/dog/1`, and a route key with a trailing slash (`GET /slash/`) cannot be created ("Part of the given route key path is empty"). `//` collapses (`//pets/dog/1` matched `GET /pets/dog/1`, `rawPath` `/pets/dog/1`); the path is percent-decoded before matching, so `%2F` becomes a separator (`/enc/a%2Fb` did not match `GET /enc/{p}`) and `%31` matches a literal `1`. A route with no target takes no part in routing, differing from the earlier "404" reading: `GET /noint` was skipped for `ANY /{proxy+}`. Whether `HEAD` matches a `GET` route stays UNVERIFIED: a `HEAD` answer has no body to show which route served it.
- "API Gateway decodes URL-encoded request parameters before passing them to your backend integration" ([D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)): path parameters and query values reach the function decoded. Function URLs, which share the 2.0 format, keep percent-encoding in `rawPath` and decode `queryStringParameters` (`lambda-events.md 7.3`, third-party-recorded). HTTP APIs differ (APIGW2-05, APIGW2-07, recorded 2026-11-03): `rawPath` and `requestContext.http.path` are decoded (`/pets/dog/a b`), path parameters too, and query values are form-decoded (`f=a+b` gives `a b`, `b=%2Fx` gives `/x`) while `rawQueryString` keeps the bytes sent.

### 4.5 Responses API Gateway generates itself

| Case | Status and body | Source |
|---|---|---|
| No stage matches and no `$default` stage | `{"message":"Not Found"}` | [D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html); status 404 inferred |
| No route matches | `{"message":"Not Found"}` | same |
| Lambda integration failure (missing permission, malformed output) | `{"message":"Internal Server Error"}` | [D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html); status 500 inferred |
| JWT authorizer refuses | `401 {"message":"Unauthorized"}`, `www-authenticate: Bearer scope="" error="invalid_token" error_description="..."` | [D JWT troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-jwt.html) |
| Disabled default endpoint | 404 `{"message":"Not Found"}` (APIGW2-06, recorded 2026-11-03; differs from the 403 once thought plausible) | |
| Unsigned, mis-signed or unauthorized request on an `AWS_IAM` route | 403 `{"message":"Forbidden"}`, no `x-amzn-ErrorType`, the same body for every cause (APIGW2-12, recorded 2026-11-03) | |
| Unknown API id | no HTTP answer: the host `<unknown-id>.execute-api.<region>.amazonaws.com` does not resolve (APIGW2-06, recorded 2026-11-03) | |
| Function error, malformed output, a function the policy denies | 500 `{"message":"Internal Server Error"}` (APIGW2-09, APIGW2-10, APIGW2-11, recorded 2026-11-03) | |
| Integration timeout | 503 `{"message":"Service Unavailable"}` (APIGW2-10, recorded 2026-11-03) | |
| Body over 10,000,000 bytes, or an event over Lambda's 6 MB | 413 `{"message":"Request Entity Too Large"}` (APIGW2-10, recorded 2026-11-03); over 10 MiB the same body comes from a front layer with no `Content-Type` and no `apigw-requestid` | |
| Headers over 10 KB | 431 `{"message":"Request Header Fields Too Large"}`, no `Content-Type`, no `apigw-requestid` (APIGW2-10, recorded 2026-11-03) | |

The JWT example's headers are `Date`, `Content-Length: 26`, `Connection: keep-alive`, `www-authenticate`, `apigw-requestid`, and **no** `Content-Type`; the printed body `{"message":"Unauthorized"}}` has one brace too many for its `Content-Length` (26 is `{"message":"Unauthorized"}`) ([D JWT troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-jwt.html)). Gateway errors carry `Content-Type: application/json` and `apigw-requestid` (APIGW2-06, recorded 2026-11-03), except the 413 over 10 MiB and the 431 (APIGW2-10). Header names AWS sends: `apigw-requestid` (same page). Responses relayed from an integration: "API Gateway includes a Content-Type header for all integration responses. By default, the content type is application/json" is listed under REST APIs ([D important notes](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)); AWS's .NET tool uses `text/plain; charset=utf-8` for HTTP API responses without a `Content-Type` ([E ApiGatewayResponseExtensions.cs:L176-L177](https://github.com/aws/aws-lambda-dotnet/blob/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2/src/Amazon.Lambda.TestTool/Extensions/ApiGatewayResponseExtensions.cs)), and so does AWS for a structured response of either payload version (APIGW2-09, APIGW2-13, recorded 2026-11-03); a response inferred from a bare string or object is `application/json`.

### 4.6 CORS on the data plane

- "If you configure CORS for an API, API Gateway automatically sends a response to preflight OPTIONS requests, even if there isn't an OPTIONS route configured for your API. For a CORS request, API Gateway adds the configured CORS headers to the response from an integration." "If you configure CORS for an API, API Gateway ignores CORS headers returned from your backend integration." "To return CORS headers, your request must contain an `origin` header. For the `OPTIONS` method, your request must contain an `origin` header and an `Access-Control-Request-Method` header" ([D CORS](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-cors.html)).
- Header mapping: `allowOrigins` to `Access-Control-Allow-Origin`, `allowCredentials` to `Access-Control-Allow-Credentials`, `exposeHeaders` to `Access-Control-Expose-Headers`, `maxAge` to `Access-Control-Max-Age`, `allowMethods` to `Access-Control-Allow-Methods`, `allowHeaders` to `Access-Control-Allow-Headers` (same page).
- The same page also says the `$default` route "catches requests for all methods ... including `OPTIONS` requests" and recommends an unauthenticated `OPTIONS /{proxy+}` route when `$default` has an authorizer. That reads as routing before the automatic preflight answer, against the first quote. APIGW2-13 (recorded 2026-11-03) settles it for the second reading: an `OPTIONS` request that matches a route invokes the function (200, its body), and no gateway-generated preflight was seen. A matching preflight gets `access-control-allow-origin` (the request's origin), `-allow-methods`, `-allow-headers`, `-allow-credentials` and `-max-age` added to the function's response; a non-matching origin or method, or no `Access-Control-Request-Method`, gets no CORS headers. On other methods a matching origin gets `-allow-origin`, `-allow-credentials` and `-expose-headers`, replacing the function's own; any other origin gets none, and the function's own are dropped. A preflight needs no signature: `OPTIONS /iam` did not match `GET /iam` (AWS_IAM) and reached `ANY /{proxy+}`.

### 4.7 Limits on the data plane

Payload 10 MB, request line plus headers 10240 bytes, integration timeout 30 s ([D quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)); Lambda's synchronous payload limit of 6 MB applies to the event the function receives (`lambda-api.md 8.2`, ARCHITECTURE.md 14.7). 10 MB means 10,000,000 bytes: 10,000,001 is refused with 413 (APIGW2-10, recorded 2026-11-03; section 4.5 lists each limit's answer).

## 5. Lambda proxy integrations

### 5.1 Payload format 2.0 request event

The documented example is in [D lambda integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html); the best capture is AWS's sample from a real call on a named stage ([A event-v2.json](https://github.com/awsdocs/aws-lambda-developer-guide/blob/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050/sample-apps/nodejs-apig/event-v2.json)). Field by field:

| Field | Rule | Source |
|---|---|---|
| `version` | `"2.0"` | D |
| `routeKey` | the matched route key, or `$default`; no stage segment | A L3, L40 |
| `rawPath` | the request path including a named stage's segment (`/default/...`) | A L4 |
| `rawQueryString` | query without `?`, `""` when none | A L5, D |
| `cookies` | one element per cookie in AWS's sample (`s_fid=...`, `regStatus=pre-register`); `cookie` absent from `headers`; key omitted when there are no cookies. The sample does not show how many `Cookie` fields the browser sent, AWS splits every `Cookie` field at `; `: `Cookie: a=1; b=2` and `Cookie: c=3` gave `["a=1","b=2","c=3"]` (APIGW2-07, recorded 2026-11-03) | A L6-L9, C no-authorizer fixture. The doc's "combined with commas" wording disagrees (section 12) |
| `headers` | always present; names lower-cased; duplicates joined with `,`; includes `host`, `content-length` (`"0"` on a bodyless GET), `x-amzn-trace-id`, `x-forwarded-for`, `x-forwarded-port: 443`, `x-forwarded-proto: https` | A L10-L26, C fixture L6-L15, D ("All headernames are lowercased") |
| `queryStringParameters` | duplicates joined with `,`; omitted when there is no query | D, A |
| `pathParameters` | route variables, decoded; a greedy `{proxy+}` reads as key `proxy`; omitted when the route has none | decoding: D routes note; key `proxy`: [D HTTP proxy](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-http.html) maps route `/parent/{proxy+}` to URI `.../{proxy}`; C apigw-v2-request-iam.json:L18-L20 (synthetic); omission: A (no key, route without variables) |
| `requestContext.accountId` | the API owner's account | D logging variables (`$context.accountId`) |
| `requestContext.apiId`, `domainName`, `domainPrefix` | `domainName` "should be the same as the incoming Host header"; `domainPrefix` its first label | D logging variables, A L29-L31 |
| `requestContext.http` | `{method, path, protocol, sourceIp, userAgent}`; `path` equals `rawPath`; `sourceIp` is "the immediate TCP connection" | A L32-L38, D |
| `requestContext.requestId` | 16-character id (section 2.5) | A L39 |
| `requestContext.routeKey`, `stage` | as `routeKey`; stage name or `$default` | A L40-L41 |
| `requestContext.time`, `timeEpoch` | `10/Mar/2020:05:16:23 +0000`; integer milliseconds | A L42-L43 |
| `requestContext.authorizer` | omitted for `NONE`; `{iam: {accessKey, accountId, callerId, cognitoIdentity, principalOrgId, userArn, userId}}` for `AWS_IAM`; `{jwt: {claims, scopes}}`; `{lambda: {...}}` | A (absent), C apigw-v2-request-iam.json:L26-L40, C apigw.go:L150-L171 |
| `requestContext.authentication` | only with mutual TLS on a custom domain; omit | D |
| `body` | omitted when the request has none; base64 for binary content | A (absent), C fixtures |
| `isBase64Encoded` | `false` for text | D |
| `stageVariables` | the stage's variables; omitted when none | D, A |

aws-lambda-go marks `cookies`, `queryStringParameters`, `pathParameters`, `stageVariables`, `body` and `requestContext.authorizer` `omitempty` and `headers` not ([C apigw.go:L118-L147](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/events/apigw.go)), so omission is safe for Go clients. APIGW2-07 (recorded 2026-11-03): a bodyless request has no `body` key and `isBase64Encoded` false (aws-lambda-go's fixture, not AWS's sample); `text/plain` and `application/json` arrive as text, while `application/octet-stream`, `multipart/form-data`, `application/x-www-form-urlencoded` and a body with no `Content-Type` arrive base64; `content-length` is added (`"0"` when bodyless); duplicate headers join with `,` (`x-dup: a,b`) and duplicate query values too (`a: "1,2"`); `rawPath` is decoded (section 4.3); a client's own `X-Forwarded-For` is kept with the source IP appended (`203.0.113.3, 198.51.100.2`); `http.protocol` is `HTTP/1.1` for an HTTP/2 client; `requestId` equals `apigw-requestid`; `stageVariables` appear on a stage that has them.

### 5.2 Payload format 1.0 request event

The documented example ([D lambda integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)): `version: "1.0"`, `resource`, `path`, `httpMethod`, `headers`, `multiValueHeaders`, `queryStringParameters`, `multiValueQueryStringParameters`, `requestContext{accountId, apiId, authorizer{claims, scopes}, domainName, domainPrefix, extendedRequestId, httpMethod, identity{accessKey, accountId, caller, cognitoAuthenticationProvider, cognitoAuthenticationType, cognitoIdentityId, cognitoIdentityPoolId, principalOrgId, sourceIp, user, userAgent, userArn, clientCert}, path, protocol, requestId, requestTime, requestTimeEpoch, resourceId: null, resourcePath, stage}`, `pathParameters: null`, `stageVariables: null`, `body`, `isBase64Encoded`. "All headernames are lowercased" (same page). Unlike 2.0, absent maps are `null`, not omitted (same example). "Use format `1.0` and `path` to access the API mapping for your custom domain name" (same page): `path` is the full request path.

The example keeps the **last** duplicate header in `headers` (`header2: value2` against `["value1","value2"]`) and the **first** duplicate query value in `queryStringParameters` (`parameter1: value1` against `["value1","value2"]`) (same page). APIGW2-08 (recorded 2026-11-03) differs on the query: both single-value maps keep the **last** value (`X-Dup: b` against `["a","b"]`, `x: "2"` against `["1","2"]`). `resource` and `requestContext.resourcePath` are the route's path template (`/v1/{proxy+}`), `requestContext.resourceId` is the route key (`ANY /v1/{proxy+}`), and `path` includes a named stage's segment (`/prod/v1/a`). On a `NONE` route `requestContext.authorizer` is null and `identity` holds `sourceIp` and `userAgent` with every other member null, `cognitoAmr` after `caller`. A bodyless request has `body: null` and `isBase64Encoded` false. Header case, recorded (APIGW2-08, recorded 2026-11-03, all six steps): the doc's "All headernames are lowercased" does not hold for 1.0. `headers` and `multiValueHeaders` keep the client's spelling (`User-Agent`, `Host`, `Content-Type`, `Cookie`, `X-Dup` as curl sent them), name the headers API Gateway adds in canonical case (`Content-Length`, `X-Amzn-Trace-Id`, `X-Forwarded-For`, `X-Forwarded-Port`, `X-Forwarded-Proto`), and report curl's `Accept` as `accept`, the one name lowercased. `Content-Length` is added on a bodyless GET as `"0"`. That settles the user template the doc contradicted: aws-samples/serverless-patterns `apigw-http-api-lambda-terraform` (commit `deb84af`, a 1.0 integration by Terraform's default) reads `event["headers"]["X-Forwarded-For"]`, and its check now passes against understudy (`conformance/patterns/apigw-http-api-lambda-terraform`).

### 5.3 Interpreting a 2.0 response

- "API Gateway makes the following assumptions if your Lambda function returns valid JSON and doesn't return a `statusCode`: `isBase64Encoded` is `false`. `statusCode` is `200`. `content-type` is `application/json`. `body` is the function's response." Examples: `"Hello from Lambda!"` becomes body `Hello from Lambda!`; `{ "message": "Hello from Lambda!" }` becomes body `{ "message": "Hello from Lambda!" }` ([D lambda integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)). A function URL returning a bare JSON string answers the bare string: body `hello`, `Content-Length: 5`, no quotes (`testdata/parity/lambda/R13/02-resp-str.json`, AWS-recorded 2026-10-14; `internal/service/lambda/furl/doc.go`), which settles the reading `lambda-events.md 7.4` once took the other way.
- A customized response is `{cookies, isBase64Encoded, statusCode, headers, body}`; "each cookie becomes a `set-cookie` header" (same page).
- aws-lambda-go's response type serializes unset `headers`, `multiValueHeaders` and `cookies` as `null` ([C apigw.go:L190-L196](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/events/apigw.go)), so `null` in each of those must be accepted: every Go function that leaves them unset sends it.
- APIGW2-09 (recorded 2026-11-03): an object with `body` but no `statusCode` is itself the body (200 `application/json`, the object as the function serialized it); a non-string `body` with a `statusCode` answers 500 `{"message":"Internal Server Error"}`; a number header value is sent as text (`X-N: 1`); `multiValueHeaders` in a 2.0 response are ignored; a `statusCode` outside 100..599 answers 500, while a numeric string (`"201"`) is accepted; a structured response with no `content-type` is sent as `text/plain; charset=utf-8`; `cookies` and `set-cookie` in `headers` are all sent, one `set-cookie` header each; null `headers`, `cookies` and `isBase64Encoded` are accepted; a bare string is sent as `application/json` as is (`Hello`).

### 5.4 Interpreting a 1.0 response

"With the `1.0` format version, Lambda integrations must return a response in the following JSON format: `{isBase64Encoded, statusCode, headers, multiValueHeaders, body}`" ([D lambda integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)); there is no inference. APIGW2-09 (recorded 2026-11-03): a 1.0 response without `statusCode`, or a bare string, answers 500 `{"message":"Internal Server Error"}`; `multiValueHeaders` are sent one header per value (`X-M: a`, `X-M: b`); with no `Content-Type` the response is `text/plain; charset=utf-8`. How `headers` and `multiValueHeaders` merge when both name a header stays UNVERIFIED (not tried).

### 5.5 Invocation, credentials and the resource policy

- Invocation is synchronous; the function's response becomes the HTTP response ([D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html)).
- Permission: without `CredentialsArn`, API Gateway needs a resource-policy statement `lambda:InvokeFunction` for principal `apigateway.amazonaws.com`, usually with `ArnLike` `aws:SourceArn` on the execute ARN and `StringEquals` `aws:SourceAccount` ([D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html)). Documented source ARNs: `<api-id>/$default/$default` for the `$default` stage and `$default` route; `<api-id>/prod/*/test` for the `prod` stage and the `test` route ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)); `<api-id>/*/*/*` and `<api-id>/*/GET/*` patterns ([D services-apigateway](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway.html)). "If your function and API are in different AWS Regions, the Region identifier in the source ARN must match the Region of the function" (same page).
- With `CredentialsArn` set, API Gateway assumes that role to invoke the function; `arn:aws:iam::*:user/*` passes the caller's identity through ([S] `CreateIntegrationRequest$CredentialsArn`).
- The source ARN is `<api-id>/<stage>/<METHOD>/<route path>` with the route key's template and `ANY` as written: a permission on `$default/GET/perm/{id}`, `$default/ANY/permany`, `$default/GET/permg/{proxy+}` or `prod/GET/perm/{id}` let the request through, the concrete path or the request's method did not (APIGW2-11, recorded 2026-11-03). `${execution_arn}/*/*` matches all of them, because `ArnLike`'s `*` crosses `/`.

### 5.6 Failures: function errors, malformed output, timeouts

- HTTP APIs: the troubleshooting entry for `{"message":"Internal Server Error"}` names a missing invoke permission as the cause in its example and, as the other cause, function code that does not return "a response in the required format" ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)).
- The Lambda guide, for API Gateway in general: "If the Lambda API rejects the invocation request, API Gateway returns a 500 error code. If the Event Function runs but returns an error, or returns a response in the wrong format, API Gateway returns a 502. In both cases, the body of the response from API Gateway is `{"message": "Internal server error"}`" ([D services-apigateway-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway-errors.html)). That body is REST's spelling (section 12).
- AWS's .NET tool answers HTTP API function errors and malformed responses with `500 {"message":"Internal Server Error"}` and REST with `502 {"message":"Internal server error"}` ([E ApiGatewayResponseExtensions.cs:L77-L91](https://github.com/aws/aws-lambda-dotnet/blob/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2/src/Amazon.Lambda.TestTool/Extensions/ApiGatewayResponseExtensions.cs)).
- APIGW2-10 (recorded 2026-11-03): an unhandled function error answers 500 `{"message":"Internal Server Error"}` for both payload versions (not REST's 502), an integration timeout 503 `{"message":"Service Unavailable"}`, and an event over Lambda's 6 MB 413 `{"message":"Request Entity Too Large"}`. Malformed output answers the same 500 (APIGW2-09).

## 6. AWS service integrations (integration subtypes)

### 6.1 The subtypes, and the absence of SNS

All version `1.0` ([D subtype reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services-reference.html)):

| Subtype | Required parameters | Optional parameters |
|---|---|---|
| `EventBridge-PutEvents` | `Detail`, `DetailType`, `Source` | `Time`, `EventBusName`, `Resources`, `Region`, `TraceHeader` |
| `SQS-SendMessage` | `QueueUrl`, `MessageBody` | `DelaySeconds`, `MessageAttributes`, `MessageDeduplicationId`, `MessageGroupId`, `MessageSystemAttributes`, `Region` |
| `SQS-ReceiveMessage` | `QueueUrl` | `AttributeNames`, `MaxNumberOfMessages`, `MessageAttributeNames`, `ReceiveRequestAttemptId`, `VisibilityTimeout`, `WaitTimeSeconds`, `Region` |
| `SQS-DeleteMessage` | `ReceiptHandle`, `QueueUrl` | `Region` |
| `SQS-PurgeQueue` | `QueueUrl` | `Region` |
| `AppConfig-GetConfiguration` | `Application`, `Environment`, `Configuration`, `ClientId` | `ClientConfigurationVersion`, `Region` |
| `Kinesis-PutRecord` | `StreamName`, `Data`, `PartitionKey` | `SequenceNumberForOrdering`, `ExplicitHashKey`, `Region` |
| `StepFunctions-StartExecution` | `StateMachineArn` | `Name`, `Input`, `Region` |
| `StepFunctions-StartSyncExecution` | `StateMachineArn` | `Name`, `Input`, `Region`, `TraceHeader` |
| `StepFunctions-StopExecution` | `ExecutionArn` | `Cause`, `Error`, `Region` |

No SNS subtype exists. The AWS CDK's enum lists the same ten ([aws-cdk enums.ts](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/core/lib/analytics-data-source/enums.ts), via code search, `HttpIntegrationSubtype`). Publishing to SNS from an HTTP API takes a Lambda function, or EventBridge with an SNS target. `SNS-Publish` as a subtype answers `BadRequestException` `Operation: SNS-Publish is not supported.` (APIGW2-01, recorded 2026-11-03). Of the targets, understudy emulates SQS and EventBridge; Kinesis, Step Functions and AppConfig are not among its twelve services.

### 6.2 Request parameters and their values

"You must configure all required parameters to create an integration. You can use static values or map parameters that are dynamically evaluated at runtime" ([D service integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services.html)). Values:

| Source | Syntax | Notes |
|---|---|---|
| Header | `$request.header.<name>` | case-insensitive; multiple values joined with `,` |
| Query string | `$request.querystring.<name>` | case-sensitive; joined with `,` |
| Path parameter | `$request.path.<name>` | |
| Body | `$request.body` | the whole body |
| Body field | `$request.body.<json-path>` | no recursive descent, no filters; "API Gateway truncates the request body at 100 KB and then applies the selection expression" |
| Context variable | `$context.<name>` | section 3.4's variables |
| Stage variable | `$stageVariables.<name>` | |
| Static | any string | |

Same page. Several variables in one value use `${...}` braces ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)). Terraform re-sends unchanged request parameters on every update "as for AWS service integrations some parameters are always required" ([TF] `integration.go:L387-L389`), so UpdateIntegration with a partial map was expected to be validated against the merged map or refused for a missing parameter. It differs from both (APIGW2-01 case 22, recorded 2026-11-03): `--request-parameters '{"MessageBody":"$request.body"}'` on an SQS-SendMessage integration answered `BadRequestException` `Invalid integration URI specified`. A mapped header value for the non-string `DelaySeconds` was accepted (APIGW2-14, recorded 2026-11-03); `MessageAttributes` and `Resources` stay UNVERIFIED (not tried).

### 6.3 Credentials

"Before you create a first-class integration, you must create an IAM role that grants API Gateway permissions to invoke the AWS service action" and pass it as `CredentialsArn` ([D service integrations](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services.html)). The role's trust policy names `apigateway.amazonaws.com` ([TF] `integration_test.go`, `testAccIntegrationConfig_sqs`, L1070-L1083). APIGW2-14 (recorded 2026-11-03): a role that lacks the action relays SQS's own `403` `text/xml` `AccessDenied` (the action lower-cased, `sqs:sendmessage`); a role that does not exist answers the gateway's 500 `{"message":"Internal Server Error"}`. An untrusted role was not tried (UNVERIFIED).

### 6.4 What the client receives

No document states the status, headers or body a client gets from a service integration. APIGW2-14 (recorded 2026-11-03): the target's own response is relayed, status and body, and SQS answers in its Query (XML) protocol, not JSON: SendMessage gives 200 `text/xml` `<SendMessageResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/">...`, a missing queue 400 `text/xml` `<ErrorResponse>` with `AWS.SimpleQueueService.NonExistentQueue`, ReceiveMessage the XML-escaped body. EventBridge answers `application/x-amz-json-1.1` (`{"Entries":[...],"FailedEntryCount":0}`), a partial failure relayed as 200. A required mapped parameter that evaluates empty answers the gateway's 400 `{"message":"Bad Request"}`. No retry was seen (failures were no slower than successes). `responseParameters` with `overwrite:statuscode` can rewrite the status per returned code ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)).

## 7. HTTP_PROXY integrations

### 7.1 What AWS does

- "API Gateway passes the entire request and response between the frontend and the backend"; path variables can be referenced in the URI (`https://petstore-demo-endpoint.execute-api.com/petstore/{proxy}` for route `/parent/{proxy+}`); the `$default` route "sends the full request path to the integration" ([D HTTP proxy](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-http.html), [D routes](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-routes.html)).
- "HTTP APIs translate incoming `X-Forwarded-*` headers into a standard `Forwarded` header and will append the egress IP, Host, and protocol"; "API Gateway adds the Content-type header to your request if there is no payload and the Content-Length is 0" ([D important notes](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)).
- Parameter mapping keys `append|overwrite|remove:header.<name>`, `append|overwrite|remove:querystring.<name>`, `overwrite:path`; response keys `append|overwrite|remove:header.<name>`, `overwrite:statuscode`; reserved headers that cannot be mapped: `access-control-*`, `apigw-*`, `Authorization`, `Connection`, `Content-Encoding`, `Content-Length`, `Content-Location`, `Forwarded`, `Keep-Alive`, `Origin`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailers`, `Transfer-Encoding`, `Upgrade`, `x-amz-*`, `x-amzn-*`, `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Via` ([D parameter mapping](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)).
- Private integrations (`VPC_LINK`) target ALB or NLB listeners or Cloud Map services ([S] `IntegrationUri` doc).

### 7.2 What an emulator must refuse

Calling an arbitrary `IntegrationUri` is outbound network: ADR 0015 calls an emulator that "makes outbound HTTP requests to arbitrary hosts ... an SSRF vector" that "breaks the rule of no network access at runtime", and gates SNS HTTP delivery behind `sns.httpDelivery.allow`, empty by default. An `HTTP_PROXY` integration must be stored (Terraform applies it) and run only to hosts an operator allow-lists, loopback test servers included; any other request must be refused visibly, with a journal event, never answered as if the backend replied. `VPC_LINK` integrations name AWS networking understudy does not have and never run. Decision: section 13.3.

## 8. IAM

### 8.1 Control plane actions and resources

The control plane's IAM prefix is `apigateway` (the ARN namespace, [S]). "The API-managing `Action` expression has the format `apigateway:{action}`, where {action} is one of the following API Gateway actions: GET, POST, PUT, DELETE, PATCH (to update resources), or *"; "For information about HTTP verbs to use for specific API Gateway operations, see ... Amazon API Gateway Version 2 API Reference (WebSocket and HTTP APIs)"; "API Gateway resources have the following ARN format: `arn:aws:apigateway:{region}::{resource-path-specifier}`" ([D How Amazon API Gateway works with IAM](https://docs.aws.amazon.com/apigateway/latest/developerguide/security_iam_service-with-iam.html), Actions and Resources). So an operation's action is `apigateway:<the method of its HTTP binding>` (2.2). `third_party/aws-service-reference/actions.json` (bumped 2026-10-26 from the machine-readable service reference) agrees: each operation below lists `apigateway:<its method>`, and some list more (`iam:PassRole` on CreateApi, UpdateApi, CreateIntegration and UpdateIntegration; `apigateway:PUT` on CreateStage; `apigateway:PATCH` beside TagResource's and UntagResource's methods, entries shared with REST APIs). `codegen/services/apigatewayv2.json` takes the method as the primary action (`iamPrimary`). `execute-api` has no entry, and the raw data-plane operation `InvokeApi` declares no action: AWS evaluates no IAM on a `NONE` route (8.2, 8.3), and an `AWS_IAM` route would be authorized as `execute-api:Invoke` on its execute ARN when the request arrives. The Service Authorization Reference pages for both prefixes, which map actions to resource types, render only in a browser.

Resources. The resource of a call is the ARN whose path is the operation's URI without `/v2`. AWS's v2 policy examples allow CreateApi on `/apis` and UpdateApi on `/apis/??????????` ("requires that resources have a maximum of 10 characters, which excludes sub-resources of an API"), CreateRoute and UpdateRoute on `/apis/*/routes` and `/apis/*/routes/*`, the authorizer calls on `/apis/*/authorizers` and `/apis/*/authorizers/*`, and every read of an API's sub-resources on `/apis/<api-id>/*` ([D identity-based policy examples](https://docs.aws.amazon.com/apigateway/latest/developerguide/security_iam_id-based-policy-examples.html): "Require that API routes have authorization", "Create only REQUEST or JWT authorizers", "Simple read permissions"). The ARN reference lists a resource for every path below except `/tags/...`, which it lists for REST APIs only ([D ARN reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html), "HTTP API and WebSocket API resources" and "REST API resources"). Form: collection for creates and lists, item for one resource, tags for the tag operations. Grounds: "example" where a policy example above names the call, "path" where the ARN reference row whose path is the URI is the only source.

| Operations | Action | Form | Resource (after `arn:aws:apigateway:<region>::`) | ARN reference row | Grounds |
|---|---|---|---|---|---|
| CreateApi / GetApis | POST / GET | collection | `/apis` | Apis | example / path |
| UpdateApi / GetApi, DeleteApi | PATCH / GET, DELETE | item | `/apis/<api-id>` | Api | example / path |
| DeleteCorsConfiguration | DELETE | item | `/apis/<api-id>/cors` | Cors | path |
| CreateRoute / GetRoutes | POST / GET | collection | `/apis/<api-id>/routes` | Routes | example / path |
| UpdateRoute / GetRoute, DeleteRoute | PATCH / GET, DELETE | item | `/apis/<api-id>/routes/<route-id>` | Route | example / path |
| CreateIntegration, GetIntegrations | POST, GET | collection | `/apis/<api-id>/integrations` | Integrations | path |
| GetIntegration, UpdateIntegration, DeleteIntegration | GET, PATCH, DELETE | item | `/apis/<api-id>/integrations/<integration-id>` | Integration | path |
| CreateStage, GetStages | POST, GET | collection | `/apis/<api-id>/stages` | Stages | path |
| GetStage, UpdateStage, DeleteStage | GET, PATCH, DELETE | item | `/apis/<api-id>/stages/<stage-name>` | Stage | path |
| DeleteAccessLogSettings | DELETE | item | `/apis/<api-id>/stages/<stage-name>/accesslogsettings` | AccessLogSettings | path |
| DeleteRouteSettings | DELETE | item | `/apis/<api-id>/stages/<stage-name>/routesettings/<route-key>` | RouteSettings | path; the route key is written as sent, its space and `/` unencoded (`.../routesettings/GET /any`; APIGW2-15, recorded 2026-11-03) |
| CreateDeployment, GetDeployments | POST, GET | collection | `/apis/<api-id>/deployments` | Deployments | path |
| GetDeployment, UpdateDeployment, DeleteDeployment | GET, PATCH, DELETE | item | `/apis/<api-id>/deployments/<deployment-id>` | Deployment | path |
| TagResource, GetTags, UntagResource | PATCH, GET, PATCH | item (the tagged resource) | `/apis/<api-id>` | Api (a stage's tags: Stage, `/apis/<api-id>/stages/<stage-name>`, inferred) | differs from the `/tags/...` form (APIGW2-15, recorded 2026-11-03): the denial names the tagged API, and a policy allowing only `/tags/*` is still denied. Whether `/tags/...` is also required stays UNVERIFIED (the resource check fails first). For REST APIs "To get the tags for a resource, the user must have `GET` permissions for that resource. To update the tags for a resource, the user must have `PATCH` permissions for that resource" ([D tags and IAM](https://docs.aws.amazon.com/apigateway/latest/developerguide/apigateway-tagging-iam-policy.html), "Allow tagging operations"), which the shared `actions.json` entries fit |
| `InvokeApi` (raw) | none | | | | 8.2 |

### 8.2 execute-api:Invoke and the execute-api ARN

Actions: `execute-api:Invoke`, `execute-api:InvalidateCache`, `execute-api:*`; resource `arn:aws:execute-api:<region>:<account-id>:<api-id>/<stage-name>/<HTTP-VERB>/<resource-path-specifier>`; "If you specify a wildcard (`*`), the `Resource` expression applies the wildcard to the rest of the expression" ([D control access](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-control-access-using-iam-policies-to-invoke-api.html)). "Resource policies aren't currently supported for HTTP APIs" ([D IAM authorization](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-access-control-iam.html)).

### 8.3 AWS_IAM routes

"When IAM authorization is enabled, clients must use Signature Version 4 (SigV4) to sign their requests with AWS credentials. API Gateway invokes your API route only if the client has `execute-api` permission for the route" ([D IAM authorization](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-access-control-iam.html)). The signing name is `execute-api`, the prefix the policy names; "Signature Version 4A is not officially supported ... for HTTP and WebSocket APIs" ([D important notes](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)). The `Authorization` header "is dropped if it contains a Signature Version 4 signature or if `AWS_IAM` authorization is used" (same page, REST section); HTTP APIs differ (APIGW2-12, recorded 2026-11-03): the event keeps `authorization`, `x-amz-date` and `x-amz-security-token` on both an `AWS_IAM` and a `NONE` route. The caller's identity fills `requestContext.authorizer.iam` with `accessKey`, `accountId`, `callerId`, `cognitoIdentity: null`, `principalOrgId`, `userArn` and `userId` (section 5.1) and `$context.identity.*` ([D logging variables](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-logging-variables.html)); a signed request to a `NONE` route has no `iam`. An unsigned request, one signed for another service or region, a malformed `Authorization`, and an authenticated caller without `execute-api:Invoke` all answer 403 `{"message":"Forbidden"}`, `Content-Type: application/json`, no `x-amzn-ErrorType` (APIGW2-12).

### 8.4 lambda:InvokeFunction for Lambda integrations

Section 5.5. A missing statement answers `{"message":"Internal Server Error"}` ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)); the console adds the permission itself, the CLI, CloudFormation and SDKs do not (same page).

## 9. Terraform AWS provider v6.68.0

### 9.1 aws_apigatewayv2_api

- Create: `CreateApi` with `Name`, `ProtocolType`, `Tags`, and every set optional; `api_key_selection_expression` defaults to `$request.header.x-api-key` and `route_selection_expression` to `$request.method $request.path`, both sent on every create because they have defaults ([TF] `api.go:L53-L61`, `L158-L162`, `L185-L231`). Then `ReimportApi` if `body` is set ([TF] `api.go:L239`, `L385-L467`), then Read.
- Read: `GetApi`; sets `api_endpoint` from `ApiEndpoint`, `arn` and `execution_arn` built locally, `cors_configuration` (nil gives an empty list), tags from `GetApi.Tags` ([TF] `api.go:L246-L288`, `L528-L541`). A 404 `NotFoundException` on refresh removes the resource from state ([TF] `api.go:L252-L256`).
- Update: `DeleteCorsConfiguration` when the block is removed, `UpdateApi` with only changed fields, `ReimportApi` when `body` changes ([TF] `api.go:L290-L362`). Delete: `DeleteApi`, a `NotFoundException` is success ([TF] `api.go:L364-L383`).
- `expandCORS` always sends `AllowCredentials` and `MaxAge` (0 when unset) ([TF] `api.go:L497-L526`).
- Empty second plan needs: `ApiKeySelectionExpression` and `RouteSelectionExpression` echoed byte for byte; `IpAddressType` returned (Optional and Computed, [TF] `api.go:L136-L141`); `CorsConfiguration` absent when never set and after `DeleteCorsConfiguration`; tags returned by `GetApi`; `Description` and `Version` absent when unset.

### 9.2 aws_apigatewayv2_route

- Create: `CreateRoute` with `ApiKeyRequired` (default false), `AuthorizationType` (default `NONE`), `RouteKey`, and set optionals ([TF] `route.go:L58-L73`, `L127-L179`). Read: `GetRoute` ([TF] `route.go:L181-L202`, `L431-L447`). Update: `DeleteRouteRequestParameter` for removed parameters (WebSocket), then `UpdateRoute` ([TF] `route.go:L204-L303`). Delete: `DeleteRoute`, 404 is success ([TF] `route.go:L305-L325`). Import refuses a route with `ApiGatewayManaged` ([TF] `route.go:L348-L350`).
- Empty plan needs `ApiKeyRequired: false`, `AuthorizationType: "NONE"` and `Target` echoed.

### 9.3 aws_apigatewayv2_integration

- Defaults sent on create: `connection_type` `INTERNET`, `passthrough_behavior` `WHEN_NO_MATCH`, `payload_format_version` `1.0` ([TF] `integration.go:L65-L70`, `L118-L139`, `L199-L281`).
- Read: `GetIntegration` sets every field ([TF] `integration.go:L283-L323`). Diff suppression: `integration_method` `POST` read back against an empty config for `AWS` and `AWS_PROXY` ([TF] `integration.go:L85-L97`); `passthrough_behavior` empty read back against the default ([TF] `integration.go:L123-L129`). `timeout_milliseconds` is Optional and Computed ([TF] `integration.go:L175-L179`).
- Update always sends `IntegrationType` and any `IntegrationSubtype`, and the merged request parameters ([TF] `integration.go:L325-L442`).
- Empty plan needs: `ConnectionType: "INTERNET"`; `IntegrationMethod: "POST"` or absent for Lambda, absent for subtypes; `PassthroughBehavior` absent (or `WHEN_NO_MATCH`, which also compares equal); `PayloadFormatVersion` and `RequestParameters` echoed exactly; `TimeoutInMillis: 30000` when unset; `IntegrationUri` verbatim.

### 9.4 aws_apigatewayv2_stage

- Create: `GetApi` (for the protocol type), then `CreateStage` with `AutoDeploy` (default false), `StageName`, `Tags` and set optionals ([TF] `stage.go:L195-L252`).
- Read: `GetStage`, then `GetApi` again for `invoke_url` ([TF] `stage.go:L254-L301`). Update: `GetApi`, `DeleteRouteSettings` for removed route settings, `UpdateStage` ([TF] `stage.go:L303-L394`). Import refuses a managed stage ([TF] `stage.go:L435-L437`).
- `default_route_settings` suppresses a block AWS returns but the config lacks ([TF] `stage.go:L87-L93`); AWS returns it (`default_route_settings.# = 1` with all zero values, [TF] `stage_test.go:L95-L100`). `deployment_id` is Optional and Computed and reads `""` on a stage with no deployment ([TF] `stage.go:L122-L126`, `stage_test.go:L101`).
- Empty plan needs: `AutoDeploy` echoed; `DefaultRouteSettings` present; `DeploymentId` stable across refreshes except through automatic deployment; `StageVariables` and `RouteSettings` maps echoed.

### 9.5 aws_apigatewayv2_deployment

Create: `CreateDeployment` with `Description` only (never `StageName`), then polls `GetDeployment` for up to 5 minutes, pending `PENDING`, target `DEPLOYED` ([TF] `deployment.go:L70-L96`, `L226-L246`). A missing or other status fails the apply. Read: `GetDeployment` sets `auto_deployed` and `description` ([TF] `deployment.go:L98-L118`). Update: `UpdateDeployment`, then the same wait. `triggers` is local and forces replacement ([TF] `deployment.go:L59-L64`). The usual pattern pairs it with `aws_apigatewayv2_stage.deployment_id`.

### 9.6 aws_lambda_permission with execution_arn

`source_arn` is any valid ARN, sent as `SourceArn` and read back from the policy's `AWS:SourceArn` condition ([TF] `internal/service/lambda/permission.go:L109-L114`, `L180-L182`, `L264`). The common value `"${aws_apigatewayv2_api.x.execution_arn}/*/*"` is `arn:aws:execute-api:<region>:<account>:<api-id>/*/*`. understudy's Lambda already stores and returns these statements (`internal/service/lambda/doc.go`).

### 9.7 Values the provider builds itself

| Attribute | Built from | Host |
|---|---|---|
| `aws_apigatewayv2_api.api_endpoint` | the server's `ApiEndpoint` ([TF] `api.go:L270`) | whatever the server says |
| `aws_apigatewayv2_api.arn`, `aws_apigatewayv2_stage.arn` | `RegionalARNNoAccount("apigateway", "/apis/<id>[/stages/<name>]")` ([TF] `api.go:L543-L545`, `stage.go:L617-L619`) | none |
| `aws_apigatewayv2_api.execution_arn` | `RegionalARN("execute-api", "<id>")` with the provider's region and account ([TF] `api.go:L547-L549`, `internal/conns/awsclient.go:L224-L226`) | none |
| `aws_apigatewayv2_stage.execution_arn` | `RegionalARN("execute-api", "<id>/<stage>")` ([TF] `stage.go:L621-L623`) | none |
| `aws_apigatewayv2_stage.invoke_url` | `https://<id>.execute-api.<region>.<dns-suffix>/<stage>`, or `.../` for `$default` ([TF] `internal/conns/awsclient.go:L313-L323`) | the partition's DNS suffix, `amazonaws.com` by default ([TF] `internal/conns/awsclient.go:L362-L369`), never the configured endpoint |
| `aws_lambda_function.invoke_arn` | `arn:<partition>:apigateway:<region>:lambda:path/2015-03-31/functions/<arn>/invocations` ([TF] `internal/service/lambda/function.go:L1698-L1700`) | none |

The account id is known whenever credentials are validated: aws-sdk-go-base returns the account from STS `GetCallerIdentity` unless `skip_credentials_validation` is set, whatever `skip_requesting_account_id` says ([C aws_config.go:L299-L338](https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/aws_config.go), [TF] `internal/conns/config.go:L145-L148`, `L186-L189`). With `skip_credentials_validation` as well, `execution_arn` reads `arn:aws:execute-api:<region>::<id>` and no permission built from it matches. understudy's guide (`docs/guide/terraform.md`) and `conformance/terraform/provider.tf` set both flags, and must at an IP-literal endpoint. Correction, 2026-10-27: this paragraph first said the guide sets only `skip_requesting_account_id`, so `execution_arn` would carry the account. Run locally against understudy built on 2026-10-27 with provider 6.68.0, `conformance/terraform/run.sh s3` passed with `provider.tf` as it is; with `skip_credentials_validation` removed, the account came from `GetCallerIdentity` and every `aws_s3_bucket` failed with `lookup 100000000003.127.0.0.1: no such host` (S3 Control `ListTagsForResource`). So the account is present without the flag, the flag stays, and a Terraform configuration builds the permission's source ARN from `data.aws_caller_identity` instead: `arn:${partition}:execute-api:${region}:${account_id}:${api_id}/*/*`. `conformance/terraform/apigatewayv2.tf` does so and asserts the account-less `execution_arn` (13.6).

### 9.8 Calls per phase

| Resource | Apply (create) | Refresh (plan) |
|---|---|---|
| api | CreateApi, [ReimportApi, GetApi, UpdateApi], GetApi | GetApi |
| route | CreateRoute, GetRoute | GetRoute |
| integration | CreateIntegration, GetIntegration | GetIntegration |
| stage | GetApi, CreateStage, GetStage, GetApi | GetStage, GetApi |
| deployment | CreateDeployment, GetDeployment until DEPLOYED, GetDeployment | GetDeployment |
| tags change (api, stage) | TagResource/UntagResource, GetTags ([TF] `internal/provider/sdkv2/tags_interceptor.go:L170-L182`) | none: tags come from GetApi/GetStage ([TF] same file L88-L98) |

## 10. SDKs and the CLI

- Every SDK sends restJson1 with camelCase `jsonName` members and reads errors by the rules in `wire-rest.md 4.2`. The CLI's `--cors-configuration`, `--request-parameters` and `--stage-variables` take JSON or shorthand ([D CORS CLI example](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-cors.html), [D parameter mapping examples](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-parameter-mapping.html)).
- Data-plane clients are plain HTTP clients (curl, fetch, `requests`), plus SigV4 signers with service `execute-api` for `AWS_IAM` routes. No SDK resolves an `execute-api` endpoint from `AWS_ENDPOINT_URL`; clients take the URL from `ApiEndpoint`, a Terraform output, or by building it.

## 11. Addressing for an emulator

### 11.1 What reaches the emulator

A client reaches an HTTP API by the URL it holds: `ApiEndpoint` from GetApi or Terraform's `api_endpoint`, plus the stage segment. If understudy mints `ApiEndpoint` on a host it serves, every client that uses the returned value reaches it with no DNS server and no change to the client. Terraform's `invoke_url` cannot be steered (section 9.7). The data plane has no SigV4 scope to route on except on `AWS_IAM` routes, so it must be claimed by host or path before auth is parsed, as function URLs are (ARCHITECTURE.md 4.1 rules 1 and 2).

### 11.2 LocalStack's forms (context only)

LocalStack serves `<api-id>.execute-api.localhost.localstack.cloud:4566/<stage>/<path>`, a path form `/_aws/execute-api/<api-id>/<stage>/<path>`, and the legacy `/restapis/<api-id>/<stage>/_user_request_/<path>` (localstack@8b9a79f `localstack-core/localstack/services/apigateway/helpers.py`, `localstack_path_based_url` and `host_based_url`, and `.../next_gen/execute_api/router.py`, read through GitHub code search of the archived repository). Its path parser strips `/_aws/execute-api/<api-id>` case-insensitively and accepts `//` and `%2F` in the remainder (`.../next_gen/execute_api/handlers/parse.py`). Context for migrations, not authority.

### 11.3 What ADR 0013 implies

- No DNS server; virtual-host detection is configuration on base domains (default `localhost`; `localhost.localstack.cloud` may be added for migrations). Function URLs already follow it: `http://<url-id>.lambda-url.<region>.localhost:<port>/` minted on the port the client addressed, with the path fallback `/_understudy/lambda-url/<url-id>/...` (ADR 0013, `internal/service/lambda/urls_runtime.go`, `internal/edge/claim.go`).
- The same shape fits: host `<api-id>.execute-api.<region>.<base>` and path fallback `/_understudy/execute-api/<api-id>/<stage-or-path>`. `*.localhost` resolution varies by resolver and does not work in containers (ADR 0013 Context and Consequences), so functions calling an API from inside Lambda containers use the fallback on the container-reachable endpoint (ADR 0011).
- `amazonaws.com` names are not base domains (ADR 0013 amendment, 2026-10-13): understudy never answers `<id>.execute-api.<region>.amazonaws.com`, which is also why `invoke_url` cannot work.
- `/_aws/` is a safe extra claim: S3 bucket names cannot start with `_`, the reason ARCHITECTURE.md 4.1 rule 1 gives for `/_understudy/`.

## 12. Conflicts between sources

1. **Gateway error body spelling and status.** HTTP API docs: `{"message":"Internal Server Error"}` ([D troubleshooting](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-troubleshooting-lambda.html)). Lambda guide: 502 or 500 with `{"message": "Internal server error"}` ([D](https://docs.aws.amazon.com/lambda/latest/dg/services-apigateway-errors.html)), which is REST's body ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_lambda.snapshot.json:L1520, a REST API, third-party-recorded, validated 2024-07-10 per test_apigateway_lambda.validation.json:L20-L21). The HTTP API page is right: 500 `{"message":"Internal Server Error"}` (APIGW2-10, recorded 2026-11-03).
2. **429 shape name.** Model `TooManyRequestsException`; API Reference `LimitExceededException` (section 2.3). Still open: APIGW2-02 drew no 429 (2.3).
3. **`isBase64Encoded` on a bodyless request.** `true` in AWS's sample ([A event-v2.json:L45](https://github.com/awsdocs/aws-lambda-developer-guide/blob/0d45f4afd8eb5b7dc0484b1731dfb23d06afa050/sample-apps/nodejs-apig/event-v2.json)), `false` in aws-lambda-go's fixture ([C L46](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/events/testdata/apigw-v2-request-no-authorizer.json)). AWS sends `false` (APIGW2-07, recorded 2026-11-03).
4. **Cookies.** The doc says cookie headers "are combined with commas and added to the `cookies` field"; the doc's example and AWS's sample give one element per cookie. Neither shows the request's `Cookie` fields: the sample's browser may have used HTTP/2, which lets a client send each cookie as its own field (RFC 9113 8.2.3), and `http.protocol` reads `HTTP/1.1` either way (section 4.1). AWS splits each `Cookie` field on `; `: `Cookie: a=1; b=2` plus `Cookie: c=3` gave three elements (APIGW2-07, recorded 2026-11-03).
5. **`routeSelectionExpression`.** The API Reference says it "must be `${request.method} ${request.path}`"; AWS returns `$request.method $request.path` ([A create-api.rst:L19](https://github.com/aws/aws-cli/blob/e9993cfc8bf6d85d9870c1ff3b86cd59c5bfdba6/awscli/examples/apigatewayv2/create-api.rst)) and Terraform sends that by default. Accept both on input; echo what was sent. AWS stores and echoes the braced form as sent (APIGW2-03, recorded 2026-11-03).
6. **1.0 single-value maps.** The doc's example keeps the last header value and the first query value (section 5.2). AWS keeps the last of both (APIGW2-08, recorded 2026-11-03).
7. **Stage names.** "Stage names can only contain alphanumeric characters, hyphens, and underscores. Maximum length is 128" sits under REST and WebSocket notes ([D important notes](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)); the v2 reference states the same rule and adds `$default` (section 2.4). No conflict for HTTP APIs once `$default` is allowed.
8. **Stage variable value length.** Quota 512 characters ([D quotas](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-quotas.html)); the model's value shape is named `StringWithLengthBetween0And2048` with no trait ([S] `StageVariablesMap`). Enforce 512 (inferred text); APIGW2-01 found the documented value pattern not enforced (a space was accepted).
9. **Path case.** AWS's .NET tool matches literal segments case-insensitively ([E](https://github.com/aws/aws-lambda-dotnet/blob/f43a9f0d75435a513c3094875a7571d9543372fe/Tools/LambdaTestTool-v2/src/Amazon.Lambda.TestTool/Services/ApiGatewayRouteConfigService.cs)); AWS matches them case-sensitively (APIGW2-05, recorded 2026-11-03).
10. **Default response `Content-Type`.** REST notes: `application/json`; AWS's .NET tool for HTTP APIs: `text/plain; charset=utf-8` (section 4.5), which AWS sends for a structured response (APIGW2-09, recorded 2026-11-03).
11. **Validation identity.** The v2 model and API Reference name `BadRequestException` 400 the only 400 shape, "One of the parameters in the request is invalid" (section 2.3). REST APIs, on the same signing name and host, answer a model enum failure and a pattern failure with an unmodeled `ValidationException` and the Coral text, and keep `BadRequestException` for their handlers' own checks ([R] localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_api.snapshot.json:L3089, `IntegrationType` `HTTPS_PROXY` against the five-value enum v2 also has, recorded 2024-04-15; L4067, a `statusCode` pattern, recorded 2025-06-18; L169 `BadRequestException` for an empty description; `apigateway-rest.md` finding 13). v2 keeps `BadRequestException` 400 and REST keeps `ValidationException`: APIGW2-01 case 17 (recorded 2026-11-03) answered a model range failure with `BadRequestException` `Timeout should be between 50 ms and 30000 ms`, and case 17b an unknown enum value with `IntegrationType is a required parameter for creating integration.`, so `codegen/overlays/apigatewayv2.json` labels the identity aws-recorded and lets the handler answer `IntegrationType`.

## 13. Implications for understudy

### 13.1 Service shape and claiming

1. Vendor `models/apigatewayv2/service/2018-11-29/apigatewayv2-2018-11-29.json` at the pinned commit and generate the restJson1 types and route table (done 2026-10-26, `internal/api/apigatewayv2api`). Overlay only HTTP-API-relevant operations as bound.
2. The signing name `apigateway` is shared with REST APIs (finding 3). ADR 0019 (accepted 2026-10-26) decides it: two registry services, `apigateway` (v1) and `apigatewayv2`, on the one signing name, accepted because their route tables are disjoint (every v2 URI starts with `/v2/`, no v1 URI does); `BySigningName` returns both in registration order. The single merged service this item first proposed was rejected there: the models share operation names (`CreateDeployment`, `TagResource`), and an operation's identity is `service.Name` in bindings, coverage, the journal and IAM.
3. Data-plane claim, a new rule beside ARCHITECTURE.md 4.1 rule 2: host `<api-id>.execute-api.<region>.<base>` (pattern `^[a-z0-9]+\.execute-api\.[a-z0-9-]+\.`) goes to a raw operation, `apigatewayv2.InvokeApi`, so it is journaled, faultable and covered as function URLs are. It declares no IAM action: AWS evaluates none on a `NONE` route, and an `AWS_IAM` route would authorize `execute-api:Invoke` on its execute ARN when the request arrives (8.1 to 8.3). Path fallback `/_understudy/execute-api/<api-id>/...` on the main listener, region from the API's home, `domainName` reported in the host form under `localhost` (the function URL fallback's rule, `internal/service/lambda/furl/doc.go`). Optional migration alias `/_aws/execute-api/<api-id>/...` (section 11.3).
4. `ApiEndpoint` is `http://<api-id>.execute-api.<region>.localhost:<port>` on the port the client addressed, no trailing slash, mirroring `functionURL` (`internal/service/lambda/urls_runtime.go`); `https://` only on the TLS listener. It is a deviation from AWS's `https://...amazonaws.com` and goes in the coverage note.
5. Packages, one primary type each: `apigatewayv2` (control plane, state, snapshots), `apigatewayv2/route` (route-key parser to a typed key `Default | Keyed{Method, Segments}` with `Segment = Literal | Var | Greedy`, matcher with the precedence of 4.3, rapid properties: a request matches at most one route; adding a route never changes the match for requests it does not match; a full match always beats a greedy one), `apigatewayv2/selexpr` (the `$request.*`, `$context.*`, `$stageVariables.*` grammar of 6.2), and a 1.0/2.0 event and response package shared with function URLs: `furl` already builds 2.0 events and infers responses; extract the format into one package and keep the front ends' differences (function URL: no path parameters or stage, 502 plain-text `Internal Server Error` for function errors, `lambda-events.md 7.4`; HTTP API: route key, stage, path parameters, JSON gateway errors).
6. Deployments are snapshots (finding 7): a stage serves the configuration captured by its deployment; `autoDeploy` captures a new one, with `AutoDeployed: true` and the documented description, before the mutating call returns (a coverage-note item; AWS took about 2 s, APIGW2-04, recorded 2026-11-03; 13.4). `CreateDeployment` answers `DEPLOYED` at once, which satisfies Terraform's waiter (section 9.5).

### 13.2 Operation coverage

The marks and notes below are the target. The first slice (roadmap item 6, HTTP APIs) binds the roadmap's minimum: every bound control-plane operation is at least Partial, its note starting with a base note. The first slice's base note named error texts and ids as inferred; APIGW2-01 and APIGW2-03 (recorded 2026-11-03) recorded both, so it now reads `error texts are AWS's where APIGW2-01 recorded them and understudy's elsewhere; AWS pretty-prints some error bodies (GetApi, GetStage, CreateStage), which understudy sends compact`. A row that goes beyond the minimum reads "first slice: ...; target: ...", the first part being what the first slice binds and marks. A valid member understudy does not implement answers NotImplemented (501) naming the member and saying nothing was changed, never `BadRequestException`, which would claim that AWS refuses it (the repository's convention, `internal/service/lambda/errors.go` `checkUnimplementedConfig`).

| Operations | Mark | Coverage note text |
|---|---|---|
| CreateApi, UpdateApi | Partial | first slice: `HTTP APIs only: protocolType WEBSOCKET and quick create (Target, RouteKey, CredentialsArn) answer NotImplemented; corsConfiguration is stored and returned, and a data-plane request with an Origin header to an API that has one answers 501 (CORS is not applied); ipAddressType is stored without effect; ApiEndpoint names the emulator (http://<api-id>.execute-api.<region>.localhost:<port>), not amazonaws.com`; target: `HTTP APIs only: protocolType WEBSOCKET answers NotImplemented naming WebSocket APIs as not emulated; ipAddressType is stored without effect; ApiEndpoint names the emulator (http://<api-id>.execute-api.<region>.localhost:<port>), not amazonaws.com` |
| GetApi, GetApis, DeleteApi, DeleteCorsConfiguration | Full | first slice: Partial (base note only); target: Full |
| CreateRoute, UpdateRoute | Partial | first slice: `authorizationType JWT and CUSTOM, authorizerId and authorizationScopes answer NotImplemented (authorizers are not emulated); a route with AWS_IAM is stored and a request it matches answers 501; WebSocket-only members are stored and echoed without effect`; target: `a route whose authorizationType is JWT or CUSTOM is stored, and a request it matches answers 501 instead of running its integration; AWS_IAM checks the SigV4 signature under sigv4 verify and does not evaluate execute-api:Invoke policies` |
| GetRoute, GetRoutes, DeleteRoute | Full | first slice: Partial (base note only); target: Full |
| CreateIntegration, UpdateIntegration | Partial | first slice: `Lambda AWS_PROXY with payload 1.0 and 2.0 runs; integration subtypes, HTTP_PROXY and VPC_LINK connections are stored and a request that reaches them answers 501; a Lambda integration with credentialsArn, requestParameters, responseParameters or a stage variable in its URI is stored and a request that reaches it answers 501; tlsConfig is stored without effect; requestTemplates, templateSelectionExpression and contentHandlingStrategy answer NotImplemented (WebSocket API members)`; target: `Lambda AWS_PROXY (payload 1.0 and 2.0) and the SQS-SendMessage, SQS-ReceiveMessage, SQS-DeleteMessage, SQS-PurgeQueue and EventBridge-PutEvents subtypes run; Kinesis-PutRecord, StepFunctions-* and AppConfig-GetConfiguration are stored and answer 501 when a request reaches them; HTTP_PROXY runs only to allow-listed hosts and VPC_LINK never; responseParameters, tlsConfig and HTTP_PROXY requestParameters are stored without effect` |
| GetIntegration, GetIntegrations, DeleteIntegration | Full | first slice: Partial (base note only); target: Full |
| CreateStage, UpdateStage | Partial | `accessLogSettings, defaultRouteSettings and routeSettings are stored without effect (no access log records, no throttling); an automatic deployment completes before the call returns, where AWS took about 2 s (APIGW2-04); clientCertificateId answers NotImplemented (WebSocket APIs only)` |
| GetStage, GetStages, DeleteStage | Full | first slice: Partial (base note only); target: Full |
| CreateDeployment, DeleteDeployment | Partial | base note only: APIGW2-04 (recorded 2026-11-03) refused a deployment of an API with no routes and a delete of a deployment a stage uses, with the texts understudy sends |
| GetDeployment, GetDeployments, UpdateDeployment | Full | first slice: Partial (base note only); target: Full |
| TagResource, UntagResource, GetTags | Partial | first slice: base note only (the clause below stays a code comment in `declareAccess` until APIGW2-15 is recorded or the spec adds it); APIGW2-15 (recorded 2026-11-03) settled the target clause: understudy authorizes them as AWS does, GET or PATCH on the tagged resource (8.1) |
| CreateAuthorizer, UpdateAuthorizer | StoredOnly | first slice: unbound (501); target: `stored and returned; authorizers never run, and routes that use them answer 501` |
| GetAuthorizer, GetAuthorizers, DeleteAuthorizer | Full | first slice: unbound (501) |
| DeleteAccessLogSettings, DeleteRouteSettings | StoredOnly | first slice: `the settings they remove are stored without effect` for both (the clause below stays a code comment beside the DeleteRouteSettings resource until APIGW2-15 is recorded or the spec adds it); the route key's spelling in DeleteRouteSettings' IAM resource is as sent (APIGW2-15, recorded 2026-11-03; 8.1) |
| `InvokeApi` (raw data plane) | Partial | first slice: `Lambda AWS_PROXY with payload 1.0 and 2.0; stage and route selection as documented; where the documents leave the order of two matching routes open it is inferred (APIGW2-05): a full match on ANY before a greedy match on the method, the greedy route with more segments before its variable first, then the leftmost literal segment; AWS_IAM routes, CORS requests and integrations other than Lambda AWS_PROXY answer 501; parameter mapping, access logging and throttling are not applied` (APIGW2-05, -07, -08 and -09, recorded 2026-11-03, settled the route order, the event members and the response defaults the first slice listed as inferred); target: `payload 1.0 and 2.0 Lambda proxy and the SQS and EventBridge subtypes; stage selection, route selection and CORS as documented; parameter mapping, access logging and throttling are not applied; JWT and CUSTOM routes answer 501` |
| ImportApi, ReimportApi, ExportApi, ResetAuthorizersCache, DeleteRouteRequestParameter, models, route and integration responses, domain names, API mappings, VPC links, portals, routing rules | unbound (501) | Terraform's `body` argument calls ReimportApi ([TF] `api.go:L239`), so an `aws_apigatewayv2_api` with `body` fails to apply; say so in `docs/guide/` |

### 13.3 Data plane

Items 3 to 5 and the `CredentialsArn` parts of item 1 state the target. In the first slice (13.2) an integration subtype, an `HTTP_PROXY` or `VPC_LINK` integration and a Lambda integration with `CredentialsArn` are stored and a request that reaches them answers 501 naming the setting, journaled; authorizers are unbound, so `JWT` and `CUSTOM` routes are refused at CreateRoute and UpdateRoute with NotImplemented, and an `AWS_IAM` route answers 501.

1. Lambda proxy: invoke `RequestResponse` through `kernel.Invoke` as the service principal `apigateway.amazonaws.com` (or as the `CredentialsArn` role), with the source ARN of 5.5. Evaluate the function's resource policy with `policydoc` first, as `furl` does for function URLs, and answer `500 {"message":"Internal Server Error"}` when it does not allow the call, under every `iam.enforce` mode (finding 9, switch `apigatewayv2.lambda.permission`); with `CredentialsArn`, require the role to exist and skip the resource policy. Journal the data-plane request and the invoke as parent and child.
2. Docker unavailable: Lambda's Invoke answers 501 (ARCHITECTURE.md 14.7). Pass that through as a 501 naming Docker, not as AWS's 500: the failure is understudy's, and an AWS-shaped 500 would be a fake integration failure.
3. Service integrations: map parameters (6.2), assume `CredentialsArn` (the role must exist and trust `apigateway.amazonaws.com`), call the target operation through `kernel.Invoke` as that role so the access recorder attributes the action to it, and relay the result in the shape APIGW2-14 records; until it does, a request that reaches a subtype answers 501 (13.4, out of scope). Not outbox deliveries (finding 2).
4. `HTTP_PROXY`: a new allow-list on the ADR 0015 pattern (say `apigatewayv2.httpProxy.allow`, empty by default), needing an ADR 0015 amendment; a request to any other host answers 501 with a message naming the host and the setting, journaled.
5. Authorizers: a route with `JWT` or `CUSTOM` answers 501 naming the authorizer type, before any integration runs. Never run the integration unauthorized.
6. The integration timeout bounds real work in a container, like the function timeout, so it belongs in the wall-clock column of ARCHITECTURE.md 9 (that table puts "function timeout enforcement" there); add the row.
7. Body handling: a 10,000,000-byte request cap before anything else (APIGW2-10); lower-case header names; cookies split per `;`-separated pair (APIGW2-07, 12 item 4); base64 for non-text content types with the function URL's text rule (`internal/service/lambda/furl/doc.go`), which APIGW2-07 matched for octet-stream, multipart, urlencoded and a body with no content type.
8. Gateway errors carry `apigw-requestid` (a 16-character id from the seeded id source, 2.5) and `Content-Type: application/json`, as APIGW2-06 recorded.

### 13.4 Fidelity switches

Five switches, one per recording case or group of related cases, each struct-valued; every other unknown is a code comment plus a Partial coverage note naming its case (13.2). Each is a `fidelity.Switch` (`internal/fidelity/switch.go`), listed by the service's `Switches()`; `Ref` points at the first section of its grounds, `Record` at the cases. The last column names the switches an earlier draft of this section listed one per unknown.

| Name | Value | Label | Grounds | Record | Replaces |
|---|---|---|---|---|---|
| `apigatewayv2.lambda.failure-answers` | a function error or malformed output answers `500` `{"message":"Internal Server Error"}`; an integration timeout `503` `{"message":"Service Unavailable"}`; a request body over 10,000,000 bytes `413` `{"message":"Request Entity Too Large"}`, and an event over Lambda's 6 MB (6,291,456 bytes) synchronous limit likewise | aws-recorded (APIGW2-10, 2026-11-03; it differs from the first draft's 10,485,760-byte limit) | 5.6, 4.7, 12 item 1 | APIGW2-10 | `lambda.error-status`, `integration.timeout-response`, `request.too-large` |
| `apigatewayv2.lambda.permission` | the source ARN is `<api-id>/<stage>/<METHOD>/<route path without leading slash>` with the route key's template and `ANY` as written, `<api-id>/<stage>/$default` for the `$default` route (the last form documented); a denial answers `500` `{"message":"Internal Server Error"}` and the function does not run | aws-recorded (APIGW2-11, 2026-11-03) | 5.5, 8.4 | APIGW2-11 | `lambda.missing-permission`, `lambda.source-arn` |
| `apigatewayv2.route.matching` | literal segments match case-sensitively; `/a` and `/a/` are distinct; `HEAD` matches only `HEAD` and `ANY` routes; a route with no target takes no part in routing | inferred: APIGW2-05 (2026-11-03) recorded all but `HEAD`, whose answer cannot show the route; it differs from the first draft, which answered a targetless route `404` | 4.4, 3.2, 12 item 9 | APIGW2-05 | `route.path-case`, `route.trailing-slash`, `route.head`, `route.no-target` |
| `apigatewayv2.front.answers` | a first path segment equal to a stage name selects that stage; an unknown API id, or a host naming another region, answers `403` `{"message":"Forbidden"}`; a disabled default endpoint, no stage, a stage without a deployment, and no route answer `404` `{"message":"Not Found"}`; the gateway's own answers carry `apigw-requestid` and `Content-Type: application/json`. The three deployment unknowns of 3.5, now recorded texts: deleting a deployment a stage uses, CreateDeployment on an API with no routes, and an explicit `DeploymentId` on an `autoDeploy` stage are refused with `BadRequestException` | inferred: APIGW2-04 and APIGW2-06 (2026-11-03) recorded everything but the unknown API id, whose host does not resolve; they differ from the first draft on the disabled endpoint (403) and the routeless deployment (accepted) | 4.5, 3.1, 3.4, 3.5, 4.2 | APIGW2-06, APIGW2-04 | `stage.prefix-shadows-default`, `front.unknown-api`, `front.disabled-endpoint`; new: the 3.5 deployment unknowns |
| `apigatewayv2.payload.unrecorded` | 2.0: `isBase64Encoded` is `false` on a bodyless request, `requestContext.requestId` equals the `apigw-requestid` header, a structured response without a `content-type` gets `text/plain; charset=utf-8` (AWS's .NET emulator's answer), and `multiValueHeaders` in a 2.0 response is ignored; 1.0: the last value of a repeated header and of a repeated query parameter | aws-recorded (APIGW2-07, -08, -09, 2026-11-03; the query value differs from the documented example's first) | 5.1, 5.2, 5.3, 4.5, 12 items 3, 6 and 10 | APIGW2-07, APIGW2-08, APIGW2-09 | `event.empty-body-base64`, `event.v1-single-value`, `response.default-content-type`, `response.v2-multivalue-headers` |

Coverage-note items, no switch: `deploy.auto-latency` (an automatic deployment completes before the call returns, where AWS took about 2 s; the CreateStage and UpdateStage note, APIGW2-04, 3.5), `ids.shape` (api 10, route 7, integration 7, deployment 6, authorizer 6 lower-case alphanumerics; recorded, APIGW2-03, 2.5) and `error.not-found-text` (`Invalid API identifier specified <account>:<api-id>`, recorded for v2 too, APIGW2-01, 2.3). Tests assert an inferred id or text only by presence, uniqueness and a non-empty message, never as a literal.

Out of scope while the feature answers 501 (13.2): `iam.unsigned` (8.3, APIGW2-12), `cors.preflight` (4.6, APIGW2-13) and `service-integration.response` (6.4, APIGW2-14). Each becomes a switch, or a recorded value, with the follow-up that builds its feature.

### 13.5 Limits and validation

Hand-code, with `rapid` properties per limit (`sqs.md` 13 item 1 is the precedent): name 1..128; description 0..1024; version 1..64; route keys (method set of 4.4 plus `ANY`, path starting with `/`, `{var}` whole segments, one greedy at the end, `$default`); stage name `[A-Za-z0-9_-]{1,128}` or `$default`; stage variable names `[A-Za-z0-9_]+` up to 64, values `[A-Za-z0-9-._~:/?#&=,]+` up to 512, at most 100; `TimeoutInMillis` 50..30000; `MaxAge` -1..86400; payload versions `1.0` and `2.0` (2.0 only for Lambda); required subtype parameters (6.1); access log format containing `$context.requestId`, at most 3 KB; quotas of 3.9 (300 routes, 300 integrations, 10 stages, 10 authorizers, 50 tags per stage). Every constraint failure is `BadRequestException` (400), duplicates `ConflictException` (409), missing resources `NotFoundException` (404), whose body has no `resourceType` (APIGW2-01, recorded 2026-11-03). APIGW2-01 recorded the constraint-failure identity (`BadRequestException`, 2.3, 12 item 11) and the texts of section 2.4; a constraint it did not try keeps a text that names the constraint, listed in the coverage report as inferred. A stage variable value is not checked against the documented pattern (a space was accepted). Never emit `TooManyRequestsException` except by fault injection: Terraform and the SDKs retry it, and the `CreateDeployment` quota of one per 5 s would slow every apply.

### 13.6 Terraform: apply and an empty second plan

The conformance suite applies an API with routes, a Lambda integration in each payload format, an SQS integration, a `$default` auto-deploy stage, a named stage with an explicit deployment and `triggers`, and an `aws_lambda_permission` whose source ARN is built from `data.aws_caller_identity` (9.7: `execution_arn` has no account under the suite's provider block); then plans again and expects no changes. The fields that must round-trip are in 9.1 to 9.5. Fixed points: `GetDeployment` returns `DEPLOYED`; `GetStage` returns `DefaultRouteSettings`; `GetApi` omits `CorsConfiguration` after `DeleteCorsConfiguration`; `IntegrationMethod` is `POST` for Lambda without one; `TimeoutInMillis` defaults to 30000; request parameters come back byte for byte. The suite takes URLs from `api_endpoint`, never `invoke_url` (9.7), asserts that `execution_arn` carries no account under the guide's provider block (9.7, corrected 2026-10-27), and `docs/guide/` says so with commands run against a build.

### 13.7 Never faked

- No answer on `*.amazonaws.com` names and no `ApiEndpoint` minted on them.
- No outbound request for `HTTP_PROXY` outside the allow-list, and none ever for `VPC_LINK`.
- No integration run for a route whose authorizer understudy does not run.
- No Lambda invocation the resource policy (or the credentials role) would not allow, by default.
- No pretended success for Kinesis, Step Functions or AppConfig subtypes; no invented SNS subtype.
- No undeployed change served on a stage without `autoDeploy`.
- No WebSocket API accepted as if it worked.
- No AWS-shaped 500 for a failure that is understudy's own (Docker missing, allow-list refusal): those answer 501 naming the cause.

### 13.8 Roadmap corrections

`docs/ROADMAP.md` item 6 says "add SQS, SNS and EventBridge direct integrations (new `delivery.Target` variants)". SNS has no subtype (6.1), and integrations are synchronous calls, not deliveries (finding 2). The item should read "SQS and EventBridge direct integrations as synchronous calls through `kernel.Invoke`". ARCHITECTURE.md 21 lists API Gateway in the row whose seam is "new `delivery.Target` variants" and needs the same correction. Lambda proxy integrations are "mostly routing, stages and `$default` handling" as the item says, plus the 1.0 format Terraform defaults to (finding 6) and deployment snapshots (finding 7).

## 14. Recording queue

Every case below was recorded on 2026-11-03 (us-east-2, `testdata/parity/apigatewayv2/APIGW2-NN/`, with `notes.md` beside most); the sections each case settles carry its results. The fixtures are evidence only: the parity replayer does not route `execute-api` requests. One recipe line changed since: 14.1 no longer creates `GET /slash/`, which AWS refuses (APIGW2-05). APIGW2-02 drew no 429 at its rate, so its shape stays open. For a human with credentials, in the recording-kit shell (`docs/recording-kit.md` sections 1 and 2: `$P`, `$R`, `$A`, `$Q`, `rec`, `recurl`, the redaction step). Agents never run these. Run the cases in order after 14.1; each builds on the shared API. Run 14.1 through the 14.17 teardown in a kit shell of its own: 14.1 sets `TRUST` and `FN`, 14.5 `SH` and 14.7 `G`, which other queues set to other values (recording-kit.md 2). Costs: HTTP API requests bill USD 1.00 per million in the first tier, metered in 512 KB increments, with one million free a month in the first 12 months ([API Gateway pricing](https://aws.amazon.com/api-gateway/pricing/)); the queue makes a few hundred requests, the large uploads of APIGW2-10 counting as about 20 each. Lambda, SQS, EventBridge and CloudWatch Logs stay in the free tier at these volumes. The queue costs under USD 0.01 in total and takes about 2 h 25 min. On a free-plan project, a call the guardrail denies is noted `n/a (free-plan SCP)` (kit section 1). Add `aws apigatewayv2 get-apis --query "Items[?starts_with(Name, '$P')].Name"` to the kit's section 5 sweep when this queue lands.

### 14.1 Shared setup

Ten minutes. Creates a function that echoes its event or returns a shape chosen by the `mode` query parameter (and a second copy, `$P-gw-perm`, with no permission for APIGW2-11), an API `$P-gw` with routes for every case, a `$default` auto-deploy stage, the queue `$P-gw-q`, the API Gateway roles `$P-gw-int` and `$P-gw-none`, and the helper `gw` for unsigned data-plane requests.

```sh
mkdir -p "$REC/_work/APIGW2"
cat >"$REC/_work/APIGW2/gw.py" <<'EOF'
import base64, json, time

SHAPES = {
    "string": "Hello",
    "object": {"message": "hi"},
    "body-no-status": {"body": "x", "headers": {"x-a": "b"}},
    "object-body": {"statusCode": 200, "body": {"a": 1}},
    "number-header": {"statusCode": 200, "headers": {"x-n": 1}, "body": "x"},
    "nulls": {"statusCode": 201, "headers": None, "multiValueHeaders": None, "cookies": None, "body": "x"},
    "mvh": {"statusCode": 200, "multiValueHeaders": {"x-m": ["a", "b"]}, "body": "x"},
    "no-ctype": {"statusCode": 200, "body": "plain"},
    "b64": {"statusCode": 200, "isBase64Encoded": True,
            "headers": {"content-type": "application/octet-stream"},
            "body": base64.b64encode(b"\x00\x01").decode()},
    "cookies": {"statusCode": 200, "cookies": ["a=1; Path=/", "b=2"], "headers": {"set-cookie": "c=3"}, "body": "x"},
    "status-999": {"statusCode": 999, "body": "x"},
    "status-string": {"statusCode": "201", "body": "x"},
    "cors": {"statusCode": 200, "headers": {"access-control-allow-origin": "https://fn.example"}, "body": "x"},
}

def handler(event, context):
    mode = (event.get("queryStringParameters") or {}).get("mode", "echo")
    if mode == "throw":
        raise Exception("boom")
    if mode == "sleep":
        time.sleep(3)
    if mode in SHAPES:
        return SHAPES[mode]
    return {"statusCode": 200, "headers": {"content-type": "application/json"}, "body": json.dumps(event)}
EOF
(cd "$REC/_work/APIGW2" && zip -q gw.zip gw.py)
aws iam create-role --role-name "$P-gw-fn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
aws iam attach-role-policy --role-name "$P-gw-fn" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
sleep 15                                   # the new role must propagate before Lambda accepts it
for f in "$P-gw" "$P-gw-perm"; do
  aws lambda create-function --function-name "$f" --runtime python3.13 --handler gw.handler --timeout 10 \
    --role "arn:aws:iam::$A:role/$P-gw-fn" --zip-file "fileb://$REC/_work/APIGW2/gw.zip" >/dev/null
  aws lambda wait function-active-v2 --function-name "$f"
done
FN=arn:aws:lambda:$R:$A:function:$P-gw
aws lambda add-permission --function-name "$P-gw" --statement-id gw --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:$R:$A:*" >/dev/null
API=$(aws apigatewayv2 create-api --name "$P-gw" --protocol-type HTTP --query ApiId --output text)
GW=https://$API.execute-api.$R.amazonaws.com
I2=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY \
  --integration-uri "$FN" --payload-format-version 2.0 --query IntegrationId --output text)
I1=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY \
  --integration-uri "$FN" --payload-format-version 1.0 --query IntegrationId --output text)
route() {  # route <route key> [integration id]: create a route on $API, print its id
  aws apigatewayv2 create-route --api-id "$API" --route-key "$1" ${2:+--target "integrations/$2"} \
    --query RouteId --output text
}
for k in 'GET /pets/dog/1' 'GET /pets/dog/{id}' 'GET /pets/{proxy+}' 'ANY /pets/bird/1' 'ANY /{proxy+}' \
         'GET /any' 'ANY /any' 'GET /Case' 'GET /enc/{p}' 'GET /iam'; do
  route "$k" "$I2" >/dev/null
done
# No 'GET /slash/': AWS refuses a route key with a trailing slash ("Part of the given
# route key path is empty", APIGW2-05, recorded 2026-11-03), so /slash and /slash/
# reach ANY /{proxy+}.
route 'GET /noint' >/dev/null
route 'ANY /v1/{proxy+}' "$I1" >/dev/null
aws apigatewayv2 create-stage --api-id "$API" --stage-name '$default' --auto-deploy >/dev/null
aws sqs create-queue --queue-name "$P-gw-q" >/dev/null          # for APIGW2-01 and APIGW2-14
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"apigateway.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name "$P-gw-int" --assume-role-policy-document "$TRUST" >/dev/null
aws iam create-role --role-name "$P-gw-none" --assume-role-policy-document "$TRUST" >/dev/null
aws iam put-role-policy --role-name "$P-gw-int" --policy-name gw --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"sqs:SendMessage\",\"sqs:ReceiveMessage\"],\"Resource\":\"arn:aws:sqs:$R:$A:$P-gw-q\"},{\"Effect\":\"Allow\",\"Action\":\"events:PutEvents\",\"Resource\":\"arn:aws:events:$R:$A:event-bus/default\"}]}"
gw() {  # gw <item>/<step> <curl args...>: an unsigned data-plane request, kept as recurl keeps its exchange
  local out="$REC/$1"; shift
  mkdir -p "$(dirname "$out")"
  recreq "$out" "$@"
  curl -sS -i -v --http1.1 --path-as-is "$@" >"$out.http" 2>"$out.trace"
}
sleep 20                                   # the automatic deployment; APIGW2-04 measures it
```

### 14.2 APIGW2-01: control-plane error texts and validation

Settles: section 2.3's texts and header form, the `x-amzn-ErrorType` suffix, `resourceType` values, the validation texts of 13.5, the SNS subtype error (6.1), partial-update validation for subtype parameters (6.2), literal text beside a path variable (3.2), tag limits (3.6); the identity of a model constraint failure, `BadRequestException` or REST's `ValidationException` (2.3, 12 item 11: case 17 fails a model range, case 17b a model enum); the base coverage note's texts (13.4, the `error.not-found-text` item). Ten minutes, USD 0.

```sh
c() { rec "APIGW2-01/$1" aws apigatewayv2 "${@:2}"; }
c 01-get-missing-api get-api --api-id 0000000000
c 02-delete-missing-api delete-api --api-id 0000000000
c 03-get-missing-route get-route --api-id "$API" --route-id 0000000
c 04-get-missing-integration get-integration --api-id "$API" --integration-id 0000000
c 05-get-missing-stage get-stage --api-id "$API" --stage-name nope
c 06-get-missing-deployment get-deployment --api-id "$API" --deployment-id 000000
c 07-get-missing-authorizer get-authorizer --api-id "$API" --authorizer-id 0000000
c 08-duplicate-stage create-stage --api-id "$API" --stage-name '$default'
c 09-duplicate-route-key create-route --api-id "$API" --route-key 'GET /any'
c 10-bad-method create-route --api-id "$API" --route-key 'FETCH /x'
c 11-path-without-slash create-route --api-id "$API" --route-key 'GET x'
c 12-greedy-not-last create-route --api-id "$API" --route-key 'GET /{proxy+}/x'
c 13-var-inside-segment create-route --api-id "$API" --route-key 'GET /a{b}'
c 14-target-missing-integration create-route --api-id "$API" --route-key 'GET /dangling' --target integrations/0000000
c 15-bad-stage-name create-stage --api-id "$API" --stage-name 'bad.name'
c 16-bad-stage-variable create-stage --api-id "$API" --stage-name badvar --stage-variables 'k=a b'
c 17-timeout-over create-integration --api-id "$API" --integration-type AWS_PROXY --integration-uri "$FN" \
  --payload-format-version 2.0 --timeout-in-millis 30001
c 17b-enum-integration-type create-integration --api-id "$API" --integration-type HTTPS_PROXY \
  --integration-uri "$FN" --payload-format-version 2.0
c 18-bad-payload-version create-integration --api-id "$API" --integration-type AWS_PROXY --integration-uri "$FN" \
  --payload-format-version 3.0
c 19-no-payload-version create-integration --api-id "$API" --integration-type AWS_PROXY --integration-uri "$FN"
c 20-subtype-missing-body create-integration --api-id "$API" --integration-type AWS_PROXY \
  --integration-subtype SQS-SendMessage --payload-format-version 1.0 --credentials-arn "arn:aws:iam::$A:role/$P-gw-int" \
  --request-parameters "{\"QueueUrl\":\"$Q/$P-gw-q\"}"
c 21-sns-subtype create-integration --api-id "$API" --integration-type AWS_PROXY --integration-subtype SNS-Publish \
  --payload-format-version 1.0 --credentials-arn "arn:aws:iam::$A:role/$P-gw-int" --request-parameters '{"Message":"x"}'
IS=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --integration-subtype SQS-SendMessage \
  --payload-format-version 1.0 --credentials-arn "arn:aws:iam::$A:role/$P-gw-int" \
  --request-parameters "{\"QueueUrl\":\"$Q/$P-gw-q\",\"MessageBody\":\"x\"}" --query IntegrationId --output text)
c 22-subtype-partial-update update-integration --api-id "$API" --integration-id "$IS" \
  --request-parameters '{"MessageBody":"$request.body"}'
c 23-get-tags-missing get-tags --resource-arn "arn:aws:apigateway:$R::/apis/0000000000"
c 24-tag-key-129 tag-resource --resource-arn "arn:aws:apigateway:$R::/apis/$API" \
  --tags "$(printf 'k%.0s' $(seq 1 129))=v"
c 25-tag-value-1601 tag-resource --resource-arn "arn:aws:apigateway:$R::/apis/$API" \
  --tags "k=$(printf 'v%.0s' $(seq 1 1601))"
c 26-websocket-type-on-http-api create-integration --api-id "$API" --integration-type MOCK
```

Teardown: none; anything created here goes with `$P-gw` in 14.17.

### 14.3 APIGW2-02: the 429 shape

Settles: `TooManyRequestsException` or `LimitExceededException` on the wire, its `limitType`, and the `CreateDeployment` quota (2.3, 3.9). Two minutes, USD 0.

```sh
for i in 1 2 3; do
  rec "APIGW2-02/0$i-create-deployment" env AWS_MAX_ATTEMPTS=1 aws apigatewayv2 create-deployment --api-id "$API"
done
```

`AWS_MAX_ATTEMPTS=1` keeps the CLI from retrying the throttle. Teardown: none.

### 14.4 APIGW2-03: create and read defaults, quick create, CORS case

Settles: field presence for unset values (3.1, 3.3, 3.4), integration and authorizer id formats and the base coverage note's id shapes (13.4, the `ids.shape` item), fractional seconds, the hidden `PassthroughBehavior`, CORS list case (3.7), the braced `routeSelectionExpression` (12 item 5), quick-create managed resources and their modification errors (3.1), `GetApis` paging. Ten minutes, USD 0.

```sh
rec APIGW2-03/01-create-api aws apigatewayv2 create-api --name "$P-gw-def" --protocol-type HTTP --tags k=v
D=$(aws apigatewayv2 get-apis --query "Items[?Name=='$P-gw-def'].ApiId" --output text)
rec APIGW2-03/02-get-api aws apigatewayv2 get-api --api-id "$D"
rec APIGW2-03/03-get-tags aws apigatewayv2 get-tags --resource-arn "arn:aws:apigateway:$R::/apis/$D"
rec APIGW2-03/04-route-without-target aws apigatewayv2 create-route --api-id "$D" --route-key 'GET /a'
rec APIGW2-03/05-lambda-integration-as-terraform aws apigatewayv2 create-integration --api-id "$D" \
  --integration-type AWS_PROXY --connection-type INTERNET --passthrough-behavior WHEN_NO_MATCH \
  --integration-uri "arn:aws:apigateway:$R:lambda:path/2015-03-31/functions/$FN/invocations" --payload-format-version 2.0
IL=$(sed -n 's/^ *"IntegrationId": "\([^"]*\)".*/\1/p' "$REC/APIGW2-03/05-lambda-integration-as-terraform.out")
rec APIGW2-03/05b-get-lambda-integration aws apigatewayv2 get-integration --api-id "$D" --integration-id "$IL"
rec APIGW2-03/05c-create-authorizer aws apigatewayv2 create-authorizer --api-id "$D" --name a --authorizer-type REQUEST \
  --authorizer-uri "arn:aws:apigateway:$R:lambda:path/2015-03-31/functions/$FN/invocations" \
  --identity-source '$request.header.Authorization' --authorizer-payload-format-version 2.0
rec APIGW2-03/06-http-proxy-integration aws apigatewayv2 create-integration --api-id "$D" \
  --integration-type HTTP_PROXY --integration-method ANY --integration-uri https://example.com --payload-format-version 1.0
rec APIGW2-03/07-create-stage aws apigatewayv2 create-stage --api-id "$D" --stage-name dev
rec APIGW2-03/08-get-stage aws apigatewayv2 get-stage --api-id "$D" --stage-name dev
rec APIGW2-03/09-update-cors aws apigatewayv2 update-api --api-id "$D" --cors-configuration \
  '{"AllowOrigins":["https://A.example"],"AllowMethods":["get","post"],"AllowHeaders":["Content-Type","X-Up"],"ExposeHeaders":["X-E"],"MaxAge":0,"AllowCredentials":false}'
rec APIGW2-03/10-get-api-cors aws apigatewayv2 get-api --api-id "$D"
rec APIGW2-03/11-delete-cors aws apigatewayv2 delete-cors-configuration --api-id "$D"
rec APIGW2-03/12-get-api-no-cors aws apigatewayv2 get-api --api-id "$D"
rec APIGW2-03/13-braced-selection aws apigatewayv2 update-api --api-id "$D" \
  --route-selection-expression '${request.method} ${request.path}'
rec APIGW2-03/14-get-apis-page aws apigatewayv2 get-apis --max-results 1 --no-paginate
rec APIGW2-03/15-quick-create aws apigatewayv2 create-api --name "$P-gw-quick" --protocol-type HTTP --target "$FN"
QK=$(aws apigatewayv2 get-apis --query "Items[?Name=='$P-gw-quick'].ApiId" --output text)
rec APIGW2-03/16-quick-routes aws apigatewayv2 get-routes --api-id "$QK"
rec APIGW2-03/17-quick-integrations aws apigatewayv2 get-integrations --api-id "$QK"
rec APIGW2-03/18-quick-stages aws apigatewayv2 get-stages --api-id "$QK"
rec APIGW2-03/19-delete-managed-route aws apigatewayv2 delete-route --api-id "$QK" \
  --route-id "$(aws apigatewayv2 get-routes --api-id "$QK" --query 'Items[0].RouteId' --output text)"
rec APIGW2-03/20-update-managed-stage aws apigatewayv2 update-stage --api-id "$QK" --stage-name '$default' --description x
rec APIGW2-03/21-delete-managed-integration aws apigatewayv2 delete-integration --api-id "$QK" \
  --integration-id "$(aws apigatewayv2 get-integrations --api-id "$QK" --query 'Items[0].IntegrationId' --output text)"
```

Teardown: `aws apigatewayv2 delete-api --api-id "$D"; aws apigatewayv2 delete-api --api-id "$QK"`.

### 14.5 APIGW2-04: deployments and automatic deployment

Settles: section 3.5's open items, a stage with no deployment, an undeployed route on a manual stage (finding 7), the automatic-deployment delay (the CreateStage coverage note, 13.4's `deploy.auto-latency` item); switch `apigatewayv2.front.answers`, its deployment rules and `NoDeployment`. Ten minutes, USD 0.

```sh
S=$(aws apigatewayv2 create-api --name "$P-gw-dep" --protocol-type HTTP --query ApiId --output text)
SH=https://$S.execute-api.$R.amazonaws.com
rec APIGW2-04/01-deploy-no-routes aws apigatewayv2 create-deployment --api-id "$S"
IS=$(aws apigatewayv2 create-integration --api-id "$S" --integration-type AWS_PROXY --integration-uri "$FN" \
  --payload-format-version 2.0 --query IntegrationId --output text)
aws apigatewayv2 create-route --api-id "$S" --route-key 'GET /a' --target "integrations/$IS" >/dev/null
aws apigatewayv2 create-stage --api-id "$S" --stage-name dev >/dev/null
sleep 10
gw APIGW2-04/02-stage-without-deployment "$SH/dev/a"
rec APIGW2-04/03-deploy-to-dev aws apigatewayv2 create-deployment --api-id "$S" --stage-name dev
rec APIGW2-04/04-get-stage-dev aws apigatewayv2 get-stage --api-id "$S" --stage-name dev
sleep 10
gw APIGW2-04/05-after-deploy "$SH/dev/a"
aws apigatewayv2 create-route --api-id "$S" --route-key 'GET /b' --target "integrations/$IS" >/dev/null
sleep 10
gw APIGW2-04/06-undeployed-route "$SH/dev/b"
t0=$(date +%s)
aws apigatewayv2 create-stage --api-id "$S" --stage-name auto --auto-deploy >/dev/null
for i in $(seq 1 120); do
  aws apigatewayv2 get-stage --api-id "$S" --stage-name auto --query LastDeploymentStatusMessage --output text |
    grep -q Successfully && break
  sleep 1
done
echo "auto-deploy status after $(( $(date +%s) - t0 )) s" >>"$REC/APIGW2-04/notes.md"
t0=$(date +%s)
for i in $(seq 1 120); do
  curl -s -o /dev/null -w '%{http_code}' "$SH/auto/a" | grep -q 200 && break
  sleep 1
done
echo "auto stage served 200 after $(( $(date +%s) - t0 )) s (120 s means never)" >>"$REC/APIGW2-04/notes.md"
rec APIGW2-04/07-get-stage-auto aws apigatewayv2 get-stage --api-id "$S" --stage-name auto
DEV=$(aws apigatewayv2 get-stage --api-id "$S" --stage-name dev --query DeploymentId --output text)
rec APIGW2-04/08-deployment-id-on-auto aws apigatewayv2 update-stage --api-id "$S" --stage-name auto --deployment-id "$DEV"
rec APIGW2-04/09-delete-active-deployment aws apigatewayv2 delete-deployment --api-id "$S" --deployment-id "$DEV"
rec APIGW2-04/10-get-deployments aws apigatewayv2 get-deployments --api-id "$S"
```

Teardown: `aws apigatewayv2 delete-api --api-id "$S"`.

### 14.6 APIGW2-05: route selection

Settles: section 4.3's and 4.4's open items, a route without a target (3.2); switch `apigatewayv2.route.matching`. In `notes.md`, write the `routeKey` each echoed body shows. Five minutes, USD 0.

```sh
for s in "01 GET /pets/dog/1" "02 GET /pets/dog/2" "03 GET /pets/cat/1" "04 POST /test/5" "05 GET /any" \
         "06 POST /any" "07 HEAD /any" "08 GET /Case" "09 GET /case" "10 GET /slash/" "11 GET /slash" \
         "12 GET /pets/dog/1/" "13 GET //pets/dog/1" "14 GET /enc/a%2Fb" "15 GET /enc/a%20b" "16 GET /pets/" \
         "17 GET /noint" "18 GET /" "19 OPTIONS /any" "20 GET /pets/dog/%31" "21 GET /PETS/dog/1" \
         "22 GET /pets/bird/1"; do
  set -- $s
  if [ "$2" = HEAD ]; then gw "APIGW2-05/$1" --head "$GW$3"; else gw "APIGW2-05/$1" -X "$2" "$GW$3"; fi
done
```

Teardown: none.

### 14.7 APIGW2-06: stage selection and the gateway's own answers

Settles: section 4.2, the 404 headers (4.5), unknown API ids and disabled endpoints; switch `apigatewayv2.front.answers`. Ten minutes including waits, USD 0.

```sh
G=$(aws apigatewayv2 create-api --name "$P-gw-stg" --protocol-type HTTP --query ApiId --output text)
H=https://$G.execute-api.$R.amazonaws.com
IG=$(aws apigatewayv2 create-integration --api-id "$G" --integration-type AWS_PROXY --integration-uri "$FN" \
  --payload-format-version 2.0 --query IntegrationId --output text)
for k in 'GET /prod/x' 'GET /x'; do
  aws apigatewayv2 create-route --api-id "$G" --route-key "$k" --target "integrations/$IG" >/dev/null
done
for s in '$default' prod; do
  aws apigatewayv2 create-stage --api-id "$G" --stage-name "$s" --auto-deploy --stage-variables sv=one >/dev/null
done
sleep 20
gw APIGW2-06/01-prod-x "$H/prod/x"
gw APIGW2-06/02-x "$H/x"
gw APIGW2-06/03-prod-prod-x "$H/prod/prod/x"
gw APIGW2-06/04-no-route "$H/nope"
gw APIGW2-06/05-stage-root "$H/prod"
gw APIGW2-06/06-stage-case "$H/PROD/x"
aws apigatewayv2 delete-stage --api-id "$G" --stage-name '$default'
sleep 15
gw APIGW2-06/07-no-default-stage "$H/nostage/x"
gw APIGW2-06/08-unknown-api "https://0000000000.execute-api.$R.amazonaws.com/x"
aws apigatewayv2 update-api --api-id "$G" --disable-execute-api-endpoint >/dev/null
sleep 60
gw APIGW2-06/09-disabled-endpoint "$H/prod/x"
```

If step 08 fails to resolve, note curl's exit code and message: the absence of a wildcard record is the finding. Teardown: `aws apigatewayv2 delete-api --api-id "$G"`.

### 14.8 APIGW2-07: the 2.0 request event

Settles: section 5.1's open items; switch `apigatewayv2.payload.unrecorded`, its 2.0 request fields. In `notes.md`, record per step: `body` present or absent, `isBase64Encoded`, header joining, `cookies`, query decoding, `rawPath` encoding, the stage in `rawPath`, `http.protocol`, and whether `requestContext.requestId` equals the `apigw-requestid` response header. Ten minutes, USD 0.

```sh
aws apigatewayv2 create-stage --api-id "$API" --stage-name prod --auto-deploy --stage-variables sv=one,sv2=two >/dev/null
sleep 20
head -c 16 /dev/urandom >"$REC/_work/APIGW2/bin"
gw APIGW2-07/01-get "$GW/pets/dog/abc"
gw APIGW2-07/02-text -H 'Content-Type: text/plain' --data-binary 'hello' "$GW/e"
gw APIGW2-07/03-json -H 'Content-Type: application/json' --data-binary '{"a":1}' "$GW/e"
gw APIGW2-07/04-octet -H 'Content-Type: application/octet-stream' --data-binary @"$REC/_work/APIGW2/bin" "$GW/e"
gw APIGW2-07/05-multipart -F a=1 "$GW/e"
gw APIGW2-07/06-urlencoded --data 'a=1&b=2' "$GW/e"
gw APIGW2-07/07-no-content-type -H 'Content-Type:' --data-binary 'x' "$GW/e"
gw APIGW2-07/08-dup-headers -H 'X-Dup: a' -H 'X-Dup: b' -H 'X-MiXeD: v' "$GW/e"
gw APIGW2-07/09-cookies -H 'Cookie: a=1; b=2' -H 'Cookie: c=3' "$GW/e"
gw APIGW2-07/10-query "$GW/e?a=1&a=2&b=%2Fx&c&d=&e=%20&f=a+b"
gw APIGW2-07/11-path-param "$GW/pets/dog/a%20b"
gw APIGW2-07/12-greedy "$GW/pets/cat/x/y%2Fz"
gw APIGW2-07/13-named-stage "$GW/prod/pets/dog/abc"
gw APIGW2-07/14-client-xff -H 'X-Forwarded-For: 203.0.113.9' "$GW/e"
gw APIGW2-07/15-empty-post -X POST "$GW/e"
gw APIGW2-07/16-http2 --http2 "$GW/e"
```

The later `--http2` overrides `gw`'s `--http1.1`. Teardown: none (the `prod` stage stays for APIGW2-08 and APIGW2-11).

### 14.9 APIGW2-08: the 1.0 request event

Settles: section 5.2's open items; switch `apigatewayv2.payload.unrecorded`, its 1.0 fields. Five minutes, USD 0. Steps 04 and 05 need the `prod` stage and `$REC/_work/APIGW2/bin` that the first three lines of 14.8 create; run those first when this case runs without APIGW2-07.

```sh
gw APIGW2-08/01-get "$GW/v1/a/b?x=1&x=2&y=%2F"
gw APIGW2-08/02-dup-headers -H 'X-Dup: a' -H 'X-Dup: b' "$GW/v1/a"
gw APIGW2-08/03-json -H 'Content-Type: application/json' --data-binary '{"a":1}' "$GW/v1/a"
gw APIGW2-08/04-named-stage "$GW/prod/v1/a"
gw APIGW2-08/05-octet -H 'Content-Type: application/octet-stream' --data-binary @"$REC/_work/APIGW2/bin" "$GW/v1/a"
gw APIGW2-08/06-cookies -H 'Cookie: a=1; b=2' "$GW/v1/a"
```

Teardown: none.

### 14.10 APIGW2-09: interpreting responses

Settles: sections 5.3 and 5.4; switch `apigatewayv2.payload.unrecorded`, its response fields. Five minutes, USD 0.

```sh
for m in string object body-no-status object-body number-header nulls mvh no-ctype b64 cookies status-999 status-string; do
  gw "APIGW2-09/2.0-$m" "$GW/r?mode=$m"
done
for m in string body-no-status mvh no-ctype; do
  gw "APIGW2-09/1.0-$m" "$GW/v1/r?mode=$m"
done
```

Teardown: none.

### 14.11 APIGW2-10: failures and limits

Settles: section 5.6 and 4.7; switch `apigatewayv2.lambda.failure-answers`. Ten minutes, USD 0 (about 27 MB uploaded, which is free inbound).

```sh
gw APIGW2-10/01-throw-2.0 "$GW/r?mode=throw"
gw APIGW2-10/02-throw-1.0 "$GW/v1/r?mode=throw"
IT=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --integration-uri "$FN" \
  --payload-format-version 2.0 --timeout-in-millis 1000 --query IntegrationId --output text)
route 'GET /slow' "$IT" >/dev/null
sleep 20
gw APIGW2-10/03-timeout "$GW/slow?mode=sleep"
head -c 10000001 /dev/zero >"$REC/_work/APIGW2/big-dec"   # one byte over 10 MB decimal
head -c 10485761 /dev/zero >"$REC/_work/APIGW2/big-bin"   # one byte over 10 MiB
gw APIGW2-10/04-over-10mb-decimal -H 'Content-Type: application/octet-stream' --data-binary @"$REC/_work/APIGW2/big-dec" "$GW/e"
gw APIGW2-10/05-over-10mib -H 'Content-Type: application/octet-stream' --data-binary @"$REC/_work/APIGW2/big-bin" "$GW/e"
head -c 7000000 /dev/zero | tr '\0' a >"$REC/_work/APIGW2/seven"
gw APIGW2-10/06-over-lambda-6mb -H 'Content-Type: text/plain' --data-binary @"$REC/_work/APIGW2/seven" "$GW/e"
gw APIGW2-10/07-headers-over-10kb -H "X-Big: $(head -c 11000 /dev/zero | tr '\0' a)" "$GW/e"
rm "$REC/_work/APIGW2/big-dec" "$REC/_work/APIGW2/big-bin" "$REC/_work/APIGW2/seven"
```

Teardown: none.

### 14.12 APIGW2-11: the Lambda permission and the source ARN

Settles: sections 5.5 and 8.4; switch `apigatewayv2.lambda.permission`. In `notes.md`, write which steps reached the function (a 200 echo) and which got the error. Fifteen minutes (each step waits for the policy change), USD 0.

```sh
FP=arn:aws:lambda:$R:$A:function:$P-gw-perm
IP=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --integration-uri "$FP" \
  --payload-format-version 2.0 --query IntegrationId --output text)
for k in 'GET /perm/{id}' 'ANY /permany' 'GET /permg/{proxy+}'; do route "$k" "$IP" >/dev/null; done
sleep 20
gw APIGW2-11/01-no-permission "$GW/perm/1"
try() {  # try <step> <source ARN after the api id> <path>: allow only that ARN, call once, remove it
  aws lambda add-permission --function-name "$P-gw-perm" --statement-id narrow --action lambda:InvokeFunction \
    --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:$R:$A:$API/$2" >/dev/null
  sleep 15
  gw "APIGW2-11/$1" "$GW$3"
  aws lambda remove-permission --function-name "$P-gw-perm" --statement-id narrow
}
try 02-template '$default/GET/perm/{id}' /perm/1
try 03-concrete '$default/GET/perm/1' /perm/1
try 04-any-literal '$default/ANY/permany' /permany
try 05-any-as-method '$default/GET/permany' /permany
try 06-greedy-template '$default/GET/permg/{proxy+}' /permg/a/b
try 07-greedy-concrete '$default/GET/permg/a/b' /permg/a/b
try 08-named-stage 'prod/GET/perm/{id}' /prod/perm/1
```

Teardown: none (`try` removes its statement; the function goes in 14.17).

### 14.13 APIGW2-12: AWS_IAM routes

Settles: section 8.3 (unsigned, wrong service, wrong region, malformed, and signed without `execute-api:Invoke`); 13.4's out-of-scope `iam.unsigned` item; whether the `Authorization` header reaches a `NONE` route's function. Eight minutes, USD 0. The `.trace` files hold the signature: redact as kit section 2 says.

```sh
RI=$(aws apigatewayv2 get-routes --api-id "$API" --query "Items[?RouteKey=='GET /iam'].RouteId" --output text)
aws apigatewayv2 update-route --api-id "$API" --route-id "$RI" --authorization-type AWS_IAM >/dev/null
sleep 20
gw APIGW2-12/01-unsigned "$GW/iam"
SVC=execute-api recurl APIGW2-12/02-signed "$GW/iam"
SVC=lambda recurl APIGW2-12/03-wrong-service "$GW/iam"
OTHER=us-west-2; [ "$R" = us-west-2 ] && OTHER=us-east-1
R=$OTHER SVC=execute-api recurl APIGW2-12/04-wrong-region "$GW/iam"
gw APIGW2-12/05-garbage-auth -H 'Authorization: AWS4-HMAC-SHA256 Credential=x' "$GW/iam"
SVC=execute-api recurl APIGW2-12/06-signed-to-none-route "$GW/e"
aws iam create-role --role-name "$P-gw-caller" --assume-role-policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::$A:root\"},\"Action\":\"sts:AssumeRole\"}]}" >/dev/null
sleep 15
read -r AK SK ST <<<"$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-gw-caller" --role-session-name gw \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
AWS_ACCESS_KEY_ID=$AK AWS_SECRET_ACCESS_KEY=$SK AWS_SESSION_TOKEN=$ST SVC=execute-api \
  recurl APIGW2-12/07-signed-without-invoke-permission "$GW/iam"
unset AK SK ST
```

The role `$P-gw-caller` has no policy, so its session lacks `execute-api:Invoke`; APIGW2-15 uses it again. Teardown: in 14.17.

### 14.14 APIGW2-13: CORS

Settles: sections 3.7 and 4.6; 13.4's out-of-scope `cors.preflight` item. Ten minutes, USD 0.

```sh
aws apigatewayv2 update-api --api-id "$API" --cors-configuration \
  '{"AllowOrigins":["https://a.example"],"AllowMethods":["GET","POST"],"AllowHeaders":["content-type"],"ExposeHeaders":["x-e"],"MaxAge":300,"AllowCredentials":true}' >/dev/null
sleep 20
O='Origin: https://a.example'
gw APIGW2-13/01-preflight -X OPTIONS -H "$O" -H 'Access-Control-Request-Method: POST' \
  -H 'Access-Control-Request-Headers: content-type' "$GW/any"
gw APIGW2-13/02-preflight-other-origin -X OPTIONS -H 'Origin: https://b.example' -H 'Access-Control-Request-Method: POST' "$GW/any"
gw APIGW2-13/03-preflight-other-method -X OPTIONS -H "$O" -H 'Access-Control-Request-Method: DELETE' "$GW/any"
gw APIGW2-13/04-options-without-acrm -X OPTIONS -H "$O" "$GW/any"
gw APIGW2-13/05-get-with-origin -H "$O" "$GW/r?mode=cors"
gw APIGW2-13/06-get-other-origin -H 'Origin: https://b.example' "$GW/r?mode=cors"
gw APIGW2-13/07-get-without-origin "$GW/r?mode=cors"
gw APIGW2-13/08-preflight-on-iam-route -X OPTIONS -H "$O" -H 'Access-Control-Request-Method: GET' "$GW/iam"
route 'OPTIONS /any' "$I2" >/dev/null
sleep 20
gw APIGW2-13/09-preflight-with-options-route -X OPTIONS -H "$O" -H 'Access-Control-Request-Method: POST' "$GW/any"
rec APIGW2-13/10-wildcard-with-credentials aws apigatewayv2 update-api --api-id "$API" \
  --cors-configuration '{"AllowOrigins":["*"],"AllowCredentials":true}'
rec APIGW2-13/11-max-age-over aws apigatewayv2 update-api --api-id "$API" \
  --cors-configuration '{"AllowOrigins":["*"],"MaxAge":86401}'
aws apigatewayv2 delete-cors-configuration --api-id "$API"
```

Teardown: the last line.

### 14.15 APIGW2-14: service integrations

Settles: sections 6.2 to 6.4 and finding 2's retry question; 13.4's out-of-scope `service-integration.response` item. In `notes.md`, write the time bash prints for steps 03 to 05: an answer much slower than step 01 suggests retries. Fifteen minutes, USD 0.

The queue `$P-gw-q` and the roles `$P-gw-int` (allowed) and `$P-gw-none` (no policy) come from 14.1.

```sh
ROLE=arn:aws:iam::$A:role/$P-gw-int
svc() {  # svc <route key> <subtype> <role arn> <request parameters JSON>: an integration and its route
  local i
  i=$(aws apigatewayv2 create-integration --api-id "$API" --integration-type AWS_PROXY --integration-subtype "$2" \
    --payload-format-version 1.0 --credentials-arn "$3" --request-parameters "$4" --query IntegrationId --output text)
  route "$1" "$i" >/dev/null
}
svc 'POST /sqs' SQS-SendMessage "$ROLE" "{\"QueueUrl\":\"$Q/$P-gw-q\",\"MessageBody\":\"\$request.body\"}"
svc 'POST /sqs-delay' SQS-SendMessage "$ROLE" "{\"QueueUrl\":\"$Q/$P-gw-q\",\"MessageBody\":\"x\",\"DelaySeconds\":\"\$request.header.x-delay\"}"
svc 'POST /sqs-missing' SQS-SendMessage "$ROLE" "{\"QueueUrl\":\"$Q/$P-gw-missing\",\"MessageBody\":\"\$request.body\"}"
svc 'POST /sqs-denied' SQS-SendMessage "arn:aws:iam::$A:role/$P-gw-none" "{\"QueueUrl\":\"$Q/$P-gw-q\",\"MessageBody\":\"\$request.body\"}"
svc 'POST /sqs-norole' SQS-SendMessage "arn:aws:iam::$A:role/$P-gw-absent" "{\"QueueUrl\":\"$Q/$P-gw-q\",\"MessageBody\":\"\$request.body\"}"
svc 'GET /sqs-receive' SQS-ReceiveMessage "$ROLE" "{\"QueueUrl\":\"$Q/$P-gw-q\"}"
svc 'POST /events' EventBridge-PutEvents "$ROLE" '{"Source":"$request.header.x-source","DetailType":"t","Detail":"$request.body"}'
sleep 20
time gw APIGW2-14/01-sqs-send -H 'Content-Type: application/json' --data-binary '{"a":1}' "$GW/sqs"
gw APIGW2-14/02-sqs-delay -H 'x-delay: 1' --data-binary 'x' "$GW/sqs-delay"
time gw APIGW2-14/03-sqs-missing-queue --data-binary 'x' "$GW/sqs-missing"
time gw APIGW2-14/04-sqs-role-without-permission --data-binary 'x' "$GW/sqs-denied"
time gw APIGW2-14/05-sqs-role-absent --data-binary 'x' "$GW/sqs-norole"
gw APIGW2-14/06-sqs-receive "$GW/sqs-receive"
gw APIGW2-14/07-events -H 'x-source: p.test' -H 'Content-Type: application/json' --data-binary '{"a":1}' "$GW/events"
gw APIGW2-14/08-events-text-detail -H 'x-source: p.test' --data-binary 'not json' "$GW/events"
gw APIGW2-14/09-events-without-source --data-binary '{"a":1}' "$GW/events"
```

Teardown: in 14.17.

### 14.16 APIGW2-15: control-plane IAM denials and resources

Settles: the identity of an IAM denial on a v2 control-plane call (status, `x-amzn-ErrorType`, message key and text; 2.3, `iam-enforcement-surfaces.md` 12.3); where the message names the resource, the resource of each call (8.1): the collection and item forms, the Cors, AccessLogSettings and RouteSettings resources, how a route key is written in its ARN, and the tags form with its encoding; and, from steps 16 to 18, whether a tag call also needs a permission on the tagged API (8.1). In `notes.md`, write per step the status line, `x-amzn-ErrorType`, the body, and the action and resource the message names, if it names one. Step 01 repeats IAM-ENF-34 (`iam-enforcement-surfaces.md` 13.35); either recording settles the identity. Five minutes, USD 0.

The role `$P-gw-caller` from APIGW2-12 has no policy, so every call below should be denied and change nothing until step 16's policy allows the tag resource.

```sh
read -r AK SK ST <<<"$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-gw-caller" --role-session-name gw15 \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
deny() {  # deny <step> <apigatewayv2 args...>: one call as the $P-gw-caller session, kept as rec keeps it
  ( unset AWS_PROFILE; export AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" AWS_SESSION_TOKEN="$ST"
    rec "APIGW2-15/$1" aws apigatewayv2 "${@:2}" )
}
DEP=$(aws apigatewayv2 get-stage --api-id "$API" --stage-name '$default' --query DeploymentId --output text)
TAGGED="arn:aws:apigateway:$R::/apis/$API"
deny 01-get-apis get-apis
deny 02-create-api create-api --name "$P-gw-denied" --protocol-type HTTP
deny 03-get-api get-api --api-id "$API"
deny 04-update-api update-api --api-id "$API" --description x
deny 05-delete-cors delete-cors-configuration --api-id "$API"
deny 06-get-routes get-routes --api-id "$API"
deny 07-get-route get-route --api-id "$API" --route-id "$RI"        # RI: the GET /iam route of APIGW2-12
deny 08-get-integration get-integration --api-id "$API" --integration-id "$I2"
deny 09-get-stage get-stage --api-id "$API" --stage-name '$default'
deny 10-delete-access-log-settings delete-access-log-settings --api-id "$API" --stage-name '$default'
deny 11-delete-route-settings delete-route-settings --api-id "$API" --stage-name '$default' --route-key 'GET /any'
deny 12-get-deployment get-deployment --api-id "$API" --deployment-id "$DEP"
deny 13-get-tags get-tags --resource-arn "$TAGGED"
deny 14-tag-resource tag-resource --resource-arn "$TAGGED" --tags k=v
deny 15-untag-resource untag-resource --resource-arn "$TAGGED" --tag-keys k
aws iam put-role-policy --role-name "$P-gw-caller" --policy-name tags --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"apigateway:*\",\"Resource\":\"arn:aws:apigateway:$R::/tags/*\"}]}"
sleep 15                                   # the new role policy must propagate
deny 16-get-tags-tags-only get-tags --resource-arn "$TAGGED"
deny 17-tag-resource-tags-only tag-resource --resource-arn "$TAGGED" --tags k=v
deny 18-untag-resource-tags-only untag-resource --resource-arn "$TAGGED" --tag-keys k
aws iam delete-role-policy --role-name "$P-gw-caller" --policy-name tags
unset AK SK ST
```

A success in steps 16 to 18 means the `/tags/...` resource alone authorizes the call; a denial naming `$TAGGED` means the tagged API needs its own permission, as for REST APIs. If step 02 is not denied, its API goes with `$P-gw` in 14.17. Teardown: none.

### 14.17 Teardown

Five minutes.

```sh
for a in $(aws apigatewayv2 get-apis --query "Items[?starts_with(Name, '$P-gw')].ApiId" --output text); do
  aws apigatewayv2 delete-api --api-id "$a"
done
for f in "$P-gw" "$P-gw-perm"; do
  aws lambda delete-function --function-name "$f"
  aws logs delete-log-group --log-group-name "/aws/lambda/$f"
done
aws sqs delete-queue --queue-url "$Q/$P-gw-q"
aws iam detach-role-policy --role-name "$P-gw-fn" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws iam delete-role-policy --role-name "$P-gw-int" --policy-name gw
for r in "$P-gw-fn" "$P-gw-int" "$P-gw-none" "$P-gw-caller"; do aws iam delete-role --role-name "$r"; done
rm -rf "$REC/_work/APIGW2"
aws apigatewayv2 get-apis --query "Items[?starts_with(Name, '$P-gw')].Name"   # expect []
```

The filter is `$P-gw`, the prefix every API in this queue uses, so another queue's HTTP APIs under `$P` survive this teardown.
