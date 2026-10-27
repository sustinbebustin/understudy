# API Gateway HTTP APIs

understudy serves API Gateway HTTP APIs (`apigatewayv2`): an API, Lambda proxy integrations, routes and stages, and the data plane that invokes the function a route targets. Invoking needs Docker, as Lambda does ([lambda.md](lambda.md)). What each operation supports is in [coverage.md](../coverage.md); this page grows with it.

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

`update-api --disable-execute-api-endpoint` turns this path off from that call on: requests answer `403 {"message":"Forbidden"}` until `--no-disable-execute-api-endpoint`.

understudy stores an API's `--cors-configuration` and returns it, but does not apply CORS. A request with an `Origin` header to an API that has one answers 501 and runs nothing, rather than reaching your function without the CORS headers AWS would add:

```sh
curl -i -H 'Origin: https://app.example' http://localhost:4566/_understudy/execute-api/o6tvc1uf6b/hello
# HTTP/1.1 501 Not Implemented
# {"message":"understudy does not apply CORS: API o6tvc1uf6b has a corsConfiguration and the request carries an Origin header, so no integration ran and nothing was changed. ..."}
```

Requests without `Origin` are served as usual; `delete-cors-configuration` removes the configuration.

## What answers NotImplemented for now

Requests understudy cannot serve as AWS would are refused with `NotImplemented` (501), naming what is missing, and change nothing:

- WebSocket APIs (`--protocol-type WEBSOCKET`) and quick create (`create-api` or `update-api` with `--target`, `--route-key` or `--credentials-arn`);
- route keys with path variables (`GET /pets/{id}`, `ANY /{proxy+}`) and authorizers; `$default` and literal keys work;
- integrations other than Lambda `AWS_PROXY` with payload format 2.0, and any optional integration member;
- stage members other than `--stage-name`, `--auto-deploy` and `--tags`, so a stage without `--auto-deploy` has no deployment yet and answers `404 {"message":"Not Found"}`;
- every other operation, among them the reads, updates and deletes of routes, integrations, stages and deployments (`get-routes`, `get-stage`, ...) and `create-deployment`.

The integration timeout is not applied yet: a request waits for the function however long it runs.
