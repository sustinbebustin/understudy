# APIGW2-03: create and read defaults, quick create, CORS case (apigateway-http.md 14.4)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: n/a (CLI only)

## Expected

- Field presence for unset values, Api (3.1): CreateApi and GetApi send `ipAddressType: "ipv4"` and `disableExecuteApiEndpoint: false` in the JSON (present, not client-defaulted). GetApi carries `tags`. No `description`, no `corsConfiguration` when unset.
- Integration fields (3.3): matches: `integrationMethod: "POST"`, `connectionType: "INTERNET"`, `timeoutInMillis: 30000`, `payloadFormatVersion: "2.0"`. The invoke-URI form of `integrationUri` is stored verbatim.
- Hidden `PassthroughBehavior` (3.3): matches (not returned): the integration sent with `--passthrough-behavior WHEN_NO_MATCH` returns no `passthroughBehavior` on create or get. Whether AWS stores it hidden cannot be seen.
- Stage fields (3.4): matches: `defaultRouteSettings {"detailedMetricsEnabled":false}`, `routeSettings {}`, `stageVariables {}`, `tags {}`, `createdDate`, `lastUpdatedDate`, no `deploymentId`. `autoDeploy: false` is omitted (key absent).
- Route without target (04): `{"routeId":"<7>","routeKey":"GET /a","authorizationType":"NONE","apiKeyRequired":false}`. matches 3.2.
- Id shapes (2.5, 13.4 `ids.shape`): api 10 (`9ackjmy7v7`), route 7 (`23ikfe8`), integration 7 (`b5bzc0o`, `n2n6zwi`), deployment 6 (`98l8j5`): matches. Authorizer id (05c): 6 characters (`hguhl4`); the doc left it unverified.
- Fractional seconds (2.1): matches the no-fraction reading: `createdDate` `2026-11-03T16:43:26Z` has no fraction.
- CORS list case (3.7): AWS normalizes. `AllowOrigins ["https://A.example"]` is stored as `["https://a.example"]` (lower-cased), `AllowMethods ["get","post"]` as `["GET","POST"]` (upper-cased), `AllowHeaders ["Content-Type","X-Up"]` as `["content-type","x-up"]` and `ExposeHeaders ["X-E"]` as `["x-e"]` (lower-cased). This explains Terraform's case-insensitive compare.
- UpdateApi success status (2.2 table: 200): differs: `PATCH /v2/apis/{id}` answered `201` (steps 09 and 13) with the Api body. UpdateStage (step 20) answered 200.
- DeleteCorsConfiguration: 204 with an empty body; the later GetApi has no `corsConfiguration`. matches.
- Braced `routeSelectionExpression` (12 item 5): accepted and echoed as sent: `${request.method} ${request.path}` is stored braced, not normalized.
- GetApis paging (`--max-results 1`): one item plus `nextToken` `aWQ9UzlhY2tqbXk3djc%3D` (url-encoded base64 of `id=S<api-id>`).
- Quick create (3.1): matches: one `$default` route (`apiGatewayManaged: true`), an AWS_PROXY integration with `integrationUri` the function ARN as given, `payloadFormatVersion 2.0`, `apiGatewayManaged: true`, and a `$default` stage with `autoDeploy: true`, `apiGatewayManaged: true` and `lastDeploymentStatusMessage "Successfully deployed stage with deployment ID '98l8j5'"`.
- Delete managed route (19): differs ("You can't modify the $default route key" read as a refusal): 204, accepted.
- Update managed `$default` stage (20): differs ("You can't modify the $default stage"): 200, `description: "x"` stored.
- Delete managed integration (21): `ConflictException` 409 "Cannot delete Integration because it is referenced by the following Routes with Ids: [c4e4267]". The refusal names the route deleted in step 19, so either the managed route survives its 204 delete or the reference check is stale. The doc's "can't delete it" holds, but through the reference check, not a managed-resource error.
