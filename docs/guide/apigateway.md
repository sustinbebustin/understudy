# API Gateway

understudy serves API Gateway HTTP APIs (`apigatewayv2`): an API, its routes, integrations and stages, and the data plane that invokes the Lambda function a route targets. It also serves a first set of REST APIs (`apigateway`), described in [REST APIs](#rest-apis) at the end of this page. Invoking a function needs Docker, as Lambda does ([lambda.md](lambda.md)). What each operation supports is in [coverage.md](../coverage.md); this page grows with it.

## A Lambda-backed API

```sh
cat > handler.py <<'EOF'
def handler(event, context):
    return {"statusCode": 200, "body": "hello from " + event["routeKey"]}
EOF
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name hello --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip

aws apigatewayv2 create-api --name hello --protocol-type HTTP
# "ApiId": "wfod7wzey6"
aws apigatewayv2 create-integration --api-id wfod7wzey6 --integration-type AWS_PROXY \
  --integration-uri arn:aws:lambda:us-east-1:000000000000:function:hello --payload-format-version 2.0
# "IntegrationId": "ge9nmvg"
aws apigatewayv2 create-route --api-id wfod7wzey6 --route-key 'GET /hello' --target integrations/ge9nmvg
aws apigatewayv2 create-stage --api-id wfod7wzey6 --stage-name '$default' --auto-deploy
```

`create-api` takes every HTTP API member and answers the defaults AWS fills. `ApiEndpoint` names understudy, on the port you addressed, never `amazonaws.com`:

```sh
aws apigatewayv2 create-api --name hello --protocol-type HTTP --description 'demo API' --tags team=payments
# "ApiEndpoint": "http://o6tvc1uf6b.execute-api.us-east-1.localhost:4566",
# "ApiKeySelectionExpression": "$request.header.x-api-key",
# "DisableExecuteApiEndpoint": false,
# "IpAddressType": "ipv4",
# "RouteSelectionExpression": "$request.method $request.path",
# "Tags": {"team": "payments"}, ...
```

`get-api`, `get-apis` (in creation order; the CLI pages through it, so `--page-size` and `--max-items` work), `update-api`, `delete-api` and `delete-cors-configuration` work, and so do `tag-resource`, `untag-resource` and `get-tags` on an API's or a stage's ARN, `arn:aws:apigateway:us-east-1::/apis/<api-id>[/stages/<stage-name>]`. `--ip-address-type` is stored and changes nothing.

With `--auto-deploy` the stage serves every change to the API's routes and integrations by the time the call that made it returns; `create-stage` answers the deployment it made (`"LastDeploymentStatusMessage": "Successfully deployed stage with deployment ID '8axhjs'"`).

API Gateway may invoke the function only if its resource policy says so, as on AWS. Until then a request answers `500 {"message":"Internal Server Error"}` and the function does not run, whatever `iam.enforce` says:

```sh
aws lambda add-permission --function-name hello --statement-id apigateway --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn 'arn:aws:execute-api:us-east-1:000000000000:wfod7wzey6/*/*'
```

An integration whose `--integration-uri` names a version or alias (`arn:aws:lambda:us-east-1:000000000000:function:hello:live`) invokes that version or alias, and the policy checked is that alias's own, so its permission is added with `--qualifier live`. A permission on the function alone still answers `500`:

```sh
aws lambda add-permission --function-name hello --qualifier live --statement-id apigateway --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn 'arn:aws:execute-api:us-east-1:000000000000:u8vvg5sx8u/*/*'
curl http://localhost:4566/_understudy/execute-api/u8vvg5sx8u/live
# hello from GET /live
```

## Calling the API

An API is reached at its `ApiEndpoint`, `http://<api-id>.execute-api.<region>.localhost:4566`, or, where `*.localhost` does not resolve (inside a container, for one), at `/_understudy/execute-api/<api-id>/<path>` on understudy's endpoint. The `$default` stage serves the paths themselves; a named stage `prod` serves them under `/prod/...`.

```sh
curl -i http://wfod7wzey6.execute-api.us-east-1.localhost:4566/hello
# HTTP/1.1 200 OK
# Apigw-Requestid: uey0EOEuNMRNhTQ=
# Content-Type: text/plain; charset=utf-8
#
# hello from GET /hello
curl -i http://localhost:4566/_understudy/execute-api/wfod7wzey6/hello
# HTTP/1.1 200 OK
# Apigw-Requestid: 2JjoL0Qr2QtVkdk=
# Content-Security-Policy: sandbox
# Content-Type: text/plain; charset=utf-8
#
# hello from GET /hello
```

`http://<api-id>.execute-api.localhost:4566` serves the API too, in its own region; a host naming another region answers `403 {"message":"Forbidden"}`, as an unknown API id does (on AWS such a host does not resolve at all). An API of any account is reached by its id alone.

The function gets the event of its integration's payload format. Under 2.0, `rawPath` is the path the API sees, a named stage's segment included, and `requestContext.domainName` is the host the request named, or on the path fallback the `ApiEndpoint` host name. Every answer on the path fallback carries `Content-Security-Policy: sandbox`, since it shares understudy's origin (ADR 0020); the host is an origin of its own and gets none.

Under `--payload-format-version 1.0` the function gets the 1.0 event and must answer in the 1.0 format (`statusCode`, `headers`, `multiValueHeaders`, `body`, `isBase64Encoded`); any other output, a bare string included, answers `500 {"message":"Internal Server Error"}`. With a handler that returns some of its event's members and `"multiValueHeaders": {"x-many": ["a", "b"]}`:

```sh
curl -i 'http://localhost:4566/_understudy/execute-api/nhkese1z20/pets/rex?q=1&q=2' -H 'X-H: first' -H 'X-H: last'
# HTTP/1.1 200 OK
# Content-Type: text/plain; charset=utf-8
# X-Many: a
# X-Many: b
#
# {"version": "1.0", "resource": "/pets/{id}", "path": "/pets/rex", "queryStringParameters": {"q": "2"},
#  "multiValueQueryStringParameters": {"q": ["1", "2"]}, "X-H": ["last", ["first", "last"]]}
```

The single-value maps keep the last value of a repeated header and of a repeated query parameter, as AWS does (its documented example shows the first query value). Header names keep the client's spelling, as on AWS, and the headers API Gateway adds (`Content-Length`, `X-Forwarded-For`, ...) are in canonical case. `path` includes a named stage's segment, `resource` is the route's path (`$default` for the `$default` route), `requestContext.resourceId` is the route key, and a 1.0 response without a `Content-Type` gets `text/plain; charset=utf-8`, as a 2.0 response with a `statusCode` does.

`update-api --disable-execute-api-endpoint` turns off both forms from that call on: requests answer `404 {"message":"Not Found"}`, as on AWS, until `--no-disable-execute-api-endpoint`:

```sh
aws apigatewayv2 update-api --api-id 0x2wsevugv --disable-execute-api-endpoint
curl -i http://localhost:4566/_understudy/execute-api/0x2wsevugv/prod/hello
# HTTP/1.1 404 Not Found
# Content-Type: application/json
#
# {"message":"Not Found"}
```

## Routes

A route key is `$default` or `<METHOD> <path>`. A `{name}` segment matches one segment, and a `{name+}` segment, last only, matches the rest of the path; the function gets their values decoded in `pathParameters`. With a handler that returns `event["routeKey"] + " " + json.dumps(event["pathParameters"])`:

```sh
aws apigatewayv2 create-route --api-id pfovz1oe8r --route-key 'GET /pets/{id}' --target integrations/5iy2elh
aws apigatewayv2 create-route --api-id pfovz1oe8r --route-key 'ANY /{proxy+}' --target integrations/5iy2elh

curl http://localhost:4566/_understudy/execute-api/pfovz1oe8r/pets/rex%20the%20dog
# GET /pets/{id} {"id": "rex the dog"}
curl -X POST http://localhost:4566/_understudy/execute-api/pfovz1oe8r/orders/1/items
# ANY /{proxy+} {"proxy": "orders/1/items"}
curl http://localhost:4566/_understudy/execute-api/pfovz1oe8r/pets/
# ANY /{proxy+} {"proxy": "pets/"}
curl http://localhost:4566/_understudy/execute-api/pfovz1oe8r/
# {"message":"Not Found"}
```

`create-route` answers the defaults AWS fills, `ApiKeyRequired` false and `AuthorizationType` `NONE`, and `get-route`, `get-routes`, `update-route` and `delete-route` work. Every change serves on an `--auto-deploy` stage by the time the call returns. Members that act only on WebSocket APIs (`--api-key-required`, `--operation-name`, the model and request-parameter members) are stored and returned and change nothing. understudy does not evaluate IAM on requests, so a route with `--authorization-type AWS_IAM` is stored but a request it matches answers 501 and runs nothing; `JWT` and `CUSTOM` name authorizers, which understudy does not emulate, so they are refused:

```sh
aws apigatewayv2 create-route --api-id vsstkl7iok --route-key 'GET /me' --authorization-type JWT --target integrations/7tvrwuh
# An error occurred (NotImplemented) when calling the CreateRoute operation: understudy does not implement
# authorizationType JWT (authorizers are not emulated) on CreateRoute yet; nothing was changed. ...
aws apigatewayv2 create-route --api-id vsstkl7iok --route-key 'GET /admin' --authorization-type AWS_IAM --target integrations/7tvrwuh
curl http://localhost:4566/_understudy/execute-api/vsstkl7iok/admin
# {"message":"understudy does not evaluate IAM authorization of requests: route \"GET /admin\" of API vsstkl7iok has authorizationType AWS_IAM, so no integration ran and nothing was changed. ..."}
```

A request takes the most specific route, in AWS's documented order: a full match, then a match with a `{name+}` variable, then `$default`; on one path, a literal segment before a variable and the request's own method before `ANY`. A full match on `ANY` beats a `{name+}` match on the request's method, as on AWS; between two `{name+}` routes understudy prefers the one with more segments before its variable. A variable never matches an empty segment, so `/pets/` is not `GET /pets/{id}`. As on AWS, literal segments match case-sensitively, `/pets` and `/pets/` are different paths, `//` collapses to `/`, the path is percent-decoded before matching (so `%2F` separates segments), and a route with no target takes no part in matching. A route key with a trailing slash is refused, as AWS refuses it:

```sh
aws apigatewayv2 create-route --api-id 0x2wsevugv --route-key 'GET /slash/'
# An error occurred (BadRequestException) when calling the CreateRoute operation: Part of the given route key path is empty
```

`HEAD` reaches only `HEAD` and `ANY` routes; AWS's answer there is not recorded ([coverage.md](../coverage.md)).

## Integrations

`create-integration` answers the defaults AWS fills, and `get-integration`, `get-integrations`, `update-integration` and `delete-integration` work:

```sh
aws apigatewayv2 create-integration --api-id vsstkl7iok --integration-type AWS_PROXY \
  --integration-uri arn:aws:lambda:us-east-1:000000000000:function:hello --payload-format-version 2.0
# "ConnectionType": "INTERNET",
# "IntegrationMethod": "POST",
# "PayloadFormatVersion": "2.0",
# "TimeoutInMillis": 30000, ...
```

`--integration-uri` names the function by its ARN or by the invoke URI Terraform's `invoke_arn` gives. `--payload-format-version` is required, as on AWS: `1.0` or `2.0` for a Lambda integration and `1.0` for anything else. An AWS service integration (`--integration-subtype`) needs every request parameter AWS lists as required for its subtype. `--request-parameters` and `--response-parameters` come back exactly as sent; on `update-integration` a request parameter sent with an empty value, or a response status sent as `{}`, is removed. `delete-integration` is refused while a route targets the integration, with AWS's text:

```sh
aws apigatewayv2 delete-integration --api-id 0x2wsevugv --integration-id q6si386
# An error occurred (ConflictException) when calling the DeleteIntegration operation:
# Cannot delete Integration because it is referenced by the following Routes with Ids: [wv50gg8]
```

Only a Lambda proxy integration runs. HTTP proxy integrations, AWS service integrations (`--integration-subtype`) and private integrations (`--connection-type VPC_LINK`) are stored, and so is a Lambda integration with `--credentials-arn`, `--request-parameters`, `--response-parameters` or a stage variable in its URI, but a request that reaches one answers 501 and runs nothing:

```sh
curl http://localhost:4566/_understudy/execute-api/vsstkl7iok/legacy/x
# {"message":"understudy does not run HTTP proxy integrations (integrationType HTTP_PROXY): route \"ANY /legacy/{proxy+}\" of API vsstkl7iok targets integration 9r4p1ir, so no integration ran and nothing was changed. ..."}
```

A request waits for the function at most the integration's `--timeout-in-millis` (30000 by default) of real time, whatever the emulator clock does. Past it the client gets `503 {"message":"Service Unavailable"}`, and the function runs on to its end with its answer dropped. A function error answers `500 {"message":"Internal Server Error"}`, as malformed output does. AWS has not been recorded on these statuses; [coverage.md](../coverage.md) lists them. With a handler that sleeps five seconds behind `--timeout-in-millis 2000`:

```sh
curl -i http://localhost:4566/_understudy/execute-api/u8vvg5sx8u/slow
# HTTP/1.1 503 Service Unavailable       (after two seconds)
# Apigw-Requestid: 1nPP1e0vLnV9JB0=
# Content-Type: application/json
#
# {"message":"Service Unavailable"}
```

Without Docker the function cannot run, so a request that reaches it answers 501 with Lambda's reason, never an AWS-shaped error:

```sh
curl http://localhost:4566/_understudy/execute-api/fk1emmw8kw/hello
# {"message":"understudy could not run the Lambda proxy integration of API fk1emmw8kw, route \"GET /hello\": the function did not run
#  and nothing was changed. Lambda answered: Lambda cannot run functions: Docker is disabled by configuration ..."}
```

`--tls-config` is stored and changes nothing.

understudy stores an API's `--cors-configuration` and returns it, but does not apply CORS. A request with an `Origin` header to an API that has one answers 501 and runs nothing, rather than reaching your function without the CORS headers AWS would add:

```sh
curl -i -H 'Origin: https://app.example' http://localhost:4566/_understudy/execute-api/o6tvc1uf6b/hello
# HTTP/1.1 501 Not Implemented
# {"message":"understudy does not apply CORS: API o6tvc1uf6b has a corsConfiguration and the request carries an Origin header, so no integration ran and nothing was changed. ..."}
```

Requests without `Origin` are served as usual; `delete-cors-configuration` removes the configuration. The configuration is stored as AWS stores it: origins and headers lower-cased, methods upper-cased, `update-api` merging what it sends into what is there, and a wildcard origin with credentials refused:

```sh
aws apigatewayv2 update-api --api-id 0x2wsevugv --cors-configuration AllowOrigins='https://A.example',AllowMethods=get,post,AllowHeaders=Content-Type
# "CorsConfiguration": {"AllowHeaders": ["content-type"], "AllowMethods": ["GET", "POST"], "AllowOrigins": ["https://a.example"]}, ...
aws apigatewayv2 update-api --api-id 0x2wsevugv --cors-configuration AllowOrigins='*',AllowCredentials=true
# An error occurred (BadRequestException) when calling the UpdateApi operation: allow-credentials is not supported if 'allow-origin' is *
```

## Stages and deployments

A stage without `--auto-deploy` serves a deployment, a snapshot of the API's routes and integrations taken when it was made. Until the stage has one it answers `404 {"message":"Not Found"}`, and a route created after the deployment is not served there until the next one. With a handler that returns `event["routeKey"] + " " + json.dumps(event.get("stageVariables"))`:

```sh
aws apigatewayv2 create-stage --api-id o6tvc1uf6b --stage-name prod --stage-variables color=blue
# "DefaultRouteSettings": {"DetailedMetricsEnabled": false},
# "RouteSettings": {}, "StageVariables": {"color": "blue"}, "Tags": {}, ...
aws apigatewayv2 create-deployment --api-id o6tvc1uf6b --stage-name prod --description v1
# "AutoDeployed": false, "DeploymentId": "w92293", "DeploymentStatus": "DEPLOYED", ...
curl http://localhost:4566/_understudy/execute-api/o6tvc1uf6b/prod/hello
# GET /hello {"color": "blue"}
aws apigatewayv2 create-route --api-id o6tvc1uf6b --route-key 'GET /later' --target integrations/5oge6gq
curl http://localhost:4566/_understudy/execute-api/o6tvc1uf6b/prod/later
# {"message":"Not Found"}
```

A deployment reads `DEPLOYED` as soon as `create-deployment` returns. `update-stage --deployment-id` points the stage at another deployment, and `--stage-variables` merges into the stage's variables, where a variable sent empty (`color=`) is removed; the function gets them in `stageVariables`. `get-stage`, `get-stages`, `delete-stage`, `get-deployment`, `get-deployments`, `update-deployment` and `delete-deployment` work as well, and `update-stage --auto-deploy` deploys at once.

Three deployments are refused, with AWS's texts: one of an API with no routes, a delete of a deployment a stage serves (point the stage at the new deployment first), and an explicit deployment on a stage with `--auto-deploy`:

```sh
aws apigatewayv2 create-deployment --api-id 0x2wsevugv
# An error occurred (BadRequestException) when calling the CreateDeployment operation:
# Unable to deploy API because no valid routes exist in this API
aws apigatewayv2 delete-deployment --api-id 0x2wsevugv --deployment-id drplti
# An error occurred (BadRequestException) when calling the DeleteDeployment operation:
# Active stages pointing to this deployment must be moved or deleted
aws apigatewayv2 create-stage --api-id 0x2wsevugv --stage-name auto --auto-deploy --deployment-id drplti
# An error occurred (BadRequestException) when calling the CreateStage operation:
# Deployment ID cannot be set on this stage 000000000000:0x2wsevugv because AutoDeploy is enabled.
```

`create-deployment --stage-name` on an `--auto-deploy` stage is refused the same way.

`--access-log-settings` (its format must include `$context.requestId`), `--default-route-settings` and `--route-settings` are stored and returned but change nothing: understudy writes no access log records and throttles nothing. `delete-access-log-settings` and `delete-route-settings` remove them.

## Terraform

`aws_apigatewayv2_api`, `_integration`, `_route`, `_stage` and `_deployment` apply, plan again with no changes, update and destroy with provider 6.68.0 and the provider block in [terraform.md](terraform.md); `conformance/terraform/apigatewayv2.tf` is a complete configuration. Three attributes need care at understudy's endpoint:

- `execution_arn` has no account, `arn:aws:execute-api:us-east-1::<api-id>`, because `skip_credentials_validation` leaves the provider without one, and dropping that setting breaks S3 buckets. Build the permission's source ARN from `data.aws_caller_identity` instead:

  ```hcl
  data "aws_caller_identity" "current" {}

  resource "aws_lambda_permission" "api" {
    action        = "lambda:InvokeFunction"
    function_name = aws_lambda_function.hello.function_name
    principal     = "apigateway.amazonaws.com"
    source_arn    = "arn:aws:execute-api:us-east-1:${data.aws_caller_identity.current.account_id}:${aws_apigatewayv2_api.hello.id}/*/*"
  }
  ```

- `invoke_url` on a stage always names `amazonaws.com`. Call the API at `api_endpoint`, or at `/_understudy/execute-api/<api-id>/...`.
- `body` on `aws_apigatewayv2_api` fails to apply: the provider creates the API, then imports the OpenAPI definition with `ReimportApi`, which understudy does not implement. The API is left tainted, and `destroy` removes it. Declare routes and integrations as resources instead.

  ```text
  Error: reimporting API Gateway v2 API (lbdaxqbjyb) OpenAPI definition: operation error ApiGatewayV2: ReimportApi,
  https response error StatusCode: 501, ..., api error NotImplemented: understudy does not implement apigatewayv2.ReimportApi. ...
  ```

A deployment with `triggers` and `create_before_destroy` is replaced on an update: Terraform creates the new deployment, points the stage at it, then deletes the old one, which understudy refuses while a stage serves it. Hash the attributes that change what the API serves (route keys and targets, integration URIs), not whole resource objects: the provider reads empty maps such as `request_models` back as `{}` on the first refresh after a create, so a hash of the objects changes and the second plan replaces the deployment again.

## What answers NotImplemented for now

HTTP API requests understudy cannot serve as AWS would are refused with `NotImplemented` (501), naming what is missing, and change nothing; REST APIs have their own list ([What answers 501 in a REST API](#what-answers-501-in-a-rest-api)):

- WebSocket APIs (`--protocol-type WEBSOCKET`) and quick create (`create-api` or `update-api` with `--target`, `--route-key` or `--credentials-arn`);
- authorizers: `--authorization-type JWT` or `CUSTOM`, `--authorizer-id` and `--authorization-scopes` on a route;
- the WebSocket integration members `--request-templates`, `--template-selection-expression` and `--content-handling-strategy`;
- the WebSocket stage member `--client-certificate-id`;
- every operation this page does not describe, among them authorizers, `import-api`, `reimport-api`, `export-api`, custom domain names and VPC links.

## REST APIs

REST APIs (`apigateway`) serve resources, methods, `AWS_PROXY` integrations to Lambda functions and `MOCK` integrations, deployed to stages: the set Terraform's `aws_api_gateway_*` resources and most SAM templates use. A Lambda proxy API with a `/pets/{id}` resource and a greedy `/{proxy+}`:

```sh
cat > rest.py <<'EOF'
def handler(event, context):
    return {"statusCode": 200, "body": "hello from " + event["httpMethod"] + " " + event["resource"]}
EOF
python3 -m zipfile -c function.zip rest.py
aws lambda create-function --function-name pets --runtime python3.13 --handler rest.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip

aws apigateway create-rest-api --name pets
# "id": "leb10wzbif", "rootResourceId": "c7egw89dyp", "apiStatus": "AVAILABLE", ...
aws apigateway create-resource --rest-api-id leb10wzbif --parent-id c7egw89dyp --path-part pets
# "id": "cwlqnw", "path": "/pets", ...
aws apigateway create-resource --rest-api-id leb10wzbif --parent-id cwlqnw --path-part '{id}'
# "id": "s2nk6p", "path": "/pets/{id}", ...
aws apigateway create-resource --rest-api-id leb10wzbif --parent-id c7egw89dyp --path-part '{proxy+}'
# "id": "lr4qek", "path": "/{proxy+}", ...
aws apigateway put-method --rest-api-id leb10wzbif --resource-id s2nk6p --http-method GET --authorization-type NONE
aws apigateway put-integration --rest-api-id leb10wzbif --resource-id s2nk6p --http-method GET \
  --type AWS_PROXY --integration-http-method POST \
  --uri arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:pets/invocations
aws apigateway put-method --rest-api-id leb10wzbif --resource-id lr4qek --http-method ANY --authorization-type NONE
aws apigateway put-integration --rest-api-id leb10wzbif --resource-id lr4qek --http-method ANY \
  --type AWS_PROXY --integration-http-method POST \
  --uri arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:pets/invocations
aws apigateway create-deployment --rest-api-id leb10wzbif --stage-name dev
# "id": "joadtf", ...
aws lambda add-permission --function-name pets --statement-id apigateway --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:us-east-1:000000000000:leb10wzbif/*/*"
```

The function gets the REST proxy event (payload format 1.0) and answers in the REST proxy format: `statusCode` and `body`, with `Content-Type: application/json` unless it sets one. As on AWS, API Gateway may invoke the function only if its resource policy says so: before `add-permission` a request answers `500 {"message": "Internal server error"}` and the function does not run, whatever `iam.enforce` says.

### Calling a REST API

A REST API has no `ApiEndpoint`. AWS's invoke URL is `https://<api-id>.execute-api.<region>.amazonaws.com/<stage>/`, and understudy never answers an `amazonaws.com` name, so the local invoke URL is `http://<api-id>.execute-api.<region>.localhost:4566/<stage>/`, or, where `*.localhost` does not resolve (inside a container, for one), `http://localhost:4566/_understudy/execute-api/<api-id>/<stage>/`. The stage is always the first path segment:

```sh
curl -i http://localhost:4566/_understudy/execute-api/leb10wzbif/dev/pets/rex
# HTTP/1.1 200 OK
# Content-Security-Policy: sandbox
# Content-Type: application/json
# X-Amz-Apigw-Id: S7r-0pL8gO4C2OI=
# X-Amzn-Requestid: c598a493-1987-400f-8184-4ddcb3f42d9e
#
# hello from GET /pets/{id}
curl -i -X POST http://leb10wzbif.execute-api.us-east-1.localhost:4566/dev/orders/1/items
# HTTP/1.1 200 OK
# Content-Type: application/json
# ...
# hello from POST /{proxy+}
curl -i http://localhost:4566/_understudy/execute-api/leb10wzbif/dev/
# HTTP/1.1 403 Forbidden
# X-Amzn-Errortype: MissingAuthenticationTokenException
#
# {"message":"Missing Authentication Token"}
curl -i http://localhost:4566/_understudy/execute-api/leb10wzbif/prod/pets/rex
# HTTP/1.1 403 Forbidden
# X-Amzn-Errortype: ForbiddenException
#
# {"message":"Forbidden"}
```

A path no deployed method serves answers `403 Missing Authentication Token`, as on AWS; a stage that does not exist answers `403 Forbidden`. An id neither an HTTP API nor a REST API holds gets the HTTP API answer, `403 {"message":"Forbidden"}`. The path form carries `Content-Security-Policy: sandbox`, as for HTTP APIs.

### API settings

`create-rest-api` and `update-rest-api` accept every member but `--clone-from`, which answers `NotImplemented`. Some settings change what a request does in ways understudy does not emulate, so a request to an API that has one answers 501 naming it and runs nothing: `--disable-execute-api-endpoint` and an endpoint type `PRIVATE` (read live, so clearing them serves again at once), and `--binary-media-types` (read from the stage's deployment, so deploy again after removing them). A resource policy (`--policy`) is stored and returned as AWS returns it, its quotes escaped and `execute-api:/` resources rewritten to the API's ARN, but never evaluated: under `iam.enforce=off` requests are served, under `advise` each one also records an `access-unevaluated` advisory, and under `enforce` each one answers 501 until the policy is removed with `update-rest-api --patch-operations op=replace,path=/policy,value=''`. VPC endpoint ids, `--security-policy`, `--endpoint-access-mode`, `--api-key-source` and `--minimum-compression-size` are stored without effect, and an `EDGE` API is served as a `REGIONAL` one. `get-rest-apis` and `delete-rest-api` work as on AWS.

### Resources and request matching

Resources can be created, read, renamed, moved and deleted. A move or a rename carries the subtree's paths along, and deleting a resource deletes its subtree. The sibling and greedy rules answer AWS's texts:

```sh
aws apigateway create-resource --rest-api-id leb10wzbif --parent-id cwlqnw --path-part '{name}'
# An error occurred (BadRequestException) when calling the CreateResource operation: A sibling ({id}) of this
# resource already has a variable path part -- only one is allowed
aws apigateway update-resource --rest-api-id leb10wzbif --resource-id cwlqnw --patch-operations op=replace,path=/pathPart,value=animals
# "path": "/animals", ...
aws apigateway get-resources --rest-api-id i644mvi1ym --query 'items[].[id,path]' --output text
# 4qo8mhv20b  /
# 8jgd4g      /alpha
# a9u3zj      /animals
# ket3ck      /zeta
aws apigateway get-resources --rest-api-id i644mvi1ym --limit 2 --query position --output text
# aWQ9UzhqZ2Q0Zw%3D%3D
```

As on AWS, the items are sorted by id, so the root falls wherever its id sorts, and a page's `position` is AWS's form, the base64 of `id=S` and the page's last id.

`get-resources --embed methods` returns each method in full; without it `resourceMethods` maps each method to `{}`. A path part is letters, digits and `._-,:`, or `{name}` and `{name+}`; anything else is refused.

A request is matched against the resources of the stage's deployment as AWS does: at the first segment where two matching resources differ, a literal part wins over `{name}`, and `{name}` over `{name+}`. A resource that matches but has no method for the request is passed over for a less specific one that has it, so with `/part1` (GET) and `/{param+}` (GET), `GET /part1/random-value` reaches `/{param+}`. The event's `pathParameters` hold the bound values as sent, still percent-encoded; a greedy value keeps interior empty segments and drops a trailing slash. Nothing matched answers `403 Missing Authentication Token`. As on AWS, a resource's explicit method serves before its `ANY`, and `GET` does not serve `HEAD`; which of `GET` and `ANY` serves `HEAD` when both exist is not recorded, and understudy takes `ANY`.

### Methods

Methods and method responses can be put, read, updated and deleted. `put-method` replaces a method that exists, its responses and integration included. Every API holds the models `Empty` and `Error`, and request and response models can name only those; no authorizer or request validator can exist, so `--authorizer-id`, `--request-validator-id`, and `CUSTOM` or `COGNITO_USER_POOLS` are refused with AWS's texts:

```sh
aws apigateway put-method --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST --authorization-type NONE \
  --operation-name createPet --request-parameters method.request.querystring.page=true --request-models application/json=Empty
aws apigateway put-method --rest-api-id leb10wzbif --resource-id cwlqnw --http-method GET --authorization-type NONE \
  --request-models application/json=Pet
# An error occurred (BadRequestException) when calling the PutMethod operation: Invalid model identifier specified: Pet
aws apigateway put-method-response --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST --status-code 200 \
  --response-models application/json=Empty
aws apigateway update-method-response --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST --status-code 200 \
  --patch-operations op=add,path=/responseParameters/method.response.header.X-Id,value=false
# 201, as on AWS
aws apigateway get-method --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST
# "methodResponses": {"200": {"statusCode": "200", "responseParameters": {"method.response.header.X-Id": false},
#   "responseModels": {"application/json": "Empty"}}}, ...
```

A method with `apiKeyRequired` answers `403 {"message":"Forbidden"}`, since no API key can be created. Request parameters and models are stored and returned; without a request validator they do not act, as on AWS.

### Integrations

Integrations and integration responses can be put, read, updated and deleted, of every type. `put-integration` answers AWS's defaults (`timeoutInMillis` 29000, `passthroughBehavior` `WHEN_NO_MATCH`, `cacheNamespace` the resource id, `connectionType` `INTERNET` for `HTTP` and `HTTP_PROXY` only) and drops `--integration-http-method` on a `MOCK` integration, as AWS does. URIs are checked with AWS's texts:

```sh
aws apigateway put-integration --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST \
  --type AWS --integration-http-method POST --uri arn:aws:lambda:us-east-1:000000000000:function:pets
# An error occurred (BadRequestException) when calling the PutIntegration operation: AWS ARN for integration must contain path or action
aws apigateway put-integration --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST \
  --type MOCK --integration-http-method POST --request-templates '{"application/json":"{\"statusCode\": 200}"}'
# "type": "MOCK", "timeoutInMillis": 29000, "passthroughBehavior": "WHEN_NO_MATCH", "cacheNamespace": "cwlqnw", ... (no httpMethod)
aws apigateway put-integration-response --rest-api-id leb10wzbif --resource-id cwlqnw --http-method POST \
  --status-code 200 --selection-pattern ''
# "statusCode": "200", "selectionPattern": ""
```

An `AWS_PROXY` integration to a Lambda function and a `MOCK` integration run. `HTTP`, `HTTP_PROXY` and `AWS` integrations are stored, and so are `--credentials`, a `${stageVariables.name}` in `--uri`, `--response-transfer-mode STREAM` and `--content-handling`, but a request that reaches one answers `501` naming it and runs nothing ([What answers 501](#what-answers-501-in-a-rest-api)). `--connection-type VPC_LINK`, `--connection-id`, `--tls-config` and `--integration-target` answer `NotImplemented`. An empty-string template is returned as `""`, where AWS returns `null`, for request and response templates alike.

### MOCK integrations

A `MOCK` integration answers from its configuration alone, as the common CORS `OPTIONS` method does. The request template the `Content-Type` selects (`application/json` when the request has none) gives the status, read leniently, so `{statusCode: 200}` works as `{"statusCode": 200}` does; under `passthroughBehavior` the request body takes the template's place, or the request answers `415`, per AWS's passthrough table. The status selects the integration response whose `selectionPattern` matches it, the one without a pattern otherwise; its static `responseParameters` set headers and its response template is the body, empty when it has none:

```sh
aws apigateway put-method --rest-api-id leb10wzbif --resource-id cwlqnw --http-method OPTIONS --authorization-type NONE
aws apigateway put-integration --rest-api-id leb10wzbif --resource-id cwlqnw --http-method OPTIONS --type MOCK \
  --request-templates '{"application/json":"{\"statusCode\": 200}"}'
aws apigateway put-method-response --rest-api-id leb10wzbif --resource-id cwlqnw --http-method OPTIONS --status-code 200 \
  --response-parameters method.response.header.Access-Control-Allow-Origin=false
aws apigateway put-integration-response --rest-api-id leb10wzbif --resource-id cwlqnw --http-method OPTIONS --status-code 200 \
  --response-parameters "{\"method.response.header.Access-Control-Allow-Origin\":\"'*'\"}" \
  --response-templates '{"application/json":"{\"ok\": true}"}'
aws apigateway create-deployment --rest-api-id leb10wzbif --stage-name dev
curl -i -X OPTIONS http://localhost:4566/_understudy/execute-api/leb10wzbif/dev/animals
# HTTP/1.1 200 OK
# Access-Control-Allow-Origin: *
# Content-Security-Policy: sandbox
# Content-Type: application/json
#
# {"ok": true}
```

understudy renders no Velocity: a template is run only when it holds none of `$`, `#` and `\`, so it is the same text before and after rendering. A request that reaches any other template answers `501` naming it, and so do a `selectionPattern` Go's regular expressions cannot compile (a lookaround or a backreference), `contentHandling` on the selected integration response, and a `responseParameters` value that is not a static `'value'`. As on AWS, media types compare by type and subtype, case-insensitively; a pattern must match the whole status code; and of several response templates the one named by the whole `Accept` value answers, else `application/json`, else the first by media type, always sent as `Content-Type: application/json`. A request whose media type no template takes answers AWS's 415:

```sh
aws apigateway put-integration --rest-api-id i644mvi1ym --resource-id a9u3zj --http-method GET --type MOCK \
  --passthrough-behavior NEVER --request-templates '{"application/json":"{\"statusCode\": 200}"}'
aws apigateway put-integration-response --rest-api-id i644mvi1ym --resource-id a9u3zj --http-method GET --status-code 200 \
  --response-templates '{"application/xml":"<r>xml</r>","text/plain":"plain"}'
curl -i http://localhost:4566/_understudy/execute-api/i644mvi1ym/dev/animals -H 'Accept: text/html, text/plain'
# HTTP/1.1 200 OK
# Content-Type: application/json
#
# <r>xml</r>
curl -i -X GET http://localhost:4566/_understudy/execute-api/i644mvi1ym/dev/animals -H 'Content-Type: application/xml' -d x
# HTTP/1.1 415 Unsupported Media Type
# Content-Type: application/json
# X-Amzn-Errortype: BadRequestException
#
# {"message": "Unsupported Media Type"}
```

Of two patterns that both match, AWS's recordings fit both "the longer wins" and "the one that sorts last wins"; understudy takes the one that sorts last.

### Deployments and stages

A deployment is a snapshot of the API's resources, methods and integrations; a stage serves one deployment, and later edits reach callers only after a new deployment. `create-deployment --stage-name` creates the stage or repoints it, and its `--stage-description`, `--variables` and cache and tracing members set that stage:

```sh
aws apigateway create-deployment --rest-api-id leb10wzbif --stage-name test --stage-description first --variables color=blue
# "id": "7w3i9d", ...
aws apigateway get-stage --rest-api-id leb10wzbif --stage-name test
# "deploymentId": "7w3i9d", "stageName": "test", "description": "first", "cacheClusterEnabled": false,
# "cacheClusterStatus": "NOT_AVAILABLE", "methodSettings": {}, "variables": {"color": "blue"}, "tracingEnabled": false, ...
aws apigateway create-stage --rest-api-id leb10wzbif --stage-name prod --deployment-id 7w3i9d --tags owner=ops
aws apigateway update-stage --rest-api-id leb10wzbif --stage-name prod \
  --patch-operations op=replace,path=/variables/color,value=green 'op=replace,path=/*/*/metrics/enabled,value=true'
# "methodSettings": {"*/*": {"metricsEnabled": true, "throttlingBurstLimit": 5000, "throttlingRateLimit": 10000.0,
#   "cacheTtlInSeconds": 300, ...}}, "variables": {"color": "green"}, ...
aws apigateway update-stage --rest-api-id leb10wzbif --stage-name prod --patch-operations op=replace,path=/tags/owner,value=x
# An error occurred (BadRequestException) when calling the UpdateStage operation: Invalid method setting path: /tags/owner.
# Must be one of: [/deploymentId, /description, ...]
aws apigateway delete-deployment --rest-api-id leb10wzbif --deployment-id 7w3i9d
# An error occurred (BadRequestException) when calling the DeleteDeployment operation: Active stages pointing to this
# deployment must be moved or deleted
aws apigateway get-stages --rest-api-id leb10wzbif --deployment-id 7w3i9d
# "test", "prod"
aws apigateway get-tags --resource-arn arn:aws:apigateway:us-east-1::/restapis/leb10wzbif/stages/prod
# "tags": {"owner": "ops"}
```

Stage variables reach the Lambda proxy event. Cache settings, method settings, access log settings, `clientCertificateId` and `tracingEnabled` are stored and returned without effect: no cache runs (`cacheClusterStatus` stays `NOT_AVAILABLE`), and nothing is throttled or logged. `--canary-settings` on `create-deployment` and `create-stage`, and `--documentation-version` on `create-stage`, answer `NotImplemented`. Tags on the API and on its stages are read and written with `tag-resource`, `untag-resource` and `get-tags`.

### Terraform for REST APIs

`aws_api_gateway_rest_api`, `_resource`, `_method`, `_method_response`, `_integration`, `_integration_response`, `_deployment` and `_stage` apply, plan again with no changes, update and destroy with provider 6.68.0 and the provider block in [terraform.md](terraform.md); `conformance/terraform/apigateway.tf` is a complete configuration, with `AWS_PROXY` methods on `/items/{id}` and `/{proxy+}`, a `MOCK` `OPTIONS` method and a deployment replaced on update. As for HTTP APIs:

- `execution_arn` has no account under that provider block, so a permission built from it never matches; build `aws_lambda_permission`'s `source_arn` from `data.aws_caller_identity`, as in [Terraform](#terraform) above.
- `invoke_url` on `aws_api_gateway_stage` is built by the provider, `https://<api-id>.execute-api.<region>.amazonaws.com/<stage>`, and cannot be pointed at understudy. Build the local URL from the API's `id` and the stage name instead, `http://localhost:4566/_understudy/execute-api/<id>/<stage>/...` ([Calling a REST API](#calling-a-rest-api)).
- A deployment with `triggers` and `create_before_destroy` is replaced on an update: Terraform creates the new deployment, points the stage at it, then deletes the old one, which understudy refuses while a stage serves it. Hash the attributes that change what the API serves (resource paths, method authorization, integration types, URIs and templates) rather than whole resource objects, and hash an unset `request_templates` as `{}`.
- `body` (an OpenAPI definition) on `aws_api_gateway_rest_api` is not supported: it imports the definition with `PutRestApi`, which answers `NotImplemented`. Declare resources and methods instead.

### What answers 501 in a REST API

understudy refuses with `501`, naming what is missing, rather than answer where it would not act as AWS does; nothing is changed and no integration runs. These requests to a deployed API answer `501` with a `{"message": ...}` body:

- a method whose `authorizationType` is `AWS_IAM` (and `CUSTOM` or `COGNITO_USER_POOLS`, which `put-method` already refuses, since no authorizer can exist);
- an `HTTP`, `HTTP_PROXY` or `AWS` integration, and an `AWS_PROXY` integration with `--credentials`, a stage variable in its `--uri`, `--response-transfer-mode STREAM` or `--content-handling`;
- a mapping template with any of `$`, `#` and `\` (Velocity), a `selectionPattern` Go cannot compile, `contentHandling` on the selected integration response, and a `responseParameters` value that is not a static `'value'`;
- an API with `disableExecuteApiEndpoint`, an endpoint type `PRIVATE`, or `binaryMediaTypes` in the stage's deployment, and, under `iam.enforce=enforce`, an API with a resource policy.

With an `HTTP_PROXY` method on `/legacy` and a `MOCK` method whose request template reads `$input.params("code")` on `/vtl`:

```sh
curl http://localhost:4566/_understudy/execute-api/7lyjk5gpgg/dev/legacy
# {"message":"understudy does not run HTTP_PROXY integrations yet: method GET /legacy of REST API 7lyjk5gpgg,
#  deployment 1ya5az, reaches it, so no integration ran and nothing was changed. Use an AWS_PROXY integration to a
#  Lambda function or a MOCK integration."}
curl http://localhost:4566/_understudy/execute-api/7lyjk5gpgg/dev/vtl
# {"message":"understudy does not render mapping templates with VTL: the request template for application/json of the
#  MOCK integration of method GET /vtl of REST API 7lyjk5gpgg contains '$', '#' or '\\', so no integration ran and
#  nothing was changed. Use a literal request template, such as {\"statusCode\": 200}."}
```

These calls answer `NotImplemented`:

- `create-rest-api --clone-from`, `put-rest-api` and `import-rest-api` (OpenAPI);
- authorizers, request validators, the model operations (methods may still name the seeded `Empty` and `Error`), API keys, usage plans, gateway responses, documentation, client certificates, VPC links, custom domain names, exports and SDKs, `test-invoke-method`, `get-account` and `update-account`, and every other operation this page does not describe;
- the members named above: canary settings, `--documentation-version` on a stage, and `VPC_LINK`, `--tls-config` and `--integration-target` on an integration.

```sh
aws apigateway create-authorizer --rest-api-id leb10wzbif --name a --type TOKEN
# An error occurred (NotImplemented) when calling the CreateAuthorizer operation: understudy does not implement
# apigateway.CreateAuthorizer. Run 'understudy coverage --service apigateway' for what is supported; ...
```
