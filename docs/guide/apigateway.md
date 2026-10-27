# API Gateway HTTP APIs

understudy serves API Gateway HTTP APIs (`apigatewayv2`): an API, its routes, integrations and stages, and the data plane that invokes the Lambda function a route targets. Invoking needs Docker, as Lambda does ([lambda.md](lambda.md)). What each operation supports is in [coverage.md](../coverage.md); this page grows with it.

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

`http://<api-id>.execute-api.localhost:4566` serves the API too, in its own region; a host naming another region answers `403 {"message":"Forbidden"}`, as an unknown API id does. An API of any account is reached by its id alone.

The function gets the event of its integration's payload format. Under 2.0, `rawPath` is the path the API sees, a named stage's segment included, and `requestContext.domainName` is the host the request named, or on the path fallback the `ApiEndpoint` host name. Every answer on the path fallback carries `Content-Security-Policy: sandbox`, since it shares understudy's origin (ADR 0020); the host is an origin of its own and gets none.

Under `--payload-format-version 1.0` the function gets the 1.0 event and must answer in the 1.0 format (`statusCode`, `headers`, `multiValueHeaders`, `body`, `isBase64Encoded`); any other output, a bare string included, answers `500 {"message":"Internal Server Error"}`. With a handler that returns some of its event's members and `"multiValueHeaders": {"x-many": ["a", "b"]}`:

```sh
curl -i 'http://localhost:4566/_understudy/execute-api/nhkese1z20/pets/rex?q=1&q=2' -H 'X-H: first' -H 'X-H: last'
# HTTP/1.1 200 OK
# Content-Type: text/plain; charset=utf-8
# X-Many: a
# X-Many: b
#
# {"version": "1.0", "resource": "/pets/{id}", "path": "/pets/rex", "queryStringParameters": {"q": "1"},
#  "multiValueQueryStringParameters": {"q": ["1", "2"]}, "x-h": ["last", ["first", "last"]]}
```

The single-value maps keep a repeated header's last value and a repeated query parameter's first, as AWS's documented example does. `path` includes a named stage's segment, `resource` is the route's path (`$default` for the `$default` route), and a 1.0 response without a `Content-Type` gets `text/plain; charset=utf-8`, as a 2.0 response with a `statusCode` does. AWS has not been recorded on these choices; [coverage.md](../coverage.md) lists them.

`update-api --disable-execute-api-endpoint` turns off both forms from that call on: requests answer `403 {"message":"Forbidden"}` until `--no-disable-execute-api-endpoint`.

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

A request takes the most specific route, in AWS's documented order: a full match, then a match with a `{name+}` variable, then `$default`; on one path, a literal segment before a variable and the request's own method before `ANY`. Where AWS documents no order, understudy prefers a full match on `ANY` to a `{name+}` match on the request's method, and the `{name+}` route with more segments before its variable. A variable never matches an empty segment, so `/pets/` is not `GET /pets/{id}`. Literal segments match case-sensitively, `/pets` and `/pets/` are different paths, and `HEAD` reaches only `HEAD` and `ANY` routes. AWS has not been recorded on any of these choices; [coverage.md](../coverage.md) lists them.

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

`--integration-uri` names the function by its ARN or by the invoke URI Terraform's `invoke_arn` gives. `--payload-format-version` is required, as on AWS: `1.0` or `2.0` for a Lambda integration and `1.0` for anything else. An AWS service integration (`--integration-subtype`) needs every request parameter AWS lists as required for its subtype. `--request-parameters` and `--response-parameters` come back exactly as sent; on `update-integration` a request parameter sent with an empty value, or a response status sent as `{}`, is removed. `delete-integration` is refused while a route targets the integration:

```sh
aws apigatewayv2 delete-integration --api-id vsstkl7iok --integration-id 7tvrwuh
# An error occurred (BadRequestException) when calling the DeleteIntegration operation:
# integration 7tvrwuh of API vsstkl7iok is the target of routes wa0s05t, uzmc2mg; delete them or change their targets first
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

Requests without `Origin` are served as usual; `delete-cors-configuration` removes the configuration.

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

AWS documents no answer for three cases, and understudy picks one for each. A deployment of an API with no routes succeeds, and its stage answers `404`. Deleting a deployment a stage serves is refused, as REST APIs refuse it, so point the stage at the new deployment first. A stage with `--auto-deploy` takes no explicit deployment, since AWS documents its deployment as not updatable:

```sh
aws apigatewayv2 delete-deployment --api-id o6tvc1uf6b --deployment-id d6gheb
# An error occurred (BadRequestException) when calling the DeleteDeployment operation:
# Active stages pointing to this deployment must be moved or deleted (stages prod serve deployment d6gheb)
aws apigatewayv2 create-stage --api-id o6tvc1uf6b --stage-name auto --auto-deploy --deployment-id d6gheb
# An error occurred (BadRequestException) when calling the CreateStage operation:
# stage "auto" has autoDeploy enabled, so its deploymentId can't be set; turn autoDeploy off to serve deployment d6gheb
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

Requests understudy cannot serve as AWS would are refused with `NotImplemented` (501), naming what is missing, and change nothing:

- WebSocket APIs (`--protocol-type WEBSOCKET`) and quick create (`create-api` or `update-api` with `--target`, `--route-key` or `--credentials-arn`);
- authorizers: `--authorization-type JWT` or `CUSTOM`, `--authorizer-id` and `--authorization-scopes` on a route;
- the WebSocket integration members `--request-templates`, `--template-selection-expression` and `--content-handling-strategy`;
- the WebSocket stage member `--client-certificate-id`;
- every operation this page does not describe, among them authorizers, `import-api`, `reimport-api`, `export-api`, custom domain names and VPC links.
