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

## Calling the API

An API is reached at `/_understudy/execute-api/<api-id>/<path>` on understudy's endpoint. The `$default` stage serves the paths themselves; a named stage `prod` serves them under `/prod/...`.

```sh
curl -i http://localhost:4566/_understudy/execute-api/wfod7wzey6/hello
# HTTP/1.1 200 OK
# Apigw-Requestid: c4Zzo5MmWjE_qVY=
# Content-Type: text/plain; charset=utf-8
#
# hello from GET /hello
```

The function gets the payload format 2.0 event. Its `rawPath` is the path after the API id, a named stage's segment included. Every answer on this path carries `Content-Security-Policy: sandbox`, since it shares understudy's origin (ADR 0020).

The execute-api host form, `http://<api-id>.execute-api.us-east-1.localhost:4566/hello`, still answers 501.

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

Only a Lambda proxy integration with payload format 2.0 runs. HTTP proxy integrations, AWS service integrations (`--integration-subtype`), private integrations (`--connection-type VPC_LINK`) and Lambda integrations with payload format 1.0 are stored, and so is a Lambda integration with `--credentials-arn`, `--request-parameters`, `--response-parameters` or a stage variable in its URI, but a request that reaches one answers 501 and runs nothing:

```sh
curl http://localhost:4566/_understudy/execute-api/vsstkl7iok/legacy/x
# {"message":"understudy does not run HTTP proxy integrations (integrationType HTTP_PROXY): route \"ANY /legacy/{proxy+}\" of API vsstkl7iok targets integration 9r4p1ir, so no integration ran and nothing was changed. ..."}
```

`--timeout-in-millis` and `--tls-config` are stored and change nothing yet.

`update-api --disable-execute-api-endpoint` turns this path off from that call on: requests answer `403 {"message":"Forbidden"}` until `--no-disable-execute-api-endpoint`.

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

## What answers NotImplemented for now

Requests understudy cannot serve as AWS would are refused with `NotImplemented` (501), naming what is missing, and change nothing:

- WebSocket APIs (`--protocol-type WEBSOCKET`) and quick create (`create-api` or `update-api` with `--target`, `--route-key` or `--credentials-arn`);
- authorizers: `--authorization-type JWT` or `CUSTOM`, `--authorizer-id` and `--authorization-scopes` on a route;
- the WebSocket integration members `--request-templates`, `--template-selection-expression` and `--content-handling-strategy`;
- the WebSocket stage member `--client-certificate-id`;
- every operation this page does not describe, among them authorizers, `import-api` and `export-api`.

The integration timeout is not applied yet: a request waits for the function however long it runs.
